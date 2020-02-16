package cluster

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/util"
	"io/ioutil"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	dockerapi "github.com/docker/docker/client"
	"github.com/hashicorp/go-sockaddr"
	"github.com/ncabatoff/yurt/docker"
)

func TestYurtRunCluster_Start(t *testing.T) {
	tmpDir, err := ioutil.TempDir(".", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	absDir, err := filepath.Abs(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cli, err := dockerapi.NewClientWithOpts(dockerapi.FromEnv, dockerapi.WithVersion("1.40"))
	if err != nil {
		t.Fatal(err)
	}

	cidr := fmt.Sprintf("10.%d.%d.0/24", rand.Int31n(255), rand.Int31n(255))
	_, err = docker.SetupNetwork(ctx, cli, t.Name(), cidr)
	if err != nil {
		t.Fatal(err)
	}
	netsa, err := sockaddr.NewSockAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	netipv4 := sockaddr.ToIPv4Addr(netsa).NetIP().To4()
	var sas []string
	for _, oct := range []byte{51, 52, 53} {
		netipv4[3] = oct
		sa, _ := sockaddr.NewSockAddr(netipv4.String())
		sas = append(sas, sa.String())
	}

	y, err := NewYurtRunCluster(YurtRunClusterOptions{
		Network: util.NetworkConfig{
			DockerNetName: t.Name(),
			Network:       netsa,
		},
		ConsulServerIPs: sas,
		BaseImage:       "noenv/nomad:0.10.3",
		YurtRunBin:      "../../yurt-run",
		WorkDir:         absDir,
	}, cli)
	if err != nil {
		t.Fatal(err)
	}
	err = y.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{}, 3)
	for _, cont := range y.containers {
		go func(id, name string) {
			err := docker.Wait(cli, id)
			if err != nil {
				t.Logf("container %q exited with err %v", name, err)
			}
			done <- struct{}{}
		}(cont.ID, cont.Name)
	}
	defer func() {
		if err := y.Stop(context.Background()); err != nil {
			t.Logf("stop err: %v", err)
		}
		<-done
		<-done
		<-done
		y.Cleanup(!t.Failed())
	}()

	{
		clients, err := y.ConsulAPIs()
		if err != nil {
			t.Fatal(err)
		}

		var leaderAPIs []runner.LeaderAPI
		for _, c := range clients {
			leaderAPIs = append(leaderAPIs, c.Status())
		}

		var addrs []string
		for _, ip := range y.ConsulServerIPs {
			addrs = append(addrs, fmt.Sprintf("%s:%d", ip, runner.DefConsulPorts().Server))
		}

		if err := runner.LeaderAPIsHealthy(ctx, leaderAPIs, addrs); err != nil {
			t.Fatal(err)
		}
	}

	{
		clients, err := y.NomadAPIs()
		if err != nil {
			t.Fatal(err)
		}

		var leaderAPIs []runner.LeaderAPI
		for _, c := range clients {
			leaderAPIs = append(leaderAPIs, c.Status())
		}

		var addrs []string
		for _, ip := range y.ConsulServerIPs {
			addrs = append(addrs, fmt.Sprintf("%s:%d", ip, runner.DefNomadPorts().RPC))
		}

		if err := runner.LeaderAPIsHealthy(ctx, leaderAPIs, addrs); err != nil {
			t.Fatal(err)
		}
	}

}

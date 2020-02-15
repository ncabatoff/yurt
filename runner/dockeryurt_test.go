package runner

import (
	"context"
	"fmt"
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

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
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
		Network: NetworkConfig{
			DockerNetName: t.Name(),
			Network:       netsa,
		},
		ConsulServerIPs: sas,
		BaseImage:       imageNomad,
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
	defer func() {
		<-done
		<-done
		<-done
		y.Cleanup(!t.Failed())
	}()
	for _, cont := range y.containers {
		go func(id, name string) {
			err := docker.DockerWait(cli, id)
			if err != nil {
				t.Logf("container %q exited with err %v", name, err)
			}
			done <- struct{}{}
		}(cont.ID, cont.Name)
	}

	{
		clients, err := y.ConsulAPIs()
		if err != nil {
			t.Fatal(err)
		}

		var leaderAPIs []LeaderAPI
		for _, c := range clients {
			leaderAPIs = append(leaderAPIs, c.Status())
		}

		var addrs []string
		for _, ip := range y.ConsulServerIPs {
			addrs = append(addrs, fmt.Sprintf("%s:%d", ip, DefConsulPorts().Server))
		}

		if err := LeaderAPIsHealthy(ctx, leaderAPIs, addrs); err != nil {
			t.Fatal(err)
		}
	}

	{
		clients, err := y.NomadAPIs()
		if err != nil {
			t.Fatal(err)
		}

		var leaderAPIs []LeaderAPI
		for _, c := range clients {
			leaderAPIs = append(leaderAPIs, c.Status())
		}

		var addrs []string
		for _, ip := range y.ConsulServerIPs {
			addrs = append(addrs, fmt.Sprintf("%s:%d", ip, DefNomadPorts().RPC))
		}

		if err := LeaderAPIsHealthy(ctx, leaderAPIs, addrs); err != nil {
			t.Fatal(err)
		}
	}

	if err := y.Stop(context.Background()); err != nil {
		t.Logf("stop err: %v", err)
	}
}

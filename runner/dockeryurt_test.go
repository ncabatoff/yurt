package runner

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"path/filepath"
	"testing"

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

	ctx := context.Background()
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
			addrs = append(addrs, fmt.Sprintf("%s:%d", ip, DefConsulPorts(false).Server))
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
}

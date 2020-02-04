package runner

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"path/filepath"
	"testing"
	"time"

	dockerapi "github.com/docker/docker/client"
	"golang.org/x/sync/errgroup"
)

const (
	imageConsul = "consul:1.6.2"
	// There's no official nomad docker image
	imageNomad = "noenv/nomad:0.10.2"
)

type testenv struct {
	docker  *dockerapi.Client
	netName string
	netCidr string
	tmpDir  string
	ctx     context.Context
}

func init() {
	rand.Seed(int64(time.Now().Nanosecond()))
}

func testSetupDocker(t *testing.T, timeout time.Duration) (testenv, func()) {
	// TODO clean up containers on network if it exists
	t.Helper()
	cli, err := dockerapi.NewClientWithOpts(dockerapi.FromEnv, dockerapi.WithVersion("1.40"))
	if err != nil {
		t.Fatal(err)
	}

	tmpDir, ctx, cleanup := testSetup(t, timeout)
	cidr := randomNetwork()
	_, err = setupNetwork(ctx, cli, t.Name(), cidr)
	if err != nil {
		t.Fatal(err)
	}
	return testenv{
			docker:  cli,
			netName: t.Name(),
			netCidr: cidr,
			tmpDir:  tmpDir,
			ctx:     ctx,
		}, func() {
			//_ = cli.NetworkRemove(ctx, netID)
			cleanup()
		}
}

func ipnet(t *testing.T, cidr string) (net.IP, net.IPNet) {
	t.Helper()
	i, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return i, *n
}

// return a random 10. /24
func randomNetwork() string {
	return fmt.Sprintf("10.%d.%d.0/24", rand.Int31n(255), rand.Int31n(255))
}

func TestConsulDocker(t *testing.T) {
	testenv, cancel := testSetupDocker(t, 10*time.Second)
	defer cancel()
	g, ctx := errgroup.WithContext(testenv.ctx)

	_, network := ipnet(t, testenv.netCidr)
	runner, err := NewConsulDockerRunner(testenv.docker, imageConsul, "", ConsulServerConfig{
		ConsulConfig{
			NetworkConfig: NetworkConfig{
				DockerNetName: testenv.netName,
				Network:       network,
			},
			NodeName:  "consul-test",
			DataDir:   filepath.Join(testenv.tmpDir, "consul-data"),
			JoinAddrs: []string{"127.0.0.1:8301"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ip, err := runner.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g.Go(runner.Wait)

	go func() {
		if err := g.Wait(); err != nil {
			t.Log(err)
		}
	}()

	expectedPeers := []string{fmt.Sprintf("%s:%d", ip, 8300)}
	if err := consulRunnersHealthy(ctx, []ConsulRunner{runner}, expectedPeers); err != nil {
		t.Fatal(err)
	}
}

func (te testenv) NetworkConfig() NetworkConfig {
	_, n, _ := net.ParseCIDR(te.netCidr)
	return NetworkConfig{Network: *n, DockerNetName: te.netName}
}

func threeNodeConsulDocker(t *testing.T, te testenv) (*ConsulClusterRunner, *ConsulDockerRunner) {
	t.Helper()
	nodeNames := []string{"consul-srv-1", "consul-srv-2", "consul-srv-3"}
	var ips []string
	netip, _ := ipnet(t, te.netCidr)
	serverIP := netip.To4()
	for i := range nodeNames {
		serverIP[3] = byte(i) + 51
		ips = append(ips, serverIP.String())
	}
	cluster, err := NewConsulClusterRunner(
		ConsulClusterConfigFixedIPs{
			NetworkConfig:   te.NetworkConfig(),
			WorkDir:         te.tmpDir,
			ServerNames:     nodeNames,
			ConsulServerIPs: ips,
		},
		&ConsulDockerServerBuilder{
			DockerAPI: te.docker,
			Image:     imageConsul,
			IPs:       ips,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := cluster.StartServers(te.ctx); err != nil {
		t.Fatal(err)
	}

	var runners []ConsulRunner
	for _, runner := range cluster.servers {
		runners = append(runners, runner)
	}

	clientRunner, _ := NewConsulDockerRunner(te.docker, imageConsul, "", ConsulConfig{
		NetworkConfig: te.NetworkConfig(),
		NodeName:      fmt.Sprintf("consul-cli-%d", 1),
		DataDir:       filepath.Join(te.tmpDir, fmt.Sprintf("consul-data-cli-%d", 1)),
		JoinAddrs:     cluster.Config.JoinAddrs(),
	})
	if _, err := clientRunner.Start(te.ctx); err != nil {
		t.Fatal(err)
	}
	cluster.group.Go(clientRunner.Wait)
	runners = append(runners, clientRunner)

	go func() {
		if err := cluster.group.Wait(); err != nil {
			t.Log(err)
		}
	}()
	var expectedPeers []string
	for _, ip := range ips {
		expectedPeers = append(expectedPeers, ip+":8300")
	}
	if err := consulRunnersHealthy(te.ctx, runners, expectedPeers); err != nil {
		t.Fatal(err)
	}
	return cluster, clientRunner
}

func TestConsulDockerCluster(t *testing.T) {
	testenv, cancel := testSetupDocker(t, 10*time.Second)
	defer cancel()

	threeNodeConsulDocker(t, testenv)
}

func TestNomadDocker(t *testing.T) {
	testenv, cancel := testSetupDocker(t, 15*time.Second)
	defer cancel()
	g, ctx := errgroup.WithContext(testenv.ctx)

	_, consulClientRunner := threeNodeConsulDocker(t, testenv)
	consulAddr, err := consulClientRunner.AgentAddress()
	if err != nil {
		t.Fatal(err)
	}

	nomadRunner, _ := NewNomadDockerRunner(testenv.docker, imageNomad, "", NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: NomadConfig{
			NetworkConfig: testenv.NetworkConfig(),
			NodeName:      "nomad-test",
			DataDir:       filepath.Join(testenv.tmpDir, "nomad-data"),
			ConfigDir:     filepath.Join(testenv.tmpDir, "nomad-cfg"),
			ConsulAddr:    consulAddr,
		},
	})

	ip, err := nomadRunner.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g.Go(nomadRunner.Wait)

	go func() {
		if err := g.Wait(); err != nil {
			t.Log(err)
		}
	}()

	expectedPeers := []string{fmt.Sprintf("%s:%d", ip, 4648)}
	if err := nomadRunnersHealthy(ctx, []NomadRunner{nomadRunner}, expectedPeers); err != nil {
		t.Fatal(err)
	}
}

func TestNomadDockerCluster(t *testing.T) {
	testenv, cancel := testSetupDocker(t, 15*time.Second)
	defer cancel()

	consulCluster, _ := threeNodeConsulDocker(t, testenv)

	nomadCluster, err := NewNomadClusterRunner(
		NomadClusterConfigFixedIPs{
			NetworkConfig: testenv.NetworkConfig(),
			WorkDir:       testenv.tmpDir,
			ServerNames:   []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3"},
			ConsulAddrs:   consulCluster.Config.APIAddrs(),
		},
		&NomadDockerServerBuilder{
			DockerAPI: testenv.docker,
			Image:     imageNomad,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := nomadCluster.StartServers(testenv.ctx); err != nil {
		t.Fatal(err)
	}

	if err := nomadRunnersHealthy(testenv.ctx, nomadCluster.servers, nomadCluster.NomadPeerAddrs); err != nil {
		t.Fatal(err)
	}
}

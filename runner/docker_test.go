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
	"github.com/hashicorp/go-sockaddr"
	"github.com/ncabatoff/yurt/docker"
	"github.com/ncabatoff/yurt/pki"
)

const (
	imageConsul = "consul:1.7.0-beta4"
	// There's no official nomad docker image
	imageNomad = "noenv/nomad:0.10.3"
)

type dktestenv struct {
	docker  *dockerapi.Client
	netConf NetworkConfig
	tmpDir  string
	ctx     context.Context
}

func init() {
	rand.Seed(int64(time.Now().Nanosecond()))
}

func testSetupDocker(t *testing.T, timeout time.Duration) (dktestenv, func()) {
	// TODO clean up containers on network if it exists
	t.Helper()
	cli, err := dockerapi.NewClientWithOpts(dockerapi.FromEnv, dockerapi.WithVersion("1.40"))
	if err != nil {
		t.Fatal(err)
	}

	tmpDir, ctx, cleanup := testSetup(t, timeout)
	cidr := fmt.Sprintf("10.%d.%d.0/24", rand.Int31n(255), rand.Int31n(255))
	_, err = docker.SetupNetwork(ctx, cli, t.Name(), cidr)
	if err != nil {
		t.Fatal(err)
	}

	sa, err := sockaddr.NewSockAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}

	return dktestenv{
			docker: cli,
			netConf: NetworkConfig{
				DockerNetName: t.Name(),
				Network:       sa,
			},
			tmpDir: tmpDir,
			ctx:    ctx,
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

func testConsulDockerTLS(t *testing.T, te dktestenv, ca *pki.CertificateAuthority, cfg ConsulServerConfig) {
	tls, err := ca.ConsulServerTLS(te.ctx, "127.0.0.1", "10m")
	if err != nil {
		t.Fatal(err)
	}
	cfg.TLS = *tls
	testConsulDocker(t, te, cfg)
}

func testConsulDocker(t *testing.T, te dktestenv, cfg ConsulServerConfig) {
	cfg.ConfigDir = filepath.Join(te.tmpDir, "consul/config")
	cfg.DataDir = filepath.Join(te.tmpDir, "consul/data")
	cfg.LogConfig.LogDir = filepath.Join(te.tmpDir, "consul/log")
	runner, _ := NewConsulDockerRunner(te.docker, imageConsul, "", cfg)

	ip, err := runner.Start(te.ctx)
	if err != nil {
		t.Fatal(err)
	}

	expectedPeerAddrs := []string{fmt.Sprintf("%s:%d", ip, cfg.Ports.Server)}
	if err := ConsulRunnersHealthy(te.ctx, []ConsulRunner{runner}, expectedPeerAddrs); err != nil {
		t.Fatal(err)
	}
}

// TestConsulDocker tests a single node docker Consul cluster.
func TestConsulDocker(t *testing.T) {
	t.Parallel()
	te, cleanup := testSetupDocker(t, 15*time.Second)
	defer cleanup()

	testConsulDocker(t, te, ConsulServerConfig{ConsulConfig: ConsulConfig{
		NetworkConfig: te.netConf,
		NodeName:      "consul-srv1",
		JoinAddrs:     []string{"127.0.0.1:8301"},
		Ports:         DefConsulPorts(),
	}})
}

// TestConsulDockerTLS tests a single node docker Consul cluster with TLS.
func TestConsulDockerTLS(t *testing.T) {
	t.Parallel()
	te, cleanup := testSetupDocker(t, 15*time.Second)
	defer cleanup()

	ca := tempca(t, te.ctx, te.tmpDir)
	testConsulDockerTLS(t, te, ca, ConsulServerConfig{ConsulConfig: ConsulConfig{
		NetworkConfig: te.netConf,
		NodeName:      "consul-srv1",
		JoinAddrs:     []string{"127.0.0.1:8301"},
		Ports:         DefConsulPorts(),
	}})
}

func threeNodeConsulDocker(t *testing.T, te dktestenv) (*ConsulClusterRunner, error) {
	names := []string{"consul-srv-1", "consul-srv-2", "consul-srv-3", "consul-cli-1"}
	var ips []string
	netip, _ := ipnet(t, te.netConf.Network.String())
	serverIP := netip.To4()
	for i := range names {
		serverIP[3] = byte(i) + 51
		ips = append(ips, serverIP.String())
	}
	return BuildConsulCluster(te.ctx,
		ConsulClusterConfigFixedIPs{
			NetworkConfig:   te.netConf,
			WorkDir:         te.tmpDir,
			ServerNames:     names,
			ConsulServerIPs: ips[:3],
		},
		&ConsulDockerServerBuilder{
			DockerAPI: te.docker,
			Image:     imageConsul,
			IPs:       ips,
		},
	)
}

func threeNodeConsulDockerTLS(t *testing.T, te dktestenv, ca *pki.CertificateAuthority) (*ConsulClusterRunner, error) {
	names := []string{"consul-srv-1", "consul-srv-2", "consul-srv-3", "consul-cli-1"}
	certs := make(map[string]pki.TLSConfigPEM)
	var ips []string
	netip, _ := ipnet(t, te.netConf.Network.String())
	serverIP := netip.To4()
	for i := range names {
		serverIP[3] = byte(i) + 51
		ips = append(ips, serverIP.String())

		tls, err := ca.ConsulServerTLS(te.ctx, serverIP.String(), "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[names[i]] = *tls
	}
	return BuildConsulCluster(te.ctx,
		ConsulClusterConfigFixedIPs{
			NetworkConfig:   te.netConf,
			WorkDir:         te.tmpDir,
			ServerNames:     names,
			ConsulServerIPs: ips[:3],
			TLS:             certs,
		},
		&ConsulDockerServerBuilder{
			DockerAPI: te.docker,
			Image:     imageConsul,
			IPs:       ips,
		},
	)
}

// TestConsulDockerCluster tests a three node docker Consul cluster.
func TestConsulDockerCluster(t *testing.T) {
	t.Parallel()
	te, cleanup := testSetupDocker(t, 15*time.Second)
	defer cleanup()

	if _, err := threeNodeConsulDocker(t, te); err != nil {
		t.Fatal(err)
	}
}

// TestConsulDockerClusterTLS tests a three node docker Consul cluster with TLS.
func TestConsulDockerClusterTLS(t *testing.T) {
	t.Parallel()
	te, cleanup := testSetupDocker(t, 15*time.Second)
	defer cleanup()

	ca := tempca(t, te.ctx, te.tmpDir)
	if _, err := threeNodeConsulDockerTLS(t, te, ca); err != nil {
		t.Fatal(err)
	}
}

func TestNomadDocker(t *testing.T) {
	t.Parallel()
	te, cleanup := testSetupDocker(t, 30*time.Second)
	defer cleanup()

	consulCluster, err := threeNodeConsulDocker(t, te)
	if err != nil {
		t.Fatal(err)
	}
	client, err := consulCluster.Client()
	if err != nil {
		t.Fatal(err)
	}

	testNomadDocker(t, te, consulCluster, NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: NomadConfig{
			NetworkConfig: te.netConf,
			NodeName:      "nomad-test",
			DataDir:       filepath.Join(te.tmpDir, "nomad-data"),
			ConfigDir:     filepath.Join(te.tmpDir, "nomad-cfg"),
			LogConfig: LogConfig{
				LogDir: filepath.Join(te.tmpDir, "nomad", "log"),
			},
			ConsulAddr: fmt.Sprintf("localhost:%d", client.Config().Ports.HTTP),
		},
	})
}

func TestNomadDockerTLS(t *testing.T) {
	t.Parallel()
	te, cleanup := testSetupDocker(t, 30*time.Second)
	defer cleanup()

	ca := tempca(t, te.ctx, te.tmpDir)
	consulCluster, err := threeNodeConsulDockerTLS(t, te, ca)
	if err != nil {
		t.Fatal(err)
	}
	client, err := consulCluster.Client()
	if err != nil {
		t.Fatal(err)
	}

	cert, err := ca.NomadServerTLS(te.ctx, "127.0.0.1", "10m")
	if err != nil {
		t.Fatal(err)
	}
	testNomadDocker(t, te, consulCluster, NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: NomadConfig{
			NetworkConfig: te.netConf,
			NodeName:      "nomad-test",
			DataDir:       filepath.Join(te.tmpDir, "nomad-data"),
			ConfigDir:     filepath.Join(te.tmpDir, "nomad-cfg"),
			LogConfig: LogConfig{
				LogDir: filepath.Join(te.tmpDir, "nomad", "log"),
			},
			ConsulAddr: fmt.Sprintf("localhost:%d", client.Config().Ports.HTTP),
			TLS:        *cert,
		},
	})
}

func testNomadDocker(t *testing.T, te dktestenv, consulCluster *ConsulClusterRunner, cfg NomadServerConfig) {
	nomadRunner, err := NewNomadDockerRunner(te.docker, imageNomad, "", cfg)
	if err != nil {
		t.Fatal(err)
	}

	ip, err := nomadRunner.Start(te.ctx)
	if err != nil {
		t.Fatal(err)
	}

	expectedPeers := []string{fmt.Sprintf("%s:%d", ip, 4648)}
	if err := NomadRunnersHealthy(te.ctx, []NomadRunner{nomadRunner}, expectedPeers); err != nil {
		t.Fatal(err)
	}
}

// TestConsulDockerClusterTLS tests a three node docker Consul and a three node
// docker Nomad cluster with TLS.
func TestNomadDockerCluster(t *testing.T) {
	t.Parallel()
	te, cleanup := testSetupDocker(t, 30*time.Second)
	defer cleanup()

	consulCluster, _ := threeNodeConsulDocker(t, te)
	ip := consulCluster.clients[0].(*ConsulDockerRunner).IP

	_, err := BuildNomadCluster(te.ctx, NomadClusterConfigFixedIPs{
		NetworkConfig: te.netConf,
		WorkDir:       te.tmpDir,
		ServerNames:   []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3"},
		ConsulAddrs:   append(consulCluster.Config.APIAddrs(), fmt.Sprintf("%s:%d", ip, DefConsulPorts().HTTP)),
	}, &NomadDockerBuilder{
		DockerAPI: te.docker,
		Image:     imageNomad,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNomadDockerClusterTLS(t *testing.T) {
	t.Parallel()
	te, cleanup := testSetupDocker(t, 30*time.Second)
	defer cleanup()

	ca := tempca(t, te.ctx, te.tmpDir)
	consulCluster, _ := threeNodeConsulDockerTLS(t, te, ca)
	if _, err := threeNodeNomadDockerTLS(t, te, ca, consulCluster); err != nil {
		t.Fatal(err)
	}
}

func threeNodeNomadDockerTLS(t *testing.T, te dktestenv, ca *pki.CertificateAuthority, consulCluster *ConsulClusterRunner) (*NomadClusterRunner, error) {
	names := []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3", "nomad-cli-1"}
	certs := make(map[string]pki.TLSConfigPEM)
	var ips []string
	netip, _ := ipnet(t, te.netConf.Network.String())
	serverIP := netip.To4()
	for i := range names {
		serverIP[3] = byte(i) + 61
		ips = append(ips, serverIP.String())

		tls, err := ca.NomadServerTLS(te.ctx, serverIP.String(), "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[names[i]] = *tls
	}

	ip := consulCluster.clients[0].(*ConsulDockerRunner).IP
	return BuildNomadCluster(te.ctx,
		NomadClusterConfigFixedIPs{
			NetworkConfig:  te.netConf,
			WorkDir:        te.tmpDir,
			ServerNames:    names[:3],
			NomadServerIPs: ips,
			ConsulAddrs:    append(consulCluster.Config.APIAddrs(), fmt.Sprintf("%s:%d", ip, DefConsulPorts().HTTP)),
			TLS:            certs,
		},
		&NomadDockerServerBuilder{
			DockerAPI: te.docker,
			Image:     imageNomad,
			IPs:       ips,
		},
	)
}

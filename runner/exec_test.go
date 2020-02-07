package runner

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt/pki"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

type testenv struct {
	tmpDir                string
	ctx                   context.Context
	cleanup               func()
	group                 *errgroup.Group
	consulPath, nomadPath string
}

func newtestenv(t *testing.T, timeout time.Duration) testenv {
	t.Helper()
	consulPath, nomadPath := getConsulNomadBinaries(t)
	tmpDir, err := ioutil.TempDir(".", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	absDir, err := filepath.Abs(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	g, ctx := errgroup.WithContext(ctx)
	return testenv{
		tmpDir:     absDir,
		ctx:        ctx,
		group:      g,
		consulPath: consulPath,
		nomadPath:  nomadPath,
		cleanup: func() {
			cancel()
			go func() {
				// TODO must this be a goroutine? Would like to be able to use a
				// goroutine leak detector without it being noisy
				if err := g.Wait(); err != nil {
					t.Log(err)
				}
			}()
			_ = os.RemoveAll(tmpDir)
		},
	}
}

func tempca(t *testing.T, ctx context.Context, tmpdir string) *pki.CertificateAuthority {
	t.Helper()
	ca, err := pki.NewCertificateAuthority(tmpdir)
	if err != nil {
		t.Fatal(err)
	}
	err = ca.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func TestConsulExec(t *testing.T) {
	te := newtestenv(t, 15*time.Second)
	defer te.cleanup()

	testConsulExec(t, te, ConsulServerConfig{
		ConsulConfig{
			NodeName:  "consul-test",
			JoinAddrs: []string{"127.0.0.1:8301"},
		},
	})
}

func TestConsulExecTLS(t *testing.T) {
	te := newtestenv(t, 15*time.Second)
	defer te.cleanup()

	ca := tempca(t, te.ctx, te.tmpDir)
	testConsulExecTLS(t, te, ca, ConsulConfig{
		NodeName:  "consul-test",
		JoinAddrs: []string{"127.0.0.1:8301"},
	})
}

func testConsulExecTLS(t *testing.T, te testenv, ca *pki.CertificateAuthority, cfg ConsulConfig) {
	tls, err := ca.ConsulServerTLS(te.ctx, "127.0.0.1", "10m")
	if err != nil {
		t.Fatal(err)
	}
	cfg.TLS = *tls
	cfg.Ports.HTTP, cfg.Ports.HTTPS = -1, 8501
	testConsulExec(t, te, ConsulServerConfig{ConsulConfig: cfg})
}

func testConsulExec(t *testing.T, te testenv, cfg ConsulServerConfig) {
	cfg.ConfigDir = filepath.Join(te.tmpDir, "consul/config")
	cfg.DataDir = filepath.Join(te.tmpDir, "consul/data")
	runner, _ := NewConsulExecRunner(te.consulPath, cfg)

	ip, err := runner.Start(te.ctx)
	if err != nil {
		t.Fatal(err)
	}
	te.group.Go(runner.Wait)

	expectedPeerAddrs := []string{fmt.Sprintf("%s:%d", ip, 8300)}
	if err := consulRunnersHealthy(te.ctx, []ConsulRunner{runner}, expectedPeerAddrs); err != nil {
		t.Fatal(err)
	}
}

func threeNodeConsulExecNoTLS(t *testing.T, te testenv) (*ConsulClusterRunner, *ConsulExecRunner) {
	clientName := "consul-cli-1"
	firstPort := int(rand.Int31n(20000) + 2000)
	return threeNodeConsulExec(t, te,
		ConsulClusterConfigSingleIP{
			WorkDir:     te.tmpDir,
			ServerNames: []string{"consul-srv-1", "consul-srv-2", "consul-srv-3"},
			FirstPorts: ConsulPorts{
				HTTP:    firstPort,
				HTTPS:   -1,
				DNS:     firstPort + 1,
				SerfLAN: firstPort + 2,
				SerfWAN: firstPort + 3,
				Server:  firstPort + 4,
			},
			PortIncrement: 5,
		}, ConsulConfig{
			NodeName:  clientName,
			ConfigDir: filepath.Join(te.tmpDir, clientName, "consul", "config"),
			DataDir:   filepath.Join(te.tmpDir, clientName, "consul", "data"),
		})
}

func threeNodeConsulExecTLS(t *testing.T, te testenv, ca *pki.CertificateAuthority) (*ConsulClusterRunner, *ConsulExecRunner) {
	certs := make([]pki.TLSConfigPEM, 4)
	for i := 0; i < 4; i++ {
		tls, err := ca.ConsulServerTLS(te.ctx, "127.0.0.1", "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[i] = *tls
	}

	clientName := "consul-cli-1"
	firstPort := int(rand.Int31n(20000) + 2000)
	return threeNodeConsulExec(t, te,
		ConsulClusterConfigSingleIP{
			WorkDir:     te.tmpDir,
			ServerNames: []string{"consul-srv-1", "consul-srv-2", "consul-srv-3"},
			FirstPorts: ConsulPorts{
				HTTP:    -1,
				HTTPS:   firstPort,
				DNS:     firstPort + 1,
				SerfLAN: firstPort + 2,
				SerfWAN: firstPort + 3,
				Server:  firstPort + 4,
			},
			PortIncrement: 5,
			TLS:           certs[:3],
		}, ConsulConfig{
			NodeName:  clientName,
			ConfigDir: filepath.Join(te.tmpDir, clientName, "consul", "config"),
			DataDir:   filepath.Join(te.tmpDir, clientName, "consul", "data"),
			Ports: ConsulPorts{
				HTTP:  -1,
				HTTPS: 8501,
			},
			TLS: certs[3],
		})
}

func threeNodeConsulExec(t *testing.T, te testenv, serverCfg ConsulClusterConfig, clientCfg ConsulConfig) (*ConsulClusterRunner, *ConsulExecRunner) {
	consulCluster, err := NewConsulClusterRunner(serverCfg, &ConsulExecBuilder{te.consulPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := consulCluster.StartServers(te.ctx); err != nil {
		t.Fatal(err)
	}

	clientCfg.JoinAddrs = consulCluster.Config.JoinAddrs()
	clientRunner, _ := NewConsulExecRunner(te.consulPath, clientCfg)
	if _, err := clientRunner.Start(te.ctx); err != nil {
		t.Fatal(err)
	}
	te.group.Go(clientRunner.Wait)

	go func() {
		if err := consulCluster.group.Wait(); err != nil {
			t.Log(err)
		}
	}()

	runners := []ConsulRunner{clientRunner}
	for _, runner := range consulCluster.servers {
		runners = append(runners, runner)
	}
	if err := consulRunnersHealthy(te.ctx, runners, consulCluster.Config.ServerAddrs()); err != nil {
		t.Fatal(err)
	}

	return consulCluster, clientRunner
}

func TestConsulExecCluster(t *testing.T) {
	te := newtestenv(t, 15*time.Second)
	defer te.cleanup()
	threeNodeConsulExecNoTLS(t, te)
}

func TestConsulExecClusterTLS(t *testing.T) {
	te := newtestenv(t, 15*time.Second)
	defer te.cleanup()
	ca := tempca(t, te.ctx, te.tmpDir)
	threeNodeConsulExecTLS(t, te, ca)
}

// TestNomadExec tests a single node Nomad cluster talking to a single node Consul cluster.
func TestNomadExec(t *testing.T) {
	te := newtestenv(t, 15*time.Second)
	defer te.cleanup()

	testConsulExec(t, te, ConsulServerConfig{
		ConsulConfig{
			NodeName:  "consul-test",
			JoinAddrs: []string{"127.0.0.1:8301"},
		}})

	testNomadExec(t, te, NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: NomadConfig{
			NodeName:   "nomad-test",
			ConsulAddr: "127.0.0.1:8500",
		}})
}

func TestNomadExecTLS(t *testing.T) {
	te := newtestenv(t, 15*time.Second)
	defer te.cleanup()

	ca := tempca(t, te.ctx, te.tmpDir)
	testConsulExecTLS(t, te, ca, ConsulConfig{
		NodeName:  "consul-test",
		JoinAddrs: []string{"127.0.0.1:8301"},
	})

	testNomadExecTLS(t, te, ca, NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: NomadConfig{
			NodeName:   "nomad-test",
			ConsulAddr: "127.0.0.1:8500",
		}})
}

func testNomadExecTLS(t *testing.T, te testenv, ca *pki.CertificateAuthority, cfg NomadServerConfig) {
	tls, err := ca.ConsulServerTLS(te.ctx, "127.0.0.1", "10m")
	if err != nil {
		t.Fatal(err)
	}
	cfg.NomadConfig.TLS = *tls
	testNomadExec(t, te, cfg)
}

func testNomadExec(t *testing.T, te testenv, cfg NomadServerConfig) {
	cfg.ConfigDir = filepath.Join(te.tmpDir, "nomad/config")
	cfg.DataDir = filepath.Join(te.tmpDir, "nomad/data")
	runner, _ := NewNomadExecRunner(te.nomadPath, cfg)

	ip, err := runner.Start(te.ctx)
	if err != nil {
		t.Fatal(err)
	}
	te.group.Go(runner.Wait)

	expectedNomadPeers := []string{fmt.Sprintf("%s:%d", ip, 4648)}
	if err := nomadRunnersHealthy(te.ctx, []NomadRunner{runner}, expectedNomadPeers); err != nil {
		t.Fatal(err)
	}
}

func TestNomadExecCluster(t *testing.T) {
	te := newtestenv(t, 15*time.Second)
	defer te.cleanup()

	consulCluster, _ := threeNodeConsulExecNoTLS(t, te)

	firstPort := int(rand.Int31n(20000) + 2000)
	testNomadExecCluster(t, te, NomadClusterConfigSingleIP{
		WorkDir:     filepath.Join(te.tmpDir, "nomad"),
		ServerNames: []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3"},
		FirstPorts: NomadPorts{
			HTTP: firstPort,
			Serf: firstPort + 1,
			RPC:  firstPort + 2,
		},
		PortIncrement: 3,
		ConsulAddrs:   consulCluster.Config.APIAddrs(),
	},
	)
}

func TestNomadExecClusterTLS(t *testing.T) {
	te := newtestenv(t, 15*time.Second)
	defer te.cleanup()

	ca := tempca(t, te.ctx, te.tmpDir)
	consulCluster, _ := threeNodeConsulExecTLS(t, te, ca)

	certs := make([]pki.TLSConfigPEM, 3)
	for i := 0; i < 3; i++ {
		tls, err := ca.NomadServerTLS(te.ctx, "127.0.0.1", "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[i] = *tls
	}

	firstPort := int(rand.Int31n(20000) + 2000)
	testNomadExecCluster(t, te, NomadClusterConfigSingleIP{
		WorkDir:     filepath.Join(te.tmpDir, "nomad"),
		ServerNames: []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3"},
		FirstPorts: NomadPorts{
			HTTP: firstPort,
			Serf: firstPort + 1,
			RPC:  firstPort + 2,
		},
		PortIncrement: 3,
		ConsulAddrs:   consulCluster.Config.APIAddrs(),
		TLS:           certs,
	})
}

func testNomadExecCluster(t *testing.T, te testenv, serverCfg NomadClusterConfigSingleIP) {
	nomadCluster, err := NewNomadClusterRunner(serverCfg, &NomadExecBuilder{te.nomadPath})
	if err != nil {
		t.Fatal(err)
	}
	if err := nomadCluster.StartServers(te.ctx); err != nil {
		t.Fatal(err)
	}

	var expectedNomadPeers []string
	for _, runner := range nomadCluster.servers {
		expectedNomadPeers = append(expectedNomadPeers, fmt.Sprintf("127.0.0.1:%d", runner.Config().Ports.RPC))
	}
	if err := nomadRunnersHealthy(te.ctx, nomadCluster.servers, expectedNomadPeers); err != nil {
		t.Fatal(err)
	}
}

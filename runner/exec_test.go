package runner

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncabatoff/yurt/pki"
	"go.uber.org/atomic"
	"golang.org/x/sync/errgroup"
)

var nextPort = atomic.NewUint32(20000)

func nextConsulPorts(tls bool) ConsulPorts {
	start := nextPort.Add(5) - 5
	return SeqConsulPorts(int(start), tls)
}

func nextConsulBatch(nodes int, tls bool) ConsulPorts {
	numPorts := 5 * nodes
	start := int(nextPort.Add(uint32(numPorts))) - numPorts
	return SeqConsulPorts(int(start), tls)
}

func nextNomadPorts() NomadPorts {
	start := nextPort.Add(3) - 3
	return SeqNomadPorts(int(start))
}

func nextNomadBatch(nodes int) NomadPorts {
	numPorts := 3 * nodes
	start := int(nextPort.Add(uint32(numPorts))) - numPorts
	return SeqNomadPorts(int(start))
}

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
					t.Logf("exit with error: %v", err)
				}
			}()
			_ = os.RemoveAll(tmpDir)
		},
	}
}

func tempca(t *testing.T, ctx context.Context, tmpdir string) *pki.CertificateAuthority {
	t.Helper()
	ca, err := pki.NewCertificateAuthority(tmpdir, int(nextPort.Add(1)))
	if err != nil {
		t.Fatal(err)
	}
	err = ca.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// TestConsulExec tests a single node exec Consul cluster.
func TestConsulExec(t *testing.T) {
	t.Parallel()
	te := newtestenv(t, 30*time.Second)
	defer te.cleanup()

	ports := nextConsulPorts(false)
	testConsulExec(t, te, ConsulServerConfig{
		ConsulConfig{
			NodeName:  "consul-test",
			JoinAddrs: []string{fmt.Sprintf("127.0.0.1:%d", ports.SerfLAN)},
			Ports:     ports,
		},
	})
}

// TestConsulExec tests a single node exec Consul cluster with TLS.
func TestConsulExecTLS(t *testing.T) {
	t.Parallel()
	te := newtestenv(t, 30*time.Second)
	defer te.cleanup()

	ports := nextConsulPorts(true)
	ca := tempca(t, te.ctx, te.tmpDir)
	testConsulExecTLS(t, te, ca, ConsulConfig{
		NodeName:  "consul-test",
		JoinAddrs: []string{fmt.Sprintf("127.0.0.1:%d", ports.SerfLAN)},
		Ports:     ports,
	})
}

func testConsulExecTLS(t *testing.T, te testenv, ca *pki.CertificateAuthority, cfg ConsulConfig) {
	tls, err := ca.ConsulServerTLS(te.ctx, "127.0.0.1", "10m")
	if err != nil {
		t.Fatal(err)
	}
	cfg.TLS = *tls
	testConsulExec(t, te, ConsulServerConfig{ConsulConfig: cfg})
}

func testConsulExec(t *testing.T, te testenv, cfg ConsulServerConfig) {
	cfg.ConfigDir = filepath.Join(te.tmpDir, "consul/config")
	cfg.DataDir = filepath.Join(te.tmpDir, "consul/data")
	cfg.LogConfig.LogDir = filepath.Join(te.tmpDir, "consul/log")
	runner, _ := NewConsulExecRunner(te.consulPath, cfg)
	expectedPeerAddrs := []string{fmt.Sprintf("127.0.0.1:%d", cfg.Ports.Server)}
	if err := ConsulRunnersHealthyNow([]ConsulRunner{runner}, expectedPeerAddrs); err == nil {
		t.Fatal("API healthy before process starts - is there an orphan from a previous test running?")
	}

	_, err := runner.Start(te.ctx)
	if err != nil {
		t.Fatal(err)
	}
	te.group.Go(runner.Wait)

	if err := ConsulRunnersHealthy(te.ctx, []ConsulRunner{runner}, expectedPeerAddrs); err != nil {
		t.Fatal(err)
	}
}

func threeNodeConsulExecNoTLS(t *testing.T, te testenv) (*ConsulClusterRunner, error) {
	return BuildConsulCluster(te.ctx,
		ConsulClusterConfigSingleIP{
			WorkDir:     te.tmpDir,
			ServerNames: []string{"consul-srv-1", "consul-srv-2", "consul-srv-3"},
			FirstPorts:  nextConsulBatch(4, false),
		}, &ConsulExecBuilder{te.consulPath})
}

func threeNodeConsulExecTLS(t *testing.T, te testenv, ca *pki.CertificateAuthority) (*ConsulClusterRunner, error) {
	names := []string{"consul-srv-1", "consul-srv-2", "consul-srv-3", "consul-cli-1"}
	certs := make(map[string]pki.TLSConfigPEM)
	for i := 0; i < 4; i++ {
		tls, err := ca.ConsulServerTLS(te.ctx, "127.0.0.1", "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[names[i]] = *tls
	}

	return BuildConsulCluster(te.ctx,
		ConsulClusterConfigSingleIP{
			WorkDir:     te.tmpDir,
			ServerNames: names[:3],
			FirstPorts:  nextConsulBatch(4, true),
			TLS:         certs,
		}, &ConsulExecBuilder{te.consulPath})
}

// TestConsulExecCluster tests running a 3-node exec Consul cluster without TLS.
func TestConsulExecCluster(t *testing.T) {
	t.Parallel()
	te := newtestenv(t, 30*time.Second)
	defer te.cleanup()
	if _, err := threeNodeConsulExecNoTLS(t, te); err != nil {
		t.Fatal(err)
	}
}

// TestConsulExecCluster tests running a 3-node exec Consul cluster with TLS.
func TestConsulExecClusterTLS(t *testing.T) {
	t.Parallel()
	te := newtestenv(t, 30*time.Second)
	defer te.cleanup()
	ca := tempca(t, te.ctx, te.tmpDir)
	if _, err := threeNodeConsulExecTLS(t, te, ca); err != nil {
		t.Fatal(err)
	}
}

// TestNomadExec tests a single node exec Nomad cluster talking to a single node
// exec Consul cluster, without TLS.
func TestNomadExec(t *testing.T) {
	t.Parallel()
	te := newtestenv(t, 30*time.Second)
	defer te.cleanup()

	consulPorts := nextConsulPorts(false)
	testConsulExec(t, te, ConsulServerConfig{
		ConsulConfig{
			NodeName:  "consul-test",
			JoinAddrs: []string{fmt.Sprintf("127.0.0.1:%d", consulPorts.SerfLAN)},
			Ports:     consulPorts,
		}})

	testNomadExec(t, te, NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: NomadConfig{
			NodeName:   "nomad-test",
			ConsulAddr: fmt.Sprintf("127.0.0.1:%d", consulPorts.HTTP),
			Ports:      nextNomadPorts(),
		}})
}

// TestNomadExecTLS tests a single node Nomad cluster talking to a single node
// Consul cluster, with TLS.
func TestNomadExecTLS(t *testing.T) {
	t.Parallel()
	te := newtestenv(t, 30*time.Second)
	defer te.cleanup()

	consulPorts := nextConsulPorts(true)
	ca := tempca(t, te.ctx, te.tmpDir)
	testConsulExecTLS(t, te, ca, ConsulConfig{
		NodeName:  "consul-test",
		JoinAddrs: []string{fmt.Sprintf("127.0.0.1:%d", consulPorts.SerfLAN)},
		Ports:     consulPorts,
	})

	testNomadExecTLS(t, te, ca, NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: NomadConfig{
			NodeName:   "nomad-test",
			ConsulAddr: fmt.Sprintf("127.0.0.1:%d", consulPorts.HTTPS),
			Ports:      nextNomadPorts(),
		}})
}

func testNomadExecTLS(t *testing.T, te testenv, ca *pki.CertificateAuthority, cfg NomadServerConfig) {
	tls, err := ca.NomadServerTLS(te.ctx, "127.0.0.1", "10m")
	if err != nil {
		t.Fatal(err)
	}
	cfg.NomadConfig.TLS = *tls
	testNomadExec(t, te, cfg)
}

func testNomadExec(t *testing.T, te testenv, cfg NomadServerConfig) {
	cfg.ConfigDir = filepath.Join(te.tmpDir, "nomad/config")
	cfg.DataDir = filepath.Join(te.tmpDir, "nomad/data")
	cfg.LogConfig = LogConfig{LogDir: filepath.Join(te.tmpDir, "nomad/log")}
	runner, _ := NewNomadExecRunner(te.nomadPath, cfg)

	ip, err := runner.Start(te.ctx)
	if err != nil {
		t.Fatal(err)
	}
	te.group.Go(runner.Wait)

	expectedNomadPeers := []string{fmt.Sprintf("%s:%d", ip, cfg.Ports.RPC)}
	if err := NomadRunnersHealthy(te.ctx, []NomadRunner{runner}, expectedNomadPeers); err != nil {
		t.Fatal(err)
	}
}

// TestNomadExecCluster tests a three node exec Nomad cluster talking to a
// three node exec Consul cluster.
func TestNomadExecCluster(t *testing.T) {
	t.Parallel()
	te := newtestenv(t, 30*time.Second)
	defer te.cleanup()

	consulCluster, _ := threeNodeConsulExecNoTLS(t, te)
	clientPorts := consulCluster.clients[0].Config().Ports

	_, err := BuildNomadCluster(te.ctx, NomadClusterConfigSingleIP{
		WorkDir:     filepath.Join(te.tmpDir, "nomad"),
		ServerNames: []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3"},
		FirstPorts:  nextNomadBatch(4),
		ConsulAddrs: append(consulCluster.Config.APIAddrs(), fmt.Sprintf("localhost:%d", clientPorts.HTTP)),
	}, &NomadExecBuilder{te.nomadPath})
	if err != nil {
		t.Fatal(err)
	}
}

// TestNomadExecClusterTLS tests a three node exec Nomad cluster talking to a
// three node exec Consul cluster, with TLS.
func TestNomadExecClusterTLS(t *testing.T) {
	t.Parallel()
	names := []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3", "nomad-cli-1"}
	te := newtestenv(t, 30*time.Second)
	defer te.cleanup()

	ca := tempca(t, te.ctx, te.tmpDir)
	consulCluster, err := threeNodeConsulExecTLS(t, te, ca)
	if err != nil {
		t.Fatal(err)
	}
	clientPorts := consulCluster.clients[0].Config().Ports

	certs := make(map[string]pki.TLSConfigPEM)
	for i := 0; i < 4; i++ {
		tls, err := ca.NomadServerTLS(te.ctx, "127.0.0.1", "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[names[i]] = *tls
	}

	_, err = BuildNomadCluster(te.ctx, NomadClusterConfigSingleIP{
		WorkDir:     filepath.Join(te.tmpDir, "nomad"),
		ServerNames: names[:3],
		FirstPorts:  nextNomadBatch(4),
		ConsulAddrs: append(consulCluster.Config.APIAddrs(), fmt.Sprintf("localhost:%d", clientPorts.HTTPS)),
		TLS:         certs,
	}, &NomadExecBuilder{te.nomadPath})
	if err != nil {
		t.Fatal(err)
	}
}

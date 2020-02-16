package exec

import (
	"fmt"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/testutil"
	"path/filepath"
	"testing"
	"time"
)

// TestConsulExec tests a single node exec Consul cluster.
func TestConsulExec(t *testing.T) {
	t.Parallel()
	te := testutil.NewExecTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	testConsulExec(t, te, SingleConsulServerConfig())
}

// TestConsulExec tests a single node exec Consul cluster with TLS.
func TestConsulExecTLS(t *testing.T) {
	t.Parallel()
	te := testutil.NewExecTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	ca, err := pki.NewCertificateAuthority(Vault.Cli)
	if err != nil {
		t.Fatal(err)
	}
	testConsulExecTLS(t, te, ca, SingleConsulServerConfig())
}

func SingleConsulServerConfig() runner.ConsulServerConfig {
	ports := runner.SeqConsulPorts(testutil.NextPortRange(5))
	return runner.ConsulServerConfig{ConsulConfig: runner.ConsulConfig{
		NodeName:  "consul-srv1",
		JoinAddrs: []string{fmt.Sprintf("127.0.0.1:%d", ports.SerfLAN)},
		Ports:     ports,
	}}
}

func testConsulExecTLS(t *testing.T, te testutil.ExecTestEnv, ca *pki.CertificateAuthority, cfg runner.ConsulServerConfig) *ConsulExecRunner {
	tls, err := ca.ConsulServerTLS(te.Ctx, "127.0.0.1", "10m")
	if err != nil {
		t.Fatal(err)
	}
	cfg.TLS = *tls
	return testConsulExec(t, te, cfg)
}

func testConsulExec(t *testing.T, te testutil.ExecTestEnv, cfg runner.ConsulServerConfig) *ConsulExecRunner {
	cfg.ConfigDir = filepath.Join(te.TmpDir, "consul/config")
	cfg.DataDir = filepath.Join(te.TmpDir, "consul/data")
	cfg.LogConfig.LogDir = filepath.Join(te.TmpDir, "consul/log")
	r, _ := NewConsulExecRunner(te.ConsulPath, cfg)
	expectedPeerAddrs := []string{fmt.Sprintf("127.0.0.1:%d", cfg.Ports.Server)}
	if err := runner.ConsulRunnersHealthyNow([]runner.ConsulRunner{r}, expectedPeerAddrs); err == nil {
		t.Fatal("API healthy before process starts - is there an orphan from a previous test running?")
	}

	_, err := r.Start(te.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	te.Group.Go(r.Wait)

	if err := runner.ConsulRunnersHealthy(te.Ctx, []runner.ConsulRunner{r}, expectedPeerAddrs); err != nil {
		t.Fatal(err)
	}
	return r
}

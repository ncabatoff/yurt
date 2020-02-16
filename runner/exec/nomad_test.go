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

// TestNomadExec tests a single node exec Nomad cluster talking to a single node
// exec Consul cluster, without TLS.
func TestNomadExec(t *testing.T) {
	t.Parallel()
	te := testutil.NewExecTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	consulRunner := testConsulExec(t, te, SingleConsulServerConfig())
	testNomadExec(t, te, SingleNomadServerConfig(consulRunner.Config().Ports.HTTP))
}

// TestNomadExecTLS tests a single node Nomad cluster talking to a single node
// Consul cluster, with TLS.
func TestNomadExecTLS(t *testing.T) {
	t.Parallel()
	te := testutil.NewExecTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	ca, err := pki.NewCertificateAuthority(Vault.Cli)
	if err != nil {
		t.Fatal(err)
	}
	consulRunner := testConsulExec(t, te, SingleConsulServerConfig())
	testNomadExecTLS(t, te, ca, SingleNomadServerConfig(consulRunner.Config().Ports.HTTP))
}

func SingleNomadServerConfig(consulPort int) runner.NomadServerConfig {
	return runner.NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: runner.NomadConfig{
			NodeName:   "nomad-test",
			ConsulAddr: fmt.Sprintf("127.0.0.1:%d", consulPort),
			Ports:      runner.SeqNomadPorts(testutil.NextPortRange(3)),
		}}
}

func testNomadExecTLS(t *testing.T, te testutil.ExecTestEnv, ca *pki.CertificateAuthority, cfg runner.NomadServerConfig) {
	tls, err := ca.NomadServerTLS(te.Ctx, "127.0.0.1", "10m")
	if err != nil {
		t.Fatal(err)
	}
	cfg.NomadConfig.TLS = *tls
	testNomadExec(t, te, cfg)
}

func testNomadExec(t *testing.T, te testutil.ExecTestEnv, cfg runner.NomadServerConfig) {
	cfg.ConfigDir = filepath.Join(te.TmpDir, "config")
	cfg.DataDir = filepath.Join(te.TmpDir, "data")
	cfg.LogConfig = runner.LogConfig{LogDir: filepath.Join(te.TmpDir, "log")}
	r, _ := NewNomadExecRunner(te.NomadPath, cfg)

	ip, err := r.Start(te.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	te.Group.Go(r.Wait)

	expectedNomadPeers := []string{fmt.Sprintf("%s:%d", ip, cfg.Ports.RPC)}
	if err := runner.NomadRunnersHealthy(te.Ctx, []runner.NomadRunner{r}, expectedNomadPeers); err != nil {
		t.Fatal(err)
	}
}

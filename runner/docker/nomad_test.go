package docker

import (
	"fmt"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/testutil"
	"github.com/ncabatoff/yurt/util"
	"path/filepath"
	"testing"
	"time"
)

const imageNomad = "noenv/nomad:0.10.3"

func TestNomadDocker(t *testing.T) {
	t.Parallel()
	te := testutil.NewDockerTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	ip := te.NextIP()
	consulRunner := testConsulDocker(t, te, ip, SingleConsulServerConfig(te.NetConf))
	addr, err := consulRunner.APIConfig()
	if err != nil {
		t.Fatal(err)
	}

	testNomadDocker(t, te, "", SingleNomadServerConfig(te.NetConf, addr.Address.Host))
}

func TestNomadDockerTLS(t *testing.T) {
	t.Parallel()
	te := testutil.NewDockerTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	ca, err := pki.NewCertificateAuthority(Vault.Cli)
	if err != nil {
		t.Fatal(err)
	}

	consulRunner := testConsulDockerTLS(t, te, SingleConsulServerConfig(te.NetConf), ca)
	addr, err := consulRunner.APIConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg := SingleNomadServerConfig(te.NetConf, addr.Address.Host)

	ip := te.NextIP()
	cert, err := ca.NomadServerTLS(te.Ctx, ip, "10m")
	if err != nil {
		t.Fatal(err)
	}
	cfg.TLS = *cert
	testNomadDocker(t, te, ip, cfg)
}

func SingleNomadServerConfig(netConf util.NetworkConfig, consulAddr string) runner.NomadServerConfig {
	return runner.NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: runner.NomadConfig{
			NetworkConfig: netConf,
			NodeName:      "nomad-test",
			ConsulAddr:    consulAddr,
			Ports:         runner.DefNomadPorts(),
		},
	}
}

func testNomadDocker(t *testing.T, te testutil.DockerTestEnv, ip string, cfg runner.NomadServerConfig) {
	cfg.ConfigDir = filepath.Join(te.TmpDir, "nomad/config")
	cfg.DataDir = filepath.Join(te.TmpDir, "nomad/data")
	cfg.LogConfig.LogDir = filepath.Join(te.TmpDir, "nomad/log")

	r, err := NewDockerRunner(te.Docker, imageNomad, ip, cfg)
	if err != nil {
		t.Fatal(err)
	}

	ip, err = r.Start(te.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	te.Group.Go(r.Wait)

	expectedPeers := []string{fmt.Sprintf("%s:%d", ip, cfg.Ports.RPC)}
	if err := runner.NomadRunnersHealthy(te.Ctx, []runner.NomadRunner{r}, expectedPeers); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
}

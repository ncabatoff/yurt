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

const imageConsul = "consul:1.7.0"

// TestConsulDocker tests a single node docker Consul cluster.
func TestConsulDocker(t *testing.T) {
	t.Parallel()
	te := testutil.NewDockerTestEnv(t, 15*time.Second)
	defer te.Cleanup()

	ip := te.NextIP()
	testConsulDocker(t, te, ip, SingleConsulServerConfig(te.NetConf))
}

// TestConsulDockerTLS tests a single node docker Consul cluster with TLS.
func TestConsulDockerTLS(t *testing.T) {
	t.Parallel()
	te := testutil.NewDockerTestEnv(t, 15*time.Second)
	defer te.Cleanup()

	ca, err := pki.NewCertificateAuthority(Vault.Cli)
	if err != nil {
		t.Fatal(err)
	}
	testConsulDockerTLS(t, te, SingleConsulServerConfig(te.NetConf), ca)
}

func SingleConsulServerConfig(netConf util.NetworkConfig) runner.ConsulServerConfig {
	return runner.ConsulServerConfig{ConsulConfig: runner.ConsulConfig{
		NetworkConfig: netConf,
		NodeName:      "consul-srv1",
		Ports:         runner.DefConsulPorts(),
	}}
}

func testConsulDockerTLS(t *testing.T, te testutil.DockerTestEnv, cfg runner.ConsulServerConfig, ca *pki.CertificateAuthority) *DockerRunner {
	ip := te.NextIP()
	tls, err := ca.ConsulServerTLS(te.Ctx, ip, "10m")
	if err != nil {
		t.Fatal(err)
	}
	cfg.TLS = *tls
	return testConsulDocker(t, te, ip, cfg)
}

func testConsulDocker(t *testing.T, te testutil.DockerTestEnv, ip string, cfg runner.ConsulServerConfig) *DockerRunner {
	cfg.ConfigDir = filepath.Join(te.TmpDir, "consul/config")
	cfg.DataDir = filepath.Join(te.TmpDir, "consul/data")
	cfg.LogConfig.LogDir = filepath.Join(te.TmpDir, "consul/log")
	cfg.JoinAddrs = []string{fmt.Sprintf("%s:%d", ip, cfg.Ports.SerfLAN)}
	r, err := NewDockerRunner(te.Docker, imageConsul, ip, cfg)
	if err != nil {
		t.Fatal(err)
	}

	_, err = r.Start(te.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	te.Group.Go(r.Wait)

	expectedPeerAddrs := []string{fmt.Sprintf("%s:%d", ip, cfg.Ports.Server)}
	if err := runner.ConsulRunnersHealthy(te.Ctx, []runner.ConsulRunner{r}, expectedPeerAddrs); err != nil {
		t.Fatal(err)
	}
	return r
}

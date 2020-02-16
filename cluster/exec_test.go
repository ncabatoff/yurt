package cluster

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/runner/exec"
	"github.com/ncabatoff/yurt/testutil"
)

func nextConsulBatch(nodes int) runner.ConsulPorts {
	return runner.SeqConsulPorts(testutil.NextPortRange(5 * nodes))
}

func nextNomadBatch(nodes int) runner.NomadPorts {
	return runner.SeqNomadPorts(testutil.NextPortRange(3 * nodes))
}

func threeNodeConsulExecNoTLS(t *testing.T, te testutil.ExecTestEnv) (*ConsulClusterRunner, error) {
	return BuildConsulCluster(te.Ctx,
		ConsulClusterConfigSingleIP{
			WorkDir:     te.TmpDir,
			ServerNames: []string{"consul-srv-1", "consul-srv-2", "consul-srv-3"},
			FirstPorts:  nextConsulBatch(4),
		}, &exec.ConsulExecBuilder{BinPath: te.ConsulPath})
}

func threeNodeConsulExecTLS(t *testing.T, te testutil.ExecTestEnv, ca *pki.CertificateAuthority) (*ConsulClusterRunner, error) {
	names := []string{"consul-srv-1", "consul-srv-2", "consul-srv-3", "consul-cli-1"}
	certs := make(map[string]pki.TLSConfigPEM)
	for i := 0; i < 4; i++ {
		tls, err := ca.ConsulServerTLS(te.Ctx, "127.0.0.1", "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[names[i]] = *tls
	}

	return BuildConsulCluster(te.Ctx,
		ConsulClusterConfigSingleIP{
			WorkDir:     te.TmpDir,
			ServerNames: names[:3],
			FirstPorts:  nextConsulBatch(4),
			TLS:         certs,
		}, &exec.ConsulExecBuilder{BinPath: te.ConsulPath})
}

// TestConsulExecCluster tests running a 3-node exec Consul cluster without TLS.
func TestConsulExecCluster(t *testing.T) {
	t.Parallel()
	te := testutil.NewExecTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	if _, err := threeNodeConsulExecNoTLS(t, te); err != nil {
		t.Fatal(err)
	}
}

// TestConsulExecCluster tests running a 3-node exec Consul cluster with TLS.
func TestConsulExecClusterTLS(t *testing.T) {
	t.Parallel()
	te := testutil.NewExecTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	ca, err := pki.NewCertificateAuthority(Vault.Cli)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threeNodeConsulExecTLS(t, te, ca); err != nil {
		t.Fatal(err)
	}
}

// TestNomadExecCluster tests a three node exec Nomad cluster talking to a
// three node exec Consul cluster.
func TestNomadExecCluster(t *testing.T) {
	t.Parallel()
	te := testutil.NewExecTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	consulCluster, err := threeNodeConsulExecNoTLS(t, te)
	if err != nil {
		t.Fatal(err)
	}
	consulClient, err := consulCluster.Client()
	if err != nil {
		t.Fatal(err)
	}
	clientPorts := consulClient.Config().Ports

	_, err = BuildNomadCluster(te.Ctx, NomadClusterConfigSingleIP{
		WorkDir:     filepath.Join(te.TmpDir, "nomad"),
		ServerNames: []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3"},
		FirstPorts:  nextNomadBatch(4),
		ConsulAddrs: append(consulCluster.Config.APIAddrs(), fmt.Sprintf("localhost:%d", clientPorts.HTTP)),
	}, &exec.NomadExecBuilder{BinPath: te.NomadPath})
	if err != nil {
		t.Fatal(err)
	}
}

// TestNomadExecClusterTLS tests a three node exec Nomad cluster talking to a
// three node exec Consul cluster, with TLS.
func TestNomadExecClusterTLS(t *testing.T) {
	t.Parallel()
	names := []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3", "nomad-cli-1"}
	te := testutil.NewExecTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	ca, err := pki.NewCertificateAuthority(Vault.Cli)
	if err != nil {
		t.Fatal(err)
	}
	consulCluster, err := threeNodeConsulExecTLS(t, te, ca)
	if err != nil {
		t.Fatal(err)
	}
	consulClient, err := consulCluster.Client()
	if err != nil {
		t.Fatal(err)
	}
	clientPorts := consulClient.Config().Ports

	certs := make(map[string]pki.TLSConfigPEM)
	for i := 0; i < 4; i++ {
		tls, err := ca.NomadServerTLS(te.Ctx, "127.0.0.1", "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[names[i]] = *tls
	}

	_, err = BuildNomadCluster(te.Ctx, NomadClusterConfigSingleIP{
		WorkDir:     filepath.Join(te.TmpDir, "nomad"),
		ServerNames: names[:3],
		FirstPorts:  nextNomadBatch(4),
		ConsulAddrs: append(consulCluster.Config.APIAddrs(), fmt.Sprintf("localhost:%d", clientPorts.HTTP)),
		TLS:         certs,
	}, &exec.NomadExecBuilder{BinPath: te.NomadPath})
	if err != nil {
		t.Fatal(err)
	}
}

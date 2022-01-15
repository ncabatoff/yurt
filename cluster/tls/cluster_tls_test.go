package tls

import (
	"context"
	"testing"
	"time"

	"github.com/ncabatoff/yurt/cluster"
	"github.com/ncabatoff/yurt/helper/testhelper"
	"github.com/ncabatoff/yurt/runenv"
)

func TestConsulExecClusterTLS(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 20*time.Second)
	defer cleanup()
	_, _, err := cluster.NewConsulClusterAndClient(t.Name(), e, VaultCA)
	if err != nil {
		t.Fatal(err)
	}
}

func TestConsulDockerClusterTLS(t *testing.T) {
	e, cleanup := runenv.NewDockerTestEnv(t, 30*time.Second)
	defer cleanup()
	_, _, err := cluster.NewConsulClusterAndClient(t.Name(), e, VaultCA)
	if err != nil {
		t.Fatal(err)
	}
}

func TestNomadExecClusterTLS(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 60*time.Second)
	defer cleanup()

	cnc, _, err := cluster.NewConsulNomadClusterAndClient(t.Name(), e, VaultCA)
	if err != nil {
		t.Fatal(err)
	}

	consulAPIs, err := cnc.Consul.ClientAPIs()
	if err != nil {
		t.Fatal(err)
	}

	nomadAPIs, err := cnc.Nomad.ClientAPIs()
	if err != nil {
		t.Fatal(err)
	}
	testhelper.TestNomadJobs(t, e.Context(), consulAPIs[0], nomadAPIs[0],
		"prometheus", testhelper.ExecDockerJobHCL(t), testhelper.TestPrometheus)
}

func TestVaultExecClusterTLS(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 60*time.Second)
	defer cleanup()

	vc, err := cluster.NewVaultCluster(e.Context(), e, VaultCA, t.Name(), 3, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.Go(vc.Wait)
}

func TestConsulVaultExecClusterTLS(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 30*time.Second)
	defer cleanup()

	_, err := cluster.NewConsulVaultCluster(e.Context(), e, VaultCA, t.Name(), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCertificateAuthority_ConsulServerTLS(t *testing.T) {
	tlspem, err := VaultCA.ConsulServerTLS(context.Background(), "192.168.2.51", "168h")
	if err != nil {
		t.Fatal(err)
	}
	// TODO parse and check values
	if tlspem.CA == "" {
		t.Fatal("no cacert")
	}
	if tlspem.Cert == "" {
		t.Fatal("no cert")
	}
	if tlspem.PrivateKey == "" {
		t.Fatal("no key")
	}
}

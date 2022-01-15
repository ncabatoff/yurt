package cluster

import (
	"context"
	"github.com/ncabatoff/yurt/pki"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/ncabatoff/yurt/helper/testhelper"
	"github.com/ncabatoff/yurt/runenv"
	"github.com/ncabatoff/yurt/vault"
)

type TestFunc func(name string, e runenv.Env, ca pki.CertificateAuthority) error

func testConsulCluster(name string, e runenv.Env, ca *pki.CertificateAuthority) error {
	_, _, err := NewConsulClusterAndClient(name, e, ca)
	return err
}

func TestConsulExecCluster(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 20*time.Second)
	defer cleanup()
	if err := testConsulCluster(t.Name(), e, nil); err != nil {
		t.Fatal(err)
	}
}

func TestConsulDockerCluster(t *testing.T) {
	e, cleanup := runenv.NewDockerTestEnv(t, 20*time.Second)
	defer cleanup()
	if err := testConsulCluster(t.Name(), e, nil); err != nil {
		t.Fatal(err)
	}
}

func TestNomadDockerCluster(t *testing.T) {
	t.Skip("still need to copy prom bin into nomad client container for this to work")
	//e, cleanup := runenv.NewDockerTestEnv(t, 40*time.Second)
	//defer cleanup()
}

func TestNomadExecCluster(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 40*time.Second)
	defer cleanup()

	cnc, _, err := NewConsulNomadClusterAndClient(t.Name(), e, nil)
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

func TestVaultExecCluster(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 90*time.Second)
	defer cleanup()

	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Kill()
	e.Go(vc.Wait)
}

func TestVaultPrometheusExecCluster(t *testing.T) {
	e, cleanup := runenv.NewMonitoredExecTestEnv(t, 60*time.Second)
	defer cleanup()

	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	e.Go(vc.Wait)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	testhelper.UntilPass(t, ctx, func() error {
		return testhelper.PromQueryAlive(ctx, e.PromAddr().Address.String(), "vault", "vault_barrier_get_count", 3)
	})
}

func TestVaultExecClusterTransitSeal(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 60*time.Second)
	defer cleanup()

	seal, sealCleanup := testAutoSeal(t, e)
	defer sealCleanup()

	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, seal, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Stop()
	e.Go(vc.Wait)
}

func testAutoSeal(t *testing.T, e runenv.Env) (*vault.Seal, func()) {
	vcSeal, err := NewVaultCluster(e.Context(), e, nil, t.Name()+"-sealer", 1, nil, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	e.Go(vcSeal.Wait)

	vcSealClis, err := vcSeal.Clients()
	if err != nil {
		t.Fatal(err)
	}

	seal, err := vault.NewSealSource(e.Context(), vcSealClis[0], t.Name())
	if err != nil {
		t.Fatal(err)
	}

	return seal, vcSeal.Stop
}

func TestVaultExecClusterMigrateShamirToTransit(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 250*time.Second)
	defer cleanup()

	seal, sealCleanup := testAutoSeal(t, e)
	defer sealCleanup()

	testVaultClusterMigrateSeals(t, e, nil, seal, func() {})
}

func TestVaultExecClusterMigrateTransitToShamir(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 250*time.Second)
	defer cleanup()

	seal, sealCleanup := testAutoSeal(t, e)
	defer sealCleanup()

	testVaultClusterMigrateSeals(t, e, seal, nil, sealCleanup)
}

func TestVaultExecClusterMigrateTransitToTransit(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 250*time.Second)
	defer cleanup()

	seal1, sealCleanup1 := testAutoSeal(t, e)
	defer sealCleanup1()

	seal2, sealCleanup2 := testAutoSeal(t, e)
	defer sealCleanup2()

	testVaultClusterMigrateSeals(t, e, seal1, seal2, sealCleanup1)
}

func testVaultClusterMigrateSeals(t *testing.T, e runenv.Env, oldSeal, newSeal *vault.Seal, postMigrate func()) {
	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, oldSeal, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Stop()
	e.Go(vc.Wait)

	cli, err := vc.client(0)
	if err != nil {
		t.Fatal(err)
	}
	err = cli.Sys().PutRaftAutopilotConfiguration(&vaultapi.AutopilotConfig{
		LastContactThreshold: 5 * time.Second,
		//MaxTrailingLogs:                10,
		ServerStabilizationTime: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Second)
	vc.oldSeal = oldSeal
	vc.seal = newSeal

	err = vc.ReplaceAllActiveLast(e, true)
	if err != nil {
		t.Fatal(err)
	}
	// There's no way for us to monitor what raft has applied on the
	// node (or is there? can we look at seal config in storage using sys/raw?)
	// so for now sleep in the hope that will ensure the former leader
	// has the seal migration changes applied locally.
	time.Sleep(15 * time.Second)
	vc.oldSeal = nil

	t.Log("doing postMigrate")
	postMigrate()
	vc.oldSeal = nil

	err = vc.ReplaceAllActiveLast(e, false)
	if err != nil {
		t.Fatal(err)
	}
}

func TestVaultExecClusterWithReplace(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 120*time.Second)
	defer cleanup()

	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Stop()
	e.Go(vc.Wait)

	for i := 2; i >= 0; i-- {
		err = vc.ReplaceNode(e.Context(), e, i, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		client, err := vc.client(i)
		if err != nil {
			t.Fatal(err)
		}

		var state *vaultapi.AutopilotState
		for e.Context().Err() == nil {
			state, err = client.Sys().RaftAutopilotState()
			if state != nil && state.Healthy {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			t.Fatalf("timed out waiting for raft autopilot health, state=%#v err=%#v", state, err)
		}

		//if err := vault.LeadersHealthy(e.Context(), vc.servers); err != nil {
		//	t.Fatal(err)
		//}
	}
}

func TestConsulVaultExecCluster(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 30*time.Second)
	defer cleanup()

	cluster, err := NewConsulVaultCluster(e.Context(), e, nil, t.Name(), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Go(cluster.Wait)
}

func TestConsulVaultDockerCluster(t *testing.T) {
	t.Skip("need https://github.com/hashicorp/vault/pull/9109")
	e, cleanup := runenv.NewDockerTestEnv(t, 30*time.Second)
	defer cleanup()

	cluster, err := NewConsulVaultCluster(e.Context(), e, nil, t.Name(), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.Go(cluster.Wait)
}

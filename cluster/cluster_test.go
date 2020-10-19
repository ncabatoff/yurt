package cluster

import (
	"context"
	"fmt"
	"testing"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt/binaries"
	"github.com/ncabatoff/yurt/consul"
	"github.com/ncabatoff/yurt/helper/testhelper"
	"github.com/ncabatoff/yurt/nomad"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runenv"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/vault"
	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
)

func TestConsulExecCluster(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 20*time.Second)
	defer cleanup()
	testConsulCluster(t, e, nil)
}

func TestConsulExecClusterTLS(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 20*time.Second)
	defer cleanup()
	testConsulCluster(t, e, VaultCA)
}

func TestConsulDockerCluster(t *testing.T) {
	e, cleanup := runenv.NewDockerTestEnv(t, 20*time.Second)
	defer cleanup()
	testConsulCluster(t, e, nil)
}

func TestConsulDockerClusterTLS(t *testing.T) {
	e, cleanup := runenv.NewDockerTestEnv(t, 20*time.Second)
	defer cleanup()
	testConsulCluster(t, e, VaultCA)
}

func testConsulCluster(t *testing.T, e runenv.Env, ca *pki.CertificateAuthority) {
	cluster, err := NewConsulCluster(e.Context(), e, ca, t.Name(), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Stop()
	e.Go(cluster.Wait)

	client, err := cluster.ClientAgent(e.Context(), e, ca, t.Name()+"-consul-cli")
	e.Go(client.Wait)
	if err := consul.LeadersHealthy(e.Context(), []runner.Harness{client}, cluster.peerAddrs); err != nil {
		t.Fatalf("consul cluster not healthy: %v", err)
	}
}

func TestNomadDockerCluster(t *testing.T) {
	t.Skip("still need to copy prom bin into nomad client container for this to work")
	e, cleanup := runenv.NewDockerTestEnv(t, 40*time.Second)
	defer cleanup()

	// Note that we're not testing running docker jobs in Nomad yet, it's only
	// the infrastructure that's containerized here.
	testNomadCluster(t, e, nil, execDockerJobHCL(t))
}

func TestNomadExecCluster(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 40*time.Second)
	defer cleanup()

	testNomadCluster(t, e, nil, execDockerJobHCL(t))
}

func TestNomadExecClusterTLS(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 40*time.Second)
	defer cleanup()

	testNomadCluster(t, e, VaultCA, execDockerJobHCL(t))
}

func testNomadCluster(t *testing.T, e runenv.Env, ca *pki.CertificateAuthority, job string) {
	cnc, err := NewConsulNomadCluster(e.Context(), e, ca, t.Name(), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer cnc.Stop()
	e.Go(cnc.Wait)

	nomadClient, err := cnc.NomadClient(e, ca)
	if err != nil {
		t.Fatal(err)
	}
	defer nomadClient.Stop()
	e.Go(nomadClient.Wait)

	consulCli, err := consul.HarnessToAPI(cnc.Consul.servers[0])
	if err != nil {
		t.Fatal(err)
	}

	nomadCli, err := nomad.HarnessToAPI(cnc.Nomad.servers[0])
	if err != nil {
		t.Fatal(err)
	}
	testJobs(t, e.Context(), consulCli, nomadCli, job)
}

func testJobs(t *testing.T, ctx context.Context, consulCli *consulapi.Client, nomadCli *nomadapi.Client, jobhcl string) {
	job, err := nomadCli.Jobs().ParseHCL(jobhcl, true)
	if err != nil {
		t.Fatal(err)
	}
	register, _, err := nomadCli.Jobs().Register(job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if register.Warnings != "" {
		t.Logf("register warnings: %v", register.Warnings)
	}

	defer func() {
		_, _, err := nomadCli.Jobs().Deregister(*job.ID, false, nil)
		if err != nil {
			return
		}

		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			resp, _, err := nomadCli.Jobs().Summary(*job.ID, nil)
			if err != nil {
				continue
			}
			if s, ok := resp.Summary["prometheus"]; ok {
				if s.Running > 0 {
					continue
				}
			}
			return
		}
	}()

	deadline := time.Now().Add(900 * time.Second)
	var promaddr string
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		svcs, _, err := consulCli.Catalog().Service("prometheus", "", nil)
		if err != nil {
			t.Fatal(err, consulCli)
		}
		if len(svcs) != 0 {
			t.Log(svcs[0])
			hostip := svcs[0].ServiceTaggedAddresses["lan_ipv4"]
			promaddr = fmt.Sprintf("http://%s:%d", hostip.Address, hostip.Port)
			break
		}
	}
	if time.Now().After(deadline) {
		t.Fatalf("timed out without seeing service")
	}

	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		cli, err := promapi.NewClient(promapi.Config{Address: promaddr})
		if err != nil {
			t.Fatal(err)
		}
		api := promv1.NewAPI(cli)
		targs, err := api.Targets(ctx)
		if err != nil {
			continue
		}
		if len(targs.Active) > 0 {
			return
		}
		if len(targs.Dropped) > 0 {
			t.Logf("dropped targets: %v", targs.Dropped)
		}
	}
	t.Fatal("timed out without seeing targets")
}

func promDockerJobHCL(t *testing.T) string {
	return fmt.Sprintf(promJobHCL, "", "docker", `image = "prom/prometheus:v2.18.0"`)
}

func execDockerJobHCL(t *testing.T) string {
	promcmd, err := binaries.Default.Get("prometheus")
	if err != nil {
		t.Fatal(err)
	}

	return fmt.Sprintf(promJobHCL, "", "raw_exec", fmt.Sprintf(`command = "%s"`, promcmd))
}

//
// params: extra scrape configs, driver (docker or raw_exec), config,
var promJobHCL = `
job "prometheus" {
  datacenters = ["dc1"]
  type = "service"
  group "prometheus" {
    task "prometheus" {
      template {
        destination = "local/prometheus.yml"
        data = <<EOH
global:
  scrape_interval: "1s"

scrape_configs:
- job_name: prometheus-local
  static_configs:
  - targets: ['{{env "NOMAD_ADDR_http"}}']
%s
EOH
      }
      driver = "%s"
      config {
		%s
        args = [
          "--config.file=${NOMAD_TASK_DIR}/prometheus.yml",
          "--storage.tsdb.path=${NOMAD_TASK_DIR}/data/prometheus",
          "--web.listen-address=${NOMAD_ADDR_http}",
        ]
      }
      resources {
        network {
          port "http" {}
        }
      }
      service {
        name = "prometheus"
        port = "http"
        check {
          type = "http"
          port = "http"
          path = "/"
          interval = "3s"
          timeout = "1s"
        }
      }
    }
  }
}
`

func TestVaultExecCluster(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 30*time.Second)
	defer cleanup()

	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Stop()
	e.Go(vc.Wait)
}

func TestVaultPrometheusExecCluster(t *testing.T) {
	e, cleanup := runenv.NewMonitoredExecTestEnv(t, 30*time.Second)
	defer cleanup()

	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Stop()
	e.Go(vc.Wait)

	deadline := time.Now().Add(10 * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	testhelper.UntilPass(t, ctx, func() error {
		return testhelper.PromQueryAlive(ctx, e.PromAddr().Address.String(), "vault", "vault_barrier_get_count", 3)
	})
}

func TestVaultExecClusterTransitSeal(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 30*time.Second)
	defer cleanup()

	seal, sealCleanup := testAutoSeal(t, e)
	defer sealCleanup()

	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, seal)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Stop()
	e.Go(vc.Wait)
}

func testAutoSeal(t *testing.T, e runenv.Env) (*vault.Seal, func()) {
	vcSeal, err := NewVaultCluster(e.Context(), e, nil, t.Name()+"-sealer", 1, nil, nil)
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
	e, cleanup := runenv.NewExecTestEnv(t, 120*time.Second)
	defer cleanup()

	seal, sealCleanup := testAutoSeal(t, e)
	defer sealCleanup()

	testVaultClusterMigrateSeals(t, e, nil, seal, func() {})
}

func TestVaultExecClusterMigrateTransitToShamir(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 120*time.Second)
	defer cleanup()

	seal, sealCleanup := testAutoSeal(t, e)
	defer sealCleanup()

	testVaultClusterMigrateSeals(t, e, seal, nil, sealCleanup)
}

func TestVaultExecClusterMigrateTransitToTransit(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 120*time.Second)
	defer cleanup()

	seal1, sealCleanup1 := testAutoSeal(t, e)
	defer sealCleanup1()

	seal2, sealCleanup2 := testAutoSeal(t, e)
	defer sealCleanup2()

	testVaultClusterMigrateSeals(t, e, seal1, seal2, sealCleanup1)
}

func testVaultClusterMigrateSeals(t *testing.T, e runenv.Env, oldSeal, newSeal *vault.Seal, postMigrate func()) {
	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, oldSeal)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Stop()
	e.Go(vc.Wait)

	time.Sleep(10 * time.Second)
	vc.oldSeal, vc.seal = oldSeal, newSeal

	clients, err := vc.Clients()
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i >= 0; i-- {
		if i == 0 {
			err = clients[0].Sys().StepDown()
			if err != nil {
				t.Fatal(err)
			}
			// There's no way for us to monitor what raft has applied on the
			// node (or is there? can we look at seal config in storage using sys/raw?)
			// so for now sleep in the hope that will ensure the former leader
			// has the seal migration changes applied locally.
			time.Sleep(15 * time.Second)
			vc.oldSeal = nil
		}
		err = vc.replaceNode(e.Context(), e, i, nil, i != 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := vault.LeadersHealthy(e.Context(), vc.servers); err != nil {
			t.Fatal(err)
		}
	}

	postMigrate()
	vc.oldSeal = nil

	for i := 2; i >= 0; i-- {
		err = vc.replaceNode(e.Context(), e, i, nil, false)
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := vault.LeadersHealthy(e.Context(), vc.servers); err != nil {
		t.Fatal(err)
	}
}

func TestVaultExecClusterTLS(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 30*time.Second)
	defer cleanup()

	vc, err := NewVaultCluster(e.Context(), e, VaultCA, t.Name(), 3, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Stop()
	e.Go(vc.Wait)
}

func TestVaultExecClusterWithReplace(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 30*time.Second)
	defer cleanup()

	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Stop()
	e.Go(vc.Wait)

	for i := 2; i >= 0; i-- {
		err = vc.replaceNode(e.Context(), e, i, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if err := vault.LeadersHealthy(e.Context(), vc.servers); err != nil {
			t.Fatal(err)
		}
	}
}

func TestConsulVaultExecCluster(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 30*time.Second)
	defer cleanup()
	testConsulVaultCluster(t, e, nil)
}

func TestConsulVaultExecClusterTLS(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 30*time.Second)
	defer cleanup()
	testConsulVaultCluster(t, e, VaultCA)
}

func TestConsulVaultDockerCluster(t *testing.T) {
	t.Skip("need https://github.com/hashicorp/vault/pull/9109")
	e, cleanup := runenv.NewDockerTestEnv(t, 30*time.Second)
	defer cleanup()
	testConsulVaultCluster(t, e, nil)
}

func testConsulVaultCluster(t *testing.T, e runenv.Env, ca *pki.CertificateAuthority) {
	cluster, err := NewConsulVaultCluster(e.Context(), e, ca, t.Name(), 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Stop()
	e.Go(cluster.Wait)
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

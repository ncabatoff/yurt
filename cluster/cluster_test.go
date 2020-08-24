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

func TestVaultExecClusterTransitSeal(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 30*time.Second)
	defer cleanup()

	seal, err := vault.NewSealSource(e.Ctx, VaultCLI, t.Name())
	if err != nil {
		t.Fatal(err)
	}

	vc, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, seal)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Stop()
	e.Go(vc.Wait)
}

func TestVaultExecClusterMigrateShamirToTransit(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 60*time.Second)
	defer cleanup()

	vc2, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vc2.Stop()
	e.Go(vc2.Wait)

	vc1, err := NewVaultCluster(e.Context(), e, nil, t.Name()+"-sealer", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vc1.Stop()
	e.Go(vc1.Wait)

	vclis, err := vc1.Clients()
	if err != nil {
		t.Fatal(err)
	}

	vc2.seal, err = vault.NewSealSource(e.Ctx, vclis[0], t.Name())
	if err != nil {
		t.Fatal(err)
	}

	for i := 2; i >= 0; i-- {
		err = vc2.replaceNode(e.Context(), e, i, nil, true)
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := vault.LeadersHealthy(e.Context(), vc2.servers); err != nil {
		t.Fatal(err)
	}

	for i := 2; i >= 0; i-- {
		err = vc2.replaceNode(e.Context(), e, i, nil, false)
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := vault.LeadersHealthy(e.Context(), vc2.servers); err != nil {
		t.Fatal(err)
	}
}

func TestVaultExecClusterMigrateTransitToShamir(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 60*time.Second)
	defer cleanup()

	vc1, err := NewVaultCluster(e.Context(), e, nil, t.Name()+"-sealer", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vc1.Stop()
	e.Go(vc1.Wait)

	vclis, err := vc1.Clients()
	if err != nil {
		t.Fatal(err)
	}

	seal, err := vault.NewSealSource(e.Ctx, vclis[0], t.Name())
	if err != nil {
		t.Fatal(err)
	}

	vc2, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, seal)
	if err != nil {
		t.Fatal(err)
	}
	defer vc2.Stop()
	e.Go(vc2.Wait)

	vc2.oldSeal, vc2.seal = seal, nil

	for i := 2; i >= 0; i-- {
		err = vc2.replaceNode(e.Context(), e, i, nil, true)
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := vault.LeadersHealthy(e.Context(), vc2.servers); err != nil {
		t.Fatal(err)
	}

	vc1.Stop()
	vc2.oldSeal = nil

	for i := 2; i >= 0; i-- {
		err = vc2.replaceNode(e.Context(), e, i, nil, false)
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := vault.LeadersHealthy(e.Context(), vc2.servers); err != nil {
		t.Fatal(err)
	}
}

func TestVaultExecClusterMigrateTransitToTransit(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 60*time.Second)
	defer cleanup()

	vc1, err := NewVaultCluster(e.Context(), e, nil, t.Name()+"-sealer1", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vc1.Stop()
	e.Go(vc1.Wait)

	vclis, err := vc1.Clients()
	if err != nil {
		t.Fatal(err)
	}

	seal1, err := vault.NewSealSource(e.Ctx, vclis[0], t.Name()+"-seal1")
	if err != nil {
		t.Fatal(err)
	}

	vc2, err := NewVaultCluster(e.Context(), e, nil, t.Name(), 3, nil, seal1)
	if err != nil {
		t.Fatal(err)
	}
	defer vc2.Stop()
	e.Go(vc2.Wait)

	vc3, err := NewVaultCluster(e.Context(), e, nil, t.Name()+"-sealer2", 1, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer vc3.Stop()
	e.Go(vc3.Wait)

	vclis, err = vc3.Clients()
	if err != nil {
		t.Fatal(err)
	}

	seal2, err := vault.NewSealSource(e.Ctx, vclis[0], t.Name()+"-seal2")
	if err != nil {
		t.Fatal(err)
	}
	vc2.seal, vc2.oldSeal = seal2, vc2.seal

	for i := 2; i >= 0; i-- {
		err = vc2.replaceNode(e.Context(), e, i, nil, true)
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := vault.LeadersHealthy(e.Context(), vc2.servers); err != nil {
		t.Fatal(err)
	}

	vc1.Stop()
	vc2.oldSeal = nil

	for i := 2; i >= 0; i-- {
		err = vc2.replaceNode(e.Context(), e, i, nil, false)
		if err != nil {
			t.Fatal(err)
		}
	}

	if err := vault.LeadersHealthy(e.Context(), vc2.servers); err != nil {
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
	}

	if err := vault.LeadersHealthy(e.Context(), vc.servers); err != nil {
		t.Fatal(err)
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

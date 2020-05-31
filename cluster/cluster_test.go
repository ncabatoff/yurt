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
	"github.com/ncabatoff/yurt/runenv"
	"github.com/ncabatoff/yurt/runner"
	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
)

func TestConsulExecCluster(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 20*time.Second)
	defer cleanup()
	testConsulCluster(t, e)
}

func TestConsulDockerCluster(t *testing.T) {
	e, cleanup := runenv.NewDockerTestEnv(t, 20*time.Second)
	defer cleanup()
	testConsulCluster(t, e)
}

func testConsulCluster(t *testing.T, e runenv.Env) {
	//ca, err := pki.NewCertificateAuthority(Vault.Cli)
	//if err != nil {
	//	t.Fatal(err)
	//}
	//cm := &ConsulCertificateMaker{ca: ca, ttl: "30m"}

	cluster, err := NewConsulCluster(e.Context(), e, t.Name(), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Stop()
	e.Go(cluster.Wait)

	client, err := cluster.Client(e.Context(), e, t.Name()+"-consul-cli")
	e.Go(client.Wait)
	if err := consul.LeadersHealthy(e.Context(), []runner.Harness{client}, cluster.peerAddrs); err != nil {
		t.Fatal(err)
	}
}

func TestNomadDockerCluster(t *testing.T) {
	t.Skip("still need to copy prom bin into nomad client container for this to work")
	e, cleanup := runenv.NewDockerTestEnv(t, 40*time.Second)
	defer cleanup()

	// Note that we're not testing running docker jobs in Nomad yet, it's only
	// the infrastructure that's containerized here.
	testNomadCluster(t, e, execDockerJobHCL(t))
}

func TestNomadExecCluster(t *testing.T) {
	e, cleanup := runenv.NewExecTestEnv(t, 40*time.Second)
	defer cleanup()

	testNomadCluster(t, e, execDockerJobHCL(t))
}

func testNomadCluster(t *testing.T, e runenv.Env, job string) {
	cnc, err := NewConsulNomadCluster(e.Context(), e, t.Name(), 3)
	if err != nil {
		t.Fatal(err)
	}
	defer cnc.Stop()
	e.Go(cnc.Wait)

	nomadClient, err := cnc.NomadClient(e)
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
	return fmt.Sprintf(promJobHCL, "", "docker", `image = "prom/prometheus:v2.16.0"`)
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

package cluster

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/runner/exec"
	"github.com/ncabatoff/yurt/testutil"
	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/skratchdot/open-golang/open"
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
	te := testutil.NewExecTestEnv(t, 45*time.Second)
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

	cluster, err := BuildNomadCluster(te.Ctx, NomadClusterConfigSingleIP{
		WorkDir:     te.TmpDir,
		ServerNames: []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3"},
		FirstPorts:  nextNomadBatch(4),
		ConsulAddrs: append(consulCluster.Config.APIAddrs(), fmt.Sprintf("localhost:%d", clientPorts.HTTP)),
	}, &exec.NomadExecBuilder{BinPath: te.NomadPath})
	if err != nil {
		t.Fatal(err)
	}

	ccfg, err := consulCluster.servers[0].ConsulAPIConfig()
	if err != nil {
		t.Fatal(err)
	}
	_ = open.Run(fmt.Sprintf("%s://%s", ccfg.Scheme, ccfg.Address))

	consul, err := consulClient.ConsulAPI()
	if err != nil {
		t.Fatal(err)
	}

	ncfg, err := cluster.servers[0].NomadAPIConfig()
	if err != nil {
		t.Fatal(err)
	}
	open.Run(ncfg.Address)

	nomad, err := cluster.clients[0].NomadAPI()
	if err != nil {
		t.Fatal(err)
	}
	testJobs(t, te.Ctx, consul, nomad, fmt.Sprintf(execJobHCL, te.PrometheusPath))
}

var execJobHCL = `
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
EOH
      }
      driver = "raw_exec"
      config {
        command = "%s"
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
          path = "/metrics"
          interval = "3s"
          timeout = "1s"
        }
      }
    }
  }
} 
`

func testJobs(t *testing.T, ctx context.Context, consul *consulapi.Client, nomad *nomadapi.Client, jobhcl string) {
	job, err := nomad.Jobs().ParseHCL(jobhcl, true)
	if err != nil {
		t.Fatal(err)
	}
	register, _, err := nomad.Jobs().Register(job, nil)
	if err != nil {
		t.Fatal(err)
	}
	if register.Warnings != "" {
		t.Logf("register warnings: %v", register.Warnings)
	}

	defer func() {
		_, _, err := nomad.Jobs().Deregister(*job.ID, false, nil)
		if err != nil {
			return
		}

		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			resp, _, err := nomad.Jobs().Summary(*job.ID, nil)
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
	deadline := time.Now().Add(15 * time.Second)

	if time.Now().After(deadline) {
		t.Fatal("timed out without seeing running state")
	}

	var promaddr string
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		svcs, _, err := consul.Catalog().Service("prometheus", "", nil)
		if err != nil {
			t.Fatal(err, consul)
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

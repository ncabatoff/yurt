package cluster

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
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

	cluster, err := BuildNomadCluster(te.Ctx, NomadClusterConfigSingleIP{
		WorkDir:           te.TmpDir,
		ServerNames:       []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3"},
		FirstPorts:        nextNomadBatch(4),
		ConsulServerAddrs: consulCluster.Config.APIAddrs(),
	}, &exec.NomadExecBuilder{BinPath: te.NomadPath})
	if err != nil {
		t.Fatal(err)
	}

	ccfg, err := consulCluster.servers[0].APIConfig()
	if err != nil {
		t.Fatal(err)
	}
	// Open the first consul node's UI in the local browser, best-effort
	_ = open.Run(ccfg.Address.String())

	// Use the consul client node's API port
	consul, err := consulapi.NewClient(runner.ConsulAPIConfig(ccfg))
	if err != nil {
		t.Fatal(err)
	}

	ncfg, err := cluster.servers[0].APIConfig()
	if err != nil {
		t.Fatal(err)
	}
	// Open the first nomad server's UI in the local browser, best-effort
	open.Run(ncfg.Address.String())

	ctx := context.Background()
	consulClient, consulClientAddr, err := StartConsulClient(ctx, consulCluster)
	if err != nil {
		t.Fatal(err)
	}
	err = runner.ConsulRunnersHealthy(ctx, []runner.ConsulRunner{consulClient}, consulCluster.consulPeerAddrs)
	if err != nil {
		t.Fatal(err)
	}

	nomadClient, _, err := StartNomadClient(ctx, cluster, consulClientAddr)
	if err != nil {
		t.Fatal(err)
	}
	err = runner.NomadRunnersHealthy(ctx, []runner.NomadRunner{nomadClient}, cluster.nomadPeerAddrs)
	if err != nil {
		t.Fatal(err)
	}

	// Use the first nomad client node's API port to interact with the
	// cluster job interface.
	nomad, err := runner.NomadRunnerToAPI(nomadClient)
	if err != nil {
		t.Fatal(err)
	}

	var consulTargets, nomadTargets []string
	for _, c := range consulCluster.servers {
		ccfg, err := c.APIConfig()
		if err != nil {
			t.Fatal(err)
		}
		consulTargets = append(consulTargets, fmt.Sprintf("'%s'", ccfg.Address.Host))
	}

	for _, c := range cluster.servers {
		ncfg, err := c.APIConfig()
		if err != nil {
			t.Fatal(err)
		}
		nomadTargets = append(nomadTargets, fmt.Sprintf("'%s'", ncfg.Address.Host))
	}

	pcfg := fmt.Sprintf(`
- job_name: consul-servers
  metrics_path: /v1/agent/metrics
  params:
    format:
    - prometheus
  static_configs:
  - targets: [%s]
  # See https://github.com/hashicorp/consul/issues/4450
  metric_relabel_configs:
  - source_labels: [__name__]
    regex: 'consul_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat|installSnapshot)_((\w){36})((_sum)|(_count))?'
    target_label: raft_id
    replacement: '${2}'
  - source_labels: [__name__]
    regex: 'consul_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat|installSnapshot)_((\w){36})((_sum)|(_count))?'
    target_label: __name__
    replacement: 'consul_raft_replication_${1}${4}'

- job_name: nomad-servers
  metrics_path: /v1/metrics
  params:
    format:
    - prometheus
  static_configs:
  - targets: [%s]
  metric_relabel_configs:
  - source_labels: [__name__]
    regex: 'nomad_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat)_([^:]+:\d+)(_sum|_count)?'
    target_label: peer_instance
    replacement: '${2}'
  - source_labels: [__name__]
    regex: 'nomad_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat)_([^:]+:\d+)(_sum|_count)?'
    target_label: __name__
    replacement: 'nomad_raft_replication_${1}${3}'

`, strings.Join(consulTargets, ", "), strings.Join(nomadTargets, ", "))

	testJobs(t, te.Ctx, consul, nomad, fmt.Sprintf(execJobHCL, pcfg, te.PrometheusPath))
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
%s
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

	certs := make(map[string]pki.TLSConfigPEM)
	for i := 0; i < 4; i++ {
		tls, err := ca.NomadServerTLS(te.Ctx, "127.0.0.1", "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[names[i]] = *tls
	}

	_, err = BuildNomadCluster(te.Ctx, NomadClusterConfigSingleIP{
		WorkDir:           filepath.Join(te.TmpDir, "nomad"),
		ServerNames:       names[:3],
		FirstPorts:        nextNomadBatch(4),
		ConsulServerAddrs: consulCluster.Config.APIAddrs(),
		TLS:               certs,
	}, &exec.NomadExecBuilder{BinPath: te.NomadPath})
	if err != nil {
		t.Fatal(err)
	}
}

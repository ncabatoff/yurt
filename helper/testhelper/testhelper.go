package testhelper

import (
	"context"
	"fmt"
	"testing"
	"time"

	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt/binaries"
	promapi "github.com/prometheus/client_golang/api"
	"github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
)

func UntilPass(t *testing.T, ctx context.Context, f func() error) {
	t.Helper()
	lastErr := fmt.Errorf("timed out")
	for {
		time.Sleep(100 * time.Millisecond)
		err := f()
		switch {
		case err == nil:
			return
		case ctx.Err() != nil:
			t.Fatalf("timed out, last error: %v", lastErr)
		default:
			lastErr = err
		}
	}
}

func PromQueryActiveInstances(ctx context.Context, addr string, job string) ([]string, error) {
	cli, err := promapi.NewClient(promapi.Config{Address: addr})
	if err != nil {
		return nil, err
	}
	api := v1.NewAPI(cli)
	targs, err := api.Targets(ctx)
	if err != nil {
		return nil, err
	}
	var instances []string
	for _, target := range targs.Active {
		if string(target.Labels["job"]) == job {
			instances = append(instances, string(target.Labels["instance"]))
		}
	}
	return instances, err
}

func PromQueryVector(ctx context.Context, addr string, job string, metric string) ([]float64, error) {
	cli, err := promapi.NewClient(promapi.Config{Address: addr})
	if err != nil {
		return nil, err
	}
	api := v1.NewAPI(cli)
	query := fmt.Sprintf(`%s{job="%s"}`, metric, job)
	val, _, err := api.Query(ctx, query, time.Now())
	if err != nil {
		return nil, fmt.Errorf("query %q failed: %w", query, err)
	}
	vect, ok := val.(model.Vector)
	if !ok {
		return nil, fmt.Errorf("query %q did not return a vector: %v", query, err)
	}

	var samples []float64
	for _, s := range vect {
		samples = append(samples, float64(s.Value))
	}
	return samples, nil
}

// PromQueryAlive makes sure that the job has count target instances and that the
// chosen canary metric is present for all of them.
func PromQueryAlive(ctx context.Context, addr string, job string, metric string, count int) error {
	instances, err := PromQueryActiveInstances(ctx, addr, job)
	if err != nil {
		return err
	}
	if len(instances) != count {
		return fmt.Errorf("expected %d instances, got %d", count, len(instances))
	}

	samples, err := PromQueryVector(ctx, addr, job, metric)
	if len(samples) != count {
		return fmt.Errorf("expected %d samples in vector for metric %q, got %d", count, metric, len(samples))
	}

	return nil
}

// TestNomadJobs exercises a Consul/Nomad/Prometheus cluster by registering
// jobhcl as a Nomad job.
func TestNomadJobs(t *testing.T, ctx context.Context, consulCli *consulapi.Client,
	nomadCli *nomadapi.Client, name, jobhcl string, tester func(ctx context.Context, addr string) error) {

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
			if s, ok := resp.Summary[name]; ok {
				if s.Running > 0 {
					continue
				}
			}
			return
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var svcaddr string
	for ctx.Err() == nil {
		time.Sleep(100 * time.Millisecond)
		svcs, _, err := consulCli.Catalog().Service(name, "", nil)
		if err != nil {
			t.Fatal(err, consulCli)
		}
		if len(svcs) != 0 {
			hostip := svcs[0].ServiceTaggedAddresses["lan_ipv4"]
			svcaddr = fmt.Sprintf("http://%s:%d", hostip.Address, hostip.Port)
			break
		}
	}
	err = ctx.Err()

	for ctx.Err() == nil {
		time.Sleep(100 * time.Millisecond)
		err = tester(ctx, svcaddr)
		if err == nil {
			return
		}
	}
	t.Fatalf("timed out without satisfying tester, last err: %v", err)
}

func TestPrometheus(ctx context.Context, promaddr string) error {
	cli, err := promapi.NewClient(promapi.Config{Address: promaddr})
	if err != nil {
		return err
	}
	api := v1.NewAPI(cli)
	targs, err := api.Targets(ctx)
	if err != nil {
		return err
	}
	if len(targs.Active) > 0 {
		return nil
	}
	return fmt.Errorf("no active targets")
}

func promDockerJobHCL(t *testing.T) string {
	return fmt.Sprintf(promJobHCL, "", "docker", `image = "prom/prometheus:v2.18.0"`)
}

func ExecDockerJobHCL(t *testing.T) string {
	promcmd, err := binaries.Default.Get("prometheus")
	if err != nil {
		t.Fatal(err)
	}

	return fmt.Sprintf(promJobHCL, "", "raw_exec", fmt.Sprintf(`command = "%s"`, promcmd))
}

// promJobHCL is a fmt template that defines a Nomad job named "prometheus"
// which runs prometheus server on a dynamic listening port, configured to
// scrape itself plus any other scrape config provided (first fmt param).
// A service named "prometheus" is also defined, which will be registered in
// Consul together with a health check to ensure the port is accepting connections..
// fmt params: extra scrape configs, driver (docker or raw_exec), config
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

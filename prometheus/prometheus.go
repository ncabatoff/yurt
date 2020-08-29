package prometheus

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"time"
)

type Ports struct {
	HTTP int
}

var PortNames = struct {
	HTTP string
}{
	"http",
}

func DefPorts() Ports {
	return Ports{
		HTTP: 9090,
	}
}

func (c Ports) RunnerPorts() yurt.Ports {
	return yurt.Ports{
		Kind: "prometheus",
		NameOrder: []string{
			PortNames.HTTP,
		},
		ByName: map[string]yurt.Port{
			PortNames.HTTP: {c.HTTP, yurt.TCPOnly},
		},
	}
}

// Config describes how to run a single Prometheus instance.
type Config struct {
	Common   runner.Config
	JobNames []string
}

func (cc Config) Config() runner.Config {
	return cc.Common
}

func (cc Config) Name() string {
	return "prometheus"
}

func NewConfig(jobNames []string, tls *pki.TLSConfigPEM) Config {
	var t pki.TLSConfigPEM
	if tls != nil {
		t = *tls
	}
	return Config{
		JobNames: jobNames,
		Common: runner.Config{
			Ports: DefPorts().RunnerPorts(),
			TLS:   t,
		},
	}
}

func (cc Config) WithConfig(cfg runner.Config) runner.Command {
	cc.Common = cfg
	return cc
}

func (cc Config) Args() []string {
	args := []string{
		fmt.Sprintf("--storage.tsdb.path=%s", cc.Common.DataDir),
		fmt.Sprintf("--config.file=%s/prometheus.yml", cc.Common.ConfigDir),
	}
	port := cc.Common.Ports.ByName["http"].Number
	addr := "127.0.0.1"
	if cc.Common.NetworkConfig.Network != nil {
		addr = "0.0.0.0"
	}
	args = append(args, fmt.Sprintf("--web.listen-address=%s:%d", addr, port))

	return args
}

func (cc Config) Env() []string {
	return nil
}

func (cc Config) Files() map[string]string {
	files := map[string]string{}
	var tlsConfig string
	if cc.Common.TLS.CA != "" {
		files["ca.pem"] = cc.Common.TLS.CA
		tlsConfig = fmt.Sprintf(`
    ca_file: %s/ca.pem
`, cc.Common.ConfigDir)
	}

	files["prometheus.yml"] = fmt.Sprintf(`---
global:
  scrape_interval: "5s"

scrape_configs:
- job_name: prometheus
  static_configs:
  - targets:
    - 127.0.0.1:%d

`, cc.Common.Ports.ByName["http"].Number)

	for _, jobName := range cc.JobNames {
		files["prometheus.yml"] += fmt.Sprintf(`
- job_name: "%s"
  tls_config: %s
  file_sd_configs:
    - files: "%s/%s*.json"
      refresh_interval: 10s
`, jobName, tlsConfig, cc.Common.ConfigDir, jobName)

		files[jobName+".json"] = fmt.Sprintf(`
		
`)
	}
	return files
}

func HealthCheck(ctx context.Context, promAddr string) error {
	var err error
	var cli promapi.Client
	for ctx.Err() == nil {
		time.Sleep(100 * time.Millisecond)
		cli, err = promapi.NewClient(promapi.Config{Address: promAddr})
		if err != nil {
			continue
		}
		api := promv1.NewAPI(cli)
		var targets promv1.TargetsResult
		targets, err = api.Targets(ctx)
		if err != nil {
			continue
		}
		if len(targets.Active) == 0 || len(targets.Dropped) > 0 {
			err = fmt.Errorf("targets active=%d, dropped=%d", len(targets.Active), len(targets.Dropped))
			continue
		}
		break
	}
	if err == nil {
		err = ctx.Err()
	}
	return err
}

package prometheus

import (
	"net/url"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	config "github.com/prometheus/common/config"
	"github.com/prometheus/common/model"
)

func TestJobSerialization(t *testing.T) {
	jobs := map[string]ScrapeConfig{
		"one": {
			JobName:        "one",
			Params:         url.Values{"format": []string{"prometheus"}},
			ScrapeInterval: 5 * time.Second,
			MetricsPath:    "/v1/metrics",
			Scheme:         "https",
			ConsulServiceDiscoveryConfigs: []ConsulServiceDiscoveryConfig{
				{
					Server:   "localhost:8500",
					Services: []string{"nomad-client"},
				},
			},
			HTTPClientConfig: config.HTTPClientConfig{
				TLSConfig: config.TLSConfig{
					CAFile: "ca.pem",
				},
			},
			RelabelConfigs: []RelabelConfig{
				{
					SourceLabels: model.LabelNames{
						model.AddressLabel,
					},
					Regex:       "([^:]*):4646",
					TargetLabel: model.AddressLabel,
					Replacement: "${1}:9100",
					Action:      Replace,
				},
			},
		},
	}

	c := NewConfig(jobs, nil)
	files := c.Files()
	promyml, ok := files["prometheus.yml"]
	if !ok {
		t.Fatal("job one not found")
	}

	expected := `global:
  scrape_interval: 5s
scrape_configs:
- job_name: one
  params:
    format:
    - prometheus
  scrape_interval: 5s
  metrics_path: /v1/metrics
  scheme: https
  consul_sd_configs:
  - server: localhost:8500
    services:
    - nomad-client
  tls_config:
    ca_file: ca.pem
    insecure_skip_verify: false
  relabel_configs:
  - source_labels: [__address__]
    regex: ([^:]*):4646
    target_label: __address__
    replacement: ${1}:9100
    action: replace
- job_name: prometheus
  file_sd_configs:
  - files:
    - prometheus.*.json
    refresh_interval: 1s
`
	if d := cmp.Diff(expected, promyml); len(d) > 0 {
		t.Fatal(d)
	}
}

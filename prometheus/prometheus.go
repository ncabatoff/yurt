package prometheus

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"time"

	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
	promapi "github.com/prometheus/client_golang/api"
	promv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/config"
	"github.com/prometheus/common/model"
	"gopkg.in/yaml.v2"
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
	Common runner.Config
	Jobs   map[string]ScrapeConfig
}

func (cc Config) Config() runner.Config {
	return cc.Common
}

func (cc Config) Name() string {
	return "prometheus"
}

func NewConfig(jobs map[string]ScrapeConfig, tls *pki.TLSConfigPEM) Config {
	var t pki.TLSConfigPEM
	if tls != nil {
		t = *tls
	}
	c := Config{
		Jobs: make(map[string]ScrapeConfig, len(jobs)+1),
		Common: runner.Config{
			Ports: DefPorts().RunnerPorts(),
			TLS:   t,
		},
	}
	for name, job := range jobs {
		c.Jobs[name] = job
	}
	return c
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

type ConsulServiceDiscoveryConfig struct {
	Server   string
	Services []string
}

type FileServiceDiscoveryConfig struct {
	FilePatterns    []string       `yaml:"files"`
	RefreshInterval *time.Duration `yaml:"refresh_interval,omitempty"`
}

type RelabelConfig struct {
	// A list of labels from which values are taken and concatenated
	// with the configured separator in order.
	SourceLabels model.LabelNames `yaml:"source_labels,flow,omitempty"`
	// Separator is the string between concatenated values from the source labels.
	Separator string `yaml:"separator,omitempty"`
	// Regex against which the concatenation is matched.
	Regex string `yaml:"regex,omitempty"`
	// Modulus to take of the hash of concatenated values from the source labels.
	Modulus uint64 `yaml:"modulus,omitempty"`
	// TargetLabel is the label to which the resulting string is written in a replacement.
	// Regexp interpolation is allowed for the replace action.
	TargetLabel string `yaml:"target_label,omitempty"`
	// Replacement is the regex replacement pattern to be used.
	Replacement string `yaml:"replacement,omitempty"`
	// Action is the action to be performed for the relabeling.
	Action Action `yaml:"action,omitempty"`
}

type Action string

const (
	// Replace performs a regex replacement.
	Replace Action = "replace"
	// Keep drops targets for which the input does not match the regex.
	Keep Action = "keep"
	// Drop drops targets for which the input does match the regex.
	Drop Action = "drop"
	// HashMod sets a label to the modulus of a hash of labels.
	HashMod Action = "hashmod"
	// LabelMap copies labels to other labelnames based on a regex.
	LabelMap Action = "labelmap"
	// LabelDrop drops any label matching the regex.
	LabelDrop Action = "labeldrop"
	// LabelKeep drops any label not matching the regex.
	LabelKeep Action = "labelkeep"
)

type ScrapeConfig struct {
	JobName string `yaml:"job_name"`
	// Indicator whether the scraped metrics should remain unmodified.
	HonorLabels bool `yaml:"honor_labels,omitempty"`
	// Indicator whether the scraped timestamps should be respected.
	HonorTimestamps bool `yaml:"honor_timestamps,omitempty"`
	// A set of query parameters with which the target is scraped.
	Params url.Values `yaml:"params,omitempty"`
	// How frequently to scrape the targets of this scrape config.
	ScrapeInterval time.Duration `yaml:"scrape_interval,omitempty"`
	// The timeout for scraping targets of this config.
	ScrapeTimeout time.Duration `yaml:"scrape_timeout,omitempty"`
	// The HTTP resource path on which to fetch metrics from targets.
	MetricsPath string `yaml:"metrics_path,omitempty"`
	// The URL scheme with which to fetch metrics from targets.
	Scheme string `yaml:"scheme,omitempty"`
	// More than this many samples post metric-relabeling will cause the scrape to fail.
	SampleLimit uint `yaml:"sample_limit,omitempty"`
	// More than this many targets after the target relabeling will cause the
	// scrapes to fail.
	TargetLimit uint `yaml:"target_limit,omitempty"`

	ConsulServiceDiscoveryConfigs []ConsulServiceDiscoveryConfig `yaml:"consul_sd_configs,omitempty"`
	FileServiceDiscoveryConfigs   []FileServiceDiscoveryConfig   `yaml:"file_sd_configs,omitempty"`

	HTTPClientConfig config.HTTPClientConfig `yaml:",inline"`

	// List of target relabel configurations.
	RelabelConfigs []RelabelConfig `yaml:"relabel_configs,omitempty"`
	// List of metric relabel configurations.
	MetricRelabelConfigs []RelabelConfig `yaml:"metric_relabel_configs,omitempty"`
}

type GlobalConfig struct {
	ScrapeInterval time.Duration `yaml:"scrape_interval,omitempty"`
}

type PrometheusConfig struct {
	Global        *GlobalConfig  `yaml:"global,omitempty"`
	ScrapeConfigs []ScrapeConfig `yaml:"scrape_configs"`
}

func (cc Config) Files() map[string]string {
	files := map[string]string{}
	if cc.Common.TLS.CA != "" {
		files["ca.pem"] = cc.Common.TLS.CA
	}

	cc.Jobs["prometheus"] = ScrapeConfig{
		JobName: "prometheus",
	}

	localTargets := []map[string]interface{}{
		map[string]interface{}{
			"targets": []string{fmt.Sprintf("127.0.0.1:%d", cc.Common.Ports.ByName["http"].Number)},
		},
	}
	tbytes, _ := json.Marshal(localTargets)
	files["prometheus.local.json"] = string(tbytes)

	p := PrometheusConfig{
		Global: &GlobalConfig{
			ScrapeInterval: 5 * time.Second,
		},
	}
	for name, job := range cc.Jobs {
		if cc.Common.TLS.CA != "" {
			job.HTTPClientConfig.TLSConfig = config.TLSConfig{
				CAFile: cc.Common.TLS.CA,
			}
			job.Scheme = "https"
		}
		if len(job.ConsulServiceDiscoveryConfigs) == 0 {
			interval := time.Second
			job.FileServiceDiscoveryConfigs = []FileServiceDiscoveryConfig{
				{
					RefreshInterval: &interval,
					FilePatterns:    []string{fmt.Sprintf("%s.*.json", name)},
				},
			}
		}
		p.ScrapeConfigs = append(p.ScrapeConfigs, job)
	}
	b, err := yaml.Marshal(p)
	if err != nil {
		log.Fatal(err)
	}
	files["prometheus.yml"] = string(b)
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

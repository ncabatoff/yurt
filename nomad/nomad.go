package nomad

import (
	"context"
	"fmt"
	"log"
	"net/url"

	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/hashicorp/vault/sdk/helper/jsonutil"
	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/prometheus"
	"github.com/ncabatoff/yurt/runner"
	"github.com/prometheus/common/model"
)

type Ports struct {
	HTTP int
	Serf int
	RPC  int
}

var PortNames = struct {
	HTTP string
	Serf string
	RPC  string
}{
	"http",
	"serf",
	"rpc",
}

func DefPorts() Ports {
	return Ports{
		HTTP: 4646,
		Serf: 4647,
		RPC:  4648,
	}
}

func (c Ports) RunnerPorts() yurt.Ports {
	return yurt.Ports{
		Kind: "nomad",
		NameOrder: []string{
			PortNames.HTTP,
			PortNames.Serf,
			PortNames.RPC,
		},
		ByName: map[string]yurt.Port{
			PortNames.HTTP: {c.HTTP, yurt.TCPOnly},
			PortNames.Serf: {c.Serf, yurt.TCPAndUDP},
			PortNames.RPC:  {c.RPC, yurt.TCPOnly},
		},
	}
}

type NomadConfig struct {
	Common runner.Config
	// BootstrapExpect is how many servers to wait for when bootstrapping;
	// set this to 0 for clients.
	BootstrapExpect int
	// ConsulAddr is the address of the (normally local) consul agent, format is Host:Port
	ConsulAddr string
}

func NewConfig(bootstrapExpect int, consulAddr string, tls *pki.TLSConfigPEM) NomadConfig {
	var t pki.TLSConfigPEM
	if tls != nil {
		t = *tls
	}
	return NomadConfig{
		BootstrapExpect: bootstrapExpect,
		ConsulAddr:      consulAddr,
		Common: runner.Config{
			Ports: DefPorts().RunnerPorts(),
			TLS:   t,
		},
	}
}

func (nc NomadConfig) Config() runner.Config {
	return nc.Common
}

func (nc NomadConfig) Name() string {
	return "nomad"
}

func (nc NomadConfig) WithConfig(cfg runner.Config) runner.Command {
	nc.Common = cfg
	return nc
}

func (nc NomadConfig) Args() []string {
	args := []string{"agent"}
	if nc.BootstrapExpect > 0 {
		args = append(args, "-server",
			fmt.Sprintf("-bootstrap-expect=%d", nc.BootstrapExpect))
	} else {
		args = append(args, "-client")
	}

	if nc.Common.NodeName != "" {
		args = append(args, fmt.Sprintf("-node=%s", nc.Common.NodeName))
	}

	if nc.Common.ConfigDir != "" {
		args = append(args, fmt.Sprintf("-config=%s", nc.Common.ConfigDir))
	}

	if nc.ConsulAddr != "" {
		args = append(args, fmt.Sprintf("-consul-address=%s", nc.ConsulAddr))
	}

	args = append(args,
		fmt.Sprintf("-data-dir=%s", nc.Common.DataDir),
		fmt.Sprintf("-retry-interval=1s"),
		fmt.Sprintf("-consul-checks-use-advertise"),
	)

	if nc.Common.NetworkConfig.Network != nil {
		args = append(args,
			fmt.Sprintf(`-bind={{ GetPrivateInterfaces | include "network" "%s" | attr "address" }}`,
				nc.Common.NetworkConfig.Network.String()))
	} else {
		args = append(args, "-bind=127.0.0.1")
	}

	return args
}

func (nc NomadConfig) Env() []string {
	return nil
}

func (nc NomadConfig) Files() map[string]string {
	tlsCfg := map[string]interface{}{
		"http":                   true,
		"rpc":                    true,
		"verify_server_hostname": true,
	}
	allcfg := map[string]interface{}{
		"tls": tlsCfg,
		"consul": map[string]interface{}{
			"ssl":     true,
			"ca_file": "ca.pem",
		},
	}

	files := map[string]string{}
	if nc.Common.TLS.Cert != "" {
		files["nomad.pem"] = nc.Common.TLS.Cert
		tlsCfg["cert_file"] = "nomad.pem"
	}
	if nc.Common.TLS.PrivateKey != "" {
		files["nomad-key.pem"] = nc.Common.TLS.PrivateKey
		tlsCfg["key_file"] = "nomad-key.pem"
	}
	if nc.Common.TLS.CA != "" {
		files["ca.pem"] = nc.Common.TLS.CA
		tlsCfg["ca_file"] = "ca.pem"
	}
	if len(files) > 0 {
		tlsCfgBytes, err := jsonutil.EncodeJSON(allcfg)
		if err != nil {
			log.Fatal(err)
		}
		files["tls.json"] = string(tlsCfgBytes)
	}

	ports := nc.Common.Ports.ByName
	network := "127.0.0.0/8"
	if nc.Common.NetworkConfig.Network != nil {
		network = nc.Common.NetworkConfig.Network.String()
	}
	common := fmt.Sprintf(`
advertise { http = <<EOF
{{- GetAllInterfaces | include "network" "%s" | attr "address" -}}
EOF
  rpc = <<EOF
{{- GetAllInterfaces | include "network" "%s" | attr "address" -}}
EOF
  serf = <<EOF
{{- GetAllInterfaces | include "network" "%s" | attr "address" -}}
EOF
}
ports {
  http = %d
  serf = %d
  rpc = %d
}
telemetry {
  disable_hostname = true
  prometheus_metrics = true
  publish_allocation_metrics = true
}
disable_update_check = true
`, network, network, network, ports["http"].Number, ports["serf"].Number, ports["rpc"].Number)

	if nc.Common.LogDir != "" {
		common += fmt.Sprintf(`log_file="%s/"`+"\n", nc.Common.LogDir)
	}

	files["common.hcl"] = common

	if nc.BootstrapExpect == 0 {
		// Disable Java so I don't get popups on my MacOS machine about installing it.
		files["client.hcl"] = `
client {
  options = {
    "driver.blacklist" = "java"
  }
}
plugin "raw_exec" {
  config {
    enabled = true
  }
}
`
	}
	return files
}

func HarnessToAPI(r runner.Harness) (*nomadapi.Client, error) {
	apicfg, err := r.Endpoint("http", true)
	if err != nil {
		return nil, err
	}
	return apiConfigToClient(apicfg)
}

func apiConfigToClient(a *runner.APIConfig) (*nomadapi.Client, error) {
	cfg := nomadapi.DefaultConfig()
	cfg.Address = a.Address.String()
	cfg.TLSConfig.CACert = a.CAFile
	return nomadapi.NewClient(cfg)
}

func nomadLeaderAPIs(servers []runner.Harness) ([]runner.LeaderPeersAPI, error) {
	var ret []runner.LeaderPeersAPI
	for _, server := range servers {
		api, err := HarnessToAPI(server)
		if err != nil {
			return nil, err
		}
		ret = append(ret, api.Status())
	}
	return ret, nil
}

func LeadersHealthy(ctx context.Context, servers []runner.Harness, expectedPeers []string) error {
	apis, err := nomadLeaderAPIs(servers)
	if err != nil {
		return err
	}
	return runner.LeaderPeerAPIsHealthy(ctx, apis, expectedPeers)
}

var ServerScrapeConfig = prometheus.ScrapeConfig{
	JobName:     "nomad-servers",
	Params:      url.Values{"format": []string{"prometheus"}},
	MetricsPath: "/v1/metrics",
	RelabelConfigs: []prometheus.RelabelConfig{
		{
			Action:       prometheus.Replace,
			SourceLabels: model.LabelNames{model.MetricNameLabel},
			Regex:        "nomad_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat|installSnapshot)_([^:]*:\\d+)(_sum|_count)?",
			TargetLabel:  model.MetricNameLabel,
			Replacement:  "nomad_raft_replication_${1}${3}",
		},
		{
			Action:       prometheus.Replace,
			SourceLabels: model.LabelNames{model.MetricNameLabel},
			Regex:        "nomad_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat|installSnapshot)_([^:]*:\\d+)(_sum|_count)?",
			TargetLabel:  "raft_id",
			Replacement:  "${2}",
		},
	},
}

var ClientScrapeConfig = prometheus.ScrapeConfig{
	JobName:     "nomad-clients",
	Params:      url.Values{"format": []string{"prometheus"}},
	MetricsPath: "/v1/metrics",
	ConsulServiceDiscoveryConfigs: []prometheus.ConsulServiceDiscoveryConfig{
		{
			Server: "127.0.0.1:8500",
		},
	},
	RelabelConfigs: []prometheus.RelabelConfig{
		{
			Action:       prometheus.Keep,
			SourceLabels: model.LabelNames{model.MetaLabelPrefix + "consul_tags"},
			Regex:        ".*,prom,.*",
		},
	},
}

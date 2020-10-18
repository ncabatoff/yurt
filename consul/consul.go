package consul

import (
	"context"
	"fmt"
	"log"
	"net/url"

	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/vault/sdk/helper/jsonutil"
	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/prometheus"
	"github.com/ncabatoff/yurt/runner"
	"github.com/pkg/errors"
	"github.com/prometheus/common/model"
)

type Ports struct {
	HTTP    int
	DNS     int
	SerfLAN int
	SerfWAN int
	Server  int
}

var PortNames = struct {
	HTTP    string
	DNS     string
	SerfLAN string
	SerfWAN string
	Server  string
}{
	"http",
	"dns",
	"serf-lan",
	"serf-wan",
	"server",
}

func DefPorts() Ports {
	return Ports{
		Server:  8300,
		SerfLAN: 8301,
		SerfWAN: 8302,
		HTTP:    8500,
		DNS:     8600,
	}
}

func (c Ports) RunnerPorts() yurt.Ports {
	return yurt.Ports{
		Kind: "consul",
		NameOrder: []string{
			PortNames.Server,
			PortNames.SerfLAN,
			PortNames.SerfWAN,
			PortNames.HTTP,
			PortNames.DNS,
		},
		ByName: map[string]yurt.Port{
			PortNames.Server:  {c.Server, yurt.TCPOnly},
			PortNames.SerfLAN: {c.SerfLAN, yurt.TCPAndUDP},
			PortNames.SerfWAN: {c.SerfWAN, yurt.TCPAndUDP},
			PortNames.HTTP:    {c.HTTP, yurt.TCPOnly},
			PortNames.DNS:     {c.DNS, yurt.TCPAndUDP},
		},
	}
}

// ConsulConfig describes how to run a single Consul agent.
type ConsulConfig struct {
	Common runner.Config
	Server bool
	// JoinAddrs specifies the addresses of the Consul servers.  If they have
	// a :port suffix, it should be that of the SerfLAN port.
	JoinAddrs []string
}

func (cc ConsulConfig) Config() runner.Config {
	return cc.Common
}

func (cc ConsulConfig) Name() string {
	return "consul"
}

func NewConfig(server bool, joinAddrs []string, tls *pki.TLSConfigPEM) ConsulConfig {
	var t pki.TLSConfigPEM
	if tls != nil {
		t = *tls
	}
	return ConsulConfig{
		Server:    server,
		JoinAddrs: joinAddrs,
		Common: runner.Config{
			Ports: DefPorts().RunnerPorts(),
			TLS:   t,
		},
	}
}

func (cc ConsulConfig) WithConfig(cfg runner.Config) runner.Command {
	cc.Common = cfg
	return cc
}

func (cc ConsulConfig) Args() []string {
	args := []string{"agent",
		fmt.Sprintf("-data-dir=%s", cc.Common.DataDir),
		fmt.Sprintf("-retry-interval=1s"),
	}
	if cc.Common.NetworkConfig.Network != nil {
		args = append(args, "-client=0.0.0.0",
			fmt.Sprintf(`-bind={{ GetPrivateInterfaces | include "network" "%s" | attr "address" }}`,
				cc.Common.NetworkConfig.Network))
	} else {
		args = append(args, "-bind=127.0.0.1")
	}
	if cc.Common.NodeName != "" {
		args = append(args, fmt.Sprintf("-node=%s", cc.Common.NodeName))
	}
	if cc.Common.ConfigDir != "" {
		args = append(args, fmt.Sprintf("-config-dir=%s", cc.Common.ConfigDir))
	}
	if cc.Common.LogDir != "" {
		args = append(args, fmt.Sprintf("-log-file=%s/", cc.Common.LogDir))
	}
	for _, portName := range cc.Common.Ports.NameOrder {
		port := cc.Common.Ports.ByName[portName].Number
		if port != 0 {
			if portName == "http" {
				if len(cc.Common.TLS.Cert) > 0 {
					portName = "https"
					args = append(args, "-http-port=-1")
				} else {
					args = append(args, "-https-port=-1")
				}
			}
			args = append(args, fmt.Sprintf("-%s-port=%d", portName, port))
		}
	}

	for _, addr := range cc.JoinAddrs {
		args = append(args, fmt.Sprintf("-retry-join=%s", addr))
	}
	if cc.Server {
		args = append(args, "-ui", "-server",
			"-bootstrap-expect", fmt.Sprintf("%d", len(cc.JoinAddrs)))
	}
	return args
}

func (cc ConsulConfig) Env() []string {
	// This is only needed for Docker Consul, but it doesn't hurt to have it
	// everywhere.
	return []string{"CONSUL_DISABLE_PERM_MGMT=1"}
}

func (cc ConsulConfig) Files() map[string]string {
	tlsCfg := map[string]interface{}{
		"verify_incoming_rpc":    true,
		"verify_outgoing":        true,
		"verify_server_hostname": true,
	}

	files := map[string]string{}
	if cc.Common.TLS.Cert != "" {
		files["consul.pem"] = cc.Common.TLS.Cert
		tlsCfg["cert_file"] = "consul.pem"
	}
	if cc.Common.TLS.PrivateKey != "" {
		files["consul-key.pem"] = cc.Common.TLS.PrivateKey
		tlsCfg["key_file"] = "consul-key.pem"
	}
	if cc.Common.TLS.CA != "" {
		files["ca.pem"] = cc.Common.TLS.CA
		tlsCfg["ca_file"] = "ca.pem"
	}

	if len(files) > 0 {
		tlsCfgBytes, err := jsonutil.EncodeJSON(tlsCfg)
		if err != nil {
			log.Fatal(err)
		}
		files["tls.json"] = string(tlsCfgBytes)
	}

	files["common.hcl"] = `
disable_update_check = true
telemetry {
  disable_hostname = true
  prometheus_retention_time = "10m"
}
performance {
  raft_multiplier = 1
}
`
	return files
}

func HarnessToConfig(r runner.Harness) (*consulapi.Config, error) {
	apicfg, err := r.Endpoint("http", true)
	if err != nil {
		return nil, err
	}

	cfg := consulapi.DefaultConfig()
	cfg.Address = apicfg.Address.String()
	cfg.TLSConfig.CAFile = apicfg.CAFile

	return cfg, nil
}

func HarnessToAPI(r runner.Harness) (*consulapi.Client, error) {
	apicfg, err := r.Endpoint("http", true)
	if err != nil {
		return nil, err
	}
	return apiConfigToClient(apicfg)
}

func apiConfigToClient(a *runner.APIConfig) (*consulapi.Client, error) {
	cfg := consulapi.DefaultConfig()
	cfg.Address = a.Address.String()
	cfg.TLSConfig.CAFile = a.CAFile
	return consulapi.NewClient(cfg)
}

func consulLeaderAPIs(servers []runner.Harness) ([]runner.LeaderPeersAPI, error) {
	var ret []runner.LeaderPeersAPI
	for _, server := range servers {
		api, err := HarnessToAPI(server)
		if err != nil {
			return nil, errors.Wrap(err, "cannot create Consul client from harness")
		}
		ret = append(ret, api.Status())
	}
	return ret, nil
}

func LeadersHealthy(ctx context.Context, servers []runner.Harness, expectedPeers []string) error {
	apis, err := consulLeaderAPIs(servers)
	if err != nil {
		return errors.Wrap(err, "error getting Consul Leader APIs")
	}
	return runner.LeaderPeerAPIsHealthy(ctx, apis, expectedPeers)
}

var ServerScrapeConfig = prometheus.ScrapeConfig{
	JobName:     "consul-servers",
	Params:      url.Values{"format": []string{"prometheus"}},
	MetricsPath: "/v1/agent/metrics",
	RelabelConfigs: []prometheus.RelabelConfig{
		{
			Action:       prometheus.Replace,
			SourceLabels: model.LabelNames{model.MetricNameLabel},
			Regex:        "consul_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat|installSnapshot)_((\\w){36})((_sum)|(_count))?",
			TargetLabel:  model.MetricNameLabel,
			Replacement:  "consul_raft_replication_${1}${4}",
		},
		{
			Action:       prometheus.Replace,
			SourceLabels: model.LabelNames{model.MetricNameLabel},
			Regex:        "consul_raft_replication_(appendEntries_rpc|appendEntries_logs|heartbeat|installSnapshot)_((\\w){36})((_sum)|(_count))?",
			TargetLabel:  "raft_id",
			Replacement:  "${2}",
		},
	},
}

var ServiceScrapeConfig = prometheus.ScrapeConfig{
	JobName: "consul-services",
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
		{
			Action:       prometheus.Replace,
			SourceLabels: model.LabelNames{model.MetaLabelPrefix + "consul_service"},
			TargetLabel:  model.JobLabel,
		},
	},
}

package runner

import (
	"fmt"
	"github.com/hashicorp/consul/api"
	"github.com/hashicorp/vault/sdk/helper/jsonutil"
	"github.com/ncabatoff/yurt/pki"
	"log"
)

// ConsulRunner is used to create a Consul node and talk to it.
type ConsulRunner interface {
	Runner
	// TODO replace this with access to only port info?
	Config() ConsulConfig
	ConsulAPI() (*api.Client, error)
	ConsulAPIConfig() (*api.Config, error)
}

// ConsulRunnerBuilder is a factory used by clusters to create nodes.
type ConsulRunnerBuilder interface {
	MakeConsulRunner(ConsulCommand) (ConsulRunner, error)
}

// ConsulCommand defines how to create a Consul node
type ConsulCommand interface {
	Command() []string
	Files() map[string]string
	Config() ConsulConfig
	WithDirs(config, data, log string) ConsulCommand
}

type ConsulPorts struct {
	HTTP    int
	HTTPS   int
	DNS     int
	SerfLAN int
	SerfWAN int
	Server  int
}

func DefConsulPorts(tls bool) ConsulPorts {
	cp := ConsulPorts{DNS: 8600, SerfLAN: 8301, SerfWAN: 8302, Server: 8300}
	if tls {
		cp.HTTPS, cp.HTTP = 8501, -1
	} else {
		cp.HTTPS, cp.HTTP = -1, 8500
	}
	return cp
}

func SeqConsulPorts(start int, tls bool) ConsulPorts {
	cp := ConsulPorts{DNS: start + 1, SerfLAN: start + 2, SerfWAN: start + 3, Server: start + 4}
	if tls {
		cp.HTTPS, cp.HTTP = start, -1
	} else {
		cp.HTTPS, cp.HTTP = -1, start
	}
	return cp
}

func addPort(p *int, inc int) {
	if *p > 0 {
		*p += inc
	}
}

func (c ConsulPorts) Add(inc int) ConsulPorts {
	addPort(&c.HTTP, inc)
	addPort(&c.HTTPS, inc)
	addPort(&c.DNS, inc)
	addPort(&c.SerfLAN, inc)
	addPort(&c.SerfWAN, inc)
	addPort(&c.Server, inc)
	return c
}

// ConsulConfig describes how to run a single Consul agent.
type ConsulConfig struct {
	NetworkConfig NetworkConfig
	// JoinAddrs specifies the addresses of the Consul servers.  If they have
	// a :port suffix, it should be that of the SerfLAN port.
	JoinAddrs []string
	// NodeName names the consul node; not required if using a non-localhost network.
	NodeName string

	// LogConfig is optional, if not specified stdout/stderr are used.
	LogConfig LogConfig
	DataDir   string
	ConfigDir string

	// Non-default port listener settings can be provided, and must be if
	// there's no networking config (meaning everyone listens on localhost.)
	Ports ConsulPorts

	TLS pki.TLSConfigPEM
}

// ConsulServerConfig is a superset of ConsulConfig, containing configuration only
// needed by servers.
type ConsulServerConfig struct {
	ConsulConfig
}

func (cc ConsulConfig) Command() []string {
	args := []string{"agent",
		fmt.Sprintf("-data-dir=%s", cc.DataDir),
		fmt.Sprintf("-retry-interval=1s"),
	}
	if cc.NetworkConfig.Network != nil {
		args = append(args, "-client=0.0.0.0")
		// TODO try to restore this
		//		fmt.Sprintf(`-bind={{ GetPrivateInterfaces | include "network" "%s" | attr "address" }}`,
		//			cc.NetworkConfig.Network.IP))
	} else {
		args = append(args, "-bind=127.0.0.1")
	}
	if cc.NodeName != "" {
		args = append(args, fmt.Sprintf("-node=%s", cc.NodeName))
	}
	if cc.ConfigDir != "" {
		args = append(args, fmt.Sprintf("-config-dir=%s", cc.ConfigDir))
	}
	if cc.LogConfig.LogDir != "" {
		args = append(args, fmt.Sprintf("-log-file=%s/consul.log", cc.LogConfig.LogDir))
	}
	if cc.LogConfig.LogRotateBytes != 0 {
		args = append(args, fmt.Sprintf("-log-rotate-bytes=%d", cc.LogConfig.LogRotateBytes))
	}
	if cc.LogConfig.LogRotateMaxFiles != 0 {
		args = append(args, fmt.Sprintf("-log-rotate-max-files=%d", cc.LogConfig.LogRotateMaxFiles))
	}
	if cc.Ports.DNS != 0 {
		args = append(args, fmt.Sprintf("-dns-port=%d", cc.Ports.DNS))
	}
	if cc.Ports.HTTP != 0 {
		args = append(args, fmt.Sprintf("-http-port=%d", cc.Ports.HTTP))
	}
	if cc.Ports.HTTPS != 0 {
		// requires https://github.com/hashicorp/consul/pull/7086
		args = append(args, fmt.Sprintf("-https-port=%d", cc.Ports.HTTPS))
	}
	if cc.Ports.SerfLAN != 0 {
		args = append(args, fmt.Sprintf("-serf-lan-port=%d", cc.Ports.SerfLAN))
	}
	if cc.Ports.SerfWAN != 0 {
		args = append(args, fmt.Sprintf("-serf-wan-port=%d", cc.Ports.SerfWAN))
	}
	if cc.Ports.Server != 0 {
		args = append(args, fmt.Sprintf("-server-port=%d", cc.Ports.Server))
	}

	for _, addr := range cc.JoinAddrs {
		args = append(args, fmt.Sprintf("-retry-join=%s", addr))
	}
	return args
}

func (cc ConsulConfig) Files() map[string]string {
	tlsCfg := map[string]interface{}{
		"verify_incoming_rpc":    true,
		"verify_outgoing":        true,
		"verify_server_hostname": true,
	}

	files := map[string]string{}
	if cc.TLS.Cert != "" {
		files["consul.pem"] = cc.TLS.Cert
		tlsCfg["cert_file"] = "consul.pem"
	}
	if cc.TLS.PrivateKey != "" {
		files["consul-key.pem"] = cc.TLS.PrivateKey
		tlsCfg["key_file"] = "consul-key.pem"
	}
	if cc.TLS.CA != "" {
		files["ca.pem"] = cc.TLS.CA
		tlsCfg["ca_file"] = "ca.pem"
	}
	if len(files) == 0 {
		return nil
	}

	tlsCfgBytes, err := jsonutil.EncodeJSON(tlsCfg)
	if err != nil {
		log.Fatal(err)
	}
	files["tls.json"] = string(tlsCfgBytes)

	return files
}

func (cc ConsulConfig) Config() ConsulConfig {
	return cc
}

func (cc ConsulConfig) WithDirs(config, data, log string) ConsulCommand {
	cc.ConfigDir, cc.DataDir, cc.LogConfig.LogDir = config, data, log
	return cc
}

func (cc ConsulServerConfig) Command() []string {
	return append(cc.ConsulConfig.Command(), "-ui", "-server",
		"-bootstrap-expect", fmt.Sprintf("%d", len(cc.JoinAddrs)))
}

func (cc ConsulServerConfig) Files() map[string]string {
	return cc.ConsulConfig.Files()
}

func (cc ConsulServerConfig) WithDirs(config, data, log string) ConsulCommand {
	cc.ConfigDir, cc.DataDir, cc.LogConfig.LogDir = config, data, log
	return cc
}

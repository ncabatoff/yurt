package runner

import (
	"fmt"
	"log"

	"github.com/hashicorp/vault/sdk/helper/jsonutil"
	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/pki"
)

// ConsulRunner is used to create a Consul node and talk to it.
type ConsulRunner interface {
	APIRunner
}

// ConsulCommand defines how to create a Consul node
type ConsulCommand interface {
	Command
}

type ConsulPorts struct {
	HTTP    int
	DNS     int
	SerfLAN int
	SerfWAN int
	Server  int
}

func DefConsulPorts() ConsulPorts {
	return ConsulPorts{
		DNS:     8600,
		HTTP:    8500,
		SerfLAN: 8301,
		SerfWAN: 8302,
		Server:  8300,
	}
}

func SeqConsulPorts(start int) ConsulPorts {
	return ConsulPorts{
		HTTP:    start,
		DNS:     start + 1,
		SerfLAN: start + 2,
		SerfWAN: start + 3,
		Server:  start + 4,
	}
}

func addPort(p *int, inc int) {
	if *p > 0 {
		*p += inc
	}
}

func (c ConsulPorts) Add(inc int) ConsulPorts {
	addPort(&c.DNS, inc)
	addPort(&c.HTTP, inc)
	addPort(&c.SerfLAN, inc)
	addPort(&c.SerfWAN, inc)
	addPort(&c.Server, inc)
	return c
}

func (c ConsulPorts) ToList() []string {
	return []string{
		fmt.Sprintf("%d/tcp", c.HTTP),
		fmt.Sprintf("%d/tcp", c.DNS),
		fmt.Sprintf("%d/udp", c.DNS),
		fmt.Sprintf("%d/tcp", c.SerfLAN),
		fmt.Sprintf("%d/udp", c.SerfLAN),
		fmt.Sprintf("%d/tcp", c.SerfWAN),
		fmt.Sprintf("%d/udp", c.SerfWAN),
		fmt.Sprintf("%d/tcp", c.Server),
	}
}

// ConsulConfig describes how to run a single Consul agent.
type ConsulConfig struct {
	// NodeName names the consul node; not required if using a non-localhost network.
	NodeName      string
	NetworkConfig yurt.NetworkConfig

	// Non-default port listener settings can be provided, and must be if
	// there's no networking config (meaning everyone listens on localhost.)
	Ports ConsulPorts

	// JoinAddrs specifies the addresses of the Consul servers.  If they have
	// a :port suffix, it should be that of the SerfLAN port.
	JoinAddrs []string

	// LogConfig is optional, if not specified stdout/stderr are used.
	LogConfig LogConfig
	DataDir   string
	ConfigDir string

	TLS pki.TLSConfigPEM
}

func (cc ConsulConfig) Args() []string {
	args := []string{"agent",
		fmt.Sprintf("-data-dir=%s", cc.DataDir),
		fmt.Sprintf("-retry-interval=1s"),
	}
	if cc.NetworkConfig.Network != nil {
		args = append(args, "-client=0.0.0.0",
			fmt.Sprintf(`-bind={{ GetPrivateInterfaces | include "network" "%s" | attr "address" }}`,
				cc.NetworkConfig.Network))
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
		args = append(args, fmt.Sprintf("-log-file=%s/", cc.LogConfig.LogDir))
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
	if len(cc.TLS.Cert) > 0 {
		// requires https://github.com/hashicorp/consul/pull/7086
		args = append(args, fmt.Sprintf("-https-port=%d", cc.Ports.HTTP))
		args = append(args, "-http-port=-1")
	} else {
		args = append(args, fmt.Sprintf("-http-port=%d", cc.Ports.HTTP))
		args = append(args, "-https-port=-1")
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

func (cc ConsulConfig) Config() Config {
	return Config{
		Name:          "consul",
		LogDir:        cc.LogConfig.LogDir,
		DataDir:       cc.DataDir,
		ConfigDir:     cc.ConfigDir,
		NetworkConfig: cc.NetworkConfig,
		NodeName:      cc.NodeName,
		APIPort:       cc.Ports.HTTP,
		TLS:           cc.TLS,
		Ports:         cc.Ports.ToList(),
	}
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
`
	return files
}

func (cc ConsulConfig) WithDirs(config, data, log string) Command {
	cc.ConfigDir, cc.DataDir, cc.LogConfig.LogDir = config, data, log
	return cc
}

func (cc ConsulConfig) WithName(name string) Command {
	cc.NodeName = name
	return cc
}

func (cc ConsulConfig) WithNetwork(config yurt.NetworkConfig) Command {
	cc.NetworkConfig = config
	return cc
}

func (cc ConsulConfig) WithPorts(firstPort int) Command {
	cc.Ports = SeqConsulPorts(firstPort)
	return cc
}

// ConsulServerConfig is a superset of ConsulConfig, containing configuration only
// needed by servers.
type ConsulServerConfig struct {
	ConsulConfig
}

func (cc ConsulServerConfig) Args() []string {
	return append(cc.ConsulConfig.Args(), "-ui", "-server",
		"-bootstrap-expect", fmt.Sprintf("%d", len(cc.JoinAddrs)))
}

func (cc ConsulServerConfig) Files() map[string]string {
	return cc.ConsulConfig.Files()
}

func (cc ConsulServerConfig) WithDirs(config, data, log string) Command {
	cc.ConfigDir, cc.DataDir, cc.LogConfig.LogDir = config, data, log
	return cc
}

func (cc ConsulServerConfig) WithName(name string) Command {
	cc.NodeName = name
	return cc
}

func (cc ConsulServerConfig) WithNetwork(config yurt.NetworkConfig) Command {
	cc.NetworkConfig = config
	return cc
}

func (cc ConsulServerConfig) WithPorts(firstPort int) Command {
	cc.Ports = SeqConsulPorts(firstPort)
	return cc
}

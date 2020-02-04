package runner

import (
	"fmt"
	"github.com/hashicorp/consul/api"
)

// ConsulRunner is used to create a Consul node and talk to it.
type ConsulRunner interface {
	Runner
	// TODO replace this with access to only port info?
	Config() ConsulConfig
	ConsulAPI() (*api.Client, error)
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
}

type ConsulPorts struct {
	HTTP    int
	HTTPS   int
	DNS     int
	SerfLAN int
	SerfWAN int
	Server  int
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
}

// ConsulServerConfig is a superset of ConsulConfig, containing configuration only
// needed by servers.
type ConsulServerConfig struct {
	ConsulConfig
	// TLS certs + private keys
}

func (cc ConsulConfig) Command() []string {
	args := []string{"agent",
		fmt.Sprintf("-data-dir=%s", cc.DataDir),
		fmt.Sprintf("-retry-interval=1s"),
	}
	if cc.NetworkConfig.Network.IP != nil {
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
		// TODO TLS this is not an actual argument
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
	return nil
}

func (cc ConsulConfig) Config() ConsulConfig {
	return cc
}

func (cc ConsulServerConfig) Command() []string {
	return append(cc.ConsulConfig.Command(), "-ui", "-server",
		"-bootstrap-expect", fmt.Sprintf("%d", len(cc.JoinAddrs)))
}

func (cc ConsulServerConfig) Files() map[string]string {
	return nil
}

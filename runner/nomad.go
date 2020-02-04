package runner

import (
	"fmt"
	nomadapi "github.com/hashicorp/nomad/api"
)

type NomadCommand interface {
	Command() []string
	Files() map[string]string
	Config() NomadConfig
	WithDirs(config, data, log string) NomadCommand
}

type NomadRunner interface {
	Runner
	Config() NomadConfig
	NomadAPI() (*nomadapi.Client, error)
}

// NomadRunnerBuilder is a factory used by clusters to create nodes.
type NomadRunnerBuilder interface {
	MakeNomadRunner(NomadCommand) (NomadRunner, error)
}

type NomadPorts struct {
	HTTP int
	Serf int
	RPC  int
}

type NomadConfig struct {
	NodeName      string
	NetworkConfig NetworkConfig
	Ports         NomadPorts
	LogConfig     LogConfig
	DataDir       string
	ConfigDir     string
	ConsulAddr    string
}

func (nc NomadConfig) WithDirs(config, data, log string) NomadCommand {
	nc.ConfigDir, nc.DataDir, nc.LogConfig.LogDir = config, data, log
	return nc
}

var _ NomadCommand = NomadConfig{}

type NomadServerConfig struct {
	NomadConfig
	BootstrapExpect int
	// TLS certs + private keys
}

func (nc NomadConfig) Command() []string {
	args := []string{"agent",
		fmt.Sprintf("-data-dir=%s", nc.DataDir),
		fmt.Sprintf("-retry-interval=1s"),
		fmt.Sprintf("-consul-checks-use-advertise"),
	}
	if nc.NetworkConfig.Network.IP != nil {
		args = append(args,
			fmt.Sprintf(`-bind={{ GetPrivateInterfaces | include "network" "%s" | attr "address" }}`,
				nc.NetworkConfig.Network.String()))
	} else {
		args = append(args, "-bind=127.0.0.1")
	}
	if nc.NodeName != "" {
		args = append(args, fmt.Sprintf("-node=%s", nc.NodeName))
	}
	if nc.ConfigDir != "" {
		args = append(args, fmt.Sprintf("-config=%s", nc.ConfigDir))
	}
	if nc.ConsulAddr != "" {
		args = append(args, fmt.Sprintf("-consul-address=%s", nc.ConsulAddr))
	}

	return args
}

func (nc NomadConfig) Files() map[string]string {
	portHTTP, portSerf, portRPC := 4646, 4647, 4648
	if nc.Ports.HTTP != 0 {
		portHTTP = nc.Ports.HTTP
	}
	if nc.Ports.Serf != 0 {
		portSerf = nc.Ports.Serf
	}
	if nc.Ports.RPC != 0 {
		portRPC = nc.Ports.RPC
	}
	network := "127.0.0.0/8"
	if nc.NetworkConfig.Network.IP != nil {
		network = nc.NetworkConfig.Network.IP.String()
	}
	return map[string]string{"common.hcl": fmt.Sprintf(`
advertise {
  http = <<EOF
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
`, network, network, network, portHTTP, portSerf, portRPC)}
}

func (nc NomadConfig) Config() NomadConfig {
	return nc
}

func (nc NomadServerConfig) Command() []string {
	return append(nc.NomadConfig.Command(), "-server",
		fmt.Sprintf("-bootstrap-expect=%d", nc.BootstrapExpect))
}

func (nc NomadServerConfig) WithDirs(config, data, log string) NomadCommand {
	nc.ConfigDir, nc.DataDir, nc.LogConfig.LogDir = config, data, log
	return nc
}

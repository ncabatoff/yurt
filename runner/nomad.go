package runner

import (
	"fmt"
	"github.com/hashicorp/vault/sdk/helper/jsonutil"
	"log"

	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt/pki"
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
	NomadAPIConfig() (*nomadapi.Config, error)
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

func DefNomadPorts() NomadPorts {
	return NomadPorts{HTTP: 4646, Serf: 4647, RPC: 4648}
}

func SeqNomadPorts(start int) NomadPorts {
	return NomadPorts{HTTP: start, Serf: start + 1, RPC: start + 2}
}

func (n NomadPorts) Add(inc int) NomadPorts {
	addPort(&n.HTTP, inc)
	addPort(&n.Serf, inc)
	addPort(&n.RPC, inc)
	return n
}

type NomadConfig struct {
	NodeName      string
	NetworkConfig NetworkConfig
	Ports         NomadPorts
	LogConfig     LogConfig
	DataDir       string
	ConfigDir     string
	ConsulAddr    string
	TLS           pki.TLSConfigPEM
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

type NomadClientConfig struct {
	NomadConfig
}

func (nc NomadConfig) Command() []string {
	args := []string{"agent",
		fmt.Sprintf("-data-dir=%s", nc.DataDir),
		fmt.Sprintf("-retry-interval=1s"),
		fmt.Sprintf("-consul-checks-use-advertise"),
	}
	if nc.NetworkConfig.Network != nil {
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
	if nc.TLS.Cert != "" {
		files["nomad.pem"] = nc.TLS.Cert
		tlsCfg["cert_file"] = "nomad.pem"
	}
	if nc.TLS.PrivateKey != "" {
		files["nomad-key.pem"] = nc.TLS.PrivateKey
		tlsCfg["key_file"] = "nomad-key.pem"
	}
	if nc.TLS.CA != "" {
		files["ca.pem"] = nc.TLS.CA
		tlsCfg["ca_file"] = "ca.pem"
	}
	if len(files) > 0 {
		tlsCfgBytes, err := jsonutil.EncodeJSON(allcfg)
		if err != nil {
			log.Fatal(err)
		}
		files["tls.json"] = string(tlsCfgBytes)
	}

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
	if nc.NetworkConfig.Network != nil {
		network = nc.NetworkConfig.Network.String()
	}
	files["common.hcl"] = fmt.Sprintf(`
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
`, network, network, network, portHTTP, portSerf, portRPC)

	return files
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

func (nc NomadClientConfig) Command() []string {
	return append(nc.NomadConfig.Command(), "-client")
}

func (nc NomadClientConfig) Files() map[string]string {
	files := nc.NomadConfig.Files()
	// Disable Java so I don't get popups on my MacOS machine about installing it.
	files["client.hcl"] = `
client {
  options = {
    "driver.blacklist" = "java"
  }
}
`
	return files
}

func (nc NomadClientConfig) WithDirs(config, data, log string) NomadCommand {
	nc.ConfigDir, nc.DataDir, nc.LogConfig.LogDir = config, data, log
	return nc
}

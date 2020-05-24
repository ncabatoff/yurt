package runner

import (
	"fmt"
	"github.com/ncabatoff/yurt"
	"log"

	"github.com/hashicorp/vault/sdk/helper/jsonutil"
	"github.com/ncabatoff/yurt/pki"
)

type NomadCommand interface {
	Command
}

type NomadRunner interface {
	APIRunner
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

func (c NomadPorts) ToList() []string {
	return []string{
		fmt.Sprintf("%d/tcp", c.HTTP),
		fmt.Sprintf("%d/tcp", c.Serf),
		fmt.Sprintf("%d/tcp", c.RPC),
	}
}

type NomadConfig struct {
	NodeName      string
	NetworkConfig yurt.NetworkConfig
	Ports         NomadPorts
	LogConfig     LogConfig
	DataDir       string
	ConfigDir     string
	// ConsulAddr is the address of the (normally local) consul agent, format is Host:Port
	ConsulAddr string
	TLS        pki.TLSConfigPEM
}

func (nc NomadConfig) Args() []string {
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

func (nc NomadConfig) Config() Config {
	return Config{
		Name:          "nomad",
		LogDir:        nc.LogConfig.LogDir,
		DataDir:       nc.DataDir,
		ConfigDir:     nc.ConfigDir,
		NetworkConfig: nc.NetworkConfig,
		NodeName:      nc.NodeName,
		APIPort:       nc.Ports.HTTP,
		TLS:           nc.TLS,
		Ports:         nc.Ports.ToList(),
	}
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
`, network, network, network, portHTTP, portSerf, portRPC)

	if nc.LogConfig.LogDir != "" {
		common += fmt.Sprintf(`log_file="%s/"`+"\n", nc.LogConfig.LogDir)
	}
	if nc.LogConfig.LogRotateBytes != 0 {
		common += fmt.Sprintf(`log_rotate_bytes="%d"`+"\n", nc.LogConfig.LogRotateBytes)
	}
	if nc.LogConfig.LogRotateMaxFiles != 0 {
		common += fmt.Sprintf(`log_rotate_max_files="%d"`+"\n", nc.LogConfig.LogRotateMaxFiles)
	}

	files["common.hcl"] = common
	return files
}

type NomadServerConfig struct {
	NomadConfig
	BootstrapExpect int
}

func (nc NomadServerConfig) Args() []string {
	return append(nc.NomadConfig.Args(), "-server",
		fmt.Sprintf("-bootstrap-expect=%d", nc.BootstrapExpect))
}

func (nc NomadServerConfig) WithName(name string) Command {
	nc.NodeName = name
	return nc
}

func (nc NomadServerConfig) WithNetwork(config yurt.NetworkConfig) Command {
	nc.NetworkConfig = config
	return nc
}

func (nc NomadServerConfig) WithPorts(firstPort int) Command {
	nc.Ports = SeqNomadPorts(firstPort)
	return nc
}

func (nc NomadServerConfig) WithDirs(config, data, log string) Command {
	nc.ConfigDir, nc.DataDir, nc.LogConfig.LogDir = config, data, log
	return nc
}

type NomadClientConfig struct {
	NomadConfig
}

func (nc NomadClientConfig) WithName(name string) Command {
	nc.NodeName = name
	return nc
}

func (nc NomadClientConfig) WithNetwork(config yurt.NetworkConfig) Command {
	nc.NetworkConfig = config
	return nc
}

func (nc NomadClientConfig) WithPorts(firstPort int) Command {
	nc.Ports = SeqNomadPorts(firstPort)
	return nc
}

func (nc NomadClientConfig) Args() []string {
	return append(nc.NomadConfig.Args(), "-client")
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
plugin "raw_exec" {
  config {
    enabled = true
  }
}
`
	return files
}

func (nc NomadClientConfig) WithDirs(config, data, log string) Command {
	nc.ConfigDir, nc.DataDir, nc.LogConfig.LogDir = config, data, log
	return nc
}

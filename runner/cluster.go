package runner

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt/pki"
	"golang.org/x/sync/errgroup"
	"path/filepath"
)

type ConsulClusterConfig interface {
	ServerCommands() []ConsulCommand
	ClientCommand(name string) ConsulCommand
	JoinAddrs() []string
	APIAddrs() []string
	ServerAddrs() []string
}

type ConsulClusterConfigSingleIP struct {
	WorkDir       string
	ServerNames   []string
	FirstPorts    ConsulPorts
	PortIncrement int
	TLS           []pki.TLSConfigPEM
}

var _ ConsulClusterConfig = ConsulClusterConfigSingleIP{}

func (c ConsulClusterConfigSingleIP) ServerCommands() []ConsulCommand {
	var commands []ConsulCommand
	for i, name := range c.ServerNames {
		command := ConsulServerConfig{
			ConsulConfig{
				NodeName:  name, // "consul-srv-%d", i+1
				JoinAddrs: c.JoinAddrs(),
				ConfigDir: filepath.Join(c.WorkDir, name, "consul", "config"),
				DataDir:   filepath.Join(c.WorkDir, name, "consul", "data"),
				Ports:     c.FirstPorts.Add(i * c.PortIncrement),
			},
		}
		if len(c.TLS) > i {
			command.TLS = c.TLS[i]
		}
		commands = append(commands, command)
	}
	return commands
}

func (c ConsulClusterConfigSingleIP) ClientCommand(name string) ConsulCommand {
	panic("implement me")
}

func (c ConsulClusterConfigSingleIP) JoinAddrs() []string {
	var addrs []string
	for i := range c.ServerNames {
		port := c.FirstPorts.SerfLAN + c.PortIncrement*i
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", port))
	}
	return addrs
}

func (c ConsulClusterConfigSingleIP) APIAddrs() []string {
	var addrs []string
	for i := range c.ServerNames {
		port := c.FirstPorts.HTTP + c.PortIncrement*i
		if len(c.TLS) > 0 {
			port = c.FirstPorts.HTTPS + c.PortIncrement*i
		}
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", port))
	}
	return addrs
}

func (c ConsulClusterConfigSingleIP) ServerAddrs() []string {
	var addrs []string
	for i := range c.ServerNames {
		port := c.FirstPorts.Server + c.PortIncrement*i
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", port))
	}
	return addrs
}

type ConsulClusterConfigFixedIPs struct {
	NetworkConfig
	WorkDir         string
	ServerNames     []string
	ConsulServerIPs []string
}

var _ ConsulClusterConfig = ConsulClusterConfigFixedIPs{}

func (c ConsulClusterConfigFixedIPs) ServerCommands() []ConsulCommand {
	var commands []ConsulCommand
	for _, name := range c.ServerNames {
		command := ConsulServerConfig{
			ConsulConfig{
				NodeName:      name, // "consul-srv-%d", i+1
				JoinAddrs:     c.JoinAddrs(),
				DataDir:       filepath.Join(c.WorkDir, name, "consul", "data"),
				NetworkConfig: c.NetworkConfig,
			},
		}
		commands = append(commands, command)
	}
	return commands
}

func (c ConsulClusterConfigFixedIPs) ClientCommand(name string) ConsulCommand {
	panic("implement me")
}

func (c ConsulClusterConfigFixedIPs) JoinAddrs() []string {
	return c.ConsulServerIPs
}

func (c ConsulClusterConfigFixedIPs) APIAddrs() []string {
	var addrs []string
	for _, ip := range c.ConsulServerIPs {
		addrs = append(addrs, fmt.Sprintf("%s:8500", ip))
	}
	return addrs
}

func (c ConsulClusterConfigFixedIPs) ServerAddrs() []string {
	var addrs []string
	for _, ip := range c.ConsulServerIPs {
		addrs = append(addrs, fmt.Sprintf("%s:8300", ip))
	}
	return addrs
}

type ConsulClusterRunner struct {
	Builder ConsulRunnerBuilder
	Config  ConsulClusterConfig
	servers []ConsulRunner
	group   *errgroup.Group
}

func NewConsulClusterRunner(config ConsulClusterConfig, builder ConsulRunnerBuilder) (*ConsulClusterRunner, error) {
	return &ConsulClusterRunner{
		Config:  config,
		Builder: builder,
	}, nil
}

func (c *ConsulClusterRunner) StartServers(ctx context.Context) error {
	c.group, ctx = errgroup.WithContext(ctx)

	commands := c.Config.ServerCommands()
	for _, command := range commands {
		runner, err := c.Builder.MakeConsulRunner(command)
		if err != nil {
			return err
		}
		if _, err := runner.Start(ctx); err != nil {
			return err
		}
		c.group.Go(runner.Wait)
		c.servers = append(c.servers, runner)
	}

	return nil
}

type NomadClusterConfig interface {
	ServerCommands() []NomadCommand
	ClientCommand(name string) NomadCommand
}

type NomadClusterRunner struct {
	Builder        NomadRunnerBuilder
	Config         NomadClusterConfig
	ConsulAddrs    []string
	NomadPeerAddrs []string
	servers        []NomadRunner
	group          *errgroup.Group
}

type NomadClusterConfigSingleIP struct {
	WorkDir       string
	ServerNames   []string
	FirstPorts    NomadPorts
	PortIncrement int
	ConsulAddrs   []string
	TLS           []pki.TLSConfigPEM
}

func (n NomadClusterConfigSingleIP) ServerCommands() []NomadCommand {
	var commands []NomadCommand
	for i, name := range n.ServerNames {
		command := NomadServerConfig{
			BootstrapExpect: len(n.ServerNames),
			NomadConfig: NomadConfig{
				NodeName:   name, // "nomad-srv-%d", i+1
				DataDir:    filepath.Join(n.WorkDir, name, "nomad", "data"),
				ConfigDir:  filepath.Join(n.WorkDir, name, "nomad", "config"),
				Ports:      n.FirstPorts.Add(i * n.PortIncrement),
				ConsulAddr: n.ConsulAddrs[i],
			},
		}
		if len(n.TLS) > i {
			command.TLS = n.TLS[i]
		}
		commands = append(commands, command)
	}
	return commands
}

func (n NomadClusterConfigSingleIP) ClientCommand(name string) NomadCommand {
	panic("implement me")
}

var _ NomadClusterConfig = NomadClusterConfigSingleIP{}

type NomadClusterConfigFixedIPs struct {
	NetworkConfig
	WorkDir     string
	ServerNames []string
	ConsulAddrs []string
}

func (n NomadClusterConfigFixedIPs) ClientCommand(name string) NomadCommand {
	panic("implement me")
}

var _ NomadClusterConfig = NomadClusterConfigFixedIPs{}

func (n NomadClusterConfigFixedIPs) ServerCommands() []NomadCommand {
	var commands []NomadCommand
	for i, name := range n.ServerNames {
		command := NomadServerConfig{
			BootstrapExpect: len(n.ServerNames),
			NomadConfig: NomadConfig{
				NodeName:      name,
				DataDir:       filepath.Join(n.WorkDir, name, "nomad", "data"),
				ConfigDir:     filepath.Join(n.WorkDir, name, "nomad", "config"),
				NetworkConfig: n.NetworkConfig,
				ConsulAddr:    n.ConsulAddrs[i],
			},
		}
		commands = append(commands, command)
	}
	return commands
}

func NewNomadClusterRunner(config NomadClusterConfig, builder NomadRunnerBuilder) (*NomadClusterRunner, error) {
	return &NomadClusterRunner{
		Config:  config,
		Builder: builder,
	}, nil
}

func (n *NomadClusterRunner) StartServers(ctx context.Context) error {
	n.group, ctx = errgroup.WithContext(ctx)

	commands := n.Config.ServerCommands()
	for _, command := range commands {
		runner, err := n.Builder.MakeNomadRunner(command)
		if err != nil {
			return err
		}
		ip, err := runner.Start(ctx)
		if err != nil {
			return err
		}
		serverAddr := fmt.Sprintf("%s:%d", ip, 4648)
		n.NomadPeerAddrs = append(n.NomadPeerAddrs, serverAddr)
		n.group.Go(runner.Wait)
		n.servers = append(n.servers, runner)
	}

	return nil
}

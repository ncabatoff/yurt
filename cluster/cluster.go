package cluster

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt/util"
	"path/filepath"

	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
	"golang.org/x/sync/errgroup"
)

type ConsulClusterConfig interface {
	// ServerCommands are the commands used to start the servers in the cluster.
	ServerCommands() []runner.ConsulCommand
	// ClientCommand is the command used to start a client.
	ClientCommand() runner.ConsulCommand
	// JoinAddrs are the serflan server addresses a Consul agent should join to.
	JoinAddrs() []string
	// APIAddrs are the http(s) addresses of the servers.
	APIAddrs() []string
}

type ConsulClusterConfigSingleIP struct {
	WorkDir       string
	ServerNames   []string
	FirstPorts    runner.ConsulPorts
	PortIncrement int
	TLS           map[string]pki.TLSConfigPEM
}

var _ ConsulClusterConfig = ConsulClusterConfigSingleIP{}

func (c ConsulClusterConfigSingleIP) portIncrement() int {
	if c.PortIncrement == 0 {
		return 5
	}
	return c.PortIncrement
}

func (c ConsulClusterConfigSingleIP) ServerCommands() []runner.ConsulCommand {
	var commands []runner.ConsulCommand
	for i, name := range c.ServerNames {
		command := runner.ConsulServerConfig{
			ConsulConfig: runner.ConsulConfig{
				NodeName:  name,
				JoinAddrs: c.JoinAddrs(),
				ConfigDir: filepath.Join(c.WorkDir, name, "config"),
				DataDir:   filepath.Join(c.WorkDir, name, "data"),
				LogConfig: runner.LogConfig{
					LogDir: filepath.Join(c.WorkDir, name, "log"),
				},
				Ports: c.FirstPorts.Add(i * c.portIncrement()),
			},
		}
		if len(c.TLS) > 0 {
			command.TLS = c.TLS[name]
		}

		commands = append(commands, command)
	}
	return commands
}

func (c ConsulClusterConfigSingleIP) ClientCommand() runner.ConsulCommand {
	name := "consul-cli-1"
	cfg := runner.ConsulConfig{
		NodeName:  name,
		JoinAddrs: c.JoinAddrs(),
		ConfigDir: filepath.Join(c.WorkDir, name, "config"),
		DataDir:   filepath.Join(c.WorkDir, name, "data"),
		LogConfig: runner.LogConfig{
			LogDir: filepath.Join(c.WorkDir, name, "log"),
		},
		Ports: c.FirstPorts.Add(3 * c.portIncrement()),
	}
	if len(c.TLS) > 0 {
		cfg.TLS = c.TLS[name]
	}
	if cfg.TLS.CA == "" {
		if t := c.TLS[c.ServerNames[0]]; t.CA != "" {
			// Even if we don't have a server cert for this client, at least give
			// it the CA it needs to connect to the servers
			cfg.TLS.CA = t.CA
		}
	}
	return cfg
}

func (c ConsulClusterConfigSingleIP) JoinAddrs() []string {
	var addrs []string
	for i := range c.ServerNames {
		port := c.FirstPorts.SerfLAN + c.portIncrement()*i
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", port))
	}
	return addrs
}

func (c ConsulClusterConfigSingleIP) APIAddrs() []string {
	var addrs []string
	for i := range c.ServerNames {
		port := c.FirstPorts.HTTP + c.portIncrement()*i
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", port))
	}
	return addrs
}

type ConsulClusterConfigFixedIPs struct {
	util.NetworkConfig
	WorkDir         string
	ServerNames     []string
	ConsulServerIPs []string
	TLS             map[string]pki.TLSConfigPEM
}

var _ ConsulClusterConfig = ConsulClusterConfigFixedIPs{}

func (c ConsulClusterConfigFixedIPs) ServerCommands() []runner.ConsulCommand {
	var commands []runner.ConsulCommand
	for i := range c.ConsulServerIPs {
		name := c.ServerNames[i]
		command := runner.ConsulServerConfig{
			ConsulConfig: runner.ConsulConfig{
				NodeName:  name,
				JoinAddrs: c.JoinAddrs(),
				ConfigDir: filepath.Join(c.WorkDir, name, "config"),
				DataDir:   filepath.Join(c.WorkDir, name, "data"),
				LogConfig: runner.LogConfig{
					LogDir: filepath.Join(c.WorkDir, name, "log"),
				},
				NetworkConfig: c.NetworkConfig,
				Ports:         runner.DefConsulPorts(),
			},
		}
		if len(c.TLS) > 0 {
			command.TLS = c.TLS[name]
		}
		commands = append(commands, command)
	}
	return commands
}

func (c ConsulClusterConfigFixedIPs) ClientCommand() runner.ConsulCommand {
	name := "consul-cli-1"
	cfg := runner.ConsulConfig{
		NodeName:  name,
		JoinAddrs: c.JoinAddrs(),
		ConfigDir: filepath.Join(c.WorkDir, name, "config"),
		DataDir:   filepath.Join(c.WorkDir, name, "data"),
		LogConfig: runner.LogConfig{
			LogDir: filepath.Join(c.WorkDir, name, "log"),
		},
		NetworkConfig: c.NetworkConfig,
		Ports:         runner.DefConsulPorts(),
	}
	if len(c.TLS) > 0 {
		cfg.TLS = c.TLS[name]
	}
	if cfg.TLS.CA == "" {
		if t := c.TLS[c.ServerNames[0]]; t.CA != "" {
			// Even if we don't have a server cert for this client, at least give
			// it the CA it needs to connect to the servers
			cfg.TLS.CA = t.CA
		}
	}
	return cfg
}

func (c ConsulClusterConfigFixedIPs) JoinAddrs() []string {
	return c.ConsulServerIPs
}

func (c ConsulClusterConfigFixedIPs) APIAddrs() []string {
	var addrs []string
	ports := runner.DefConsulPorts()
	for _, ip := range c.ConsulServerIPs {
		addrs = append(addrs, fmt.Sprintf("%s:%d", ip, ports.HTTP))
	}
	return addrs
}

type ConsulClusterRunner struct {
	Builder         runner.ConsulRunnerBuilder
	Config          ConsulClusterConfig
	servers         []runner.ConsulRunner
	consulPeerAddrs []string
	// TODO eliminate clients, they don't belong as part of the cluster
	clients []runner.ConsulRunner
	group   *errgroup.Group
}

func NewConsulClusterRunner(config ConsulClusterConfig, builder runner.ConsulRunnerBuilder) (*ConsulClusterRunner, error) {
	if len(config.ServerCommands()) == 0 {
		return nil, fmt.Errorf("no server commands defined")
	}
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
		ip, err := runner.Start(ctx)
		if err != nil {
			return fmt.Errorf("error starting consul server: %w", err)
		}
		c.group.Go(runner.Wait)
		c.servers = append(c.servers, runner)
		c.consulPeerAddrs = append(c.consulPeerAddrs, fmt.Sprintf("%s:%d", ip, command.Config().Ports.Server))
	}

	return nil
}

// TODO make this a func instead of a method; don't manage clients as part of
// cluster group.
func (c *ConsulClusterRunner) StartClient(ctx context.Context) error {
	command := c.Config.ClientCommand()
	runner, err := c.Builder.MakeConsulRunner(command)
	if err != nil {
		return err
	}
	if _, err := runner.Start(ctx); err != nil {
		return fmt.Errorf("error starting consul agent: %w", err)
	}
	c.clients = append(c.clients, runner)

	return nil
}

func (c *ConsulClusterRunner) Client() (runner.ConsulRunner, error) {
	if len(c.clients) == 0 {
		return nil, fmt.Errorf("no clients yet")
	}
	return c.clients[0], nil
}

func (c *ConsulClusterRunner) WaitReady(ctx context.Context) error {
	allRunners := append([]runner.ConsulRunner{}, c.servers...)
	allRunners = append(allRunners, c.clients...)
	return runner.ConsulRunnersHealthy(ctx, allRunners, c.consulPeerAddrs)
}

func (c *ConsulClusterRunner) WaitExit() error {
	return c.group.Wait()
}

func (c *ConsulClusterRunner) APIConfigs() ([]*consulapi.Config, error) {
	var ret []*consulapi.Config
	for _, runner := range c.servers {
		apiCfg, err := runner.ConsulAPIConfig()
		if err != nil {
			return nil, err
		}
		ret = append(ret, apiCfg)
	}
	return ret, nil
}

func BuildConsulCluster(ctx context.Context, clusterCfg ConsulClusterConfig, builder runner.ConsulRunnerBuilder) (*ConsulClusterRunner, error) {
	consulCluster, err := NewConsulClusterRunner(clusterCfg, builder)
	if err != nil {
		return nil, err
	}
	if err := consulCluster.StartServers(ctx); err != nil {
		return nil, err
	}
	if err := consulCluster.StartClient(ctx); err != nil {
		return nil, err
	}
	if err := consulCluster.WaitReady(ctx); err != nil {
		return nil, err
	}

	return consulCluster, nil
}

type NomadClusterConfig interface {
	ServerCommands() []runner.NomadCommand
	ClientCommand() runner.NomadCommand
}

type NomadClusterRunner struct {
	Builder        runner.NomadRunnerBuilder
	Config         NomadClusterConfig
	ConsulAddrs    []string
	nomadPeerAddrs []string
	servers        []runner.NomadRunner
	clients        []runner.NomadRunner
	group          *errgroup.Group
}

type NomadClusterConfigSingleIP struct {
	WorkDir       string
	ServerNames   []string
	FirstPorts    runner.NomadPorts
	PortIncrement int
	ConsulAddrs   []string
	TLS           map[string]pki.TLSConfigPEM
}

func (n NomadClusterConfigSingleIP) ServerCommands() []runner.NomadCommand {
	var commands []runner.NomadCommand
	for i, name := range n.ServerNames {
		command := runner.NomadServerConfig{
			BootstrapExpect: len(n.ServerNames),
			NomadConfig: runner.NomadConfig{
				NodeName:  name,
				DataDir:   filepath.Join(n.WorkDir, name, "data"),
				ConfigDir: filepath.Join(n.WorkDir, name, "config"),
				LogConfig: runner.LogConfig{
					LogDir: filepath.Join(n.WorkDir, name, "log"),
				},
				Ports:      n.FirstPorts.Add(i * n.portIncrement()),
				ConsulAddr: n.ConsulAddrs[i],
			},
		}
		if len(n.TLS) > i {
			command.TLS = n.TLS[name]
		}
		commands = append(commands, command)
	}
	return commands
}

func (n NomadClusterConfigSingleIP) ClientCommand() runner.NomadCommand {
	name := "nomad-cli-1"
	cfg := runner.NomadConfig{
		NodeName:  name,
		ConfigDir: filepath.Join(n.WorkDir, name, "config"),
		DataDir:   filepath.Join(n.WorkDir, name, "data"),
		LogConfig: runner.LogConfig{
			LogDir: filepath.Join(n.WorkDir, name, "log"),
		},
		Ports:      n.FirstPorts.Add(3 * n.portIncrement()),
		ConsulAddr: n.ConsulAddrs[3],
	}
	if len(n.TLS) > 0 {
		cfg.TLS = n.TLS[name]
	}
	if cfg.TLS.CA == "" {
		if t := n.TLS[n.ServerNames[0]]; t.CA != "" {
			// Even if we don't have a server cert for this client, at least give
			// it the CA it needs to connect to the servers
			cfg.TLS.CA = t.CA
		}
	}
	return runner.NomadClientConfig{NomadConfig: cfg}
}

func (n NomadClusterConfigSingleIP) portIncrement() int {
	if n.PortIncrement == 0 {
		return 3
	}
	return n.PortIncrement
}

var _ NomadClusterConfig = NomadClusterConfigSingleIP{}

type NomadClusterConfigFixedIPs struct {
	util.NetworkConfig
	WorkDir        string
	ServerNames    []string
	NomadServerIPs []string
	ConsulAddrs    []string
	TLS            map[string]pki.TLSConfigPEM
}

func (n NomadClusterConfigFixedIPs) ClientCommand() runner.NomadCommand {
	name := "nomad-cli-1"
	cfg := runner.NomadConfig{
		NodeName:      name,
		NetworkConfig: n.NetworkConfig,
		ConfigDir:     filepath.Join(n.WorkDir, name, "config"),
		DataDir:       filepath.Join(n.WorkDir, name, "data"),
		LogConfig: runner.LogConfig{
			LogDir: filepath.Join(n.WorkDir, name, "log"),
		},
		Ports:      runner.DefNomadPorts(),
		ConsulAddr: n.ConsulAddrs[3],
	}
	if len(n.TLS) > 0 {
		cfg.TLS = n.TLS[name]
	}
	if cfg.TLS.CA == "" {
		if t := n.TLS[n.ServerNames[0]]; t.CA != "" {
			// Even if we don't have a server cert for this client, at least give
			// it the CA it needs to connect to the servers
			cfg.TLS.CA = t.CA
		}
	}
	return runner.NomadClientConfig{NomadConfig: cfg}
}

var _ NomadClusterConfig = NomadClusterConfigFixedIPs{}

func (n NomadClusterConfigFixedIPs) ServerCommands() []runner.NomadCommand {
	var commands []runner.NomadCommand
	for i, name := range n.ServerNames {
		command := runner.NomadServerConfig{
			BootstrapExpect: len(n.ServerNames),
			NomadConfig: runner.NomadConfig{
				NodeName:  name,
				DataDir:   filepath.Join(n.WorkDir, name, "data"),
				ConfigDir: filepath.Join(n.WorkDir, name, "config"),
				LogConfig: runner.LogConfig{
					LogDir: filepath.Join(n.WorkDir, name, "log"),
				},
				NetworkConfig: n.NetworkConfig,
				ConsulAddr:    n.ConsulAddrs[i],
				Ports:         runner.DefNomadPorts(),
				TLS:           n.TLS[name],
			},
		}
		commands = append(commands, command)
	}
	return commands
}

func NewNomadClusterRunner(config NomadClusterConfig, builder runner.NomadRunnerBuilder) (*NomadClusterRunner, error) {
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
		serverAddr := fmt.Sprintf("%s:%d", ip, command.Config().Ports.RPC)
		n.nomadPeerAddrs = append(n.nomadPeerAddrs, serverAddr)
		n.group.Go(runner.Wait)
		n.servers = append(n.servers, runner)
	}

	return nil
}

func (n *NomadClusterRunner) StartClient(ctx context.Context) error {
	command := n.Config.ClientCommand()
	runner, err := n.Builder.MakeNomadRunner(command)
	if err != nil {
		return err
	}
	if _, err := runner.Start(ctx); err != nil {
		return err
	}
	n.clients = append(n.clients, runner)

	return nil
}

func (n *NomadClusterRunner) WaitReady(ctx context.Context) error {
	allRunners := append([]runner.NomadRunner{}, n.servers...)
	allRunners = append(allRunners, n.clients...)
	return runner.NomadRunnersHealthy(ctx, allRunners, n.nomadPeerAddrs)
}

func (n *NomadClusterRunner) APIConfigs() ([]*nomadapi.Config, error) {
	var ret []*nomadapi.Config
	for _, runner := range n.servers {
		apiCfg, err := runner.NomadAPIConfig()
		if err != nil {
			return nil, err
		}
		ret = append(ret, apiCfg)
	}
	return ret, nil
}

func BuildNomadCluster(ctx context.Context, clusterCfg NomadClusterConfig, builder runner.NomadRunnerBuilder) (*NomadClusterRunner, error) {
	nomadCluster, err := NewNomadClusterRunner(clusterCfg, builder)
	if err != nil {
		return nil, err
	}
	if err := nomadCluster.StartServers(ctx); err != nil {
		return nil, err
	}
	if err := nomadCluster.StartClient(ctx); err != nil {
		return nil, err
	}
	if err := nomadCluster.WaitReady(ctx); err != nil {
		return nil, err
	}

	return nomadCluster, nil
}

func (n *NomadClusterRunner) WaitExit() error {
	return n.group.Wait()
}

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
	ClientCommand() ConsulCommand
	JoinAddrs() []string
	APIAddrs() []string
	ServerAddrs() []string
}

type ConsulClusterConfigSingleIP struct {
	WorkDir       string
	ServerNames   []string
	FirstPorts    ConsulPorts
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

func (c ConsulClusterConfigSingleIP) ServerCommands() []ConsulCommand {
	var commands []ConsulCommand
	for i, name := range c.ServerNames {
		command := ConsulServerConfig{
			ConsulConfig{
				NodeName:  name, // "consul-srv-%d", i+1
				JoinAddrs: c.JoinAddrs(),
				ConfigDir: filepath.Join(c.WorkDir, name, "consul", "config"),
				DataDir:   filepath.Join(c.WorkDir, name, "consul", "data"),
				Ports:     c.FirstPorts.Add(i * c.portIncrement()),
			},
		}
		if len(c.TLS) > 0 {
			command.TLS = c.TLS[name]
		}

		commands = append(commands, command)
	}
	return commands
}

func (c ConsulClusterConfigSingleIP) ClientCommand() ConsulCommand {
	name := "consul-cli-1"
	cfg := ConsulConfig{
		NodeName:  name,
		JoinAddrs: c.JoinAddrs(),
		ConfigDir: filepath.Join(c.WorkDir, name, "consul", "config"),
		DataDir:   filepath.Join(c.WorkDir, name, "consul", "data"),
		Ports:     c.FirstPorts.Add(3 * c.portIncrement()),
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
		if len(c.TLS) > 0 {
			port = c.FirstPorts.HTTPS + c.portIncrement()*i
		}
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", port))
	}
	return addrs
}

func (c ConsulClusterConfigSingleIP) ServerAddrs() []string {
	var addrs []string
	for i := range c.ServerNames {
		port := c.FirstPorts.Server + c.portIncrement()*i
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", port))
	}
	return addrs
}

type ConsulClusterConfigFixedIPs struct {
	NetworkConfig
	WorkDir         string
	ServerNames     []string
	ConsulServerIPs []string
	TLS             map[string]pki.TLSConfigPEM
}

var _ ConsulClusterConfig = ConsulClusterConfigFixedIPs{}

func (c ConsulClusterConfigFixedIPs) ServerCommands() []ConsulCommand {
	var commands []ConsulCommand
	for i := range c.ConsulServerIPs {
		name := c.ServerNames[i]
		command := ConsulServerConfig{
			ConsulConfig{
				NodeName:      name,
				JoinAddrs:     c.JoinAddrs(),
				ConfigDir:     filepath.Join(c.WorkDir, name, "consul", "config"),
				DataDir:       filepath.Join(c.WorkDir, name, "consul", "data"),
				NetworkConfig: c.NetworkConfig,
				Ports:         DefConsulPorts(len(c.TLS) > 0),
			},
		}
		if len(c.TLS) > 0 {
			command.TLS = c.TLS[name]
		}
		commands = append(commands, command)
	}
	return commands
}

func (c ConsulClusterConfigFixedIPs) ClientCommand() ConsulCommand {
	name := "consul-cli-1"
	cfg := ConsulConfig{
		NodeName:      name,
		JoinAddrs:     c.JoinAddrs(),
		ConfigDir:     filepath.Join(c.WorkDir, name, "consul", "config"),
		DataDir:       filepath.Join(c.WorkDir, name, "consul", "data"),
		NetworkConfig: c.NetworkConfig,
		Ports:         DefConsulPorts(len(c.TLS) > 0),
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
	ports := DefConsulPorts(len(c.TLS) > 0)
	port := ports.HTTP
	if len(c.TLS) > 0 {
		port = ports.HTTPS
	}
	for _, ip := range c.ConsulServerIPs {
		addrs = append(addrs, fmt.Sprintf("%s:%d", ip, port))
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
	clients []ConsulRunner
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

func (c *ConsulClusterRunner) StartClient(ctx context.Context) error {
	c.group, ctx = errgroup.WithContext(ctx)

	command := c.Config.ClientCommand()
	runner, err := c.Builder.MakeConsulRunner(command)
	if err != nil {
		return err
	}
	if _, err := runner.Start(ctx); err != nil {
		return err
	}
	c.group.Go(runner.Wait)
	c.clients = append(c.clients, runner)

	return nil
}

func (c *ConsulClusterRunner) Client() (ConsulRunner, error) {
	if len(c.clients) == 0 {
		return nil, fmt.Errorf("no clients yet")
	}
	return c.clients[0], nil
}

func (c *ConsulClusterRunner) WaitReady(ctx context.Context) error {
	allRunners := append([]ConsulRunner{}, c.servers...)
	allRunners = append(allRunners, c.clients...)
	return ConsulRunnersHealthy(ctx, allRunners, c.Config.ServerAddrs())
}

func (c *ConsulClusterRunner) WaitExit() error {
	return c.group.Wait()
}

func BuildConsulCluster(ctx context.Context, clusterCfg ConsulClusterConfig, builder ConsulRunnerBuilder) (*ConsulClusterRunner, error) {
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
	ServerCommands() []NomadCommand
	ClientCommand() NomadCommand
	APIAddrs() []string
}

type NomadClusterRunner struct {
	Builder        NomadRunnerBuilder
	Config         NomadClusterConfig
	ConsulAddrs    []string
	NomadPeerAddrs []string
	servers        []NomadRunner
	clients        []NomadRunner
	group          *errgroup.Group
}

type NomadClusterConfigSingleIP struct {
	WorkDir       string
	ServerNames   []string
	FirstPorts    NomadPorts
	PortIncrement int
	ConsulAddrs   []string
	TLS           map[string]pki.TLSConfigPEM
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

func (n NomadClusterConfigSingleIP) ClientCommand() NomadCommand {
	name := "nomad-cli-1"
	cfg := NomadConfig{
		NodeName:   name,
		ConfigDir:  filepath.Join(n.WorkDir, name, "nomad", "config"),
		DataDir:    filepath.Join(n.WorkDir, name, "nomad", "data"),
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
	return NomadClientConfig{NomadConfig: cfg}
}

func (n NomadClusterConfigSingleIP) portIncrement() int {
	if n.PortIncrement == 0 {
		return 3
	}
	return n.PortIncrement
}

func (n NomadClusterConfigSingleIP) RPCAddrs() []string {
	var addrs []string
	for i := range n.ServerNames {
		port := n.FirstPorts.RPC + n.portIncrement()*i
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", port))
	}
	return addrs
}

func (n NomadClusterConfigSingleIP) APIAddrs() []string {
	var addrs []string
	for i := range n.ServerNames {
		port := n.FirstPorts.HTTP + n.portIncrement()*i
		addrs = append(addrs, fmt.Sprintf("127.0.0.1:%d", port))
	}
	return addrs
}

var _ NomadClusterConfig = NomadClusterConfigSingleIP{}

type NomadClusterConfigFixedIPs struct {
	NetworkConfig
	WorkDir        string
	ServerNames    []string
	NomadServerIPs []string
	ConsulAddrs    []string
	TLS            map[string]pki.TLSConfigPEM
}

func (n NomadClusterConfigFixedIPs) ClientCommand() NomadCommand {
	name := "nomad-cli-1"
	cfg := NomadConfig{
		NodeName:      name,
		NetworkConfig: n.NetworkConfig,
		ConfigDir:     filepath.Join(n.WorkDir, name, "nomad", "config"),
		DataDir:       filepath.Join(n.WorkDir, name, "nomad", "data"),
		Ports:         DefNomadPorts(),
		ConsulAddr:    n.ConsulAddrs[3],
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
	return NomadClientConfig{NomadConfig: cfg}
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
				Ports:         DefNomadPorts(),
				TLS:           n.TLS[name],
			},
		}
		commands = append(commands, command)
	}
	return commands
}

func (n NomadClusterConfigFixedIPs) APIAddrs() []string {
	panic("implement me")
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
		serverAddr := fmt.Sprintf("%s:%d", ip, command.Config().Ports.RPC)
		n.NomadPeerAddrs = append(n.NomadPeerAddrs, serverAddr)
		n.group.Go(runner.Wait)
		n.servers = append(n.servers, runner)
	}

	return nil
}

func (n *NomadClusterRunner) StartClient(ctx context.Context) error {
	n.group, ctx = errgroup.WithContext(ctx)

	command := n.Config.ClientCommand()
	runner, err := n.Builder.MakeNomadRunner(command)
	if err != nil {
		return err
	}
	if _, err := runner.Start(ctx); err != nil {
		return err
	}
	n.group.Go(runner.Wait)
	n.clients = append(n.clients, runner)

	return nil
}

func (c *NomadClusterRunner) WaitReady(ctx context.Context) error {
	allRunners := append([]NomadRunner{}, c.servers...)
	allRunners = append(allRunners, c.clients...)
	return NomadRunnersHealthy(ctx, allRunners, c.NomadPeerAddrs)
}

func BuildNomadCluster(ctx context.Context, clusterCfg NomadClusterConfig, builder NomadRunnerBuilder) (*NomadClusterRunner, error) {
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

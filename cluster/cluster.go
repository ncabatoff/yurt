package cluster

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt/util"
	"path/filepath"

	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
	"golang.org/x/sync/errgroup"
)

type ConsulClusterConfig interface {
	// ServerCommands are the commands used to start the servers in the cluster.
	ServerCommands() []runner.ConsulServerConfig
	// ClientCommand is the command used to start a client.
	ClientCommand() runner.ConsulConfig
	// JoinAddrs are the serflan server addresses a Consul agent should join to.
	JoinAddrs() []string
	// APIAddrs are the http(s) addresses of the servers in host:port format.
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

func (c ConsulClusterConfigSingleIP) ServerCommands() []runner.ConsulServerConfig {
	var commands []runner.ConsulServerConfig
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

func (c ConsulClusterConfigSingleIP) ClientCommand() runner.ConsulConfig {
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

func (c ConsulClusterConfigFixedIPs) ServerCommands() []runner.ConsulServerConfig {
	var commands []runner.ConsulServerConfig
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

func (c ConsulClusterConfigFixedIPs) ClientCommand() runner.ConsulConfig {
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
	group           *errgroup.Group
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
		r, err := c.Builder.MakeConsulRunner(command)
		if err != nil {
			return err
		}
		ip, err := r.Start(ctx)
		if err != nil {
			return fmt.Errorf("error starting consul server: %w", err)
		}
		c.group.Go(r.Wait)
		c.servers = append(c.servers, r)
		c.consulPeerAddrs = append(c.consulPeerAddrs, fmt.Sprintf("%s:%d", ip, command.Ports.Server))
	}

	return nil
}

func StartConsulClient(ctx context.Context, clusterRunner *ConsulClusterRunner) (runner.ConsulRunner, string, error) {
	command := clusterRunner.Config.ClientCommand()
	r, err := clusterRunner.Builder.MakeConsulRunner(command)
	if err != nil {
		return nil, "", err
	}
	host, err := r.Start(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("error starting consul agent: %w", err)
	}

	return r, fmt.Sprintf("%s:%d", host, command.Ports.HTTP), nil
}

func (c *ConsulClusterRunner) WaitReady(ctx context.Context) error {
	return runner.ConsulRunnersHealthy(ctx, c.servers, c.consulPeerAddrs)
}

func (c *ConsulClusterRunner) WaitExit() error {
	return c.group.Wait()
}

func (c *ConsulClusterRunner) APIConfigs() ([]runner.APIConfig, error) {
	var ret []runner.APIConfig
	for _, r := range c.servers {
		apiCfg, err := r.APIConfig()
		if err != nil {
			return nil, err
		}
		ret = append(ret, *apiCfg)
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
	if err := consulCluster.WaitReady(ctx); err != nil {
		return nil, err
	}

	return consulCluster, nil
}

type NomadClusterConfig interface {
	ServerCommands() []runner.NomadServerConfig
	ClientCommand(consulAddr string) runner.NomadClientConfig
}

type NomadClusterRunner struct {
	Builder        runner.NomadRunnerBuilder
	Config         NomadClusterConfig
	ConsulAddrs    []string
	nomadPeerAddrs []string
	servers        []runner.NomadRunner
	group          *errgroup.Group
}

type NomadClusterConfigSingleIP struct {
	WorkDir           string
	ServerNames       []string
	FirstPorts        runner.NomadPorts
	PortIncrement     int
	ConsulServerAddrs []string
	TLS               map[string]pki.TLSConfigPEM
}

var _ NomadClusterConfig = NomadClusterConfigSingleIP{}

func (n NomadClusterConfigSingleIP) ServerCommands() []runner.NomadServerConfig {
	var commands []runner.NomadServerConfig
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
				ConsulAddr: n.ConsulServerAddrs[i],
			},
		}
		if len(n.TLS) > i {
			command.TLS = n.TLS[name]
		}
		commands = append(commands, command)
	}
	return commands
}

func (n NomadClusterConfigSingleIP) ClientCommand(consulAddr string) runner.NomadClientConfig {
	name := "nomad-cli-1"
	cfg := runner.NomadConfig{
		NodeName:  name,
		ConfigDir: filepath.Join(n.WorkDir, name, "config"),
		DataDir:   filepath.Join(n.WorkDir, name, "data"),
		LogConfig: runner.LogConfig{
			LogDir: filepath.Join(n.WorkDir, name, "log"),
		},
		Ports:      n.FirstPorts.Add(3 * n.portIncrement()),
		ConsulAddr: consulAddr,
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

type NomadClusterConfigFixedIPs struct {
	util.NetworkConfig
	WorkDir           string
	ServerNames       []string
	NomadServerIPs    []string
	ConsulServerAddrs []string
	TLS               map[string]pki.TLSConfigPEM
}

func (n NomadClusterConfigFixedIPs) ClientCommand(consulAddr string) runner.NomadClientConfig {
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
		ConsulAddr: consulAddr,
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

func (n NomadClusterConfigFixedIPs) ServerCommands() []runner.NomadServerConfig {
	var commands []runner.NomadServerConfig
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
				ConsulAddr:    n.ConsulServerAddrs[i],
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
		r, err := n.Builder.MakeNomadRunner(command)
		if err != nil {
			return err
		}
		ip, err := r.Start(ctx)
		if err != nil {
			return err
		}
		serverAddr := fmt.Sprintf("%s:%d", ip, command.Ports.RPC)
		n.nomadPeerAddrs = append(n.nomadPeerAddrs, serverAddr)
		n.group.Go(r.Wait)
		n.servers = append(n.servers, r)
	}

	return nil
}

func StartNomadClient(ctx context.Context, clusterRunner *NomadClusterRunner, consulAddr string) (runner.NomadRunner, string, error) {
	command := clusterRunner.Config.ClientCommand(consulAddr)
	r, err := clusterRunner.Builder.MakeNomadRunner(command)
	if err != nil {
		return nil, "", err
	}
	host, err := r.Start(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("error starting nomad agent: %w", err)
	}

	return r, fmt.Sprintf("%s:%d", host, command.Ports.HTTP), nil
}

func (n *NomadClusterRunner) WaitReady(ctx context.Context) error {
	return runner.NomadRunnersHealthy(ctx, n.servers, n.nomadPeerAddrs)
}

func (n *NomadClusterRunner) APIConfigs() ([]runner.APIConfig, error) {
	var ret []runner.APIConfig
	for _, r := range n.servers {
		apiCfg, err := r.APIConfig()
		if err != nil {
			return nil, err
		}
		ret = append(ret, *apiCfg)
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
	if err := nomadCluster.WaitReady(ctx); err != nil {
		return nil, err
	}

	return nomadCluster, nil
}

func (n *NomadClusterRunner) WaitExit() error {
	return n.group.Wait()
}

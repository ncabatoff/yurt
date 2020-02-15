package runner

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt/docker"
	"go.uber.org/atomic"
)

type ConsulDockerRunner struct {
	ConsulCommand ConsulCommand
	Image         string
	IP            string
	DockerAPI     *client.Client
	container     *types.ContainerJSON
	cancel        func()
}

var _ ConsulRunner = (*ConsulDockerRunner)(nil)

func NewConsulDockerRunner(api *client.Client, image, ip string, command ConsulCommand) (*ConsulDockerRunner, error) {
	return &ConsulDockerRunner{
		DockerAPI:     api,
		ConsulCommand: command,
		Image:         image,
		IP:            ip,
	}, nil
}

func (c *ConsulDockerRunner) Config() ConsulConfig {
	return c.ConsulCommand.Config()
}

func (c *ConsulDockerRunner) Start(ctx context.Context) (net.IP, error) {
	if c.container != nil {
		return nil, fmt.Errorf("already running")
	}

	consulConfig := c.ConsulCommand.Config()
	localConfigDir, localDataDir, localLogDir := consulConfig.ConfigDir, consulConfig.DataDir, consulConfig.LogConfig.LogDir
	for _, dir := range []string{localConfigDir, localDataDir, localLogDir} {
		if dir == "" {
			continue
		}
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return nil, err
		}
	}
	for name, contents := range consulConfig.Files() {
		if err := writeConfig(consulConfig.ConfigDir, name, contents); err != nil {
			return nil, err
		}
	}
	exposedPorts := nat.PortSet{
		"8300/tcp": {},
		"8301/tcp": {},
		"8301/udp": {},
		"8302/tcp": {},
		"8302/udp": {},
		"8500/tcp": {},
		"8501/tcp": {},
		"8600/tcp": {},
		"8600/udp": {},
	}

	dr := &docker.Runner{
		DockerAPI: c.DockerAPI,
		NetName:   consulConfig.NetworkConfig.DockerNetName,
		ContainerConfig: &container.Config{
			Image: c.Image,
			Cmd:   c.ConsulCommand.WithDirs("/consul/config", "/consul/data", "/consul/log").Command(),
			Env:   []string{"CONSUL_DISABLE_PERM_MGMT=1"},
			Labels: map[string]string{
				"yurt": "true",
			},
			ExposedPorts: exposedPorts,
			WorkingDir:   "/consul/config",
		},
		Mounts: []mount.Mount{
			{
				Type:     mount.TypeBind,
				Source:   localConfigDir,
				Target:   "/consul/config",
				ReadOnly: true,
			},
			{
				Type:   mount.TypeBind,
				Source: localDataDir,
				Target: "/consul/data",
			},
			{
				Type:   mount.TypeBind,
				Source: localLogDir,
				Target: "/consul/log",
			},
		},
		ContainerName: consulConfig.NodeName,
		IP:            c.IP,
		AutoRemove:    true,
	}

	ctx, cancel := context.WithCancel(ctx)
	cont, err := dr.Start(ctx)
	if err != nil {
		return nil, err
	}
	c.container = cont
	c.cancel = cancel
	//go func() {
	//	_ = docker.ContainerLogs(ctx, c.DockerAPI, *c.container.ID)
	//}()
	go func() {
		<-ctx.Done()
		_ = docker.CleanupContainer(context.Background(), c.DockerAPI, cont.ID)
	}()
	return docker.ContainerIP(*cont, consulConfig.NetworkConfig.DockerNetName)
}

func (c ConsulDockerRunner) Wait() error {
	return docker.DockerWait(c.DockerAPI, c.container.ID)
}

func (c ConsulDockerRunner) Stop() error {
	c.cancel()
	return nil
}

func (c *ConsulDockerRunner) ConsulAPI() (*consulapi.Client, error) {
	apiCfg, err := c.ConsulAPIConfig()
	if err != nil {
		return nil, err
	}
	return consulapi.NewClient(apiCfg)
}

func (c *ConsulDockerRunner) ConsulAPIConfig() (*consulapi.Config, error) {
	cfg := c.ConsulCommand.Config()
	apiConfig := consulapi.DefaultNonPooledConfig()

	if len(cfg.TLS.Cert) > 0 {
		apiConfig.Scheme = "https"
		apiConfig.TLSConfig.CAFile = filepath.Join(cfg.ConfigDir, "ca.pem")
	}

	port := cfg.Ports.HTTP
	ports := c.container.NetworkSettings.NetworkSettingsBase.Ports[nat.Port(fmt.Sprintf("%d/tcp", port))]
	if len(ports) == 0 {
		return nil, fmt.Errorf("no binding for Consul API port %d", port)
	}

	apiConfig.Address = fmt.Sprintf("%s:%s", "127.0.0.1", ports[0].HostPort)
	return apiConfig, nil
}

func (c *ConsulDockerRunner) AgentAddress() (string, error) {
	netName := c.ConsulCommand.Config().NetworkConfig.DockerNetName
	ip, err := docker.ContainerIP(*c.container, netName)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("http://%s:8500", ip), nil
}

type ConsulDockerBuilder struct {
	DockerAPI *client.Client
	Image     string
	IP        string
}

var _ ConsulRunnerBuilder = (*ConsulDockerBuilder)(nil)

func (c *ConsulDockerBuilder) MakeConsulRunner(command ConsulCommand) (ConsulRunner, error) {
	return NewConsulDockerRunner(c.DockerAPI, c.Image, c.IP, command)
}

type ConsulDockerServerBuilder struct {
	DockerAPI *client.Client
	Image     string
	IPs       []string
	i         atomic.Uint32
}

var _ ConsulRunnerBuilder = (*ConsulDockerServerBuilder)(nil)

func (c *ConsulDockerServerBuilder) MakeConsulRunner(command ConsulCommand) (ConsulRunner, error) {
	ip := c.IPs[c.i.Inc()-1]
	return NewConsulDockerRunner(c.DockerAPI, c.Image, ip, command)
}

type NomadDockerRunner struct {
	NomadCommand NomadCommand
	Image        string
	IP           string
	DockerAPI    *client.Client
	container    *types.ContainerJSON
	cancel       func()
}

func (n *NomadDockerRunner) NomadAPI() (*nomadapi.Client, error) {
	apiCfg, err := n.NomadAPIConfig()
	if err != nil {
		return nil, err
	}
	return nomadapi.NewClient(apiCfg)
}

func (n *NomadDockerRunner) NomadAPIConfig() (*nomadapi.Config, error) {
	apiConfig := nomadapi.DefaultConfig()

	scheme, port := "http", 4646
	port = n.NomadCommand.Config().Ports.HTTP
	if port <= 0 {
		port = 4646
	}

	ports := n.container.NetworkSettings.NetworkSettingsBase.Ports[nat.Port(fmt.Sprintf("%d/tcp", port))]
	if len(ports) == 0 {
		return nil, fmt.Errorf("no binding for Nomad API port")
	}

	if ca := n.NomadCommand.Config().TLS.CA; len(ca) > 0 {
		scheme = "https"
		apiConfig.TLSConfig.CACert = filepath.Join(n.NomadCommand.Config().ConfigDir, "ca.pem")
	}

	apiConfig.Address = fmt.Sprintf("%s://%s:%s", scheme, "127.0.0.1", ports[0].HostPort)
	return apiConfig, nil
}

var _ NomadRunner = (*NomadDockerRunner)(nil)

func NewNomadDockerRunner(api *client.Client, image, ip string, command NomadCommand) (*NomadDockerRunner, error) {
	return &NomadDockerRunner{
		DockerAPI:    api,
		NomadCommand: command,
		Image:        image,
		IP:           ip,
	}, nil
}

func (n *NomadDockerRunner) Start(ctx context.Context) (net.IP, error) {
	if n.container != nil {
		return nil, fmt.Errorf("already running")
	}

	nomadConfig := n.NomadCommand.Config()
	for name, contents := range nomadConfig.Files() {
		if err := writeConfig(nomadConfig.ConfigDir, name, contents); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(nomadConfig.DataDir, 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(nomadConfig.LogConfig.LogDir, 0700); err != nil {
		return nil, err
	}

	localConfigDir, localDataDir, localLogDir := nomadConfig.ConfigDir, nomadConfig.DataDir, nomadConfig.LogConfig.LogDir

	dr := &docker.Runner{
		DockerAPI: n.DockerAPI,
		NetName:   nomadConfig.NetworkConfig.DockerNetName,
		ContainerConfig: &container.Config{
			Image: n.Image,
			Cmd:   n.NomadCommand.WithDirs("/nomad/config", "/nomad/data", "/nomad/log").Command(),
			Labels: map[string]string{
				"yurt": "true",
			},
			WorkingDir: "/nomad/config",
		},
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: localConfigDir,
				Target: "/nomad/config",
				// No equivalent to CONSUL_DISABLE_PERM_MGMT, so if we make readonly chown in entrypoint will fail
				//ReadOnly: true,
			},
			{
				Type:   mount.TypeBind,
				Source: localDataDir,
				Target: "/nomad/data",
			},
			{
				Type:   mount.TypeBind,
				Source: localLogDir,
				Target: "/nomad/log",
			},
		},
		ContainerName: nomadConfig.NodeName,
		IP:            n.IP,
		AutoRemove:    true,
	}

	ctx, cancel := context.WithCancel(ctx)
	cont, err := dr.Start(ctx)
	if err != nil {
		return nil, err
	}
	n.container = cont
	n.cancel = cancel
	//go func() {
	//	_ = containerLogs(ctx, n.DockerAPI, *n.container)
	//}()
	go func() {
		<-ctx.Done()
		_ = docker.CleanupContainer(context.Background(), n.DockerAPI, cont.ID)
	}()
	return docker.ContainerIP(*cont, nomadConfig.NetworkConfig.DockerNetName)
}

func (n NomadDockerRunner) Wait() error {
	return docker.DockerWait(n.DockerAPI, n.container.ID)
}

func (n NomadDockerRunner) Stop() error {
	n.cancel()
	return nil
}

type NomadDockerBuilder struct {
	DockerAPI *client.Client
	Image     string
}

var _ NomadRunnerBuilder = (*NomadDockerBuilder)(nil)

func (c *NomadDockerBuilder) MakeNomadRunner(command NomadCommand) (NomadRunner, error) {
	return NewNomadDockerRunner(c.DockerAPI, c.Image, "", command)
}

type NomadDockerServerBuilder struct {
	DockerAPI *client.Client
	Image     string
	IPs       []string
	i         atomic.Uint32
}

var _ NomadRunnerBuilder = (*NomadDockerServerBuilder)(nil)

func (c *NomadDockerServerBuilder) MakeNomadRunner(command NomadCommand) (NomadRunner, error) {
	ip := c.IPs[c.i.Inc()-1]
	return NewNomadDockerRunner(c.DockerAPI, c.Image, ip, command)
}

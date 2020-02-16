package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/hashicorp/consul/api"
	"github.com/ncabatoff/yurt/docker"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/util"
	"go.uber.org/atomic"
)

type ConsulDockerRunner struct {
	ConsulCommand runner.ConsulCommand
	Image         string
	IP            string
	DockerAPI     *client.Client
	container     *types.ContainerJSON
	cancel        func()
}

var _ runner.ConsulRunner = (*ConsulDockerRunner)(nil)

func NewConsulDockerRunner(api *client.Client, image, ip string, command runner.ConsulCommand) (*ConsulDockerRunner, error) {
	return &ConsulDockerRunner{
		DockerAPI:     api,
		ConsulCommand: command,
		Image:         image,
		IP:            ip,
	}, nil
}

func (c *ConsulDockerRunner) Config() runner.ConsulConfig {
	return c.ConsulCommand.Config()
}

func (c *ConsulDockerRunner) Start(ctx context.Context) (string, error) {
	if c.container != nil {
		return "", fmt.Errorf("already running")
	}

	consulConfig := c.ConsulCommand.Config()
	localConfigDir, localDataDir, localLogDir := consulConfig.ConfigDir, consulConfig.DataDir, consulConfig.LogConfig.LogDir
	for _, dir := range []string{localConfigDir, localDataDir, localLogDir} {
		if dir == "" {
			continue
		}
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return "", err
		}
	}
	for name, contents := range consulConfig.Files() {
		if err := util.WriteConfig(consulConfig.ConfigDir, name, contents); err != nil {
			return "", err
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
	}

	ctx, cancel := context.WithCancel(ctx)
	cont, err := dr.Start(ctx)
	if err != nil {
		return "", err
	}
	c.container = cont
	c.cancel = cancel
	//go func() {
	//	_ = docker.ContainerLogs(ctx, c.DockerAPI, cont.ID)
	//}()
	return docker.ContainerIP(*cont, consulConfig.NetworkConfig.DockerNetName)
}

func (c ConsulDockerRunner) Wait() error {
	return docker.Wait(c.DockerAPI, c.container.ID)
}

func (c ConsulDockerRunner) Stop() error {
	c.cancel()
	return nil
}

func (c *ConsulDockerRunner) ConsulAPI() (*api.Client, error) {
	apiCfg, err := c.ConsulAPIConfig()
	if err != nil {
		return nil, err
	}
	return api.NewClient(apiCfg)
}

func (c *ConsulDockerRunner) ConsulAPIConfig() (*api.Config, error) {
	cfg := c.ConsulCommand.Config()
	apiConfig := api.DefaultNonPooledConfig()

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

var _ runner.ConsulRunnerBuilder = (*ConsulDockerBuilder)(nil)

func (c *ConsulDockerBuilder) MakeConsulRunner(command runner.ConsulCommand) (runner.ConsulRunner, error) {
	return NewConsulDockerRunner(c.DockerAPI, c.Image, c.IP, command)
}

type ConsulDockerServerBuilder struct {
	DockerAPI *client.Client
	Image     string
	IPs       []string
	i         atomic.Uint32
}

var _ runner.ConsulRunnerBuilder = (*ConsulDockerServerBuilder)(nil)

func (c *ConsulDockerServerBuilder) MakeConsulRunner(command runner.ConsulCommand) (runner.ConsulRunner, error) {
	ip := c.IPs[c.i.Inc()-1]
	return NewConsulDockerRunner(c.DockerAPI, c.Image, ip, command)
}

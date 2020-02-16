package docker

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt/docker"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/util"
	"go.uber.org/atomic"
	"os"
	"path/filepath"
)

type NomadDockerRunner struct {
	NomadCommand runner.NomadCommand
	Image        string
	IP           string
	DockerAPI    *client.Client
	container    *types.ContainerJSON
	cancel       func()
}

func (n *NomadDockerRunner) NomadAPI() (*api.Client, error) {
	apiCfg, err := n.NomadAPIConfig()
	if err != nil {
		return nil, err
	}
	return api.NewClient(apiCfg)
}

func (n *NomadDockerRunner) NomadAPIConfig() (*api.Config, error) {
	apiConfig := api.DefaultConfig()

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

var _ runner.NomadRunner = (*NomadDockerRunner)(nil)

func NewNomadDockerRunner(api *client.Client, image, ip string, command runner.NomadCommand) (*NomadDockerRunner, error) {
	return &NomadDockerRunner{
		DockerAPI:    api,
		NomadCommand: command,
		Image:        image,
		IP:           ip,
	}, nil
}

func (n *NomadDockerRunner) Start(ctx context.Context) (string, error) {
	if n.container != nil {
		return "", fmt.Errorf("already running")
	}

	nomadConfig := n.NomadCommand.Config()
	for name, contents := range nomadConfig.Files() {
		if err := util.WriteConfig(nomadConfig.ConfigDir, name, contents); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(nomadConfig.DataDir, 0700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(nomadConfig.LogConfig.LogDir, 0700); err != nil {
		return "", err
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
	}

	ctx, cancel := context.WithCancel(ctx)
	cont, err := dr.Start(ctx)
	if err != nil {
		return "", err
	}
	n.container = cont
	n.cancel = cancel
	//go func() {
	//	_ = containerLogs(ctx, n.DockerAPI, *n.container)
	//}()
	return docker.ContainerIP(*cont, nomadConfig.NetworkConfig.DockerNetName)
}

func (n NomadDockerRunner) Wait() error {
	return docker.Wait(n.DockerAPI, n.container.ID)
}

func (n NomadDockerRunner) Stop() error {
	n.cancel()
	return nil
}

type NomadDockerBuilder struct {
	DockerAPI *client.Client
	Image     string
}

var _ runner.NomadRunnerBuilder = (*NomadDockerBuilder)(nil)

func (c *NomadDockerBuilder) MakeNomadRunner(command runner.NomadCommand) (runner.NomadRunner, error) {
	return NewNomadDockerRunner(c.DockerAPI, c.Image, "", command)
}

type NomadDockerServerBuilder struct {
	DockerAPI *client.Client
	Image     string
	IPs       []string
	i         atomic.Uint32
}

var _ runner.NomadRunnerBuilder = (*NomadDockerServerBuilder)(nil)

func (c *NomadDockerServerBuilder) MakeNomadRunner(command runner.NomadCommand) (runner.NomadRunner, error) {
	ip := c.IPs[c.i.Inc()-1]
	return NewNomadDockerRunner(c.DockerAPI, c.Image, ip, command)
}

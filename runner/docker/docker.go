package docker

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/ncabatoff/yurt/docker"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/util"
	"net/url"
	"os"
	"path/filepath"
)

type DockerRunner struct {
	runner.Command
	Image     string
	IP        string
	DockerAPI *client.Client
	container *types.ContainerJSON
	cancel    func()
}

var _ runner.Runner = (*DockerRunner)(nil)

func NewDockerRunner(api *client.Client, image, ip string, command runner.Command) (*DockerRunner, error) {
	return &DockerRunner{
		DockerAPI: api,
		Command:   command,
		Image:     image,
		IP:        ip,
	}, nil
}

func (d *DockerRunner) Start(ctx context.Context) (string, error) {
	if d.container != nil {
		return "", fmt.Errorf("already running")
	}

	for _, dir := range []string{d.Config().DataDir, d.Config().LogDir} {
		if dir != "" {
			if err := os.MkdirAll(dir, 0777); err != nil {
				return "", err
			}
		}
	}
	for name, contents := range d.Files() {
		if err := util.WriteConfig(d.Config().ConfigDir, name, contents); err != nil {
			return "", err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	cont, err := docker.Start(ctx, d.DockerAPI, docker.RunOptions{
		NetName: d.Config().NetworkConfig.DockerNetName,
		ContainerConfig: &container.Config{
			Image: d.Image,
			// Since we don't know what container is running and we don't want
			// to conflict with other directories in the image, use project
			// name as base dir.
			Cmd: d.Command.WithDirs("/yurt/config", "/yurt/data", "/yurt/log").Args(),
			Labels: map[string]string{
				"yurt": "true",
			},
			WorkingDir: "/yurt/config",
		},
		Mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: d.Command.Config().ConfigDir,
				Target: "/yurt/config",
				// Nomad has no equivalent to CONSUL_DISABLE_PERM_MGMT, so if we make readonly chown
				// in entrypoint will fail
				//ReadOnly: true,
			},
			{
				Type:   mount.TypeBind,
				Source: d.Command.Config().DataDir,
				Target: "/yurt/data",
			},
			{
				Type:   mount.TypeBind,
				Source: d.Command.Config().LogDir,
				Target: "/yurt/log",
			},
		},
		ContainerName: d.Config().NodeName,
		IP:            d.IP,
	})
	if err != nil {
		return "", err
	}
	d.container = cont
	d.cancel = cancel
	//go func() {
	//	_ = containerLogs(ctx, d.DockerAPI, *d.container)
	//}()
	return docker.ContainerIP(*cont, d.Config().NetworkConfig.DockerNetName)
}

func (d *DockerRunner) APIConfig() (*runner.APIConfig, error) {
	apiConfig := runner.APIConfig{Address: url.URL{Scheme: "http"}}

	cfg := d.Config()
	if cfg.APIPort == 0 {
		return nil, fmt.Errorf("no API port defined in config")
	}

	if len(cfg.TLS.Cert) > 0 {
		apiConfig.Address.Scheme = "https"
		apiConfig.CAFile = filepath.Join(cfg.ConfigDir, "ca.pem")
	}

	ports := d.container.NetworkSettings.NetworkSettingsBase.Ports[nat.Port(fmt.Sprintf("%d/tcp", cfg.APIPort))]
	if len(ports) == 0 {
		return nil, fmt.Errorf("no binding for API port")
	}

	apiConfig.Address.Host = fmt.Sprintf("%s:%s", "127.0.0.1", ports[0].HostPort)

	return &apiConfig, nil
}

func (d DockerRunner) Wait() error {
	return docker.Wait(d.DockerAPI, d.container.ID)
}

func (d DockerRunner) Stop() error {
	d.cancel()
	return nil
}

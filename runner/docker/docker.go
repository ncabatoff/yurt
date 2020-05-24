package docker

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/hashicorp/vault/sdk/helper/certutil"
	"github.com/ncabatoff/yurt/docker"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/util"
)

type DockerRunner struct {
	command   runner.Command
	Image     string
	IP        string
	DockerAPI *client.Client
	container *types.ContainerJSON
	cancel    func()
}

var _ runner.Runner = (*DockerRunner)(nil)

// NewDockerRunner creates a Docker-based runner for the given command.  If ip
// is nonempty, it will be assigned as a static IP.  The command should specify
// a docker network that already exists to be used for communication with other
// docker-based runners.
func NewDockerRunner(api *client.Client, image, ip string, command runner.Command) (*DockerRunner, error) {
	if command.Config().TLS.Cert != "" {
		b, err := certutil.ParsePEMBundle(command.Config().TLS.Cert)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "Cert has CN=%s, IP SANS=%s\n", b.Certificate.Subject.CommonName, b.Certificate.IPAddresses)
	}
	return &DockerRunner{
		DockerAPI: api,
		command:   command,
		Image:     image,
		IP:        ip,
	}, nil
}

func (d *DockerRunner) Command() runner.Command {
	return d.command
}

// Start a new docker container based on the runner config.  Any existing container
// with the same name will be removed first.  Return IP of new container or error.
func (d *DockerRunner) Start(ctx context.Context) (string, error) {
	if d.container != nil {
		return "", fmt.Errorf("already running")
	}

	cfg := d.command.Config()
	matches, err := d.DockerAPI.ContainerList(ctx, types.ContainerListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "yurt"),
			filters.Arg("name", cfg.NodeName),
		),
	})
	if err != nil {
		return "", err

	}
	for _, cont := range matches {
		err = d.DockerAPI.ContainerRemove(ctx, cont.ID, types.ContainerRemoveOptions{Force: true})
		if err != nil {
			return "", err
		}
	}

	fromDirs := map[string]string{
		"config": cfg.ConfigDir,
		"data":   cfg.DataDir,
		"log":    cfg.LogDir,
	}
	copyFromTo := map[string]string{}
	for dir, from := range fromDirs {
		if from == "" {
			continue
		}
		if err := os.MkdirAll(from, 0777); err != nil {
			return "", err
		}
		copyFromTo[from] = "/yurt/" + dir
	}
	for name, contents := range d.command.Files() {
		if err := util.WriteConfig(cfg.ConfigDir, name, contents); err != nil {
			return "", err
		}
	}

	portset := nat.PortSet{}
	for _, p := range cfg.Ports {
		portset[nat.Port(p)] = struct{}{}
	}
	ctx, cancel := context.WithCancel(ctx)
	cont, err := docker.Start(ctx, d.DockerAPI, docker.RunOptions{
		NetName: cfg.NetworkConfig.DockerNetName,
		ContainerConfig: &container.Config{
			Image: d.Image,
			// Since we don't know what container is running and we don't want
			// to conflict with other directories in the image, use project
			// name as base dir.
			Cmd: d.command.WithDirs("/yurt/config", "/yurt/data", "/yurt/log").Args(),
			Env: d.command.Env(),
			Labels: map[string]string{
				"yurt": "true",
			},
			WorkingDir:   "/yurt/config",
			ExposedPorts: portset,
		},
		CopyFromTo:    copyFromTo,
		ContainerName: cfg.NodeName,
		IP:            d.IP,
	})
	log.Println(d.command.Config(), d.command.Args(), cont.ID, err, cont)
	if err != nil {
		log.Println(err)
		return "", err
	}
	d.container = cont
	d.cancel = cancel
	return docker.ContainerIP(*cont, cfg.NetworkConfig.DockerNetName)
}

func (d *DockerRunner) APIConfig() (*runner.APIConfig, error) {
	apiConfig := runner.APIConfig{Address: url.URL{Scheme: "http"}}

	cfg := d.command.Config()
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

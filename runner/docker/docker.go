package docker

import (
	"context"
	"fmt"
	"log"
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
	config    runner.Config
	NodeDir   string
	Image     string
	IP        string
	DockerAPI *client.Client
	binary    string
}

type harness struct {
	cancel    func()
	container *types.ContainerJSON
	dockerAPI *client.Client
	ip        string
	config    runner.Config
}

var _ runner.Harness = &harness{}

// NewDockerRunner creates a Docker-based runner for the given command.  If ip
// is nonempty, it will be assigned as a static IP.  The command should specify
// a docker network that already exists to be used for communication with other
// docker-based runners.
func NewDockerRunner(binary, nodeDir string, api *client.Client, image, ip string, command runner.Command, config runner.Config) (*DockerRunner, error) {
	if config.TLS.Cert != "" {
		b, err := certutil.ParsePEMBundle(config.TLS.Cert)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "Cert has CN=%s, IP SANS=%s\n", b.Certificate.Subject.CommonName, b.Certificate.IPAddresses)
	}
	return &DockerRunner{
		DockerAPI: api,
		config:    config,
		NodeDir:   nodeDir,
		binary:    binary,
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
func (d *DockerRunner) Start(ctx context.Context) (*harness, error) {
	// Clean up any existing container whose name we want to use
	{
		matches, err := d.DockerAPI.ContainerList(ctx, types.ContainerListOptions{
			All: true,
			Filters: filters.NewArgs(
				filters.Arg("label", "yurt"),
				filters.Arg("name", d.config.NodeName),
			),
		})
		if err != nil {
			return nil, err

		}
		for _, cont := range matches {
			err = d.DockerAPI.ContainerRemove(ctx, cont.ID, types.ContainerRemoveOptions{Force: true})
			if err != nil {
				return nil, err
			}
		}
	}

	adjConfig := d.config
	cfgDir := filepath.Join(d.NodeDir, "config")
	copyFromTo := map[string]string{
		cfgDir:                           adjConfig.ConfigDir,
		filepath.Join(d.NodeDir, "data"): adjConfig.DataDir,
		filepath.Join(d.NodeDir, "log"):  adjConfig.LogDir,
	}
	for from := range copyFromTo {
		if err := os.MkdirAll(from, 0777); err != nil {
			return nil, err
		}
	}
	if d.binary != "" {
		copyFromTo[d.binary] = filepath.Join("/bin", filepath.Base(d.binary))
	}

	command := d.command.WithConfig(adjConfig)
	adjConfig = command.Config()

	for name, contents := range command.Files() {
		if err := util.WriteConfig(cfgDir, name, contents); err != nil {
			return nil, err
		}
	}

	portset := nat.PortSet{}
	for _, p := range adjConfig.Ports.AsList() {
		portset[nat.Port(p)] = struct{}{}
	}
	ctx, cancel := context.WithCancel(ctx)
	args := command.Args()
	if len(args) > 1 && args[1] == "-config=/vault/config" {
		// Yuck.  This is because the docker-vault entrypoint insists on adding
		// its own arg like this, and vault itself reads the same config twice,
		// tries to bind to the same listener address twice, then fails.
		args = append(args[:1], args[2:]...)
	}
	contConfig := container.Config{
		Image: d.Image,
		Cmd:   args,
		Env:   command.Env(),
		Labels: map[string]string{
			"yurt": "true",
		},
		//WorkingDir:   adjConfig.ConfigDir,
		ExposedPorts: portset,
		Entrypoint:   []string{"/bin/sh", "-x", "/usr/local/bin/docker-entrypoint.sh"},
	}
	cont, err := docker.Start(ctx, d.DockerAPI, docker.RunOptions{
		NetName:         adjConfig.NetworkConfig.DockerNetName,
		ContainerConfig: &contConfig,
		CopyFromTo:      copyFromTo,
		ContainerName:   d.config.NodeName,
		IP:              d.IP,
	})
	id := ""
	if cont != nil {
		id = cont.ID
	}
	log.Printf("docker.Start: id=%v err=%v args=%v config=%#v contConfig=%#v",
		id, err, command.Args(), adjConfig, contConfig)
	if err != nil {
		cancel()
		log.Println(err)
		return nil, err
	}
	ip, err := docker.ContainerIP(*cont, adjConfig.NetworkConfig.DockerNetName)
	if err != nil {
		cancel()
		return nil, err
	}
	return &harness{
		cancel:    cancel,
		config:    d.config,
		container: cont,
		dockerAPI: d.DockerAPI,
		ip:        ip,
	}, nil
}

func (d *harness) Endpoint(name string, local bool) (*runner.APIConfig, error) {
	port := d.config.Ports.ByName[name]
	if port.Number == 0 {
		return nil, fmt.Errorf("no port %q defined in config", name)
	}

	var apiConfig runner.APIConfig
	if len(d.config.TLS.Cert) > 0 {
		if name == "http" {
			name = "https"
		}
	}
	apiConfig.Address.Scheme = name

	if local {
		portWithNetwork := port.AsList()[0] // assume TCP; should we make UDP an option?
		ports := d.container.NetworkSettings.NetworkSettingsBase.Ports[nat.Port(portWithNetwork)]
		if len(ports) == 0 {
			return nil, fmt.Errorf("no binding for API port")
		}
		apiConfig.Address.Host = fmt.Sprintf("%s:%s", "127.0.0.1", ports[0].HostPort)
		if name == "https" {
			apiConfig.CAFile = filepath.Join(d.config.ConfigDir, "ca.pem")
		}
	} else {
		apiConfig.Address.Host = fmt.Sprintf("%s:%d", d.ip, port.Number)
		if name == "https" {
			apiConfig.CAFile = filepath.Join(d.config.ConfigDir, "ca.pem")
		}
	}

	return &apiConfig, nil
}

func (d *harness) Wait() error {
	return docker.Wait(d.dockerAPI, d.container.ID)
}

func (d *harness) Stop() error {
	d.cancel()
	return nil
}

func (d *harness) Kill() {
	d.cancel()
}

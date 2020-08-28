package cluster

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	dockerapi "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/hashicorp/go-multierror"
	"github.com/ncabatoff/yurt/binaries"
	"github.com/ncabatoff/yurt/docker"
)

type YurtRunClusterOptions struct {
	// Network is the network the docker containers will run in, which the caller
	// must ensure exists.
	Network yurt.NetworkConfig
	// ConsulServerIPs are the IPs to use for the server nodes.  For consistency
	// with other cluster styles in this project, it is named "Consul"ServerIPs,
	// but the Nomad servers run on these IPs as well.
	ConsulServerIPs []string
	// BaseImage is the docker image to use as a starting point.  No binaries
	// from the image will be run, but we do need glibc for Nomad.
	BaseImage string
	// WorkDir is where all files are created, excluding binaries.
	WorkDir string
}

// YurtRunCluster is used for testing yurt-run.
type YurtRunCluster struct {
	YurtRunClusterOptions
	docker     *dockerapi.Client
	containers []types.ContainerJSON
}

func NewYurtRunCluster(options YurtRunClusterOptions, cli *dockerapi.Client) (*YurtRunCluster, error) {
	return &YurtRunCluster{
		YurtRunClusterOptions: options,
		docker:                cli,
	}, nil
}

// Start launches the cluster server nodes.
func (y *YurtRunCluster) Start(ctx context.Context) error {
	for i, ip := range y.ConsulServerIPs {
		if err := y.startNode(ctx, i, ip); err != nil {
			return err
		}
	}
	return nil
}

func (y *YurtRunCluster) Stop(ctx context.Context) error {
	var errs multierror.Error
	for _, cont := range y.containers {
		if err := docker.CleanupContainer(ctx, y.docker, cont.ID); err != nil {
			errs.Errors = append(errs.Errors, err)
		}
	}
	return errs.ErrorOrNil()
}

func (y *YurtRunCluster) startNode(ctx context.Context, node int, ip string) error {
	nodeName := fmt.Sprintf("yurt%d", node+1)
	nodeDir := filepath.Join(y.WorkDir, nodeName)
	err := os.MkdirAll(nodeDir, 0755)
	if err != nil {
		return err
	}

	exposedPorts := nat.PortSet{
		"4646/tcp": {},
		"4647/tcp": {},
		"4648/tcp": {},
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

	copyFromTo := map[string]string{
		nodeDir: "/var/yurt",
	}
	for _, name := range []string{"yurt-run", "consul", "nomad"} {
		bin, err := binaries.Default.GetOSArch(name, "linux", "amd64", "")
		if err != nil {
			return err
		}
		copyFromTo[bin] = filepath.Join("/bin", name)
	}

	cont, err := docker.Start(ctx, y.docker, docker.RunOptions{
		ContainerName: nodeName,
		IP:            ip,
		NetName:       y.Network.DockerNetName,
		CopyFromTo:    copyFromTo,
		ContainerConfig: &container.Config{
			Image:      y.BaseImage,
			Entrypoint: []string{"/bin/yurt-run"},
			Cmd: []string{
				"-consul-server-ips=" + strings.Join(y.ConsulServerIPs, ","),
				"-consul-bin=/bin/consul",
				"-nomad-bin=/bin/nomad",
			},
			Labels: map[string]string{
				"yurt": "true",
			},
			ExposedPorts: exposedPorts,
			WorkingDir:   "/consul/config",
		},
	})
	if err != nil {
		return err
	}

	y.containers = append(y.containers, *cont)
	return nil
}

/*
func (y *YurtRunCluster) ConsulAPIs() ([]*consulapi.Client, error) {
	var ret []*consulapi.Client
	for i := range y.ConsulServerIPs {
		apiConfig := consulapi.DefaultNonPooledConfig()
		guestport := runner.DefConsulPorts().HTTP
		ports := y.containers[i].NetworkSettings.NetworkSettingsBase.Ports[nat.Port(fmt.Sprintf("%d/tcp", guestport))]
		if len(ports) == 0 {
			return nil, fmt.Errorf("no binding for Consul API port %d", guestport)
		}
		apiConfig.Address = fmt.Sprintf("%s:%s", "127.0.0.1", ports[0].HostPort)
		client, err := consulapi.NewClient(apiConfig)
		if err != nil {
			return nil, err
		}
		ret = append(ret, client)
	}

	return ret, nil
}

func (y *YurtRunCluster) NomadAPIs() ([]*nomadapi.Client, error) {
	var ret []*nomadapi.Client
	for i := range y.ConsulServerIPs {
		apiConfig := nomadapi.DefaultConfig()
		guestport := runner.DefNomadPorts().HTTP
		ports := y.containers[i].NetworkSettings.NetworkSettingsBase.Ports[nat.Port(fmt.Sprintf("%d/tcp", guestport))]
		if len(ports) == 0 {
			return nil, fmt.Errorf("no binding for Nomad API port %d", guestport)
		}
		apiConfig.Address = fmt.Sprintf("%s://%s:%s", "http", "127.0.0.1", ports[0].HostPort)
		client, err := nomadapi.NewClient(apiConfig)
		if err != nil {
			return nil, err
		}
		ret = append(ret, client)
	}

	return ret, nil
}
*/

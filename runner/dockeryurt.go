package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	dockerapi "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt/packages"
)

type YurtRunClusterOptions struct {
	Network         NetworkConfig
	ConsulServerIPs []string
	BaseImage       string
	YurtRunBin      string
	WorkDir         string
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

func (y *YurtRunCluster) Start(ctx context.Context) error {
	err := y.installBinDir()
	if err != nil {
		return err
	}
	for i, ip := range y.ConsulServerIPs {
		if err := y.startNode(ctx, i, ip); err != nil {
			return err
		}
	}
	return nil
}

func (y *YurtRunCluster) startNode(ctx context.Context, node int, ip string) error {
	nodeName := fmt.Sprintf("yurt%d", node+1)
	nodeDir := filepath.Join(y.WorkDir, nodeName)
	err := os.MkdirAll(nodeDir, 0755)
	if err != nil {
		return err
	}
	binDir, err := filepath.Abs("binaries")
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
	dr := &dockerRunner{
		DockerAPI: y.docker,
		netName:   y.Network.DockerNetName,
		cfg: &container.Config{
			Image:      y.BaseImage,
			Entrypoint: []string{"/opt/yurt/bin/yurt-run"},
			Cmd: []string{
				"-consul-server-ips=" + strings.Join(y.ConsulServerIPs, ","),
				"-consul-bin=/opt/yurt/bin/consul",
				"-nomad-bin=/opt/yurt/bin/nomad",
			},
			Labels: map[string]string{
				"yurt": "true",
			},
			ExposedPorts: exposedPorts,
			WorkingDir:   "/consul/config",
		},
		mounts: []mount.Mount{
			{
				Type:   mount.TypeBind,
				Source: nodeDir,
				Target: "/var/yurt",
			},
			{
				Type:     mount.TypeBind,
				Source:   binDir,
				Target:   "/opt/yurt/bin",
				ReadOnly: true,
			},
		},
		containerName: nodeName,
		ip:            ip,
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cont, err := dr.start(ctx)
	if err != nil {
		return err
	}

	y.containers = append(y.containers, *cont)
	return nil
}

func (y *YurtRunCluster) installBinDir() error {
	for _, p := range []string{"consul", "nomad"} {
		bin, err := packages.GetBinary(p, "linux", "amd64", "binaries")
		if err != nil {
			return err
		}
		err = os.Link(bin, filepath.Join("binaries", filepath.Base(bin)))
		if err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return nil
}

func (y *YurtRunCluster) ConsulAPIs() ([]*consulapi.Client, error) {
	var ret []*consulapi.Client
	for i := range y.ConsulServerIPs {
		apiConfig := consulapi.DefaultNonPooledConfig()
		guestport := DefConsulPorts(false).HTTP
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
		guestport := DefNomadPorts().HTTP
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

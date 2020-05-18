package cluster

import (
	"context"
	"errors"
	"fmt"
	"github.com/ncabatoff/yurt/util"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	dockerapi "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-multierror"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt/docker"
	"github.com/ncabatoff/yurt/packages"
	"github.com/ncabatoff/yurt/runner"
)

type YurtRunClusterOptions struct {
	// Network is the network the docker containers will run in, which the caller
	// must ensure exists.
	Network util.NetworkConfig
	// ConsulServerIPs are the IPs to use for the server nodes.  For consistency
	// with other cluster styles in this project, it is named "Consul"ServerIPs,
	// but the Nomad servers run on these IPs as well.
	ConsulServerIPs []string
	// BaseImage is the docker image to use as a starting point.  No binaries
	// from the image will be run, but we do need glibc for Nomad.
	BaseImage string
	// YurtRunBin is where yurt-run can be found.
	YurtRunBin string
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

func (y *YurtRunCluster) Stop(ctx context.Context) error {
	var errs multierror.Error
	for _, cont := range y.containers {
		if err := docker.CleanupContainer(ctx, y.docker, cont.ID); err != nil {
			errs.Errors = append(errs.Errors, err)
		}
	}
	return errs.ErrorOrNil()
}

func (y *YurtRunCluster) installBinDir() error {
	fqfn, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("can't find 'go' in path: %v", err)
	}
	cmd := exec.Command(fqfn, "build", "-o", "../../runner/binaries/yurt-run")
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GOOS=") && !strings.HasPrefix(e, "CGO_ENABLED=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GOOS=linux", "GO111MODULE=on", "GOFLAGS=-mod=")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir, err = filepath.Abs("../cmd/yurt-run")
	if err != nil {
		return err
	}
	log.Print("running command:", cmd)

	err = cmd.Run()
	if err != nil {
		return err
	}

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

	cont, err := docker.Start(ctx, y.docker, docker.RunOptions{
		NetName: y.Network.DockerNetName,
		ContainerConfig: &container.Config{
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
		Mounts: []mount.Mount{
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
		ContainerName: nodeName,
		IP:            ip,
	})
	if err != nil {
		return err
	}

	y.containers = append(y.containers, *cont)
	return nil
}

// Cleanup removes the workdir contents.  If all is specified, everything
// should be cleaned up; otherwise only empty directories are removed.
func (y *YurtRunCluster) Cleanup(all bool) {
	for i := range y.ConsulServerIPs {
		nodeName := fmt.Sprintf("yurt%d", i+1)
		nodeDir := filepath.Join(y.WorkDir, nodeName)
		if all {
			_ = os.RemoveAll(nodeDir)
		} else {
			_ = os.Remove(nodeDir)
		}
	}
	_ = os.Remove(y.WorkDir)
}

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

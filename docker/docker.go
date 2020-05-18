package docker

import (
	"bytes"
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	dockerapi "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"io"
	"io/ioutil"
	"log"
)

func SetupNetwork(ctx context.Context, cli *dockerapi.Client, netName, cidr string) (string, error) {
	netResources, err := cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return "", err
	}
	for _, netRes := range netResources {
		if netRes.Name == netName {
			if len(netRes.IPAM.Config) > 0 && netRes.IPAM.Config[0].Subnet == cidr {
				return netRes.ID, nil
			}
			err = cli.NetworkRemove(ctx, netRes.ID)
			if err != nil {
				return "", err
			}
		}
	}

	id, err := createNetwork(ctx, cli, netName, cidr)
	if err != nil {
		return "", fmt.Errorf("couldn't create network %s on %s: %w", netName, cidr, err)
	}
	return id, nil
}

func createNetwork(ctx context.Context, cli *dockerapi.Client, netName, cidr string) (string, error) {
	resp, err := cli.NetworkCreate(ctx, netName, types.NetworkCreate{
		CheckDuplicate: true,
		Driver:         "bridge",
		Options:        map[string]string{},
		IPAM: &network.IPAM{
			Driver:  "default",
			Options: map[string]string{},
			Config: []network.IPAMConfig{
				{
					Subnet: cidr,
				},
			},
		},
	})
	if err != nil {
		return "", err
	}

	return resp.ID, nil
}

func Wait(api *dockerapi.Client, containerID string) error {
	chanWaitOK, chanErr := api.ContainerWait(context.Background(),
		containerID, container.WaitConditionNotRunning)
	select {
	case err := <-chanErr:
		return err
	case res := <-chanWaitOK:
		if res.StatusCode != 0 {
			return fmt.Errorf("container exited with %d", res.StatusCode)
		}
	}
	return nil
}

func CleanupContainer(ctx context.Context, cli *dockerapi.Client, containerID string) error {
	err := cli.ContainerStop(ctx, containerID, nil)
	if err != nil {
		return err
	}
	return cli.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{
		//RemoveLinks: true,
		Force: true,
	})
}

func ContainerIP(cont types.ContainerJSON, netName string) (string, error) {
	if cont.NetworkSettings.Networks[netName] == nil {
		return "", fmt.Errorf("missing private network")
	}
	return cont.NetworkSettings.Networks[netName].IPAddress, nil
}

type RunOptions struct {
	ContainerConfig *container.Config
	ContainerName   string
	NetName         string
	IP              string
	Privileged      bool
	Mounts          []mount.Mount
}

func Start(ctx context.Context, client *dockerapi.Client, opts RunOptions) (*types.ContainerJSON, error) {
	hostConfig := &container.HostConfig{
		PublishAllPorts: true,
		Mounts:          opts.Mounts,
		AutoRemove:      true,
	}

	networkingConfig := &network.NetworkingConfig{}
	switch opts.NetName {
	case "":
	case "host":
		hostConfig.NetworkMode = "host"
	default:
		es := &network.EndpointSettings{}
		if len(opts.IP) != 0 {
			es.IPAMConfig = &network.EndpointIPAMConfig{
				IPv4Address: opts.IP,
			}
		}
		networkingConfig.EndpointsConfig = map[string]*network.EndpointSettings{
			opts.NetName: es,
		}
	}

	// best-effort pull
	resp, _ := client.ImageCreate(ctx, opts.ContainerConfig.Image, types.ImageCreateOptions{})
	if resp != nil {
		_, _ = ioutil.ReadAll(resp)
	}

	cfg := *opts.ContainerConfig
	cfg.Hostname = opts.ContainerName
	fullName := opts.NetName + "." + opts.ContainerName
	consulContainer, err := client.ContainerCreate(ctx, &cfg, hostConfig, networkingConfig, fullName)
	if err != nil {
		return nil, fmt.Errorf("container create failed: %v", err)
	}

	err = client.ContainerStart(ctx, consulContainer.ID, types.ContainerStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("container start failed: %v", err)
	}

	inspect, err := client.ContainerInspect(ctx, consulContainer.ID)
	if err != nil {
		return nil, err
	}
	var buf = &bytes.Buffer{}
	go func() {
		_ = ContainerLogs(ctx, client, inspect.ID, buf)
	}()
	go func(ctx context.Context) {
		//log.Printf("waiting for context on %s", fullName)
		<-ctx.Done()
		if ctx.Err() == context.DeadlineExceeded {
			log.Print(buf.String())
		}
		//log.Printf("killing %s", fullName)
		_ = CleanupContainer(context.Background(), client, inspect.ID)
	}(ctx)
	return &inspect, nil
}

func ContainerLogs(ctx context.Context, cli *dockerapi.Client, id string, writer io.Writer) error {
	resp, err := cli.ContainerLogs(ctx, id, types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return err
	}

	_, err = stdcopy.StdCopy(writer, writer, resp)
	if err != nil {
		return err
	}
	return nil
}

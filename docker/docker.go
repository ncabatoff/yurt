package docker

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	dockerapi "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/archive"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/ncabatoff/yurt/util"
	"io"
	"io/ioutil"
	"log"
	"os"
)

// Create a docker private network or if one already exists with the name netName,
// use that one.
func SetupNetwork(ctx context.Context, cli *dockerapi.Client, netName, cidr string) (*types.NetworkResource, error) {
	netResources, err := cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return nil, err
	}
	for _, netRes := range netResources {
		if netRes.Name == netName {
			if len(netRes.IPAM.Config) > 0 {
				return &netRes, nil
			}
			err = cli.NetworkRemove(ctx, netRes.ID)
			if err != nil {
				return nil, err
			}
		}
	}

	id, err := createNetwork(ctx, cli, netName, cidr)
	if err != nil {
		return nil, fmt.Errorf("couldn't create network %s on %s: %w", netName, cidr, err)
	}
	netRes, err := cli.NetworkInspect(ctx, id, types.NetworkInspectOptions{})
	if err != nil {
		return nil, err
	}
	return &netRes, nil
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
	//log.Println("waiting for container", containerID)
	chanWaitOK, chanErr := api.ContainerWait(context.Background(),
		containerID, container.WaitConditionNotRunning)
	select {
	case err := <-chanErr:
		//log.Println("waiting for container error", containerID, err)
		return err
	case res := <-chanWaitOK:
		//log.Println("waiting for container exit", containerID, res.StatusCode)
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
	CopyFromTo      map[string]string
}

func Start(ctx context.Context, client *dockerapi.Client, opts RunOptions) (*types.ContainerJSON, error) {
	hostConfig := &container.HostConfig{
		PublishAllPorts: true,
		AutoRemove:      false,
		//Privileged: true,
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
	container, err := client.ContainerCreate(ctx, &cfg, hostConfig, networkingConfig, opts.ContainerName)
	if err != nil {
		return nil, fmt.Errorf("container create failed: %v", err)
	}

	for from, to := range opts.CopyFromTo {
		if err := CopyToContainer(ctx, client, container.ID, from, to); err != nil {
			_ = client.ContainerRemove(ctx, container.ID, types.ContainerRemoveOptions{})
			return nil, err
		}
	}

	err = client.ContainerStart(ctx, container.ID, types.ContainerStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("container start failed: %v", err)
	}

	inspect, err := client.ContainerInspect(ctx, container.ID)
	if err != nil {
		return nil, err
	}
	go func() {
		err = ContainerLogs(ctx, client, inspect.ID, util.NewOutputWriter(opts.ContainerName, os.Stdout))
		if err != nil {
			log.Printf("error getting container logs for %s: %v", opts.ContainerName, err)
		}
	}()
	go func(ctx context.Context) {
		//log.Printf("waiting for context on %s", fullName)
		<-ctx.Done()
		log.Printf("context done for container %s, err: %v", opts.ContainerName, ctx.Err())
		//log.Printf("killing %s", opts.ContainerName)
		//_ = CleanupContainer(context.Background(), client, inspect.ID)
		client.ContainerStop(context.Background(), container.ID, nil)
	}(ctx)
	return &inspect, nil
}

func CopyToContainer(ctx context.Context, client *dockerapi.Client, containerID, from, to string) error {
	srcInfo, err := archive.CopyInfoSourcePath(from, false)
	if err != nil {
		return fmt.Errorf("error copying from source %q: %v", from, err)
	}

	srcArchive, err := archive.TarResource(srcInfo)
	if err != nil {
		return fmt.Errorf("error creating tar from source %q: %v", from, err)
	}
	defer srcArchive.Close()

	dstInfo := archive.CopyInfo{Path: to}

	dstDir, content, err := archive.PrepareArchiveCopy(srcArchive, srcInfo, dstInfo)
	if err != nil {
		return fmt.Errorf("error preparing copy from %q -> %q: %v", from, to, err)
	}
	defer content.Close()
	err = client.CopyToContainer(ctx, containerID, dstDir, content, types.CopyToContainerOptions{})
	if err != nil {
		return fmt.Errorf("error copying from %q -> %q: %v", from, to, err)
	}

	return nil
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

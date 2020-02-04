package runner

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"go.uber.org/atomic"
	"io/ioutil"
	"net"
	"os"
)

type ConsulDockerRunner struct {
	ConsulCommand ConsulCommand
	Image         string
	IP            net.IP
	DockerAPI     *client.Client
	container     *types.ContainerJSON
	cancel        func()
}

var _ ConsulRunner = (*ConsulDockerRunner)(nil)

func NewConsulDockerRunner(api *client.Client, image, ip string, command ConsulCommand) (*ConsulDockerRunner, error) {
	netip := net.ParseIP(ip)
	if ip != "" && netip == nil {
		return nil, fmt.Errorf("bad ip %q", ip)
	}
	return &ConsulDockerRunner{
		DockerAPI:     api,
		ConsulCommand: command,
		Image:         image,
		IP:            netip,
	}, nil
}

func (c *ConsulDockerRunner) Config() ConsulConfig {
	return c.ConsulCommand.Config()
}

func (c *ConsulDockerRunner) Start(ctx context.Context) (net.IP, error) {
	// TODO handle output
	if c.container != nil {
		return nil, fmt.Errorf("already running")
	}

	consulConfig := c.ConsulCommand.Config()
	dr := &dockerRunner{
		DockerAPI: c.DockerAPI,
		netName:   consulConfig.NetworkConfig.DockerNetName,
		cfg: &container.Config{
			Image: c.Image,
			Cmd:   c.ConsulCommand.Command(),
			Env:   []string{"CONSUL_DISABLE_PERM_MGMT=1"},
			Labels: map[string]string{
				"yurt": "true",
			},
			//cmd.Stdout = NewOutputWriter(c.Config.NodeName, os.Stdout)
			//cmd.Stderr = NewOutputWriter(c.Config.NodeName, os.Stderr)
		},
		containerName: consulConfig.NodeName,
		ip:            c.IP,
	}

	ctx, cancel := context.WithCancel(ctx)
	cont, err := dr.start(ctx)
	if err != nil {
		return nil, err
	}
	c.container = cont
	c.cancel = cancel
	go func() {
		<-ctx.Done()
		_ = cleanupContainer(context.Background(), c.DockerAPI, cont.ID)
	}()
	return getIP(*cont, consulConfig.NetworkConfig.DockerNetName)
}

func (c ConsulDockerRunner) Wait() error {
	return dockerWait(c.DockerAPI, c.container.ID)
}

func (c ConsulDockerRunner) Stop() error {
	c.cancel()
	return nil
}

func (c *ConsulDockerRunner) ConsulAPI() (*consulapi.Client, error) {
	apiConfig := consulapi.DefaultNonPooledConfig()
	apiConfig.Scheme = "http"

	ports := c.container.NetworkSettings.NetworkSettingsBase.Ports["8500/tcp"]
	if len(ports) == 0 {
		return nil, fmt.Errorf("no binding for Consul API port")
	}

	apiConfig.Address = fmt.Sprintf("%s:%s", "127.0.0.1", ports[0].HostPort)
	return consulapi.NewClient(apiConfig)
}

func getIP(cont types.ContainerJSON, netName string) (net.IP, error) {
	if cont.NetworkSettings.Networks[netName] == nil {
		return nil, fmt.Errorf("missing private network")
	}
	ipString := cont.NetworkSettings.Networks[netName].IPAddress
	ip := net.ParseIP(ipString)
	if ip == nil {
		return nil, fmt.Errorf("parse ip %q failed", ipString)
	}
	return ip, nil
}

func (c *ConsulDockerRunner) AgentAddress() (string, error) {
	netName := c.ConsulCommand.Config().NetworkConfig.DockerNetName
	ip, err := getIP(*c.container, netName)
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

var _ ConsulRunnerBuilder = (*ConsulDockerBuilder)(nil)

func (c *ConsulDockerBuilder) MakeConsulRunner(command ConsulCommand) (ConsulRunner, error) {
	return NewConsulDockerRunner(c.DockerAPI, c.Image, c.IP, command)
}

type ConsulDockerServerBuilder struct {
	DockerAPI *client.Client
	Image     string
	IPs       []string
	i         atomic.Uint32
}

var _ ConsulRunnerBuilder = (*ConsulDockerServerBuilder)(nil)

func (c *ConsulDockerServerBuilder) MakeConsulRunner(command ConsulCommand) (ConsulRunner, error) {
	ip := c.IPs[c.i.Inc()-1]
	return NewConsulDockerRunner(c.DockerAPI, c.Image, ip, command)
}

type dockerRunner struct {
	DockerAPI     *client.Client
	cfg           *container.Config
	containerName string
	netName       string
	ip            net.IP
	privileged    bool
	mounts        []mount.Mount
}

func (d *dockerRunner) start(ctx context.Context) (*types.ContainerJSON, error) {
	hostConfig := &container.HostConfig{
		PublishAllPorts: true,
		Mounts:          d.mounts,
		AutoRemove:      true,
	}

	networkingConfig := &network.NetworkingConfig{}
	switch d.netName {
	case "":
	case "host":
		hostConfig.NetworkMode = "host"
	default:
		es := &network.EndpointSettings{}
		if len(d.ip) != 0 {
			es.IPAMConfig = &network.EndpointIPAMConfig{
				IPv4Address: d.ip.String(),
			}
		}
		networkingConfig.EndpointsConfig = map[string]*network.EndpointSettings{
			d.netName: es,
		}
	}

	// best-effort pull
	resp, _ := d.DockerAPI.ImageCreate(ctx, d.cfg.Image, types.ImageCreateOptions{})
	if resp != nil {
		_, _ = ioutil.ReadAll(resp)
	}

	cfg := *d.cfg
	cfg.Hostname = d.containerName
	consulContainer, err := d.DockerAPI.ContainerCreate(ctx, &cfg, hostConfig, networkingConfig, d.netName+"."+d.containerName)
	if err != nil {
		return nil, fmt.Errorf("container create failed: %v", err)
	}

	err = d.DockerAPI.ContainerStart(ctx, consulContainer.ID, types.ContainerStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("consul container start failed: %v", err)
	}

	inspect, err := d.DockerAPI.ContainerInspect(ctx, consulContainer.ID)
	if err != nil {
		return nil, err
	}
	return &inspect, nil
}

func dockerWait(api *client.Client, containerID string) error {
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

func setupNetwork(ctx context.Context, cli *client.Client, netName, cidr string) (string, error) {
	netResources, err := cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return "", err
	}
	for _, netRes := range netResources {
		if netRes.Name == netName {
			if len(netRes.IPAM.Config) > 0 && netRes.IPAM.Config[0].Subnet == cidr {
				return netRes.ID, nil
			}
			_ = cli.NetworkRemove(ctx, netRes.ID)
		}
	}

	id, err := createNetwork(ctx, cli, netName, cidr)
	if err != nil {
		return "", err
	}
	return id, nil
}

func createNetwork(ctx context.Context, cli *client.Client, netName, cidr string) (string, error) {
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

func cleanupContainer(ctx context.Context, cli *client.Client, containerID string) error {
	err := cli.ContainerStop(ctx, containerID, nil)
	if err != nil {
		return err
	}
	return cli.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{
		RemoveLinks: true,
		Force:       true,
	})
}

type NomadDockerRunner struct {
	NomadCommand NomadCommand
	Image        string
	NetName      string
	IP           net.IP
	DockerAPI    *client.Client
	container    *types.ContainerJSON
	cancel       func()
}

func (n *NomadDockerRunner) Config() NomadConfig {
	return n.NomadCommand.Config()
}

func (n *NomadDockerRunner) NomadAPI() (*nomadapi.Client, error) {
	apiConfig := nomadapi.DefaultConfig()
	//apiConfig.Scheme = "http"

	ports := n.container.NetworkSettings.NetworkSettingsBase.Ports["4646/tcp"]
	if len(ports) == 0 {
		return nil, fmt.Errorf("no binding for Nomad API port")
	}

	apiConfig.Address = fmt.Sprintf("http://%s:%s", "127.0.0.1", ports[0].HostPort)
	return nomadapi.NewClient(apiConfig)
}

var _ NomadRunner = (*NomadDockerRunner)(nil)

func NewNomadDockerRunner(api *client.Client, image, ip string, command NomadCommand) (*NomadDockerRunner, error) {
	netip := net.ParseIP(ip)
	if ip != "" && netip == nil {
		return nil, fmt.Errorf("bad ip %q", ip)
	}
	return &NomadDockerRunner{
		DockerAPI:    api,
		NomadCommand: command,
		Image:        image,
		IP:           netip,
	}, nil
}

func (n *NomadDockerRunner) Start(ctx context.Context) (net.IP, error) {
	if n.container != nil {
		return nil, fmt.Errorf("already running")
	}

	nomadConfig := n.NomadCommand.Config()
	for name, contents := range nomadConfig.Files() {
		if err := writeConfig(nomadConfig.ConfigDir, name, contents); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(nomadConfig.DataDir, 0700); err != nil {
		return nil, err
	}

	localConfigDir, localDataDir := nomadConfig.ConfigDir, nomadConfig.DataDir

	dr := &dockerRunner{
		DockerAPI: n.DockerAPI,
		netName:   nomadConfig.NetworkConfig.DockerNetName,
		cfg: &container.Config{
			Image: n.Image,
			Cmd:   n.NomadCommand.WithDirs("/nomad/config", "/nomad/data", "").Command(),
			Labels: map[string]string{
				"yurt": "true",
			},
		},
		mounts: []mount.Mount{
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
		},
		containerName: nomadConfig.NodeName,
		ip:            n.IP,
	}

	ctx, cancel := context.WithCancel(ctx)
	cont, err := dr.start(ctx)
	if err != nil {
		return nil, err
	}
	n.container = cont
	n.cancel = cancel
	go func() {
		<-ctx.Done()
		_ = cleanupContainer(context.Background(), n.DockerAPI, cont.ID)
	}()
	return getIP(*cont, nomadConfig.NetworkConfig.DockerNetName)
}

func (n NomadDockerRunner) Wait() error {
	return dockerWait(n.DockerAPI, n.container.ID)
}

func (n NomadDockerRunner) Stop() error {
	n.cancel()
	return nil
}

type NomadDockerBuilder struct {
	DockerAPI *client.Client
	Image     string
}

var _ NomadRunnerBuilder = (*NomadDockerBuilder)(nil)

func (c *NomadDockerBuilder) MakeNomadRunner(command NomadCommand) (NomadRunner, error) {
	return NewNomadDockerRunner(c.DockerAPI, c.Image, "", command)
}

type NomadDockerServerBuilder struct {
	DockerAPI *client.Client
	Image     string
}

var _ NomadRunnerBuilder = (*NomadDockerServerBuilder)(nil)

func (c *NomadDockerServerBuilder) MakeNomadRunner(command NomadCommand) (NomadRunner, error) {
	return NewNomadDockerRunner(c.DockerAPI, c.Image, "", command)
}

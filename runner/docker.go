package runner

import (
	"context"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"go.uber.org/atomic"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
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
	if c.container != nil {
		return nil, fmt.Errorf("already running")
	}

	consulConfig := c.ConsulCommand.Config()
	localConfigDir, localDataDir := consulConfig.ConfigDir, consulConfig.DataDir
	for _, dir := range []string{localConfigDir, localDataDir} {
		if dir == "" {
			continue
		}
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return nil, err
		}
	}
	for name, contents := range consulConfig.Files() {
		if err := writeConfig(consulConfig.ConfigDir, name, contents); err != nil {
			return nil, err
		}
	}
	exposedPorts := nat.PortSet{
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
		DockerAPI: c.DockerAPI,
		netName:   consulConfig.NetworkConfig.DockerNetName,
		cfg: &container.Config{
			Image: c.Image,
			Cmd:   c.ConsulCommand.WithDirs("/consul/config", "/consul/data", "").Command(),
			Env:   []string{"CONSUL_DISABLE_PERM_MGMT=1"},
			Labels: map[string]string{
				"yurt": "true",
			},
			ExposedPorts: exposedPorts,
			WorkingDir:   "/consul/config",
		},
		mounts: []mount.Mount{
			{
				Type:     mount.TypeBind,
				Source:   localConfigDir,
				Target:   "/consul/config",
				ReadOnly: true,
			},
			{
				Type:   mount.TypeBind,
				Source: localDataDir,
				Target: "/consul/data",
			},
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
		_ = containerLogs(ctx, c.DockerAPI, *c.container)
	}()
	go func() {
		<-ctx.Done()
		_ = cleanupContainer(context.Background(), c.DockerAPI, cont.ID)
	}()
	return getIP(*cont, consulConfig.NetworkConfig.DockerNetName)
}

func containerLogs(ctx context.Context, cli *client.Client, cont types.ContainerJSON) error {
	resp, err := cli.ContainerLogs(ctx, cont.ID, types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
	})
	if err != nil {
		return err
	}

	_, err = stdcopy.StdCopy(os.Stderr, os.Stderr, resp)
	if err != nil {
		return err
	}
	return nil
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
	var port int
	switch {
	case c.Config().Ports.HTTP > 0:
		apiConfig.Scheme = "http"
		port = c.Config().Ports.HTTP
	case c.Config().Ports.HTTPS > 0:
		apiConfig.Scheme = "https"
		port = c.Config().Ports.HTTPS
		apiConfig.TLSConfig.CAFile = filepath.Join(c.Config().ConfigDir, "ca.pem")
	}

	ports := c.container.NetworkSettings.NetworkSettingsBase.Ports[nat.Port(fmt.Sprintf("%d/tcp", port))]
	if len(ports) == 0 {
		return nil, fmt.Errorf("no binding for Consul API port %d", port)
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
		return nil, fmt.Errorf("container start failed: %v", err)
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

	scheme, port := "http", 4646
	port = n.Config().Ports.HTTP
	if port <= 0 {
		port = 4646
	}

	ports := n.container.NetworkSettings.NetworkSettingsBase.Ports[nat.Port(fmt.Sprintf("%d/tcp", port))]
	if len(ports) == 0 {
		return nil, fmt.Errorf("no binding for Nomad API port")
	}

	if ca := n.Config().TLS.CA; len(ca) > 0 {
		scheme = "https"
		apiConfig.TLSConfig.CACert = filepath.Join(n.Config().ConfigDir, "ca.pem")
	}

	apiConfig.Address = fmt.Sprintf("%s://%s:%s", scheme, "127.0.0.1", ports[0].HostPort)
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
			WorkingDir: "/nomad/config",
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
		_ = containerLogs(ctx, n.DockerAPI, *n.container)
	}()
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
	IPs       []string
	i         atomic.Uint32
}

var _ NomadRunnerBuilder = (*NomadDockerServerBuilder)(nil)

func (c *NomadDockerServerBuilder) MakeNomadRunner(command NomadCommand) (NomadRunner, error) {
	ip := c.IPs[c.i.Inc()-1]
	return NewNomadDockerRunner(c.DockerAPI, c.Image, ip, command)
}

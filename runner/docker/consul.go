package docker

import (
	"github.com/docker/docker/client"
	"github.com/ncabatoff/yurt/runner"
	"go.uber.org/atomic"
)

type ConsulDockerBuilder struct {
	DockerAPI *client.Client
	Image     string
	IP        string
}

var _ runner.ConsulRunnerBuilder = (*ConsulDockerBuilder)(nil)

func (c *ConsulDockerBuilder) MakeConsulRunner(command runner.ConsulCommand) (runner.ConsulRunner, error) {
	return NewDockerRunner(c.DockerAPI, c.Image, c.IP, command)
}

type ConsulDockerServerBuilder struct {
	DockerAPI *client.Client
	Image     string
	IPs       []string
	i         atomic.Uint32
}

var _ runner.ConsulRunnerBuilder = (*ConsulDockerServerBuilder)(nil)

func (c *ConsulDockerServerBuilder) MakeConsulRunner(command runner.ConsulCommand) (runner.ConsulRunner, error) {
	ip := c.IPs[c.i.Inc()-1]
	return NewDockerRunner(c.DockerAPI, c.Image, ip, command)
}

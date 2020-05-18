package docker

import (
	dockerapi "github.com/docker/docker/client"
	"github.com/ncabatoff/yurt/runner"
	"go.uber.org/atomic"
)

type NomadDockerBuilder struct {
	DockerAPI *dockerapi.Client
	Image     string
}

var _ runner.NomadRunnerBuilder = (*NomadDockerBuilder)(nil)

func (c *NomadDockerBuilder) MakeNomadRunner(command runner.NomadCommand) (runner.NomadRunner, error) {
	return NewDockerRunner(c.DockerAPI, c.Image, "", command)
}

type NomadDockerServerBuilder struct {
	DockerAPI *dockerapi.Client
	Image     string
	IPs       []string
	i         atomic.Uint32
}

var _ runner.NomadRunnerBuilder = (*NomadDockerServerBuilder)(nil)

func (c *NomadDockerServerBuilder) MakeNomadRunner(command runner.NomadCommand) (runner.NomadRunner, error) {
	ip := c.IPs[c.i.Inc()-1]
	return NewDockerRunner(c.DockerAPI, c.Image, ip, command)
}

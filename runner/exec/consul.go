package exec

import (
	"github.com/ncabatoff/yurt/runner"
)

type ConsulExecBuilder struct {
	BinPath string
}

var _ runner.ConsulRunnerBuilder = (*ConsulExecBuilder)(nil)

func (c *ConsulExecBuilder) MakeConsulRunner(command runner.ConsulCommand) (runner.ConsulRunner, error) {
	return NewExecRunner(c.BinPath, command)
}

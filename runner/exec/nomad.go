package exec

import (
	"github.com/ncabatoff/yurt/runner"
)

type NomadExecBuilder struct {
	BinPath string
}

var _ runner.NomadRunnerBuilder = (*NomadExecBuilder)(nil)

func (c *NomadExecBuilder) MakeNomadRunner(command runner.NomadCommand) (runner.NomadRunner, error) {
	return NewExecRunner(c.BinPath, command)
}

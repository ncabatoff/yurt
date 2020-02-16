package exec

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt/runner"
	"os"
	"os/exec"
	"path/filepath"

	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt/util"
)

type NomadExecRunner struct {
	runner.NomadCommand
	BinPath string
	cmd     *exec.Cmd
	cancel  func()
}

var _ runner.NomadRunner = (*NomadExecRunner)(nil)

func NewNomadExecRunner(binPath string, command runner.NomadCommand) (*NomadExecRunner, error) {
	return &NomadExecRunner{
		NomadCommand: command,
		BinPath:      binPath,
	}, nil
}

func (ner *NomadExecRunner) Start(ctx context.Context) (string, error) {
	if ner.cmd != nil {
		return "", fmt.Errorf("already running")
	}

	args := ner.Command()
	//log.Print(ner.BinPath, args)

	for _, dir := range []string{ner.Config().ConfigDir, ner.Config().DataDir, ner.Config().LogConfig.LogDir} {
		if dir == "" {
			continue
		}
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return "", err
		}
	}
	files := ner.Files()
	for name, contents := range files {
		if err := util.WriteConfig(ner.Config().ConfigDir, name, contents); err != nil {
			return "", err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, ner.BinPath, args...)
	cmd.Dir = ner.Config().ConfigDir
	//cmd.Stdout = util.NewOutputWriter(ner.Config().NodeName, os.Stdout)
	cmd.Stderr = util.NewOutputWriter(ner.Config().NodeName, os.Stderr)

	if err := cmd.Start(); err != nil {
		return "", err
	}

	ner.cmd = cmd
	ner.cancel = cancel
	return "127.0.0.1", nil
}

func (ner *NomadExecRunner) NomadAPI() (*nomadapi.Client, error) {
	apiCfg, err := ner.NomadAPIConfig()
	if err != nil {
		return nil, err
	}

	return nomadapi.NewClient(apiCfg)
}

func (ner *NomadExecRunner) NomadAPIConfig() (*nomadapi.Config, error) {
	apiConfig := nomadapi.DefaultConfig()

	scheme, port := "http", 4646
	port = ner.Config().Ports.HTTP
	if port <= 0 {
		port = 4646
	}

	if ca := ner.Config().TLS.CA; len(ca) > 0 {
		scheme = "https"
		apiConfig.TLSConfig.CACert = filepath.Join(ner.Config().ConfigDir, "ca.pem")
	}
	apiConfig.Address = fmt.Sprintf("%s://%s:%d", scheme, "127.0.0.1", port)

	return apiConfig, nil
}

func (ner *NomadExecRunner) Wait() error {
	return ner.cmd.Wait()
}

func (ner *NomadExecRunner) Stop() error {
	ner.cancel()
	return nil
}

type NomadExecBuilder struct {
	BinPath string
}

var _ runner.NomadRunnerBuilder = (*NomadExecBuilder)(nil)

func (c *NomadExecBuilder) MakeNomadRunner(command runner.NomadCommand) (runner.NomadRunner, error) {
	return NewNomadExecRunner(c.BinPath, command)
}

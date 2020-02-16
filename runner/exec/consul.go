package exec

import (
	"context"
	"fmt"
	"github.com/hashicorp/consul/api"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/util"
	"os"
	"os/exec"
	"path/filepath"
)

type ConsulExecRunner struct {
	runner.ConsulCommand
	BinPath string
	cmd     *exec.Cmd
	cancel  func()
}

var _ runner.ConsulRunner = (*ConsulExecRunner)(nil)

func NewConsulExecRunner(binPath string, command runner.ConsulCommand) (*ConsulExecRunner, error) {
	return &ConsulExecRunner{
		ConsulCommand: command,
		BinPath:       binPath,
	}, nil
}

func (cer *ConsulExecRunner) Start(ctx context.Context) (string, error) {
	if cer.cmd != nil {
		return "", fmt.Errorf("already running")
	}

	args := cer.Command()
	//log.Print(cer.BinPath, args)

	for _, dir := range []string{cer.Config().ConfigDir, cer.Config().DataDir, cer.Config().LogConfig.LogDir} {
		if dir == "" {
			continue
		}
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return "", err
		}
	}
	for name, contents := range cer.Files() {
		if err := util.WriteConfig(cer.Config().ConfigDir, name, contents); err != nil {
			return "", err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, cer.BinPath, args...)
	cmd.Dir = cer.Config().ConfigDir
	//cmd.Stdout = util.NewOutputWriter(cer.Config().NodeName, os.Stdout)
	cmd.Stderr = util.NewOutputWriter(cer.Config().NodeName, os.Stderr)

	if err := cmd.Start(); err != nil {
		return "", err
	}

	cer.cmd = cmd
	cer.cancel = cancel
	return "127.0.0.1", nil
}

func (cer *ConsulExecRunner) ConsulAPI() (*api.Client, error) {
	apiCfg, err := cer.ConsulAPIConfig()
	if err != nil {
		return nil, err
	}

	return api.NewClient(apiCfg)

}

func (cer *ConsulExecRunner) ConsulAPIConfig() (*api.Config, error) {
	apiConfig := api.DefaultNonPooledConfig()

	// TODO there's no reason we have to limit exec runners to using localhost.
	// Use network instead.  Or better still the command/config.

	cfg := cer.ConsulCommand.Config()
	if len(cfg.TLS.Cert) > 0 {
		apiConfig.Scheme = "https"
		apiConfig.TLSConfig.CAFile = filepath.Join(cfg.ConfigDir, "ca.pem")
	}
	apiConfig.Address = fmt.Sprintf("%s:%d", "127.0.0.1", cfg.Ports.HTTP)

	return apiConfig, nil
}

func (cer *ConsulExecRunner) Wait() error {
	return cer.cmd.Wait()
}

func (cer *ConsulExecRunner) Stop() error {
	cer.cancel()
	return nil
}

type ConsulExecBuilder struct {
	BinPath string
}

var _ runner.ConsulRunnerBuilder = (*ConsulExecBuilder)(nil)

func (c *ConsulExecBuilder) MakeConsulRunner(command runner.ConsulCommand) (runner.ConsulRunner, error) {
	return NewConsulExecRunner(c.BinPath, command)
}

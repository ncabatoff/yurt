package exec

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/util"
)

type ExecRunner struct {
	command runner.Command
	BinPath string
	cmd     *exec.Cmd
	cancel  func()
}

var _ runner.Runner = (*ExecRunner)(nil)

func NewExecRunner(binPath string, command runner.Command) (*ExecRunner, error) {
	return &ExecRunner{
		command: command,
		BinPath: binPath,
	}, nil
}

func (e *ExecRunner) Command() runner.Command {
	return e.command
}

// Start launches the process
func (e *ExecRunner) Start(ctx context.Context) (string, error) {
	if e.cmd != nil {
		return "", fmt.Errorf("already running")
	}

	cfg := e.command.Config()
	for _, dir := range []string{cfg.DataDir, cfg.LogDir} {
		if dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "", err
			}
		}
	}
	for name, contents := range e.command.Files() {
		if err := util.WriteConfig(cfg.ConfigDir, name, contents); err != nil {
			return "", err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, e.BinPath, e.command.Args()...)
	cmd.Env = e.command.Env()
	cmd.Dir = cfg.ConfigDir
	//fmt.Fprintln(os.Stderr, cmd)
	cmd.Stdout = util.NewOutputWriter(cfg.NodeName, os.Stdout)
	cmd.Stderr = util.NewOutputWriter(cfg.NodeName, os.Stderr)

	if err := cmd.Start(); err != nil {
		return "", err
	}

	e.cmd = cmd
	e.cancel = cancel
	return "127.0.0.1", nil
}

func (e *ExecRunner) APIConfig() (*runner.APIConfig, error) {
	apiConfig := runner.APIConfig{Address: url.URL{Scheme: "http"}}

	cfg := e.command.Config()
	if cfg.APIPort == 0 {
		return nil, fmt.Errorf("no API port defined in config")
	}

	if len(cfg.TLS.Cert) > 0 {
		apiConfig.Address.Scheme = "https"
		apiConfig.CAFile = filepath.Join(cfg.ConfigDir, "ca.pem")
	}

	apiConfig.Address.Host = fmt.Sprintf("%s:%d", "127.0.0.1", cfg.APIPort)

	return &apiConfig, nil
}

func (e *ExecRunner) Wait() error {
	return e.cmd.Wait()
}

func (e *ExecRunner) Stop() error {
	e.cancel()
	return nil
}

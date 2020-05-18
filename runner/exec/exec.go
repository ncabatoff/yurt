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
	runner.Command
	BinPath string
	cmd     *exec.Cmd
	cancel  func()
}

var _ runner.Runner = (*ExecRunner)(nil)

func NewExecRunner(binPath string, command runner.Command) (*ExecRunner, error) {
	return &ExecRunner{
		Command: command,
		BinPath: binPath,
	}, nil
}

// Start launches the process
func (e *ExecRunner) Start(ctx context.Context) (string, error) {
	if e.cmd != nil {
		return "", fmt.Errorf("already running")
	}

	for _, dir := range []string{e.Config().DataDir, e.Config().LogDir} {
		if dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "", err
			}
		}
	}
	for name, contents := range e.Files() {
		if err := util.WriteConfig(e.Config().ConfigDir, name, contents); err != nil {
			return "", err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, e.BinPath, e.Args()...)
	cmd.Env = e.Env()
	cmd.Dir = e.Config().ConfigDir
	cmd.Stdout = util.NewOutputWriter(e.Config().NodeName, os.Stdout)
	cmd.Stderr = util.NewOutputWriter(e.Config().NodeName, os.Stderr)

	if err := cmd.Start(); err != nil {
		return "", err
	}

	e.cmd = cmd
	e.cancel = cancel
	return "127.0.0.1", nil
}

func (e *ExecRunner) APIConfig() (*runner.APIConfig, error) {
	apiConfig := runner.APIConfig{Address: url.URL{Scheme: "http"}}

	cfg := e.Config()
	if cfg.APIPort == 0 {
		return nil, fmt.Errorf("no API port defined in config")
	}

	if len(cfg.TLS.Cert) > 0 {
		apiConfig.Address.Scheme = "https"
		apiConfig.CAFile = filepath.Join(cfg.ConfigDir, "ca.pem")
	}

	// TODO there's no reason we have to limit exec runners to using localhost.
	// Use network instead.  Or better still the command/config.
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

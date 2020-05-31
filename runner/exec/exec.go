package exec

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/util"
)

type ExecRunner struct {
	command runner.Command
	config  runner.Config
	BinPath string
}

type harness struct {
	cancel func()
	config runner.Config
	cmd    *exec.Cmd
}

var _ runner.Harness = &harness{}

func NewExecRunner(binPath string, command runner.Command, config runner.Config) (*ExecRunner, error) {
	return &ExecRunner{
		config:  config,
		command: command,
		BinPath: binPath,
	}, nil
}

// Start launches the process
func (e *ExecRunner) Start(ctx context.Context) (*harness, error) {
	for _, dir := range []string{e.config.DataDir, e.config.LogDir} {
		if dir != "" {
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, err
			}
		}
	}
	command := e.command.WithConfig(e.config)
	for name, contents := range command.Files() {
		if err := util.WriteConfig(e.config.ConfigDir, name, contents); err != nil {
			return nil, err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, e.BinPath, command.Args()...)
	cmd.Env = command.Env()
	cmd.Dir = e.config.ConfigDir
	log.Println(cmd)
	cmd.Stdout = util.NewOutputWriter(e.config.NodeName, os.Stdout)
	cmd.Stderr = util.NewOutputWriter(e.config.NodeName, os.Stderr)

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	return &harness{
		config: command.Config(),
		cancel: cancel,
		cmd:    cmd,
	}, nil
}

func (h harness) Endpoint(name string, local bool) (*runner.APIConfig, error) {
	port := h.config.Ports.ByName[name]
	if port.Number == 0 {
		return nil, fmt.Errorf("no port %q defined in config", name)
	}

	var apiConfig runner.APIConfig
	if len(h.config.TLS.Cert) > 0 {
		if name == "http" {
			name = "https"
		}
		apiConfig.CAFile = filepath.Join(h.config.ConfigDir, "ca.pem")
	}
	apiConfig.Address.Scheme = name
	apiConfig.Address.Host = fmt.Sprintf("%s:%d", "127.0.0.1", port.Number)

	return &apiConfig, nil
}

func (h harness) Wait() error {
	return h.cmd.Wait()
}

func (h harness) Stop() error {
	h.cancel()
	return nil
}

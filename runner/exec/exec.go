package exec

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/util"
)

type ExecRunner struct {
	command runner.Command
	config  runner.Config
	BinPath string
}

type Harness struct {
	cancel func()
	Config runner.Config
	cmd    *exec.Cmd
}

var _ runner.Harness = &Harness{}

func NewExecRunner(binPath string, command runner.Command, config runner.Config) (*ExecRunner, error) {
	return &ExecRunner{
		config:  config,
		command: command,
		BinPath: binPath,
	}, nil
}

// Start launches the process
func (e *ExecRunner) Start(ctx context.Context, logname string) (*Harness, error) {
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

	output := os.Stdout
	if logname != "" {
		var err error
		output, err = os.Create(logname)
		if err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, e.BinPath, command.Args()...)
	cmd.Env = command.Env()
	cmd.Dir = e.config.ConfigDir
	if logname != "" {
		log.Println(cmd, ">", logname)
	} else {
		log.Println(cmd)
	}

	cmd.Stdout = util.NewLinePrefixer(e.config.NodeName, output)
	cmd.Stderr = util.NewLinePrefixer(e.config.NodeName, output)

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	return &Harness{
		Config: command.Config(),
		cancel: func() {
			log.Println("cancelling exec context for", e.config.NodeName)
			//debug.PrintStack()
			cancel()
		},
		cmd: cmd,
	}, nil
}

func (h Harness) Endpoint(name string, local bool) (*runner.APIConfig, error) {
	port := h.Config.Ports.ByName[name]
	if port.Number == 0 {
		return nil, fmt.Errorf("no port %q defined in config", name)
	}

	var apiConfig runner.APIConfig
	if len(h.Config.TLS.Cert) > 0 {
		if name == "http" {
			name = "https"
		}
		apiConfig.CAFile = filepath.Join(h.Config.ConfigDir, "ca.pem")
	}
	apiConfig.Address.Scheme = name
	apiConfig.Address.Host = fmt.Sprintf("%s:%d", "127.0.0.1", port.Number)

	return &apiConfig, nil
}

func (h Harness) Wait() error {
	err := h.cmd.Wait()
	if err != nil && strings.Contains(err.Error(), "signal: killed") {
		return nil
	}
	return err
}

func (h Harness) Kill() {
	h.cancel()
}

func (h Harness) Stop() error {
	h.cmd.Process.Signal(syscall.SIGTERM)
	time.Sleep(3 * time.Second)
	h.cancel()
	return nil
}

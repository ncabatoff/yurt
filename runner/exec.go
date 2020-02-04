package runner

import (
	"context"
	"fmt"
	"github.com/hashicorp/consul/api"
	api2 "github.com/hashicorp/nomad/api"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
)

type ConsulExecRunner struct {
	ConsulCommand
	BinPath string
	cmd     *exec.Cmd
	cancel  func()
}

var _ ConsulRunner = (*ConsulExecRunner)(nil)

var localhost = net.IPv4(127, 0, 0, 1)

func NewConsulExecRunner(binPath string, command ConsulCommand) (*ConsulExecRunner, error) {
	return &ConsulExecRunner{
		ConsulCommand: command,
		BinPath:       binPath,
	}, nil
}

func (cer *ConsulExecRunner) Start(ctx context.Context) (net.IP, error) {
	if cer.cmd != nil {
		return nil, fmt.Errorf("already running")
	}

	args := cer.Command()
	log.Print(cer.BinPath, args)

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, cer.BinPath, args...)
	cmd.Stdout = NewOutputWriter(cer.Config().NodeName, os.Stdout)
	cmd.Stderr = NewOutputWriter(cer.Config().NodeName, os.Stderr)

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	cer.cmd = cmd
	cer.cancel = cancel
	return localhost, nil
}

func (cer *ConsulExecRunner) ConsulAPI() (*api.Client, error) {
	apiConfig := api.DefaultNonPooledConfig()
	apiConfig.Scheme = "http"

	if cer.Config().Ports.HTTP != 0 {
		// TODO there's no reason we have to limit exec runners to using localhost.  Use network
		// instead.  Or better still the command/config.
		apiConfig.Address = fmt.Sprintf("%s:%d", "127.0.0.1", cer.Config().Ports.HTTP)
	}

	return api.NewClient(apiConfig)
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

var _ ConsulRunnerBuilder = (*ConsulExecBuilder)(nil)

func (c *ConsulExecBuilder) MakeConsulRunner(command ConsulCommand) (ConsulRunner, error) {
	return NewConsulExecRunner(c.BinPath, command)
}

type NomadExecRunner struct {
	NomadCommand
	BinPath string
	cmd     *exec.Cmd
	cancel  func()
}

var _ NomadRunner = (*NomadExecRunner)(nil)

func NewNomadExecRunner(binPath string, command NomadCommand) (*NomadExecRunner, error) {
	return &NomadExecRunner{
		NomadCommand: command,
		BinPath:      binPath,
	}, nil
}

func writeConfig(dir, name, contents string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	log.Print(path, contents)
	return ioutil.WriteFile(path, []byte(contents), 0600)
}

func (ner *NomadExecRunner) Start(ctx context.Context) (net.IP, error) {
	if ner.cmd != nil {
		return nil, fmt.Errorf("already running")
	}

	args := ner.Command()
	log.Print(ner.BinPath, args)

	files := ner.Files()
	for name, contents := range files {
		if err := writeConfig(ner.Config().ConfigDir, name, contents); err != nil {
			return nil, err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, ner.BinPath, args...)
	cmd.Stdout = NewOutputWriter(ner.Config().NodeName, os.Stdout)
	cmd.Stderr = NewOutputWriter(ner.Config().NodeName, os.Stderr)

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	ner.cmd = cmd
	ner.cancel = cancel
	return localhost, nil
}

func (ner *NomadExecRunner) NomadAPI() (*api2.Client, error) {
	apiConfig := api2.DefaultConfig()

	if ner.Config().Ports.HTTP != 0 {
		apiConfig.Address = fmt.Sprintf("http://%s:%d", "127.0.0.1", ner.Config().Ports.HTTP)
	}

	return api2.NewClient(apiConfig)
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

var _ NomadRunnerBuilder = (*NomadExecBuilder)(nil)

func (c *NomadExecBuilder) MakeNomadRunner(command NomadCommand) (NomadRunner, error) {
	return NewNomadExecRunner(c.BinPath, command)
}

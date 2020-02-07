package runner

import (
	"context"
	"fmt"
	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/hashicorp/vault/sdk/helper/certutil"
	"github.com/ncabatoff/yurt/util"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	for _, dir := range []string{cer.Config().ConfigDir, cer.Config().DataDir} {
		if dir == "" {
			continue
		}
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return nil, err
		}
	}
	for name, contents := range cer.Files() {
		if err := writeConfig(cer.Config().ConfigDir, name, contents); err != nil {
			return nil, err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, cer.BinPath, args...)
	cmd.Dir = cer.Config().ConfigDir
	cmd.Stdout = util.NewOutputWriter(cer.Config().NodeName, os.Stdout)
	cmd.Stderr = util.NewOutputWriter(cer.Config().NodeName, os.Stderr)

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	cer.cmd = cmd
	cer.cancel = cancel
	return localhost, nil
}

func (cer *ConsulExecRunner) ConsulAPI() (*consulapi.Client, error) {
	apiConfig := consulapi.DefaultNonPooledConfig()

	// TODO there's no reason we have to limit exec runners to using localhost.
	// Use network instead.  Or better still the command/config.

	switch {
	case cer.Config().Ports.HTTP > 0:
		apiConfig.Address = fmt.Sprintf("%s:%d", "127.0.0.1", cer.Config().Ports.HTTP)
	case cer.Config().Ports.HTTPS > 0:
		apiConfig.Address = fmt.Sprintf("%s:%d", "127.0.0.1", cer.Config().Ports.HTTPS)
		apiConfig.Scheme = "https"
		apiConfig.TLSConfig.CAFile = filepath.Join(cer.Config().ConfigDir, "ca.pem")
	}

	return consulapi.NewClient(apiConfig)
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
	if strings.HasSuffix(name, ".pem") {
		bundle, err := certutil.ParsePEMBundle(string(contents))
		if err != nil {
			return err
		}
		cert := bundle.Certificate
		if cert != nil {
			log.Printf("%s: Issuer=%s Subject=%s DNS=%v IP=%v\n", path,
				cert.Issuer, cert.Subject, cert.DNSNames, cert.IPAddresses)
		}
	} else {
		log.Print(path, contents)
	}
	return ioutil.WriteFile(path, []byte(contents), 0600)
}

func (ner *NomadExecRunner) Start(ctx context.Context) (net.IP, error) {
	if ner.cmd != nil {
		return nil, fmt.Errorf("already running")
	}

	args := ner.Command()
	log.Print(ner.BinPath, args)

	for _, dir := range []string{ner.Config().ConfigDir, ner.Config().DataDir} {
		if dir == "" {
			continue
		}
		err := os.MkdirAll(dir, 0755)
		if err != nil {
			return nil, err
		}
	}
	files := ner.Files()
	for name, contents := range files {
		if err := writeConfig(ner.Config().ConfigDir, name, contents); err != nil {
			return nil, err
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, ner.BinPath, args...)
	cmd.Dir = ner.Config().ConfigDir
	cmd.Stdout = util.NewOutputWriter(ner.Config().NodeName, os.Stdout)
	cmd.Stderr = util.NewOutputWriter(ner.Config().NodeName, os.Stderr)

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	ner.cmd = cmd
	ner.cancel = cancel
	return localhost, nil
}

func (ner *NomadExecRunner) NomadAPI() (*nomadapi.Client, error) {
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

	return nomadapi.NewClient(apiConfig)
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

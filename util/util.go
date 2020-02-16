package util

import (
	"bufio"
	"fmt"
	"github.com/hashicorp/go-sockaddr"
	"github.com/hashicorp/vault/api"
	"github.com/hashicorp/vault/sdk/helper/certutil"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

type OutputWriter struct {
	io.Writer
}

var _ io.Writer = (*OutputWriter)(nil)

func NewOutputWriter(prefix string, output io.Writer) *OutputWriter {
	r, w := io.Pipe()
	br := bufio.NewReader(r)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				_, _ = fmt.Fprintf(output, "%s: %s", prefix, line)
			}
			if err != nil {
				break
			}
		}
	}()
	return &OutputWriter{
		Writer: w,
	}
}

func WriteConfig(dir, name, contents string) error {
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
			//log.Printf("%s: Issuer=%s Subject=%s DNS=%v IP=%v\n", path,
			//	cert.Issuer, cert.Subject, cert.DNSNames, cert.IPAddresses)
		}
	} else {
		//log.Print(path, contents)
	}
	return ioutil.WriteFile(path, []byte(contents), 0600)
}

type NetworkConfig struct {
	Network       sockaddr.SockAddr
	DockerNetName string
}

func MakeVaultClient(addr, token string) (*api.Client, error) {
	vaultConfig := api.DefaultConfig()
	vaultConfig.Address = addr
	cli, err := api.NewClient(vaultConfig)
	if err != nil {
		return nil, err
	}
	cli.SetToken(token)
	return cli, nil
}

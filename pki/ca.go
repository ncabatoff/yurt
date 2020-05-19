package pki

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt/util"
	"os/exec"
	"strings"
	"time"

	"github.com/hashicorp/go-uuid"
	vaultapi "github.com/hashicorp/vault/api"
	"github.com/ncabatoff/yurt/binaries"
)

type CertificateAuthority struct {
	path string
	cli  *vaultapi.Client
}

func NewExternalCertificateAuthority(vaultAddr, vaultToken string) (*CertificateAuthority, error) {
	cli, err := util.MakeVaultClient(vaultAddr, vaultToken)
	if err != nil {
		return nil, err
	}
	return NewCertificateAuthority(cli)
}

func NewCertificateAuthority(cli *vaultapi.Client) (*CertificateAuthority, error) {
	u, err := uuid.GenerateUUID()
	if err != nil {
		return nil, err
	}

	if err := createRootCA(cli, u); err != nil {
		return nil, err
	}

	if err := createIntermediateCA(cli, u); err != nil {
		return nil, err
	}

	return &CertificateAuthority{
		path: u,
		cli:  cli,
	}, nil
}

type VaultRunner struct {
	WorkDir string
	Port    int
	cmd     *exec.Cmd
	ctx     context.Context
	cancel  func()
	Cli     *vaultapi.Client
}

func NewVaultRunner(workDir string, port int) (*VaultRunner, error) {
	return &VaultRunner{WorkDir: workDir, Port: port}, nil
}

// Start launches a Vault instance on the configured port, which may write files
// to workDir.  It will be killed when the context is cancelled.
func (v *VaultRunner) Start(ctx context.Context) (err error) {
	if v.cmd != nil {
		return fmt.Errorf("already running")
	}

	binPath, err := binaries.Default.Get("vault")
	if err != nil {
		return err
	}
	v.ctx, v.cancel = context.WithCancel(ctx)
	rootToken := "devroot"
	cmd := exec.CommandContext(v.ctx, binPath, "server", "-dev",
		"-dev-listen-address", fmt.Sprintf("0.0.0.0:%d", v.Port),
		"-dev-root-token-id", rootToken)
	// TODO send log output to file?
	//cmd.Stdout = util.NewOutputWriter("vault-ca", os.Stdout)
	//cmd.Stderr = util.NewOutputWriter("vault-ca", os.Stderr)

	if err := cmd.Start(); err != nil {
		return err
	}
	v.cmd = cmd
	defer func() {
		if err != nil {
			v.cancel()
		}
	}()

	v.Cli, err = util.MakeVaultClient(fmt.Sprintf("http://localhost:%d", v.Port), rootToken)
	if err != nil {
		return err
	}

	if err := waitVaultUnsealed(ctx, v.Cli); err != nil {
		return err
	}

	return nil
}

func (v *VaultRunner) Stop() {
	v.cancel()
}

func waitVaultUnsealed(ctx context.Context, cli *vaultapi.Client) (err error) {
	for ctx.Err() == nil {
		var resp *vaultapi.SealStatusResponse
		resp, err = cli.Sys().SealStatus()
		if resp != nil && !resp.Sealed {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("unseal failed, last error: %v", err)
}

func createRootCA(cli *vaultapi.Client, pfx string) error {
	rootPath := pfx + "-pki-root"
	if err := cli.Sys().Mount(rootPath, &vaultapi.MountInput{
		Type: "pki",
		Config: vaultapi.MountConfigInput{
			MaxLeaseTTL: "87600h",
		},
	}); err != nil {
		return err
	}

	_, err := cli.Logical().Write(rootPath+"/root/generate/internal", map[string]interface{}{
		"common_name": "example.com",
		"ttl":         "87600h",
	})
	if err != nil {
		return err
	}

	_, err = cli.Logical().Write(rootPath+"/config/urls", map[string]interface{}{
		"issuing_certificates":   fmt.Sprintf("%s/v1/%s/ca", cli.Address(), rootPath),
		"crl_distribution_point": fmt.Sprintf("%s/v1/%s/crl", cli.Address(), rootPath),
	})
	if err != nil {
		return err
	}
	return nil
}

func createIntermediateCA(cli *vaultapi.Client, pfx string) error {
	rootPath, intPath := pfx+"-pki-root", pfx+"-pki-int"

	if err := cli.Sys().Mount(intPath, &vaultapi.MountInput{
		Type: "pki",
		Config: vaultapi.MountConfigInput{
			MaxLeaseTTL: "43800h",
		},
	}); err != nil {
		return err
	}

	resp, err := cli.Logical().Write(intPath+"/intermediate/generate/internal", map[string]interface{}{
		"common_name": "example.com Intermediate Authority",
		"ttl":         "43800h",
	})
	if err != nil {
		return err
	}

	resp, err = cli.Logical().Write(rootPath+"/root/sign-intermediate", map[string]interface{}{
		"csr":    resp.Data["csr"].(string),
		"format": "pem_bundle",
	})
	if err != nil {
		return err
	}

	_, err = cli.Logical().Write(intPath+"/intermediate/set-signed", map[string]interface{}{
		"certificate": strings.Join([]string{resp.Data["certificate"].(string), resp.Data["issuing_ca"].(string)}, "\n"),
	})
	if err != nil {
		return err
	}

	resp, err = cli.Logical().Write(intPath+"/roles/consul-server", map[string]interface{}{
		"allowed_domains":  "server.dc1.consul",
		"allow_subdomains": "true",
		"allow_localhost":  "true",
		"allow_any_name":   "true",
		"allow_ip_sans":    "true",
		"max_ttl":          "720h",
	})
	if err != nil {
		return err
	}

	resp, err = cli.Logical().Write(intPath+"/roles/nomad-server", map[string]interface{}{
		"allowed_domains":  "server.global.nomad",
		"allow_subdomains": "true",
		"allow_localhost":  "true",
		"allow_any_name":   "true",
		"allow_ip_sans":    "true",
		"max_ttl":          "720h",
	})
	if err != nil {
		return err
	}
	return nil
}

func (ca *CertificateAuthority) serverTLS(ctx context.Context, role, cn, ip, ttl string) (*TLSConfigPEM, error) {
	secret, err := ca.cli.Logical().Write(ca.path+"-pki-int/issue/"+role, map[string]interface{}{
		"common_name": cn,
		"alt_names":   "localhost",
		"ip_sans":     ip,
		"ttl":         ttl,
	})
	if err != nil {
		return nil, err
	}

	var cacert string
	for _, c := range secret.Data["ca_chain"].([]interface{}) {
		cacert += c.(string) + "\n"
	}

	return &TLSConfigPEM{
		CA:         cacert,
		Cert:       secret.Data["certificate"].(string),
		PrivateKey: secret.Data["private_key"].(string),
	}, nil
}

func (ca *CertificateAuthority) ConsulServerTLS(ctx context.Context, ip, ttl string) (*TLSConfigPEM, error) {
	return ca.serverTLS(ctx, "consul-server", "server.dc1.consul", ip+",127.0.0.1", ttl)
}

func (ca *CertificateAuthority) NomadServerTLS(ctx context.Context, ip, ttl string) (*TLSConfigPEM, error) {
	return ca.serverTLS(ctx, "nomad-server", "server.global.nomad", ip+",127.0.0.1", ttl)
}

package pki

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/ncabatoff/yurt/packages"
	"github.com/ncabatoff/yurt/util"
)

type CertificateAuthority struct {
	workDir string
	port    int
	cmd     *exec.Cmd
	cancel  func()
	cli     *vaultapi.Client
}

func NewExternalCertificateAuthority(vaultAddr, vaultToken string) (*CertificateAuthority, error) {
	cli, err := makeVaultClient(vaultAddr, vaultToken)
	if err != nil {
		return nil, err
	}
	return &CertificateAuthority{cli: cli}, nil
}

func NewCertificateAuthority(workDir string, port int) (*CertificateAuthority, error) {
	return &CertificateAuthority{workDir: workDir, port: port}, nil
}

func (ca *CertificateAuthority) Start(ctx context.Context) (err error) {
	if ca.cmd != nil {
		return fmt.Errorf("already running")
	}

	var binPath string
	if binPath, err = packages.GetBinary("vault", runtime.GOOS, runtime.GOARCH, "download"); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	rootToken := "devroot"
	cmd := exec.CommandContext(ctx, binPath, "server", "-dev",
		"-dev-listen-address", fmt.Sprintf("0.0.0.0:%d", ca.port),
		"-dev-root-token-id", rootToken)
	cmd.Stdout = util.NewOutputWriter("vault-ca", os.Stdout)
	cmd.Stderr = util.NewOutputWriter("vault-ca", os.Stderr)

	if err := cmd.Start(); err != nil {
		return err
	}
	ca.cmd = cmd
	ca.cancel = cancel
	defer func() {
		if err != nil {
			cancel()
		}
	}()

	ca.cli, err = makeVaultClient(fmt.Sprintf("http://localhost:%d", ca.port), rootToken)
	if err != nil {
		return err
	}

	if err := waitVaultUnsealed(ctx, ca.cli); err != nil {
		return err
	}

	if err := createRootCA(ca.cli); err != nil {
		return err
	}

	if err := createIntermediateCA(ca.cli); err != nil {
		return err
	}

	return nil
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

func makeVaultClient(addr, token string) (*vaultapi.Client, error) {
	vaultConfig := vaultapi.DefaultConfig()
	vaultConfig.Address = addr
	cli, err := vaultapi.NewClient(vaultConfig)
	if err != nil {
		return nil, err
	}
	cli.SetToken(token)
	return cli, nil
}

func createRootCA(cli *vaultapi.Client) error {
	if err := cli.Sys().Mount("pki_root", &vaultapi.MountInput{
		Type: "pki",
		Config: vaultapi.MountConfigInput{
			MaxLeaseTTL: "87600h",
		},
	}); err != nil {
		return err
	}

	_, err := cli.Logical().Write("pki_root/root/generate/internal", map[string]interface{}{
		"common_name": "example.com",
		"ttl":         "87600h",
	})
	if err != nil {
		return err
	}

	_, err = cli.Logical().Write("pki_root/config/urls", map[string]interface{}{
		"issuing_certificates":   fmt.Sprintf("%s/v1/pki_root/ca", cli.Address()),
		"crl_distribution_point": fmt.Sprintf("%s/v1/pki_root/crl", cli.Address()),
	})
	if err != nil {
		return err
	}
	return nil
}

func createIntermediateCA(cli *vaultapi.Client) error {
	if err := cli.Sys().Mount("pki_int", &vaultapi.MountInput{
		Type: "pki",
		Config: vaultapi.MountConfigInput{
			MaxLeaseTTL: "43800h",
		},
	}); err != nil {
		return err
	}

	resp, err := cli.Logical().Write("pki_int/intermediate/generate/internal", map[string]interface{}{
		"common_name": "example.com Intermediate Authority",
		"ttl":         "43800h",
	})
	if err != nil {
		return err
	}

	resp, err = cli.Logical().Write("pki_root/root/sign-intermediate", map[string]interface{}{
		"csr":    resp.Data["csr"].(string),
		"format": "pem_bundle",
	})
	if err != nil {
		return err
	}

	_, err = cli.Logical().Write("pki_int/intermediate/set-signed", map[string]interface{}{
		"certificate": strings.Join([]string{resp.Data["certificate"].(string), resp.Data["issuing_ca"].(string)}, "\n"),
	})
	if err != nil {
		return err
	}

	resp, err = cli.Logical().Write("pki_int/roles/consul-server", map[string]interface{}{
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

	resp, err = cli.Logical().Write("pki_int/roles/nomad-server", map[string]interface{}{
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
	secret, err := ca.cli.Logical().Write("pki_int/issue/"+role, map[string]interface{}{
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
	return ca.serverTLS(ctx, "nomad-server", "server.global.nomad", "127.0.0.1", ttl)
}

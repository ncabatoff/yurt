package vault

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/prometheus"
	"github.com/ncabatoff/yurt/runner"
)

type Ports struct {
	HTTP    int
	Cluster int
}

var PortNames = struct {
	HTTP    string
	Cluster string
}{
	"http",
	"cluster",
}

func DefPorts() Ports {
	return Ports{
		HTTP:    8200,
		Cluster: 8201,
	}
}

func (c Ports) RunnerPorts() yurt.Ports {
	return yurt.Ports{
		Kind: "vault",
		NameOrder: []string{
			PortNames.HTTP,
			PortNames.Cluster,
		},
		ByName: map[string]yurt.Port{
			PortNames.HTTP:    {c.HTTP, yurt.TCPOnly},
			PortNames.Cluster: {c.Cluster, yurt.TCPOnly},
		},
	}
}

type Seal struct {
	Type   string
	Config map[string]string
}

// ConsulConfig describes how to run a single Consul agent.
type VaultConfig struct {
	Common runner.Config
	// JoinAddrs specifies the addresses of the Vault servers.  If they have
	// a :port suffix, it should be that of the "http" port (i.e. the API address,
	// 8200 by default).  Only used for Raft storage.
	JoinAddrs []string
	// ConsulAddr gives the host:port where to connect to Consul, normally
	// localhost:8500.  Only needed for Consul storage or service registration.
	ConsulAddr string
	// ConsulPath gives the Consul KV prefix where Vault will store its data.
	// Only needed for Consul storage.
	ConsulPath string
	Seal       *Seal
	OldSeal    *Seal
}

func (vc VaultConfig) Config() runner.Config {
	return vc.Common
}

func (vc VaultConfig) Name() string {
	return "vault"
}

func NewRaftConfig(joinAddrs []string, tls *pki.TLSConfigPEM) VaultConfig {
	var t pki.TLSConfigPEM
	if tls != nil {
		t = *tls
	}
	return VaultConfig{
		JoinAddrs: joinAddrs,
		Common: runner.Config{
			Ports: DefPorts().RunnerPorts(),
			TLS:   t,
		},
	}
}

func NewConsulConfig(consulAddr, consulPath string, tls *pki.TLSConfigPEM) VaultConfig {
	var t pki.TLSConfigPEM
	if tls != nil {
		t = *tls
	}
	return VaultConfig{
		ConsulAddr: consulAddr,
		ConsulPath: consulPath,
		Common: runner.Config{
			Ports: DefPorts().RunnerPorts(),
			TLS:   t,
		},
	}
}

func (vc VaultConfig) WithConfig(cfg runner.Config) runner.Command {
	vc.Common = cfg
	return vc
}

func (vc VaultConfig) Args() []string {
	return []string{"server", "-config", vc.Common.ConfigDir}
}

func (vc VaultConfig) Env() []string {
	return nil
}

func (vc VaultConfig) raftConfig() string {
	var retryJoin string
	scheme, ca := "http", ""
	if len(vc.Common.TLS.Cert) > 0 {
		scheme = "https"
		ca = `leader_ca_cert_file = "ca.pem"`
	}
	for _, j := range vc.JoinAddrs {
		retryJoin += fmt.Sprintf(`
  retry_join {
    leader_api_addr = "%s://%s"
    %s
  }`, scheme, j, ca)
	}

	return fmt.Sprintf(`
storage "raft" {
  path = "%s"
  node_id = "%s"
  performance_multiplier = "1"
  %s
}
`, vc.Common.DataDir, vc.Common.NodeName, retryJoin)
}

func (vc VaultConfig) consulConfig() string {
	var tls string
	if vc.Common.TLS.Cert != "" {
		tls = `
    scheme = "https"
    tls_ca_file = "ca.pem"
    tls_cert_file = "vault.pem"
    tls_key_file = "vault-key.pem"
`
	}

	return fmt.Sprintf(`
storage "consul" {
  address = "%s"
  path = "%s"
%s
}
	`, vc.ConsulAddr, vc.ConsulPath, tls)
}

func (vc VaultConfig) Files() map[string]string {
	listenerAddr := fmt.Sprintf("127.0.0.1:%d", vc.Common.Ports.ByName[PortNames.HTTP].Number)
	apiAddr := fmt.Sprintf("http://127.0.0.1:%d", vc.Common.Ports.ByName[PortNames.HTTP].Number)
	clusterAddr := fmt.Sprintf("http://127.0.0.1:%d", vc.Common.Ports.ByName[PortNames.Cluster].Number)
	var tlsConfig string
	files := map[string]string{}
	if vc.Common.TLS.Cert != "" {
		apiAddr = fmt.Sprintf("https://127.0.0.1:%d", vc.Common.Ports.ByName[PortNames.HTTP].Number)
		clusterAddr = fmt.Sprintf("https://127.0.0.1:%d", vc.Common.Ports.ByName[PortNames.Cluster].Number)

		files["vault.pem"] = vc.Common.TLS.Cert
		tlsConfig += `  tls_cert_file = "vault.pem"
`
		files["vault-key.pem"] = vc.Common.TLS.PrivateKey
		tlsConfig += `  tls_key_file = "vault-key.pem"
`
	}
	if vc.Common.TLS.CA != "" {
		files["ca.pem"] = vc.Common.TLS.CA
		tlsConfig += `  tls_client_ca_file = "ca.pem"
`
	}
	config := fmt.Sprintf(`
disable_mlock = true
ui = true
api_addr = "%s"
cluster_addr = "%s"
listener "tcp" {
  telemetry {
	unauthenticated_metrics_access = true
  }
  address = "%s"
  tls_disable = %v
  tls_disable_client_certs = true
%s
}
telemetry {
  disable_hostname = true
  prometheus_retention_time = "10m"
}
`, apiAddr, clusterAddr, listenerAddr, vc.Common.TLS.Cert == "", tlsConfig)

	if vc.ConsulAddr != "" {
		config += vc.consulConfig()
	} else {
		config += vc.raftConfig()
	}

	if vc.Seal != nil {
		var kvals []string
		for k, v := range vc.Seal.Config {
			kvals = append(kvals, fmt.Sprintf(`%s = "%s"`, k, v))
		}
		config += fmt.Sprintf(`
seal "%s" {
  %s
}
`, vc.Seal.Type, strings.Join(kvals, "\n  "))
	}

	if vc.OldSeal != nil {
		var kvals = []string{`disabled = "true"`}
		for k, v := range vc.OldSeal.Config {
			kvals = append(kvals, fmt.Sprintf(`%s = "%s"`, k, v))
		}
		config += fmt.Sprintf(`
seal "%s" {
  %s
}
`, vc.OldSeal.Type, strings.Join(kvals, "\n  "))
	}

	log.Println(config)
	files["vault.hcl"] = config
	return files
}

func HarnessToAPI(r runner.Harness) (*vaultapi.Client, error) {
	apicfg, err := r.Endpoint("http", true)
	if err != nil {
		return nil, err
	}
	return apiConfigToClient(apicfg)
}

func apiConfigToClient(a *runner.APIConfig) (*vaultapi.Client, error) {
	cfg := vaultapi.DefaultConfig()
	cfg.Address = a.Address.String()
	cfg.ConfigureTLS(&vaultapi.TLSConfig{
		CACert: a.CAFile,
	})
	return vaultapi.NewClient(cfg)
}

type leaderShim struct {
	client *vaultapi.Client
}

var _ runner.LeaderPeersAPI = leaderShim{}

func (l leaderShim) Leader() (string, error) {
	resp, err := l.client.Sys().Leader()
	if err != nil {
		return "", err
	}
	return resp.LeaderAddress, nil
}

// TODO this only works for raft
func (l leaderShim) Peers() ([]string, error) {
	resp, err := l.client.Logical().Read("sys/storage/raft/configuration")
	if err != nil {
		return nil, err
	}

	var peers []string
	serverMap := resp.Data["config"].(map[string]interface{})["servers"].(map[string]interface{})
	for _, serverIface := range serverMap {
		server := serverIface.(map[string]interface{})
		addr := server["address"].(string)
		peers = append(peers, addr)
	}
	return peers, nil
}

func vaultLeaderAPIs(servers []runner.Harness) ([]runner.LeaderAPI, error) {
	var ret []runner.LeaderAPI
	for _, server := range servers {
		api, err := HarnessToAPI(server)
		if err != nil {
			return nil, err
		}
		ret = append(ret, &leaderShim{client: api})
	}
	return ret, nil
}

func LeadersHealthy(ctx context.Context, servers []runner.Harness) error {
	apis, err := vaultLeaderAPIs(servers)
	if err != nil {
		return err
	}
	return runner.LeaderAPIsHealthy(ctx, apis)
}

func Initialize(ctx context.Context, cli *vaultapi.Client, seal *Seal) (string, []string, error) {
	req := &vaultapi.InitRequest{
		SecretShares:    1,
		SecretThreshold: 1,
	}
	if seal != nil {
		req.RecoveryShares = 1
		req.RecoveryThreshold = 1
	}
	resp, err := cli.Sys().Init(req)
	switch {
	case err == nil && seal != nil:
		return resp.RootToken, resp.RecoveryKeys, nil
	case err == nil && seal == nil:
		return resp.RootToken, resp.Keys, nil
	default:
		return "", nil, err
	}
}

func Status(ctx context.Context, cli *vaultapi.Client) (*vaultapi.SealStatusResponse, error) {
	var err error
	for ctx.Err() == nil {
		var sealResp *vaultapi.SealStatusResponse
		sealResp, err = cli.Sys().SealStatus()
		if err == nil {
			return sealResp, nil
		}
		time.Sleep(1000 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("timeout trying to check seal status, last attempt error: %v", err)
	}
	return nil, ctx.Err()
}

func Unseal(ctx context.Context, cli *vaultapi.Client, key string, migrate bool) error {
	resp, err := cli.Sys().UnsealWithOptions(&vaultapi.UnsealOpts{
		Key:     key,
		Migrate: migrate,
	})
	if err != nil {
		return err
	}
	if resp.Sealed || !resp.Initialized {
		return fmt.Errorf("expected to get an unsealed initialized response, got: %v", resp)
	}

	// This shouldn't be necessary, we validate the seal status just to be sure
	// that if subsequent seal status checks fail, it's because something changed.
	for ctx.Err() == nil {
		var resp *vaultapi.SealStatusResponse
		resp, err = cli.Sys().SealStatus()
		if resp != nil && !resp.Sealed {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("unseal failed, last error: %v", err)
}

func NewSealSource(ctx context.Context, cli *vaultapi.Client, uniqueID string) (*Seal, error) {
	rootPath := "transit"
	err := cli.Sys().Mount(rootPath, &vaultapi.MountInput{
		Type: "transit",
	})
	if err != nil {
		// If we get an error mounting, see if it's already mounted.
		mounts, listerr := cli.Sys().ListMounts()
		if listerr != nil {
			return nil, err
		}
		var found bool
		for path, mount := range mounts {
			if mount.Type == "transit" && path == "transit/" {
				found = true
				break
			}
		}
		if !found {
			return nil, err
		}
	}

	_, err = cli.Logical().Write("transit/keys/"+uniqueID, nil)
	if err != nil {
		return nil, err
	}

	err = cli.Sys().PutPolicy("transit-seal-client", fmt.Sprintf(`
path "transit/encrypt/%s" {
  capabilities = ["update"]
}

path "transit/decrypt/%s" {
  capabilities = ["update"]
}
`, uniqueID, uniqueID))
	if err != nil {
		return nil, err
	}

	secret, err := cli.Logical().Write("auth/token/create", map[string]interface{}{
		"no_parent": true,
		"policies":  []string{"transit-seal-client"},
	})
	if err != nil {
		return nil, err
	}

	return &Seal{
		Type: "transit",
		Config: map[string]string{
			"address":         cli.Address(),
			"token":           secret.Auth.ClientToken,
			"key_name":        uniqueID,
			"mount_path":      "transit/",
			"tls_skip_verify": "true",
		},
	}, nil
}

var ServerScrapeConfig = prometheus.ScrapeConfig{
	JobName:     "vault",
	Params:      url.Values{"format": []string{"prometheus"}},
	MetricsPath: "/v1/sys/metrics",
}

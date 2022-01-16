package vault

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-multierror"
	vaultapi "github.com/hashicorp/vault/api"
	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/prometheus"
	"github.com/ncabatoff/yurt/runner"
	"go.uber.org/atomic"
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

// VaultConfig describes how to run a single Vault node.
type VaultConfig struct {
	Common runner.Config
	// JoinAddrs specifies the addresses of the Vault servers in the cluster.
	// If they have a :port suffix, it should be the API address, otherwise
	// 8200 is assumed. Only used when joining new Raft nodes to the cluster.
	JoinAddrs []string
	// ConsulAddr gives the host:port for this node's Consul agent.
	// Only needed for Consul storage or service registration.
	ConsulAddr string
	// ConsulPath gives the Consul KV prefix where Vault will store its data.
	// Only needed for Consul storage.
	ConsulPath string
	// Seal is used for non-Shamir seals, i.e. AutoUnseal.
	Seal *Seal
	// OldSeal is used in seal migration scenarios. When migrating away from
	// a non-Shamir seal, the old seal's config stanza must be kept in the
	// config file, with a new disabled="true" keyval.  Once migration has
	// completed successfully on all nodes, the old seal stanza should be removed.
	OldSeal            *Seal
	RaftPerfMultiplier int
}

func (vc VaultConfig) Config() runner.Config {
	return vc.Common
}

func (vc VaultConfig) Name() string {
	return "vault"
}

func NewRaftConfig(joinAddrs []string, tls *pki.TLSConfigPEM, raftPerfMultiplier int) VaultConfig {
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
		RaftPerfMultiplier: raftPerfMultiplier,
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
	return []string{"server", "-config=" + vc.Common.ConfigDir}
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
	if len(vc.JoinAddrs) > 1 {
		for _, j := range vc.JoinAddrs {
			retryJoin += fmt.Sprintf(`
  retry_join {
    leader_api_addr = "%s://%s"
    %s
  }`, scheme, j, ca)
		}
	}

	perfMultiplier := 1
	if vc.RaftPerfMultiplier > 0 {
		perfMultiplier = vc.RaftPerfMultiplier
	}
	return fmt.Sprintf(`
storage "raft" {
  path = "%s"
  node_id = "%s"
  performance_multiplier = "%d"
  %s
}
`, vc.Common.DataDir, vc.Common.NodeName, perfMultiplier, retryJoin)
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
	scheme := "http"
	networkCIDR := "127.0.0.0/8"
	if vc.Common.NetworkConfig.Network != nil {
		networkCIDR = vc.Common.NetworkConfig.Network.String()
	}
	network := fmt.Sprintf(`{{- GetAllInterfaces | include "network" "%s" | attr "address" -}}`, networkCIDR)
	var tlsConfig string
	files := map[string]string{}
	if vc.Common.TLS.Cert != "" {
		scheme = "https"

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

	listenerAddr := fmt.Sprintf("%s:%d", network, vc.Common.Ports.ByName[PortNames.HTTP].Number)
	apiAddr := fmt.Sprintf("%s://%s", scheme, listenerAddr)
	clusterAddr := fmt.Sprintf("https://%s:%d", network, vc.Common.Ports.ByName[PortNames.Cluster].Number)
	config := fmt.Sprintf(`
disable_mlock = true
log_level = "info"
log_requests_level = "trace"
ui = true
api_addr = <<EOF
%s
EOF
cluster_addr = <<EOF
%s
EOF
listener "tcp" {
  telemetry {
	unauthenticated_metrics_access = true
  }
  address = <<EOF
%s
EOF
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

	//log.Println(config)
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
	cfg.MinRetryWait = 50 * time.Millisecond
	cfg.Address = a.Address.String()
	err := cfg.ConfigureTLS(&vaultapi.TLSConfig{
		CACert: a.CAFile,
	})
	if err != nil {
		return nil, err
	}
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

func Leader(servers []runner.Harness) (string, error) {
	apis, err := vaultLeaderAPIs(servers)
	if err != nil {
		return "", err
	}
	return runner.LeaderAPIsHealthyNow(apis)
}

// RaftAutopilotHealthy returns nil if any of the servers report Autopilot healthy,
// or the errors obtained.  Autopilot health requests are always forwarded to the leader,
// and the leader won't report a healthy cluster if any peers fail health checks.
// Health checks are usually thresholds for replication lag and last-contact.
func RaftAutopilotHealthy(ctx context.Context, servers []runner.Harness, token string) error {
	return AnyVault(ctx, servers, func(client *vaultapi.Client) error {
		client.SetToken(token)
		state, err := client.Sys().RaftAutopilotState()
		if err != nil {
			return err
		}
		if state != nil && state.Healthy {
			var buf bytes.Buffer
			for name, state := range state.Servers {
				buf.WriteString(fmt.Sprintf("%s=%#v, ", name, state.Healthy))
			}
			buf.Truncate(buf.Len() - 2)
			log.Printf("got healthy apstate from %s: %s", client.Address(), buf.String())
			return nil
		}
		return fmt.Errorf("unhealthy")
	})
}

// AnyVault returns nil if f returns a non-nil result for any of the given servers.
// Errors will be retried with a short constant delay so long as ctx.Err() returns nil.
func AnyVault(ctx context.Context, servers []runner.Harness, f func(*vaultapi.Client) error) error {
	errs := make([]error, len(servers))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	var success = atomic.NewBool(false)
	for i, server := range servers {
		client, err := HarnessToAPI(server)
		if err != nil {
			// An error here indicates a broken config, and has no bearing on
			// the health of the server.
			return err
		}
		wg.Add(1)
		go func(client *vaultapi.Client) {
			defer wg.Done()
			for ctx.Err() == nil {
				errs[i] = f(client)
				if errs[i] == nil {
					success.Store(true)
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		}(client)
	}
	wg.Wait()

	if success.Load() {
		return nil
	}
	return multierror.Append(nil, errs...)
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
		time.Sleep(100 * time.Millisecond)
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
		time.Sleep(100 * time.Millisecond)
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

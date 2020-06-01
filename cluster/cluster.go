package cluster

import (
	"context"
	"fmt"
	"time"

	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/consul"
	"github.com/ncabatoff/yurt/nomad"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runenv"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/vault"
	"golang.org/x/sync/errgroup"
)

type ConsulCertificateMaker struct {
	ca  *pki.CertificateAuthority
	ttl string
}

var _ yurt.CertificateMaker = &ConsulCertificateMaker{}

func (c ConsulCertificateMaker) MakeCertificate(ctx context.Context, hostname, ip string) (*pki.TLSConfigPEM, error) {
	return c.ca.ConsulServerTLS(ctx, ip, c.ttl)
}

func NewConsulCluster(ctx context.Context, e runenv.Env, name string, nodeCount int) (*ConsulCluster, error) {
	cluster := ConsulCluster{group: &errgroup.Group{}}
	var nodes []yurt.Node
	for i := 0; i < nodeCount; i++ {
		node := e.AllocNode(name+"-consul-srv", consul.DefPorts().RunnerPorts())
		joinAddr, err := node.Address(consul.PortNames.SerfLAN)
		if err != nil {
			return nil, err
		}
		cluster.joinAddrs = append(cluster.joinAddrs, joinAddr)

		serverAddr, err := node.Address(consul.PortNames.Server)
		if err != nil {
			return nil, err
		}
		cluster.peerAddrs = append(cluster.peerAddrs, serverAddr)

		nodes = append(nodes, node)
	}

	for _, node := range nodes {
		cfg := consul.NewConfig(true, cluster.joinAddrs)
		h, err := e.Run(ctx, cfg, node)
		if err != nil {
			return nil, err
		}
		cluster.servers = append(cluster.servers, h)
		cluster.group.Go(h.Wait)
	}

	if err := consul.LeadersHealthy(ctx, cluster.servers, cluster.peerAddrs); err != nil {
		return nil, err
	}

	return &cluster, nil
}

type ConsulCluster struct {
	servers   []runner.Harness
	group     *errgroup.Group
	joinAddrs []string
	peerAddrs []string
}

func (c *ConsulCluster) Client(ctx context.Context, e runenv.Env, name string) (runner.Harness, error) {
	return e.Run(ctx, consul.NewConfig(false, c.joinAddrs), e.AllocNode(name, consul.DefPorts().RunnerPorts()))
}

func (c *ConsulCluster) Wait() error {
	return c.group.Wait()
}

func (c *ConsulCluster) Stop() {
	for _, s := range c.servers {
		_ = s.Stop()
	}
}

func NewNomadCluster(ctx context.Context, e runenv.Env, name string, nodeCount int, consulCluster *ConsulCluster) (*NomadCluster, error) {
	cluster := NomadCluster{group: &errgroup.Group{}}
	var nodes []yurt.Node
	for i := 0; i < nodeCount; i++ {
		node := e.AllocNode(name+"-nomad-srv", nomad.DefPorts().RunnerPorts())
		nodes = append(nodes, node)
		cluster.peerAddrs = append(cluster.peerAddrs,
			fmt.Sprintf("%s:%d", node.Host, node.Ports.ByName[nomad.PortNames.RPC].Number))
	}

	for _, node := range nodes {
		consulHarness, err := consulCluster.Client(ctx, e, name+"-consul-cli")
		if err != nil {
			return nil, err
		}
		cluster.consulAgents = append(cluster.consulAgents, consulHarness)
		cluster.group.Go(consulHarness.Wait)

		consulAddr, err := consulHarness.Endpoint("http", false)
		if err != nil {
			cluster.Stop()
			return nil, err
		}
		cfg := nomad.NewConfig(nodeCount, consulAddr.Address.Host)
		nomadHarness, err := e.Run(ctx, cfg, node)
		if err != nil {
			cluster.Stop()
			return nil, err
		}
		cluster.servers = append(cluster.servers, nomadHarness)
		cluster.group.Go(nomadHarness.Wait)
	}

	if err := nomad.LeadersHealthy(ctx, cluster.servers, cluster.peerAddrs); err != nil {
		cluster.Stop()
		return nil, err
	}

	return &cluster, nil
}

type NomadCluster struct {
	consulAgents []runner.Harness
	servers      []runner.Harness
	group        *errgroup.Group
	peerAddrs    []string
}

func (c *NomadCluster) Wait() error {
	return c.group.Wait()
}

func (c *NomadCluster) Stop() {
	for _, s := range c.servers {
		_ = s.Stop()
	}
	for _, a := range c.consulAgents {
		_ = a.Stop()
	}
}

func (c *NomadCluster) Client(ctx context.Context, e runenv.Env, name, consulAddr string) (runner.Harness, error) {
	return e.Run(ctx, nomad.NomadConfig{
		ConsulAddr: consulAddr,
	}, e.AllocNode(name, nomad.DefPorts().RunnerPorts()))
}

type ConsulNomadCluster struct {
	Name   string
	Consul *ConsulCluster
	Nomad  *NomadCluster
}

func NewConsulNomadCluster(ctx context.Context, e runenv.Env, name string, nodeCount int) (*ConsulNomadCluster, error) {
	consulCluster, err := NewConsulCluster(ctx, e, name, nodeCount)
	if err != nil {
		return nil, err
	}
	e.Go(consulCluster.Wait)

	nomadCluster, err := NewNomadCluster(ctx, e, name, nodeCount, consulCluster)
	if err != nil {
		return nil, err
	}
	e.Go(nomadCluster.Wait)

	return &ConsulNomadCluster{
		Name:   name,
		Consul: consulCluster,
		Nomad:  nomadCluster,
	}, nil
}

func (c *ConsulNomadCluster) Wait() error {
	var g errgroup.Group
	g.Go(c.Nomad.Wait)
	g.Go(c.Consul.Wait)
	return g.Wait()
}

func (c *ConsulNomadCluster) Stop() {
	c.Nomad.Stop()
	c.Consul.Stop()
}

type NomadClient struct {
	ConsulHarness runner.Harness
	NomadHarness  runner.Harness
}

func (c *ConsulNomadCluster) NomadClient(e runenv.Env) (*NomadClient, error) {
	consulHarness, err := c.Consul.Client(e.Context(), e, c.Name+"-consul-cli")
	if err != nil {
		return nil, err
	}
	consulAddr, err := consulHarness.Endpoint("http", false)
	if err != nil {
		return nil, err
	}
	nomadHarness, err := c.Nomad.Client(e.Context(), e, c.Name+"-nomad-cli", consulAddr.Address.Host)
	if err != nil {
		_ = consulHarness.Stop()
		return nil, err
	}
	return &NomadClient{
		ConsulHarness: consulHarness,
		NomadHarness:  nomadHarness,
	}, nil
}

func (c *NomadClient) Stop() {
	_ = c.NomadHarness.Stop()
	_ = c.ConsulHarness.Stop()
}

func (c *NomadClient) Wait() error {
	var g errgroup.Group
	g.Go(c.NomadHarness.Wait)
	g.Go(c.ConsulHarness.Wait)
	return g.Wait()
}

func NewVaultCluster(ctx context.Context, e runenv.Env, name string, nodeCount int, parallelStart bool, consulAddrs []string) (c *VaultCluster, err error) {
	cluster := VaultCluster{
		group:       &errgroup.Group{},
		consulAddrs: consulAddrs,
	}
	defer func() {
		if err != nil {
			cluster.Stop()
		}
	}()

	nodes := make([]yurt.Node, nodeCount)
	for i := 0; i < nodeCount; i++ {
		nodes[i] = e.AllocNode(name+"-vault-srv", vault.DefPorts().RunnerPorts())
		joinAddr, err := nodes[i].Address(vault.PortNames.HTTP)
		if err != nil {
			return nil, err
		}
		cluster.joinAddrs = append(cluster.joinAddrs, joinAddr)
	}

	if !parallelStart {
		err = cluster.addNode(ctx, e, nodes[0], "")
		if err != nil {
			return nil, err
		}

		cli, err := vault.HarnessToAPI(cluster.servers[0])
		if err != nil {
			return nil, err
		}

		_, err = vault.Status(ctx, cli)
		if err != nil {
			return nil, err
		}

		cluster.rootToken, cluster.unsealKeys, err = vault.Initialize(ctx, cli)
		if err != nil {
			return nil, err
		}
	}

	for i, node := range nodes {
		if i > 0 || parallelStart {
			var consulAddr string
			if i < len(consulAddrs) {
				consulAddr = consulAddrs[i]
			}
			if err := cluster.addNode(ctx, e, node, consulAddr); err != nil {
				return nil, err
			}
		}
		cli, err := vault.HarnessToAPI(cluster.servers[i])
		if err != nil {
			return nil, err
		}

		_, err = vault.Status(ctx, cli)
		if err != nil {
			return nil, err
		}

		if i == 0 && parallelStart {
			cluster.rootToken, cluster.unsealKeys, err = vault.Initialize(ctx, cli)
			if err != nil {
				return nil, err
			}
		}

		for ctx.Err() == nil {
			// TODO support multiple keys
			err = vault.Unseal(ctx, cli, cluster.unsealKeys[0])
			if err == nil {
				break
			}
			if i == 0 {
				return nil, err
			}
			time.Sleep(500 * time.Millisecond)
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("timeout waiting for standby node to unseal successfully, last error: %v", err)
		}
		if err := vault.LeadersHealthy(ctx, []runner.Harness{cluster.servers[i]}); err != nil {
			return nil, err
		}
	}

	if err := vault.LeadersHealthy(ctx, cluster.servers); err != nil {
		return nil, err
	}

	return &cluster, nil
}

type VaultCluster struct {
	servers     []runner.Harness
	group       *errgroup.Group
	joinAddrs   []string
	consulAddrs []string
	rootToken   string
	unsealKeys  []string
}

func (c *VaultCluster) addNode(ctx context.Context, e runenv.Env, node yurt.Node, consulAddr string) error {
	var cfg vault.VaultConfig
	if consulAddr != "" {
		cfg = vault.NewConsulConfig(consulAddr, "vault")
	} else {
		cfg = vault.NewRaftConfig(c.joinAddrs)
	}

	h, err := e.Run(ctx, cfg, node)
	if err != nil {
		return err
	}
	c.servers = append(c.servers, h)
	c.group.Go(h.Wait)
	return nil
}

func (c *VaultCluster) Wait() error {
	return c.group.Wait()
}

func (c *VaultCluster) Stop() {
	for _, s := range c.servers {
		_ = s.Stop()
	}
}

type ConsulVaultCluster struct {
	Name         string
	Consul       *ConsulCluster
	Vault        *VaultCluster
	consulAgents []runner.Harness
	group        *errgroup.Group
}

func NewConsulVaultCluster(ctx context.Context, e runenv.Env, name string, nodeCount int) (*ConsulVaultCluster, error) {
	consulCluster, err := NewConsulCluster(ctx, e, name, nodeCount)
	if err != nil {
		return nil, err
	}
	e.Go(consulCluster.Wait)

	cluster := &ConsulVaultCluster{
		Name:   name,
		Consul: consulCluster,
		group:  &errgroup.Group{},
	}

	var consulAddrs []string
	for i := 0; i < nodeCount; i++ {
		consulHarness, err := consulCluster.Client(ctx, e, name+"-consul-cli")
		if err != nil {
			return nil, err
		}
		cluster.consulAgents = append(cluster.consulAgents, consulHarness)
		cluster.group.Go(consulHarness.Wait)

		consulAddr, err := consulHarness.Endpoint("http", false)
		if err != nil {
			cluster.Stop()
			return nil, err
		}
		consulAddrs = append(consulAddrs, consulAddr.Address.Host)
	}

	cluster.Vault, err = NewVaultCluster(ctx, e, name, nodeCount, true, consulAddrs)
	if err != nil {
		return nil, err
	}
	e.Go(cluster.Vault.Wait)

	return cluster, nil
}

func (c *ConsulVaultCluster) Wait() error {
	var g errgroup.Group
	g.Go(c.Vault.Wait)
	g.Go(c.Consul.Wait)
	return g.Wait()
}

func (c *ConsulVaultCluster) Stop() {
	c.Vault.Stop()
	c.Consul.Stop()
}

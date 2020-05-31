package cluster

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt/consul"
	"github.com/ncabatoff/yurt/nomad"
	"github.com/ncabatoff/yurt/runenv"

	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
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
		nodes = append(nodes, node)
		cluster.joinAddrs = append(cluster.joinAddrs,
			fmt.Sprintf("%s:%d", node.StaticIP, node.Ports.ByName[consul.PortNames.SerfLAN].Number))
		cluster.peerAddrs = append(cluster.peerAddrs,
			fmt.Sprintf("%s:%d", node.StaticIP, node.Ports.ByName[consul.PortNames.Server].Number))
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

	if err := runner.ConsulRunnersHealthy(ctx, cluster.servers, cluster.peerAddrs); err != nil {
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
			fmt.Sprintf("%s:%d", node.StaticIP, node.Ports.ByName[nomad.PortNames.RPC].Number))
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

	if err := runner.NomadRunnersHealthy(ctx, cluster.servers, cluster.peerAddrs); err != nil {
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

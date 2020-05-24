package cluster

import (
	"context"
	"fmt"
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
		node := e.AllocNode(name+"-consul-srv", 5)
		nodes = append(nodes, node)
		ports := runner.SeqConsulPorts(node.FirstPort)
		cluster.joinAddrs = append(cluster.joinAddrs, fmt.Sprintf("%s:%d", node.StaticIP, ports.SerfLAN))
		cluster.peerAddrs = append(cluster.peerAddrs, fmt.Sprintf("%s:%d", node.StaticIP, ports.Server))
	}

	for _, node := range nodes {
		cfg := runner.ConsulServerConfig{ConsulConfig: runner.ConsulConfig{
			JoinAddrs: cluster.joinAddrs,
		}}
		r, _, err := e.Run(ctx, cfg, node)
		if err != nil {
			return nil, err
		}
		cluster.servers = append(cluster.servers, r)
		cluster.group.Go(r.Wait)
	}

	if err := runner.ConsulRunnersHealthy(ctx, cluster.servers, cluster.peerAddrs); err != nil {
		return nil, err
	}

	return &cluster, nil
}

type ConsulCluster struct {
	servers   []runner.ConsulRunner
	group     *errgroup.Group
	joinAddrs []string
	peerAddrs []string
}

func (c *ConsulCluster) Client(ctx context.Context, e runenv.Env, name string) (runner.ConsulRunner, yurt.Node, error) {
	return e.Run(ctx, runner.ConsulConfig{JoinAddrs: c.joinAddrs}, e.AllocNode(name, 5))
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
		node := e.AllocNode(name+"-nomad-srv", 5)
		nodes = append(nodes, node)
		ports := runner.SeqNomadPorts(node.FirstPort)
		cluster.peerAddrs = append(cluster.peerAddrs, fmt.Sprintf("%s:%d", node.StaticIP, ports.RPC))
	}

	for _, node := range nodes {
		consul, consulNode, err := consulCluster.Client(ctx, e, name+"-consul-cli")
		if err != nil {
			return nil, err
		}
		cluster.consulAgents = append(cluster.consulAgents, consul)
		cluster.group.Go(consul.Wait)

		cfg := runner.NomadServerConfig{
			BootstrapExpect: nodeCount,
			NomadConfig: runner.NomadConfig{
				ConsulAddr: fmt.Sprintf("%s:%d", consulNode.StaticIP, consul.Command().Config().APIPort),
			},
		}
		r, _, err := e.Run(ctx, cfg, node)
		if err != nil {
			cluster.Stop()
			return nil, err
		}
		cluster.servers = append(cluster.servers, r)
		cluster.group.Go(r.Wait)
	}

	if err := runner.NomadRunnersHealthy(ctx, cluster.servers, cluster.peerAddrs); err != nil {
		cluster.Stop()
		return nil, err
	}

	return &cluster, nil
}

type NomadCluster struct {
	consulAgents []runner.ConsulRunner
	servers      []runner.NomadRunner
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

func (c *NomadCluster) Client(ctx context.Context, e runenv.Env, name, consulAddr string) (runner.NomadRunner, yurt.Node, error) {
	return e.Run(ctx, runner.NomadClientConfig{
		NomadConfig: runner.NomadConfig{
			ConsulAddr: consulAddr,
		},
	}, e.AllocNode(name, 3))
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
	runner.ConsulRunner
	runner.NomadRunner
}

func (c *ConsulNomadCluster) NomadClient(e runenv.Env) (*NomadClient, error) {
	consulClient, consulClientNode, err := c.Consul.Client(e.Context(), e, c.Name+"-consul-cli")
	if err != nil {
		return nil, err
	}
	consulAddr := fmt.Sprintf("%s:%d", consulClientNode.StaticIP, consulClient.Command().Config().APIPort)
	nomadClient, _, err := c.Nomad.Client(e.Context(), e, c.Name+"-nomad-cli", consulAddr)
	if err != nil {
		_ = consulClient.Stop()
		return nil, err
	}
	return &NomadClient{
		ConsulRunner: consulClient,
		NomadRunner:  nomadClient,
	}, nil
}

func (c *NomadClient) Stop() {
	_ = c.NomadRunner.Stop()
	_ = c.ConsulRunner.Stop()
}

func (c *NomadClient) Wait() error {
	var g errgroup.Group
	g.Go(c.NomadRunner.Wait)
	g.Go(c.ConsulRunner.Wait)
	return g.Wait()
}

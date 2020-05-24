package runenv

import (
	"fmt"
	"testing"
	"time"

	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/runner"
)

func TestConsulExec(t *testing.T) {
	e, cleanup := NewExecTestEnv(t, 30*time.Second)
	defer cleanup()
	r, _ := runConsulServer(t, e)
	e.Go(r.Wait)
}

func TestConsulExecClient(t *testing.T) {
	e, cleanup := NewExecTestEnv(t, 30*time.Second)
	defer cleanup()
	r, server := runConsulServer(t, e)
	e.Go(r.Wait)
	runConsul(t, e, &server)
}

func runConsulServer(t *testing.T, e Env) (runner.APIRunner, yurt.Node) {
	return runConsul(t, e, nil)
}

func runConsul(t *testing.T, e Env, server *yurt.Node) (runner.APIRunner, yurt.Node) {
	node := e.AllocNode(t.Name()+"-consul", 5)
	ports := runner.SeqConsulPorts(node.FirstPort)
	var command runner.Command = &runner.ConsulServerConfig{
		ConsulConfig: runner.ConsulConfig{
			JoinAddrs: []string{fmt.Sprintf("%s:%d", node.StaticIP, ports.SerfLAN)},
		},
	}
	expectedPeerAddrs := []string{fmt.Sprintf("%s:%d", node.StaticIP, ports.Server)}
	if server != nil {
		command = &runner.ConsulConfig{
			JoinAddrs: []string{fmt.Sprintf("%s:%d", server.StaticIP, runner.SeqConsulPorts(server.FirstPort).SerfLAN)},
		}
		expectedPeerAddrs = []string{fmt.Sprintf("%s:%d", server.StaticIP, runner.SeqConsulPorts(server.FirstPort).Server)}
	}

	r, node, err := e.Run(e.Context(), command, node)
	if err != nil {
		t.Fatal(err)
	}

	if err := runner.ConsulRunnersHealthy(e.Context(), []runner.ConsulRunner{r}, expectedPeerAddrs); err != nil {
		t.Fatal(err)
	}
	return r, node
}

func TestNomadExec(t *testing.T) {
	e, cleanup := NewExecTestEnv(t, 30*time.Second)
	defer cleanup()
	consul, consulNode := runConsulServer(t, e)
	nomad, _ := runNomad(t, e, consulNode)
	e.Go(consul.Wait)
	e.Go(nomad.Wait)
}

func runNomad(t *testing.T, e Env, consulNode yurt.Node) (runner.APIRunner, yurt.Node) {
	node := e.AllocNode(t.Name()+"-nomad", 5)
	ports := runner.SeqNomadPorts(node.FirstPort)
	cfg := runner.NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: runner.NomadConfig{
			ConsulAddr: fmt.Sprintf("%s:%d", consulNode.StaticIP, runner.SeqConsulPorts(consulNode.FirstPort).HTTP),
		},
	}

	r, node, err := e.Run(e.Context(), cfg, node)
	if err != nil {
		t.Fatal(err)
	}

	expectedPeerAddrs := []string{fmt.Sprintf("%s:%d", node.StaticIP, ports.RPC)}
	if err := runner.NomadRunnersHealthy(e.Context(), []runner.NomadRunner{r}, expectedPeerAddrs); err != nil {
		t.Fatal(err)
	}
	return r, node
}

func TestConsulDocker(t *testing.T) {
	e, cleanup := NewDockerTestEnv(t, 30*time.Second)
	defer cleanup()
	r, _ := runConsulServer(t, e)
	e.Go(r.Wait)
}

func TestConsulDockerClient(t *testing.T) {
	e, cleanup := NewDockerTestEnv(t, 30*time.Second)
	defer cleanup()
	r, server := runConsulServer(t, e)
	e.Go(r.Wait)
	runConsul(t, e, &server)
}

func TestNomadDocker(t *testing.T) {
	e, cleanup := NewDockerTestEnv(t, 30*time.Second)
	defer cleanup()
	consul, consulNode := runConsulServer(t, e)
	nomad, _ := runNomad(t, e, consulNode)
	e.Go(consul.Wait)
	e.Go(nomad.Wait)
}

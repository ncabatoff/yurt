package runenv

import (
	"context"
	"github.com/ncabatoff/yurt/prometheus"
	"testing"
	"time"

	"github.com/ncabatoff/yurt/consul"
	"github.com/ncabatoff/yurt/nomad"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/vault"
)

func TestConsulExec(t *testing.T) {
	e, cleanup := NewExecTestEnv(t, 5*time.Second)
	defer cleanup()

	e.Go(runConsulServer(t, e).Wait)
}

func TestConsulExecClient(t *testing.T) {
	e, cleanup := NewExecTestEnv(t, 5*time.Second)
	defer cleanup()
	consulHarness := runConsulServer(t, e)
	e.Go(consulHarness.Wait)
	runConsul(t, e, consulHarness)
}

func runConsulServer(t *testing.T, e Env) runner.Harness {
	node := e.AllocNode(t.Name()+"-consul", consul.DefPorts().RunnerPorts())
	joinAddr, err := node.Address(consul.PortNames.SerfLAN)
	if err != nil {
		t.Fatal(err)
	}
	command := consul.NewConfig(true, []string{joinAddr}, nil)

	h, err := e.Run(e.Context(), command, node)
	if err != nil {
		t.Fatal(err)
	}

	serverAddr, err := node.Address(consul.PortNames.Server)
	if err != nil {
		t.Fatal(err)
	}
	if err := consul.LeadersHealthy(e.Context(), []runner.Harness{h}, []string{serverAddr}); err != nil {
		t.Fatal(err)
	}
	return h
}

// Start a consul agent in client mode, joining to the provided consul server.
func runConsul(t *testing.T, e Env, server runner.Harness) runner.Harness {
	serfAddr, err := server.Endpoint(consul.PortNames.SerfLAN, false)
	if err != nil {
		t.Fatal(err)
	}
	serverAddr, err := server.Endpoint(consul.PortNames.Server, false)
	if err != nil {
		t.Fatal(err)
	}
	command := consul.NewConfig(false, []string{serfAddr.Address.Host}, nil)
	expectedPeerAddrs := []string{serverAddr.Address.Host}

	h, err := e.Run(e.Context(), command, e.AllocNode(t.Name()+"consul-cli", consul.DefPorts().RunnerPorts()))
	if err != nil {
		t.Fatal(err)
	}

	if err := consul.LeadersHealthy(e.Context(), []runner.Harness{h}, expectedPeerAddrs); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestNomadExec(t *testing.T) {
	e, cleanup := NewExecTestEnv(t, 10*time.Second)
	defer cleanup()
	consulHarness := runConsulServer(t, e)
	e.Go(consulHarness.Wait)
	e.Go(runNomad(t, e, consulHarness).Wait)
}

func runNomad(t *testing.T, e Env, consulHarness runner.Harness) runner.Harness {
	node := e.AllocNode(t.Name()+"-nomad", nomad.DefPorts().RunnerPorts())
	consulAddr, err := consulHarness.Endpoint("http", false)
	if err != nil {
		t.Fatal(err)
	}

	command := nomad.NewConfig(1, consulAddr.Address.Host, nil)
	nomadServer, err := e.Run(e.Context(), command, node)
	if err != nil {
		t.Fatal(err)
	}

	serverAddr, err := nomadServer.Endpoint(nomad.PortNames.RPC, false)
	if err != nil {
		t.Fatal(err)
	}
	expectedPeerAddrs := []string{serverAddr.Address.Host}
	if err := nomad.LeadersHealthy(e.Context(), []runner.Harness{nomadServer}, expectedPeerAddrs); err != nil {
		t.Fatal(err)
	}
	return nomadServer
}

func TestConsulDocker(t *testing.T) {
	e, cleanup := NewDockerTestEnv(t, 10*time.Second)
	defer cleanup()
	e.Go(runConsulServer(t, e).Wait)
}

func TestConsulDockerClient(t *testing.T) {
	e, cleanup := NewDockerTestEnv(t, 10*time.Second)
	defer cleanup()
	h := runConsulServer(t, e)
	e.Go(h.Wait)
	runConsul(t, e, h)
}

func TestNomadDocker(t *testing.T) {
	e, cleanup := NewDockerTestEnv(t, 15*time.Second)
	defer cleanup()
	consul := runConsulServer(t, e)
	nomad := runNomad(t, e, consul)
	e.Go(consul.Wait)
	e.Go(nomad.Wait)
}

func TestVaultExec(t *testing.T) {
	e, cleanup := NewExecTestEnv(t, 15*time.Second)
	defer cleanup()

	v1, _ := runVaultServer(t, e, "", nil)
	e.Go(v1.Wait)
}

func runVaultServer(t *testing.T, e Env, consulAddr string, seal *vault.Seal) (runner.Harness, string) {
	node := e.AllocNode(t.Name()+"-vault", vault.DefPorts().RunnerPorts())
	apiAddr, err := node.Address(vault.PortNames.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	var vcfg vault.VaultConfig
	if consulAddr != "" {
		vcfg = vault.NewConsulConfig(consulAddr, "vault", nil)
	} else {
		vcfg = vault.NewRaftConfig([]string{apiAddr}, nil)
	}
	vcfg.Seal = seal

	h, err := e.Run(e.Context(), vcfg, node)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := vault.HarnessToAPI(h)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(e.Context(), 10*time.Second)
	defer cancel()

	_, err = vault.Status(ctx, cli)
	if err != nil {
		t.Fatal(err)
	}

	var unsealKeys []string
	var rootToken string
	//if !sealStatus.Initialized {
	rootToken, unsealKeys, err = vault.Initialize(ctx, cli, seal)
	if err != nil {
		t.Fatal(err)
	}
	//}

	err = vault.Unseal(ctx, cli, unsealKeys[0], false)
	if err != nil {
		t.Fatal(err)
	}

	if err := vault.LeadersHealthy(e.Context(), []runner.Harness{h}); err != nil {
		t.Fatal(err)
	}
	return h, rootToken
}

func TestVaultExecTransitSeal(t *testing.T) {
	e, cleanup := NewExecTestEnv(t, 30*time.Second)
	defer cleanup()

	v1, v1root := runVaultServer(t, e, "", nil)
	e.Go(v1.Wait)

	cli, err := vault.HarnessToAPI(v1)
	if err != nil {
		t.Fatal(err)
	}
	cli.SetToken(v1root)
	seal, err := vault.NewSealSource(e.Ctx, cli, t.Name())
	if err != nil {
		t.Fatal(err)
	}

	v2, _ := runVaultServer(t, e, "", seal)
	e.Go(v2.Wait)
}

func TestPrometheusExec(t *testing.T) {
	e, cleanup := NewExecTestEnv(t, 60*time.Second)
	defer cleanup()
	promHarness := runPrometheusServer(t, e)
	e.Go(promHarness.Wait)
}

func runPrometheusServer(t *testing.T, e Env) runner.Harness {
	node := e.AllocNode(t.Name()+"-prometheus", prometheus.DefPorts().RunnerPorts())
	command := prometheus.NewConfig(nil, nil)

	h, err := e.Run(e.Context(), command, node)
	if err != nil {
		t.Fatal(err)
	}

	serverAddr, err := node.Address(prometheus.PortNames.HTTP)
	if err != nil {
		t.Fatal(err)
	}
	if err := prometheus.HealthCheck(e.Context(), "http://"+serverAddr); err != nil {
		t.Fatal(err)
	}
	return h
}

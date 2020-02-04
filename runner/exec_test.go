package runner

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

func TestConsulExec(t *testing.T) {
	consulPath, _ := getConsulNomadBinaries(t)
	tmpDir, ctx, cancel := testSetup(t, 10*time.Second)
	defer cancel()
	g, ctx := errgroup.WithContext(ctx)

	runner, _ := NewConsulExecRunner(consulPath, ConsulServerConfig{
		ConsulConfig{
			NodeName:  "consul-test",
			DataDir:   filepath.Join(tmpDir, "consul-data"),
			JoinAddrs: []string{"127.0.0.1:8301"},
		},
	})

	ip, err := runner.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g.Go(runner.Wait)

	go func() {
		if err := g.Wait(); err != nil {
			t.Log(err)
		}
	}()
	expectedPeerAddrs := []string{fmt.Sprintf("%s:%d", ip, 8300)}
	if err := consulRunnersHealthy(ctx, []ConsulRunner{runner}, expectedPeerAddrs); err != nil {
		t.Fatal(err)
	}
}

func threeNodeConsulExec(t *testing.T, ctx context.Context, tmpDir string) (*ConsulClusterRunner, *ConsulExecRunner) {
	t.Helper()
	consulPath, _ := getConsulNomadBinaries(t)
	firstPort := int(rand.Int31n(20000) + 2000)
	cluster, err := NewConsulClusterRunner(
		ConsulClusterConfigSingleIP{
			WorkDir:     tmpDir,
			ServerNames: []string{"consul-srv-1", "consul-srv-2", "consul-srv-3"},
			FirstPorts: ConsulPorts{
				HTTP:    firstPort,
				HTTPS:   0,
				DNS:     firstPort + 1,
				SerfLAN: firstPort + 2,
				SerfWAN: firstPort + 3,
				Server:  firstPort + 4,
			},
			PortIncrement: 5,
		},
		&ConsulExecBuilder{consulPath},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := cluster.StartServers(ctx); err != nil {
		t.Fatal(err)
	}

	clientName := "consul-cli-1"
	clientRunner, _ := NewConsulExecRunner(consulPath, ConsulConfig{
		NodeName:  clientName,
		DataDir:   filepath.Join(tmpDir, clientName, "consul", "data"),
		JoinAddrs: cluster.Config.JoinAddrs(),
	})
	if _, err := clientRunner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cluster.group.Go(clientRunner.Wait)

	go func() {
		if err := cluster.group.Wait(); err != nil {
			t.Log(err)
		}
	}()

	runners := []ConsulRunner{clientRunner}
	for _, runner := range cluster.servers {
		runners = append(runners, runner)
	}
	if err := consulRunnersHealthy(ctx, runners, cluster.Config.ServerAddrs()); err != nil {
		t.Fatal(err)
	}

	return cluster, clientRunner
}

func TestConsulExecCluster(t *testing.T) {
	tmpDir, ctx, cancel := testSetup(t, 10*time.Second)
	defer cancel()

	threeNodeConsulExec(t, ctx, tmpDir)
}

func TestNomadExec(t *testing.T) {
	consulPath, nomadPath := getConsulNomadBinaries(t)
	tmpDir, ctx, cancel := testSetup(t, 15*time.Second)
	defer cancel()
	g, ctx := errgroup.WithContext(ctx)

	consulRunner, _ := NewConsulExecRunner(consulPath, ConsulServerConfig{
		ConsulConfig{
			NodeName:  "consul-test",
			DataDir:   filepath.Join(tmpDir, "consul-data"),
			JoinAddrs: []string{"127.0.0.1:8301"},
		},
	})
	ip, err := consulRunner.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g.Go(consulRunner.Wait)

	expectedConsulPeers := []string{fmt.Sprintf("%s:%d", ip, 8300)}
	if err := consulRunnersHealthy(ctx, []ConsulRunner{consulRunner}, expectedConsulPeers); err != nil {
		t.Fatal(err)
	}

	nomadRunner, _ := NewNomadExecRunner(nomadPath, NomadServerConfig{
		BootstrapExpect: 1,
		NomadConfig: NomadConfig{
			NodeName:   "nomad-test",
			DataDir:    filepath.Join(tmpDir, "nomad-data"),
			ConfigDir:  filepath.Join(tmpDir, "nomad-cfg"),
			ConsulAddr: "127.0.0.1:8500",
		},
	})

	ip, err = nomadRunner.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g.Go(nomadRunner.Wait)

	go func() {
		if err := g.Wait(); err != nil {
			t.Log(err)
		}
	}()
	expectedNomadPeers := []string{fmt.Sprintf("%s:%d", ip, 4648)}
	if err := nomadRunnersHealthy(ctx, []NomadRunner{nomadRunner}, expectedNomadPeers); err != nil {
		t.Fatal(err)
	}
}

func TestNomadExecCluster(t *testing.T) {
	_, nomadPath := getConsulNomadBinaries(t)
	tmpDir, ctx, cancel := testSetup(t, 15*time.Second)
	defer cancel()

	consulCluster, _ := threeNodeConsulExec(t, ctx, tmpDir)

	firstPort := int(rand.Int31n(20000) + 2000)
	nomadCluster, err := NewNomadClusterRunner(
		NomadClusterConfigSingleIP{
			WorkDir:     filepath.Join(tmpDir, "nomad"),
			ServerNames: []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3"},
			FirstPorts: NomadPorts{
				HTTP: firstPort,
				Serf: firstPort + 1,
				RPC:  firstPort + 2,
			},
			PortIncrement: 3,
			ConsulAddrs:   consulCluster.Config.APIAddrs(),
		},
		&NomadExecBuilder{nomadPath},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := nomadCluster.StartServers(ctx); err != nil {
		t.Fatal(err)
	}

	var expectedNomadPeers []string
	for _, runner := range nomadCluster.servers {
		expectedNomadPeers = append(expectedNomadPeers, fmt.Sprintf("127.0.0.1:%d", runner.Config().Ports.RPC))
	}
	if err := nomadRunnersHealthy(ctx, nomadCluster.servers, expectedNomadPeers); err != nil {
		t.Fatal(err)
	}
}

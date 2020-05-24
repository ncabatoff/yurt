package cluster

import (
	"context"
	"testing"
	"time"

	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/docker"
	"github.com/ncabatoff/yurt/runenv"
)

func TestYurtRunCluster_Start(t *testing.T) {
	e, cleanup := runenv.NewDockerTestEnv(t, 600*time.Second)
	defer cleanup()

	var nodes []yurt.Node
	numNodes := 3
	var consulServerIPs []string
	for i := 0; i < numNodes; i++ {
		node := e.AllocNode(t.Name(), 0)
		node.FirstPort = 0
		nodes = append(nodes, node)
		consulServerIPs = append(consulServerIPs, node.StaticIP)
	}

	y, err := NewYurtRunCluster(YurtRunClusterOptions{
		Network:         e.NetConf,
		ConsulServerIPs: consulServerIPs,
		BaseImage:       "noenv/nomad:0.10.3",
		WorkDir:         e.WorkDir,
	}, e.DockerAPI)
	if err != nil {
		t.Fatal(err)
	}
	err = y.Start(e.Context())
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{}, 3)
	for _, cont := range y.containers {
		go func(id, name string) {
			err := docker.Wait(e.DockerAPI, id)
			if err != nil {
				t.Logf("container %q exited with err %v", name, err)
			}
			done <- struct{}{}
		}(cont.ID, cont.Name)
	}
	defer func() {
		if err := y.Stop(context.Background()); err != nil {
			t.Logf("stop err: %v", err)
		}
		<-done
		<-done
		<-done
	}()

	/*
		{
			clients, err := y.ConsulAPIs()
			if err != nil {
				t.Fatal(err)
			}

			var leaderAPIs []runner.LeaderAPI
			for _, c := range clients {
				leaderAPIs = append(leaderAPIs, c.Status())
			}

			var addrs []string
			for _, ip := range y.ConsulServerIPs {
				addrs = append(addrs, fmt.Sprintf("%s:%d", ip, runner.DefConsulPorts().Server))
			}

			if err := runner.LeaderAPIsHealthy(ctx, leaderAPIs, addrs); err != nil {
				t.Fatal(err)
			}
		}

		{
			clients, err := y.NomadAPIs()
			if err != nil {
				t.Fatal(err)
			}

			var leaderAPIs []runner.LeaderAPI
			for _, c := range clients {
				leaderAPIs = append(leaderAPIs, c.Status())
			}

			var addrs []string
			for _, ip := range y.ConsulServerIPs {
				addrs = append(addrs, fmt.Sprintf("%s:%d", ip, runner.DefNomadPorts().RPC))
			}

			if err := runner.LeaderAPIsHealthy(ctx, leaderAPIs, addrs); err != nil {
				t.Fatal(err)
			}
		}
	*/
}

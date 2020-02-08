package runner

import (
	"context"
	"fmt"
	"github.com/hashicorp/go-sockaddr"
	"net"
	"reflect"
	"sort"
	"time"
)

type (
	Runner interface {
		Start(ctx context.Context) (net.IP, error)
		Wait() error
		Stop() error
	}

	LeaderAPI interface {
		Leader() (string, error)
		Peers() ([]string, error)
	}

	NetworkConfig struct {
		Network       sockaddr.SockAddr
		DockerNetName string
	}

	LogConfig struct {
		LogDir            string
		LogRotateBytes    int
		LogRotateMaxFiles int
	}
)

func LeaderAPIsHealthy(ctx context.Context, apis []LeaderAPI, expectedPeers []string) error {
	var errs []error
	var peers []string
	var leaders map[string]struct{}
	for ctx.Err() == nil {
		errs = nil
		peers = []string{}
		leaders = make(map[string]struct{})

		for _, api := range apis {
			leader, err := api.Leader()
			if err != nil {
				errs = append(errs, err)
				break
			}
			if leader != "" {
				leaders[leader] = struct{}{}
			}
			peers, err = api.Peers()
			if err != nil {
				errs = append(errs, err)
				break
			}
		}
		sort.Strings(peers)
		if len(errs) == 0 && len(leaders) == 1 && reflect.DeepEqual(peers, expectedPeers) {
			break
		}
		time.Sleep(1000 * time.Millisecond)
	}
	if ctx.Err() != nil {
		return fmt.Errorf("expected no errs, 1 leader, peers=%v, got %v, %v, %v", expectedPeers,
			errs, leaders, peers)
	}
	return nil
}

func ConsulLeaderAPIs(runners []ConsulRunner) ([]LeaderAPI, error) {
	var ret []LeaderAPI
	for _, runner := range runners {
		api, err := runner.ConsulAPI()
		if err != nil {
			return nil, err
		}
		ret = append(ret, api.Status())
	}
	return ret, nil
}

func NomadLeaderAPIs(runners []NomadRunner) ([]LeaderAPI, error) {
	var ret []LeaderAPI
	for _, runner := range runners {
		api, err := runner.NomadAPI()
		if err != nil {
			return nil, err
		}
		ret = append(ret, api.Status())
	}
	return ret, nil
}

func ConsulRunnersHealthy(ctx context.Context, runners []ConsulRunner, expectedPeers []string) error {
	apis, err := ConsulLeaderAPIs(runners)
	if err != nil {
		return err
	}
	return LeaderAPIsHealthy(ctx, apis, expectedPeers)
}

func NomadRunnersHealthy(ctx context.Context, runners []NomadRunner, expectedPeers []string) error {
	apis, err := NomadLeaderAPIs(runners)
	if err != nil {
		return err
	}
	return LeaderAPIsHealthy(ctx, apis, expectedPeers)
}
package runner

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt/packages"
	"io/ioutil"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"testing"
	"time"
)

func testSetup(t *testing.T, timeout time.Duration) (string, context.Context, func()) {
	t.Helper()
	tmpDir, err := ioutil.TempDir(".", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	absDir, err := filepath.Abs(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return absDir, ctx, func() {
		cancel()
		_ = os.RemoveAll(tmpDir)
	}
}

func consulRunnersHealthy(ctx context.Context, runners []ConsulRunner, expectedPeers []string) error {
	apis, err := consulLeaderAPIs(runners)
	if err != nil {
		return err
	}
	return leaderAPIsHealthy(ctx, apis, expectedPeers)
}

func getConsulNomadBinaries(t *testing.T) (string, string) {
	t.Helper()
	consulPath, err := packages.GetBinary("consul", runtime.GOOS, runtime.GOARCH, "download")
	if err != nil {
		t.Fatal(err)
	}

	nomadPath, err := packages.GetBinary("nomad", runtime.GOOS, runtime.GOARCH, "download")
	if err != nil {
		t.Fatal(err)
	}

	return consulPath, nomadPath
}

func nomadRunnersHealthy(ctx context.Context, runners []NomadRunner, expectedPeers []string) error {
	apis, err := nomadLeaderAPIs(runners)
	if err != nil {
		return err
	}
	return leaderAPIsHealthy(ctx, apis, expectedPeers)
}

func consulLeaderAPIs(runners []ConsulRunner) ([]LeaderAPI, error) {
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

func nomadLeaderAPIs(runners []NomadRunner) ([]LeaderAPI, error) {
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

func leaderAPIsHealthy(ctx context.Context, apis []LeaderAPI, expectedPeers []string) error {
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

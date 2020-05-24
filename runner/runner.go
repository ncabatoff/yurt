package runner

import (
	"context"
	"fmt"
	consulapi "github.com/hashicorp/consul/api"
	nomadapi "github.com/hashicorp/nomad/api"
	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/pki"
	"net/url"
	"reflect"
	"sort"
	"time"
)

type (
	// Config is the common config shared by all runners, though not all
	// members may be used.  For example, a stateless service may have no DataDir.
	Config struct {
		// ConfigDir is where config files live
		ConfigDir string
		// DataDir is where the process writes data
		DataDir string
		// LogDir is where logs are written by the process, if it knows how to
		// log to disk.
		LogDir string
		// NetworkConfig specifies how network addresses get assigned
		NetworkConfig yurt.NetworkConfig
		// NodeName is the name for this instance of the process.  This may or
		// may not be an addressable name, depending on NetworkConfig.
		NodeName string
		// APIPort is the port used to reach the API provided by the node, if any
		APIPort int
		// TLS is used to configure TLS.  CA is required for any TLS connection,
		// the other fields are only needed if client TLS is used.
		TLS   pki.TLSConfigPEM
		Name  string
		Ports []string
	}

	// Command describes how to run and interact with a process that starts
	// a service.
	Command interface {
		// Config describes the directories containing process outputs and the
		// networking details used to connect to its API.
		Config() Config
		// Args are the command-line arguments to launch the process with.
		Args() []string
		// Env is the key=value settings to put in the environment.
		Env() []string
		// Files are a map from base file name to file contents.  These inputs
		// must be written to the ConfigDir before launch.
		Files() map[string]string
		// WithDirs returns a new Command with alternate config, data, and log dirs.
		WithDirs(config, data, log string) Command
		WithPorts(firstPort int) Command
		WithNetwork(config yurt.NetworkConfig) Command
		WithName(name string) Command
	}

	// Runner is the basic interface used for launching processes.
	Runner interface {
		// Start launches a process and returns the IP or hostname it runs on.
		Command() Command
		Start(ctx context.Context) (string, error)
		Wait() error
		Stop() error
	}

	// APIConfig contains enough information to create a connection to a service:
	// the address of the service and the CA needed for TLS handshaking.
	APIConfig struct {
		Address url.URL
		CAFile  string
	}

	// APIRunner is a Runner that creates a process with an API.
	APIRunner interface {
		Runner
		APIConfig() (*APIConfig, error)
	}

	// LeaderAPI describes a distributed consensus API of many nodes with a
	// single leader under quorum.
	LeaderAPI interface {
		Leader() (string, error)
		Peers() ([]string, error)
	}

	// LogConfig is used by processes which can do self-log-rotation.
	LogConfig struct {
		LogDir            string
		LogRotateBytes    int
		LogRotateMaxFiles int
	}
)

func ConsulRunnerToAPI(r ConsulRunner) (*consulapi.Client, error) {
	apicfg, err := r.APIConfig()
	if err != nil {
		return nil, err
	}
	return consulapi.NewClient(ConsulAPIConfig(apicfg))
}

func ConsulAPIConfig(a *APIConfig) *consulapi.Config {
	cfg := consulapi.DefaultConfig()
	cfg.Address = a.Address.String()
	cfg.TLSConfig.CAFile = a.CAFile
	return cfg
}

func NomadRunnerToAPI(r NomadRunner) (*nomadapi.Client, error) {
	apicfg, err := r.APIConfig()
	if err != nil {
		return nil, err
	}
	return nomadapi.NewClient(NomadAPIConfig(apicfg))
}

func NomadAPIConfig(a *APIConfig) *nomadapi.Config {
	cfg := nomadapi.DefaultConfig()
	cfg.Address = a.Address.String()
	cfg.TLSConfig.CACert = a.CAFile
	return cfg
}

func LeaderAPIsHealthyNow(apis []LeaderAPI, expectedPeers []string) error {
	var errs []error
	var peers []string
	var leaders = make(map[string]struct{})

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
		return nil
	}

	return fmt.Errorf("expected no errs, 1 leader, peers=%v, got %v, %v, %v", expectedPeers,
		errs, leaders, peers)
}

func LeaderAPIsHealthy(ctx context.Context, apis []LeaderAPI, expectedPeers []string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var err error
	for ctx.Err() == nil {
		err = LeaderAPIsHealthyNow(apis, expectedPeers)
		if err == nil {
			return nil
		}
		time.Sleep(1000 * time.Millisecond)
	}
	return err
}

func ConsulLeaderAPIs(runners []ConsulRunner) ([]LeaderAPI, error) {
	var ret []LeaderAPI
	for _, runner := range runners {
		apicfg, err := runner.APIConfig()
		if err != nil {
			return nil, err
		}
		api, err := consulapi.NewClient(ConsulAPIConfig(apicfg))
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
		apicfg, err := runner.APIConfig()
		if err != nil {
			return nil, err
		}
		api, err := nomadapi.NewClient(NomadAPIConfig(apicfg))
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

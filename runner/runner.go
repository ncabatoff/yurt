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
		TLS      pki.TLSConfigPEM
		Ports    yurt.Ports
	}

	// Command describes how to run and interact with a process that starts
	// a service.
	Command interface {
		Name() string
		// Args are the command-line arguments to launch the process with.
		Args() []string
		// Env is the key=value settings to put in the environment.
		Env() []string
		// Files are a map from base file name to file contents.  These inputs
		// must be written to the ConfigDir before launch.
		Files() map[string]string
		// WithConfig returns a new Command with alternate config
		Config() Config
		WithConfig(Config) Command
	}

	// APIConfig contains enough information to create a connection to a service:
	// the address of the service and the CA needed for TLS handshaking.
	APIConfig struct {
		Address url.URL
		CAFile  string
	}

	Harness interface {
		// Endpoint returns the config for the named service.  If local is true,
		// the config is relative to the caller, otherwise it is given in terms
		// of the service nodes themselves.  These may be equivalent depending
		// on the execution model, i.e. whether port forwarding is being used
		// to bridge the local and execution networks.
		Endpoint(name string, local bool) (*APIConfig, error)
		Stop() error
		Wait() error
	}

	// LeaderAPI describes a distributed consensus API of many nodes with a
	// single leader under quorum.
	LeaderAPI interface {
		Leader() (string, error)
		Peers() ([]string, error)
	}
)

func ConsulRunnerToAPI(r Harness) (*consulapi.Client, error) {
	apicfg, err := r.Endpoint("http", true)
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

func NomadRunnerToAPI(r Harness) (*nomadapi.Client, error) {
	apicfg, err := r.Endpoint("http", true)
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

func ConsulLeaderAPIs(servers []Harness) ([]LeaderAPI, error) {
	var ret []LeaderAPI
	for _, server := range servers {
		apicfg, err := server.Endpoint("http", true)
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

func NomadLeaderAPIs(servers []Harness) ([]LeaderAPI, error) {
	var ret []LeaderAPI
	for _, server := range servers {
		apicfg, err := server.Endpoint("http", true)
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

func ConsulRunnersHealthy(ctx context.Context, servers []Harness, expectedPeers []string) error {
	apis, err := ConsulLeaderAPIs(servers)
	if err != nil {
		return err
	}
	return LeaderAPIsHealthy(ctx, apis, expectedPeers)
}

func NomadRunnersHealthy(ctx context.Context, servers []Harness, expectedPeers []string) error {
	apis, err := NomadLeaderAPIs(servers)
	if err != nil {
		return err
	}
	return LeaderAPIsHealthy(ctx, apis, expectedPeers)
}

package runner

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"time"

	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/pki"
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
		Kill()
		Wait() error
	}

	Status interface {
		// Status() returns the service-dependent status result, or an error
		// if the service isn't even able to do that
		Status() (interface{}, error)
	}

	LeaderAPI interface {
		Leader() (string, error)
	}

	// LeaderPeersAPI describes a distributed consensus API of many nodes with a
	// single leader under quorum.
	LeaderPeersAPI interface {
		Leader() (string, error)
		Peers() ([]string, error)
	}
)

func LeaderPeerAPIsHealthyNow(apis []LeaderPeersAPI, expectedPeers []string) error {
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

func LeaderPeerAPIsHealthy(ctx context.Context, apis []LeaderPeersAPI, expectedPeers []string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var err error
	for ctx.Err() == nil {
		err = LeaderPeerAPIsHealthyNow(apis, expectedPeers)
		if err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

func LeaderAPIsHealthy(ctx context.Context, apis []LeaderAPI) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var err error
	for ctx.Err() == nil {
		_, err = LeaderAPIsHealthyNow(apis)
		if err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

func LeaderAPIsHealthyNow(apis []LeaderAPI) (string, error) {
	var errs []error
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
	}
	if len(errs) == 0 && len(leaders) == 1 {
		for leader := range leaders {
			return leader, nil
		}
	}

	return "", fmt.Errorf("expected no errs, 1 leader got %v, %v", errs, leaders)
}

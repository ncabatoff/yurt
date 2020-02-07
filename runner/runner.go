package runner

import (
	"context"
	"net"
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
		Network       net.IPNet
		DockerNetName string
	}

	LogConfig struct {
		LogDir            string
		LogRotateBytes    int
		LogRotateMaxFiles int
	}
)

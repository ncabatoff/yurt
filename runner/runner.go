package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
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

type OutputWriter struct {
	io.Writer
}

var _ io.Writer = (*OutputWriter)(nil)

func NewOutputWriter(prefix string, output io.Writer) *OutputWriter {
	r, w := io.Pipe()
	br := bufio.NewReader(r)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				_, _ = fmt.Fprintf(output, "%s: %s", prefix, line)
			}
			if err != nil {
				break
			}
		}
	}()
	return &OutputWriter{
		Writer: w,
	}
}

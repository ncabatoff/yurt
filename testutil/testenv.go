package testutil

import (
	"context"
	"fmt"
	"go.uber.org/atomic"
	"io/ioutil"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	dockerapi "github.com/docker/docker/client"
	"github.com/hashicorp/go-sockaddr"
	"github.com/ncabatoff/yurt/docker"
	"github.com/ncabatoff/yurt/packages"
	"github.com/ncabatoff/yurt/util"
	"golang.org/x/sync/errgroup"
)

type TestEnv struct {
	TmpDir  string
	Ctx     context.Context
	Cleanup func()
	Group   *errgroup.Group
}

var portSource = atomic.NewUint32(20001)

func init() {
	rand.Seed(time.Now().UnixNano())
}

// NewTestEnv creates a TestEnv, such that:
// - TmpDir is created
// - Ctx is created with given timeout
// - Group will be waited on by Cleanup and its error logged
// - Cleanup will cancel the context, and thereby stop Group, then remove TmpDir
func NewTestEnv(t *testing.T, timeout time.Duration) TestEnv {
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
	g, ctx := errgroup.WithContext(ctx)
	return TestEnv{
		TmpDir: absDir,
		Ctx:    ctx,
		Group:  g,
		Cleanup: func() {
			cancel()
			if err := g.Wait(); err != nil {
				t.Logf("exit with error (this may be normal): %v", err)
			}
			_ = os.RemoveAll(tmpDir)
			//t.Logf("cleanup %s: %v", tmpDir, err)
		},
	}
}

type ExecTestEnv struct {
	TestEnv
	ConsulPath     string
	NomadPath      string
	PrometheusPath string
}

func NewExecTestEnv(t *testing.T, timeout time.Duration) ExecTestEnv {
	te := NewTestEnv(t, timeout)
	dldirBase := filepath.Join(os.TempDir(), "yurt-test-downloads")
	consulPath, err := packages.GetBinary("consul", runtime.GOOS, runtime.GOARCH, dldirBase)
	if err != nil {
		t.Fatal(err)
	}
	consulAbs, err := filepath.Abs(consulPath)
	if err != nil {
		t.Fatal(err)
	}

	nomadPath, err := packages.GetBinary("nomad", runtime.GOOS, runtime.GOARCH, dldirBase)
	if err != nil {
		t.Fatal(err)
	}
	nomadAbs, err := filepath.Abs(nomadPath)
	if err != nil {
		t.Fatal(err)
	}

	promPath, err := packages.GetBinary("prometheus", runtime.GOOS, runtime.GOARCH, dldirBase)
	if err != nil {
		t.Fatal(err)
	}
	promAbs, err := filepath.Abs(promPath)
	if err != nil {
		t.Fatal(err)
	}

	return ExecTestEnv{
		TestEnv:        te,
		ConsulPath:     consulAbs,
		NomadPath:      nomadAbs,
		PrometheusPath: promAbs,
	}
}

func NextPortRange(i int) int {
	return int(portSource.Add(uint32(i))) - i
}

type DockerTestEnv struct {
	TestEnv
	Docker   *dockerapi.Client
	NetConf  util.NetworkConfig
	curIPOct *atomic.Int32
}

func NewDockerTestEnv(t *testing.T, timeout time.Duration) DockerTestEnv {
	te := NewTestEnv(t, timeout)
	cli, err := dockerapi.NewClientWithOpts(dockerapi.FromEnv, dockerapi.WithVersion("1.40"))
	if err != nil {
		t.Fatal(err)
	}

	cidr := fmt.Sprintf("10.%d.%d.0/24", rand.Int31n(255), rand.Int31n(255))
	_, err = docker.SetupNetwork(te.Ctx, cli, t.Name(), cidr)
	if err != nil {
		t.Fatal(err)
	}

	sa, err := sockaddr.NewSockAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}

	return DockerTestEnv{
		TestEnv: te,
		Docker:  cli,
		NetConf: util.NetworkConfig{
			DockerNetName: t.Name(),
			Network:       sa,
		},
		curIPOct: atomic.NewInt32(1),
	}
}

func (d *DockerTestEnv) NextIP() string {
	i4 := sockaddr.ToIPv4Addr(d.NetConf.Network).NetIP().To4()
	i4[3] = byte(d.curIPOct.Inc())
	return i4.String()
}

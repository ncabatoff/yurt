package runenv

import (
	"context"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	dockerapi "github.com/docker/docker/client"
	"github.com/hashicorp/go-sockaddr"
	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/binaries"
	"github.com/ncabatoff/yurt/docker"
	"github.com/ncabatoff/yurt/runner"
	dockerrunner "github.com/ncabatoff/yurt/runner/docker"
	"github.com/ncabatoff/yurt/runner/exec"
	"go.uber.org/atomic"
	"golang.org/x/sync/errgroup"
)

var portSource = atomic.NewUint32(20001)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type Env interface {
	// Run starts the specified command as the requested node.
	Run(ctx context.Context, cmd runner.Command, node yurt.Node) (runner.Harness, error)
	AllocNode(baseName string, ports yurt.Ports) yurt.Node
	Context() context.Context
	Go(f func() error)
}

type BaseEnv struct {
	// WorkDir contains any files created by the env
	WorkDir string
	// Ctx controls the lifecycle: when it's done, everything gets cleaned up
	Ctx context.Context
	// The env terminates as soon as Ctx is done or a member of the group returns
	// an error.
	*errgroup.Group
}

func (b *BaseEnv) Context() context.Context {
	return b.Ctx
}

func NewBaseEnv(ctx context.Context, workDir string) (*BaseEnv, error) {
	if workDir == "" {
		tmpDir, err := ioutil.TempDir("", "yurt-env")
		if err != nil {
			return nil, err
		}
		workDir = tmpDir
	}

	absDir, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		<-ctx.Done()
		// TODO add retries to handle slow exiters
		_ = os.RemoveAll(absDir)
		return nil
	})
	return &BaseEnv{
		WorkDir: absDir,
		Ctx:     ctx,
		Group:   g,
	}, nil
}

type ExecEnv struct {
	BaseEnv
	firstPort *atomic.Int32
	nodes     *atomic.Int32
}

var _ Env = &ExecEnv{}

func NewExecEnv(ctx context.Context, name, workDir string, firstPort int) (*ExecEnv, error) {
	e, err := NewBaseEnv(ctx, workDir)
	if err != nil {
		return nil, err
	}
	return &ExecEnv{
		BaseEnv:   *e,
		firstPort: atomic.NewInt32(int32(firstPort)),
		nodes:     atomic.NewInt32(0),
	}, nil
}

func (e ExecEnv) AllocNode(baseName string, ports yurt.Ports) yurt.Node {
	name := fmt.Sprintf("%s-%d", baseName, e.nodes.Add(1))
	lastPort := e.firstPort.Add(int32(len(ports.NameOrder)))
	return yurt.Node{
		Name:     name,
		StaticIP: "127.0.0.1",
		Ports:    ports.Sequential(int(lastPort) - len(ports.NameOrder)),
	}
}

func (e ExecEnv) Run(ctx context.Context, cmd runner.Command, node yurt.Node) (runner.Harness, error) {
	binPath, err := binaries.Default.Get(cmd.Name())
	if err != nil {
		return nil, err
	}

	r, err := exec.NewExecRunner(binPath, cmd, runner.Config{
		NodeName:  node.Name,
		ConfigDir: filepath.Join(e.WorkDir, node.Name, "config"),
		DataDir:   filepath.Join(e.WorkDir, node.Name, "data"),
		LogDir:    filepath.Join(e.WorkDir, node.Name, "log"),
		Ports:     node.Ports,
	})
	if err != nil {
		return nil, err
	}
	h, err := r.Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("error starting server: %w", err)
	}
	return h, nil
}

type DockerEnv struct {
	BaseEnv
	DockerAPI *dockerapi.Client
	NetConf   yurt.NetworkConfig
	baseCIDR  net.IPNet
	curIPOct  *atomic.Int32
	nodes     *atomic.Int32
}

func (d *DockerEnv) AllocNode(baseName string, ports yurt.Ports) yurt.Node {
	name := fmt.Sprintf("%s-%d", baseName, d.nodes.Add(1))
	i4 := sockaddr.ToIPv4Addr(d.NetConf.Network).NetIP().To4()
	i4[3] = byte(d.curIPOct.Add(1))
	return yurt.Node{
		Name:     name,
		Ports:    ports.Sequential(17000),
		StaticIP: i4.String(),
	}
}

func NewDockerEnv(ctx context.Context, name, workDir, cidr string) (*DockerEnv, error) {
	b, err := NewBaseEnv(ctx, workDir)
	if err != nil {
		return nil, err
	}

	cli, err := dockerapi.NewClientWithOpts(dockerapi.FromEnv, dockerapi.WithVersion("1.39"))
	if err != nil {
		return nil, err
	}

	if cidr == "" {
		cidr = fmt.Sprintf("10.%d.%d.0/24", rand.Int31n(255), rand.Int31n(255))
	}

	netRes, err := docker.SetupNetwork(context.Background(), cli, name, cidr)
	if err != nil {
		return nil, err
	}

	sa, err := sockaddr.NewSockAddr(netRes.IPAM.Config[0].Subnet)
	if err != nil {
		return nil, err
	}

	return &DockerEnv{
		BaseEnv: *b,
		NetConf: yurt.NetworkConfig{
			DockerNetName: name,
			Network:       sa,
		},
		DockerAPI: cli,
		nodes:     atomic.NewInt32(0),
		curIPOct:  atomic.NewInt32(1),
	}, nil
}

func (d *DockerEnv) Run(ctx context.Context, cmd runner.Command, node yurt.Node) (runner.Harness, error) {
	var image string
	switch cmd.Name() {
	case "consul":
		image = "consul:1.7.0-beta4"
	case "nomad":
		image = "noenv/nomad:0.10.3"
	default:
		return nil, fmt.Errorf("unknown config %q", cmd.Name())
	}
	r, err := dockerrunner.NewDockerRunner(d.DockerAPI, image, node.StaticIP, cmd, runner.Config{
		NodeName:      node.Name,
		NetworkConfig: d.NetConf,
		ConfigDir:     filepath.Join(d.WorkDir, node.Name, "config"),
		DataDir:       filepath.Join(d.WorkDir, node.Name, "data"),
		LogDir:        filepath.Join(d.WorkDir, node.Name, "log"),
		Ports:         node.Ports,
	})
	if err != nil {
		return nil, err
	}
	h, err := r.Start(ctx)
	if err != nil {
		return nil, fmt.Errorf("error starting server: %w", err)
	}
	return h, nil
}

var _ Env = &DockerEnv{}

func NewDockerTestEnv(t *testing.T, timeout time.Duration) (*DockerEnv, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	e, err := NewDockerEnv(ctx, t.Name(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	return e, func() {
		cancel()
		err := e.Group.Wait()
		if err != nil {
			t.Log(err)
		}
	}
}

func NewExecTestEnv(t *testing.T, timeout time.Duration) (*ExecEnv, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	e, err := NewExecEnv(ctx, t.Name(), "", 18000)
	if err != nil {
		t.Fatal(err)
	}
	return e, func() {
		cancel()
		err := e.Group.Wait()
		if err != nil {
			t.Log(err)
		}
	}
}

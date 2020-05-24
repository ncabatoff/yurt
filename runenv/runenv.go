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
	Run(ctx context.Context, cmd runner.Command, node yurt.Node) (runner.APIRunner, yurt.Node, error)
	// IP returns the IP of a node that was previously passed to Run without error.
	IP(node yurt.Node) (string, error)
	AllocNode(baseName string, numPorts int) yurt.Node
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

// TODO randomize firstport when 0?
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

func (e ExecEnv) AllocNode(baseName string, numPorts int) yurt.Node {
	name := fmt.Sprintf("%s-%d", baseName, e.nodes.Add(1))
	return yurt.Node{
		Name:      name,
		StaticIP:  "127.0.0.1",
		FirstPort: int(e.firstPort.Add(int32(numPorts))),
		WorkDir:   filepath.Join(e.BaseEnv.WorkDir, name),
	}
}

func (e ExecEnv) Run(ctx context.Context, cmd runner.Command, node yurt.Node) (runner.APIRunner, yurt.Node, error) {
	confDir := filepath.Join(e.WorkDir, node.Name, "config")
	dataDir := filepath.Join(e.WorkDir, node.Name, "data")
	logDir := filepath.Join(e.WorkDir, node.Name, "log")
	cmd = cmd.WithDirs(confDir, dataDir, logDir).WithName(node.Name)
	if cmd.Config().Ports[0][0] == '0' {
		cmd = cmd.WithPorts(node.FirstPort)
	}

	binPath, err := binaries.Default.Get(cmd.Config().Name)
	if err != nil {
		return nil, yurt.Node{}, err
	}

	r, err := exec.NewExecRunner(binPath, cmd)
	if err != nil {
		return nil, yurt.Node{}, err
	}
	_, err = r.Start(ctx)
	if err != nil {
		return nil, yurt.Node{}, fmt.Errorf("error starting server: %w", err)
	}
	return r, node, nil
}

func (e ExecEnv) IP(node yurt.Node) (string, error) {
	return "127.0.0.1", nil
}

type DockerEnv struct {
	BaseEnv
	DockerAPI *dockerapi.Client
	NetConf   yurt.NetworkConfig
	ips       map[string]string
	baseCIDR  net.IPNet
	curIPOct  *atomic.Int32
	nodes     *atomic.Int32
}

func (d *DockerEnv) AllocNode(baseName string, numPorts int) yurt.Node {
	name := fmt.Sprintf("%s-%d", baseName, d.nodes.Add(1))
	i4 := sockaddr.ToIPv4Addr(d.NetConf.Network).NetIP().To4()
	i4[3] = byte(d.curIPOct.Add(1))
	return yurt.Node{
		Name:      name,
		FirstPort: 17000,
		StaticIP:  i4.String(),
		WorkDir:   filepath.Join(d.BaseEnv.WorkDir, name),
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
		ips:       make(map[string]string),
		nodes:     atomic.NewInt32(0),
		curIPOct:  atomic.NewInt32(1),
	}, nil
}

func (d *DockerEnv) Run(ctx context.Context, cmd runner.Command, node yurt.Node) (runner.APIRunner, yurt.Node, error) {
	confDir := filepath.Join(d.WorkDir, node.Name, "config")
	dataDir := filepath.Join(d.WorkDir, node.Name, "data")
	logDir := filepath.Join(d.WorkDir, node.Name, "log")
	cmd = cmd.WithDirs(confDir, dataDir, logDir).WithPorts(node.FirstPort).WithNetwork(d.NetConf).WithName(node.Name)

	var image string
	switch cmd.Config().Name {
	case "consul":
		image = "consul:1.7.0-beta4"
	case "nomad":
		image = "noenv/nomad:0.10.3"
	default:
		return nil, yurt.Node{}, fmt.Errorf("unknown config %q", cmd.Config().Name)
	}
	r, err := dockerrunner.NewDockerRunner(d.DockerAPI, image, node.StaticIP, cmd)
	if err != nil {
		return nil, yurt.Node{}, err
	}
	ip, err := r.Start(ctx)
	if err != nil {
		return nil, yurt.Node{}, fmt.Errorf("error starting server: %w", err)
	}
	node.StaticIP = ip
	d.ips[node.Name] = ip
	return r, node, nil
}

func (d *DockerEnv) IP(node yurt.Node) (string, error) {
	name := d.ips[node.Name]
	if name == "" {
		return "", fmt.Errorf("node not running")
	}
	return name, nil
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

package runenv

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	dockerapi "github.com/docker/docker/client"
	"github.com/hashicorp/go-sockaddr"
	"github.com/ncabatoff/yurt"
	"github.com/ncabatoff/yurt/binaries"
	"github.com/ncabatoff/yurt/consul"
	"github.com/ncabatoff/yurt/docker"
	"github.com/ncabatoff/yurt/nomad"
	"github.com/ncabatoff/yurt/prometheus"
	"github.com/ncabatoff/yurt/runner"
	dockerrunner "github.com/ncabatoff/yurt/runner/docker"
	"github.com/ncabatoff/yurt/runner/exec"
	"github.com/ncabatoff/yurt/vault"
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
	AllocNode(baseName string, ports yurt.Ports) (yurt.Node, error)
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
	firstPort  *atomic.Int32
	nodes      *atomic.Int32
	binmgr     binaries.Manager
	LogToFiles bool
}

var _ Env = &ExecEnv{}

func NewExecEnv(ctx context.Context, name, workDir string, firstPort int, binmgr binaries.Manager) (*ExecEnv, error) {
	e, err := NewBaseEnv(ctx, workDir)
	if err != nil {
		return nil, err
	}
	return &ExecEnv{
		BaseEnv:   *e,
		firstPort: atomic.NewInt32(int32(firstPort)),
		nodes:     atomic.NewInt32(0),
		binmgr:    binmgr,
	}, nil
}

func (e ExecEnv) AllocNode(baseName string, ports yurt.Ports) (yurt.Node, error) {
	name := fmt.Sprintf("%s-%d", baseName, e.nodes.Add(1))
	lastPort := e.firstPort.Add(int32(len(ports.NameOrder)))
	return yurt.Node{
		Name:  name,
		Host:  "127.0.0.1",
		Ports: ports.Sequential(int(lastPort) - len(ports.NameOrder)),
	}, nil
}

func (e ExecEnv) Run(ctx context.Context, cmd runner.Command, node yurt.Node) (runner.Harness, error) {
	binPath, err := e.binmgr.Get(cmd.Name())
	if err != nil {
		return nil, err
	}

	logDir := filepath.Join(e.WorkDir, node.Name, "log")
	logName := ""
	if e.LogToFiles {
		logName = filepath.Join(logDir, fmt.Sprintf("%s-stdout.txt", time.Now().Format(time.RFC3339)))
	}

	r, err := exec.NewExecRunner(binPath, cmd, runner.Config{
		NodeName:  node.Name,
		ConfigDir: filepath.Join(e.WorkDir, node.Name, "config"),
		DataDir:   filepath.Join(e.WorkDir, node.Name, "data"),
		LogDir:    logDir,
		Ports:     node.Ports,
		TLS:       cmd.Config().TLS,
	})
	if err != nil {
		return nil, err
	}
	h, err := r.Start(ctx, logName)
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

func (d *DockerEnv) AllocNode(baseName string, ports yurt.Ports) (yurt.Node, error) {
	name := fmt.Sprintf("%s-%d", baseName, d.nodes.Add(1))
	i4 := sockaddr.ToIPv4Addr(d.NetConf.Network).NetIP().To4()
	i4[3] = byte(d.curIPOct.Add(1))
	return yurt.Node{
		Name:  name,
		Ports: ports.Sequential(17000),
		Host:  i4.String(),
	}, nil
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
		image = "consul:1.8.3"
	case "nomad":
		image = "noenv/nomad:0.10.3"
	case "vault":
		image = "vault:1.5.2"
	default:
		return nil, fmt.Errorf("unknown config %q", cmd.Name())
	}
	r, err := dockerrunner.NewDockerRunner(d.DockerAPI, image, node.Host, cmd, runner.Config{
		NodeName:      node.Name,
		NetworkConfig: d.NetConf,
		ConfigDir:     filepath.Join(d.WorkDir, node.Name, "config"),
		DataDir:       filepath.Join(d.WorkDir, node.Name, "data"),
		LogDir:        filepath.Join(d.WorkDir, node.Name, "log"),
		Ports:         node.Ports,
		TLS:           cmd.Config().TLS,
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

func NewMonitoredExecTestEnv(t *testing.T, timeout time.Duration) (*MonitoredEnv, func()) {
	t.Helper()
	e, cleanup := NewExecTestEnv(t, timeout)
	m, err := NewMonitoredEnv(e, e)
	if err != nil {
		t.Fatal(err)
	}
	return m, cleanup
}

func NewExecTestEnv(t *testing.T, timeout time.Duration) (*ExecEnv, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	//e, err := NewExecEnv(ctx, t.Name(), "", 18000, binaries.EnvPathManager{})
	e, err := NewExecEnv(ctx, t.Name(), "", 18000, binaries.Default)
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

// MonitoredEnv runs a Prometheus server whose targets are configured
// dynamically as we start them.  Prometheus is run locally as a
// sub-process.
// TODO make it possible to run Prometheus via any env with network
// access to the monitored targets.
type MonitoredEnv struct {
	exec          Env
	parent        Env
	promConfigDir string
	promAddr      *runner.APIConfig
	targetAddrs   targetAddrsByKind
}

type targetAddrsByKind struct {
	lock  sync.Mutex
	addrs map[string][]string
}

var _ Env = &MonitoredEnv{}

func NewMonitoredEnv(parent, ex Env) (*MonitoredEnv, error) {
	promNode, _ := ex.AllocNode("prometheus", prometheus.DefPorts().RunnerPorts())
	//consulClientNode := ex.AllocNode("consul", consul.DefPorts().RunnerPorts())
	// TODO trying to get the address before the client is running will be an
	// issue in some envs.
	//consulClientAddr, err := consulClientNode.Address(consul.PortNames.HTTP)
	//if err != nil {
	//	return nil, err
	//}
	//ssc := consul.ServiceScrapeConfig
	//ssc.ConsulServiceDiscoveryConfigs[0].Server = consulClientAddr
	p := prometheus.NewConfig(map[string]prometheus.ScrapeConfig{
		"consul": consul.ServerScrapeConfig,
		//"consul-services": ssc,
		//"nomad-clients": nomad.ClientScrapeConfig,
		"nomad": nomad.ServerScrapeConfig,
		"vault": vault.ServerScrapeConfig,
	}, nil)
	h, err := ex.Run(parent.Context(), p, promNode)
	if err != nil {
		return nil, err
	}
	ex.Go(h.Wait)

	apiConf, err := h.Endpoint(prometheus.PortNames.HTTP, true)
	if err != nil {
		return nil, err
	}

	return &MonitoredEnv{
		exec:          ex,
		parent:        parent,
		promConfigDir: h.(*exec.Harness).Config.ConfigDir,
		promAddr:      apiConf,
		targetAddrs: targetAddrsByKind{
			addrs: map[string][]string{},
		},
	}, nil
}

func (e *MonitoredEnv) PromAddr() *runner.APIConfig {
	return e.promAddr
}

func (e *MonitoredEnv) Run(ctx context.Context, cmd runner.Command, node yurt.Node) (runner.Harness, error) {
	return e.parent.Run(ctx, cmd, node)
}

func (e *MonitoredEnv) AllocNode(baseName string, ports yurt.Ports) (yurt.Node, error) {
	node, _ := e.parent.AllocNode(baseName, ports)
	addr, _ := node.Address("http")
	targets := []string{addr}

	e.targetAddrs.lock.Lock()
	defer e.targetAddrs.lock.Unlock()

	for _, target := range e.targetAddrs.addrs[ports.Kind] {
		targets = append(targets, target)
	}

	localTargets := []map[string]interface{}{
		map[string]interface{}{
			"targets": targets,
		},
	}

	tbytes, err := json.Marshal(localTargets)
	if err != nil {
		return yurt.Node{}, err
	}

	dest := filepath.Join(e.promConfigDir, ports.Kind+".servers.json")
	err = ioutil.WriteFile(dest, tbytes, 0644)
	if err != nil {
		return yurt.Node{}, err
	}
	e.targetAddrs.addrs[ports.Kind] = targets
	return node, nil
}

func (e *MonitoredEnv) Context() context.Context {
	return e.parent.Context()
}

func (e *MonitoredEnv) Go(f func() error) {
	e.parent.Go(f)
}

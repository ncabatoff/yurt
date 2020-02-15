package main

import (
	"context"
	"flag"
	"fmt"
	dockerapi "github.com/docker/docker/client"
	"github.com/hashicorp/go-sockaddr"
	"github.com/ncabatoff/yurt/docker"
	"golang.org/x/exp/rand"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ncabatoff/yurt/packages"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
	"github.com/skratchdot/open-golang/open"
	"golang.org/x/sync/errgroup"
)

func main() {
	var (
		flagCleanup     = flag.String("cleanup", "data", "on exit, cleanup: procs, data, all")
		flagConsulBin   = flag.String("consul-bin", "", "path to Consul binary, will download if empty")
		flagConsulImage = flag.String("consul-image", "consul:1.7.0-beta4", "docker image name for Consul")
		flagMode        = flag.String("mode", "exec", "cluster creation mode: exec or docker")
		flagFirstPort   = flag.Int("first-port", 23000, "first port to allocate to cluster, only for mode=exec")
		flagNodes       = flag.Int("nodes", 3, "number of server nodes")
		flagNomadBin    = flag.String("nomad-bin", "", "path to Nomad binary, will download if empty")
		flagNomadImage  = flag.String("nomad-image", "noenv/nomad:0.10.3", "docker image name for Nomad")
		flagOpen        = flag.Bool("open", true, "open browser to Consul and Nomad UIs")
		flagTLS         = flag.Bool("tls", false, "generate certs and enable TLS authentication")
		flagWorkDir     = flag.String("workdir", "/tmp/yurt", "directory to store files")
	)
	flag.Parse()

	switch *flagMode {
	case "docker", "exec":
	default:
		log.Fatalf("invalid mode %q", *flagMode)
	}

	err := os.MkdirAll(*flagWorkDir, 0755)
	if err != nil {
		log.Fatal(err)
	}

	datadir, err := ioutil.TempDir(*flagWorkDir, "cluster")
	if err != nil {
		log.Fatal(err)
	}

	bindir := filepath.Join(*flagWorkDir, "downloads")
	consulBin := *flagConsulBin
	if consulBin == "" {
		consulBin, err = packages.GetBinary("consul", runtime.GOOS, runtime.GOARCH, bindir)
		if err != nil {
			log.Fatal(err)
		}
	}
	consulAbs, err := filepath.Abs(consulBin)
	if err != nil {
		log.Fatal(err)
	}

	nomadBin := *flagNomadBin
	if nomadBin == "" {
		nomadBin, err = packages.GetBinary("nomad", runtime.GOOS, runtime.GOARCH, bindir)
		if err != nil {
			log.Fatal(err)
		}
	}
	nomadAbs, err := filepath.Abs(nomadBin)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	var ca *pki.CertificateAuthority
	if *flagTLS {
		ca, err = vaultCA(ctx, datadir, flagFirstPort)
	}

	var cli *dockerapi.Client
	var cc *runner.ConsulClusterRunner
	var network runner.NetworkConfig
	switch *flagMode {
	case "exec":
		cc, err = consulClusterExec(ctx, datadir, *flagNodes, consulAbs, flagFirstPort, ca)
		if err != nil {
			log.Print(err)
			return
		}
	case "docker":
		cli, err = dockerapi.NewClientWithOpts(dockerapi.FromEnv, dockerapi.WithVersion("1.40"))
		if err != nil {
			log.Fatal(err)
		}

		netName := "yurt-cluster"
		sa, err := sockaddr.NewSockAddr(fmt.Sprintf("192.168.%d.0/24", 2+rand.Int31n(250)))
		if err != nil {
			log.Fatal(err)
		}
		netID, err := docker.SetupNetwork(ctx, cli, netName, sa.String())
		if err != nil {
			log.Fatal(err)
		}
		defer cli.NetworkRemove(ctx, netID)
		network = runner.NetworkConfig{
			DockerNetName: netName,
			Network:       sa,
		}
		cc, err = consulClusterDocker(ctx, datadir, *flagNodes, cli, *flagConsulImage, network, ca)
		if err != nil {
			log.Print(err)
			return
		}
	default:
		log.Fatalf("invalid mode %q, must be one of exec or docker", *flagMode)
	}

	if *flagOpen {
		cfgs, err := cc.APIConfigs()
		if err != nil {
			log.Fatal(err)
		}
		u := fmt.Sprintf("%s://%s", cfgs[0].Scheme, cfgs[0].Address)
		if err := open.Run(u); err != nil {
			log.Printf("error opening URL %s: %v", u, err)
		}
	}

	var nc *runner.NomadClusterRunner
	switch *flagMode {
	case "exec":
		nc, err = nomadClusterExec(ctx, datadir, *flagNodes, nomadAbs, flagFirstPort, cc, ca)
		if err != nil {
			log.Print(err)
			return
		}
	case "docker":
		nc, err = nomadClusterDocker(ctx, datadir, *flagNodes, cli, *flagNomadImage, network, cc, ca)
		if err != nil {
			log.Print(err)
			return
		}
	default:
		log.Fatalf("invalid mode %q, must be one of exec or docker", *flagMode)
	}

	if *flagOpen {
		cfgs, err := nc.APIConfigs()
		if err != nil {
			log.Fatal(err)
		}
		u := cfgs[0].Address
		if err := open.Run(u); err != nil {
			log.Printf("error opening URL %s: %v", u, err)
		}
	}

	g := errgroup.Group{}
	g.Go(cc.WaitExit)
	g.Go(nc.WaitExit)
	err = g.Wait()
	if err != nil {
		log.Print(err)
	}

	switch *flagCleanup {
	case "procs":
		// Do nothing since this is the default for now
	case "data":
		cancel()
		defer os.RemoveAll(datadir)
	case "all":
	default:
		log.Fatal("invalid value for cleanup")
	}
}

func consulClusterExec(ctx context.Context, workDir string, nodes int, consulBin string, firstPort *int, ca *pki.CertificateAuthority) (*runner.ConsulClusterRunner, error) {
	serverNames := make([]string, nodes+1)
	for i := range serverNames {
		serverNames[i] = fmt.Sprintf("consul-srv-%d", i+1)
	}
	serverNames[nodes] = "consul-cli-1"

	clusterCfg := runner.ConsulClusterConfigSingleIP{
		WorkDir:     workDir,
		ServerNames: serverNames[:nodes],
		FirstPorts:  runner.SeqConsulPorts(*firstPort),
	}
	*firstPort += 5*nodes + 5

	if ca != nil {
		clusterCfg.TLS = make(map[string]pki.TLSConfigPEM)
		for _, name := range serverNames {
			cert, err := ca.ConsulServerTLS(ctx, "127.0.0.1", "168h")
			if err != nil {
				return nil, err
			}
			clusterCfg.TLS[name] = *cert
		}
	}
	return runner.BuildConsulCluster(ctx, clusterCfg,
		&runner.ConsulExecBuilder{BinPath: consulBin})
}

func consulClusterDocker(ctx context.Context, workDir string, nodes int, cli *dockerapi.Client, consulImage string, network runner.NetworkConfig, ca *pki.CertificateAuthority) (*runner.ConsulClusterRunner, error) {
	names := make([]string, nodes+1)
	for i := range names {
		names[i] = fmt.Sprintf("consul-srv-%d", i+1)
	}
	names[nodes] = "consul-cli-1"

	var ips []string
	serverIP := sockaddr.ToIPv4Addr(network.Network).NetIP().To4()
	for i := range names {
		serverIP[3] = byte(i) + 51
		ips = append(ips, serverIP.String())
	}

	clusterCfg := runner.ConsulClusterConfigFixedIPs{
		NetworkConfig:   network,
		WorkDir:         workDir,
		ServerNames:     names[:nodes],
		ConsulServerIPs: ips[:nodes],
	}

	if ca != nil {
		clusterCfg.TLS = make(map[string]pki.TLSConfigPEM)
		for _, name := range names {
			cert, err := ca.ConsulServerTLS(ctx, "127.0.0.1", "168h")
			if err != nil {
				return nil, err
			}
			clusterCfg.TLS[name] = *cert
		}
	}
	return runner.BuildConsulCluster(ctx, clusterCfg,
		&runner.ConsulDockerServerBuilder{
			DockerAPI: cli,
			Image:     consulImage,
			IPs:       ips,
		})
}

func nomadClusterExec(ctx context.Context, workDir string, nodes int, nomadBin string, firstPort *int, cc *runner.ConsulClusterRunner, ca *pki.CertificateAuthority) (*runner.NomadClusterRunner, error) {
	serverNames := make([]string, nodes+1)
	for i := range serverNames {
		serverNames[i] = fmt.Sprintf("nomad-srv-%d", i+1)
	}
	serverNames[nodes] = "nomad-cli-1"

	client, _ := cc.Client()
	clientPorts := client.Config().Ports
	clientAddr := fmt.Sprintf("localhost:%d", clientPorts.HTTP)
	clusterCfg := runner.NomadClusterConfigSingleIP{
		WorkDir:     workDir,
		ServerNames: serverNames[:nodes],
		FirstPorts:  runner.SeqNomadPorts(*firstPort),
		ConsulAddrs: append(cc.Config.APIAddrs(), clientAddr),
	}
	*firstPort += 3*nodes + 3

	if ca != nil {
		clusterCfg.TLS = make(map[string]pki.TLSConfigPEM)
		for _, name := range serverNames {
			cert, err := ca.NomadServerTLS(ctx, "127.0.0.1", "168h")
			if err != nil {
				return nil, err
			}
			clusterCfg.TLS[name] = *cert
		}
		clusterCfg.ConsulAddrs[nodes] = fmt.Sprintf("localhost:%d", clientPorts.HTTP)
	}

	return runner.BuildNomadCluster(ctx, clusterCfg,
		&runner.NomadExecBuilder{BinPath: nomadBin})
}

func nomadClusterDocker(ctx context.Context, workDir string, nodes int, cli *dockerapi.Client, nomadImage string, network runner.NetworkConfig, cc *runner.ConsulClusterRunner, ca *pki.CertificateAuthority) (*runner.NomadClusterRunner, error) {
	names := make([]string, nodes+1)
	for i := range names {
		names[i] = fmt.Sprintf("nomad-srv-%d", i+1)
	}
	names[nodes] = "nomad-cli-1"

	var ips []string
	serverIP := sockaddr.ToIPv4Addr(network.Network).NetIP().To4()
	for i := range names {
		serverIP[3] = byte(i) + 61
		ips = append(ips, serverIP.String())
	}

	consulClient, err := cc.Client()
	if err != nil {
		return nil, err
	}
	port := runner.DefConsulPorts().HTTP
	ip := consulClient.(*runner.ConsulDockerRunner).IP
	clusterCfg := runner.NomadClusterConfigFixedIPs{
		NetworkConfig: network,
		WorkDir:       workDir,
		ServerNames:   names[:nodes],
		ConsulAddrs:   append(cc.Config.APIAddrs(), fmt.Sprintf("%s:%d", ip, port)),
	}

	if ca != nil {
		clusterCfg.TLS = make(map[string]pki.TLSConfigPEM)
		for _, name := range names {
			cert, err := ca.NomadServerTLS(ctx, "127.0.0.1", "168h")
			if err != nil {
				return nil, err
			}
			clusterCfg.TLS[name] = *cert
		}
	}
	return runner.BuildNomadCluster(ctx, clusterCfg,
		&runner.NomadDockerServerBuilder{
			DockerAPI: cli,
			Image:     nomadImage,
			IPs:       ips,
		})
}

func vaultCA(ctx context.Context, workDir string, firstPort *int) (*pki.CertificateAuthority, error) {
	ca, err := pki.NewCertificateAuthority(workDir, *firstPort)
	*firstPort++
	if err != nil {
		return nil, err
	}
	err = ca.Start(ctx)
	if err != nil {
		return nil, err
	}
	return ca, nil
}

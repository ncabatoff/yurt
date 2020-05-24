package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ncabatoff/yurt/cluster"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runenv"
	//"github.com/skratchdot/open-golang/open"
)

func main() {
	var (
		flagMode      = flag.String("mode", "exec", "cluster creation mode: exec or docker")
		flagFirstPort = flag.Int("first-port", 23000, "first port to allocate to cluster, only for mode=exec")
		flagCIDR      = flag.String("cidr", "", "cidr to allocate to cluster, only for mode=docker")
		flagNodes     = flag.Int("nodes", 3, "number of server nodes")
		//flagOpen        = flag.Bool("open", true, "open browser to Consul and Nomad UIs")
		//flagTLS         = flag.Bool("tls", false, "generate certs and enable TLS authentication")
		flagWorkDir = flag.String("workdir", "/tmp/yurt", "directory to store files")
	)
	flag.Parse()

	var e runenv.Env
	switch *flagMode {
	case "exec":
		ee, err := runenv.NewExecEnv(context.Background(), "yurt-cluster", *flagWorkDir, *flagFirstPort)
		if err != nil {
			log.Print(err)
			return
		}
		e = ee
	case "docker":
		de, err := runenv.NewDockerEnv(context.Background(), "yurt-cluster", *flagWorkDir, *flagCIDR)
		if err != nil {
			log.Print(err)
			return
		}
		e = de
	default:
		log.Fatalf("invalid mode %q", *flagMode)
	}

	//var ca *pki.CertificateAuthority
	//if *flagTLS {
	//	ca, err = vaultCA(ctx, datadir, flagFirstPort)
	//}

	cnc, err := cluster.NewConsulNomadCluster(e.Context(), e, "cluster1", *flagNodes)
	if err != nil {
		log.Fatal(err)
	}
	defer cnc.Stop()
	e.Go(cnc.Wait)

	nomadClient, err := cnc.NomadClient(e)
	if err != nil {
		log.Fatal(err)
	}
	defer nomadClient.Stop()
	e.Go(nomadClient.Wait)

	sigchan := make(chan os.Signal)
	signal.Notify(sigchan, syscall.SIGINT)
	signal.Notify(sigchan, syscall.SIGTERM)
	<-sigchan
}

func vaultCA(ctx context.Context, workDir string, firstPort *int) (*pki.CertificateAuthority, error) {
	v, err := pki.NewVaultRunner(workDir, *firstPort)
	*firstPort++
	if err != nil {
		return nil, err
	}
	err = v.Start(ctx)
	if err != nil {
		return nil, err
	}
	return pki.NewCertificateAuthority(v.Cli)
}

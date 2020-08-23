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
		flagTLS     = flag.Bool("tls", false, "generate certs and enable TLS authentication")
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

	var ca *pki.CertificateAuthority
	var err error
	if *flagTLS {
		ca, err = vaultCA(e)
		if err != nil {
			log.Fatal(err)
		}
	}

	cnc, err := cluster.NewConsulNomadCluster(e.Context(), e, ca, "cluster1", *flagNodes)
	if err != nil {
		log.Fatal(err)
	}
	defer cnc.Stop()
	e.Go(cnc.Wait)

	nomadClient, err := cnc.NomadClient(e, ca)
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

func vaultCA(e runenv.Env) (*pki.CertificateAuthority, error) {
	cluster, err := cluster.NewVaultCluster(e.Context(), e, nil, "yurt-vault-pki", 1, false, nil)
	if err != nil {
		return nil, err
	}
	clients, err := cluster.Clients()
	if err != nil {
		return nil, err
	}
	e.Go(cluster.Wait)

	return pki.NewCertificateAuthority(clients[0])
}

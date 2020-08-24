package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ncabatoff/yurt/cluster"
	"github.com/ncabatoff/yurt/nomad"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runenv"
	"github.com/skratchdot/open-golang/open"
)

func main() {
	var (
		flagMode      = flag.String("mode", "exec", "cluster creation mode: exec or docker")
		flagFirstPort = flag.Int("first-port", 23000, "first port to allocate to cluster, only for mode=exec")
		flagCIDR      = flag.String("cidr", "", "cidr to allocate to cluster, only for mode=docker")
		flagNodes     = flag.Int("nodes", 3, "number of server nodes")
		flagOpen      = flag.Bool("open", true, "open browser to Consul and Nomad UIs")
		flagTLS       = flag.Bool("tls", false, "generate certs and enable TLS authentication")
		flagWorkDir   = flag.String("workdir", "/tmp/yurt", "directory to store files")
		flagVault     = flag.Bool("vault", true, "create a Vault cluster")
		flagNomad     = flag.Bool("nomad", true, "create a Nomad cluster")
	)
	flag.Parse()

	if !*flagNomad && !*flagVault {
		log.Fatal("must specify at least one of -vault=true and -nomad=true")
	}
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

	if *flagVault {
		vc, err := cluster.NewVaultCluster(e.Context(), e, ca, "cluster1", *flagNodes, nil, nil)
		if err != nil {
			log.Fatal(err)
		}
		defer vc.Stop()
		e.Go(vc.Wait)

		if *flagOpen {
			clients, err := vc.Clients()
			if err != nil {
				log.Fatal(err)
			}
			err = open.Start(clients[0].Address())
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	if *flagNomad {
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

		if *flagOpen {
			addrs, err := cnc.Consul.Addrs()
			if err != nil {
				log.Fatal(err)
			}
			err = open.Start(addrs[0])
			if err != nil {
				log.Fatal(err)
			}

			nc, err := nomad.HarnessToAPI(nomadClient.NomadHarness)
			if err != nil {
				log.Fatal(err)
			}
			err = open.Start(nc.Address())
			if err != nil {
				log.Fatal(err)
			}
		}
	}

	sigchan := make(chan os.Signal)
	signal.Notify(sigchan, syscall.SIGINT)
	signal.Notify(sigchan, syscall.SIGTERM)
	<-sigchan
}

func vaultCA(e runenv.Env) (*pki.CertificateAuthority, error) {
	cluster, err := cluster.NewVaultCluster(e.Context(), e, nil, "yurt-vault-pki", 1, nil, nil)
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

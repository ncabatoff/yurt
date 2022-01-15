package tls

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/ncabatoff/yurt/binaries"
	"github.com/ncabatoff/yurt/cluster"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runenv"
)

var VaultCA *pki.CertificateAuthority
var VaultCLI *vaultapi.Client

func TestMain(m *testing.M) {
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		log.Println("cancelling main context")
		cancel()
	}()

	exit := func(code int) {
		os.Exit(code)
	}
	fail := func(err error) {
		if err != nil {
			fmt.Fprintln(os.Stderr, "TestMain error: ", err)
			exit(1)
		}
	}

	workdir, err := ioutil.TempDir(os.TempDir(), "yurt-cluster-test")
	fail(err)

	e, err := runenv.NewExecEnv(ctx, "yurt-cluster-TestMain", workdir, 30000, binaries.Default)
	fail(err)

	exit = func(code int) {
		e.Group.Wait()
		os.Exit(code)
	}
	defer func() {
		cancel()
		err := e.Group.Wait()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}()

	c, err := cluster.NewVaultCluster(ctx, e, nil, "testmain-vaultca", 1, nil, nil, 0)
	fail(err)
	exit = func(code int) {
		c.Stop()
		cancel()
		e.Group.Wait()
		os.Exit(code)
	}
	e.Go(c.Wait)

	clients, err := c.Clients()
	fail(err)

	VaultCA, err = pki.NewCertificateAuthority(clients[0])
	fail(err)

	VaultCLI = clients[0]

	ret := m.Run()
	exit(ret)
}

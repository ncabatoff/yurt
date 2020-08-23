package cluster

import (
	"context"
	"fmt"
	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runenv"
	"io/ioutil"
	"os"
	"testing"
)

var VaultCA *pki.CertificateAuthority

func TestMain(m *testing.M) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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

	e, err := runenv.NewExecEnv(ctx, "yurt-cluster-TestMain", workdir, 30000)
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

	c, err := NewVaultCluster(ctx, e, nil, "testmain-vaultca", 1, false, nil)
	fail(err)
	exit = func(code int) {
		c.Stop()
		e.Group.Wait()
		os.Exit(code)
	}
	e.Go(c.Wait)

	clients, err := c.Clients()
	fail(err)

	VaultCA, err = pki.NewCertificateAuthority(clients[0])
	fail(err)

	ret := m.Run()
	exit(ret)
}

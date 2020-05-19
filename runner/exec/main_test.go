package exec

import (
	"context"
	"log"
	"os"
	"testing"

	"github.com/ncabatoff/yurt/pki"
)

var Vault *pki.VaultRunner

func TestMain(m *testing.M) {
	v, err := pki.NewVaultRunner("", 28200)
	if err != nil {
		log.Fatal(err)
	}
	err = v.Start(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	Vault = v
	ret := m.Run()
	v.Stop()
	os.Exit(ret)
}

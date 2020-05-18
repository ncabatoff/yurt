package cluster

import (
	"context"
	"github.com/ncabatoff/yurt/pki"
	"log"
	"os"
	"testing"
)

var Vault *pki.VaultRunner

func TestMain(m *testing.M) {
	v, err := pki.NewVaultRunner("", 28000)
	if err != nil {
		log.Fatal(err)
	}
	err = v.Start(context.Background())
	if err != nil {
		log.Fatal(err)
	}
	Vault = v
	os.Exit(m.Run())
}

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
	v, err := pki.NewVaultRunner("", 20000)
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

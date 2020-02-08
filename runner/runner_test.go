package runner

import (
	"context"
	"github.com/ncabatoff/yurt/packages"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testSetup(t *testing.T, timeout time.Duration) (string, context.Context, func()) {
	t.Helper()
	tmpDir, err := ioutil.TempDir(".", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	absDir, err := filepath.Abs(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return absDir, ctx, func() {
		cancel()
		_ = os.RemoveAll(tmpDir)
	}
}

func getConsulNomadBinaries(t *testing.T) (string, string) {
	t.Helper()
	consulPath, err := packages.GetBinary("consul", runtime.GOOS, runtime.GOARCH, "download")
	if err != nil {
		t.Fatal(err)
	}
	consulAbs, err := filepath.Abs(consulPath)
	if err != nil {
		t.Fatal(err)
	}

	nomadPath, err := packages.GetBinary("nomad", runtime.GOOS, runtime.GOARCH, "download")
	if err != nil {
		t.Fatal(err)
	}
	nomadAbs, err := filepath.Abs(nomadPath)
	if err != nil {
		t.Fatal(err)
	}

	return consulAbs, nomadAbs
}

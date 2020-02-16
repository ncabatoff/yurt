package packages

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBinaries(t *testing.T) {
	dldirBase := filepath.Join(os.TempDir(), "yurt-test-downloads")
	for name := range registry {
		path, err := GetBinary(name, runtime.GOOS, runtime.GOARCH, dldirBase)
		if err != nil {
			t.Fatal(err)
		}

		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}

		if fi.IsDir() || (fi.Mode()&0111) != 0111 {
			t.Fatalf("bad path %q: %v", path, fi)
		}
	}
}

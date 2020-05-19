package binaries

import (
	"os"
	"testing"
)

func TestBinaries(t *testing.T) {
	for name := range registry() {
		path, err := Default.Get(name)
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

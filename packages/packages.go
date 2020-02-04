package packages

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	"github.com/hashicorp/go-getter"
)

var registry = map[string]Upstream{
	"nomad": {
		name:      "nomad",
		version:   "0.10.2",
		urlFormat: "https://releases.hashicorp.com/nomad/%s/nomad_%s_%s_%s.zip",
	},
	"consul": {
		name:      "consul",
		version:   "1.5.3",
		urlFormat: "https://releases.hashicorp.com/consul/%s/consul_%s_%s_%s.zip",
	},
}

type Upstream struct {
	// name of package
	name string
	// package upstream version
	version string
	// template with placeholders for version (twice) and arch
	urlFormat string
}

// dldirToBinary takes as input dldir, a directory that go-getter wrote to,
// and the name of an upstream package (e.g. "consul").  It returns the binary
// which lives under dldir, unqualified (no slashes).
// We support two scenarios: either the binary is the sole file directly under
// dldir and we don't care what it's named, or dldir contains 1+ files of which one
// matches the name argument, and that's the one we return.
func dldirToBinary(dldir, name string) string {
	var source os.FileInfo
	fis, err := ioutil.ReadDir(dldir)
	if err != nil {
		log.Fatalf("error reading dir %s: %v", dldir, err)
	}

	if len(fis) == 1 {
		return fis[0].Name()
	}

	var fnames []string
	for _, fi := range fis {
		if fi.Name() == name {
			return name
		}

		fnames = append(fnames, fi.Name())
	}
	if source == nil {
		log.Fatalf("expected exactly one file in %q or a file named %q, but found: %v", dldir, name, fnames)
	}
	return ""
}

// getBinary fetches the binary if it's not already present locally, returning
// the path at which it may be found on disk.
func GetBinary(packageName, osName, arch, dldirBase string) (string, error) {
	o, ok := registry[packageName]
	if !ok {
		return "", fmt.Errorf("unknown package name %q", packageName)
	}

	name := fmt.Sprintf("%s-%s-%s-%s", o.name, o.version, osName, arch)
	dldir := filepath.Join(dldirBase, name)
	if err := os.MkdirAll(dldir, 0755); err != nil {
		return "", err
	}

	binname := filepath.Join(dldir, o.name)
	_, err := os.Stat(binname)
	if err == nil {
		// Already downloaded
		return binname, nil
	}

	client := &getter.Client{
		Src:  fmt.Sprintf(o.urlFormat, o.version, o.version, osName, arch),
		Dst:  dldir,
		Mode: getter.ClientModeAny,
	}
	if err := client.Get(); err != nil {
		return "", fmt.Errorf("go-getter error: %w", err)
	}

	relbin := dldirToBinary(dldir, o.name)
	fullbin := filepath.Join(dldir, o.name)
	if relbin != o.name {
		if err = os.Rename(filepath.Join(dldir, relbin), fullbin); err != nil {
			return "", fmt.Errorf("rename binary failed: %w", err)
		}
	}

	if err = os.Chmod(fullbin, 0755); err != nil {
		return "", fmt.Errorf("chmdo binary failed: %w", err)
	}

	return fullbin, nil
}

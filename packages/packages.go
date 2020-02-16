package packages

import (
	"fmt"
	"github.com/hashicorp/go-getter"
	"io/ioutil"
	"os"
	"path/filepath"
)

var registry = map[string]Upstream{
	"nomad": {
		name:      "nomad",
		version:   "0.10.3",
		urlFormat: "https://releases.hashicorp.com/nomad/%s/nomad_%s_%s_%s.zip",
	},
	"consul": {
		name:      "consul",
		version:   "1.7.0",
		urlFormat: "https://releases.hashicorp.com/consul/%s/consul_%s_%s_%s.zip",
	},
	"vault": {
		name:      "vault",
		version:   "1.3.2",
		urlFormat: "https://releases.hashicorp.com/vault/%s/vault_%s_%s_%s.zip",
	},
	"prometheus": {
		name:      "prometheus",
		version:   "2.16.0",
		urlFormat: "https://github.com/prometheus/prometheus/releases/download/v%s/prometheus-%s.%s-%s.tar.gz",
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
func dldirToBinary(dldir, packageName string) (string, error) {
	fis, err := ioutil.ReadDir(dldir)
	if err != nil {
		return "", err
	}

	switch {
	case len(fis) == 1 && fis[0].IsDir():
		return dldirToBinary(filepath.Join(dldir, fis[0].Name()), packageName)
	case len(fis) == 1 && fis[0].Name() == packageName:
		return filepath.Join(dldir, packageName), nil
	default:
		for _, fi := range fis {
			if fi.Name() == packageName && fi.Mode().IsRegular() && (fi.Mode()&0111) == 0111 {
				return filepath.Join(dldir, packageName), nil
			}
		}
	}

	return "", fmt.Errorf("didn't find %s under %s", packageName, dldir)
}

// getBinary fetches the binary if it's not already present locally, returning
// the path at which it may be found on disk.
func GetBinary(packageName, osName, arch, dldirBase string) (string, error) {
	o, ok := registry[packageName]
	if !ok {
		return "", fmt.Errorf("unknown package name %q", packageName)
	}

	fullname := fmt.Sprintf("%s-%s-%s-%s", o.name, o.version, osName, arch)
	dldir := filepath.Join(dldirBase, fullname)
	if err := os.MkdirAll(dldir, 0755); err != nil {
		return "", err
	}

	bindir := filepath.Join(dldirBase, "binaries")
	err := os.MkdirAll(bindir, 0755)
	if err != nil {
		return "", fmt.Errorf("error creating bin dir: %w", err)
	}
	binname := filepath.Join(bindir, fullname)
	_, err = os.Stat(binname)
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

	dlbin, err := dldirToBinary(dldir, packageName)
	if err != nil {
		return "", err
	}

	if err = os.Link(dlbin, binname); err != nil {
		return "", fmt.Errorf("link binary failed: %w", err)
	}

	return binname, nil
}

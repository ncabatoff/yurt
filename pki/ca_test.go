package pki

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
	"time"
)

type testenv struct {
	tmpdir  string
	cleanup func()
	ctx     context.Context
	ca      *CertificateAuthority
}

func testca(t *testing.T, timeout time.Duration) *testenv {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	tmpdir, err := ioutil.TempDir("", t.Name())
	if err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		cancel()
		_ = os.RemoveAll(tmpdir)
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	var ca *CertificateAuthority
	ca, err = NewCertificateAuthority(tmpdir, 8200)
	if err != nil {
		t.Fatal(err)
	}
	err = ca.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return &testenv{
		tmpdir:  tmpdir,
		cleanup: cleanup,
		ctx:     ctx,
		ca:      ca,
	}
}

func TestCertificateAuthority_ConsulServerTLS(t *testing.T) {
	te := testca(t, 10*time.Second)
	tlspem, err := te.ca.ConsulServerTLS(te.ctx, "192.168.2.51", "168h")
	if err != nil {
		t.Fatal(err)
	}
	if tlspem.CA == "" {
		t.Fatal("no cacert")
	}
	if tlspem.Cert == "" {
		t.Fatal("no cert")
	}
	if tlspem.PrivateKey == "" {
		t.Fatal("no key")
	}
}

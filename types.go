package yurt

import (
	"context"
	"github.com/hashicorp/go-sockaddr"
	"github.com/ncabatoff/yurt/pki"
)

// CertificateMaker makes TLS certificates appropriate for a particular use case.
// We are agnostic as to how they are stored and distributed, their lifetime,
// or what fields are stored.
type CertificateMaker interface {
	// MakeCertificate returns a certificate for the given hostname.  The
	// hostname and IP address are both optional, but typically provide
	// additional security.
	MakeCertificate(ctx context.Context, hostname, ip string) (*pki.TLSConfigPEM, error)
}

// A Node describes a service instance, typically a member or future member of a
// cluster.  The Node may not yet exist.  Multiple nodes of different types may
// map to the same host, e.g. a host might run both Consul and Nomad, and may
// use different TLS for each.
type Node struct {
	// Name is the node name, which may mean different things to different
	// services.  May also be the host name for some drivers.  May also be
	// resolvable with some drivers, though not necessarily by the cluster
	// creating code.
	Name      string
	FirstPort int
	StaticIP  string
	WorkDir   string
	// TLS cert (optional)
	TLS *pki.TLSConfigPEM
}

/*
// Address returns the address of a service running on the node.
func (n Node) Address(firstPortOffset int) string {
	switch {
	case n.FirstPort > 0:
		return fmt.Sprintf("127.0.0.1:%d", n.FirstPort+firstPortOffset)
	case n.StaticIP != "":
		return n.StaticIP
	default:
		return n.Name
	}
}
*/

type NetworkConfig struct {
	Network       sockaddr.SockAddr
	DockerNetName string
}

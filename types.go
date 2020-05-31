package yurt

import (
	"context"
	"fmt"
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
	Name  string
	Ports Ports
	Host  string
	TLS   *pki.TLSConfigPEM
}

// Address returns the host:port address of a service running on the node.
func (n Node) Address(name string) (string, error) {
	port := n.Ports.ByName[name]
	if port.Number == 0 {
		return "", fmt.Errorf("no address for service %q", name)
	}
	return fmt.Sprintf("%s:%d", n.Host, port.Number), nil
}

type NetworkConfig struct {
	Network       sockaddr.SockAddr
	DockerNetName string
}

type PortNetworkType int

const TCPOnly PortNetworkType = 0
const UDPOnly PortNetworkType = 1
const TCPAndUDP PortNetworkType = 2

type Port struct {
	Number int
	Type   PortNetworkType
}

type Ports struct {
	// ByName is a map from port name (e.g. "http", "rpc") to port.
	ByName map[string]Port
	// NameOrder specifies the order to assign ports sequentially
	NameOrder []string
}

func (p Port) AsList() []string {
	var ret []string
	if p.Type == TCPOnly || p.Type == TCPAndUDP {
		ret = append(ret, fmt.Sprintf("%d/tcp", p.Number))
	}
	if p.Type == UDPOnly || p.Type == TCPAndUDP {
		ret = append(ret, fmt.Sprintf("%d/udp", p.Number))
	}
	return ret
}

func (p Ports) Sequential(firstPort int) Ports {
	for _, name := range p.NameOrder {
		p.ByName[name] = Port{
			Number: firstPort,
			Type:   p.ByName[name].Type,
		}
		firstPort++
	}
	return p
}

func (p Ports) AsList() []string {
	var ret []string
	for _, name := range p.NameOrder {
		ret = append(ret, p.ByName[name].AsList()...)
	}
	return ret
}

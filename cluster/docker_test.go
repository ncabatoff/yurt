package cluster

import (
	"fmt"
	"math/rand"
	"net"
	"testing"
	"time"

	"github.com/ncabatoff/yurt/pki"
	"github.com/ncabatoff/yurt/runner"
	"github.com/ncabatoff/yurt/runner/docker"
	"github.com/ncabatoff/yurt/testutil"
)

const (
	imageConsul = "consul:1.7.0-beta4"
	// There's no official nomad docker image
	imageNomad = "noenv/nomad:0.10.3"
)

func init() {
	rand.Seed(int64(time.Now().Nanosecond()))
}

func ipnet(t *testing.T, cidr string) (net.IP, net.IPNet) {
	t.Helper()
	i, n, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return i, *n
}

func threeNodeConsulDocker(t *testing.T, te testutil.DockerTestEnv) (*ConsulClusterRunner, error) {
	names := []string{"consul-srv-1", "consul-srv-2", "consul-srv-3", "consul-cli-1"}
	var ips []string
	netip, _ := ipnet(t, te.NetConf.Network.String())
	serverIP := netip.To4()
	for i := range names {
		serverIP[3] = byte(i) + 51
		ips = append(ips, serverIP.String())
	}
	return BuildConsulCluster(te.Ctx,
		ConsulClusterConfigFixedIPs{
			NetworkConfig:   te.NetConf,
			WorkDir:         te.TmpDir,
			ServerNames:     names,
			ConsulServerIPs: ips[:3],
		},
		&docker.ConsulDockerServerBuilder{
			DockerAPI: te.Docker,
			Image:     imageConsul,
			IPs:       ips,
		},
	)
}

func threeNodeConsulDockerTLS(t *testing.T, te testutil.DockerTestEnv, ca *pki.CertificateAuthority) (*ConsulClusterRunner, error) {
	names := []string{"consul-srv-1", "consul-srv-2", "consul-srv-3", "consul-cli-1"}
	certs := make(map[string]pki.TLSConfigPEM)
	var ips []string
	netip, _ := ipnet(t, te.NetConf.Network.String())
	serverIP := netip.To4()
	for i := range names {
		serverIP[3] = byte(i) + 51
		ips = append(ips, serverIP.String())

		tls, err := ca.ConsulServerTLS(te.Ctx, serverIP.String(), "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[names[i]] = *tls
	}
	return BuildConsulCluster(te.Ctx,
		ConsulClusterConfigFixedIPs{
			NetworkConfig:   te.NetConf,
			WorkDir:         te.TmpDir,
			ServerNames:     names,
			ConsulServerIPs: ips[:3],
			TLS:             certs,
		},
		&docker.ConsulDockerServerBuilder{
			DockerAPI: te.Docker,
			Image:     imageConsul,
			IPs:       ips,
		},
	)
}

// TestConsulDockerCluster tests a three node docker Consul cluster.
func TestConsulDockerCluster(t *testing.T) {
	t.Parallel()
	te := testutil.NewDockerTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	if _, err := threeNodeConsulDocker(t, te); err != nil {
		t.Fatal(err)
	}
}

// TestConsulDockerClusterTLS tests a three node docker Consul cluster with TLS.
func TestConsulDockerClusterTLS(t *testing.T) {
	t.Parallel()
	te := testutil.NewDockerTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	ca, err := pki.NewCertificateAuthority(Vault.Cli)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := threeNodeConsulDockerTLS(t, te, ca); err != nil {
		t.Fatal(err)
	}
}

// TestConsulDockerClusterTLS tests a three node docker Consul and a three node
// docker Nomad cluster with TLS.
func TestNomadDockerCluster(t *testing.T) {
	t.Parallel()
	te := testutil.NewDockerTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	consulCluster, err := threeNodeConsulDocker(t, te)
	if err != nil {
		t.Fatal(err)
	}
	consulClient, err := consulCluster.Client()
	if err != nil {
		t.Fatal(err)
	}
	ip := consulClient.(*docker.ConsulDockerRunner).IP

	_, err = BuildNomadCluster(te.Ctx, NomadClusterConfigFixedIPs{
		NetworkConfig: te.NetConf,
		WorkDir:       te.TmpDir,
		ServerNames:   []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3"},
		ConsulAddrs:   append(consulCluster.Config.APIAddrs(), fmt.Sprintf("%s:%d", ip, runner.DefConsulPorts().HTTP)),
	}, &docker.NomadDockerBuilder{
		DockerAPI: te.Docker,
		Image:     imageNomad,
	})
	if err != nil {
		t.Fatal(err)
	}

	//consul, err := consulClient.ConsulAPI()
	//if err != nil {
	//	t.Fatal(err)
	//}
	//nomad, err := cluster.clients[0].NomadAPI()
	//if err != nil {
	//	t.Fatal(err)
	//}
	//testJobs(t, te.Ctx, consul, nomad, execJobHCL)
}

var dockerJobHCL = `
job "prometheus" {
  datacenters = ["dc1"]
  type = "service"
  group "prometheus" {
    task "prometheus" {
      template {
        destination = "local/prometheus.yml"
        data = <<EOH
global:
  scrape_interval: "1s"

scrape_configs:
- job_name: prometheus-local
  static_configs:
  - targets: ['{{env "NOMAD_ADDR_http"}}']
EOH
      }
      driver = "docker"
      config {
        image = "prom/prometheus:v2.16.0"
        args = [
          #"--config.file=/local/prometheus.yml",
          #"--storage.tsdb.path=/alloc/data/prometheus",
          "--config.file=${NOMAD_TASK_DIR}/prometheus.yml",
          "--storage.tsdb.path=${NOMAD_TASK_DIR}/data/prometheus",
          "--web.listen-address=${NOMAD_ADDR_http}",
        ]
      }
      resources {
        network {
          port "http" {}
        }
      }
      service {
        name = "prometheus"
        port = "http"
        check {
          type = "http"
          port = "http"
          path = "/"
          interval = "3s"
          timeout = "1s"
        }
      }
    }
  }
} 
`

func TestNomadDockerClusterTLS(t *testing.T) {
	t.Parallel()
	te := testutil.NewDockerTestEnv(t, 30*time.Second)
	defer te.Cleanup()

	ca, err := pki.NewCertificateAuthority(Vault.Cli)
	if err != nil {
		t.Fatal(err)
	}
	consulCluster, _ := threeNodeConsulDockerTLS(t, te, ca)
	if _, err := threeNodeNomadDockerTLS(t, te, ca, consulCluster); err != nil {
		t.Fatal(err)
	}
}

func threeNodeNomadDockerTLS(t *testing.T, te testutil.DockerTestEnv, ca *pki.CertificateAuthority, consulCluster *ConsulClusterRunner) (*NomadClusterRunner, error) {
	names := []string{"nomad-srv-1", "nomad-srv-2", "nomad-srv-3", "nomad-cli-1"}
	certs := make(map[string]pki.TLSConfigPEM)
	var ips []string
	netip, _ := ipnet(t, te.NetConf.Network.String())
	serverIP := netip.To4()
	for i := range names {
		serverIP[3] = byte(i) + 61
		ips = append(ips, serverIP.String())

		tls, err := ca.NomadServerTLS(te.Ctx, serverIP.String(), "10m")
		if err != nil {
			t.Fatal(err)
		}
		certs[names[i]] = *tls
	}

	consulClient, err := consulCluster.Client()
	if err != nil {
		t.Fatal(err)
	}
	ip := consulClient.(*docker.ConsulDockerRunner).IP
	return BuildNomadCluster(te.Ctx,
		NomadClusterConfigFixedIPs{
			NetworkConfig:  te.NetConf,
			WorkDir:        te.TmpDir,
			ServerNames:    names[:3],
			NomadServerIPs: ips,
			ConsulAddrs:    append(consulCluster.Config.APIAddrs(), fmt.Sprintf("%s:%d", ip, runner.DefConsulPorts().HTTP)),
			TLS:            certs,
		},
		&docker.NomadDockerServerBuilder{
			DockerAPI: te.Docker,
			Image:     imageNomad,
			IPs:       ips,
		},
	)
}

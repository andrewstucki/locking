package acceptance

import (
	"fmt"
	"os/exec"
	"testing"
	"time"

	mctesting "github.com/andrewstucki/locking/multicluster/testing"
)

func SetupClusters(t *testing.T, names []string, cleanup ...bool) ConnectedClusters {
	t.Helper()

	shouldCleanup := true
	if len(cleanup) != 0 && !cleanup[0] {
		shouldCleanup = false
	}

	var clusters []*ConnectedCluster
	network := mctesting.GenerateRandomString(5)
	ports := mctesting.GetFreePorts(t, len(names))

	if shouldCleanup {
		t.Cleanup(func() {
			_, _ = exec.Command("docker", "network", "rm", network).CombinedOutput()
		})
	}

	for i, name := range names {
		domain := fmt.Sprintf("%s.kubernetes.local", name)
		cluster, _, err := GetOrCreate(name, WithAgents(0), WithDomain(domain), WithNetwork(network), WithPort(ports[i]), WithNoWait())
		if err != nil {
			t.Fatalf("error creating cluster: %v", err)
		}
		cluster.cleanup = shouldCleanup
		if shouldCleanup {
			t.Cleanup(func() {
				if err := cluster.Cleanup(); err != nil {
					t.Fatalf("error cleaning up cluster: %v", err)
				}
			})
		}
		clusters = append(clusters, &ConnectedCluster{
			Cluster: cluster,
			domain:  domain,
		})
	}

	return clusters
}

func (c *ConnectedCluster) IP(t *testing.T) string {
	return c.waitForIP(t)
}

func (c *ConnectedCluster) waitForIP(t *testing.T) string {
	t.Helper()

	// wait up to 30 seconds
	for range 30 {
		out, err := exec.Command("kubectl", "get", "svc", "--context", "k3d-"+c.Name, "-n", "kube-system", "traefik",
			"--template", `{{range .status.loadBalancer.ingress}}{{.ip}}{{end}}`,
		).CombinedOutput()
		if err == nil {
			t.Logf("got ip: %s", string(out))
		}
		if err == nil && string(out) != "" {
			return string(out)
		}

		time.Sleep(1 * time.Second)
	}

	t.Fatalf("never received ingress ip for %q", c.Name)
	return ""
}

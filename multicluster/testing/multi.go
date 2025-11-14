package testing

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"math/rand"
	"net"
	"os"
	"os/exec"
	"path"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ConnectedCluster struct {
	*Cluster
	domain             string
	ca                 CACertificate
	linkerdCertificate Certificate
	initialized        bool
	tmp                string
}

func (c *ConnectedCluster) DNSNames(name, namespace string) []string {
	return []string{
		name,
		name + "." + namespace,
		name + "." + namespace + ".svc",
		name + "." + namespace + ".svc." + c.domain,
		// for exports
		name + "-" + c.Name,
		name + "-" + c.Name + "." + namespace,
		name + "-" + c.Name + "." + namespace + ".svc",
		name + "-" + c.Name + "." + namespace + ".svc." + c.domain,
	}
}

func (c *ConnectedCluster) RemoteFQDN(name, namespace string) string {
	return name + "-" + c.Name + "." + namespace + ".svc." + c.domain
}

func (c *ConnectedCluster) RemoteName(name string) string {
	return name + "-" + c.Name
}

func (c *ConnectedCluster) ServiceAccountKubeconfig(t *testing.T, name, namespace string) *corev1.Secret {
	t.Helper()

	c.Create(t, &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + "-token",
			Namespace: namespace,
			Annotations: map[string]string{
				corev1.ServiceAccountNameKey: name,
			},
		},
		Type: corev1.SecretTypeServiceAccountToken,
	})

	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + "-token",
			Namespace: namespace,
		},
	}
	c.wait(t, &secret, func(o client.Object) bool {
		secret := o.(*corev1.Secret)
		return len(secret.Data["token"]) != 0
	})

	token := string(secret.Data["token"])
	ca := base64.StdEncoding.EncodeToString(secret.Data["ca.crt"])

	ip := c.waitForIP(t)

	var buf bytes.Buffer
	err := kubeconfigTemplate.Execute(&buf, struct {
		CA        string
		APIServer string
		Cluster   string
		User      string
		Token     string
	}{
		CA:        ca,
		APIServer: fmt.Sprintf("https://%s:6443", ip),
		Token:     token,
		User:      name,
		Cluster:   "k3d-" + c.Name,
	})

	if err != nil {
		t.Fatalf("error executing template: %v", err)
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + "-" + c.Name + "-kubeconfig",
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"kubeconfig.yaml": buf.Bytes(),
		},
	}
}

var kubeconfigTemplate = template.Must(template.New("").Parse(`
apiVersion: v1
clusters:
- cluster:
    certificate-authority-data: {{.CA}}
    server: {{.APIServer}}
  name: {{.Cluster}}
contexts:
- context:
    cluster: {{.Cluster}}
    user: {{.User}}
  name: {{.Cluster}}
current-context: {{.Cluster}}
kind: Config
preferences: {}
users:
- name: {{.User}}
  user:
    token: {{.Token}}
`))

func (c *ConnectedCluster) CheckConnectivity(t *testing.T) {
	if out, err := exec.Command("linkerd", "--context", "k3d-"+c.Name, "mc", "check").CombinedOutput(); err != nil {
		t.Fatalf("error checking linkerd connectivity: %v: %s", err, string(out))
	}
}

func (c *ConnectedCluster) initializeTemp(t *testing.T) {
	tmp, err := os.MkdirTemp("", "multicluster*")
	if err != nil {
		t.Fatalf("error making temporary directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(tmp); err != nil {
			t.Fatalf("failed to cleanup temporary files: %v", err)
		}
	})
	c.tmp = tmp
}

func (c *ConnectedCluster) dumpCertificates(t *testing.T) {
	root := path.Join(c.tmp, "root.crt")
	crt := path.Join(c.tmp, "ca.crt")
	key := path.Join(c.tmp, "ca.key")
	if err := os.WriteFile(root, c.ca.pem, 0o644); err != nil {
		t.Fatalf("failed to write temporary root ca file: %v", err)
	}
	if err := os.WriteFile(crt, c.linkerdCertificate.certificate, 0o644); err != nil {
		t.Fatalf("failed to write temporary cert file: %v", err)
	}
	if err := os.WriteFile(key, c.linkerdCertificate.privateKey, 0o644); err != nil {
		t.Fatalf("failed to write temporary cert file: %v", err)
	}
}

func (c *ConnectedCluster) installLinkerdCRDs(t *testing.T) {
	t.Log("installing linkerd CRDs to cluster", c.Name)
	cmd := exec.Command("linkerd", "install", "--context", "k3d-"+c.Name, "--crds")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("error installing linkerd: %v: %s", err, stderr.String())
	}

	crds := path.Join(c.tmp, "crds.yaml")
	if err := os.WriteFile(crds, stdout.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write temporary CRD file: %v", err)
	}

	if out, err := exec.Command("kubectl", "apply", "--context", "k3d-"+c.Name, "-f", crds).CombinedOutput(); err != nil {
		t.Fatalf("error installing linkerd: %v: %s", err, string(out))
	}
}

func (c *ConnectedCluster) installLinkerd(t *testing.T) {
	t.Log("installing linkerd to cluster", c.Name)
	cmd := exec.Command("linkerd", []string{"install", "--context", "k3d-" + c.Name,
		`--cluster-domain`, c.domain,
		`--identity-trust-domain`, c.domain,
		`--identity-trust-anchors-file`, path.Join(c.tmp, "root.crt"),
		`--identity-issuer-certificate-file`, path.Join(c.tmp, "ca.crt"),
		`--identity-issuer-key-file`, path.Join(c.tmp, "ca.key"),
	}...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("error installing linkerd: %v: %s", err, stderr.String())
	}

	install := path.Join(c.tmp, "install.yaml")
	if err := os.WriteFile(install, stdout.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write temporary installation file: %v", err)
	}

	if out, err := exec.Command("kubectl", "apply", "--context", "k3d-"+c.Name, "-f", install).CombinedOutput(); err != nil {
		t.Fatalf("error installing linkerd: %v: %s", err, string(out))
	}
}

func (c *ConnectedCluster) linkCluster(t *testing.T, useIP bool, other *ConnectedCluster) {
	ip := other.waitForIP(t)

	t.Logf("linking cluster %q with cluster %q", c.Name, other.Name)

	args := []string{"multicluster", "--context", "k3d-" + other.Name,
		`link`, `--cluster-name`, other.Name,
	}

	if useIP {
		args = append(args, `--api-server-address`, "https://"+ip+":6443")
	}

	cmd := exec.Command("linkerd", args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("error linking clusters: %v: %s", err, stderr.String())
	}

	install := path.Join(c.tmp, "link-"+other.Name+".yaml")
	if err := os.WriteFile(install, stdout.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write temporary installation file: %v", err)
	}

	if out, err := exec.Command("kubectl", "apply", "--context", "k3d-"+c.Name, "-f", install).CombinedOutput(); err != nil {
		t.Fatalf("error linking clusters: %v: %s", err, string(out))
	}
}

func (c *ConnectedCluster) installLinkerdMultiCluster(t *testing.T) {
	t.Log("installing multicluster linkerd to cluster", c.Name)
	cmd := exec.Command("linkerd", []string{"multicluster", "install", "--context", "k3d-" + c.Name}...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("error installing multicluster linkerd: %v: %s", err, stderr.String())
	}

	install := path.Join(c.tmp, "install-multicluster.yaml")
	if err := os.WriteFile(install, stdout.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write temporary installation file: %v", err)
	}

	if out, err := exec.Command("kubectl", "apply", "--context", "k3d-"+c.Name, "-f", install).CombinedOutput(); err != nil {
		t.Fatalf("error installing multicluster linkerd: %v: %s", err, string(out))
	}
}

func (c *ConnectedCluster) waitForIP(t *testing.T) string {
	t.Helper()

	// wait up to 30 seconds
	for range 30 {
		out, err := exec.Command("kubectl", "get", "svc", "--context", "k3d-"+c.Name, "-n", "linkerd-multicluster", "linkerd-gateway",
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

	t.Fatalf("never received gateway ip for %q", c.Name)
	return ""
}

type ConnectedClusters []*ConnectedCluster

func (c ConnectedClusters) Cluster(t *testing.T, name string) *ConnectedCluster {
	t.Helper()

	for _, cluster := range c {
		if cluster.Name == name {
			return cluster
		}
	}

	t.Fatalf("unable to find cluster named: %q", name)
	return nil
}

func (c ConnectedClusters) ImportImages(t *testing.T, images ...string) {
	t.Helper()

	for _, cluster := range c {
		if err := cluster.ImportImage(images...); err != nil {
			t.Fatalf("failed to import images to cluster %v", err)
		}
	}
}

func SetupClusters(t *testing.T, names []string, cleanup ...bool) ConnectedClusters {
	t.Helper()

	shouldCleanup := true
	if len(cleanup) != 0 && !cleanup[0] {
		shouldCleanup = false
	}

	var clusters []*ConnectedCluster
	network := generateRandomString(5)
	ports := getFreePorts(t, len(names))

	ca := GenerateCA(t)
	for i, name := range names {
		domain := fmt.Sprintf("%s.kubernetes.local", name)
		cluster, created, err := GetOrCreate(name, WithAgents(0), WithDomain(domain), WithNetwork(network), WithPort(ports[i]), WithNoWait())
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
			Cluster:            cluster,
			ca:                 ca,
			domain:             domain,
			initialized:        !created,
			linkerdCertificate: ca.SignCA(t, "identity.linkerd."+domain),
		})
	}

	for _, cluster := range clusters {
		if cluster.initialized {
			continue
		}

		cluster.initializeTemp(t)
		cluster.dumpCertificates(t)
		cluster.installLinkerdCRDs(t)
		cluster.installLinkerd(t)
		cluster.installLinkerdMultiCluster(t)
	}

	// do an initial link with the service ip so the endpoint slices come up
	for _, cluster := range clusters {
		if cluster.initialized {
			continue
		}

		for _, other := range clusters {
			if cluster.Name != other.Name {
				cluster.linkCluster(t, true, other)
			}
		}
	}

	// re-link without
	for _, cluster := range clusters {
		if cluster.initialized {
			continue
		}

		for _, other := range clusters {
			if cluster.Name != other.Name {
				cluster.linkCluster(t, false, other)
			}
		}
	}

	return clusters
}

func getFreePorts(t *testing.T, n int) []int {
	t.Helper()

	ports := make([]int, 0, n)
	listeners := make([]net.Listener, 0, n)

	for i := 0; i < n; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("error getting free port: %v", err)
		}
		listeners = append(listeners, l)
		ports = append(ports, l.Addr().(*net.TCPAddr).Port)
	}

	for _, l := range listeners {
		l.Close()
	}

	return ports
}

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func generateRandomString(length int) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

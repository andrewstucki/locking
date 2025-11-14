package testing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	DefaultK3sImage   = `rancher/k3s:v1.29.6-k3s2`
	K3sImageEnv       = `K3S_IMAGE`
	SharedClusterName = "testenv"
)

var (
	ErrExists = errors.New("cluster with name already exists")
)

type Cluster struct {
	Name string

	mu           sync.Mutex
	restConfig   *rest.Config
	agentCounter int32
	cleanup      bool
}

type ClusterOpt interface {
	apply(config *clusterConfig)
}

type clusterOpt func(config *clusterConfig)

func (c clusterOpt) apply(config *clusterConfig) {
	c(config)
}

type clusterConfig struct {
	agents           int
	timeout          time.Duration
	image            string
	serverNoSchedule bool
	domain           string
	port             int
	network          string
	extraFlags       []string
	wait             bool
}

func defaultClusterConfig() *clusterConfig {
	image := DefaultK3sImage
	if override, ok := os.LookupEnv(K3sImageEnv); ok {
		image = override
	}

	return &clusterConfig{
		agents:  3,
		timeout: 3 * time.Minute,
		image:   image,
		wait:    true,
	}
}

func WithAgents(agents int) clusterOpt {
	return func(config *clusterConfig) {
		config.agents = agents
	}
}

func WithServerNoSchedule() clusterOpt {
	return func(config *clusterConfig) {
		config.serverNoSchedule = true
	}
}

func WithImage(image string) clusterOpt {
	return func(config *clusterConfig) {
		config.image = image
	}
}

func WithTimeout(timeout time.Duration) clusterOpt {
	return func(config *clusterConfig) {
		config.timeout = timeout
	}
}

func WithDomain(domain string) clusterOpt {
	return func(config *clusterConfig) {
		config.domain = domain
	}
}

func WithPort(port int) clusterOpt {
	return func(config *clusterConfig) {
		config.port = port
	}
}

func WithNetwork(network string) clusterOpt {
	return func(config *clusterConfig) {
		config.network = network
	}
}

func WithFlags(flags ...string) clusterOpt {
	return func(config *clusterConfig) {
		config.extraFlags = flags
	}
}

func WithNoWait() clusterOpt {
	return func(config *clusterConfig) {
		config.wait = false
	}
}

func GetOrCreate(name string, opts ...ClusterOpt) (*Cluster, bool, error) {
	config := defaultClusterConfig()
	for _, opt := range opts {
		opt.apply(config)
	}

	cluster, err := NewCluster(name, opts...)
	if err != nil {
		if errors.Is(err, ErrExists) {
			cluster, err := loadCluster(name, config)
			return cluster, false, err
		}
		return nil, false, err
	}

	return cluster, true, nil
}

func NewCluster(name string, opts ...ClusterOpt) (*Cluster, error) {
	name = strings.ToLower(name)

	config := defaultClusterConfig()
	for _, opt := range opts {
		opt.apply(config)
	}

	args := []string{
		"cluster",
		"create",
		name,
		fmt.Sprintf("--agents=%d", config.agents),
		fmt.Sprintf("--timeout=%s", config.timeout),
		fmt.Sprintf("--image=%s", config.image),
		// If k3d cluster create will fail please uncomment no-rollback flag
		// "--no-rollback",
		// See also https://github.com/k3d-io/k3d/blob/main/docs/faq/faq.md#passing-additional-argumentsflags-to-k3s-and-on-to-eg-the-kube-apiserver
		// As the formatting is QUITE finicky.
		// Halve the node-monitor-grace-period to speed up tests that rely on dead node detection.
		`--k3s-arg`, `--kube-controller-manager-arg=node-monitor-grace-period=10s@server:*`,
		// Dramatically decrease (5m -> 10s) the default tolerations to ensure
		// Pod eviction happens in a timely fashion.
		`--k3s-arg`, `--kube-apiserver-arg=default-not-ready-toleration-seconds=10@server:*`,
		`--k3s-arg`, `--kube-apiserver-arg=default-unreachable-toleration-seconds=10@server:*`,
		`--k3s-arg`, `--disable=local-storage,metrics-server@server:*`,
		`--verbose`,
	}

	if config.domain != "" {
		args = append(args, `--k3s-arg`, `--cluster-domain=`+config.domain+"@server:*")
	}

	if config.port != 0 {
		args = append(args, `--api-port`, strconv.Itoa(config.port))
	}

	if config.network != "" {
		args = append(args, `--network`, config.network)
	}

	if config.serverNoSchedule {
		args = append(args, []string{
			// This can be useful for tests in which we don't want to accidentally
			// kill the API server when we delete arbitrary nodes to simulate
			// hardware failures
			`--k3s-arg`, `--node-taint=server=true:NoSchedule@server:*`,
		}...)
	}

	args = append(args, config.extraFlags...)

	out, err := exec.Command("k3d", args...).CombinedOutput()
	if err != nil {
		if bytes.Contains(out, []byte(`because a cluster with that name already exists`)) {
			return nil, fmt.Errorf("%q: %w", name, ErrExists)
		}

		// If k3d cluster create will fail please uncomment the following debug logs from containers
		for i := 0; i < config.agents; i++ {
			containerLogs, _ := exec.Command("docker", "logs", fmt.Sprintf("k3d-%s-agent-%d", name, i)).CombinedOutput()
			fmt.Printf("Agent-%d logs:\n%s\n", i, string(containerLogs))
		}
		containerLogs, _ := exec.Command("docker", "logs", fmt.Sprintf("k3d-%s-server-0", name)).CombinedOutput()
		fmt.Printf("server-0 logs:\n%s\n", string(containerLogs))
		containerLogs, _ = exec.Command("docker", "network", "inspect", fmt.Sprintf("k3d-%s", name)).CombinedOutput()
		fmt.Printf("docker network inspect:\n%s\n", string(containerLogs))

		return nil, fmt.Errorf("%s: %w", out, err)
	}

	return loadCluster(name, config)
}

func loadCluster(name string, config *clusterConfig) (*Cluster, error) {
	kubeconfigYAML, err := exec.Command("k3d", "kubeconfig", "get", name).CombinedOutput()
	if err != nil {
		return nil, err
	}

	kubeconfig, err := clientcmd.Load(kubeconfigYAML)
	if err != nil {
		return nil, err
	}

	clientConfig := clientcmd.NewNonInteractiveClientConfig(*kubeconfig, kubeconfig.CurrentContext, &clientcmd.ConfigOverrides{}, nil)
	cfg, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, err
	}

	cluster := &Cluster{
		Name:         name,
		restConfig:   cfg,
		agentCounter: int32(config.agents),
		cleanup:      true,
	}

	if config.wait {
		if err := cluster.waitForJobs(context.Background()); err != nil {
			return nil, err
		}
	}

	return cluster, nil
}

func (c *Cluster) Wait(ctx context.Context) error {
	return c.waitForJobs(ctx)
}

func (c *Cluster) RESTConfig() *rest.Config {
	return c.restConfig
}

func (c *Cluster) ImportImage(images ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if out, err := exec.Command(
		"k3d",
		"image",
		"import",
		fmt.Sprintf("--cluster=%s", c.Name),
		strings.Join(images, " "),
	).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func (c *Cluster) DeleteNode(name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if out, err := exec.Command(
		"k3d",
		"node",
		"delete",
		name,
	).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func (c *Cluster) CreateNode() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.agentCounter += 1

	return c.CreateNodeWithName(fmt.Sprintf("%s-agent-%d", c.Name, c.agentCounter))
}

func (c *Cluster) CreateNodeWithName(name string) error {
	if out, err := exec.Command(
		"k3d",
		"node",
		"create",
		name,
		fmt.Sprintf("--cluster=%s", c.Name),
		"--wait",
		"--role=agent",
	).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

func (c *Cluster) Cleanup() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	_, err := exec.Command(
		"k3d",
		"cluster",
		"delete",
		c.Name,
	).CombinedOutput()
	return err
}

// setupCluster applies any embedded manifests and  blocks until all jobs in
// the kube-system namespace have completed. This is a course grain way to wait
// for k3s to finish installing it's bundled dependencies via helm.
// See: https://docs.k3s.io/helm, https://k3d.io/v5.7.4/usage/k3s/?h=
func (c *Cluster) waitForJobs(ctx context.Context) error {
	cl, err := client.New(c.restConfig, client.Options{})
	if err != nil {
		return err
	}

	// Wait for all bootstrapping jobs to finish running.
	return wait.PollUntilContextTimeout(ctx, time.Second, 2*time.Minute, false, func(ctx context.Context) (done bool, err error) {
		var jobs batchv1.JobList
		if err := cl.List(ctx, &jobs, client.InNamespace("kube-system")); err != nil {
			return false, err
		}

		for _, job := range jobs.Items {
			idx := slices.IndexFunc(job.Status.Conditions, func(cond batchv1.JobCondition) bool {
				return cond.Type == batchv1.JobComplete
			})

			if idx == -1 || job.Status.Conditions[idx].Status != corev1.ConditionTrue {
				return false, nil
			}
		}

		return true, nil
	})
}

func (c *Cluster) Create(t *testing.T, objs ...client.Object) {
	t.Helper()

	cl, err := client.New(c.restConfig, client.Options{})
	if err != nil {
		t.Fatalf("error initializing client: %v", err)
	}

	for _, o := range objs {
		if err := cl.Create(t.Context(), o); err != nil {
			t.Fatalf("error creating object: %v", err)
		}

		if c.cleanup {
			t.Cleanup(func() {
				if err := cl.Delete(context.Background(), o); err != nil {
					t.Fatalf("error deleting object: %v", err)
				}
			})
		}
	}
}

func (c *Cluster) Exec(t *testing.T, target, container string, commands ...string) string {
	t.Helper()

	var out []byte
	var err error
	for range 10 {
		args := append([]string{"exec", "--context", "k3d-" + c.Name, target, "-c", container, "--"}, commands...)
		out, err = exec.Command("kubectl", args...).CombinedOutput()
		if err != nil {
			err = fmt.Errorf("error execing into container %q: %v: %s", container, err, string(out))
			time.Sleep(1 * time.Second)
			continue
		}
		return string(out)
	}

	if err != nil {
		t.Fatal(err.Error())
	}

	return string(out)
}

func (c *Cluster) wait(t *testing.T, obj client.Object, cond func(o client.Object) bool) {
	t.Helper()

	cl, err := client.New(c.restConfig, client.Options{})
	if err != nil {
		t.Fatalf("error initializing client: %v", err)
	}

	for range 30 {
		if err := cl.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); err != nil {
			t.Fatalf("error fetching object: %v", err)
		}

		if cond(obj) {
			return
		}
		time.Sleep(1 * time.Second)
	}

	t.Fatal("condition never met")
}

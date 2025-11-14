package integration

import (
	"context"
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/andrewstucki/locking/multicluster"
	mctesting "github.com/andrewstucki/locking/multicluster/testing"
	"github.com/go-logr/logr/testr"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
)

type TestClusters []*TestCluster

func (c TestClusters) Cluster(t *testing.T, name string) *TestCluster {
	t.Helper()

	for _, cluster := range c {
		if cluster.Name == name {
			return cluster
		}
	}

	t.Fatalf("unable to find cluster named: %q", name)
	return nil
}

type TestCluster struct {
	Name           string
	CA             mctesting.CACertificate
	Certificate    mctesting.Certificate
	APIServer      *TestAPIServer
	Config         multicluster.RaftConfiguration
	KubeconfigFile string

	tmp string
}

func (c *TestCluster) Create(t *testing.T, objs ...client.Object) {
	t.Helper()

	for _, o := range objs {
		if err := c.APIServer.client.Create(t.Context(), o); err != nil {
			t.Fatalf("error creating object: %v", err)
		}

		t.Cleanup(func() {
			if err := c.APIServer.client.Delete(context.Background(), o); err != nil {
				t.Fatalf("error deleting object: %v", err)
			}
		})
	}
}

func (c *TestCluster) dumpConfig(t *testing.T) {
	var err error
	c.tmp, err = os.MkdirTemp("", "mcintegration-*")
	if err != nil {
		t.Fatalf("error creating temp file: %v", err)
	}

	t.Cleanup(func() {
		_ = os.RemoveAll(c.tmp)
	})

	c.Config.CAFile = path.Join(c.tmp, "ca.crt")
	if err := os.WriteFile(c.Config.CAFile, c.CA.Bytes(), 0o644); err != nil {
		t.Fatalf("error writing ca file: %v", err)
	}

	c.Config.CertificateFile = path.Join(c.tmp, "tls.crt")
	if err := os.WriteFile(c.Config.CertificateFile, c.Certificate.Bytes(), 0o644); err != nil {
		t.Fatalf("error writing certificate file: %v", err)
	}

	c.Config.PrivateKeyFile = path.Join(c.tmp, "tls.key")
	if err := os.WriteFile(c.Config.PrivateKeyFile, c.Certificate.PrivateKeyBytes(), 0o644); err != nil {
		t.Fatalf("error writing private key file: %v", err)
	}

	c.Config.PrivateKeyFile = path.Join(c.tmp, "tls.key")
	if err := os.WriteFile(c.Config.PrivateKeyFile, c.Certificate.PrivateKeyBytes(), 0o644); err != nil {
		t.Fatalf("error writing private key file: %v", err)
	}

	c.KubeconfigFile = path.Join(c.tmp, "kubeconfig.yaml")

	kubeConfig := clientcmdapi.NewConfig()
	kubeConfig.Clusters["default-cluster"] = &clientcmdapi.Cluster{
		Server:                   c.APIServer.config.Host,
		CertificateAuthorityData: c.APIServer.config.TLSClientConfig.CAData,
	}
	kubeConfig.AuthInfos["default-user"] = &clientcmdapi.AuthInfo{
		ClientCertificateData: c.APIServer.config.TLSClientConfig.CertData,
		ClientKeyData:         c.APIServer.config.TLSClientConfig.KeyData,
		Token:                 c.APIServer.config.BearerToken,
	}
	kubeConfig.Contexts["default-context"] = &clientcmdapi.Context{
		Cluster:  "default-cluster",
		AuthInfo: "default-user",
	}
	kubeConfig.CurrentContext = "default-context"

	if err := clientcmd.WriteToFile(*kubeConfig, c.KubeconfigFile); err != nil {
		t.Fatalf("error writing kubeconfig file: %v", err)
	}
}

func (c *TestCluster) WaitFor(t *testing.T, obj client.Object, cond func(o client.Object) bool) {
	t.Helper()

	for range 30 {
		if err := c.APIServer.client.Get(t.Context(), client.ObjectKeyFromObject(obj), obj); err != nil {
			t.Fatalf("error fetching object: %v", err)
		}

		if cond(obj) {
			return
		}
		time.Sleep(1 * time.Second)
	}

	t.Fatal("condition never met")
}

func SetupTest(t *testing.T, names []string, build func(mcmanager.Manager, *TestCluster, *mcbuilder.Builder) error) TestClusters {
	if len(names) < 3 {
		t.Fatalf("at least three servers are required")
	}

	ctrl.SetLogger(testr.New(t))

	ports := mctesting.GetFreePorts(t, len(names))
	ca := mctesting.GenerateCA(t)
	clusters := []*TestCluster{}
	raftClusters := []multicluster.RaftCluster{}

	for i, name := range names {
		scheme := runtime.NewScheme()
		if err := clientgoscheme.AddToScheme(scheme); err != nil {
			t.Fatalf("failed to register client go scheme: %v", err)
		}
		apiServer := RunTestServer(t)
		address := fmt.Sprintf("127.0.0.1:%d", ports[i])
		cluster := &TestCluster{
			Name:        name,
			CA:          ca,
			Certificate: ca.Sign(t, "127.0.0.1"),
			APIServer:   apiServer,
			Config: multicluster.RaftConfiguration{
				Name:              name,
				Address:           address,
				Scheme:            scheme,
				ElectionTimeout:   1 * time.Second,
				HeartbeatInterval: 100 * time.Millisecond,
				Logger:            testr.New(t).WithName(name),
				RestConfig:        apiServer.config,
			},
		}
		cluster.dumpConfig(t)

		raftClusters = append(raftClusters, multicluster.RaftCluster{
			Name:           name,
			Address:        address,
			KubeconfigFile: cluster.KubeconfigFile,
		})
		clusters = append(clusters, cluster)
	}

	for _, cluster := range clusters {
		cluster.Config.Peers = raftClusters
	}

	for _, cluster := range clusters {
		manager, err := multicluster.NewRaftRuntimeManager(cluster.Config)
		if err != nil {
			t.Fatalf("error creating manager: %v", err)
		}
		if err := build(manager, cluster, mcbuilder.ControllerManagedBy(manager)); err != nil {
			t.Fatalf("error creating manager: %v", err)
		}
		go func() {
			if err := manager.Start(t.Context()); err != nil {
				t.Logf("error starting manager %q: %v", cluster.Name, err)
			}
		}()
	}

	return clusters
}

type TestAPIServer struct {
	config *rest.Config
	client client.Client
}

func RunTestServer(t *testing.T) *TestAPIServer {
	t.Helper()

	var environment envtest.Environment

	t.Cleanup(func() {
		_ = environment.Stop()
	})

	cfg, err := environment.Start()
	if err != nil {
		t.Fatalf("failed to start test environment: %v", err)
	}

	t.Logf("started environment with API server host: %s", cfg.Host)

	cl, err := client.New(cfg, client.Options{})
	if err != nil {
		t.Fatalf("failed to start test environment: %v", err)
	}

	return &TestAPIServer{
		client: cl,
		config: cfg,
	}
}

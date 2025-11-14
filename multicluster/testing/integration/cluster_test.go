package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func TestSetupCluster(t *testing.T) {
	clusters := SetupTest(t, []string{
		"a", "b", "c",
	}, func(cluster *TestCluster, b *mcbuilder.Builder) error {
		return b.For(&corev1.ConfigMap{}, mcbuilder.WithEngageWithLocalCluster(true)).Named(fmt.Sprintf("test-configmap-%s", cluster.Name)).Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			log := ctrllog.FromContext(ctx).WithValues("cluster", req.ClusterName, "namespace", req.Namespace, "name", req.Name)
			log.Info("Reconciling ConfigMap")

			return ctrl.Result{}, nil
		}))
	})

	clusters.Cluster(t, "a").Create(t, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: metav1.NamespaceDefault,
		},
	})

	time.Sleep(10 * time.Minute)
}

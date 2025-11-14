package integration

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func TestSetupClusters(t *testing.T) {
	clusters := SetupTest(t, []string{
		"a", "b", "c",
	}, func(manager mcmanager.Manager, cluster *TestCluster, b *mcbuilder.Builder) error {
		return b.For(&corev1.ConfigMap{}, mcbuilder.WithEngageWithLocalCluster(true)).Named(fmt.Sprintf("test-configmap-%s", cluster.Name)).Complete(mcreconcile.Func(func(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
			log := ctrllog.FromContext(ctx).WithValues("cluster", req.ClusterName, "namespace", req.Namespace, "name", req.Name)
			cluster, err := manager.GetCluster(ctx, req.ClusterName)
			if err != nil {
				return ctrl.Result{}, err
			}

			client := cluster.GetClient()
			var configmap corev1.ConfigMap
			if err := client.Get(ctx, req.NamespacedName, &configmap); err != nil {
				if apierrors.IsNotFound(err) {
					return ctrl.Result{}, nil
				}
				return ctrl.Result{}, err
			}

			if configmap.Annotations["integration.testing/reconcile"] == "true" {
				log.Info("Reconciling ConfigMap")
				if configmap.Data == nil {
					log.Info("Reconciling ConfigMap")
					configmap.Data = map[string]string{}
					log.Info("config map empty")
				} else {
					log.Info("config map value", "value", configmap.Data["reconciled"])
				}

				if configmap.Data["reconciled"] != "true" {
					configmap.Data["reconciled"] = "true"
					if err := client.Update(ctx, &configmap); err != nil {
						if apierrors.IsConflict(err) {
							return ctrl.Result{Requeue: true}, nil
						}
						return ctrl.Result{}, err
					}
				}
			}
			return ctrl.Result{}, nil
		}))
	})

	for _, cluster := range clusters {
		cluster.Create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "integration-test",
				Namespace: metav1.NamespaceDefault,
				Annotations: map[string]string{
					"integration.testing/reconcile": "true",
				},
			},
		})
	}

	for _, cluster := range clusters {
		cluster.WaitFor(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "integration-test",
				Namespace: metav1.NamespaceDefault,
			},
		}, func(o client.Object) bool {
			data := o.(*corev1.ConfigMap).Data
			return data != nil && data["reconciled"] == "true"
		})
	}
}

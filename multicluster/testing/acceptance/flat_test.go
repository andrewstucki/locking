package acceptance

import (
	"testing"

	"github.com/andrewstucki/locking/multicluster/bootstrap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSetupClusters(t *testing.T) {
	image := HasImageOrBuild(t, "local/operator:dev", "../../..", "operator/Dockerfile", false)

	clusters := SetupClusters(t, []string{"d", "e", "f"}, true)
	clusters.ImportImages(t, image)

	configuration := bootstrap.BootstrapClusterConfiguration{
		OperatorNamespace: metav1.NamespaceDefault,
		ServiceName:       "operator",
		RemoteClusters: []bootstrap.RemoteConfiguration{{
			ContextName:    clusters.Cluster(t, "d").ContextName(),
			APIServer:      "https://" + clusters.Cluster(t, "d").IP(t) + ":6443",
			ServiceAddress: clusters.Cluster(t, "d").IP(t),
		}, {
			ContextName:    clusters.Cluster(t, "e").ContextName(),
			APIServer:      "https://" + clusters.Cluster(t, "e").IP(t) + ":6443",
			ServiceAddress: clusters.Cluster(t, "e").IP(t),
		}, {
			ContextName:    clusters.Cluster(t, "f").ContextName(),
			APIServer:      "https://" + clusters.Cluster(t, "f").IP(t) + ":6443",
			ServiceAddress: clusters.Cluster(t, "f").IP(t),
		}},
	}

	if err := bootstrap.BootstrapKubernetesClusters(t.Context(), "acceptance", configuration); err != nil {
		t.Fatalf("error bootstrapping cluster: %v", err)
	}

	for _, cluster := range clusters {
		cluster.Create(t, operatorDeploymentForCluster(configuration, cluster, image)...)
	}

	for _, cluster := range clusters {
		cluster.Create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "acceptance-test",
				Namespace: metav1.NamespaceDefault,
				Annotations: map[string]string{
					"acceptance.testing/reconcile": "true",
				},
			},
		})
		cluster.WaitFor(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "acceptance-test",
				Namespace: metav1.NamespaceDefault,
			},
		}, func(o client.Object) bool {
			data := o.(*corev1.ConfigMap).Data
			return data != nil && data["reconciled"] == "true"
		})
	}
}

func operatorDeploymentForCluster(configuration bootstrap.BootstrapClusterConfiguration, cluster *ConnectedCluster, image string) []client.Object {
	peerVolumes := []corev1.Volume{}
	peerVolumeMounts := []corev1.VolumeMount{}
	peerArgs := []string{}
	for _, cluster := range configuration.RemoteClusters {
		name := configuration.ServiceName + "-" + cluster.ContextName
		peerVolumes = append(peerVolumes, corev1.Volume{
			Name: name + "-kubeconfig",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: name + "-kubeconfig",
					Items: []corev1.KeyToPath{{
						Key:  "kubeconfig.yaml",
						Path: "kubeconfig.yaml",
					}},
				},
			},
		})
		peerVolumeMounts = append(peerVolumeMounts, corev1.VolumeMount{
			Name:      name + "-kubeconfig",
			MountPath: "/config-" + name,
		})
		peerArgs = append(peerArgs, "--raft-peers", name+"://"+cluster.ServiceAddress+":9443/config-"+name+"/kubeconfig.yaml")
	}
	return []client.Object{
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "operator",
				Namespace: metav1.NamespaceDefault,
			},
			Rules: []rbacv1.PolicyRule{{
				Verbs:     []string{"watch", "list", "get", "update"},
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
			}},
		}, &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "operator",
				Namespace: metav1.NamespaceDefault,
			},
			Subjects: []rbacv1.Subject{{
				Kind:      "ServiceAccount",
				Name:      "operator",
				Namespace: metav1.NamespaceDefault,
			}},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Name:     "operator",
				Kind:     "ClusterRole",
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configuration.ServiceName,
				Namespace: metav1.NamespaceDefault,
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": configuration.ServiceName,
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": configuration.ServiceName,
						},
					},
					Spec: corev1.PodSpec{
						ServiceAccountName: "operator",
						Containers: []corev1.Container{{
							Name:  configuration.ServiceName,
							Image: image,
							Ports: []corev1.ContainerPort{{
								Name:          "https",
								ContainerPort: 9443,
							}},
							Args: append([]string{
								"--raft-node-name", configuration.ServiceName + "-" + cluster.ContextName(),
								"--raft-node-address", "0.0.0.0:9443",
								"--raft-ca-file", "/tls/ca.crt",
								"--raft-certificate-file", "/tls/tls.crt",
								"--raft-private-key-file", "/tls/tls.key",
							}, peerArgs...),
							VolumeMounts: append(peerVolumeMounts, []corev1.VolumeMount{{
								Name:      configuration.ServiceName + "-certificates",
								MountPath: "/tls",
							}}...),
						}},
						Volumes: append(peerVolumes, []corev1.Volume{{
							Name: configuration.ServiceName + "-certificates",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: configuration.ServiceName + "-certificates",
									Items: []corev1.KeyToPath{{
										Key:  "ca.crt",
										Path: "ca.crt",
									}, {
										Key:  "tls.crt",
										Path: "tls.crt",
									}, {
										Key:  "tls.key",
										Path: "tls.key",
									}},
								},
							},
						}}...),
					},
				},
			},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: metav1.NamespaceDefault,
				Name:      configuration.ServiceName,
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					"app": configuration.ServiceName,
				},
				Type: corev1.ServiceTypeLoadBalancer,
				Ports: []corev1.ServicePort{{
					Port:       9443,
					TargetPort: intstr.FromString("https"),
				}},
			},
		},
	}
}

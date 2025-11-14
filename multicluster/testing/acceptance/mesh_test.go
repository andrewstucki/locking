package acceptance

import (
	"fmt"
	"testing"

	mctesting "github.com/andrewstucki/locking/multicluster/testing"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestSetupClusters(t *testing.T) {
	image := HasImageOrBuild(t, "local/operator:dev", "../../..", "operator/Dockerfile", true)

	clusters := SetupClusters(t, []string{"a", "b", "c"}, false)
	clusters.ImportImages(t, image)

	peers := operatorPeerAddresses(clusters)
	ca := mctesting.GenerateCA(t)

	configs := []*corev1.Secret{}
	for _, cluster := range clusters {
		configs = append(configs, cluster.ServiceAccountKubeconfig(t, "operator", metav1.NamespaceDefault))
		cluster.Create(t, &rbacv1.ClusterRole{
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
		})
	}

	for _, cluster := range clusters {
		for _, config := range configs {
			cluster.Create(t, config.DeepCopy())
		}
		cluster.Create(t, operatorDeploymentForCluster(t, ca, peers, cluster, image)...)
		cluster.Create(t, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "acceptance-test",
				Namespace: metav1.NamespaceDefault,
				Annotations: map[string]string{
					"acceptance.testing/reconcile": "true",
				},
			},
		})
	}

	for _, cluster := range clusters {
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

type peerAddress struct {
	name    string
	address string
}

func operatorPeerAddresses(clusters ConnectedClusters) []peerAddress {
	name := "operator"

	peers := []peerAddress{}
	for _, cluster := range clusters {
		operatorName := name + "-" + cluster.Name
		operatorFQDN := cluster.RemoteName(name)
		peers = append(peers, peerAddress{
			name:    operatorName,
			address: fmt.Sprintf("%s:9443", operatorFQDN),
		})
	}

	return peers
}

func operatorDeploymentForCluster(t *testing.T, ca mctesting.CACertificate, peers []peerAddress, cluster *ConnectedCluster, image string) []client.Object {
	name := "operator"

	peerVolumes := []corev1.Volume{}
	peerVolumeMounts := []corev1.VolumeMount{}
	peerArgs := []string{}
	for _, peer := range peers {
		peerVolumes = append(peerVolumes, corev1.Volume{
			Name: peer.name + "-kubeconfig",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: peer.name + "-kubeconfig",
					Items: []corev1.KeyToPath{{
						Key:  "kubeconfig.yaml",
						Path: "kubeconfig.yaml",
					}},
				},
			},
		})
		peerVolumeMounts = append(peerVolumeMounts, corev1.VolumeMount{
			Name:      peer.name + "-kubeconfig",
			MountPath: "/config-" + peer.name,
		})
		peerArgs = append(peerArgs, "--raft-peers", peer.name+"://"+peer.address+"/config-"+peer.name+"/kubeconfig.yaml")
	}

	certificate := ca.Sign(t, cluster.DNSNames(name, metav1.NamespaceDefault)...)
	return []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name + "-certificates",
				Namespace: metav1.NamespaceDefault,
			},
			Data: map[string][]byte{
				"ca.crt":  ca.Bytes(),
				"tls.crt": certificate.Bytes(),
				"tls.key": certificate.PrivateKeyBytes(),
			},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: metav1.NamespaceDefault,
			},
			Spec: appsv1.DeploymentSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": name,
					},
				},
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"app": name,
						},
						Annotations: map[string]string{
							"linkerd.io/inject": "enabled",
						},
					},
					Spec: corev1.PodSpec{
						ServiceAccountName: "operator",
						Containers: []corev1.Container{{
							Name:  name,
							Image: image,
							Ports: []corev1.ContainerPort{{
								Name:          "https",
								ContainerPort: 9443,
							}},
							Args: append([]string{
								"--raft-node-name", name + "-" + cluster.Name,
								"--raft-node-address", "0.0.0.0:9443",
								"--raft-ca-file", "/tls/ca.crt",
								"--raft-certificate-file", "/tls/tls.crt",
								"--raft-private-key-file", "/tls/tls.key",
							}, peerArgs...),
							VolumeMounts: append(peerVolumeMounts, []corev1.VolumeMount{{
								Name:      name + "-certificates",
								MountPath: "/tls",
							}}...),
						}},
						Volumes: append(peerVolumes, []corev1.Volume{{
							Name: name + "-certificates",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: name + "-certificates",
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
				Name:      name,
				Labels: map[string]string{
					"mirror.linkerd.io/exported": "true",
				},
			},
			Spec: corev1.ServiceSpec{
				Selector: map[string]string{
					"app": name,
				},
				Type: corev1.ServiceTypeClusterIP,
				Ports: []corev1.ServicePort{{
					Port:       9443,
					TargetPort: intstr.FromString("https"),
				}},
			},
		},
	}
}

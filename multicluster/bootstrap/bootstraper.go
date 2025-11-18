package bootstrap

import (
	"context"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RemoteConfiguration struct {
	ContextName    string
	APIServer      string
	ServiceAddress string
}

func (r RemoteConfiguration) Client() (client.Client, error) {
	config, err := configFromContext(r.ContextName)
	if err != nil {
		return nil, err
	}

	return client.New(config, client.Options{})
}

func (r RemoteConfiguration) Config() (*rest.Config, error) {
	return configFromContext(r.ContextName)
}

func (r RemoteConfiguration) Address() (string, error) {
	if r.APIServer != "" {
		return r.APIServer, nil
	}
	config, err := r.Config()
	if err != nil {
		return "", err
	}

	return config.Host, nil
}

func (r RemoteConfiguration) FQDN(c BootstrapClusterConfiguration) (string, error) {
	if r.ServiceAddress != "" {
		return r.ServiceAddress, nil
	}

	return c.ServiceName + "-" + r.ContextName, nil
}

type BootstrapClusterConfiguration struct {
	OperatorNamespace string
	ServiceName       string
	RemoteClusters    []RemoteConfiguration
}

func BootstrapKubernetesClusters(ctx context.Context, organization string, configuration BootstrapClusterConfiguration) error {
	caCertificate, err := GenerateCA(organization, "Root CA", nil)
	if err != nil {
		return err
	}

	kubeconfigs := [][]byte{}
	certificates := []*Certificate{}
	for _, cluster := range configuration.RemoteClusters {
		address, err := cluster.Address()
		if err != nil {
			return err
		}
		serviceFQDN, err := cluster.FQDN(configuration)
		if err != nil {
			return err
		}
		certificate, err := caCertificate.Sign(serviceFQDN)
		if err != nil {
			return err
		}
		config, err := CreateRemoteKubeconfig(ctx, &RemoteKubernetesConfiguration{
			ContextName: cluster.ContextName,
			Namespace:   configuration.OperatorNamespace,
			Name:        configuration.ServiceName,
			APIServer:   address,
		})
		if err != nil {
			return err
		}
		certificates = append(certificates, certificate)
		kubeconfigs = append(kubeconfigs, config)
	}

	for i, cluster := range configuration.RemoteClusters {
		certificate := certificates[i]
		for i := range certificates {
			kubeconfig := kubeconfigs[i]

			if err := CreateKubeconfigSecret(ctx, kubeconfig, &RemoteKubernetesConfiguration{
				ContextName: cluster.ContextName,
				Namespace:   configuration.OperatorNamespace,
				Name:        configuration.ServiceName + "-" + configuration.RemoteClusters[i].ContextName,
			}); err != nil {
				return err
			}
		}

		if err := CreateTLSSecret(ctx, caCertificate, certificate, &RemoteKubernetesConfiguration{
			ContextName: cluster.ContextName,
			Namespace:   configuration.OperatorNamespace,
			Name:        configuration.ServiceName,
		}); err != nil {
			return err
		}
	}

	return nil
}

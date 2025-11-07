package kube

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	coordinationv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

type singleLock struct {
	*resourcelock.LeaseLock
}

// newSingleResourceLock creates a new resource lock for use in a leader election loop.
func newSingleResourceLock(id string, config LockConfiguration, o *options) (resourcelock.Interface, error) {
	k8sConfig := rest.AddUserAgent(config.Config, "leader-election")

	if o.renewDeadline != 0 {
		timeout := o.renewDeadline / 2
		if timeout < time.Second {
			timeout = time.Second
		}
		k8sConfig.Timeout = timeout
	}

	coordinationClient, err := coordinationv1client.NewForConfig(k8sConfig)
	if err != nil {
		return nil, err
	}

	lockConfig := resourcelock.ResourceLockConfig{
		Identity: id,
	}

	if o.recorderProvider != nil {
		lockConfig.EventRecorder = o.recorderProvider.GetEventRecorderFor(id) //nolint:staticcheck
	}

	return &singleLock{LeaseLock: &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Namespace: config.Namespace,
			Name:      config.Name,
		},
		Client:     coordinationClient,
		LockConfig: lockConfig,
		Labels:     o.leaderLabels,
	}}, nil
}

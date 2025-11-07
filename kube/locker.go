package kube

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"sigs.k8s.io/controller-runtime/pkg/recorder"
)

type LockConfiguration struct {
	ID        string
	Name      string
	Namespace string
	Config    *rest.Config
}

func (lc *LockConfiguration) Validate() error {
	if lc.Name == "" {
		return fmt.Errorf("lock name must be specified")
	}
	if lc.Namespace == "" {
		return fmt.Errorf("lock namespace must be specified")
	}
	if lc.Config == nil {
		return fmt.Errorf("kube config must be specified")
	}
	return nil
}

type LockOptions func(o *options)

type options struct {
	leaderLabels     map[string]string
	renewDeadline    time.Duration
	leaseDuration    time.Duration
	retryPeriod      time.Duration
	releaseOnCancel  bool
	recorderProvider recorder.Provider
}

func WithLeaderLabels(labels map[string]string) LockOptions {
	return func(o *options) {
		o.leaderLabels = labels
	}
}

func WithRenewDeadline(d time.Duration) LockOptions {
	return func(o *options) {
		o.renewDeadline = d
	}
}

func WithLeaseDuration(d time.Duration) LockOptions {
	return func(o *options) {
		o.leaseDuration = d
	}
}

func WithRetryPeriod(d time.Duration) LockOptions {
	return func(o *options) {
		o.retryPeriod = d
	}
}

func WithReleaseOnCancel(release bool) LockOptions {
	return func(o *options) {
		o.releaseOnCancel = release
	}
}

func WithRecorderProvider(provider recorder.Provider) LockOptions {
	return func(o *options) {
		o.recorderProvider = provider
	}
}

func defaultOptions() *options {
	return &options{
		renewDeadline:   15 * time.Second,
		leaseDuration:   30 * time.Second,
		retryPeriod:     5 * time.Second,
		releaseOnCancel: true,
	}
}

type LeaderCallbacks struct {
	OnStartedLeading func(ctx context.Context)
	OnStoppedLeading func()

	initialize atomic.Bool
	isLeader   atomic.Bool
}

func RunSingle(ctx context.Context, config LockConfiguration, callbacks *LeaderCallbacks, opts ...LockOptions) error {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	id := config.ID
	if id == "" {
		id, err := os.Hostname()
		if err != nil {
			return err
		}
		id = id + "_" + string(uuid.NewUUID())
	}

	lock, err := newSingleResourceLock(id, config, o)
	if err != nil {
		return fmt.Errorf("could not create resource lock: %w", err)
	}

	elector, err := leaderElector(config.Name, lock, callbacks, o)
	if err != nil {
		return fmt.Errorf("could not create leader elector: %w", err)
	}

	elector.Run(ctx)
	return nil
}

func RunMulti(ctx context.Context, configs []LockConfiguration, callbacks *LeaderCallbacks, opts ...LockOptions) error {
	o := defaultOptions()
	for _, opt := range opts {
		opt(o)
	}

	if len(configs) < 2 {
		return fmt.Errorf("at least two configurations are required to create a multi resource lock")
	}

	id := configs[0].ID
	if id == "" {
		id, err := os.Hostname()
		if err != nil {
			return err
		}
		id = id + "_" + string(uuid.NewUUID())
	}

	lock, err := newMultiResourceLock(id, configs, o)
	if err != nil {
		return fmt.Errorf("could not create resource lock: %w", err)
	}

	elector, err := leaderElector(configs[0].Name, lock, callbacks, o)
	if err != nil {
		return fmt.Errorf("could not create leader elector: %w", err)
	}

	elector.Run(ctx)
	return nil
}

func leaderElector(name string, lock resourcelock.Interface, callbacks *LeaderCallbacks, o *options) (*leaderelection.LeaderElector, error) {
	leaderElector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock:          lock,
		LeaseDuration: o.leaseDuration,
		RenewDeadline: o.renewDeadline,
		RetryPeriod:   o.retryPeriod,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				if callbacks != nil {
					callbacks.initialize.Store(true)
					if !callbacks.isLeader.Swap(true) && callbacks.OnStartedLeading != nil {
						callbacks.OnStartedLeading(ctx)
					}
				}
			},
			OnStoppedLeading: func() {
				if callbacks != nil {
					if (!callbacks.initialize.Swap(true) || callbacks.isLeader.Swap(false)) && callbacks.OnStoppedLeading != nil {
						callbacks.OnStoppedLeading()
					}
				}
			},
			OnNewLeader: func(_ string) {
				if callbacks != nil {
					if (!callbacks.initialize.Swap(true) || callbacks.isLeader.Swap(false)) && callbacks.OnStoppedLeading != nil {
						callbacks.OnStoppedLeading()
					}
				}
			},
		},
		ReleaseOnCancel: o.releaseOnCancel,
		Name:            name,
	})

	if err != nil {
		return nil, fmt.Errorf("failed to initialize leader elector: %w", err)
	}

	return leaderElector, nil
}

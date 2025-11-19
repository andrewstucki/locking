package multicluster

import (
	"context"
	"sort"

	"github.com/andrewstucki/locking"
	"github.com/go-logr/logr"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

type Manager interface {
	mcmanager.Manager
	GetClusterNames() []string
}

type managerI struct {
	mcmanager.Manager
	runnable    *leaderRunnable
	logger      logr.Logger
	getClusters func() map[string]cluster.Cluster
}

func (m *managerI) GetClusterNames() []string {
	clusters := []string{mcmanager.LocalCluster}
	if m.getClusters == nil {
		return clusters
	}

	for cluster := range m.getClusters() {
		clusters = append(clusters, cluster)
	}
	sort.Strings(clusters)
	return clusters
}

func newManager(logger logr.Logger, config *rest.Config, provider multicluster.Provider, restart chan struct{}, getClusters func() map[string]cluster.Cluster, manager *locking.LeaderManager, opts manager.Options) (Manager, error) {
	mgr, err := mcmanager.New(config, provider, opts)
	if err != nil {
		return nil, err
	}

	manager.RegisterRoutine(func(ctx context.Context) error {
		logger.Info("got leader")
		<-ctx.Done()
		logger.Info("lost leader")
		return nil
	})

	runnable := &leaderRunnable{manager: manager, logger: logger, restart: restart, getClusters: getClusters}
	if err := mgr.Add(runnable); err != nil {
		return nil, err
	}
	return &managerI{Manager: mgr, runnable: runnable, logger: logger}, nil
}

func (m *managerI) Add(r mcmanager.Runnable) error {
	if _, ok := r.(reconcile.TypedReconciler[mcreconcile.Request]); ok {
		m.logger.Info("adding multicluster reconciler")
		m.runnable.Add(r)
		return nil
	}

	return m.Manager.Add(r)
}

type warmupRunnable interface {
	Warmup(context.Context) error
}

type leaderRunnable struct {
	runnables   []mcmanager.Runnable
	manager     *locking.LeaderManager
	logger      logr.Logger
	restart     chan struct{}
	getClusters func() map[string]cluster.Cluster
}

func (l *leaderRunnable) Add(r mcmanager.Runnable) {
	doEngage := func() {
		for name, cluster := range l.getClusters() {
			// engage any static clusters
			_ = r.Engage(context.Background(), name, cluster)
		}
	}

	l.runnables = append(l.runnables, r)
	if warmup, ok := r.(warmupRunnable); ok {
		// start caches and sources
		l.manager.RegisterRoutine(l.wrapStart(doEngage, warmup.Warmup))
	}
	l.manager.RegisterRoutine(l.wrapStart(doEngage, r.Start))
}

func (l *leaderRunnable) Engage(ctx context.Context, s string, c cluster.Cluster) error {
	for _, runnable := range l.runnables {
		if err := runnable.Engage(ctx, s, c); err != nil {
			l.logger.Info("engaging runnable")
			return err
		}
	}
	return nil
}

func (l *leaderRunnable) Start(ctx context.Context) error {
	return l.manager.Run(ctx)
}

func (l *leaderRunnable) wrapStart(doEngage func(), fn func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		cancelCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		doEngage()

		go func() {
			for {
				select {
				case <-cancelCtx.Done():
					return
				case <-l.restart:
					// re-engage
					doEngage()
				}
			}
		}()

		return fn(cancelCtx)
	}
}

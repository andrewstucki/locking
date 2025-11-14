package multicluster

import (
	"context"

	"github.com/andrewstucki/locking"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

type managerI struct {
	mcmanager.Manager
	runnable *leaderRunnable
}

func newManager(config *rest.Config, provider multicluster.Provider, manager *locking.LeaderManager, opts manager.Options) (mcmanager.Manager, error) {
	mgr, err := mcmanager.New(config, provider, opts)
	if err != nil {
		return nil, err
	}

	runnable := &leaderRunnable{manager: manager}
	if err := mgr.Add(runnable); err != nil {
		return nil, err
	}
	return &managerI{Manager: mgr, runnable: runnable}, nil
}

func (m *managerI) Add(r mcmanager.Runnable) error {
	if runnable, ok := r.(manager.LeaderElectionRunnable); ok && runnable.NeedLeaderElection() {
		m.runnable.Add(r)
		return nil
	}

	return m.Manager.Add(r)
}

type leaderRunnable struct {
	manager *locking.LeaderManager
}

func (l *leaderRunnable) Add(r mcmanager.Runnable)                              { l.manager.RegisterRoutine(r.Start) }
func (l *leaderRunnable) Engage(context.Context, string, cluster.Cluster) error { return nil }
func (l *leaderRunnable) Start(ctx context.Context) error {
	return l.manager.Run(ctx)
}

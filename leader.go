package locking

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/andrewstucki/locking/kube"
	"github.com/andrewstucki/locking/raft"
)

type runFactory func(ctx context.Context) error

type LeaderManager struct {
	leaderRoutines []func(ctx context.Context)

	mutex sync.RWMutex

	isLeader atomic.Bool
	runner   runFactory
}

func NewKubernetesLockManager(configuration kube.LockConfiguration) *LeaderManager {
	manager := &LeaderManager{}
	manager.runner = func(ctx context.Context) error {
		return kube.Run(ctx, configuration, &kube.LeaderCallbacks{
			OnStartedLeading: manager.runLeaderRoutines,
			OnStoppedLeading: func() {
				manager.isLeader.Store(false)
			},
		})
	}

	return manager
}

func NewRaftLockManager(configuration raft.LockConfiguration) *LeaderManager {
	manager := &LeaderManager{}
	manager.runner = func(ctx context.Context) error {
		return raft.Run(ctx, configuration, &raft.LeaderCallbacks{
			OnStartedLeading: manager.runLeaderRoutines,
			OnStoppedLeading: func() {
				manager.isLeader.Store(false)
			},
		})
	}

	return manager
}

func (lm *LeaderManager) runLeaderRoutines(ctx context.Context) {
	lm.isLeader.Store(true)

	lm.mutex.RLock()
	defer lm.mutex.RUnlock()

	for _, fn := range lm.leaderRoutines {
		go fn(ctx)
	}
}

func (lm *LeaderManager) RegisterRoutine(fn func(ctx context.Context)) {
	lm.mutex.Lock()
	defer lm.mutex.Unlock()

	lm.leaderRoutines = append(lm.leaderRoutines, fn)
}

func (lm *LeaderManager) IsLeader() bool {
	return lm.isLeader.Load()
}

func (lm *LeaderManager) Run(ctx context.Context) error {
	return lm.runner(ctx)
}

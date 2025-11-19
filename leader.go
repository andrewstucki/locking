package locking

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andrewstucki/locking/kube"
	"github.com/andrewstucki/locking/raft"
	"github.com/go-logr/logr"
)

type runFactory func(ctx context.Context) error

type LeaderManager struct {
	leaderRoutines []func(ctx context.Context) error

	logger logr.Logger

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
		go func() {
			for {
				err := fn(ctx)
				select {
				case <-ctx.Done():
					return
				default:
					if err != nil {
						lm.logger.Error(err, "error encountered on leader routine, restarting in 10 seconds")
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(10 * time.Second):
					}
				}
			}
		}()
	}
}

func (lm *LeaderManager) RegisterRoutine(fn func(ctx context.Context) error) {
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

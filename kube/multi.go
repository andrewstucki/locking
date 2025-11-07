package kube

import (
	"context"
	"encoding/json"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
)

type multiLock struct {
	primary     resourcelock.Interface
	secondaries []resourcelock.Interface
}

func (ml *multiLock) Get(ctx context.Context) (*resourcelock.LeaderElectionRecord, []byte, error) {
	primary, record, err := ml.primary.Get(ctx)
	if err != nil {
		return nil, nil, err
	}

	for _, ml := range ml.secondaries {
		secondary, secondaryRaw, err := ml.Get(ctx)
		if err != nil {
			// Lock is held by old client
			if apierrors.IsNotFound(err) && primary.HolderIdentity != ml.Identity() {
				return primary, record, nil
			}
			return nil, nil, err
		}

		if primary.HolderIdentity != secondary.HolderIdentity {
			primary.HolderIdentity = resourcelock.UnknownLeader
			record, err = json.Marshal(primary)
			if err != nil {
				return nil, nil, err
			}
		}

		record = resourcelock.ConcatRawRecord(record, secondaryRaw)
	}

	return primary, record, nil
}

func (ml *multiLock) Create(ctx context.Context, ler resourcelock.LeaderElectionRecord) error {
	err := ml.primary.Create(ctx, ler)
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	for _, ml := range ml.secondaries {
		err := ml.Create(ctx, ler)
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}

func (ml *multiLock) Update(ctx context.Context, ler resourcelock.LeaderElectionRecord) error {
	err := ml.primary.Update(ctx, ler)
	if err != nil {
		return err
	}

	for _, ml := range ml.secondaries {
		_, _, err = ml.Get(ctx)
		if err != nil && apierrors.IsNotFound(err) {
			return ml.Create(ctx, ler)
		}

		if err := ml.Update(ctx, ler); err != nil {
			return err
		}
	}

	return nil
}

func (ml *multiLock) RecordEvent(s string) {
	ml.primary.RecordEvent(s)
	for _, ml := range ml.secondaries {
		ml.RecordEvent(s)
	}
}

func (ml *multiLock) Describe() string {
	return ml.primary.Describe()
}

func (ml *multiLock) Identity() string {
	return ml.primary.Identity()
}

func newMultiResourceLock(id string, configs []LockConfiguration, o *options) (resourcelock.Interface, error) {
	if len(configs) < 2 {
		return nil, fmt.Errorf("at least two configurations are required to create a multi resource lock")
	}

	name := configs[0].Name
	for _, config := range configs[1:] {
		if config.Name != name {
			return nil, fmt.Errorf("all locks in a multi resource lock must have the same name")
		}
	}

	secondaryLocks := make([]resourcelock.Interface, 0, len(configs)-1)
	for _, config := range configs[1:] {
		secondaryLock, err := newSingleResourceLock(id, config, o)
		if err != nil {
			return nil, err
		}
		secondaryLocks = append(secondaryLocks, secondaryLock)
	}

	primaryLock, err := newSingleResourceLock(id, configs[0], o)
	if err != nil {
		return nil, err
	}

	return &multiLock{
		primary:     primaryLock,
		secondaries: secondaryLocks,
	}, nil
}

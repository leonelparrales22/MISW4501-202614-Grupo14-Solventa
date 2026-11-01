package triage_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// The sweep that gives every claim in evaluation its level within the 60
// seconds of criterion 1 (HU-W2.2-01, decision D3).

func (s *fakeStore) assessed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.records[id]) > 0
}

func TestTheWorkerSweepsRightAwayAndAtEveryInterval(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", amount(4_800_000)))
	svc, _ := newService(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		triage.NewWorker(svc, 20*time.Millisecond, 10).Run(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool { return store.assessed("cl-1") }, time.Second, 5*time.Millisecond,
		"the first sweep does not wait for the interval")

	// A claim filed after the first sweep is assessed in the next one.
	store.set(waterDamage("cl-2", nil))
	require.Eventually(t, func() bool { return store.assessed("cl-2") }, time.Second, 5*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the worker did not stop with its context")
	}
}

func TestTheWorkerKeepsSweepingAfterAFailure(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", nil))
	store.pendingErr = errors.New("the database is down")
	svc, _ := newService(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go triage.NewWorker(svc, 10*time.Millisecond, 10).Run(ctx)

	time.Sleep(30 * time.Millisecond)
	assert.False(t, store.assessed("cl-1"))

	store.mu.Lock()
	store.pendingErr = nil
	store.mu.Unlock()
	require.Eventually(t, func() bool { return store.assessed("cl-1") }, time.Second, 5*time.Millisecond)
}

func TestTheWorkerTakesItsDefaults(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", nil))
	svc, _ := newService(store)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		triage.NewWorker(svc, 0, 0).Run(ctx)
		close(done)
	}()

	require.Eventually(t, func() bool { return store.assessed("cl-1") }, time.Second, 5*time.Millisecond)
	cancel()
	<-done
	assert.Equal(t, 10*time.Second, triage.DefaultInterval)
	assert.Equal(t, 200, triage.DefaultBatch)
}

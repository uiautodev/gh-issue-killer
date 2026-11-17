package main

import (
	"context"
	"testing"
	"time"
)

// The CI queue exists so a slow pull-request gate never occupies the
// implementation slot: with a single work worker busy, a CI job must still run.
func TestCIQueueDoesNotBlockOnWork(t *testing.T) {
	router := NewRouter(
		&Config{PlanParallel: 1, ImplementParallel: 1},
		map[string]*AppContext{"o/r": {}},
		"bot", nil, NewSilentLogger(),
	)

	workStarted := make(chan struct{})
	releaseWork := make(chan struct{})
	router.enqueue("o/r", "o/r#1", stageWork, func(context.Context) {
		close(workStarted)
		<-releaseWork
	})
	select {
	case <-workStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("work job did not start")
	}

	ciDone := make(chan struct{})
	router.enqueue("o/r", "o/r#1#ci", stageCI, func(context.Context) {
		close(ciDone)
	})
	select {
	case <-ciDone:
	case <-time.After(2 * time.Second):
		close(releaseWork)
		t.Fatal("ci job did not run while the only work worker was busy")
	}
	close(releaseWork)
}

// cancelActive must stop the CI job too, otherwise closing an issue races the
// worktree cleanup against a running repair agent.
func TestCancelActiveStopsCIJob(t *testing.T) {
	router := NewRouter(
		&Config{PlanParallel: 1, ImplementParallel: 1},
		map[string]*AppContext{"o/r": {}},
		"bot", nil, NewSilentLogger(),
	)

	started := make(chan struct{})
	stopped := make(chan struct{})
	router.enqueue("o/r", "o/r#2#ci", stageCI, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(stopped)
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("ci job did not start")
	}

	router.cancelActive("o/r", 2)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelActive did not cancel the ci job")
	}
	router.waitCISettle(context.Background(), "o/r", 2)
}

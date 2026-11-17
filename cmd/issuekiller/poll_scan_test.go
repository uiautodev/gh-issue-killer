package main

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
)

// The reaction, PR-feedback and CI-retry scans must start from the tracked
// state: listing every open issue on each 30 s cycle exhausted the token's
// hourly quota, and a rate-limited daemon cannot see a 👍 at all.
func TestPeriodicScansDoNotListOpenIssues(t *testing.T) {
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	app := &AppContext{
		Repo:  RepoConfig{FullName: "acme/app", Approvers: []string{"owner"}},
		GH:    gh,
		Store: store,
		Log:   NewSilentLogger(),
	}
	router := NewRouter(&Config{PlanParallel: 1, ImplementParallel: 1},
		map[string]*AppContext{"acme/app": app}, "bot", store, NewSilentLogger())

	router.checkReactions(context.Background(), app)
	router.checkPRFeedback(context.Background(), app)
	router.retryBlockedCI(context.Background(), app)

	if got := len(calls.snapshot()); got != 0 {
		t.Fatalf("scans made %d API calls without tracked issues: %+v", got, calls.snapshot())
	}
}

// A tracked issue with nothing pending must not cost a request either.
func TestCheckReactionsSkipsIssuesWithoutTargets(t *testing.T) {
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.UpdateIssue("acme/app", 3, func(st *IssueState) { st.Phase = "done" })
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	app := &AppContext{
		Repo:  RepoConfig{FullName: "acme/app", Approvers: []string{"owner"}},
		GH:    gh,
		Store: store,
		Log:   NewSilentLogger(),
	}
	router := NewRouter(&Config{PlanParallel: 1, ImplementParallel: 1},
		map[string]*AppContext{"acme/app": app}, "bot", store, NewSilentLogger())

	router.checkReactions(context.Background(), app)
	if got := len(calls.snapshot()); got != 0 {
		t.Fatalf("checkReactions made %d API calls for a done issue: %+v", got, calls.snapshot())
	}
}

func TestIssueNumbers(t *testing.T) {
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	for _, number := range []int{9, 3, 172} {
		store.UpdateIssue("acme/app", number, func(st *IssueState) { st.Phase = "new" })
	}
	numbers := store.IssueNumbers("acme/app")
	want := []int{3, 9, 172}
	if len(numbers) != len(want) {
		t.Fatalf("IssueNumbers = %v, want %v", numbers, want)
	}
	for i := range want {
		if numbers[i] != want[i] {
			t.Fatalf("IssueNumbers = %v, want %v", numbers, want)
		}
	}
	if got := store.IssueNumbers("other/repo"); got != nil {
		t.Fatalf("IssueNumbers(unknown) = %v, want nil", got)
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// Accepting a 👍 must move the task out of the waiting phase immediately: the
// implementation usually queues behind another job, and until it starts the
// dashboard would otherwise keep showing "waiting for confirmation" while the
// label already says working.
func TestApproveAdvancesPhaseImmediately(t *testing.T) {
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.UpdateIssue("acme/app", 5, func(st *IssueState) {
		st.Phase = "awaiting_approval"
		st.PlanCommentID = 77
	})
	gh, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/acme/app/issues/5") {
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "closed"})
			return
		}
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

	router.enqueueApprove(context.Background(), app, "acme/app", 5, "owner")
	if got := store.Issue("acme/app", 5).Phase; got != "implementing" {
		t.Fatalf("phase = %q, want implementing right after the approval", got)
	}
}

func TestMergeAdvancesPhaseImmediately(t *testing.T) {
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.UpdateIssue("acme/app", 6, func(st *IssueState) {
		st.Phase = "pr_open"
		st.PRNumber = 60
	})
	gh, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/repos/acme/app/issues/6") {
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "closed"})
			return
		}
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

	router.enqueueMerge(app, "acme/app", 6, "owner")
	if got := store.Issue("acme/app", 6).Phase; got != "merging" {
		t.Fatalf("phase = %q, want merging right after the approval", got)
	}
}

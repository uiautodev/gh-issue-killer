package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// The reaction webhook is not always delivered, so the poller must apply 👍 on
// the review prompt even though a reviewed PR has no task phase. The prompt is
// consumed once: the 👍 stays on the comment and must not restart the fix on
// every scan.
func TestCheckReactionsAppliesReviewThumbOnce(t *testing.T) {
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.UpdateIssue("acme/app", 5, func(st *IssueState) {
		st.Phase = ""
		st.ReviewCommentID = 77
	})

	gh, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/app/issues":
			// pull_request marks it as a PR: the reaction scan must not filter it.
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 5, "state": "open", "pull_request": map[string]any{}}})
		case strings.HasSuffix(r.URL.Path, "/issues/comments/77/reactions"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"content": "+1", "user": map[string]string{"login": "owner"}},
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	app := &AppContext{
		Cfg:   &Config{PlanParallel: 1, ImplementParallel: 1},
		Repo:  RepoConfig{FullName: "acme/app", Approvers: []string{"owner"}},
		GH:    gh,
		Store: store,
		Log:   NewSilentLogger(),
	}
	router := NewRouter(&Config{PlanParallel: 1, ImplementParallel: 1},
		map[string]*AppContext{"acme/app": app}, "bot", store, NewSilentLogger())

	router.checkReactions(context.Background(), app)
	if got := store.Issue("acme/app", 5).ReviewCommentID; got != 0 {
		t.Fatalf("review prompt = %d, want it consumed", got)
	}

	// A second scan finds the same 👍 but nothing left to consume.
	router.checkReactions(context.Background(), app)
	if got := store.Issue("acme/app", 5).ReviewCommentID; got != 0 {
		t.Fatalf("review prompt came back: %d", got)
	}
}

// A non-approver thumbs-up must not start the fix.
func TestCheckReactionsIgnoresNonApprover(t *testing.T) {
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.UpdateIssue("acme/app", 5, func(st *IssueState) {
		st.ReviewCommentID = 77
	})
	gh, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/app/issues":
			// pull_request marks it as a PR: the reaction scan must not filter it.
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 5, "state": "open", "pull_request": map[string]any{}}})
		case strings.HasSuffix(r.URL.Path, "/issues/comments/77/reactions"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"content": "+1", "user": map[string]string{"login": "random"}},
			})
		default:
			w.WriteHeader(http.StatusOK)
		}
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
	if got := store.Issue("acme/app", 5).ReviewCommentID; got != 77 {
		t.Fatalf("review prompt = %d, want it kept for a real approver", got)
	}
}

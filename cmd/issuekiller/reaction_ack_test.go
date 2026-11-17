package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func newStatusCommentStore(t *testing.T, number int, phase string, statusID int64) *Store {
	t.Helper()
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.UpdateIssue("acme/app", number, func(st *IssueState) {
		st.Phase = phase
		st.StatusCommentID = statusID
	})
	return store
}

func TestPostStatusCommentRemembersID(t *testing.T) {
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.UpdateIssue("acme/app", 5, func(st *IssueState) { st.Phase = "blocked" })
	gh, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/5/comments") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 4242})
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	app := &AppContext{Repo: RepoConfig{FullName: "acme/app"}, GH: gh, Store: store, Log: NewSilentLogger()}
	postStatusComment(context.Background(), app, 5, "blocked")
	if got := store.Issue("acme/app", 5).StatusCommentID; got != 4242 {
		t.Fatalf("StatusCommentID = %d, want 4242", got)
	}
}

// A 👍 on the bot's "something went wrong" message must acknowledge with 👀 and
// retry the blocked task.
func TestReactionOnStatusCommentRetries(t *testing.T) {
	store := newStatusCommentStore(t, 5, "blocked", 42)
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/comments/42/reactions"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"content": "+1", "user": map[string]string{"login": "owner"}},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/5"):
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "closed"})
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

	router.HandleReaction(context.Background(), "acme/app", 5, 42, "owner", "+1")

	state := store.Issue("acme/app", 5)
	if state.Phase != "implementing" {
		t.Fatalf("phase = %q, want implementing (retry queued)", state.Phase)
	}
	if state.StatusCommentID != 0 {
		t.Fatalf("StatusCommentID = %d, want consumed", state.StatusCommentID)
	}
	eyes := false
	for _, call := range calls.snapshot() {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/comments/42/reactions") &&
			strings.Contains(call.body, "eyes") {
			eyes = true
		}
	}
	if !eyes {
		t.Fatalf("no 👀 acknowledgment was posted: %+v", calls.snapshot())
	}
}

func TestReactionOnPlanAcknowledgesWithEyes(t *testing.T) {
	store := newStatusCommentStore(t, 6, "awaiting_approval", 0)
	store.UpdateIssue("acme/app", 6, func(st *IssueState) { st.PlanCommentID = 77 })
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/comments/77/reactions"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"content": "+1", "user": map[string]string{"login": "owner"}},
			})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/issues/6"):
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "closed"})
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

	router.HandleReaction(context.Background(), "acme/app", 6, 77, "owner", "+1")

	eyes := false
	for _, call := range calls.snapshot() {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/comments/77/reactions") &&
			strings.Contains(call.body, "eyes") {
			eyes = true
		}
	}
	if !eyes {
		t.Fatalf("plan approval was not acknowledged: %+v", calls.snapshot())
	}
}

// State written by an older build has no statusCommentId. The poller must still
// find the bot's last message, adopt the 👍 that is already on it and retry.
func TestCheckReactionsAdoptsUntrackedStatusComment(t *testing.T) {
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.UpdateIssue("acme/app", 5, func(st *IssueState) {
		st.Phase = "blocked"
		st.BlockedReason = "revision failed: coding agent timed out"
	})
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/app/issues":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 5, "state": "open"}})
		case strings.HasSuffix(r.URL.Path, "/issues/5/comments"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 900, "body": "### 🤖 Something went wrong\n\n<!-- ghik:agent -->"},
			})
		case strings.Contains(r.URL.Path, "/issues/comments/900/reactions"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"content": "+1", "user": map[string]string{"login": "owner"}},
			})
		case strings.Contains(r.URL.Path, "/issues/comments/") && strings.HasSuffix(r.URL.Path, "/reactions"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case strings.Contains(r.URL.Path, "/issues/5"):
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "closed"})
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

	state := store.Issue("acme/app", 5)
	if state.Phase != "implementing" {
		t.Fatalf("phase = %q, want implementing (retry queued)", state.Phase)
	}
	if state.StatusCommentID != 0 {
		t.Fatalf("StatusCommentID = %d, want consumed", state.StatusCommentID)
	}
	eyes := false
	for _, call := range calls.snapshot() {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/comments/900/reactions") &&
			strings.Contains(call.body, "eyes") {
			eyes = true
		}
	}
	if !eyes {
		t.Fatalf("no 👀 acknowledgment was posted: %+v", calls.snapshot())
	}
}

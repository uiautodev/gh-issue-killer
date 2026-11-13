package main

import (
	"testing"
)

func TestWebSnapshotOnlyListsTrackedTasks(t *testing.T) {
	old := stateDirOverride
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = old })

	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateIssue("o/r", 1, func(state *IssueState) {
		state.Phase = "planning"
		state.Issue = &IssueMeta{Title: "tracked", URL: "https://example.com/1"}
	})
	store.UpdateIssue("o/r", 2, func(state *IssueState) {
		// A comment watermark with no phase must not show up as a task.
		state.LastCommentID = 5
	})

	cfg := &Config{File: "config.json", PlanParallel: 5, ImplementParallel: 1, Repos: []RepoConfig{{FullName: "o/r", CodingAgent: "opencode"}}}
	router := NewRouter(&Config{PlanParallel: 1, ImplementParallel: 1}, map[string]*AppContext{"o/r": {}}, "bot", store, NewSilentLogger())
	web := &webServer{cfg: cfg, router: router, store: store, log: NewSilentLogger()}

	snapshot := web.snapshot()
	if len(snapshot.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snapshot.Tasks))
	}
	task := snapshot.Tasks[0]
	if task.Number != 1 || task.Title != "tracked" || task.PhaseLabel != "规划中" {
		t.Fatalf("task = %+v", task)
	}
	if len(snapshot.Repos) != 1 || snapshot.Repos[0].Issues != 1 {
		t.Fatalf("repo counts = %+v", snapshot.Repos)
	}
}

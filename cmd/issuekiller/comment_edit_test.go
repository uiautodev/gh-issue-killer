package main

import (
	"path/filepath"
	"testing"
)

// An edited comment that adds the mention must run again even though the
// original delivery was already processed; every other comment stays deduped.
func TestShouldSkipCommentReprocess(t *testing.T) {
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.MarkCommentProcessed("o/r", 7, 42)
	router := NewRouter(&Config{PlanParallel: 1, ImplementParallel: 1},
		map[string]*AppContext{"o/r": {}}, "bot", store, NewSilentLogger())

	if !router.shouldSkipComment("o/r", Trigger{IssueNumber: 7, CommentID: 42}) {
		t.Fatal("processed comment should be skipped")
	}
	if router.shouldSkipComment("o/r", Trigger{IssueNumber: 7, CommentID: 42, Reprocess: true}) {
		t.Fatal("an edited comment must not be skipped")
	}
	if router.shouldSkipComment("o/r", Trigger{IssueNumber: 7, CommentID: 43}) {
		t.Fatal("a new comment must not be skipped")
	}
}

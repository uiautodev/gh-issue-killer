package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	oldStateDir := stateDirOverride
	stateDirOverride = dir
	t.Cleanup(func() { stateDirOverride = oldStateDir })
	if err := ensureDirs(); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateIssue("me/app", 7, func(state *IssueState) {
		state.Phase = "awaiting_approval"
		state.Plan = "do it"
		state.PlanCommentID = 42
	})
	store.MarkCommentProcessed("me/app", 7, 99)

	reloaded, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	issue := reloaded.Issue("me/app", 7)
	if issue.Phase != "awaiting_approval" || issue.PlanCommentID != 42 {
		t.Fatalf("unexpected issue: %+v", issue)
	}
	if !reloaded.IsCommentProcessed("me/app", 7, 99) {
		t.Fatal("comment should be marked processed")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal("state file missing")
	}
}

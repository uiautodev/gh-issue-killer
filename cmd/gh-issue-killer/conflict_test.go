package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func boolPtr(value bool) *bool { return &value }

func TestMergeBlockedByConflicts(t *testing.T) {
	cases := []struct {
		name string
		pr   PullRequest
		want bool
	}{
		{"dirty", PullRequest{MergeableState: "dirty"}, true},
		{"mergeable", PullRequest{MergeableState: "clean", Mergeable: boolPtr(true)}, false},
		{"blocked review", PullRequest{MergeableState: "blocked", Mergeable: boolPtr(false)}, false},
		{"behind", PullRequest{MergeableState: "behind", Mergeable: boolPtr(false)}, false},
		{"unmergeable without state", PullRequest{Mergeable: boolPtr(false)}, true},
		{"unknown state", PullRequest{MergeableState: "unknown"}, false},
	}
	for _, tc := range cases {
		if got := mergeBlockedByConflicts(tc.pr); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsConflictError(t *testing.T) {
	if !isConflictError(&APIError{Status: http.StatusMethodNotAllowed, Message: "Pull Request has merge conflicts"}) {
		t.Fatal("405 conflict message not recognized")
	}
	if !isConflictError(&APIError{Status: http.StatusConflict}) {
		t.Fatal("409 not recognized")
	}
	if isConflictError(&APIError{Status: http.StatusMethodNotAllowed, Message: "not mergeable"}) {
		t.Fatal("unrelated 405 treated as a conflict")
	}
	if isConflictError(errors.New("boom")) {
		t.Fatal("plain error treated as a conflict")
	}
}

func TestConflictedFiles(t *testing.T) {
	dir := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	ctx := context.Background()
	git := func(args ...string) {
		t.Helper()
		if _, err := gitWorktree(ctx, dir, env, 30*time.Second, false, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	git("init", "-q", "-b", "main")
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("a.txt", "base\n")
	git("add", "a.txt")
	git("commit", "-q", "-m", "base")

	git("checkout", "-q", "-b", "feature")
	writeFile("a.txt", "feature\n")
	git("add", "a.txt")
	git("commit", "-q", "-m", "feature")

	git("checkout", "-q", "main")
	writeFile("a.txt", "main\n")
	git("add", "a.txt")
	git("commit", "-q", "-m", "main")

	if _, err := gitWorktree(ctx, dir, env, 30*time.Second, true, "merge", "--no-edit", "feature"); err != nil {
		t.Fatalf("merge: %v", err)
	}
	files := conflictedFiles(ctx, dir, env)
	if len(files) != 1 || files[0] != "a.txt" {
		t.Fatalf("conflicted files = %v, want [a.txt]", files)
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A pull request head may live in a fork under a different branch name, so the
// push has to take the local and remote refs separately.
func TestPushBranchToDifferentNames(t *testing.T) {
	ctx := context.Background()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	mustGit := func(dir string, args ...string) {
		t.Helper()
		if _, err := gitWorktree(ctx, dir, env, 30*time.Second, false, args...); err != nil {
			t.Fatalf("git %v in %s: %v", args, dir, err)
		}
	}
	mustGit(root, "init", "-q", "--bare", remote)
	mustGit(root, "init", "-q", "-b", "main", work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(work, "add", "a.txt")
	mustGit(work, "commit", "-q", "-m", "one")

	if err := pushBranchTo(ctx, work, remote, env, "main", "feature-x"); err != nil {
		t.Fatalf("push local main to remote feature-x: %v", err)
	}
	if got := gitOutput(ctx, remote, env, time.Minute, "rev-parse", "--verify", "refs/heads/feature-x"); got == "" {
		t.Fatal("remote branch feature-x was not created")
	}
	if got := gitOutput(ctx, remote, env, time.Minute, "rev-parse", "--verify", "refs/heads/main"); got != "" {
		t.Fatal("push must not create the local branch name on the remote")
	}
}

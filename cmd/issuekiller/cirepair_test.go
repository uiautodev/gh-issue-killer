package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A repair agent that commits on its own leaves the worktree ahead of the PR.
// worktreeAheadOfPR is what lets the gate publish that commit instead of
// reporting "no change".
func TestWorktreeAheadOfPR(t *testing.T) {
	ctx := context.Background()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	work := filepath.Join(root, "work")
	mustGit := func(dir string, args ...string) {
		t.Helper()
		if _, err := gitWorktree(ctx, dir, env, 30*time.Second, false, args...); err != nil {
			t.Fatalf("git %v in %s: %v", args, dir, err)
		}
	}
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustGit(root, "init", "-q", "--bare", remote)
	mustGit(root, "init", "-q", "-b", "main", seed)
	write(seed, "a.txt", "one\n")
	mustGit(seed, "add", "a.txt")
	mustGit(seed, "commit", "-q", "-m", "seed")
	mustGit(seed, "remote", "add", "origin", remote)
	mustGit(seed, "push", "-q", "origin", "main")
	remoteHead := gitOutput(ctx, seed, env, time.Minute, "rev-parse", "HEAD")

	mustGit(root, "clone", "-q", "-b", "main", remote, work)
	write(work, "b.txt", "two\n")
	mustGit(work, "add", "b.txt")
	mustGit(work, "commit", "-q", "-m", "local only")
	localHead := gitOutput(ctx, work, env, time.Minute, "rev-parse", "HEAD")

	local := &LocalRepo{HTTPSURL: remote, Env: env}
	ahead, head := worktreeAheadOfPR(ctx, work, local, "main", remoteHead)
	if !ahead {
		t.Fatal("ahead = false, want true for an unpushed local commit")
	}
	if head != localHead {
		t.Fatalf("head = %s, want %s", head, localHead)
	}

	mustGit(work, "push", "-q", "origin", "main")
	ahead, _ = worktreeAheadOfPR(ctx, work, local, "main", localHead)
	if ahead {
		t.Fatal("ahead = true after the commit was pushed")
	}
}

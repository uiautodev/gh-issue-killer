package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
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

// extraheaderToken extracts the bot token from the one-shot http.extraheader
// in a git environment built by buildGitEnv.
func extraheaderToken(t *testing.T, env []string) string {
	t.Helper()
	for _, entry := range env {
		rest, ok := strings.CutPrefix(entry, "GIT_CONFIG_VALUE_")
		if !ok {
			continue
		}
		if _, value, ok := strings.Cut(rest, "="); ok {
			rest = value
		}
		basic, ok := strings.CutPrefix(rest, "AUTHORIZATION: basic ")
		if !ok {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(basic)
		if err != nil {
			t.Fatalf("decode extraheader: %v", err)
		}
		token, ok := strings.CutPrefix(string(decoded), "x-access-token:")
		if !ok {
			t.Fatalf("extraheader = %q, want x-access-token: prefix", decoded)
		}
		return token
	}
	t.Fatal("no http.extraheader in git environment")
	return ""
}

func envEntryValue(env []string, key string) string {
	for _, entry := range env {
		if k, value, ok := strings.Cut(entry, "="); ok && k == key {
			return value
		}
	}
	return ""
}

// The bot token rotates every few hours; a git environment built once at
// startup kept authenticating with the expired token, so a fetch after a
// rotation failed with 401. gitEnv must follow the live token.
func TestLocalRepoGitEnvFollowsTokenRotation(t *testing.T) {
	token := "gho_first"
	local := &LocalRepo{token: func() string { return token }}
	if got := extraheaderToken(t, local.gitEnv()); got != "gho_first" {
		t.Fatalf("token = %q, want gho_first", got)
	}
	token = "gho_second"
	if got := extraheaderToken(t, local.gitEnv()); got != "gho_second" {
		t.Fatalf("token after rotation = %q, want gho_second", got)
	}
}

// The same rotation applies to the child-process environment: agents and
// their tools call gh with GH_TOKEN, and git with the extraheader, from
// AppContext.Env.
func TestAppContextEnvFollowsTokenRotation(t *testing.T) {
	gh := &GH{Token: "gho_first", Log: NewSilentLogger()}
	app := &AppContext{GH: gh, Identity: User{Login: "bot", ID: 1}}
	if got := extraheaderToken(t, app.Env()); got != "gho_first" {
		t.Fatalf("token = %q, want gho_first", got)
	}
	if got := envEntryValue(app.Env(), "GH_TOKEN"); got != "gho_first" {
		t.Fatalf("GH_TOKEN = %q, want gho_first", got)
	}
	gh.Token = "gho_second"
	if got := extraheaderToken(t, app.Env()); got != "gho_second" {
		t.Fatalf("token after rotation = %q, want gho_second", got)
	}
	if got := envEntryValue(app.Env(), "GH_TOKEN"); got != "gho_second" {
		t.Fatalf("GH_TOKEN after rotation = %q, want gho_second", got)
	}
	if got := envEntryValue(app.Env(), "GITHUB_TOKEN"); got != "gho_second" {
		t.Fatalf("GITHUB_TOKEN after rotation = %q, want gho_second", got)
	}
}

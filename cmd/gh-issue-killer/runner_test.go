package main

import (
	"slices"
	"testing"
)

func TestOpencodeArgs(t *testing.T) {
	args := opencodeArgs(RunnerOptions{
		Prompt:    "do the thing",
		Model:     "magpie/group/deepseek",
		SessionID: "ses_123",
	})
	for _, want := range []string{"run", "--session", "ses_123", "--model", "magpie/group/deepseek", "--format", "json", "--auto", "do the thing"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if slices.Contains(args, "--agent") {
		t.Fatalf("agent mode must not be passed: %v", args)
	}
}

func TestClaudeArgs(t *testing.T) {
	opts := RunnerOptions{
		Prompt: "plan it",
		Model:  "sonnet",
		RunDir: "/tmp/run",
	}
	args := claudeArgs(opts, "11111111-1111-4111-8111-111111111111")
	for _, want := range []string{"-p", "plan it", "--output-format", "json", "--permission-mode", "bypassPermissions", "--model", "sonnet", "--add-dir", "/tmp/run", "--session-id", "11111111-1111-4111-8111-111111111111"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if slices.Contains(args, "--resume") {
		t.Fatalf("fresh run must not resume: %v", args)
	}

	resumed := claudeArgs(RunnerOptions{Prompt: "fix it", SessionID: "22222222-2222-4222-8222-222222222222"}, "")
	if !slices.Contains(resumed, "--resume") || !slices.Contains(resumed, "22222222-2222-4222-8222-222222222222") {
		t.Fatalf("resume args missing: %v", resumed)
	}
	if slices.Contains(resumed, "--session-id") {
		t.Fatalf("resume must not set a new session id: %v", resumed)
	}
}

func TestCodexArgs(t *testing.T) {
	args := codexArgs(RunnerOptions{
		Cwd:    "/tmp/worktree",
		Prompt: "implement it",
		Model:  "gpt-5.1-codex",
		RunDir: "/tmp/run",
	}, "/tmp/run/last.txt")
	for _, want := range []string{"exec", "implement it", "--cd", "/tmp/worktree", "--full-auto", "--model", "gpt-5.1-codex", "--add-dir", "/tmp/run", "--output-last-message", "/tmp/run/last.txt"} {
		if !slices.Contains(args, want) {
			t.Fatalf("missing %q in %v", want, args)
		}
	}
	if slices.Contains(args, "--agent") || slices.Contains(args, "--profile") {
		t.Fatalf("agent/profile must not be passed: %v", args)
	}
}

func TestNewCodingRunner(t *testing.T) {
	for _, id := range []string{"opencode", "claude", "codex"} {
		runner, err := newCodingRunner(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if runner.ID() != id {
			t.Fatalf("runner id = %q, want %q", runner.ID(), id)
		}
	}
	if _, err := newCodingRunner("vim"); err == nil {
		t.Fatal("expected unsupported runner error")
	}
}

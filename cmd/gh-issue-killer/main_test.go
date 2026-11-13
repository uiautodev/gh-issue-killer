package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseProxy(t *testing.T) {
	proxy, err := parseProxy("socks5h://127.0.0.1:10808")
	if err != nil {
		t.Fatal(err)
	}
	if proxy.Scheme != "socks5h" || proxy.Host != "127.0.0.1" || proxy.Port != "10808" {
		t.Fatalf("unexpected proxy: %+v", proxy)
	}
	if proxy.goEnvValue() != "socks5://127.0.0.1:10808" {
		t.Fatalf("go env value = %q", proxy.goEnvValue())
	}

	typo, err := parseProxy("socks5h:/x127.0.0.1:10808")
	if err != nil {
		t.Fatal(err)
	}
	if typo.Raw != "socks5h://127.0.0.1:10808" {
		t.Fatalf("typo raw = %q", typo.Raw)
	}

	if _, err := parseProxy("ftp://127.0.0.1:21"); err == nil {
		t.Fatal("expected unsupported protocol error")
	}
	if _, err := parseProxy("socks5://noport"); err == nil {
		t.Fatal("expected missing port error")
	}
	empty, err := parseProxy("")
	if err != nil || empty != nil {
		t.Fatalf("empty proxy = %+v, %v", empty, err)
	}
}

func TestGitProxyEntries(t *testing.T) {
	proxy, _ := parseProxy("socks5h://127.0.0.1:10808")
	entries := gitProxyEntries(proxy)
	if len(entries) != 2 || entries[0][0] != "http.proxy" || entries[1][1] != "socks5h://127.0.0.1:10808" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	if gitProxyEntries(nil) != nil {
		t.Fatal("nil proxy should have no entries")
	}
}

func TestNormalizeRepos(t *testing.T) {
	raw := json.RawMessage(`{"me/one": {}, "me/two": {"codingAgent": "claude"}}`)
	repos, err := normalizeRepos(raw, repoOptions{Approvers: []string{"me"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("len = %d", len(repos))
	}
	if repos[0].Approvers[0] != "me" {
		t.Fatalf("approvers = %+v", repos[0].Approvers)
	}
	if repos[0].CodingAgent != "opencode" {
		t.Fatalf("default codingAgent = %q", repos[0].CodingAgent)
	}
	if repos[1].CodingAgent != "claude" {
		t.Fatalf("codingAgent = %q", repos[1].CodingAgent)
	}
	if _, err := normalizeRepos(json.RawMessage(`{"me/x": {"codingAgent": "vim"}}`), repoOptions{}); err == nil {
		t.Fatal("expected unsupported codingAgent error")
	}

	if _, err := normalizeRepos(json.RawMessage(`["me/dup","me/dup"]`), repoOptions{}); err == nil {
		t.Fatal("expected duplicate error")
	}
	if _, err := normalizeRepos(json.RawMessage(`"nope"`), repoOptions{}); err == nil {
		t.Fatal("expected type error")
	}
	if _, err := normalizeRepos(json.RawMessage(`[]`), repoOptions{}); err == nil {
		t.Fatal("expected empty error")
	}
}

func TestIsMentionAndCommands(t *testing.T) {
	if !isMention("hey @my-agent look", "my-agent") {
		t.Fatal("expected mention")
	}
	if isMention("hey @my-agent-2", "my-agent") {
		t.Fatal("longer login must not match")
	}
	if isMention("nothing here", "my-agent") {
		t.Fatal("unexpected mention")
	}

	cases := map[string]string{
		"@my-agent lgtm":             "approve",
		"@my-agent continue":         "approve",
		"@my-agent 继续":               "approve",
		"@my-agent lgtm 👍":           "approve",
		"@my-agent 合并":               "merge",
		"@my-agent 合并吧":              "merge",
		"@my-agent merge":            "merge",
		"@my-agent ship":             "merge",
		"@my-agent cancel":           "cancel",
		"@my-agent 停止":               "cancel",
		"lgtm":                       "",
		"@my-agent looks good, lgtm": "",
		"@my-agent 样式有点丑，合并到 input-group 里": "",
		"@my-agent 继续改":           "",
		"@my-agent 这个方案 lgtm，but": "",
	}
	for input, expected := range cases {
		if got := resolveCommand(input, true, "my-agent"); got != expected {
			t.Fatalf("resolveCommand(%q) = %q, want %q", input, got, expected)
		}
	}
	if got := resolveCommand("@my-agent lgtm", false, "my-agent"); got != "" {
		t.Fatalf("non-approver command = %q, want empty", got)
	}
}

func TestIsRelevantIssue(t *testing.T) {
	base := Issue{Number: 1, User: User{Login: "human"}}
	if !isRelevantIssue(Issue{Number: 1, User: User{Login: "human"}, Assignees: []User{{Login: "my-agent"}}}, "my-agent") {
		t.Fatal("assignee should be relevant")
	}
	mentioned := base
	mentioned.Body = "cc @my-agent"
	if !isRelevantIssue(mentioned, "my-agent") {
		t.Fatal("mention should be relevant")
	}
	pr := base
	pr.PullRequest = &struct{}{}
	if isRelevantIssue(pr, "my-agent") {
		t.Fatal("PR must be ignored")
	}
	self := base
	self.User = User{Login: "my-agent"}
	if isRelevantIssue(self, "my-agent") {
		t.Fatal("self-authored issue must be ignored")
	}
}

func TestClosablePhase(t *testing.T) {
	for _, phase := range []string{"new", "planning", "implementing", "awaiting_approval", "blocked", "pr_open"} {
		if !closablePhase(phase) {
			t.Fatalf("%q should be closable", phase)
		}
	}
	for _, phase := range []string{"", "done", "cancelled", "merging"} {
		if closablePhase(phase) {
			t.Fatalf("%q should not be closable", phase)
		}
	}
}

func TestIsAgentComment(t *testing.T) {
	if !isAgentComment("### 🤖 修复计划\n\nReply **LGTM** to proceed.") {
		t.Fatal("legacy bot heading should be daemon output")
	}
	if !isAgentComment("### 🤖 已完成\n\n" + agentCommentMarker) {
		t.Fatal("marked comment should be daemon output")
	}
	if isAgentComment("lgtm") {
		t.Fatal("a plain command from the bot account is not daemon output")
	}
	if isAgentComment("看起来不错，`### 🤖` 是标题") {
		t.Fatal("inline heading text must not be treated as daemon output")
	}
}

func TestResolveCommandApproverOnly(t *testing.T) {
	for _, body := range []string{"@my-agent lgtm", "@my-agent 合并", "@my-agent cancel"} {
		if got := resolveCommand(body, false, "my-agent"); got != "" {
			t.Fatalf("non-approver %q = %q, want empty", body, got)
		}
	}
}

func TestWasReopened(t *testing.T) {
	if !wasReopened(IssueState{Phase: "done"}) {
		t.Fatal("an open done issue was reopened")
	}
	if wasReopened(IssueState{Phase: "cancelled"}) {
		t.Fatal("cancel via comment keeps the issue open; must not resume")
	}
	if !wasReopened(IssueState{Phase: "cancelled", IssueClosed: true}) {
		t.Fatal("cancelled by closing the issue should resume when reopened")
	}
	for _, phase := range []string{"", "new", "planning", "implementing", "pr_open", "blocked"} {
		if wasReopened(IssueState{Phase: phase}) {
			t.Fatalf("%q must not count as reopened", phase)
		}
	}
}

func TestHasThumbsUp(t *testing.T) {
	reactions := []Reaction{
		{Content: "eyes", User: User{Login: "alice"}},
		{Content: "+1", User: User{Login: "bob"}},
	}
	if !hasThumbsUp(reactions, "bob") {
		t.Fatal("bob's 👍 should be found")
	}
	if hasThumbsUp(reactions, "alice") {
		t.Fatal("alice only left 👀")
	}
	if hasThumbsUp([]Reaction{{Content: "-1", User: User{Login: "bob"}}}, "bob") {
		t.Fatal("👎 must not approve")
	}
}

func TestMissingHookEvents(t *testing.T) {
	if got := missingHookEvents(hookEvents); len(got) != 0 {
		t.Fatalf("nothing should be missing, got %v", got)
	}
	got := missingHookEvents([]string{"issues", "issue_comment"})
	want := []string{"reaction", "pull_request_review", "pull_request_review_comment"}
	if len(got) != len(want) {
		t.Fatalf("missing events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("missing events = %v, want %v", got, want)
		}
	}
}

func TestChecksSummary(t *testing.T) {
	got := checksSummary([]CheckResult{{Name: "go test", OK: true}, {Name: "lint", OK: true}})
	want := "✅ 检查通过（2 项）\n- ✅ go test\n- ✅ lint"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	got = checksSummary([]CheckResult{{Name: "go test", OK: true}, {Name: "lint", OK: false}})
	want = "❌ 检查未通过（1/2 项）\n- ✅ go test\n- ❌ lint"
	if got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
	if got := checksSummary(nil); got != "" {
		t.Fatalf("empty summary = %q", got)
	}
	if got := checkLabel(CheckResult{Cmd: "node /tmp/run/verify.js verify"}); got != "node verify.js verify" {
		t.Fatalf("fallback label = %q", got)
	}
}

func TestEnsureGhAgentWrapper(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "empty"))

	path, created, err := ensureGhAgentWrapper()
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("wrapper should be created on a clean machine")
	}
	want := filepath.Join(home, ".local", "bin", "gh-agent")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != ghAgentWrapperScript {
		t.Fatalf("wrapper content = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0755", info.Mode().Perm())
	}

	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, created, err := ensureGhAgentWrapper(); err != nil || created {
		t.Fatalf("existing wrapper must be kept (created=%v err=%v)", created, err)
	}
}

func TestRouterSeparatesPlanAndWorkPools(t *testing.T) {
	cfg := &Config{PlanParallel: 1, ImplementParallel: 1}
	contexts := map[string]*AppContext{"repo": {}}
	router := NewRouter(cfg, contexts, "bot", nil, NewSilentLogger())

	block := make(chan struct{})
	started := make(chan struct{})
	planRan := make(chan struct{})
	t.Cleanup(func() { close(block) })

	router.enqueue("repo", "repo#1", stageWork, func(ctx context.Context) {
		close(started)
		<-block
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("work job did not start")
	}

	router.enqueue("repo", "repo#2", stagePlan, func(ctx context.Context) { close(planRan) })
	select {
	case <-planRan:
	case <-time.After(2 * time.Second):
		t.Fatal("plan job was blocked behind the work pool")
	}
}

func TestLoadConfigParallelKeys(t *testing.T) {
	cases := []struct {
		body       string
		want       int
		deprecated bool
	}{
		{`{"repos":["me/app"],"implementParallel":2}`, 2, false},
		{`{"repos":["me/app"],"maxParallel":3}`, 3, true},
		{`{"repos":["me/app"]}`, 1, false},
	}
	for _, c := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(path)
		if err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		if cfg.ImplementParallel != c.want || cfg.DeprecatedMaxParallel != c.deprecated {
			t.Fatalf("%s: implementParallel=%d deprecated=%v", c.body, cfg.ImplementParallel, cfg.DeprecatedMaxParallel)
		}
	}
}

func TestHeadFromLSRemote(t *testing.T) {
	if got := headFromLSRemote("abc123def456\trefs/heads/master\n"); got != "abc123def456" {
		t.Fatalf("head = %q", got)
	}
	if got := headFromLSRemote("  \n"); got != "" {
		t.Fatalf("empty head = %q", got)
	}
}

func TestIssueByPRAndFeedback(t *testing.T) {
	old := stateDirOverride
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = old })

	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateIssue("o/r", 9, func(state *IssueState) {
		state.Phase = "pr_open"
		state.PRNumber = 42
	})
	if number, st, ok := store.IssueByPR("o/r", 42); !ok || number != 9 || st.Phase != "pr_open" {
		t.Fatalf("IssueByPR = %d %+v %v", number, st, ok)
	}
	if _, _, ok := store.IssueByPR("o/r", 43); ok {
		t.Fatal("unknown PR should not match")
	}
	if store.IsFeedbackProcessed("o/r", 9, 77) {
		t.Fatal("feedback should start unprocessed")
	}
	store.MarkFeedbackProcessed("o/r", 9, 77)
	if !store.IsFeedbackProcessed("o/r", 9, 77) {
		t.Fatal("feedback should be marked processed")
	}
}

func TestRouterPoolsArePerRepo(t *testing.T) {
	cfg := &Config{PlanParallel: 1, ImplementParallel: 1}
	contexts := map[string]*AppContext{"a/a": {}, "b/b": {}}
	router := NewRouter(cfg, contexts, "bot", nil, NewSilentLogger())

	block := make(chan struct{})
	started := make(chan struct{})
	second := make(chan struct{})
	t.Cleanup(func() { close(block) })

	router.enqueue("a/a", "a/a#1", stageWork, func(ctx context.Context) {
		close(started)
		<-block
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first repo job did not start")
	}

	router.enqueue("b/b", "b/b#1", stageWork, func(ctx context.Context) { close(second) })
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		t.Fatal("repo b was blocked by repo a")
	}
}

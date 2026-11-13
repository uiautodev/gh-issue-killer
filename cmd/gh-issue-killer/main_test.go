package main

import (
	"encoding/json"
	"testing"
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
	if got := missingHookEvents([]string{"issues", "issue_comment", "reaction"}); len(got) != 0 {
		t.Fatalf("nothing should be missing, got %v", got)
	}
	got := missingHookEvents([]string{"issues", "issue_comment"})
	if len(got) != 1 || got[0] != "reaction" {
		t.Fatalf("missing events = %v, want [reaction]", got)
	}
}

func TestChecksSummary(t *testing.T) {
	if got := checksSummary([]CheckResult{{OK: true}, {OK: true}}); got != "✅ 检查通过（2 项）" {
		t.Fatalf("summary = %q", got)
	}
	if got := checksSummary([]CheckResult{{OK: true}, {OK: false}}); got != "❌ 检查未通过（1/2 项）" {
		t.Fatalf("summary = %q", got)
	}
	if got := checksSummary(nil); got != "" {
		t.Fatalf("empty summary = %q", got)
	}
}

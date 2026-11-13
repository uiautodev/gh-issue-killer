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
		"lgtm":                    "approve",
		"  continue  ":            "approve",
		"@my-agent lgtm":          "approve",
		"合并":                      "merge",
		"merge it":                "merge",
		"ship":                    "merge",
		"cancel":                  "cancel",
		"停止":                      "cancel",
		"continuously improving":  "",
		"looks reasonable, notes": "",
	}
	for input, expected := range cases {
		if got := parseCommand(input); got != expected {
			t.Fatalf("parseCommand(%q) = %q, want %q", input, got, expected)
		}
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

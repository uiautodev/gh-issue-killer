package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubWizardEnv(t *testing.T) {
	t.Helper()
	originalDetect := wizardDetectAgents
	originalProxyDetect := wizardDetectProxy
	originalProxyCheck := wizardProxyCheck
	originalRepoCheck := wizardRepoCheck
	originalModels := wizardModelList

	wizardDetectAgents = func() []DetectedCodingAgent {
		return []DetectedCodingAgent{
			{Info: CodingAgentInfo{ID: "opencode", Name: "opencode", Binary: "opencode"}, Path: "/bin/opencode", Version: "1.0.0"},
			{Info: CodingAgentInfo{ID: "claude", Name: "Claude Code", Binary: "claude"}, Path: "/bin/claude", Version: "2.1.0"},
			{Info: CodingAgentInfo{ID: "codex", Name: "Codex CLI", Binary: "codex"}, Path: "/bin/codex", Version: "0.100.0"},
		}
	}
	wizardDetectProxy = func() string { return "" }
	wizardProxyCheck = func(proxy *Proxy) error { return nil }
	wizardRepoCheck = func(proxy *Proxy, repos []string) ([]repoAccessInfo, error) {
		results := make([]repoAccessInfo, 0, len(repos))
		for _, repo := range repos {
			results = append(results, repoAccessInfo{Repo: repo, Permission: "admin", WebhookReady: true})
		}
		return results, nil
	}
	wizardModelList = func(string) []string { return []string{"model-a", "model-b"} }

	t.Cleanup(func() {
		wizardDetectAgents = originalDetect
		wizardDetectProxy = originalProxyDetect
		wizardProxyCheck = originalProxyCheck
		wizardRepoCheck = originalRepoCheck
		wizardModelList = originalModels
	})
}

func wizardInput(t *testing.T, lines ...string) *strings.Reader {
	t.Helper()
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func readConfigMap(t *testing.T, file string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var written map[string]any
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("written config is not JSON: %v\n%s", err, data)
	}
	return written
}

func TestSetupWizardWritesMinimalConfig(t *testing.T) {
	stubWizardEnv(t)
	file := filepath.Join(t.TempDir(), "config.json")

	var out strings.Builder
	input := wizardInput(t,
		"",                                // coding agent: opencode (default)
		"me/app",                          // repo 1
		"https://github.com/me/other.git", // repo 2
		"",                                // end repos
		"",                                // proxy: none
		"",                                // model: default
		"",                                // approvers: repo owner
		"",                                // advanced: no
		"y",                               // write
	)
	if err := runSetupWizard(file, input, &out); err != nil {
		t.Fatalf("runSetupWizard: %v", err)
	}
	if !strings.Contains(out.String(), "Detected coding agents") {
		t.Fatalf("missing detection output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Wrote "+file) {
		t.Fatalf("missing confirmation:\n%s", out.String())
	}

	written := readConfigMap(t, file)
	for _, key := range []string{"codingAgent", "model", "proxy", "approvers", "pollIntervalSec", "webhook"} {
		if _, ok := written[key]; ok {
			t.Fatalf("%q should be omitted, got %v", key, written)
		}
	}
	cfg, err := loadConfig(file)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.Repos) != 2 || cfg.Repos[1].FullName != "me/other" {
		t.Fatalf("repos = %+v", cfg.Repos)
	}
	if cfg.CodingAgent != "opencode" || cfg.Model != "" || cfg.Proxy != "" {
		t.Fatalf("agent/model/proxy = %q/%q/%q", cfg.CodingAgent, cfg.Model, cfg.Proxy)
	}
	if len(cfg.Repos[0].Approvers) != 1 || cfg.Repos[0].Approvers[0] != "me" {
		t.Fatalf("approvers = %+v", cfg.Repos[0].Approvers)
	}
}

func TestSetupWizardAdvancedAndClaude(t *testing.T) {
	stubWizardEnv(t)
	file := filepath.Join(t.TempDir(), "config.json")

	var out strings.Builder
	input := wizardInput(t,
		"2",                         // Claude Code
		"me/app",                    // repo
		"",                          // end repos
		"socks5h://127.0.0.1:10808", // proxy
		"anthropic/claude",          // model
		"alice, @bob, alice",        // approvers
		"y",                         // advanced
		"45",                        // poll interval
		"n",                         // webhooks off
		"5",                         // fix rounds
		"2",                         // parallel
		"20",                        // plan timeout
		"90",                        // implement timeout
		"~/trees",                   // worktree root
		"y",                         // write
	)
	if err := runSetupWizard(file, input, &out); err != nil {
		t.Fatalf("runSetupWizard: %v", err)
	}

	cfg, err := loadConfig(file)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	repo := cfg.Repos[0]
	if cfg.CodingAgent != "claude" || repo.CodingAgent != "claude" {
		t.Fatalf("codingAgent = %q/%q", cfg.CodingAgent, repo.CodingAgent)
	}
	if repo.Model != "anthropic/claude" {
		t.Fatalf("model = %q", repo.Model)
	}
	if strings.Join(repo.Approvers, ",") != "alice,bob" {
		t.Fatalf("approvers = %+v", repo.Approvers)
	}
	if cfg.Proxy != "socks5h://127.0.0.1:10808" || cfg.Webhook {
		t.Fatalf("proxy/webhook = %q/%v", cfg.Proxy, cfg.Webhook)
	}
	if cfg.PollInterval.Seconds() != 45 || cfg.MaxFixRounds != 5 || cfg.MaxParallel != 2 {
		t.Fatalf("poll/fix/parallel = %v/%d/%d", cfg.PollInterval, cfg.MaxFixRounds, cfg.MaxParallel)
	}
	if cfg.PlanTimeout.Minutes() != 20 || cfg.ImplementTimeout.Minutes() != 90 {
		t.Fatalf("timeouts = %v/%v", cfg.PlanTimeout, cfg.ImplementTimeout)
	}
	if repo.WorktreeRoot != filepath.Join(homeDir(), "trees") {
		t.Fatalf("worktreeRoot = %q", repo.WorktreeRoot)
	}
}

func TestSetupWizardProxyFailureCanBeRetriedOrForced(t *testing.T) {
	stubWizardEnv(t)
	wizardProxyCheck = func(proxy *Proxy) error { return errors.New("connection refused") }
	file := filepath.Join(t.TempDir(), "config.json")

	var out strings.Builder
	input := wizardInput(t,
		"",       // coding agent default
		"me/app", // repo
		"",       // end repos
		"http://127.0.0.1:7890",
		"",     // keep anyway? -> no
		"none", // retry with direct
		"",     // model
		"",     // approvers
		"",     // advanced
		"y",    // write
	)
	if err := runSetupWizard(file, input, &out); err != nil {
		t.Fatalf("runSetupWizard: %v", err)
	}
	cfg, err := loadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Proxy != "" {
		t.Fatalf("proxy should be direct, got %q", cfg.Proxy)
	}
	if !strings.Contains(out.String(), "connection refused") {
		t.Fatalf("missing proxy failure message:\n%s", out.String())
	}

	file2 := filepath.Join(t.TempDir(), "config.json")
	input2 := wizardInput(t,
		"", "me/app", "", "http://127.0.0.1:7890", "y", "", "", "", "y",
	)
	out.Reset()
	if err := runSetupWizard(file2, input2, &out); err != nil {
		t.Fatalf("runSetupWizard: %v", err)
	}
	cfg, err = loadConfig(file2)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("proxy = %q", cfg.Proxy)
	}
}

func TestSetupWizardKeepsExistingValuesAndRepoOverrides(t *testing.T) {
	stubWizardEnv(t)
	file := filepath.Join(t.TempDir(), "config.json")
	existing := `{
  "repos": {"me/app": {"worktreeRoot": "/tmp/wt", "approvers": ["bob"]}},
  "agent": "build",
  "model": "x/y",
  "approvers": ["bob"],
  "proxy": "socks5h://127.0.0.1:10808",
  "pollIntervalSec": 60,
  "webhook": false,
  "maxFixRounds": 4,
  "maxParallel": 2,
  "planTimeoutMin": 20,
  "implementTimeoutMin": 90
}
`
	if err := os.WriteFile(file, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	input := wizardInput(t,
		"",  // coding agent default
		"",  // keep repos
		"",  // keep proxy
		"",  // keep model
		"",  // keep approvers
		"y", // advanced
		"",  // poll
		"",  // webhook
		"",  // fix
		"",  // parallel
		"",  // plan
		"",  // implement
		"",  // worktree
		"y", // write
	)
	if err := runSetupWizard(file, input, &out); err != nil {
		t.Fatalf("runSetupWizard: %v", err)
	}

	written := readConfigMap(t, file)
	if _, ok := written["agent"]; ok {
		t.Fatalf("old agent field should be removed: %v", written)
	}
	if _, ok := written["codingAgent"]; ok {
		t.Fatalf("default codingAgent should be omitted: %v", written)
	}
	repos, ok := written["repos"].(map[string]any)
	if !ok {
		t.Fatalf("per-repo overrides should be preserved as an object: %v", written["repos"])
	}
	if _, ok := repos["me/app"]; !ok {
		t.Fatalf("repo override missing: %v", repos)
	}

	cfg, err := loadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodingAgent != "opencode" {
		t.Fatalf("codingAgent = %q", cfg.CodingAgent)
	}
	if cfg.Model != "x/y" || cfg.Proxy != "socks5h://127.0.0.1:10808" {
		t.Fatalf("model/proxy = %q/%q", cfg.Model, cfg.Proxy)
	}
	if cfg.Webhook || cfg.PollInterval.Seconds() != 60 {
		t.Fatalf("webhook/poll = %v/%v", cfg.Webhook, cfg.PollInterval)
	}
	if cfg.MaxFixRounds != 4 || cfg.MaxParallel != 2 {
		t.Fatalf("fix/parallel = %d/%d", cfg.MaxFixRounds, cfg.MaxParallel)
	}
	if cfg.PlanTimeout.Minutes() != 20 || cfg.ImplementTimeout.Minutes() != 90 {
		t.Fatalf("timeouts = %v/%v", cfg.PlanTimeout, cfg.ImplementTimeout)
	}
	if cfg.Repos[0].WorktreeRoot != "/tmp/wt" || strings.Join(cfg.Repos[0].Approvers, ",") != "bob" {
		t.Fatalf("repo = %+v", cfg.Repos[0])
	}
}

func TestSetupWizardDeclinedWriteLeavesFileAlone(t *testing.T) {
	stubWizardEnv(t)
	file := filepath.Join(t.TempDir(), "config.json")
	original := `{"repos": ["me/app"]}`
	if err := os.WriteFile(file, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	var out strings.Builder
	input := wizardInput(t,
		"",  // coding agent default
		"",  // keep repos
		"",  // proxy
		"",  // model
		"",  // approvers
		"",  // advanced
		"n", // write? no
	)
	if err := runSetupWizard(file, input, &out); err != nil {
		t.Fatalf("runSetupWizard: %v", err)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("file changed despite declining:\n%s", data)
	}
	if !strings.Contains(out.String(), "Nothing written") {
		t.Fatalf("missing decline message:\n%s", out.String())
	}
}

func TestSetupWizardWithoutStdin(t *testing.T) {
	stubWizardEnv(t)
	file := filepath.Join(t.TempDir(), "config.json")
	if err := runSetupWizard(file, strings.NewReader(""), &strings.Builder{}); !errors.Is(err, errWizardNoInput) {
		t.Fatalf("err = %v, want errWizardNoInput", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("config should not be created: %v", err)
	}
}

func TestSetupWizardStopsWhenRepoAccessIsTooLow(t *testing.T) {
	stubWizardEnv(t)
	wizardRepoCheck = func(proxy *Proxy, repos []string) ([]repoAccessInfo, error) {
		return []repoAccessInfo{{Repo: "me/app", Permission: "read"}}, nil
	}
	file := filepath.Join(t.TempDir(), "config.json")

	var out strings.Builder
	input := wizardInput(t,
		"",       // coding agent default
		"me/app", // repo
		"",       // end repos
		"",       // proxy
		"n",      // continue anyway? no
	)
	if err := runSetupWizard(file, input, &out); err != nil {
		t.Fatalf("runSetupWizard: %v", err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("config should not be created: %v", err)
	}
	if !strings.Contains(out.String(), "cannot push/merge") {
		t.Fatalf("missing access warning:\n%s", out.String())
	}
}

func TestWizardModelList(t *testing.T) {
	stubWizardEnv(t)
	file := filepath.Join(t.TempDir(), "config.json")
	var out strings.Builder
	input := wizardInput(t,
		"",       // coding agent default
		"me/app", // repo
		"",       // end repos
		"",       // proxy
		"?",      // list models
		"model-a",
		"",  // approvers
		"",  // advanced
		"y", // write
	)
	if err := runSetupWizard(file, input, &out); err != nil {
		t.Fatalf("runSetupWizard: %v", err)
	}
	cfg, err := loadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Model != "model-a" {
		t.Fatalf("model = %q", cfg.Model)
	}
	if !strings.Contains(out.String(), "model-a") || !strings.Contains(out.String(), "model-b") {
		t.Fatalf("missing model list:\n%s", out.String())
	}
}

func TestCodingAgentMenu(t *testing.T) {
	ui := newWizardUI(strings.NewReader("2\n"), &strings.Builder{})
	detected := []DetectedCodingAgent{
		{Info: CodingAgentInfo{ID: "opencode", Name: "opencode", Binary: "opencode"}, Version: "1"},
		{Info: CodingAgentInfo{ID: "claude", Name: "Claude Code", Binary: "claude"}, Version: "2"},
	}
	id, err := ui.codingAgent("opencode", detected)
	if err != nil {
		t.Fatal(err)
	}
	if id != "claude" {
		t.Fatalf("selected %q", id)
	}
}

func TestWizardParsers(t *testing.T) {
	repos, err := parseRepoList("me/one; https://github.com/me/two.git,me/three")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(repos, ",") != "me/one,me/two,me/three" {
		t.Fatalf("repos = %v", repos)
	}
	if _, err := parseRepoList("nope"); err == nil {
		t.Fatal("expected repo validation error")
	}
	if _, err := parseRepoList("me/a, me/a"); err == nil {
		t.Fatal("expected duplicate error")
	}
	if _, err := parseRepoList(" , "); err == nil {
		t.Fatal("expected empty error")
	}

	logins := parseLogins(" @alice, bob ,alice,none")
	if strings.Join(logins, ",") != "alice,bob" {
		t.Fatalf("logins = %v", logins)
	}
	if len(parseLogins("none")) != 0 {
		t.Fatal("none should clear approvers")
	}
}

func TestBuildRepoOutputPreservesOverrides(t *testing.T) {
	original := map[string]repoOptions{
		"me/app": {WorktreeRoot: "/tmp/wt", Approvers: []string{"bob"}},
	}
	output := buildRepoOutput([]string{"me/app", "me/new"}, original)
	asMap, ok := output.(map[string]repoOverride)
	if !ok {
		t.Fatalf("expected object output, got %T", output)
	}
	if asMap["me/app"].WorktreeRoot != "/tmp/wt" {
		t.Fatalf("override lost: %+v", asMap)
	}
	if _, ok := asMap["me/new"]; !ok {
		t.Fatalf("new repo missing: %+v", asMap)
	}

	plain := buildRepoOutput([]string{"me/a"}, nil)
	if _, ok := plain.([]string); !ok {
		t.Fatalf("expected plain list, got %T", plain)
	}
}

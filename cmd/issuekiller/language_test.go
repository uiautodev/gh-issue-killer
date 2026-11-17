package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeReposLanguage(t *testing.T) {
	inherited, err := normalizeRepos(json.RawMessage(`["acme/one"]`), repoOptions{Language: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if got := inherited[0].Language; got != "zh-CN" {
		t.Fatalf("inherited language = %q, want zh-CN", got)
	}

	overridden, err := normalizeRepos(json.RawMessage(`{"acme/two": {"language": "en"}}`), repoOptions{Language: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if got := overridden[0].Language; got != "en" {
		t.Fatalf("per-repo language = %q, want en", got)
	}

	plain, err := normalizeRepos(json.RawMessage(`["acme/one"]`), repoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := plain[0].Language; got != "auto" {
		t.Fatalf("default language = %q, want auto", got)
	}
}

func TestLanguageConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	body := `{"repos": ["acme/one"], "language": "en"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Language != "en" || cfg.Repos[0].Language != "en" {
		t.Fatalf("language = %q, repo = %q", cfg.Language, cfg.Repos[0].Language)
	}

	defaults, err := loadConfig(writeTemp(t, dir, `{"repos": ["acme/one"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if defaults.Language != "auto" || defaults.Repos[0].Language != "auto" {
		t.Fatalf("default language = %q / %q, want auto", defaults.Language, defaults.Repos[0].Language)
	}
}

func writeTemp(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, fmt.Sprintf("config-%d.json", time.Now().UnixNano()))
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPromptLanguage(t *testing.T) {
	auto := &AppContext{
		Cfg:  &Config{Language: "auto"},
		Repo: RepoConfig{Language: "auto"},
	}
	if got := promptLanguage(auto); !strings.Contains(got, "same language") {
		t.Fatalf("auto = %q, want the follow-the-issue instruction", got)
	}

	global := &AppContext{
		Cfg:  &Config{Language: "zh-CN"},
		Repo: RepoConfig{Language: ""},
	}
	if got := promptLanguage(global); got != "zh-CN" {
		t.Fatalf("global = %q, want zh-CN", got)
	}

	perRepo := &AppContext{
		Cfg:  &Config{Language: "zh-CN"},
		Repo: RepoConfig{Language: "en"},
	}
	if got := promptLanguage(perRepo); got != "en" {
		t.Fatalf("per-repo = %q, want en", got)
	}

	noConfig := &AppContext{Repo: RepoConfig{}}
	if got := promptLanguage(noConfig); !strings.Contains(got, "same language") {
		t.Fatalf("missing config = %q, want the follow-the-issue instruction", got)
	}
}

func TestPromptLanguagePlaceholder(t *testing.T) {
	text, err := renderPrompt("plan", map[string]string{"LANGUAGE": "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "zh-CN") || strings.Contains(text, "{{LANGUAGE}}") {
		t.Fatal("plan prompt did not receive the language")
	}
}

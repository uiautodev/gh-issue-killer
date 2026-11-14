package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeReposReplyLanguage(t *testing.T) {
	inherited, err := normalizeRepos(json.RawMessage(`["acme/one"]`), repoOptions{ReplyLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if got := inherited[0].ReplyLanguage; got != "zh-CN" {
		t.Fatalf("inherited language = %q, want zh-CN", got)
	}

	overridden, err := normalizeRepos(json.RawMessage(`{"acme/two": {"replyLanguage": "en"}}`), repoOptions{ReplyLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if got := overridden[0].ReplyLanguage; got != "en" {
		t.Fatalf("per-repo language = %q, want en", got)
	}

	plain, err := normalizeRepos(json.RawMessage(`["acme/one"]`), repoOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := plain[0].ReplyLanguage; got != "auto" {
		t.Fatalf("default language = %q, want auto", got)
	}
}

func TestReplyLanguage(t *testing.T) {
	auto := &AppContext{
		Cfg:  &Config{ReplyLanguage: "auto"},
		Repo: RepoConfig{ReplyLanguage: "auto"},
	}
	if got := replyLanguage(auto); !strings.Contains(got, "same language") {
		t.Fatalf("auto = %q, want the follow-the-issue instruction", got)
	}

	global := &AppContext{
		Cfg:  &Config{ReplyLanguage: "zh-CN"},
		Repo: RepoConfig{ReplyLanguage: ""},
	}
	if got := replyLanguage(global); got != "zh-CN" {
		t.Fatalf("global = %q, want zh-CN", got)
	}

	perRepo := &AppContext{
		Cfg:  &Config{ReplyLanguage: "zh-CN"},
		Repo: RepoConfig{ReplyLanguage: "en"},
	}
	if got := replyLanguage(perRepo); got != "en" {
		t.Fatalf("per-repo = %q, want en", got)
	}

	noConfig := &AppContext{Repo: RepoConfig{}}
	if got := replyLanguage(noConfig); !strings.Contains(got, "same language") {
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

package main

import (
	"strings"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	cases := []struct {
		sample string
		want   string
	}{
		{"设备连接超时，重连后仍然失败", "zh"},
		{"Fix the reconnect loop in _adb.py", "en"},
		{"修复 reconnect 逻辑：不要在 AdbConnection 层重连，保留 timeout", "zh"},
		{"", "en"},
		{"接続がタイムアウトします", "en"}, // kana falls back to English scaffolding
		{"연결이 끊어집니다", "en"},    // hangul too
	}
	for _, tc := range cases {
		if got := detectLanguage(tc.sample); got != tc.want {
			t.Fatalf("detectLanguage(%q) = %q, want %q", tc.sample, got, tc.want)
		}
	}
}

// Hints that take no arguments must not contain a verb: a stray %s would make
// fmt append %!EXTRA(...) to the comment, which is exactly what happened to the
// "not merging yet" hint.
func TestScaffoldTextWithoutArgs(t *testing.T) {
	for _, key := range []string{"ci.blocked.hint", "ci.repair.hint", "merge.failed.hint", "review.nothing"} {
		for _, lang := range []string{"zh", "en"} {
			if got := scaffoldText(lang, key); strings.Contains(got, "%") {
				t.Fatalf("%s/%s = %q, want no format verbs", lang, key, got)
			}
		}
	}
}

func TestScaffoldCatalogParity(t *testing.T) {
	for key := range scaffoldZH {
		if _, ok := scaffoldEN[key]; !ok {
			t.Fatalf("key %q is missing from the English catalog", key)
		}
	}
	for key := range scaffoldEN {
		if _, ok := scaffoldZH[key]; !ok {
			t.Fatalf("key %q is missing from the Chinese catalog", key)
		}
	}
}

func TestScaffoldText(t *testing.T) {
	if got := scaffoldText("zh", "plan.title"); got != "### 🤖 修复计划" {
		t.Fatalf("zh title = %q", got)
	}
	if got := scaffoldText("en", "plan.title"); got != "### 🤖 Fix plan" {
		t.Fatalf("en title = %q", got)
	}
	if got := scaffoldText("ja", "plan.title"); got != "### 🤖 Fix plan" {
		t.Fatalf("unknown language = %q, want the English fallback", got)
	}
	if got := scaffoldText("zh", "checks.pass", 3); !strings.Contains(got, "3") {
		t.Fatalf("formatted text = %q", got)
	}
	if got := scaffoldText("en", "missing.key"); got != "missing.key" {
		t.Fatalf("missing key = %q", got)
	}
}

func TestAppTextFollowsConfiguredLanguage(t *testing.T) {
	app := &AppContext{
		Cfg:  &Config{},
		Repo: RepoConfig{Language: "en"},
	}
	if got := app.text(1, "merge.done.title"); got != "### 🤖 Done" {
		t.Fatalf("configured en = %q", got)
	}
	app.Repo.Language = "zh-CN"
	if got := app.text(1, "merge.done.title"); got != "### 🤖 已完成" {
		t.Fatalf("configured zh = %q", got)
	}
}

func TestAppTextFollowsIssueLanguage(t *testing.T) {
	store := &Store{file: t.TempDir() + "/state.json", state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.SetPRMeta("acme/app", 7, &IssueMeta{Title: "设备连接超时", Body: "重连之后仍然失败"})
	app := &AppContext{
		Cfg:   &Config{},
		Repo:  RepoConfig{FullName: "acme/app"},
		Store: store,
	}
	if got := app.text(7, "ci.wait.title"); got != "### 🤖 正在等待 CI" {
		t.Fatalf("auto zh = %q", got)
	}

	store.SetPRMeta("acme/app", 8, &IssueMeta{Title: "connection timeout", Body: "the client hangs"})
	if got := app.text(8, "ci.wait.title"); got != "### 🤖 Waiting for CI" {
		t.Fatalf("auto en = %q", got)
	}
}

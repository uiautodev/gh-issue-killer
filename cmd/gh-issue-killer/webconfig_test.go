package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newConfigTestWeb(t *testing.T) (*webServer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	web := &webServer{
		cfg: &Config{File: path, WebPort: 8787, PollInterval: 30, PlanParallel: 5, ImplementParallel: 1},
		log: NewSilentLogger(),
	}
	return web, path
}

func putConfig(t *testing.T, web *webServer, body string, origin string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	recorder := httptest.NewRecorder()
	web.handleConfig(recorder, request)
	return recorder
}

func TestWebConfigRejectsInvalidValuesWithoutWriting(t *testing.T) {
	web, path := newConfigTestWeb(t)
	cases := []string{
		`{"config":{"repos":["o/r"],"codingAgent":"bogus"}}`,
		`{"config":{"repos":["not-a-repo"],"codingAgent":"opencode"}}`,
		`{"config":{"repos":["o/r"],"webPort":70000}}`,
		`{"config":{"repos":["o/r"],"proxy":"ftp://127.0.0.1:21"}}`,
		`{"config":{"repos":["o/r"],"updateProxy":"ftp://mirror"}}`,
		`{"config":{"repos":[]}}`,
	}
	for _, body := range cases {
		recorder := putConfig(t, web, body, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (%s)", body, recorder.Code, recorder.Body.String())
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: config must not be written", body)
		}
	}
}

func TestWebConfigRejectsCrossOrigin(t *testing.T) {
	web, path := newConfigTestWeb(t)
	body := `{"config":{"repos":["o/r"],"codingAgent":"opencode"}}`
	recorder := putConfig(t, web, body, "https://evil.example")
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cross-origin PUT must not write the config")
	}
}

func TestWebConfigSavesValidDocument(t *testing.T) {
	web, path := newConfigTestWeb(t)
	body := `{"config":{"repos":["o/r"],"codingAgent":"claude","webPort":9000,"updateProxy":"gh-proxy.com","selfUpdate":true}}`
	recorder := putConfig(t, web, body, "http://127.0.0.1:8787")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("saved config does not load: %v", err)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0].CodingAgent != "claude" || cfg.WebPort != 9000 {
		t.Fatalf("config = %+v", cfg)
	}
	if cfg.UpdateProxy != "https://gh-proxy.com" || !cfg.SelfUpdate {
		t.Fatalf("update settings = %q selfUpdate=%v", cfg.UpdateProxy, cfg.SelfUpdate)
	}
	if _, err := os.Stat(path + ".validate"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("validation temp file must be removed")
	}
}

func TestWebConfigDefaultsIncludeRelease(t *testing.T) {
	web, _ := newConfigTestWeb(t)
	payload := web.configPayload()
	defaults, ok := payload["defaults"].(map[string]any)
	if !ok {
		t.Fatalf("defaults = %#v", payload["defaults"])
	}
	release, ok := defaults["release"].(map[string]any)
	if !ok {
		t.Fatalf("defaults.release = %#v", defaults["release"])
	}
	want := ReleaseConfig{Enabled: false, IntervalDays: 1}
	got := ReleaseConfig{
		Enabled:      release["enabled"] == true,
		IntervalDays: release["intervalDays"].(int),
	}
	if got != want {
		t.Fatalf("defaults.release = %+v, want %+v", got, want)
	}
	if _, ok := release["bugBump"]; ok {
		t.Fatalf("defaults.release must not carry a bump: %#v", release)
	}
}

func TestWebConfigSavesReleaseObjectForm(t *testing.T) {
	web, path := newConfigTestWeb(t)
	body := `{"config":{"repos":{"o/r":{"codingAgent":"claude","model":"sonnet","language":"en",` +
		`"release":{"enabled":true,"intervalDays":7,"bugBump":"MINOR","featureBump":"major"}}}}}`
	recorder := putConfig(t, web, body, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("saved config does not load: %v", err)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("repos = %+v", cfg.Repos)
	}
	repo := cfg.Repos[0]
	want := ReleaseConfig{Enabled: true, IntervalDays: 7}
	if repo.Release != want {
		t.Fatalf("release = %+v, want %+v", repo.Release, want)
	}
	// The per-repo overrides must survive the release row being written.
	if repo.CodingAgent != "claude" || repo.Model != "sonnet" || repo.Language != "en" {
		t.Fatalf("overrides = %+v", repo)
	}
	// The removed bump switches are stripped, so an upgraded file saves clean.
	raw := readRawConfig(t, path)
	release := raw["repos"].(map[string]any)["o/r"].(map[string]any)["release"].(map[string]any)
	if release["intervalDays"] != float64(7) {
		t.Fatalf("raw release = %#v", release)
	}
	if _, ok := release["bugBump"]; ok {
		t.Fatalf("legacy bugBump must be dropped, got %#v", release)
	}
	if _, ok := release["featureBump"]; ok {
		t.Fatalf("legacy featureBump must be dropped, got %#v", release)
	}
}

func TestWebConfigSavesReleaseArrayForm(t *testing.T) {
	web, path := newConfigTestWeb(t)
	body := `{"config":{"repos":[{"repo":"o/r","codingAgent":"codex","release":{"enabled":true,"intervalDays":3}},` +
		`{"repo":"o/two"}]}}`
	recorder := putConfig(t, web, body, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatalf("saved config does not load: %v", err)
	}
	if len(cfg.Repos) != 2 {
		t.Fatalf("repos = %+v", cfg.Repos)
	}
	want := ReleaseConfig{Enabled: true, IntervalDays: 3}
	if cfg.Repos[0].FullName != "o/r" || cfg.Repos[0].Release != want || cfg.Repos[0].CodingAgent != "codex" {
		t.Fatalf("first repo = %+v", cfg.Repos[0])
	}
	if cfg.Repos[1].Release.Enabled {
		t.Fatalf("second repo release = %+v", cfg.Repos[1].Release)
	}
}

func TestWebConfigRejectsInvalidRelease(t *testing.T) {
	web, path := newConfigTestWeb(t)
	cases := []string{
		`{"config":{"repos":{"o/r":{"release":{"enabled":true,"intervalDays":0}}}}}`,
		`{"config":{"repos":{"o/r":{"release":{"intervalDays":-2}}}}}`,
		`{"config":{"repos":{"o/r":{"release":{"intervalDays":1.5}}}}}`,
		`{"config":{"repos":{"o/r":{"release":{"enabled":"yes"}}}}}`,
		`{"config":{"repos":{"o/r":{"release":true}}}}`,
		`{"config":{"repos":[{"repo":"o/r","release":{"intervalDays":0}}]}}`,
	}
	for _, body := range cases {
		recorder := putConfig(t, web, body, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d (%s)", body, recorder.Code, recorder.Body.String())
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s: config must not be written", body)
		}
	}
}

func TestWebConfigDropsNullReleaseFields(t *testing.T) {
	web, path := newConfigTestWeb(t)
	body := `{"config":{"repos":{"o/r":{"release":{"enabled":false,"intervalDays":null,"bugBump":null}}}}}`
	recorder := putConfig(t, web, body, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	raw := readRawConfig(t, path)
	release := raw["repos"].(map[string]any)["o/r"].(map[string]any)["release"].(map[string]any)
	if _, ok := release["intervalDays"]; ok {
		t.Fatalf("intervalDays must be dropped, got %#v", release)
	}
	if _, ok := release["bugBump"]; ok {
		t.Fatalf("bugBump must be dropped, got %#v", release)
	}
}

func readRawConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWebConfigSaveRequestsRestart(t *testing.T) {
	web, _ := newConfigTestWeb(t)
	restarted := make(chan struct{}, 1)
	web.reload = func() {
		select {
		case restarted <- struct{}{}:
		default:
		}
	}
	recorder := putConfig(t, web, `{"repos":["o/r"],"codingAgent":"opencode"}`, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("saving the config must request a restart")
	}
}

func TestWebConfigPayloadReturnsRawFile(t *testing.T) {
	web, path := newConfigTestWeb(t)
	if err := os.WriteFile(path, []byte(`{"repos":["o/r"],"language":"en","customKey":42}`), 0o644); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	web.handleConfig(recorder, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload struct {
		Exists bool           `json:"exists"`
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Exists || payload.Config["customKey"] != float64(42) || payload.Config["language"] != "en" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestWebDetectDegradesToEmptyLists(t *testing.T) {
	web, _ := newConfigTestWeb(t)
	oldAgents, oldModels, oldProxy, oldRepoCheck := wizardDetectAgents, wizardModelList, wizardDetectProxy, wizardRepoCheck
	t.Cleanup(func() {
		wizardDetectAgents, wizardModelList, wizardDetectProxy, wizardRepoCheck = oldAgents, oldModels, oldProxy, oldRepoCheck
	})
	wizardDetectAgents = func() []DetectedCodingAgent { return nil }
	wizardModelList = func(string) []string { return nil }
	wizardDetectProxy = func() string { return "" }
	wizardRepoCheck = func(*Proxy, []string) ([]repoAccessInfo, error) {
		return nil, errors.New("GitHub token is missing")
	}

	request := httptest.NewRequest(http.MethodGet, "/api/detect?proxy=1&repos=o/r", nil)
	recorder := httptest.NewRecorder()
	web.handleDetect(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload detectPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Agents == nil || payload.Models == nil || payload.Repos == nil {
		t.Fatalf("lists must serialize as [], got %s", recorder.Body.String())
	}
	if len(payload.Agents) != 0 || len(payload.Repos) != 0 {
		t.Fatalf("payload = %+v", payload)
	}
	if !strings.Contains(payload.Error, "GitHub token") {
		t.Fatalf("probe error = %q", payload.Error)
	}
}

func TestWebDetectReportsAgentsAndModels(t *testing.T) {
	web, _ := newConfigTestWeb(t)
	oldAgents, oldModels, oldProxy, oldRepoCheck := wizardDetectAgents, wizardModelList, wizardDetectProxy, wizardRepoCheck
	t.Cleanup(func() {
		wizardDetectAgents, wizardModelList, wizardDetectProxy, wizardRepoCheck = oldAgents, oldModels, oldProxy, oldRepoCheck
	})
	wizardDetectAgents = func() []DetectedCodingAgent {
		return []DetectedCodingAgent{{Info: CodingAgentInfo{ID: "claude", Name: "Claude Code", Binary: "claude"}, Path: "/bin/claude", Version: "1.0"}}
	}
	wizardModelList = func(id string) []string {
		if id != "claude" {
			t.Fatalf("model list asked for %q", id)
		}
		return []string{"sonnet", "opus"}
	}
	wizardDetectProxy = func() string { return "socks5h://127.0.0.1:1080" }
	wizardRepoCheck = func(_ *Proxy, repos []string) ([]repoAccessInfo, error) {
		return []repoAccessInfo{{Repo: repos[0], Permission: "write"}}, nil
	}

	request := httptest.NewRequest(http.MethodGet, "/api/detect?agent=claude&proxy=1&repos=o/r", nil)
	recorder := httptest.NewRecorder()
	web.handleDetect(recorder, request)
	var payload detectPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Agents) != 1 || payload.Agents[0].ID != "claude" || len(payload.Models) != 2 {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Proxy != "socks5h://127.0.0.1:1080" || len(payload.Repos) != 1 || payload.Repos[0].Permission != "write" {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestWebUpdateReportsAvailableRelease(t *testing.T) {
	web, _ := newConfigTestWeb(t)
	web.version = "v1.0.0"
	web.latestRelease = func(context.Context) (*releaseInfo, error) {
		return &releaseInfo{TagName: "v1.1.0", HTMLURL: "https://example.com", Body: "notes"}, nil
	}
	recorder := httptest.NewRecorder()
	web.handleUpdate(recorder, httptest.NewRequest(http.MethodGet, "/api/update", nil))
	var payload updatePayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Current != "v1.0.0" || payload.Latest != "v1.1.0" || !payload.Available {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestWebRestartRequiresJSONPost(t *testing.T) {
	web, _ := newConfigTestWeb(t)
	recorder := httptest.NewRecorder()
	web.handleRestart(recorder, httptest.NewRequest(http.MethodGet, "/api/restart", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", recorder.Code)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/restart", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://evil.example")
	recorder = httptest.NewRecorder()
	web.handleRestart(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d, want 403", recorder.Code)
	}
}

func TestWebSnapshotWithoutRouter(t *testing.T) {
	old := stateDirOverride
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = old })
	store, _ := LoadStore()
	web := &webServer{
		cfg:     &Config{File: "config.json", PlanParallel: 5, ImplementParallel: 1},
		store:   store,
		log:     NewSilentLogger(),
		started: time.Now(),
	}
	snapshot := web.snapshot()
	if snapshot.Tasks == nil || snapshot.Repos == nil {
		t.Fatal("an empty snapshot must serialize as [] not null")
	}
	if snapshot.ConfigError != "" {
		t.Fatalf("configError = %q", snapshot.ConfigError)
	}
}

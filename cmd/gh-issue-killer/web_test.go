package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSnapshotOnlyListsTrackedTasks(t *testing.T) {
	old := stateDirOverride
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = old })

	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateIssue("o/r", 1, func(state *IssueState) {
		state.Phase = "planning"
		state.Issue = &IssueMeta{Title: "tracked", URL: "https://example.com/1"}
	})
	store.UpdateIssue("o/r", 2, func(state *IssueState) {
		// A comment watermark with no phase must not show up as a task.
		state.LastCommentID = 5
	})

	cfg := &Config{File: "config.json", PlanParallel: 5, ImplementParallel: 1, Repos: []RepoConfig{{FullName: "o/r", CodingAgent: "opencode"}}}
	router := NewRouter(&Config{PlanParallel: 1, ImplementParallel: 1}, map[string]*AppContext{"o/r": {}}, "bot", store, NewSilentLogger())
	web := &webServer{cfg: cfg, router: router, store: store, log: NewSilentLogger()}

	snapshot := web.snapshot()
	if len(snapshot.Tasks) != 1 {
		t.Fatalf("tasks = %d, want 1", len(snapshot.Tasks))
	}
	task := snapshot.Tasks[0]
	if task.Number != 1 || task.Title != "tracked" || task.PhaseLabel != "规划中" {
		t.Fatalf("task = %+v", task)
	}
	if len(snapshot.Repos) != 1 || snapshot.Repos[0].Issues != 1 {
		t.Fatalf("repo counts = %+v", snapshot.Repos)
	}
}

func TestWebSnapshotCountsTasksWaitingForUser(t *testing.T) {
	old := stateDirOverride
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = old })

	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	phases := map[int]string{
		1: "planning",
		2: "awaiting_approval",
		3: "blocked",
		4: "pr_open",
		5: "done",
	}
	for number, phase := range phases {
		store.UpdateIssue("o/r", number, func(state *IssueState) {
			state.Phase = phase
			state.Issue = &IssueMeta{Title: "task", URL: "https://example.com"}
			if phase == "pr_open" || phase == "blocked" {
				state.PRNumber = 40 + number
			}
		})
	}

	cfg := &Config{File: "config.json", PlanParallel: 5, ImplementParallel: 1, Repos: []RepoConfig{{FullName: "o/r", CodingAgent: "opencode"}}}
	router := NewRouter(&Config{PlanParallel: 1, ImplementParallel: 1}, map[string]*AppContext{"o/r": {}}, "bot", store, NewSilentLogger())
	web := &webServer{cfg: cfg, router: router, store: store, log: NewSilentLogger()}

	snapshot := web.snapshot()
	if snapshot.Waiting != 3 {
		t.Fatalf("waiting = %d, want 3 (awaiting_approval, blocked, pr_open)", snapshot.Waiting)
	}
	for _, phase := range []string{"planning", "done"} {
		if phaseNeedsUser(phase) {
			t.Fatalf("%s must not count as waiting", phase)
		}
	}
}

func newOpenURLTestWeb(t *testing.T) *webServer {
	t.Helper()
	old := runOpenURL
	t.Cleanup(func() { runOpenURL = old })
	return &webServer{
		cfg: &Config{File: "config.json", WebPort: 8787},
		log: NewSilentLogger(),
	}
}

func postOpenURL(t *testing.T, web *webServer, body, origin string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/open-url", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	recorder := httptest.NewRecorder()
	web.handleOpenURL(recorder, request)
	return recorder
}

func TestWebOpenURLHandsHTTPLinkToBrowser(t *testing.T) {
	web := newOpenURLTestWeb(t)
	var opened []string
	runOpenURL = func(_ string, rawURL string) error {
		opened = append(opened, rawURL)
		return nil
	}

	recorder := postOpenURL(t, web, `{"url":"https://github.com/uiautodev/gh-issue-killer/pull/7"}`, "http://127.0.0.1:8787")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(opened) != 1 || opened[0] != "https://github.com/uiautodev/gh-issue-killer/pull/7" {
		t.Fatalf("opened = %#v", opened)
	}
	var payload struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil || !payload.OK {
		t.Fatalf("payload = %s", recorder.Body.String())
	}
}

func TestWebOpenURLRejectsUnsafeInput(t *testing.T) {
	web := newOpenURLTestWeb(t)
	opened := 0
	runOpenURL = func(string, string) error {
		opened++
		return nil
	}

	cases := []struct {
		name   string
		body   string
		origin string
		want   int
	}{
		{"file URL", `{"url":"file:///etc/passwd"}`, "", http.StatusBadRequest},
		{"javascript URL", `{"url":"javascript:alert(1)"}`, "", http.StatusBadRequest},
		{"ftp URL", `{"url":"ftp://example.com/x"}`, "", http.StatusBadRequest},
		{"missing host", `{"url":"https://"}`, "", http.StatusBadRequest},
		{"relative URL", `{"url":"//example.com"}`, "", http.StatusBadRequest},
		{"bad body", `{"url":`, "", http.StatusBadRequest},
		{"cross origin", `{"url":"https://example.com"}`, "https://evil.example", http.StatusForbidden},
	}
	for _, tc := range cases {
		recorder := postOpenURL(t, web, tc.body, tc.origin)
		if recorder.Code != tc.want {
			t.Fatalf("%s: status = %d, want %d (%s)", tc.name, recorder.Code, tc.want, recorder.Body.String())
		}
	}
	if opened != 0 {
		t.Fatalf("browser opened %d times for rejected input", opened)
	}
	if recorder := postOpenURL(t, web, `{"url":"https://example.com"}`, ""); recorder.Code != http.StatusOK {
		t.Fatalf("loopback-less POST must work: %d", recorder.Code)
	}
}

func TestWebOpenURLRequiresJSONPost(t *testing.T) {
	web := newOpenURLTestWeb(t)
	called := false
	runOpenURL = func(string, string) error {
		called = true
		return nil
	}
	recorder := httptest.NewRecorder()
	web.handleOpenURL(recorder, httptest.NewRequest(http.MethodGet, "/api/open-url", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", recorder.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/open-url", strings.NewReader(`{"url":"https://example.com"}`))
	request.Header.Set("Content-Type", "text/plain")
	recorder = httptest.NewRecorder()
	web.handleOpenURL(recorder, request)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain status = %d, want 415", recorder.Code)
	}
	if called {
		t.Fatal("rejected requests must not reach the browser")
	}
}

func TestWebOpenURLReportsBrowserFailure(t *testing.T) {
	web := newOpenURLTestWeb(t)
	runOpenURL = func(string, string) error { return errors.New("no browser") }
	recorder := postOpenURL(t, web, `{"url":"https://example.com"}`, "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", recorder.Code, recorder.Body.String())
	}
}

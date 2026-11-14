package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveReviewCommand(t *testing.T) {
	for _, body := range []string{"@bot review", "@bot CR", "@bot review pr", "@bot 审查"} {
		if got := resolveCommand(body, true, "bot"); got != "review" {
			t.Fatalf("%q -> %q, want review", body, got)
		}
	}
}

func TestLoadReviewResult(t *testing.T) {
	dir := t.TempDir()
	raw := `{"verdict":"request_changes","summary":"needs a timeout","comments":[{"path":"a.py","line":12,"body":"blocks forever"}]}`
	if err := os.WriteFile(filepath.Join(dir, "review.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	result := loadReviewResult(dir, "fallback")
	if result.Verdict != "request_changes" || result.Summary != "needs a timeout" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Findings) != 1 || result.Findings[0].Line != 12 {
		t.Fatalf("findings = %+v", result.Findings)
	}

	missing := loadReviewResult(t.TempDir(), "agent text")
	if missing.Verdict != "comment" || missing.Summary != "agent text" {
		t.Fatalf("missing review.json -> %+v", missing)
	}
}

func TestInlineReviewComments(t *testing.T) {
	findings := []reviewFinding{
		{Path: "a.py", Line: 3, Body: "one"},
		{Path: "", Line: 4, Body: "no path"},
		{Path: "b.py", Line: 0, Body: "no line"},
		{Path: "c.py", Line: 9, Body: ""},
	}
	got := inlineReviewComments(findings)
	if len(got) != 1 || got[0].Path != "a.py" || got[0].Side != "RIGHT" {
		t.Fatalf("comments = %+v", got)
	}

	many := make([]reviewFinding, 0, 15)
	for i := 0; i < 15; i++ {
		many = append(many, reviewFinding{Path: "a.py", Line: i + 1, Body: "x"})
	}
	if got := inlineReviewComments(many); len(got) != 10 {
		t.Fatalf("inline comments = %d, want the 10 cap", len(got))
	}
}

func TestRenderReviewBody(t *testing.T) {
	app := &AppContext{AgentLogin: "bot"}
	body := renderReviewBody(app, reviewResult{
		Verdict: "request_changes",
		Summary: "This retry loop can spin forever.",
	})
	for _, want := range []string{"### 🤖 Review", "This retry loop can spin forever.", "⚠️ request changes", "@bot review"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
}

func TestFindingsFallback(t *testing.T) {
	if got := findingsFallback(nil); got != "" {
		t.Fatalf("fallback for no findings = %q", got)
	}
	got := findingsFallback([]reviewFinding{{Path: "a.py", Line: 7, Body: "line one\nline two"}})
	if !strings.Contains(got, "`a.py:7`") || strings.Contains(got, "\nline two") {
		t.Fatalf("fallback = %q", got)
	}
}

func TestCreatePRReview(t *testing.T) {
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/reviews") {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "state": "COMMENTED"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := gh.CreatePRReview(context.Background(), "acme/app", 5, "abc123", "body", "COMMENT", []PRReviewComment{
		{Path: "a.py", Line: 4, Side: "RIGHT", Body: "note"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := (*calls)[0]
	if got.method != http.MethodPost || got.path != "/repos/acme/app/pulls/5/reviews" {
		t.Fatalf("call = %+v", got)
	}
	var payload struct {
		Body     string            `json:"body"`
		Event    string            `json:"event"`
		CommitID string            `json:"commit_id"`
		Comments []PRReviewComment `json:"comments"`
	}
	if err := json.Unmarshal([]byte(got.body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.CommitID != "abc123" || payload.Event != "COMMENT" || payload.Body != "body" {
		t.Fatalf("payload = %+v", payload)
	}
	if len(payload.Comments) != 1 || payload.Comments[0].Path != "a.py" {
		t.Fatalf("comments = %+v", payload.Comments)
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// newCommentRouter builds a router whose fake API answers issue reads as
// closed, so any job HandleTrigger enqueues bails out before doing real work.
func newCommentRouter(t *testing.T, phase, blockedReason string) (*Router, *Store, *callLog) {
	t.Helper()
	store := &Store{file: filepath.Join(t.TempDir(), "state.json"), state: State{Version: 1, Repos: map[string]*RepoState{}}}
	store.UpdateIssue("acme/app", 5, func(st *IssueState) {
		st.Phase = phase
		st.BlockedReason = blockedReason
	})
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/issues/5"):
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "closed", "number": 5})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/issues/5/comments"):
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 99})
		default:
			w.WriteHeader(http.StatusOK)
		}
	})
	app := &AppContext{
		Repo:  RepoConfig{FullName: "acme/app", Approvers: []string{"owner"}, Language: "zh"},
		GH:    gh,
		Store: store,
		Log:   NewSilentLogger(),
	}
	router := NewRouter(&Config{PlanParallel: 1, ImplementParallel: 1},
		map[string]*AppContext{"acme/app": app}, "bot", store, NewSilentLogger())
	return router, store, calls
}

func commentTrigger(body, sender string) Trigger {
	return Trigger{
		Type:        "comment",
		IssueNumber: 5,
		CommentID:   7,
		CommentBody: body,
		Sender:      sender,
		Key:         "test-comment",
	}
}

// postedCommentBody returns the body of a comment the router posted on the
// issue, if any.
func postedCommentBody(t *testing.T, calls *callLog) (string, bool) {
	t.Helper()
	for _, call := range calls.snapshot() {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/issues/5/comments") {
			return call.body, true
		}
	}
	return "", false
}

func reactedWith(t *testing.T, calls *callLog, content string) bool {
	t.Helper()
	for _, call := range calls.snapshot() {
		if call.method == http.MethodPost && strings.Contains(call.path, "/reactions") &&
			strings.Contains(call.body, content) {
			return true
		}
	}
	return false
}

const failureQuote = "> ### 🤖 流程出错\n> ```\n> fetch base: git fetch failed (exit 128)\n> ```\n> \n> 回复 `@bot continue` 重试。"

// A quote-reply ending in "@bot continue" must retry the blocked task: the
// quoted failure notice is not the command, the author's own line is.
func TestQuoteReplyContinueRetriesBlocked(t *testing.T) {
	router, store, calls := newCommentRouter(t, "blocked", "fetch base: boom")
	router.HandleTrigger(context.Background(), "acme/app", commentTrigger(failureQuote+"\n\n@bot continue", "owner"))
	if got := store.Issue("acme/app", 5).Phase; got != "implementing" {
		t.Fatalf("phase = %q, want implementing", got)
	}
	if !reactedWith(t, calls, "eyes") {
		t.Fatalf("no 👀 acknowledgment was posted: %+v", calls.snapshot())
	}
}

// A question on a blocked issue gets the stored failure reason quoted back,
// and is not filed as retry notes.
func TestBlockedQuestionRepliesWithReason(t *testing.T) {
	router, store, calls := newCommentRouter(t, "blocked", "fetch base: boom")
	router.HandleTrigger(context.Background(), "acme/app", commentTrigger("@bot 为什么失败了？", "owner"))
	if got := store.Issue("acme/app", 5).Phase; got != "blocked" {
		t.Fatalf("phase = %q, want blocked", got)
	}
	if got := store.Issue("acme/app", 5).ExtraNotes; got != "" {
		t.Fatalf("ExtraNotes = %q, want empty", got)
	}
	body, ok := postedCommentBody(t, calls)
	if !ok {
		t.Fatal("no reply comment was posted")
	}
	if !strings.Contains(body, "fetch base: boom") || !strings.Contains(body, "continue") {
		t.Fatalf("reply = %q, want the failure reason and the retry hint", body)
	}
}

// An instruction on a blocked issue is filed as ExtraNotes for the retry.
func TestBlockedInstructionSavedAsNotes(t *testing.T) {
	router, store, calls := newCommentRouter(t, "blocked", "fetch base: boom")
	router.HandleTrigger(context.Background(), "acme/app", commentTrigger("@bot 顺便把超时也修了", "owner"))
	if got := store.Issue("acme/app", 5).ExtraNotes; got != "顺便把超时也修了" {
		t.Fatalf("ExtraNotes = %q, want the instruction", got)
	}
	body, ok := postedCommentBody(t, calls)
	if !ok {
		t.Fatal("no reply comment was posted")
	}
	if !strings.Contains(body, "记下了") {
		t.Fatalf("reply = %q, want the notes-saved acknowledgment", body)
	}
}

// A comment while work is running is acknowledged, not silently dropped.
func TestImplementingCommentAcknowledged(t *testing.T) {
	router, store, calls := newCommentRouter(t, "implementing", "")
	router.HandleTrigger(context.Background(), "acme/app", commentTrigger("@bot 先别急，还有个细节", "owner"))
	if got := store.Issue("acme/app", 5).Phase; got != "implementing" {
		t.Fatalf("phase = %q, want implementing", got)
	}
	body, ok := postedCommentBody(t, calls)
	if !ok {
		t.Fatal("no reply comment was posted")
	}
	if !strings.Contains(body, "还在处理中") {
		t.Fatalf("reply = %q, want the still-running acknowledgment", body)
	}
}

// A non-approver @-mention gets the 👀 acknowledgment and nothing else: no
// reply, no state change, no work.
func TestNonApproverMentionOnlyGetsEyes(t *testing.T) {
	router, store, calls := newCommentRouter(t, "blocked", "fetch base: boom")
	router.HandleTrigger(context.Background(), "acme/app", commentTrigger("@bot continue", "stranger"))
	if got := store.Issue("acme/app", 5).Phase; got != "blocked" {
		t.Fatalf("phase = %q, want blocked", got)
	}
	if _, ok := postedCommentBody(t, calls); ok {
		t.Fatal("a reply comment was posted for a non-approver")
	}
	if !reactedWith(t, calls, "eyes") {
		t.Fatalf("no 👀 acknowledgment was posted: %+v", calls.snapshot())
	}
}

// A comment that is only a quote has nothing new to answer: no reply text.
// (The approver still gets the 👀 "seen" reaction from the router.)
func TestQuoteOnlyCommentIgnored(t *testing.T) {
	router, store, calls := newCommentRouter(t, "blocked", "fetch base: boom")
	router.HandleTrigger(context.Background(), "acme/app", commentTrigger(failureQuote, "owner"))
	if got := store.Issue("acme/app", 5).Phase; got != "blocked" {
		t.Fatalf("phase = %q, want blocked", got)
	}
	if _, ok := postedCommentBody(t, calls); ok {
		t.Fatal("a reply comment was posted for a quote-only comment")
	}
}

// A comment on a cancelled task explains how to start over.
func TestCancelledCommentRepliesHowToRestart(t *testing.T) {
	router, store, calls := newCommentRouter(t, "cancelled", "")
	router.HandleTrigger(context.Background(), "acme/app", commentTrigger("@bot 重新做一遍", "owner"))
	if got := store.Issue("acme/app", 5).Phase; got != "cancelled" {
		t.Fatalf("phase = %q, want cancelled", got)
	}
	body, ok := postedCommentBody(t, calls)
	if !ok {
		t.Fatal("no reply comment was posted")
	}
	if !strings.Contains(body, "取消") {
		t.Fatalf("reply = %q, want the cancelled explanation", body)
	}
}

// A comment on a finished issue points at reopening instead of restarting.
func TestDoneCommentRepliesDone(t *testing.T) {
	router, _, calls := newCommentRouter(t, "done", "")
	router.HandleTrigger(context.Background(), "acme/app", commentTrigger("@bot 还有个小问题", "owner"))
	body, ok := postedCommentBody(t, calls)
	if !ok {
		t.Fatal("no reply comment was posted")
	}
	if !strings.Contains(body, "已经完成") {
		t.Fatalf("reply = %q, want the already-done reply", body)
	}
}

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"hello":"world"}`)
	mac := hmac.New(sha256.New, []byte("s3cret"))
	mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !verifySignature("s3cret", body, signature) {
		t.Fatal("valid signature rejected")
	}
	if verifySignature("s3cret", body, "sha256=deadbeef") {
		t.Fatal("bad signature accepted")
	}
	if verifySignature("s3cret", body, "") {
		t.Fatal("missing signature accepted")
	}
	if !verifySignature("", body, "") {
		t.Fatal("empty secret should skip verification")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	oldStateDir := stateDirOverride
	stateDirOverride = dir
	t.Cleanup(func() { stateDirOverride = oldStateDir })
	if err := ensureDirs(); err != nil {
		t.Fatal(err)
	}
	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	store.UpdateIssue("me/app", 7, func(state *IssueState) {
		state.Phase = "awaiting_approval"
		state.Plan = "do it"
		state.PlanCommentID = 42
	})
	store.MarkCommentProcessed("me/app", 7, 99)

	reloaded, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	issue := reloaded.Issue("me/app", 7)
	if issue.Phase != "awaiting_approval" || issue.PlanCommentID != 42 {
		t.Fatalf("unexpected issue: %+v", issue)
	}
	if !reloaded.IsCommentProcessed("me/app", 7, 99) {
		t.Fatal("comment should be marked processed")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal("state file missing")
	}
}

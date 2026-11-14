package main

import (
	"strings"
	"testing"
)

func TestEmbeddedPrompts(t *testing.T) {
	for _, name := range []string{"plan", "implement", "fix", "revise", "ci_fix"} {
		text, err := loadPrompt(name)
		if err != nil {
			t.Fatalf("loadPrompt(%s): %v", name, err)
		}
		if strings.TrimSpace(text) == "" {
			t.Fatalf("prompt %s is empty", name)
		}
		if !strings.Contains(text, "{{") {
			t.Fatalf("prompt %s lost its placeholders", name)
		}
	}
}

func TestImplementPromptIncludesOwnerNotes(t *testing.T) {
	text, err := renderPrompt("implement", map[string]string{"NOTES": "use an Android emulator"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "use an Android emulator") {
		t.Fatal("owner notes missing from the implement prompt")
	}
}

func TestRenderPromptReplacesPlaceholders(t *testing.T) {
	text, err := renderPrompt("fix", map[string]string{"WORKTREE": "/tmp/worktree"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, "{{WORKTREE}}") {
		t.Fatal("placeholder was not replaced")
	}
	if !strings.Contains(text, "/tmp/worktree") {
		t.Fatal("value missing from rendered prompt")
	}
}

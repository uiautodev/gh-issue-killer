package main

import (
	"strings"
	"testing"
)

func TestEmbeddedPrompts(t *testing.T) {
	for _, name := range []string{"plan", "implement", "fix", "revise"} {
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

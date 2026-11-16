package main

import (
	"strings"
	"testing"
)

func TestEmbeddedPrompts(t *testing.T) {
	for _, name := range []string{"plan", "implement", "fix", "revise", "ci_fix", "conflict_fix", "review", "modify_pr"} {
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

func TestPromptsRequireUIAutomationWithoutPinningATool(t *testing.T) {
	// The verification requirement must stay tool-agnostic: desktop apps and
	// repos without Playwright still need to prove a UI change.
	for _, name := range []string{"implement", "revise", "fix"} {
		text, err := loadPrompt(name)
		if err != nil {
			t.Fatalf("loadPrompt(%s): %v", name, err)
		}
		// Prompts are hand-wrapped; compare on collapsed whitespace.
		flat := strings.Join(strings.Fields(text), " ")
		if !strings.Contains(flat, "browser or desktop automation") {
			t.Errorf("%s prompt does not ask for browser or desktop automation", name)
		}
		for _, banned := range []string{"locator.screenshot", "with Playwright or cua-driver", "in a real browser with Playwright"} {
			if strings.Contains(flat, banned) {
				t.Errorf("%s prompt still pins the UI test to %q", name, banned)
			}
		}
	}

	plan, err := loadPrompt("plan")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(strings.Fields(plan), " "), "browser/desktop automation verification") {
		t.Error("plan prompt does not ask for UI automation verification")
	}

	modify, err := loadPrompt("modify_pr")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(strings.Fields(modify), " "), "browser or desktop automation") {
		t.Error("modify_pr prompt does not ask for UI automation verification")
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

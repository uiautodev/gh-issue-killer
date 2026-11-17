package main

import "testing"

func TestTrimToPlanDropsNarration(t *testing.T) {
	input := "I'll explore the repository structure and relevant code to build the plan.## 问题分析\n\n需要加一个按钮。\n\n## 风险\n无"
	want := "## 问题分析\n\n需要加一个按钮。\n\n## 风险\n无"
	if got := trimToPlan(input); got != want {
		t.Fatalf("trimToPlan = %q, want %q", got, want)
	}
}

func TestTrimToPlanDropsNarrationBeforeH1(t *testing.T) {
	input := "Looking around...\n\n# Fix plan\n\nDo it."
	want := "# Fix plan\n\nDo it."
	if got := trimToPlan(input); got != want {
		t.Fatalf("trimToPlan = %q, want %q", got, want)
	}
}

func TestTrimToPlanKeepsTextWithoutHeading(t *testing.T) {
	input := "  Just a short plan without headings.  "
	if got := trimToPlan(input); got != "Just a short plan without headings." {
		t.Fatalf("trimToPlan = %q", got)
	}
}

func TestTrimToPlanKeepsHeadingAtStart(t *testing.T) {
	input := "## 问题分析\n\n一句话。"
	if got := trimToPlan(input); got != input {
		t.Fatalf("trimToPlan = %q, want %q", got, input)
	}
}

func TestIsMarkdownHeading(t *testing.T) {
	for _, line := range []string{"# a", "## b", "### c", "#", "##"} {
		if !isMarkdownHeading(line) {
			t.Fatalf("%q should be a heading", line)
		}
	}
	for _, line := range []string{"#### d", "#tag", "plain", "", "  "} {
		if isMarkdownHeading(line) {
			t.Fatalf("%q should not be a heading", line)
		}
	}
}

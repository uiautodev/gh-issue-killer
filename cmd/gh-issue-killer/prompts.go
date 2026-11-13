package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gh-issue-killer/prompts"
)

// promptsDir returns a prompts/ directory that overrides the embedded copies,
// for local development. Empty when the binary should use the embedded files.
func promptsDir() string {
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "prompts")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "prompts")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

func loadPrompt(name string) (string, error) {
	if dir := promptsDir(); dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, name+".md")); err == nil {
			return string(data), nil
		}
	}
	data, err := prompts.FS.ReadFile(name + ".md")
	if err != nil {
		return "", fmt.Errorf("read prompt %s: %w", name, err)
	}
	return string(data), nil
}

func renderPrompt(name string, vars map[string]string) (string, error) {
	text, err := loadPrompt(name)
	if err != nil {
		return "", err
	}
	for key, value := range vars {
		text = strings.ReplaceAll(text, "{{"+key+"}}", value)
	}
	return text, nil
}

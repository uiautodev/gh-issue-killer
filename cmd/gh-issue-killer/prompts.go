package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	promptCacheMu sync.Mutex
	promptCache   = map[string]string{}
)

func promptsDir() string {
	if value := strings.TrimSpace(os.Getenv("GWIK_PROMPTS_DIR")); value != "" {
		return expandHome(value)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(cwd, "prompts")
		if info, statErr := os.Stat(candidate); statErr == nil && info.IsDir() {
			return candidate
		}
	}
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "prompts")
	}
	return "prompts"
}

func renderPrompt(name string, vars map[string]string) (string, error) {
	promptCacheMu.Lock()
	text, ok := promptCache[name]
	promptCacheMu.Unlock()
	if !ok {
		data, err := os.ReadFile(filepath.Join(promptsDir(), name+".md"))
		if err != nil {
			return "", fmt.Errorf("read prompt %s: %w", name, err)
		}
		text = string(data)
		promptCacheMu.Lock()
		promptCache[name] = text
		promptCacheMu.Unlock()
	}
	for key, value := range vars {
		text = strings.ReplaceAll(text, "{{"+key+"}}", value)
	}
	return text, nil
}

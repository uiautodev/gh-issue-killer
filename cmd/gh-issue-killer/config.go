package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var repoNameRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

type RepoConfig struct {
	FullName     string
	Owner        string
	Name         string
	Agent        string
	Model        string
	Approvers    []string
	WorktreeRoot string
}

type Config struct {
	File             string
	Repos            []RepoConfig
	Proxy            string
	PollInterval     time.Duration
	Webhook          bool
	WebhookHost      string
	WebhookPort      int
	MaxFixRounds     int
	MaxParallel      int
	PlanTimeout      time.Duration
	ImplementTimeout time.Duration
	Agent            string
	Model            string
	Approvers        []string
}

type repoOptions struct {
	Repo         string   `json:"repo"`
	Agent        string   `json:"agent"`
	Model        string   `json:"model"`
	Approvers    []string `json:"approvers"`
	WorktreeRoot string   `json:"worktreeRoot"`
}

type rawConfig struct {
	Repos               json.RawMessage `json:"repos"`
	Agent               string          `json:"agent"`
	Model               string          `json:"model"`
	Approvers           []string        `json:"approvers"`
	Proxy               string          `json:"proxy"`
	PollIntervalSec     int             `json:"pollIntervalSec"`
	Webhook             *bool           `json:"webhook"`
	WebhookHost         string          `json:"webhookHost"`
	WebhookPort         int             `json:"webhookPort"`
	WorktreeRoot        string          `json:"worktreeRoot"`
	MaxFixRounds        int             `json:"maxFixRounds"`
	MaxParallel         int             `json:"maxParallel"`
	PlanTimeoutMin      int             `json:"planTimeoutMin"`
	ImplementTimeoutMin int             `json:"implementTimeoutMin"`
}

func defaultConfigPath() string {
	if value := strings.TrimSpace(os.Getenv("GWIK_CONFIG")); value != "" {
		return value
	}
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, "config.json")
}

func normalizeRepos(raw json.RawMessage, defaults repoOptions) ([]RepoConfig, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("config.repos is required")
	}

	var entries []repoOptions
	var names []string
	if err := json.Unmarshal(raw, &names); err == nil {
		if len(names) == 0 {
			return nil, fmt.Errorf("config.repos is empty")
		}
		entries = make([]repoOptions, 0, len(names))
	} else {
		var list []repoOptions
		if err := json.Unmarshal(raw, &list); err == nil {
			entries = list
		} else {
			var keyed map[string]repoOptions
			if err := json.Unmarshal(raw, &keyed); err != nil {
				return nil, fmt.Errorf("config.repos must be an array or object: %w", err)
			}
			for name, opts := range keyed {
				opts.Repo = name
				entries = append(entries, opts)
			}
			// Stable order for logs/tests.
			sortRepoOptions(entries)
		}
	}
	if len(names) > 0 {
		for _, name := range names {
			entries = append(entries, repoOptions{Repo: name})
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("config.repos is empty")
	}

	seen := map[string]bool{}
	repos := make([]RepoConfig, 0, len(entries))
	for _, entry := range entries {
		fullName := strings.TrimSpace(entry.Repo)
		fullName = strings.TrimPrefix(fullName, "https://github.com/")
		fullName = strings.TrimSuffix(fullName, ".git")
		if !repoNameRe.MatchString(fullName) {
			return nil, fmt.Errorf("invalid repo name: %q", entry.Repo)
		}
		if seen[fullName] {
			return nil, fmt.Errorf("duplicate repo in config: %s", fullName)
		}
		seen[fullName] = true
		parts := strings.SplitN(fullName, "/", 2)

		agent := firstNonEmpty(entry.Agent, defaults.Agent, "opencode")
		model := firstNonEmpty(entry.Model, defaults.Model, "magpie/group/deepseek")
		approvers := entry.Approvers
		if len(approvers) == 0 {
			approvers = defaults.Approvers
		}
		if len(approvers) == 0 {
			approvers = []string{parts[0]}
		}
		repos = append(repos, RepoConfig{
			FullName:     fullName,
			Owner:        parts[0],
			Name:         parts[1],
			Agent:        agent,
			Model:        model,
			Approvers:    approvers,
			WorktreeRoot: firstNonEmpty(expandHome(entry.WorktreeRoot), defaults.WorktreeRoot),
		})
	}
	return repos, nil
}

func sortRepoOptions(entries []repoOptions) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j].Repo < entries[j-1].Repo; j-- {
			entries[j], entries[j-1] = entries[j-1], entries[j]
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func loadConfig(file string) (*Config, error) {
	if file == "" {
		file = defaultConfigPath()
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", file, err)
	}
	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", file, err)
	}

	cfg := &Config{
		File:             file,
		Proxy:            strings.TrimSpace(raw.Proxy),
		PollInterval:     time.Duration(raw.PollIntervalSec) * time.Second,
		Webhook:          true,
		WebhookHost:      firstNonEmpty(raw.WebhookHost, "127.0.0.1"),
		WebhookPort:      raw.WebhookPort,
		MaxFixRounds:     raw.MaxFixRounds,
		MaxParallel:      raw.MaxParallel,
		PlanTimeout:      time.Duration(raw.PlanTimeoutMin) * time.Minute,
		ImplementTimeout: time.Duration(raw.ImplementTimeoutMin) * time.Minute,
		Agent:            firstNonEmpty(raw.Agent, "opencode"),
		Model:            firstNonEmpty(raw.Model, "magpie/group/deepseek"),
		Approvers:        raw.Approvers,
	}
	if raw.Webhook != nil {
		cfg.Webhook = *raw.Webhook
	}
	if cfg.PollInterval < 10*time.Second {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.WebhookPort == 0 {
		cfg.WebhookPort = 8788
	}
	if cfg.MaxFixRounds == 0 {
		cfg.MaxFixRounds = 3
	}
	if cfg.MaxParallel == 0 {
		cfg.MaxParallel = 1
	}
	if cfg.PlanTimeout == 0 {
		cfg.PlanTimeout = 15 * time.Minute
	}
	if cfg.ImplementTimeout == 0 {
		cfg.ImplementTimeout = 60 * time.Minute
	}

	repos, err := normalizeRepos(raw.Repos, repoOptions{
		Agent:        cfg.Agent,
		Model:        cfg.Model,
		Approvers:    cfg.Approvers,
		WorktreeRoot: expandHome(raw.WorktreeRoot),
	})
	if err != nil {
		return nil, err
	}
	cfg.Repos = repos
	return cfg, nil
}

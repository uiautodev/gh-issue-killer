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

// codingAgentIDs are the coding-agent CLIs the daemon can drive. The config
// stores the stable ID, not a binary path, so it survives upgrades and PATH
// changes.
var codingAgentIDs = []string{"opencode", "claude", "codex"}

func isSupportedCodingAgent(id string) bool {
	for _, candidate := range codingAgentIDs {
		if strings.EqualFold(id, candidate) {
			return true
		}
	}
	return false
}

func supportedCodingAgentList() string {
	return strings.Join(codingAgentIDs, ", ")
}

type RepoConfig struct {
	FullName     string
	Owner        string
	Name         string
	CodingAgent  string
	Model        string
	Approvers    []string
	WorktreeRoot string
}

type Config struct {
	File                  string
	Repos                 []RepoConfig
	Proxy                 string
	PollInterval          time.Duration
	Webhook               bool
	MaxFixRounds          int
	PlanParallel          int
	ImplementParallel     int
	SelfUpdate            bool
	DeprecatedMaxParallel bool
	PlanTimeout           time.Duration
	ImplementTimeout      time.Duration
	CodingAgent           string
	Model                 string
	Approvers             []string
	DeprecatedAgent       string
}

type repoOptions struct {
	Repo         string   `json:"repo"`
	CodingAgent  string   `json:"codingAgent"`
	Model        string   `json:"model"`
	Approvers    []string `json:"approvers"`
	WorktreeRoot string   `json:"worktreeRoot"`
}

type rawConfig struct {
	Repos             json.RawMessage `json:"repos"`
	CodingAgent       string          `json:"codingAgent"`
	Model             string          `json:"model"`
	Approvers         []string        `json:"approvers"`
	Proxy             string          `json:"proxy"`
	PollIntervalSec   int             `json:"pollIntervalSec"`
	Webhook           *bool           `json:"webhook"`
	WorktreeRoot      string          `json:"worktreeRoot"`
	MaxFixRounds      int             `json:"maxFixRounds"`
	PlanParallel      int             `json:"planParallel"`
	ImplementParallel int             `json:"implementParallel"`
	SelfUpdate        bool            `json:"selfUpdate"`
	// MaxParallel is the old name for implementParallel.
	MaxParallel         int `json:"maxParallel"`
	PlanTimeoutMin      int `json:"planTimeoutMin"`
	ImplementTimeoutMin int `json:"implementTimeoutMin"`
	// Agent is the old opencode sub-agent field. It is kept only so the daemon
	// can warn about it once; the value is otherwise ignored.
	Agent string `json:"agent"`
}

func defaultConfigPath() string {
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

		agent := strings.ToLower(firstNonEmpty(entry.CodingAgent, defaults.CodingAgent, "opencode"))
		if !isSupportedCodingAgent(agent) {
			return nil, fmt.Errorf("repo %s: unsupported codingAgent %q (supported: %s)", fullName, agent, supportedCodingAgentList())
		}
		model := firstNonEmpty(entry.Model, defaults.Model)
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
			CodingAgent:  agent,
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

func normalizeRepoName(value string) (string, error) {
	name := strings.TrimSpace(value)
	name = strings.TrimPrefix(name, "https://github.com/")
	name = strings.TrimPrefix(name, "http://github.com/")
	name = strings.TrimSuffix(name, ".git")
	name = strings.Trim(name, "/")
	if !repoNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid repo %q, expected owner/repo", value)
	}
	return name, nil
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

	agentID := strings.ToLower(firstNonEmpty(raw.CodingAgent, "opencode"))
	if !isSupportedCodingAgent(agentID) {
		return nil, fmt.Errorf("unsupported codingAgent %q (supported: %s)", raw.CodingAgent, supportedCodingAgentList())
	}

	cfg := &Config{
		File:              file,
		Proxy:             strings.TrimSpace(raw.Proxy),
		PollInterval:      time.Duration(raw.PollIntervalSec) * time.Second,
		Webhook:           true,
		MaxFixRounds:      raw.MaxFixRounds,
		PlanParallel:      raw.PlanParallel,
		ImplementParallel: raw.ImplementParallel,
		SelfUpdate:        raw.SelfUpdate,
		PlanTimeout:       time.Duration(raw.PlanTimeoutMin) * time.Minute,
		ImplementTimeout:  time.Duration(raw.ImplementTimeoutMin) * time.Minute,
		CodingAgent:       agentID,
		Model:             strings.TrimSpace(raw.Model),
		Approvers:         raw.Approvers,
		DeprecatedAgent:   strings.TrimSpace(raw.Agent),
	}
	if raw.Webhook != nil {
		cfg.Webhook = *raw.Webhook
	}
	if cfg.PollInterval < 10*time.Second {
		cfg.PollInterval = 30 * time.Second
	}
	if cfg.MaxFixRounds == 0 {
		cfg.MaxFixRounds = 3
	}
	if raw.ImplementParallel == 0 && raw.MaxParallel != 0 {
		cfg.ImplementParallel = raw.MaxParallel
		cfg.DeprecatedMaxParallel = true
	}
	if cfg.PlanParallel == 0 {
		cfg.PlanParallel = 5
	}
	if cfg.ImplementParallel == 0 {
		cfg.ImplementParallel = 1
	}
	if cfg.PlanTimeout == 0 {
		cfg.PlanTimeout = 15 * time.Minute
	}
	if cfg.ImplementTimeout == 0 {
		cfg.ImplementTimeout = 60 * time.Minute
	}

	repos, err := normalizeRepos(raw.Repos, repoOptions{
		CodingAgent:  cfg.CodingAgent,
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

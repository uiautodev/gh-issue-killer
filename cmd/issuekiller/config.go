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
var codingAgentIDs = []string{"opencode", "claude", "codex", "pi", "omp"}

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

// Issue modes decide how a non-bug issue starts. `plan` always posts a plan
// comment and waits for approval, `build` implements immediately, and `auto`
// lets the coding agent triage each issue.
const (
	modePlan  = "plan"
	modeBuild = "build"
	modeAuto  = "auto"
)

var issueModes = []string{modePlan, modeBuild, modeAuto}

func isSupportedIssueMode(mode string) bool {
	for _, candidate := range issueModes {
		if mode == candidate {
			return true
		}
	}
	return false
}

func supportedIssueModeList() string {
	return strings.Join(issueModes, ", ")
}

// effectiveIssueMode treats an absent mode as auto, so hand-written configs
// from older versions keep working.
func effectiveIssueMode(value string) string {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		return modeAuto
	}
	return mode
}

type RepoConfig struct {
	FullName     string
	Owner        string
	Name         string
	CodingAgent  string
	Model        string
	Approvers    []string
	WorktreeRoot string
	Language     string
	Mode         string
	Release      ReleaseConfig
}

// ReleaseConfig is the opt-in periodic release for one repository. Publishing
// creates a public tag and release, so disabled is the default. The version
// part follows the conventional-commit shape of the batch: a feature bumps the
// minor part, everything else the patch part. A major release is a deliberate
// human decision and is never chosen automatically.
type ReleaseConfig struct {
	Enabled      bool
	IntervalDays int
	// Time is the local time of day a due release publishes at, HH:MM. The
	// default is 09:00 so a scheduled release lands at the start of a workday
	// instead of whenever the daemon happened to start.
	Time string
}

type Config struct {
	File                  string
	Repos                 []RepoConfig
	Proxy                 string
	UpdateProxy           string
	PollInterval          time.Duration
	Webhook               bool
	MaxFixRounds          int
	PlanParallel          int
	ImplementParallel     int
	SelfUpdate            bool
	FollowMaster          bool
	WebPort               int
	DeprecatedMaxParallel bool
	PlanTimeout           time.Duration
	ImplementTimeout      time.Duration
	CodingAgent           string
	Model                 string
	Approvers             []string
	DeprecatedAgent       string
	Language              string
	Mode                  string
}

type repoOptions struct {
	Repo         string         `json:"repo"`
	CodingAgent  string         `json:"codingAgent"`
	Model        string         `json:"model"`
	Approvers    []string       `json:"approvers"`
	WorktreeRoot string         `json:"worktreeRoot"`
	Language     string         `json:"language"`
	Mode         string         `json:"mode"`
	Release      releaseOptions `json:"release"`
}

// releaseOptions mirrors ReleaseConfig in the config file. intervalDays is a
// minimum gap between checks: the tick itself is hourly. time is the local
// HH:MM a release publishes at; empty means defaultReleaseTime. Older configs
// may still carry bugBump/featureBump; the decoder ignores them because the
// bump is conventional.
type releaseOptions struct {
	Enabled      bool   `json:"enabled"`
	IntervalDays int    `json:"intervalDays"`
	Time         string `json:"time"`
}

type rawConfig struct {
	Repos       json.RawMessage `json:"repos"`
	CodingAgent string          `json:"codingAgent"`
	Model       string          `json:"model"`
	Approvers   []string        `json:"approvers"`
	Proxy       string          `json:"proxy"`
	// UpdateProxy is a release-download mirror prefix (for example
	// https://gh-proxy.com) that is prepended to the GitHub asset URL when the
	// direct download fails. It is not an HTTP proxy: it only affects updates.
	UpdateProxy       string `json:"updateProxy"`
	PollIntervalSec   int    `json:"pollIntervalSec"`
	Webhook           *bool  `json:"webhook"`
	WorktreeRoot      string `json:"worktreeRoot"`
	MaxFixRounds      int    `json:"maxFixRounds"`
	PlanParallel      int    `json:"planParallel"`
	ImplementParallel int    `json:"implementParallel"`
	SelfUpdate        bool   `json:"selfUpdate"`
	FollowMaster      bool   `json:"followMaster"`
	WebPort           int    `json:"webPort"`
	// MaxParallel is the old name for implementParallel.
	MaxParallel         int `json:"maxParallel"`
	PlanTimeoutMin      int `json:"planTimeoutMin"`
	ImplementTimeoutMin int `json:"implementTimeoutMin"`
	// Agent is the old opencode sub-agent field. It is kept only so the daemon
	// can warn about it once; the value is otherwise ignored.
	Agent string `json:"agent"`
	// Language is the language the agent and the daemon's scaffolding write in.
	// Empty or "auto" follows the language of the issue or pull request.
	Language string `json:"language"`
	// Mode decides whether a non-bug issue starts with a plan comment. Empty
	// means auto: the coding agent triages each issue.
	Mode string `json:"mode"`
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
		// Build the entries inside this branch: a failed unmarshal into
		// []string still leaves a partially filled slice, so reading names
		// afterwards would append an empty repo for every object entry.
		for _, name := range names {
			entries = append(entries, repoOptions{Repo: name})
		}
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
		mode, err := normalizeIssueMode(firstNonEmpty(entry.Mode, defaults.Mode))
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", fullName, err)
		}
		release, err := normalizeRelease(entry.Release)
		if err != nil {
			return nil, fmt.Errorf("repo %s: %w", fullName, err)
		}
		repos = append(repos, RepoConfig{
			FullName:     fullName,
			Owner:        parts[0],
			Name:         parts[1],
			CodingAgent:  agent,
			Model:        model,
			Approvers:    approvers,
			WorktreeRoot: firstNonEmpty(expandHome(entry.WorktreeRoot), defaults.WorktreeRoot),
			Language:     firstNonEmpty(entry.Language, defaults.Language, "auto"),
			Mode:         mode,
			Release:      release,
		})
	}
	return repos, nil
}

// normalizeIssueMode lowercases a mode and rejects values the router would not
// understand. Empty means auto.
func normalizeIssueMode(value string) (string, error) {
	mode := effectiveIssueMode(value)
	if !isSupportedIssueMode(mode) {
		return "", fmt.Errorf("unsupported mode %q (supported: %s)", value, supportedIssueModeList())
	}
	return mode, nil
}

// defaultReleaseTime is the local publish time used when release.time is
// absent. 09:00 keeps a daily release inside the working day of most
// maintainers; a nightly schedule would land on a quiet repository.
const defaultReleaseTime = "09:00"

// normalizeRelease fills in the optional per-repo release block. One day
// between checks is the default; the bump part is not configurable because it
// follows the conventional commits in the batch. An unusable time is a load
// error instead of a silent fallback: a typo must not move a release by hours.
func normalizeRelease(opts releaseOptions) (ReleaseConfig, error) {
	cfg := ReleaseConfig{Enabled: opts.Enabled, IntervalDays: opts.IntervalDays}
	if cfg.IntervalDays < 1 {
		cfg.IntervalDays = 1
	}
	hour, minute, err := parseReleaseTime(opts.Time)
	if err != nil {
		return ReleaseConfig{}, err
	}
	cfg.Time = formatReleaseTime(hour, minute)
	return cfg, nil
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
	mode, err := normalizeIssueMode(raw.Mode)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		File:              file,
		Proxy:             strings.TrimSpace(raw.Proxy),
		UpdateProxy:       strings.TrimSpace(raw.UpdateProxy),
		PollInterval:      time.Duration(raw.PollIntervalSec) * time.Second,
		Webhook:           true,
		MaxFixRounds:      raw.MaxFixRounds,
		PlanParallel:      raw.PlanParallel,
		ImplementParallel: raw.ImplementParallel,
		SelfUpdate:        raw.SelfUpdate,
		FollowMaster:      raw.FollowMaster,
		WebPort:           raw.WebPort,
		PlanTimeout:       time.Duration(raw.PlanTimeoutMin) * time.Minute,
		ImplementTimeout:  time.Duration(raw.ImplementTimeoutMin) * time.Minute,
		CodingAgent:       agentID,
		Model:             strings.TrimSpace(raw.Model),
		Approvers:         raw.Approvers,
		DeprecatedAgent:   strings.TrimSpace(raw.Agent),
		Language:          firstNonEmpty(raw.Language, "auto"),
		Mode:              mode,
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
	if cfg.WebPort < 0 || cfg.WebPort > 65535 {
		cfg.WebPort = 0
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
		Language:     cfg.Language,
		Mode:         cfg.Mode,
	})
	if err != nil {
		return nil, err
	}
	cfg.Repos = repos
	return cfg, nil
}

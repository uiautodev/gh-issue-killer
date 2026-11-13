package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// errWizardNoInput is returned when the wizard is started without a usable
// stdin (for example from a script or a cron job).
var errWizardNoInput = errors.New("setup needs an interactive terminal (stdin is closed)")

// errWizardNoCodingAgent means no supported coding-agent CLI is installed.
var errWizardNoCodingAgent = errors.New("no supported coding agent found on PATH")

// wizardConfig is both the pre-filled state shown by the wizard and the
// values written to config.json. Unexported fields are ignored by JSON.
type wizardConfig struct {
	Repos               []string `json:"repos"`
	CodingAgent         string   `json:"codingAgent,omitempty"`
	Model               string   `json:"model,omitempty"`
	Approvers           []string `json:"approvers,omitempty"`
	Proxy               string   `json:"proxy,omitempty"`
	PollIntervalSec     int      `json:"pollIntervalSec,omitempty"`
	Webhook             *bool    `json:"webhook,omitempty"`
	WebhookPort         int      `json:"webhookPort,omitempty"`
	MaxFixRounds        int      `json:"maxFixRounds,omitempty"`
	MaxParallel         int      `json:"maxParallel,omitempty"`
	PlanTimeoutMin      int      `json:"planTimeoutMin,omitempty"`
	ImplementTimeoutMin int      `json:"implementTimeoutMin,omitempty"`
	WorktreeRoot        string   `json:"worktreeRoot,omitempty"`

	fromFile      bool
	hasAdvanced   bool
	advanced      bool
	oldAgent      string
	originalRepos map[string]repoOptions
	originalNames []string
}

type wizardUI struct {
	reader *bufio.Reader
	out    io.Writer
	color  bool
}

func newWizardUI(in io.Reader, out io.Writer) *wizardUI {
	color := false
	if file, ok := out.(*os.File); ok {
		color = isTerminal(file)
	}
	return &wizardUI{reader: bufio.NewReader(in), out: out, color: color}
}

func (w *wizardUI) printf(format string, args ...any) {
	fmt.Fprintf(w.out, format, args...)
}

func (w *wizardUI) ok(text string) string {
	if !w.color {
		return text
	}
	return "\x1b[32m" + text + "\x1b[0m"
}

func (w *wizardUI) warn(text string) string {
	if !w.color {
		return text
	}
	return "\x1b[33m" + text + "\x1b[0m"
}

func (w *wizardUI) bad(text string) string {
	if !w.color {
		return text
	}
	return "\x1b[31m" + text + "\x1b[0m"
}

func (w *wizardUI) heading(text string) string {
	if !w.color {
		return text
	}
	return "\x1b[36m" + text + "\x1b[0m"
}

// readLine returns one line without its terminator. A closed stdin yields
// errWizardNoInput instead of a generic EOF so callers can print something
// actionable.
func (w *wizardUI) readLine() (string, error) {
	line, err := w.reader.ReadString('\n')
	if err != nil {
		if !errors.Is(err, io.EOF) {
			return "", err
		}
		if strings.TrimSpace(line) == "" {
			return "", errWizardNoInput
		}
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (w *wizardUI) text(label, def string) (string, error) {
	if def != "" {
		w.printf("? %s [%s]: ", label, def)
	} else {
		w.printf("? %s: ", label)
	}
	line, err := w.readLine()
	if err != nil {
		return "", err
	}
	if value := strings.TrimSpace(line); value != "" {
		return value, nil
	}
	return def, nil
}

func (w *wizardUI) boolean(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		w.printf("? %s [%s]: ", label, hint)
		line, err := w.readLine()
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def, nil
		case "y", "yes", "是":
			return true, nil
		case "n", "no", "否":
			return false, nil
		default:
			w.printf("  please answer y or n\n")
		}
	}
}

func (w *wizardUI) integer(label string, def, min, max int) (int, error) {
	for {
		text, err := w.text(fmt.Sprintf("%s (%d-%d)", label, min, max), strconv.Itoa(def))
		if err != nil {
			return 0, err
		}
		value, convErr := strconv.Atoi(text)
		if convErr == nil && value >= min && value <= max {
			return value, nil
		}
		w.printf("  %q is not a number between %d and %d\n", text, min, max)
	}
}

// wizardDetectAgents and wizardDetectProxy are variables so tests can stub
// out PATH/network probing.
var wizardDetectAgents = detectCodingAgents
var wizardDetectProxy = detectLocalProxy
var wizardRepoCheck = checkRepoAccess
var wizardModelList = listCodingAgentModels
var wizardProxyCheck = verifyProxyReachable

func findDetectedAgent(detected []DetectedCodingAgent, id string) (int, bool) {
	for index, agent := range detected {
		if strings.EqualFold(agent.Info.ID, id) || strings.EqualFold(agent.Info.Binary, id) {
			return index, true
		}
	}
	return 0, false
}

// codingAgent asks which installed coding-agent CLI to use.
func (w *wizardUI) codingAgent(current string, detected []DetectedCodingAgent) (string, error) {
	if len(detected) == 0 {
		w.printf("No supported coding agent found on PATH.\n\n")
		w.printf("Install one of:\n")
		for _, info := range codingAgentRegistry {
			w.printf("  %-10s %s\n", info.Binary, info.InstallHint)
		}
		return "", errWizardNoCodingAgent
	}

	w.printf("%s\n", w.heading("Detected coding agents / 检测到的编码代理:"))
	for index, agent := range detected {
		version := agent.Version
		if version == "" {
			version = "version unknown"
		}
		marker := " "
		if strings.EqualFold(agent.Info.ID, current) {
			marker = "*"
		}
		w.printf(" %s %d) %-12s %-26s %s\n", marker, index+1, agent.Info.Name, version, agent.Path)
	}

	defaultIndex := 0
	if index, ok := findDetectedAgent(detected, current); ok {
		defaultIndex = index
	} else if current != "" {
		w.printf("  configured codingAgent %q is not installed; defaulting to %s\n", current, detected[0].Info.ID)
	}
	if len(detected) == 1 {
		use, err := w.boolean(fmt.Sprintf("Use %s?", detected[0].Info.Name), true)
		if err != nil {
			return "", err
		}
		if !use {
			return "", errWizardNoCodingAgent
		}
		return detected[0].Info.ID, nil
	}

	for {
		text, err := w.text("Use which coding agent? / 选择编码代理", detected[defaultIndex].Info.ID)
		if err != nil {
			return "", err
		}
		trimmed := strings.ToLower(strings.TrimSpace(text))
		if number, convErr := strconv.Atoi(trimmed); convErr == nil {
			if number >= 1 && number <= len(detected) {
				return detected[number-1].Info.ID, nil
			}
		}
		if index, ok := findDetectedAgent(detected, trimmed); ok {
			return detected[index].Info.ID, nil
		}
		w.printf("  choose a number between 1 and %d\n", len(detected))
	}
}

// reposMultiLine reads one owner/repo per line until an empty line. Pressing
// Enter on the first line keeps the current list.
func (w *wizardUI) reposMultiLine(current []string) ([]string, error) {
	w.printf("\n%s\n", w.heading("Repos to watch / 监控仓库"))
	if len(current) > 0 {
		w.printf("Current / 当前:\n")
		for index, repo := range current {
			w.printf("  %d) %s\n", index+1, repo)
		}
	}
	w.printf("One owner/repo per line, empty line to finish; full GitHub URLs are accepted.\n")
	w.printf("一行一个，空行结束；也支持粘贴完整 GitHub URL。\n")
	if len(current) > 0 {
		w.printf("Press Enter immediately to keep the current list. / 直接回车保留当前列表。\n")
	}

	for {
		lines := make([]string, 0, len(current))
		for {
			w.printf("> ")
			line, err := w.readLine()
			if err != nil {
				return nil, err
			}
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				if len(lines) == 0 {
					if len(current) > 0 {
						return current, nil
					}
					w.printf("  at least one repo is required / 至少需要一个仓库\n")
					continue
				}
				break
			}
			if strings.EqualFold(trimmed, "clear") || trimmed == "-" {
				lines = lines[:0]
				w.printf("  list cleared\n")
				continue
			}
			lines = append(lines, trimmed)
		}

		repos, err := parseRepoLines(lines)
		if err != nil {
			w.printf("  %v\n", err)
			continue
		}
		w.printf("  %d repos: %s\n", len(repos), strings.Join(repos, ", "))
		return repos, nil
	}
}

// proxy asks for an optional proxy, offering a detected local proxy first.
func (w *wizardUI) proxy(def string) (string, error) {
	if def == "" {
		if detected := wizardDetectProxy(); detected != "" {
			w.printf("\nDetected a working local proxy / 检测到可用代理: %s\n", detected)
			use, err := w.boolean("Use it? / 使用它？", true)
			if err != nil {
				return "", err
			}
			if use {
				return detected, nil
			}
		}
	}

	for {
		text, err := w.text("Proxy (socks5h://host:port, http(s)://host:port, or none)", firstNonEmpty(def, "none"))
		if err != nil {
			return "", err
		}
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "", "none", "direct", "off", "-":
			return "", nil
		}
		proxy, parseErr := parseProxy(text)
		if parseErr != nil {
			w.printf("  %v\n", parseErr)
			continue
		}
		w.printf("  checking %s ... ", proxy.Raw)
		if checkErr := wizardProxyCheck(proxy); checkErr != nil {
			w.printf("failed\n  %v\n", checkErr)
			keep, err := w.boolean("  Keep this proxy anyway", false)
			if err != nil {
				return "", err
			}
			if keep {
				return proxy.Raw, nil
			}
			continue
		}
		w.printf("ok\n")
		return proxy.Raw, nil
	}
}

func detectLocalProxy() string {
	for _, candidate := range []string{
		"socks5h://127.0.0.1:10808",
		"socks5://127.0.0.1:1080",
		"http://127.0.0.1:7890",
		"http://127.0.0.1:8080",
	} {
		proxy, err := parseProxy(candidate)
		if err != nil {
			continue
		}
		if wizardProxyCheck(proxy) == nil {
			return candidate
		}
	}
	return ""
}

// model asks for an optional model; "?" lists what the runner knows about.
func (w *wizardUI) model(codingAgent, def string) (string, error) {
	for {
		text, err := w.text(fmt.Sprintf("Model for %s (empty = default, ? = list)", codingAgent), def)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) == "?" {
			models := wizardModelList(codingAgent)
			if len(models) == 0 {
				w.printf("  no model list available; leave empty to use the %s default\n", codingAgent)
				continue
			}
			w.printf("  available models:\n")
			for index, model := range models {
				if index >= 80 {
					w.printf("  ...\n")
					break
				}
				w.printf("    %s\n", model)
			}
			continue
		}
		return text, nil
	}
}

func listCodingAgentModels(codingAgent string) []string {
	switch codingAgent {
	case "opencode":
		result, err := runCmd(context.Background(), "opencode", []string{"models"}, CmdOptions{
			Timeout:      10 * time.Second,
			AllowFailure: true,
		})
		if err != nil || result.Code != 0 {
			return nil
		}
		lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
		models := make([]string, 0, len(lines))
		for _, line := range lines {
			if value := strings.TrimSpace(line); value != "" {
				models = append(models, value)
			}
		}
		return models
	case "claude":
		return []string{"sonnet", "opus", "haiku"}
	case "codex":
		data, err := os.ReadFile(filepath.Join(homeDir(), ".codex", "config.toml"))
		if err != nil {
			return nil
		}
		matches := regexp.MustCompile(`(?m)^\s*model\s*=\s*"([^"]+)"`).FindAllStringSubmatch(string(data), -1)
		models := make([]string, 0, len(matches))
		for _, match := range matches {
			models = append(models, match[1])
		}
		return models
	}
	return nil
}

// checkRepoAccess verifies gh-agent's role and webhook capability per repo.
func checkRepoAccess(proxy *Proxy, repos []string) ([]repoAccessInfo, error) {
	gh, err := NewGHWithTimeout(proxy, NewSilentLogger(), 20*time.Second)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	results := make([]repoAccessInfo, 0, len(repos))
	for _, repo := range repos {
		info, err := gh.GetRepo(ctx, repo)
		if err != nil {
			results = append(results, repoAccessInfo{Repo: repo, Permission: "none", Err: err.Error()})
			continue
		}
		permission := "none"
		switch {
		case info.Permissions.Admin:
			permission = "admin"
		case info.Permissions.Push:
			permission = "write"
		case info.Permissions.Triage:
			permission = "triage"
		case info.Permissions.Pull:
			permission = "read"
		}
		hooks, hookErr := gh.ListHooks(ctx, repo)
		results = append(results, repoAccessInfo{
			Repo:         repo,
			Permission:   permission,
			WebhookReady: hookErr == nil && hooks != nil,
		})
	}
	return results, nil
}

type repoAccessInfo struct {
	Repo         string
	Permission   string
	WebhookReady bool
	Err          string
}

func (w *wizardUI) printAccess(results []repoAccessInfo) bool {
	w.printf("\n%s\n", w.heading("Checking gh-agent access / 检查权限…"))
	problem := false
	for _, result := range results {
		icon := w.ok("✓")
		note := ""
		switch result.Permission {
		case "admin":
			if result.WebhookReady {
				note = "admin (webhook ready)"
			} else {
				note = "admin (webhook scope missing, polling only)"
			}
		case "write":
			note = "write (polling only)"
		case "triage":
			note = "triage (cannot push/merge)"
			problem = true
		case "read":
			note = "read (cannot push/merge)"
			problem = true
		default:
			icon = w.bad("✗")
			note = "no access"
			problem = true
		}
		if result.Err != "" {
			note += " — " + tailString(result.Err, 2)
		}
		w.printf("  %s %-42s %s\n", icon, result.Repo, note)
	}
	if problem {
		w.printf("  %s\n", w.warn("Some repos need a write/admin role for the full workflow."))
		keep, err := w.boolean("Continue anyway? / 仍然继续？", false)
		if err != nil {
			return false
		}
		return keep
	}
	return true
}

func (w *wizardUI) printSummary(file string, answers wizardConfig, access []repoAccessInfo) {
	exists := false
	if _, err := os.Stat(file); err == nil {
		exists = true
	}
	status := "new"
	if exists {
		status = "replace"
	}

	agent := answers.CodingAgent
	if agent == "" || agent == "opencode" {
		agent += " (default)"
	}
	model := answers.Model
	if model == "" {
		model = "coding agent default"
	}
	approvers := answers.Approvers
	if len(approvers) == 0 {
		approvers = repoOwners(answers.Repos)
	}
	proxy := answers.Proxy
	if proxy == "" {
		proxy = "direct"
	}
	webhook := "enabled (polling fallback)"
	if answers.Webhook != nil && !*answers.Webhook {
		webhook = "disabled (polling only)"
	}

	w.printf("\n%s\n", w.heading("Summary / 配置摘要"))
	w.printf("  Config:       %s (%s)\n", file, status)
	w.printf("  Logs:         %s (rotate %d MB, keep %d)\n", daemonLogPath(), logRotateSizeMB, logRotateKeep)
	w.printf("  Coding agent: %s\n", agent)
	w.printf("  Repos:        %s\n", strings.Join(answers.Repos, ", "))
	if len(access) > 0 {
		notes := make([]string, 0, len(access))
		for _, item := range access {
			notes = append(notes, item.Repo+" "+item.Permission)
		}
		w.printf("  Access:       %s\n", strings.Join(notes, ", "))
	}
	w.printf("  Model:        %s\n", model)
	w.printf("  Approvers:    %s\n", strings.Join(approvers, ", "))
	w.printf("  Proxy:        %s\n", proxy)
	w.printf("  Webhook:      %s\n", webhook)
}

type repoOverride struct {
	CodingAgent  string   `json:"codingAgent,omitempty"`
	Model        string   `json:"model,omitempty"`
	Approvers    []string `json:"approvers,omitempty"`
	WorktreeRoot string   `json:"worktreeRoot,omitempty"`
}

func hasRepoOverride(options repoOptions) bool {
	return firstNonEmpty(options.CodingAgent, options.Model, options.WorktreeRoot) != "" || len(options.Approvers) > 0
}

func buildRepoOutput(names []string, original map[string]repoOptions) any {
	if len(original) == 0 {
		return names
	}
	output := make(map[string]repoOverride, len(names))
	hasOverrides := false
	for _, name := range names {
		if options, ok := original[name]; ok && hasRepoOverride(options) {
			output[name] = repoOverride{
				CodingAgent:  options.CodingAgent,
				Model:        options.Model,
				Approvers:    options.Approvers,
				WorktreeRoot: options.WorktreeRoot,
			}
			hasOverrides = true
		} else {
			output[name] = repoOverride{}
		}
	}
	if !hasOverrides {
		return names
	}
	return output
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func buildWizardJSON(answers wizardConfig) ([]byte, error) {
	output := map[string]any{
		"repos": buildRepoOutput(answers.Repos, answers.originalRepos),
	}
	if answers.CodingAgent != "" && answers.CodingAgent != "opencode" {
		output["codingAgent"] = answers.CodingAgent
	}
	if answers.Model != "" {
		output["model"] = answers.Model
	}
	if len(answers.Approvers) > 0 && !sameStrings(answers.Approvers, repoOwners(answers.Repos)) {
		output["approvers"] = answers.Approvers
	}
	if answers.Proxy != "" {
		output["proxy"] = answers.Proxy
	}
	if answers.PollIntervalSec != 0 && answers.PollIntervalSec != 30 {
		output["pollIntervalSec"] = answers.PollIntervalSec
	}
	if answers.Webhook != nil && !*answers.Webhook {
		output["webhook"] = false
	}
	if answers.WebhookPort != 0 && answers.WebhookPort != 8788 {
		output["webhookPort"] = answers.WebhookPort
	}
	if answers.MaxFixRounds != 0 && answers.MaxFixRounds != 3 {
		output["maxFixRounds"] = answers.MaxFixRounds
	}
	if answers.MaxParallel != 0 && answers.MaxParallel != 1 {
		output["maxParallel"] = answers.MaxParallel
	}
	if answers.PlanTimeoutMin != 0 && answers.PlanTimeoutMin != 15 {
		output["planTimeoutMin"] = answers.PlanTimeoutMin
	}
	if answers.ImplementTimeoutMin != 0 && answers.ImplementTimeoutMin != 60 {
		output["implementTimeoutMin"] = answers.ImplementTimeoutMin
	}
	if answers.WorktreeRoot != "" {
		output["worktreeRoot"] = answers.WorktreeRoot
	}
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func parseRepoLines(lines []string) ([]string, error) {
	fields := make([]string, 0, len(lines))
	for _, line := range lines {
		parts := strings.FieldsFunc(line, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\t'
		})
		fields = append(fields, parts...)
	}
	if len(fields) == 0 {
		return nil, errors.New("at least one owner/repo is required")
	}
	seen := map[string]bool{}
	repos := make([]string, 0, len(fields))
	for _, field := range fields {
		name, err := normalizeRepoName(field)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(name)
		if seen[key] {
			return nil, fmt.Errorf("duplicate repo: %s", name)
		}
		seen[key] = true
		repos = append(repos, name)
	}
	return repos, nil
}

func parseRepoList(text string) ([]string, error) {
	return parseRepoLines([]string{text})
}

func parseLogins(text string) []string {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "", "none", "-":
		return nil
	}
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
	seen := map[string]bool{}
	logins := make([]string, 0, len(fields))
	for _, field := range fields {
		login := strings.TrimPrefix(strings.TrimSpace(field), "@")
		switch strings.ToLower(login) {
		case "", "none", "-":
			continue
		}
		if seen[login] {
			continue
		}
		seen[login] = true
		logins = append(logins, login)
	}
	return logins
}

func (w *wizardUI) logins(label string, def []string) ([]string, error) {
	text, err := w.text(label, strings.Join(def, ", "))
	if err != nil {
		return nil, err
	}
	return parseLogins(text), nil
}

func repoOwners(repos []string) []string {
	seen := map[string]bool{}
	owners := make([]string, 0, len(repos))
	for _, name := range repos {
		owner, _, ok := strings.Cut(name, "/")
		if !ok || owner == "" || seen[owner] {
			continue
		}
		seen[owner] = true
		owners = append(owners, owner)
	}
	return owners
}

// loadWizardDefaults pre-fills the wizard from an existing config so re-running
// it edits values instead of resetting them.
func loadWizardDefaults(file string) wizardConfig {
	webhook := true
	defaults := wizardConfig{
		CodingAgent:         "opencode",
		PollIntervalSec:     30,
		Webhook:             &webhook,
		WebhookPort:         8788,
		MaxFixRounds:        3,
		MaxParallel:         1,
		PlanTimeoutMin:      15,
		ImplementTimeoutMin: 60,
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return defaults
	}
	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return defaults
	}

	defaults.fromFile = true
	defaults.Repos = rawRepoNames(raw.Repos)
	defaults.CodingAgent = strings.ToLower(firstNonEmpty(raw.CodingAgent, "opencode"))
	defaults.Model = strings.TrimSpace(raw.Model)
	defaults.Approvers = raw.Approvers
	defaults.Proxy = strings.TrimSpace(raw.Proxy)
	defaults.oldAgent = strings.TrimSpace(raw.Agent)
	if raw.PollIntervalSec >= 10 {
		defaults.PollIntervalSec = raw.PollIntervalSec
	}
	if raw.Webhook != nil {
		defaults.Webhook = raw.Webhook
	}
	if raw.WebhookPort > 0 {
		defaults.WebhookPort = raw.WebhookPort
	}
	if raw.MaxFixRounds > 0 {
		defaults.MaxFixRounds = raw.MaxFixRounds
	}
	if raw.MaxParallel > 0 {
		defaults.MaxParallel = raw.MaxParallel
	}
	if raw.PlanTimeoutMin > 0 {
		defaults.PlanTimeoutMin = raw.PlanTimeoutMin
	}
	if raw.ImplementTimeoutMin > 0 {
		defaults.ImplementTimeoutMin = raw.ImplementTimeoutMin
	}
	defaults.WorktreeRoot = strings.TrimSpace(raw.WorktreeRoot)
	defaults.hasAdvanced = raw.PollIntervalSec != 0 || raw.Webhook != nil || raw.WebhookPort != 0 ||
		raw.MaxFixRounds != 0 || raw.MaxParallel != 0 || raw.PlanTimeoutMin != 0 ||
		raw.ImplementTimeoutMin != 0 || defaults.WorktreeRoot != ""

	var keyed map[string]repoOptions
	if err := json.Unmarshal(raw.Repos, &keyed); err == nil && len(keyed) > 0 {
		defaults.originalRepos = keyed
		defaults.originalNames = make([]string, 0, len(keyed))
		for name := range keyed {
			defaults.originalNames = append(defaults.originalNames, name)
		}
		sort.Strings(defaults.originalNames)
	}
	return defaults
}

func rawRepoNames(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err == nil {
		return names
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err != nil {
		return nil
	}
	names = make([]string, 0, len(keyed))
	for name := range keyed {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	// /dev/null is a character device too; never treat it as interactive.
	if null, err := os.Stat(os.DevNull); err == nil && os.SameFile(info, null) {
		return false
	}
	return true
}

// verifyProxyReachable makes one short HTTPS request through the proxy. Only
// an HTTP answer below 400 counts as reachable; dial/TLS errors and error
// statuses (including proxy auth failures) mean the proxy is unusable.
func verifyProxyReachable(proxy *Proxy) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/", nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", "gh-issue-killer-setup")
	response, err := newHTTPClient(proxy, 10*time.Second).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.CopyN(io.Discard, response.Body, 4096)
	if response.StatusCode >= 400 {
		return fmt.Errorf("api.github.com answered HTTP %d through the proxy", response.StatusCode)
	}
	return nil
}

// runSetupWizard asks the handful of questions needed to write a config file,
// shows a summary, and only writes it after confirmation.
func runSetupWizard(file string, in io.Reader, out io.Writer) error {
	if file == "" {
		file = defaultConfigPath()
	}
	ui := newWizardUI(in, out)
	answers := loadWizardDefaults(file)

	ui.printf("gh-issue-killer · config wizard\n\n")
	ui.printf("Config file / 配置文件: %s\n", file)
	if answers.fromFile {
		ui.printf("Editing existing config — press Enter to keep the current value.\n")
		ui.printf("编辑现有配置，回车保留当前值。\n")
	} else {
		ui.printf("Press Enter to accept the value in [brackets].\n")
		ui.printf("回车接受方括号中的默认值。\n")
	}
	if answers.oldAgent != "" {
		ui.printf("%s\n", ui.warn("Note: the old `agent` field is ignored; choose the coding agent below."))
	}
	ui.printf("\n")

	detected := wizardDetectAgents()
	agentID, err := ui.codingAgent(answers.CodingAgent, detected)
	if err != nil {
		return err
	}
	answers.CodingAgent = agentID

	repos, err := ui.reposMultiLine(answers.Repos)
	if err != nil {
		return err
	}
	answers.Repos = repos

	proxyRaw, err := ui.proxy(answers.Proxy)
	if err != nil {
		return err
	}
	answers.Proxy = proxyRaw
	proxyObject, err := parseProxy(proxyRaw)
	if err != nil {
		return err
	}

	var access []repoAccessInfo
	access, err = wizardRepoCheck(proxyObject, answers.Repos)
	if err != nil {
		ui.printf("\n! Could not check gh-agent access: %v\n", err)
		ui.printf("  Set GH_AGENT_TOKEN or run: GH_CONFIG_DIR=~/.config/gh-agent gh auth login\n")
	} else if !ui.printAccess(access) {
		ui.printf("Nothing written.\n")
		return nil
	}

	if answers.Model, err = ui.model(answers.CodingAgent, answers.Model); err != nil {
		return err
	}

	approverDefault := answers.Approvers
	if len(approverDefault) == 0 {
		approverDefault = repoOwners(answers.Repos)
	}
	if answers.Approvers, err = ui.logins("Approvers / 审批人 (none = repo owner)", approverDefault); err != nil {
		return err
	}

	if answers.advanced, err = ui.boolean("Configure advanced options / 配置高级选项", answers.hasAdvanced); err != nil {
		return err
	}
	if answers.advanced {
		if answers.PollIntervalSec, err = ui.integer("Poll interval when webhooks are unavailable, seconds", answers.PollIntervalSec, 10, 86400); err != nil {
			return err
		}
		webhook, err := ui.boolean("Use real-time GitHub webhooks (falls back to polling)", boolValue(answers.Webhook, true))
		if err != nil {
			return err
		}
		answers.Webhook = &webhook
		if answers.WebhookPort, err = ui.integer("Local webhook relay port", answers.WebhookPort, 1, 65535); err != nil {
			return err
		}
		if answers.MaxFixRounds, err = ui.integer("Fix rounds after failing checks", answers.MaxFixRounds, 1, 20); err != nil {
			return err
		}
		if answers.MaxParallel, err = ui.integer("Issues handled in parallel", answers.MaxParallel, 1, 16); err != nil {
			return err
		}
		if answers.PlanTimeoutMin, err = ui.integer("Plan timeout, minutes", answers.PlanTimeoutMin, 1, 600); err != nil {
			return err
		}
		if answers.ImplementTimeoutMin, err = ui.integer("Implement timeout, minutes", answers.ImplementTimeoutMin, 1, 1440); err != nil {
			return err
		}
		if answers.WorktreeRoot, err = ui.text("Worktree root (empty = ~/.gh-issue-killer/worktrees)", answers.WorktreeRoot); err != nil {
			return err
		}
	} else {
		answers.PollIntervalSec = 0
		answers.Webhook = nil
		answers.WebhookPort = 0
		answers.MaxFixRounds = 0
		answers.MaxParallel = 0
		answers.PlanTimeoutMin = 0
		answers.ImplementTimeoutMin = 0
		answers.WorktreeRoot = ""
	}

	ui.printSummary(file, answers, access)
	data, err := buildWizardJSON(answers)
	if err != nil {
		return err
	}

	for {
		choice, err := ui.text("Write / 写入? (y/n/d = show JSON)", "y")
		if err != nil {
			return err
		}
		switch strings.ToLower(strings.TrimSpace(choice)) {
		case "", "y", "yes", "是":
			if err := atomicWriteFile(file, data); err != nil {
				return err
			}
			if _, err := loadConfig(file); err != nil {
				ui.printf("warning: config written but it does not load: %v\n", err)
				return nil
			}
			ui.printf("\nWrote %s\n\n", file)
			ui.printf("Next steps / 下一步:\n")
			ui.printf("  gh-issue-killer -once        # verify the setup and run one poll\n")
			ui.printf("  gh-issue-killer -foreground  # run the daemon attached to this terminal\n")
			return nil
		case "d", "json":
			ui.printf("\n%s\n", string(data))
		case "n", "no", "q", "quit", "否":
			ui.printf("Nothing written.\n")
			return nil
		default:
			ui.printf("  choose y, n, or d\n")
		}
	}
}

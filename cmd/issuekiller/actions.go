package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type AppContext struct {
	Cfg      *Config
	Repo     RepoConfig
	RepoInfo RepoInfo
	Local    *LocalRepo
	Identity User
	Token    string
	Proxy    *Proxy
	GH       *GH
	Store    *Store
	Log      *Logger
	// AgentLogin is the login of the bot account running the daemon.
	AgentLogin string
	// EnqueueCI schedules the post-push CI wait on the router's CI queue so the
	// implementation slot is released immediately. Nil when there is no router
	// (tests).
	EnqueueCI func(number int)
}

// Env rebuilds the child-process environment with the current token. The bot
// token rotates (GH refreshes it on 401), so an environment captured at
// startup would authenticate git and gh with an expired token. GH is the
// live source in production; Token is the startup snapshot, kept only as a
// fallback for contexts without a client (tests).
func (a *AppContext) Env() []string {
	token := a.Token
	if a.GH != nil {
		token = a.GH.currentToken()
	}
	overrides := goProxyEnv(a.Proxy)
	// Agents and their tools may call gh directly. Give them the bot token
	// instead of a separate config directory, so they can never fall back to
	// the human account.
	overrides["GH_TOKEN"] = token
	overrides["GITHUB_TOKEN"] = token
	return mergeEnv(buildGitEnv(a.Proxy, token, a.Identity), overrides)
}

// projectURL is shown on plan comments so readers can find the project.
const projectURL = "https://github.com/uiautodev/issuekiller"

type Trigger struct {
	Type        string
	IssueNumber int
	CommentID   int64
	CommentBody string
	Sender      string
	Key         string
	DeliveryID  string
	// Reprocess allows an edited comment that already ran to run again, so
	// adding the mention after the fact still reaches the agent.
	Reprocess bool
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(gho|ghp|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._-]{15,}`),
}

func sanitize(text string) string {
	out := text
	for _, pattern := range secretPatterns {
		out = pattern.ReplaceAllString(out, "[REDACTED]")
	}
	return out
}

// trimToPlan drops any narration printed before the plan itself. The plan
// starts at the first Markdown heading, even when the agent glued it to the end
// of a sentence ("...build the plan.## 问题分析"); without a heading the text is
// kept as is.
func trimToPlan(text string) string {
	trimmed := strings.TrimSpace(text)
	lines := strings.Split(trimmed, "\n")
	for i, line := range lines {
		if isMarkdownHeading(line) {
			if i == 0 {
				break
			}
			return strings.TrimSpace(strings.Join(lines[i:], "\n"))
		}
		if heading, ok := midLineHeading(line); ok {
			return strings.TrimSpace(strings.Join(append([]string{heading}, lines[i+1:]...), "\n"))
		}
	}
	return trimmed
}

var midLineHeadingRe = regexp.MustCompile(`[.。:：!?！？]\s*(#{1,3}[ \t])`)

func midLineHeading(line string) (string, bool) {
	match := midLineHeadingRe.FindStringSubmatchIndex(line)
	if match == nil {
		return "", false
	}
	candidate := line[match[2]:]
	if !isMarkdownHeading(candidate) {
		return "", false
	}
	return strings.TrimSpace(candidate), true
}

func isMarkdownHeading(line string) bool {
	line = strings.TrimSpace(line)
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level < 1 || level > 3 {
		return false
	}
	return level == len(line) || line[level] == ' '
}

func stripFences(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") && strings.HasSuffix(trimmed, "```") {
		lines := strings.Split(trimmed, "\n")
		if len(lines) >= 3 {
			return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
		}
	}
	return trimmed
}

func commentsBlock(comments []Comment) string {
	if len(comments) == 0 {
		return "(no comments)"
	}
	parts := make([]string, 0, len(comments))
	for _, comment := range comments {
		parts = append(parts, fmt.Sprintf("--- @%s at %s:\n%s", comment.User.Login, comment.CreatedAt.Format(time.RFC3339), comment.Body))
	}
	return strings.Join(parts, "\n\n")
}

func issueMeta(issue Issue) *IssueMeta {
	labels := make([]string, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, label.Name)
	}
	return &IssueMeta{
		Number: issue.Number,
		Title:  issue.Title,
		Body:   issue.Body,
		Author: issue.User.Login,
		URL:    issue.HTMLURL,
		Labels: labels,
	}
}

// rememberPRMeta stores the PR title/body so scaffolding language detection has
// a sample even for pull requests that never went through planning.
func rememberPRMeta(app *AppContext, pr PullRequest) {
	if app.Store == nil {
		return
	}
	app.Store.SetPRMeta(app.Repo.FullName, pr.Number, &IssueMeta{
		Number: pr.Number,
		Title:  pr.Title,
		Body:   pr.Body,
		Author: pr.User.Login,
		URL:    pr.HTMLURL,
	})
}

// promptLanguage is the language the coding agent writes in. The default,
// "auto", keeps the issue's or pull request's own language, which the agent can
// read from the prompt.
func promptLanguage(app *AppContext) string {
	lang := strings.TrimSpace(app.Repo.Language)
	if lang == "" && app.Cfg != nil {
		lang = strings.TrimSpace(app.Cfg.Language)
	}
	if lang == "" || strings.EqualFold(lang, "auto") {
		return "the same language as the issue or pull request"
	}
	return lang
}

func runDirFor(ctx *AppContext, issueNumber int) string {
	dir := filepath.Join(runsDir(), fmt.Sprintf("%s-%s", ctx.Repo.Owner, ctx.Repo.Name), fmt.Sprintf("issue-%d", issueNumber))
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

func react(ctx context.Context, app *AppContext, trigger Trigger, content string) {
	if trigger.CommentID > 0 {
		_ = app.GH.ReactComment(ctx, app.Repo.FullName, trigger.CommentID, content)
		return
	}
	if trigger.IssueNumber > 0 {
		_ = app.GH.ReactIssue(ctx, app.Repo.FullName, trigger.IssueNumber, content)
	}
}

func bugInstructions(issue Issue) string {
	if !hasLabel(issue, bugLabel) {
		return ""
	}
	return `This issue is labelled "bug" and has no approved plan. Work in this order:
1. Reproduce the reported bug from the issue and comments. If it cannot be
   reproduced, do NOT change code: write result.json with
   {"abort": true, "summary": "<what you tried and what you saw>"} and stop.
2. If it reproduces, implement the smallest fix and add a regression test that
   fails before and passes after.
3. Run the checks; the harness repeats failing checks up to maxFixRounds.`
}

func doPlan(ctx context.Context, app *AppContext, trigger Trigger, issue Issue) {
	number := issue.Number
	if issueCancelled(app, number) {
		return
	}
	app.Log.Info("plan start for %s#%d", app.Repo.FullName, number)
	react(ctx, app, trigger, "eyes")
	app.Store.UpdateIssue(app.Repo.FullName, number, func(st *IssueState) {
		st.Phase = "planning"
		st.Issue = issueMeta(issue)
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, workingLabel)

	comments, err := app.GH.ListComments(ctx, app.Repo.FullName, number)
	if err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("list comments: %w", err))
		return
	}
	prompt, err := renderPrompt("plan", map[string]string{
		"REPO":     app.Repo.FullName,
		"NUMBER":   itoa(number),
		"TITLE":    issue.Title,
		"BODY":     firstNonEmpty(issue.Body, "(no description)"),
		"COMMENTS": commentsBlock(comments),
		"LANGUAGE": promptLanguage(app),
	})
	if err != nil {
		blockIssue(ctx, app, number, err)
		return
	}

	if err := fetchBase(ctx, app.Local); err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("fetch base: %w", err))
		return
	}
	planDir, err := createDetachedWorktree(ctx, app.Local, fmt.Sprintf("%s-%d-%s", app.Repo.Name, number, shortID(trigger.Key)))
	if err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("create plan worktree: %w", err))
		return
	}
	defer removeWorktree(ctx, app.Local, planDir, true)

	runDir := runDirFor(app, number)
	runner, err := newCodingRunner(app.Repo.CodingAgent)
	if err != nil {
		blockIssue(ctx, app, number, err)
		return
	}
	result, err := runner.Run(ctx, RunnerOptions{
		Cwd:     planDir,
		Prompt:  prompt,
		Model:   app.Repo.Model,
		RunDir:  runDir,
		Env:     app.Env(),
		Timeout: app.Cfg.PlanTimeout,
		LogFile: filepath.Join(runDir, fmt.Sprintf("plan-%s.log", app.Repo.CodingAgent)),
		Log:     app.Log,
	})
	if err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("plan failed: %w", err))
		return
	}
	app.Log.Debug("plan result: session=%s chars=%d", firstNonEmpty(result.SessionID, "(none)"), len(result.Text))
	plan := trimToPlan(stripFences(result.Text))
	if len(plan) < 40 {
		blockIssue(ctx, app, number, fmt.Errorf("plan is empty"))
		return
	}
	if lines := len(strings.Split(plan, "\n")); lines > 40 || len(plan) > 3000 {
		app.Log.Warn("plan for %s#%d is long (%d lines, %d chars); prompts/plan.md may need tightening", app.Repo.FullName, number, lines, len(plan))
	}
	if len(plan) > 55000 {
		plan = plan[:55000] + "\n\n" + app.text(number, "plan.truncated")
	}
	if issueCancelled(app, number) {
		app.Log.Debug("skip plan comment for cancelled %s#%d", app.Repo.FullName, number)
		return
	}

	body := strings.Join([]string{
		app.text(number, "plan.title"),
		"",
		plan,
		"",
		"---",
		fmt.Sprintf(app.text(number, "plan.footer"), projectURL, app.AgentLogin, app.AgentLogin),
	}, "\n")
	posted, err := app.GH.PostComment(ctx, app.Repo.FullName, number, body)
	if err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("post plan: %w", err))
		return
	}

	lastComment := int64(0)
	if len(comments) > 0 {
		lastComment = comments[len(comments)-1].ID
	}
	if posted.ID > lastComment {
		lastComment = posted.ID
	}
	app.Store.UpdateIssue(app.Repo.FullName, number, func(st *IssueState) {
		st.Phase = "awaiting_approval"
		st.ExtraNotes = ""
		st.Plan = plan
		st.PlanCommentID = posted.ID
		st.PRCommentID = 0
		st.SessionID = result.SessionID
		st.LastCommentID = lastComment
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
	app.Log.Info("plan posted for %s#%d (comment %d)", app.Repo.FullName, number, posted.ID)
}

// Triage answers are the only words the auto mode accepts.
const (
	triagePlan  = "plan"
	triageBuild = "build"
)

// planFirstRe matches an explicit request to plan before coding. Only human
// comments are checked: the triage prompt already weighs the issue body, and
// matching the body would also catch phrases like "no need to plan first".
var planFirstRe = regexp.MustCompile(`(?i)(先\s*(做|出|写|给|看|过)?\s*(一个|一份|个)?\s*(计划|规划|方案|plan)|(计划|方案)\s*先行|plan\s+(first|before\s+(code|coding|implementing))|first\s+(?:\w+\s+){0,3}plan)`)

// explicitPlanRequest reports whether a human comment asks for a plan first.
// It keeps auto mode deterministic when the requester already said what they
// want instead of spending an agent run on triage.
func explicitPlanRequest(comments []Comment) bool {
	for _, comment := range comments {
		if isAgentComment(comment.Body) {
			continue
		}
		if planFirstRe.MatchString(comment.Body) {
			return true
		}
	}
	return false
}

// parseTriageVerdict normalizes the agent's answer. The prompt demands one word
// on the first line; Markdown decoration and trailing punctuation are tolerated
// so a chatty reply still routes, while anything unexpected stays unknown and
// falls back to planning.
func parseTriageVerdict(text string) (string, bool) {
	for _, line := range strings.Split(stripFences(text), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		line = strings.ToLower(strings.Trim(line, "`*_#>-.:：。!！ \t"))
		switch line {
		case triagePlan:
			return triagePlan, true
		case triageBuild:
			return triageBuild, true
		}
		return "", false
	}
	return "", false
}

// shouldBuildDirectly runs the read-only auto-mode triage: true means the issue
// is clear enough to implement without a plan comment. Every failure returns
// false so a broken or unreadable triage falls back to the approval gate
// instead of parking the issue.
func shouldBuildDirectly(ctx context.Context, app *AppContext, trigger Trigger, issue Issue) bool {
	number := issue.Number
	if issueCancelled(app, number) {
		return false
	}
	comments, err := app.GH.ListComments(ctx, app.Repo.FullName, number)
	if err != nil {
		app.Log.Warn("triage %s#%d: list comments: %v", app.Repo.FullName, number, err)
		return false
	}
	if explicitPlanRequest(comments) {
		app.Log.Info("triage %s#%d: a comment asks for a plan first", app.Repo.FullName, number)
		return false
	}
	prompt, err := renderPrompt("triage", map[string]string{
		"REPO":     app.Repo.FullName,
		"NUMBER":   itoa(number),
		"TITLE":    issue.Title,
		"BODY":     firstNonEmpty(issue.Body, "(no description)"),
		"COMMENTS": commentsBlock(comments),
	})
	if err != nil {
		app.Log.Warn("triage %s#%d: render prompt: %v", app.Repo.FullName, number, err)
		return false
	}
	if err := fetchBase(ctx, app.Local); err != nil {
		app.Log.Warn("triage %s#%d: fetch base: %v", app.Repo.FullName, number, err)
		return false
	}
	dir, err := createDetachedWorktree(ctx, app.Local, fmt.Sprintf("%s-%d-%s", app.Repo.Name, number, shortID(trigger.Key)))
	if err != nil {
		app.Log.Warn("triage %s#%d: create worktree: %v", app.Repo.FullName, number, err)
		return false
	}
	defer removeWorktree(ctx, app.Local, dir, true)

	runDir := runDirFor(app, number)
	runner, err := runnerFor(app.Repo.CodingAgent)
	if err != nil {
		app.Log.Warn("triage %s#%d: %v", app.Repo.FullName, number, err)
		return false
	}
	result, err := runner.Run(ctx, RunnerOptions{
		Cwd:     dir,
		Prompt:  prompt,
		Model:   app.Repo.Model,
		RunDir:  runDir,
		Env:     app.Env(),
		Timeout: app.Cfg.PlanTimeout,
		LogFile: filepath.Join(runDir, fmt.Sprintf("triage-%s.log", app.Repo.CodingAgent)),
		Log:     app.Log,
	})
	if err != nil {
		app.Log.Warn("triage %s#%d failed: %v", app.Repo.FullName, number, err)
		return false
	}
	verdict, ok := parseTriageVerdict(result.Text)
	if !ok {
		app.Log.Warn("triage %s#%d: unreadable answer %q; planning", app.Repo.FullName, number, sanitize(tailString(result.Text, 2)))
		return false
	}
	app.Log.Info("triage %s#%d: %s", app.Repo.FullName, number, verdict)
	return verdict == triageBuild
}

func runChecksWithFixes(ctx context.Context, app *AppContext, runner CodingRunner, sessionID, worktree, runDir string) ([]CheckResult, bool) {
	checks := loadChecks(worktree, runDir, app.Log)
	checkResults, ok := runChecks(ctx, checks, worktree, runDir, app.Env(), app.Log)
	for attempt := 1; !ok && attempt <= app.Cfg.MaxFixRounds; attempt++ {
		app.Log.Warn("checks failed, fix round %d/%d", attempt, app.Cfg.MaxFixRounds)
		failures := make([]string, 0)
		for _, item := range checkResults {
			if item.OK {
				continue
			}
			failures = append(failures, fmt.Sprintf("### %s (exit %d)\n```\n%s\n```", item.Name, item.Code, tailString(item.OutputTail, 40)))
		}
		fixPrompt, promptErr := renderPrompt("fix", map[string]string{
			"RUN_DIR":      runDir,
			"ATTEMPT":      itoa(attempt),
			"MAX_ATTEMPTS": itoa(app.Cfg.MaxFixRounds),
			"FAILURES":     strings.Join(failures, "\n\n"),
			"WORKTREE":     worktree,
		})
		if promptErr != nil {
			app.Log.Warn("render fix prompt: %v", promptErr)
			break
		}
		if _, runErr := runner.Run(ctx, RunnerOptions{
			Cwd:       worktree,
			Prompt:    fixPrompt,
			SessionID: sessionID,
			Model:     app.Repo.Model,
			RunDir:    runDir,
			Env:       app.Env(),
			Timeout:   app.Cfg.ImplementTimeout,
			LogFile:   filepath.Join(runDir, fmt.Sprintf("fix-%d-%s.log", attempt, app.Repo.CodingAgent)),
			Log:       app.Log,
		}); runErr != nil {
			app.Log.Error("fix round %d failed: %v", attempt, runErr)
		}
		checks = loadChecks(worktree, runDir, app.Log)
		checkResults, ok = runChecks(ctx, checks, worktree, runDir, app.Env(), app.Log)
	}
	return checkResults, ok
}

func reportCheckFailures(ctx context.Context, app *AppContext, number int, worktree string, checkResults []CheckResult) {
	var details []string
	for _, item := range checkResults {
		if item.OK {
			continue
		}
		details = append(details, fmt.Sprintf("<details><summary>%s</summary>\n\n```\n%s\n```\n</details>", item.Name, sanitize(tailString(item.OutputTail, 40))))
	}
	body := strings.Join([]string{
		app.text(number, "checks_failed.title"),
		"",
		checksSummary(app.language(number), checkResults),
		"",
		strings.Join(details, "\n"),
		"",
		fmt.Sprintf(app.text(number, "checks_failed.hint"), worktree, app.AgentLogin),
	}, "\n")
	postStatusComment(ctx, app, number, body)
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "blocked"
		state.BlockedReason = "checks failed"
		state.BlockedByCI = false
		state.Checks = checkResults
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
}

func doRevise(ctx context.Context, app *AppContext, trigger Trigger, issue Issue, st IssueState) {
	number := issue.Number
	if issueCancelled(app, number) {
		return
	}
	if st.PRNumber == 0 || st.Branch == "" {
		blockIssue(ctx, app, number, fmt.Errorf("cannot apply feedback without an open PR"))
		return
	}
	worktree := st.Worktree
	if worktree == "" {
		blockIssue(ctx, app, number, fmt.Errorf("no worktree for feedback; reply @%s continue to restart", app.AgentLogin))
		return
	}
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("worktree %s is gone: %w", worktree, err))
		return
	}
	app.Log.Info("revise start for %s#%d (PR %d)", app.Repo.FullName, number, st.PRNumber)
	react(ctx, app, trigger, "rocket")
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "implementing"
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, workingLabel)

	runDir := runDirFor(app, number)
	_ = os.MkdirAll(filepath.Join(runDir, "screenshots"), 0o755)
	prompt, err := renderPrompt("revise", map[string]string{
		"REPO":     app.Repo.FullName,
		"WORKTREE": worktree,
		"RUN_DIR":  runDir,
		"NUMBER":   itoa(number),
		"TITLE":    issue.Title,
		"BODY":     firstNonEmpty(issue.Body, "(no description)"),
		"PLAN":     firstNonEmpty(st.Plan, "(missing plan)"),
		"PR":       fmt.Sprintf("#%d %s", st.PRNumber, st.PRURL),
		"FEEDBACK": firstNonEmpty(strings.TrimSpace(trigger.CommentBody), "(no text)"),
		"LANGUAGE": promptLanguage(app),
	})
	if err != nil {
		blockIssue(ctx, app, number, err)
		return
	}
	runner, err := newCodingRunner(app.Repo.CodingAgent)
	if err != nil {
		blockIssue(ctx, app, number, err)
		return
	}
	result, err := runner.Run(ctx, RunnerOptions{
		Cwd:       worktree,
		Prompt:    prompt,
		SessionID: st.SessionID,
		Model:     app.Repo.Model,
		RunDir:    runDir,
		Env:       app.Env(),
		Timeout:   app.Cfg.ImplementTimeout,
		LogFile:   filepath.Join(runDir, fmt.Sprintf("revise-%s.log", app.Repo.CodingAgent)),
		Log:       app.Log,
	})
	if err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("revision failed: %w", err))
		return
	}
	sessionID := firstNonEmpty(result.SessionID, st.SessionID)
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.SessionID = sessionID
	})

	if early := loadManifest(runDir); early.Abort {
		summary := firstNonEmpty(strings.TrimSpace(early.Summary), "could not reproduce the reported bug")
		app.Log.Info("implementation aborted for %s#%d: %s", app.Repo.FullName, number, summary)
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.Phase = "blocked"
			state.BlockedReason = "bug not reproduced: " + summary
			state.BlockedByCI = false
		})
		app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
		_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join([]string{
			app.text(number, "bug.abort.title"),
			"",
			summary,
			"",
			fmt.Sprintf(app.text(number, "bug.abort.hint"), app.AgentLogin, app.AgentLogin),
		}, "\n"))
		return
	}

	checkResults, ok := runChecksWithFixes(ctx, app, runner, sessionID, worktree, runDir)
	if !ok {
		reportCheckFailures(ctx, app, number, worktree, checkResults)
		return
	}
	if err := commitAll(ctx, worktree, app.Env(), fmt.Sprintf("chore: apply feedback for #%d", number)); err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("commit: %w", err))
		return
	}
	if issueCancelled(app, number) {
		return
	}
	if err := pushBranch(ctx, worktree, app.Local, st.Branch); err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("push: %w", err))
		return
	}

	manifest := loadManifest(runDir)
	var shots []Shot
	if manifest.UIChanged != nil && *manifest.UIChanged {
		shots = collectScreenshots(ctx, app.GH, runDir, manifest, app.RepoInfo.ID, app.Log)
	}
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "pr_open"
		state.Checks = checkResults
		if len(shots) > 0 {
			state.Screenshots = shots
		}
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)

	body := []string{
		fmt.Sprintf(app.text(number, "revise.title"), st.PRURL),
		"",
		firstNonEmpty(manifest.Summary, app.text(number, "revise.default_summary")),
		"",
		checksSummary(app.language(number), checkResults),
	}
	if shotsMD := screenshotsMarkdown(shots); shotsMD != "" {
		body = append(body, "", app.text(number, "common.screenshots"), "", shotsMD)
	}
	ciEnabled := app.GH.HasWorkflows(ctx, app.Repo.FullName)
	if ciEnabled {
		body = append(body, "", app.text(number, "common.ci_running"))
	} else {
		body = append(body, "", mergePrompt(app, number))
	}
	if posted, err := app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join(body, "\n")); err == nil {
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.PRCommentID = posted.ID
		})
	} else {
		app.Log.Warn("post revision comment: %v", err)
	}
	app.Log.Info("revision pushed for %s#%d (PR %d)", app.Repo.FullName, number, st.PRNumber)
	if ciEnabled && app.EnqueueCI != nil {
		app.Log.Info("queued CI settle for %s#%d", app.Repo.FullName, number)
		app.EnqueueCI(number)
	}
}

type reviewFinding struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

type reviewResult struct {
	Verdict  string          `json:"verdict"`
	Summary  string          `json:"summary"`
	Findings []reviewFinding `json:"comments"`
}

func runDirForPR(app *AppContext, prNumber int) string {
	dir := filepath.Join(runsDir(), fmt.Sprintf("%s-%s", app.Repo.Owner, app.Repo.Name), fmt.Sprintf("pr-%d", prNumber))
	_ = os.MkdirAll(dir, 0o755)
	return dir
}

// doReviewPR reviews a pull request in a detached worktree and posts the result
// as a GitHub review. It never touches the PR branch, so it works for anyone's
// PR, not only the ones this daemon opened.
func doReviewPR(ctx context.Context, app *AppContext, trigger Trigger, pr PullRequest) {
	number := pr.Number
	app.Log.Info("review start for %s#%d", app.Repo.FullName, number)
	rememberPRMeta(app, pr)
	react(ctx, app, trigger, "eyes")
	runDir := runDirForPR(app, number)

	if err := fetchBase(ctx, app.Local); err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("fetch %s: %w", app.Local.Base, err))
		return
	}
	ref, err := fetchPullRequestHead(ctx, app.Local, number)
	if err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("fetch PR head: %w", err))
		return
	}
	worktree, err := createDetachedWorktreeAt(ctx, app.Local, fmt.Sprintf("review-%d", number), ref)
	if err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("review worktree: %w", err))
		return
	}
	defer removeWorktree(ctx, app.Local, worktree, true)

	runner, err := newCodingRunner(app.Repo.CodingAgent)
	if err != nil {
		postReviewFailure(ctx, app, number, err)
		return
	}
	prompt, err := renderPrompt("review", map[string]string{
		"REPO":     app.Repo.FullName,
		"PR":       fmt.Sprintf("#%d %s", number, pr.HTMLURL),
		"NUMBER":   itoa(number),
		"TITLE":    pr.Title,
		"AUTHOR":   firstNonEmpty(pr.User.Login, "unknown"),
		"BODY":     firstNonEmpty(pr.Body, "(no description)"),
		"BASE":     app.Local.Base,
		"BRANCH":   pr.Head.Ref,
		"WORKTREE": worktree,
		"RUN_DIR":  runDir,
		"LANGUAGE": promptLanguage(app),
	})
	if err != nil {
		postReviewFailure(ctx, app, number, err)
		return
	}
	result, err := runner.Run(ctx, RunnerOptions{
		Cwd:     worktree,
		Prompt:  prompt,
		Model:   app.Repo.Model,
		RunDir:  runDir,
		Env:     app.Env(),
		Timeout: app.Cfg.PlanTimeout,
		LogFile: filepath.Join(runDir, fmt.Sprintf("review-%s.log", app.Repo.CodingAgent)),
		Log:     app.Log,
	})
	if err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("review agent: %w", err))
		return
	}

	review := loadReviewResult(runDir, result.Text)
	body := renderReviewBody(app, number, review)

	allowed := map[string]map[int]bool(nil)
	if files, filesErr := app.GH.ListPRFiles(ctx, app.Repo.FullName, number); filesErr != nil {
		app.Log.Warn("review %s#%d: list changed files: %v", app.Repo.FullName, number, filesErr)
	} else {
		allowed = diffRightLines(files)
	}
	inline, rest := inlineReviewComments(review.Findings, allowed)
	var posted Review
	if len(inline) > 0 {
		if created, err := app.GH.CreatePRReview(ctx, app.Repo.FullName, number, pr.Head.SHA, body+findingsFallback(app, number, rest), "COMMENT", inline); err == nil {
			posted = created
			app.Log.Info("review posted for %s#%d with %d inline comments", app.Repo.FullName, number, len(inline))
		} else {
			app.Log.Warn("inline review for %s#%d rejected (%v); posting findings in the body", app.Repo.FullName, number, err)
		}
	}
	if posted.ID == 0 {
		created, err := app.GH.CreatePRReview(ctx, app.Repo.FullName, number, pr.Head.SHA, body+findingsFallback(app, number, review.Findings), "COMMENT", nil)
		if err != nil {
			postReviewFailure(ctx, app, number, fmt.Errorf("post review: %w", err))
			return
		}
		posted = created
		app.Log.Info("review posted for %s#%d", app.Repo.FullName, number)
	}
	recordReviewedPR(ctx, app, number, posted.ID)
}

// recordReviewedPR remembers the review a 👍 applies and posts the comment that
// carries that thumbs-up (GitHub has no reactions on review bodies).
func recordReviewedPR(ctx context.Context, app *AppContext, number int, reviewID int64) {
	commentID := int64(0)
	body := strings.Join([]string{
		app.text(number, "review.posted.title"),
		"",
		app.text(number, "review.posted.hint"),
	}, "\n")
	if posted, err := app.GH.PostComment(ctx, app.Repo.FullName, number, body); err == nil {
		commentID = posted.ID
	} else {
		app.Log.Warn("post review thumbs-up prompt for %s#%d: %v", app.Repo.FullName, number, err)
	}
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		// UpdateIssue creates new entries in phase "new" for issues; a reviewed
		// pull request is not a task, so keep it out of the phase machine.
		state.Phase = ""
		state.ReviewID = reviewID
		state.ReviewCommentID = commentID
	})
}

// reviewInstructions turns a posted review into an edit instruction. A zero
// reviewID falls back to the most recent review by the bot.
func reviewInstructions(ctx context.Context, app *AppContext, pr PullRequest, reviewID int64) (string, error) {
	review := Review{ID: reviewID}
	if reviewID != 0 {
		if fetched, err := app.GH.GetReview(ctx, app.Repo.FullName, pr.Number, reviewID); err == nil {
			review = fetched
		}
	}
	if review.ID == 0 {
		reviews, err := app.GH.ListReviews(ctx, app.Repo.FullName, pr.Number)
		if err != nil {
			return "", err
		}
		for i := len(reviews) - 1; i >= 0; i-- {
			if strings.EqualFold(reviews[i].User.Login, app.AgentLogin) {
				review = reviews[i]
				break
			}
		}
	}
	if review.ID == 0 {
		return "", nil
	}
	comments, err := app.GH.ListReviewComments(ctx, app.Repo.FullName, pr.Number)
	if err != nil {
		app.Log.Warn("apply review %s#%d: list inline comments: %v", app.Repo.FullName, pr.Number, err)
	}

	var out strings.Builder
	out.WriteString("Implement the fixes for the findings in your review of this pull request. Do not restate them: change the code.\n")
	summary := reviewSummary(review.Body)
	if summary != "" {
		out.WriteString("\n" + summary + "\n")
	}
	findings := 0
	for _, comment := range comments {
		if comment.PullRequestReviewID != review.ID {
			continue
		}
		location := comment.Path
		if comment.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, comment.Line)
		}
		out.WriteString(fmt.Sprintf("\n- %s — %s", location, comment.Body))
		findings++
	}
	if findings == 0 && summary == "" {
		return "", nil
	}
	return out.String(), nil
}

// reviewSummary strips the review wrapper (heading, verdict badge and footer)
// so only the reviewer's text is handed to the agent.
func reviewSummary(body string) string {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "### 🤖 Review":
			continue
		case strings.HasPrefix(trimmed, "**Verdict:**"):
			continue
		case strings.HasPrefix(trimmed, "🤖 Generated by"):
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// loadReviewResult reads review.json, falling back to the agent's final text.
func loadReviewResult(runDir, fallback string) reviewResult {
	var result reviewResult
	if readJSONIfExists(filepath.Join(runDir, "review.json"), &result) {
		if strings.TrimSpace(result.Summary) == "" {
			result.Summary = fallback
		}
		return result
	}
	return reviewResult{Verdict: "comment", Summary: fallback}
}

func renderReviewBody(app *AppContext, number int, review reviewResult) string {
	badge := "💬 comment"
	switch strings.ToLower(strings.TrimSpace(review.Verdict)) {
	case "approve", "approved", "lgtm":
		badge = "✅ approve"
	case "request_changes", "request changes", "request-changes", "changes_requested":
		badge = "⚠️ request changes"
	}
	return strings.Join([]string{
		"### 🤖 Review",
		"",
		firstNonEmpty(strings.TrimSpace(review.Summary), app.text(number, "review.empty_summary")),
		"",
		fmt.Sprintf("**Verdict:** %s", badge),
		"",
		fmt.Sprintf(app.text(number, "review.footer"), projectURL, app.AgentLogin),
	}, "\n")
}

// diffRightLines maps each changed file to the RIGHT-side line numbers a
// review comment may target. GitHub rejects a review whose comment points at a
// file or line that is not part of the diff ("Path could not be resolved"), so
// callers keep those findings in the review body instead.
func diffRightLines(files []PRFile) map[string]map[int]bool {
	allowed := make(map[string]map[int]bool, len(files))
	for _, file := range files {
		lines := make(map[int]bool)
		for _, line := range strings.Split(file.Patch, "\n") {
			if !strings.HasPrefix(line, "@@") {
				continue
			}
			plus := strings.Index(line, "+")
			if plus < 0 {
				continue
			}
			rest := line[plus+1:]
			end := strings.IndexAny(rest, " @")
			if end < 0 {
				continue
			}
			spec := strings.SplitN(rest[:end], ",", 2)
			start, err := strconv.Atoi(spec[0])
			if err != nil {
				continue
			}
			count := 1
			if len(spec) == 2 {
				if parsed, parseErr := strconv.Atoi(spec[1]); parseErr == nil {
					count = parsed
				}
			}
			for i := 0; i < count; i++ {
				lines[start+i] = true
			}
		}
		allowed[file.Filename] = lines
	}
	return allowed
}

// inlineReviewComments splits findings into those GitHub can attach inline and
// the rest, which belong in the review body.
func inlineReviewComments(findings []reviewFinding, allowed map[string]map[int]bool) ([]PRReviewComment, []reviewFinding) {
	inline := make([]PRReviewComment, 0, len(findings))
	rest := make([]reviewFinding, 0, len(findings))
	for _, finding := range findings {
		path := strings.TrimSpace(finding.Path)
		body := strings.TrimSpace(finding.Body)
		lines, inDiff := allowed[path]
		switch {
		case path == "" || body == "":
			rest = append(rest, finding)
		case !inDiff || !lines[finding.Line]:
			rest = append(rest, finding)
		case len(inline) >= 10:
			rest = append(rest, finding)
		default:
			inline = append(inline, PRReviewComment{Path: path, Line: finding.Line, Side: "RIGHT", Body: body})
		}
	}
	return inline, rest
}

// findingsFallback renders findings in the review body when inline comments
// cannot be attached (for example a line that is not part of the diff).
func findingsFallback(app *AppContext, number int, findings []reviewFinding) string {
	lines := make([]string, 0, len(findings))
	for _, finding := range findings {
		body := strings.TrimSpace(finding.Body)
		if body == "" {
			continue
		}
		location := strings.TrimSpace(finding.Path)
		if finding.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, finding.Line)
		}
		lines = append(lines, fmt.Sprintf("- `%s` — %s", location, strings.ReplaceAll(body, "\n", " ")))
	}
	if len(lines) == 0 {
		return ""
	}
	return "\n\n" + app.text(number, "review.inline_title") + "\n\n" + strings.Join(lines, "\n")
}

func postReviewFailure(ctx context.Context, app *AppContext, prNumber int, err error) {
	app.Log.Error("review for %s#%d failed: %v", app.Repo.FullName, prNumber, err)
	_, _ = app.GH.PostComment(ctx, app.Repo.FullName, prNumber,
		fmt.Sprintf("%s\n\n```\n%s\n```\n\n%s",
			app.text(prNumber, "review.failed.title"), sanitize(err.Error()),
			fmt.Sprintf(app.text(prNumber, "review.failed.hint"), app.AgentLogin)))
}

// canPushToPR reports whether the bot may push to the PR's head branch: same
// repository, or a fork whose author allowed edits by maintainers.
func canPushToPR(app *AppContext, pr PullRequest) bool {
	head := strings.TrimSpace(pr.Head.Repo.FullName)
	if head == "" {
		return false
	}
	if strings.EqualFold(head, app.Repo.FullName) {
		return true
	}
	return pr.MaintainerCanModify
}

func prPushURL(app *AppContext, pr PullRequest) string {
	if strings.EqualFold(pr.Head.Repo.FullName, app.Repo.FullName) || pr.Head.Repo.FullName == "" {
		return app.Local.HTTPSURL
	}
	return fmt.Sprintf("https://github.com/%s.git", pr.Head.Repo.FullName)
}

// doModifyPR applies a maintainer's instructions to someone else's pull
// request and pushes the result to the PR head branch. It never force-pushes
// and never opens a second PR.
func doModifyPR(ctx context.Context, app *AppContext, trigger Trigger, pr PullRequest, instructions string) {
	number := pr.Number
	rememberPRMeta(app, pr)
	if strings.TrimSpace(instructions) == "" {
		_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number,
			fmt.Sprintf(app.text(number, "modify.empty"), app.AgentLogin))
		return
	}
	if !canPushToPR(app, pr) {
		_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join([]string{
			app.text(number, "modify.cannot.title"),
			"",
			fmt.Sprintf(app.text(number, "modify.cannot.body"), firstNonEmpty(pr.Head.Repo.FullName, "(deleted fork)")),
			"",
			app.text(number, "modify.cannot.hint"),
		}, "\n"))
		return
	}
	app.Log.Info("pr modify start for %s#%d", app.Repo.FullName, number)
	react(ctx, app, trigger, "eyes")
	runDir := runDirForPR(app, number)
	_ = os.MkdirAll(filepath.Join(runDir, "screenshots"), 0o755)

	if err := fetchBase(ctx, app.Local); err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("fetch %s: %w", app.Local.Base, err))
		return
	}
	ref, err := fetchPullRequestHead(ctx, app.Local, number)
	if err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("fetch PR head: %w", err))
		return
	}
	branch := fmt.Sprintf("pr-%d-%s", number, shortID(fmt.Sprintf("%d", time.Now().UnixNano())))
	worktree, err := createWorktreeFromRef(ctx, app.Local, branch, ref)
	if err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("worktree: %w", err))
		return
	}
	defer func() {
		removeWorktree(ctx, app.Local, worktree, true)
		deleteLocalBranch(ctx, app.Local, branch)
	}()

	runner, err := newCodingRunner(app.Repo.CodingAgent)
	if err != nil {
		postReviewFailure(ctx, app, number, err)
		return
	}
	prompt, err := renderPrompt("modify_pr", map[string]string{
		"REPO":         app.Repo.FullName,
		"PR":           fmt.Sprintf("#%d %s", number, pr.HTMLURL),
		"NUMBER":       itoa(number),
		"TITLE":        pr.Title,
		"BODY":         firstNonEmpty(pr.Body, "(no description)"),
		"BASE":         app.Local.Base,
		"BRANCH":       pr.Head.Ref,
		"HEAD_REPO":    pr.Head.Repo.FullName,
		"WORKTREE":     worktree,
		"RUN_DIR":      runDir,
		"SENDER":       firstNonEmpty(trigger.Sender, "maintainer"),
		"INSTRUCTIONS": instructions,
		"LANGUAGE":     promptLanguage(app),
	})
	if err != nil {
		postReviewFailure(ctx, app, number, err)
		return
	}
	if _, err := runner.Run(ctx, RunnerOptions{
		Cwd:     worktree,
		Prompt:  prompt,
		Model:   app.Repo.Model,
		RunDir:  runDir,
		Env:     app.Env(),
		Timeout: app.Cfg.ImplementTimeout,
		LogFile: filepath.Join(runDir, fmt.Sprintf("modify-pr-%s.log", app.Repo.CodingAgent)),
		Log:     app.Log,
	}); err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("modify agent: %w", err))
		return
	}
	if gitStatus(ctx, worktree, app.Env()) == "" {
		_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, app.text(number, "modify.nochange"))
		return
	}
	checkResults, ok := runChecksWithFixes(ctx, app, runner, "", worktree, runDir)
	if !ok {
		_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join([]string{
			app.text(number, "modify.checks_failed"),
			"",
			checksSummary(app.language(number), checkResults),
			"",
			fmt.Sprintf(app.text(number, "modify.checks_failed_hint"), app.AgentLogin),
		}, "\n"))
		return
	}
	if err := commitAll(ctx, worktree, app.Env(), fmt.Sprintf("chore: apply maintainer instructions for #%d", number)); err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("commit: %w", err))
		return
	}
	if err := pushBranchTo(ctx, worktree, prPushURL(app, pr), app.Env(), branch, pr.Head.Ref); err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("push to %s: %w", pr.Head.Repo.FullName, err))
		return
	}

	manifest := loadManifest(runDir)
	body := []string{
		fmt.Sprintf(app.text(number, "modify.done.title"), pr.Head.Repo.FullName),
		"",
		firstNonEmpty(strings.TrimSpace(manifest.Summary), fmt.Sprintf(app.text(number, "modify.done.default"), pr.Head.Ref)),
		"",
		checksSummary(app.language(number), checkResults),
	}
	if shotsMD := screenshotsMarkdown(collectScreenshots(ctx, app.GH, runDir, manifest, app.RepoInfo.ID, app.Log)); shotsMD != "" {
		body = append(body, "", app.text(number, "common.screenshots"), "", shotsMD)
	}
	body = append(body, "", fmt.Sprintf(app.text(number, "modify.done.hint"), app.AgentLogin))
	_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join(body, "\n"))
	app.Log.Info("pr modified for %s#%d (%s)", app.Repo.FullName, number, pr.Head.Repo.FullName)
}

// doMergePR lands a pull request the daemon does not own: resolve conflicts,
// run the checks, gate on CI and squash-merge. It needs maintainer_can_modify
// for forks, because the resolution is pushed back to the head branch.
func doMergePR(ctx context.Context, app *AppContext, trigger Trigger, pr PullRequest) {
	number := pr.Number
	rememberPRMeta(app, pr)
	app.Log.Info("pr merge start for %s#%d", app.Repo.FullName, number)
	react(ctx, app, trigger, "hooray")
	if !canPushToPR(app, pr) {
		_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join([]string{
			app.text(number, "modify.cannot.title"),
			"",
			fmt.Sprintf(app.text(number, "modify.cannot.body"), firstNonEmpty(pr.Head.Repo.FullName, "(deleted fork)")),
			"",
			app.text(number, "modify.cannot.hint"),
		}, "\n"))
		return
	}
	if err := fetchBase(ctx, app.Local); err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("fetch %s: %w", app.Local.Base, err))
		return
	}
	ref, err := fetchPullRequestHead(ctx, app.Local, number)
	if err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("fetch PR head: %w", err))
		return
	}
	runDir := runDirForPR(app, number)
	branch := fmt.Sprintf("pr-%d-%s", number, shortID(fmt.Sprintf("%d", time.Now().UnixNano())))
	worktree, err := createWorktreeFromRef(ctx, app.Local, branch, ref)
	if err != nil {
		postReviewFailure(ctx, app, number, fmt.Errorf("worktree: %w", err))
		return
	}
	defer func() {
		removeWorktree(ctx, app.Local, worktree, true)
		deleteLocalBranch(ctx, app.Local, branch)
	}()
	target := conflictTarget{
		Number:       number,
		Worktree:     worktree,
		LocalBranch:  branch,
		RemoteURL:    prPushURL(app, pr),
		RemoteBranch: pr.Head.Ref,
		RunDir:       runDir,
	}
	rounds := 0
	// GitHub can still report the pre-push "dirty" state for a head we just
	// fixed; skip one round of conflict handling so a single conflict does not
	// look like two.
	skipConflictCheck := false
	for {
		pr, err = waitForMergeable(ctx, app, number, time.Minute)
		if err != nil {
			postReviewFailure(ctx, app, number, fmt.Errorf("refresh PR: %w", err))
			return
		}
		if pr.State != "open" {
			return
		}
		if !skipConflictCheck && mergeBlockedByConflicts(pr) {
			if rounds >= app.Cfg.MaxFixRounds {
				reason := fmt.Sprintf(app.text(number, "conflict.blocked"), app.Local.Base, rounds)
				postStatusComment(ctx, app, number,
					fmt.Sprintf("%s\n\n%s\n\n%s", app.text(number, "merge.failed.title"), reason, app.text(number, "conflict.blocked_hint")))
				return
			}
			rounds++
			_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join([]string{
				fmt.Sprintf(app.text(number, "conflict.title"), rounds, app.Cfg.MaxFixRounds),
				"",
				fmt.Sprintf(app.text(number, "conflict.hint"), app.Local.Base, pr.Head.Ref),
			}, "\n"))
			if err := resolveConflictsIn(ctx, app, target, rounds); err != nil {
				postReviewFailure(ctx, app, number, err)
				return
			}
			skipConflictCheck = true
			continue
		}
		skipConflictCheck = false
		if verdict := waitForChecks(ctx, app, pr.Head.SHA, pr.Base.Ref, 10*time.Minute); !verdict.OK {
			postStatusComment(ctx, app, number,
				fmt.Sprintf("%s\n\n%s\n\n- %s\n\n%s", app.text(number, "ci.blocked.title"),
					app.text(number, "ci.blocked.body"), verdict.Note, app.text(number, "ci.blocked.hint")))
			return
		}
		merged, mergeErr := app.GH.MergePR(ctx, app.Repo.FullName, number)
		if mergeErr == nil && merged.Merged {
			if strings.EqualFold(pr.Head.Repo.FullName, app.Repo.FullName) {
				_ = app.GH.DeleteBranch(ctx, app.Repo.FullName, pr.Head.Ref)
			}
			_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join([]string{
				app.text(number, "merge.pr.done_title"),
				"",
				fmt.Sprintf(app.text(number, "merge.done.pr"), number, pr.Base.Ref),
			}, "\n"))
			app.Log.Info("merged PR %s#%d", app.Repo.FullName, number)
			return
		}
		if isConflictError(mergeErr) && rounds < app.Cfg.MaxFixRounds {
			// The base moved between the gate and the merge: resolve again.
			rounds++
			if err := resolveConflictsIn(ctx, app, target, rounds); err != nil {
				postReviewFailure(ctx, app, number, err)
				return
			}
			continue
		}
		detail := merged.Message
		if mergeErr != nil {
			detail = mergeErr.Error()
		}
		postStatusComment(ctx, app, number,
			fmt.Sprintf("%s\n\n```\n%s\n```\n\n%s", app.text(number, "merge.failed.title"), sanitize(detail), app.text(number, "merge.failed.hint")))
		return
	}
}

func doImplement(ctx context.Context, app *AppContext, trigger Trigger, issue Issue, st IssueState) {
	number := issue.Number
	if issueCancelled(app, number) {
		return
	}
	app.Log.Info("implement start for %s#%d", app.Repo.FullName, number)
	react(ctx, app, trigger, "rocket")
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "implementing"
		state.StartedAt = nowISO()
		state.Issue = issueMeta(issue)
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, workingLabel)

	if err := fetchBase(ctx, app.Local); err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("fetch base: %w", err))
		return
	}
	key := shortID(fmt.Sprint(st.PlanCommentID))
	worktree, branch, err := createWorktree(ctx, app.Local, number, key)
	if err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("create worktree: %w", err))
		return
	}
	runDir := runDirFor(app, number)
	_ = os.MkdirAll(filepath.Join(runDir, "screenshots"), 0o755)
	app.Log.Debug("implement workspace: worktree=%s branch=%s runDir=%s", worktree, branch, runDir)
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Worktree = worktree
		state.Branch = branch
	})

	comments, err := app.GH.ListComments(ctx, app.Repo.FullName, number)
	if err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("list comments: %w", err))
		return
	}
	prompt, err := renderPrompt("implement", map[string]string{
		"REPO":             app.Repo.FullName,
		"WORKTREE":         worktree,
		"BASE":             app.Local.Base,
		"RUN_DIR":          runDir,
		"NUMBER":           itoa(number),
		"TITLE":            issue.Title,
		"BODY":             firstNonEmpty(issue.Body, "(no description)"),
		"COMMENTS":         commentsBlock(comments),
		"PLAN":             firstNonEmpty(st.Plan, "(missing plan, implement the issue directly)"),
		"NOTES":            firstNonEmpty(strings.TrimSpace(st.ExtraNotes), "(none)"),
		"BUG_INSTRUCTIONS": bugInstructions(issue),
		"LANGUAGE":         promptLanguage(app),
	})
	if err != nil {
		blockIssue(ctx, app, number, err)
		return
	}

	runner, err := newCodingRunner(app.Repo.CodingAgent)
	if err != nil {
		blockIssue(ctx, app, number, err)
		return
	}
	result, err := runner.Run(ctx, RunnerOptions{
		Cwd:     worktree,
		Prompt:  prompt,
		Model:   app.Repo.Model,
		RunDir:  runDir,
		Env:     app.Env(),
		Timeout: app.Cfg.ImplementTimeout,
		LogFile: filepath.Join(runDir, fmt.Sprintf("implement-%s.log", app.Repo.CodingAgent)),
		Log:     app.Log,
	})
	if err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("implement failed: %w", err))
		return
	}
	sessionID := result.SessionID
	if sessionID == "" {
		sessionID = st.SessionID
	}
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.SessionID = sessionID
	})

	if early := loadManifest(runDir); early.Abort {
		summary := firstNonEmpty(strings.TrimSpace(early.Summary), "could not reproduce the reported bug")
		app.Log.Info("implementation aborted for %s#%d: %s", app.Repo.FullName, number, summary)
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.Phase = "blocked"
			state.BlockedReason = "bug not reproduced: " + summary
			state.BlockedByCI = false
		})
		app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
		_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join([]string{
			app.text(number, "bug.abort.title"),
			"",
			summary,
			"",
			fmt.Sprintf(app.text(number, "bug.abort.hint"), app.AgentLogin, app.AgentLogin),
		}, "\n"))
		return
	}

	checkResults, ok := runChecksWithFixes(ctx, app, runner, sessionID, worktree, runDir)
	if !ok {
		reportCheckFailures(ctx, app, number, worktree, checkResults)
		return
	}

	if err := commitAll(ctx, worktree, app.Env(), fmt.Sprintf("chore: finalize changes for #%d", number)); err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("commit: %w", err))
		return
	}

	manifest := loadManifest(runDir)
	files := changedFiles(ctx, worktree, app.Local)
	uiFiles := 0
	for _, file := range files {
		lower := strings.ToLower(file)
		for _, ext := range []string{".svelte", ".tsx", ".jsx", ".vue", ".css", ".scss", ".sass", ".less", ".html"} {
			if strings.HasSuffix(lower, ext) {
				uiFiles++
				break
			}
		}
	}
	uiChanged := (manifest.UIChanged != nil && *manifest.UIChanged) || (manifest.UIChanged == nil && uiFiles > 0)
	var shots []Shot
	if uiChanged {
		shots = collectScreenshots(ctx, app.GH, runDir, manifest, app.RepoInfo.ID, app.Log)
	}
	shotsMD := screenshotsMarkdown(shots)
	uiNote := ""
	if !uiChanged {
		uiNote = app.text(number, "common.no_ui")
	} else if len(shots) == 0 {
		uiNote = app.text(number, "common.ui_missing")
	}

	commits := logSinceBase(ctx, worktree, app.Local)
	stat := diffStat(ctx, worktree, app.Local)
	if issueCancelled(app, number) {
		app.Log.Debug("skip push/PR for cancelled %s#%d", app.Repo.FullName, number)
		return
	}
	if err := pushBranch(ctx, worktree, app.Local, branch); err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("push: %w", err))
		return
	}

	prTitle := firstNonEmpty(manifest.PRTitle, fmt.Sprintf("fix: %s (#%d)", issue.Title, number))
	prParts := []string{app.text(number, "implement.pr.agent_note"), ""}
	if manifest.Summary != "" {
		prParts = append(prParts, app.text(number, "implement.pr.summary"), "", manifest.Summary, "")
	}
	if len(manifest.Changes) > 0 {
		prParts = append(prParts, app.text(number, "implement.pr.changes"))
		for _, change := range manifest.Changes {
			prParts = append(prParts, "- "+change)
		}
		prParts = append(prParts, "")
	}
	prParts = append(prParts, fmt.Sprintf("Closes #%d", number), "", app.text(number, "implement.pr.commits"), "```", firstNonEmpty(commits, "(none)"), "```", "", app.text(number, "implement.pr.checks"), checksSummary(app.language(number), checkResults), "")
	if len(manifest.Tests) > 0 {
		prParts = append(prParts, app.text(number, "implement.pr.tests"))
		for _, test := range manifest.Tests {
			prParts = append(prParts, "- "+test)
		}
		prParts = append(prParts, "")
	}
	if shotsMD != "" {
		prParts = append(prParts, app.text(number, "common.screenshots"), "", shotsMD, "")
	}
	if uiNote != "" {
		prParts = append(prParts, app.text(number, "common.ui_note"), "", uiNote, "")
	}
	prParts = append(prParts, "```diff", firstNonEmpty(stat, "(no diff)"), "```")

	pr, err := app.GH.CreatePR(ctx, app.Repo.FullName, prTitle, branch, app.Local.Base, strings.Join(prParts, "\n"))
	if err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("create PR: %w", err))
		return
	}

	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "pr_open"
		state.PRNumber = pr.Number
		state.PRURL = pr.HTMLURL
		state.Branch = branch
		state.Worktree = worktree
		state.Checks = checkResults
		state.Screenshots = shots
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)

	commentParts := []string{fmt.Sprintf(app.text(number, "implement.done.title"), pr.HTMLURL), "", manifest.Summary, "", checksSummary(app.language(number), checkResults)}
	if shotsMD != "" {
		commentParts = append(commentParts, "", app.text(number, "common.screenshots"), "", shotsMD)
	}
	if uiNote != "" {
		commentParts = append(commentParts, "", app.text(number, "common.ui_note"), "", uiNote)
	}
	ciEnabled := app.GH.HasWorkflows(ctx, app.Repo.FullName)
	if ciEnabled {
		commentParts = append(commentParts, "", app.text(number, "common.ci_running"))
	} else {
		commentParts = append(commentParts, "", mergePrompt(app, number))
	}
	if posted, err := app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join(commentParts, "\n")); err == nil {
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.PRCommentID = posted.ID
		})
	}
	app.Log.Info("PR opened for %s#%d: %s", app.Repo.FullName, number, pr.HTMLURL)
	if ciEnabled && app.EnqueueCI != nil {
		app.Log.Info("queued CI settle for %s#%d", app.Repo.FullName, number)
		app.EnqueueCI(number)
	}
}

const (
	checkPollInterval = 15 * time.Second
	// checkStuckAfter is how long a check may sit queued without a runner
	// picking it up before it is treated as a job that will never run, such as
	// one scheduled on a retired runner label.
	checkStuckAfter = 5 * time.Minute
	// ciLogProblems is how many failed jobs get their log tail quoted in the
	// repair prompt.
	ciLogProblems = 4
)

type ciProblem struct {
	Name        string
	Detail      string
	URL         string
	JobID       int64
	RunID       int64
	Preexisting bool
}

func (p ciProblem) String() string { return p.Name + ": " + p.Detail }

type ciVerdict struct {
	OK       bool
	Note     string
	Problems []ciProblem
}

// waitForChecks gates a merge on GitHub check runs. It fails as soon as a
// completed check is red or a queued check has clearly lost its runner, so one
// abandoned job cannot hide a real failure until the timeout. On a protected
// branch only the required checks gate the merge. Problems that also occur on
// the base branch are flagged, so broken repository CI is not mistaken for a
// regression.
func waitForChecks(ctx context.Context, app *AppContext, sha, baseRef string, timeout time.Duration) ciVerdict {
	started := time.Now()
	var required map[string]bool
	if names, err := app.GH.RequiredChecks(ctx, app.Repo.FullName, baseRef); err != nil {
		app.Log.Warn("required checks for %s: %v", firstNonEmpty(baseRef, "base"), err)
	} else if len(names) > 0 {
		required = make(map[string]bool, len(names))
		for _, name := range names {
			required[name] = true
		}
	}
	for {
		runs, err := app.GH.ListCheckRuns(ctx, app.Repo.FullName, sha)
		if err != nil {
			return ciVerdict{OK: true, Note: "no check-runs access, skipping CI gate"}
		}
		if runs.TotalCount == 0 || len(runs.CheckRuns) == 0 {
			return ciVerdict{OK: true, Note: "no CI configured"}
		}
		problems := make([]ciProblem, 0)
		pending := make([]string, 0)
		counted := 0
		for _, run := range runs.CheckRuns {
			if !checkRunGates(run.Name, required) {
				continue
			}
			counted++
			if run.Status != "completed" {
				if checkRunStuck(run, time.Now()) {
					problems = append(problems, newCIProblem(run, "queued with no runner (check the workflow's runner label)"))
					continue
				}
				pending = append(pending, run.Name)
				continue
			}
			conclusion := ""
			if run.Conclusion != nil {
				conclusion = *run.Conclusion
			}
			switch conclusion {
			case "success", "neutral", "skipped":
			default:
				problems = append(problems, newCIProblem(run, conclusion))
			}
		}
		if len(problems) > 0 {
			broken := baseCheckProblems(ctx, app, baseRef)
			for i := range problems {
				problems[i].Preexisting = broken[problems[i].Name]
			}
			return ciVerdict{OK: false, Note: describeCIProblems(problems, baseRef), Problems: problems}
		}
		if len(pending) == 0 {
			return ciVerdict{OK: true, Note: fmt.Sprintf("%d checks passed", counted)}
		}
		if time.Since(started) > timeout {
			return ciVerdict{OK: false, Note: fmt.Sprintf("still running after %d min: %s", int(timeout.Minutes()), strings.Join(pending, ", "))}
		}
		app.Log.Info("waiting for %d checks...", len(pending))
		select {
		case <-ctx.Done():
			return ciVerdict{OK: false, Note: "cancelled"}
		case <-time.After(checkPollInterval):
		}
	}
}

func newCIProblem(run CheckRun, detail string) ciProblem {
	return ciProblem{
		Name:   run.Name,
		Detail: detail,
		URL:    run.DetailsURL,
		JobID:  parseIDFromURL(run.DetailsURL, "/job/"),
		RunID:  parseIDFromURL(run.DetailsURL, "/runs/"),
	}
}

// parseIDFromURL pulls the trailing number after marker out of a check-run
// details URL, e.g. /actions/runs/43556544848/job/132330566763.
func parseIDFromURL(raw, marker string) int64 {
	index := strings.Index(raw, marker)
	if index < 0 {
		return 0
	}
	rest := raw[index+len(marker):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	value, err := strconv.ParseInt(rest[:end], 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// checkRunGates reports whether a check run should block the merge. An empty
// required set means the branch has no protection, so every check gates.
func checkRunGates(name string, required map[string]bool) bool {
	if len(required) == 0 {
		return true
	}
	return required[name]
}

func checkRunStuck(run CheckRun, now time.Time) bool {
	if run.Status == "completed" || run.Status == "in_progress" || run.StartedAt != nil || run.CreatedAt.IsZero() {
		return false
	}
	return now.Sub(run.CreatedAt) > checkStuckAfter
}

// describeCIProblems renders failures and points out the ones the base branch
// already has, so a red repository-wide CI is visible at a glance.
func describeCIProblems(problems []ciProblem, baseRef string) string {
	parts := make([]string, 0, len(problems))
	for _, problem := range problems {
		text := problem.String()
		if problem.Preexisting && baseRef != "" {
			text += fmt.Sprintf(" (also failing on %s)", baseRef)
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, ", ")
}

// ciFailureReport is the repair prompt's view of the blocking checks: names,
// links and the tail of each failed job log, so the agent can tell a code
// failure from a broken workflow.
func ciFailureReport(ctx context.Context, app *AppContext, verdict ciVerdict) string {
	if len(verdict.Problems) == 0 {
		return verdict.Note
	}
	parts := make([]string, 0, len(verdict.Problems))
	for index, problem := range verdict.Problems {
		line := fmt.Sprintf("### %s: %s", problem.Name, problem.Detail)
		if problem.Preexisting {
			line += fmt.Sprintf(" (also failing on %s)", app.Local.Base)
		}
		if problem.URL != "" {
			line += "\n" + problem.URL
		}
		if index < ciLogProblems && problem.JobID != 0 {
			if tail := app.GH.JobLogTail(ctx, app.Repo.FullName, problem.JobID, 40); strings.TrimSpace(tail) != "" {
				line += "\n```\n" + tailString(tail, 40) + "\n```"
			}
		}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n\n")
}

func baseCheckProblems(ctx context.Context, app *AppContext, ref string) map[string]bool {
	if ref == "" {
		return nil
	}
	runs, err := app.GH.ListCheckRuns(ctx, app.Repo.FullName, ref)
	if err != nil {
		return nil
	}
	broken := make(map[string]bool, len(runs.CheckRuns))
	for _, run := range runs.CheckRuns {
		if run.Status != "completed" {
			broken[run.Name] = true
			continue
		}
		conclusion := ""
		if run.Conclusion != nil {
			conclusion = *run.Conclusion
		}
		switch conclusion {
		case "success", "neutral", "skipped":
		default:
			broken[run.Name] = true
		}
	}
	return broken
}

// settleCIGate repairs a red or stuck CI right after a push, so a pull request
// never sits there waiting for a human merge command. It returns whether the PR
// is ready to merge and how many repair rounds ran.
func settleCIGate(ctx context.Context, app *AppContext, issue Issue, pr PullRequest) (bool, int) {
	number := issue.Number
	if !app.GH.HasWorkflows(ctx, app.Repo.FullName) {
		app.Log.Debug("no workflows on %s; skipping CI settle for #%d", app.Repo.FullName, number)
		return true, 0
	}
	app.Log.Info("post-push CI gate for %s#%d", app.Repo.FullName, number)
	waitForFreshCheckRuns(ctx, app, pr.Head.SHA, time.Minute)
	if runs, err := app.GH.ListCheckRuns(ctx, app.Repo.FullName, pr.Head.SHA); err == nil && hasPendingChecks(runs) {
		postCIWaitMessage(ctx, app, number, pr, runs)
	}
	st := app.Store.Issue(app.Repo.FullName, number)
	verdict, rounds, err := ciGateWithRepairs(ctx, app, issue, st, pr)
	if err != nil {
		blockIssue(ctx, app, number, err)
		return false, rounds
	}
	if verdict.OK {
		return true, rounds
	}
	reason := verdict.Note
	if rounds > 0 {
		reason += app.text(number, "ci.rounds_suffix", rounds)
	}
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "blocked"
		state.BlockedReason = reason
		state.BlockedByCI = true
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
	postStatusComment(ctx, app, number, strings.Join([]string{
		app.text(number, "ci.blocked.title"),
		"",
		"- " + reason,
		"",
		app.text(number, "ci.blocked.hint"),
	}, "\n"))
	return false, rounds
}

// hasPendingChecks reports whether any check still has to finish.
func hasPendingChecks(runs CheckRuns) bool {
	for _, run := range runs.CheckRuns {
		if run.Status != "completed" {
			return true
		}
	}
	return false
}

// postCIWaitMessage tells the issue that its checks are running, so nobody
// wonders why the PR sits there, and that the daemon will handle failures.
func postCIWaitMessage(ctx context.Context, app *AppContext, number int, pr PullRequest, runs CheckRuns) {
	lines := make([]string, 0, len(runs.CheckRuns))
	for _, run := range runs.CheckRuns {
		state := run.Status
		if run.Conclusion != nil && *run.Conclusion != "" {
			state = *run.Conclusion
		}
		lines = append(lines, fmt.Sprintf("- `%s` — %s", run.Name, state))
	}
	sort.Strings(lines)
	body := strings.Join([]string{
		app.text(number, "ci.wait.title"),
		"",
		fmt.Sprintf(app.text(number, "ci.wait.body"), pr.Number),
		"",
		strings.Join(lines, "\n"),
		"",
		fmt.Sprintf(app.text(number, "ci.wait.hint"), app.Cfg.MaxFixRounds),
	}, "\n")
	if _, err := app.GH.PostComment(ctx, app.Repo.FullName, number, body); err != nil {
		app.Log.Warn("post CI wait message for %s#%d: %v", app.Repo.FullName, number, err)
	}
}

// mergePrompt is the sentence that invites the approver to merge.
func mergePrompt(app *AppContext, number int) string {
	return fmt.Sprintf(app.text(number, "merge.prompt"), app.AgentLogin)
}

// postMergePrompt publishes the merge invitation after the CI gate settled, so
// the thumbs-up target is always the newest message.
func postMergePrompt(ctx context.Context, app *AppContext, number, rounds int) {
	heading := app.text(number, "merge.ready")
	note := ""
	if rounds > 0 {
		heading = app.text(number, "merge.ci_fixed")
		note = "\n\n" + app.text(number, "merge.ci_fixed_body", rounds)
	}
	body := heading + note + "\n\n" + mergePrompt(app, number)
	if posted, err := app.GH.PostComment(ctx, app.Repo.FullName, number, body); err == nil {
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.PRCommentID = posted.ID
		})
	} else {
		app.Log.Warn("post merge prompt for %s#%d: %v", app.Repo.FullName, number, err)
	}
}

// ciGateWithRepairs waits for the pull request checks and, instead of parking
// the task, hands a red or stuck CI back to the coding agent: a broken workflow
// is fixed in the same PR, a real failure gets the code fixed. Workflow runs
// that wait for maintainer approval are approved automatically. It returns the
// last verdict and how many agent repair rounds ran.
func ciGateWithRepairs(ctx context.Context, app *AppContext, issue Issue, st IssueState, pr PullRequest) (ciVerdict, int, error) {
	number := issue.Number
	// A previous round may have left a commit in the worktree without pushing
	// it (for example an agent that commits on its own). Publish it before
	// gating, or the gate keeps checking the old head forever.
	if st.Worktree != "" && pr.Head.SHA != "" {
		if ahead, head := worktreeAheadOfPR(ctx, st.Worktree, app.Local, st.Branch, pr.Head.SHA); ahead {
			app.Log.Info("publishing stranded commit %s for %s#%d", shortSHA(head), app.Repo.FullName, number)
			if err := pushBranch(ctx, st.Worktree, app.Local, st.Branch); err == nil {
				if refreshed, refreshErr := waitForPRHead(ctx, app, st.PRNumber, head, 2*time.Minute); refreshErr == nil {
					pr = refreshed
				}
			}
		}
	}
	verdict := waitForChecks(ctx, app, pr.Head.SHA, pr.Base.Ref, 10*time.Minute)
	rounds := 0
	approved := false
	for !verdict.OK {
		if !approved && approveWaitingRuns(ctx, app, issue, verdict) {
			approved = true
			verdict = waitForChecks(ctx, app, pr.Head.SHA, pr.Base.Ref, 10*time.Minute)
			continue
		}
		if len(verdict.Problems) == 0 || rounds >= app.Cfg.MaxFixRounds || st.Worktree == "" {
			// Nothing actionable: the checks are merely slow, the round budget
			// is spent, or there is no worktree left to edit.
			break
		}
		rounds++
		app.Log.Warn("ci gate round %d/%d for %s#%d: %s", rounds, app.Cfg.MaxFixRounds, app.Repo.FullName, number, verdict.Note)
		_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join([]string{
			fmt.Sprintf(app.text(number, "ci.repair.title"), rounds, app.Cfg.MaxFixRounds),
			"",
			"- " + verdict.Note,
			"",
			app.text(number, "ci.repair.hint"),
		}, "\n"))
		before := pr.Head.SHA
		sha, changed, err := ciRepairRound(ctx, app, issue, &st, rounds, verdict)
		if err != nil {
			return verdict, rounds, err
		}
		if !changed {
			app.Log.Warn("ci gate: repair round %d produced no change", rounds)
			verdict.Note += app.text(number, "ci.no_change_suffix")
			break
		}
		// GitHub needs a moment to report the new head; a single read is not
		// enough and used to abort a repair round whose push had succeeded.
		refreshed, err := waitForPRHead(ctx, app, st.PRNumber, sha, 2*time.Minute)
		if err != nil {
			return verdict, rounds, fmt.Errorf("get PR after CI repair: %w", err)
		}
		pr = refreshed
		if pr.Head.SHA == before {
			app.Log.Warn("ci gate: repair round %d did not move the PR head", rounds)
			break
		}
		app.Log.Info("ci gate: repair round %d pushed %s", rounds, shortSHA(sha))
		waitForFreshCheckRuns(ctx, app, pr.Head.SHA, 3*time.Minute)
		verdict = waitForChecks(ctx, app, pr.Head.SHA, pr.Base.Ref, 10*time.Minute)
	}
	return verdict, rounds, nil
}

// worktreeAheadOfPR reports whether the worktree HEAD is a descendant of the
// given PR commit, i.e. it holds commits that were never pushed.
func worktreeAheadOfPR(ctx context.Context, worktree string, local *LocalRepo, branch, prSHA string) (bool, string) {
	head := headSHA(ctx, worktree, local.gitEnv())
	if head == "" || head == prSHA {
		return false, ""
	}
	if _, err := gitWorktree(ctx, worktree, local.gitEnv(), 2*time.Minute, true, "fetch", local.HTTPSURL, branch); err != nil {
		return false, ""
	}
	ancestor, err := gitWorktree(ctx, worktree, local.gitEnv(), 30*time.Second, true, "merge-base", "--is-ancestor", prSHA, "HEAD")
	if err != nil || ancestor.Code != 0 {
		return false, ""
	}
	return true, head
}

// ciRepairRound runs the coding agent once in the PR's worktree with the
// blocking checks as input, then commits and pushes the result. It returns the
// new head SHA, or "" when the agent left the tree untouched.
// ciRepairRound runs the coding agent once in the PR's worktree with the
// blocking checks as input, then commits and pushes the result. It reports
// whether the round actually produced a change; an agent that commits on its
// own is handled too.
func ciRepairRound(ctx context.Context, app *AppContext, issue Issue, st *IssueState, round int, verdict ciVerdict) (string, bool, error) {
	worktree := st.Worktree
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		return "", false, fmt.Errorf("worktree %s is gone: %w", worktree, err)
	}
	beforeHead := headSHA(ctx, worktree, app.Env())
	runDir := runDirFor(app, issue.Number)
	_ = os.MkdirAll(filepath.Join(runDir, "screenshots"), 0o755)
	runner, err := newCodingRunner(app.Repo.CodingAgent)
	if err != nil {
		return "", false, err
	}
	prompt, err := renderPrompt("ci_fix", map[string]string{
		"REPO":     app.Repo.FullName,
		"WORKTREE": worktree,
		"RUN_DIR":  runDir,
		"NUMBER":   itoa(issue.Number),
		"TITLE":    issue.Title,
		"BODY":     firstNonEmpty(issue.Body, "(no description)"),
		"PLAN":     firstNonEmpty(st.Plan, "(missing plan)"),
		"BRANCH":   st.Branch,
		"BASE":     app.Local.Base,
		"PR":       fmt.Sprintf("#%d %s", st.PRNumber, st.PRURL),
		"FAILURES": ciFailureReport(ctx, app, verdict),
		"LANGUAGE": promptLanguage(app),
	})
	if err != nil {
		return "", false, err
	}
	app.Log.Info("ci repair start for %s#%d round %d", app.Repo.FullName, issue.Number, round)
	result, err := runner.Run(ctx, RunnerOptions{
		Cwd:       worktree,
		Prompt:    prompt,
		SessionID: st.SessionID,
		Model:     app.Repo.Model,
		RunDir:    runDir,
		Env:       app.Env(),
		Timeout:   app.Cfg.ImplementTimeout,
		LogFile:   filepath.Join(runDir, fmt.Sprintf("ci-fix-%d-%s.log", round, app.Repo.CodingAgent)),
		Log:       app.Log,
	})
	if err != nil {
		return "", false, fmt.Errorf("ci repair agent: %w", err)
	}
	sessionID := firstNonEmpty(result.SessionID, st.SessionID)
	if sessionID != "" {
		st.SessionID = sessionID
		app.Store.UpdateIssue(app.Repo.FullName, issue.Number, func(state *IssueState) {
			state.SessionID = sessionID
		})
	}
	if issueCancelled(app, issue.Number) {
		return "", false, nil
	}
	dirty := gitStatus(ctx, worktree, app.Env()) != ""
	head := headSHA(ctx, worktree, app.Env())
	if !dirty && head == beforeHead {
		return "", false, nil
	}
	checkResults, ok := runChecksWithFixes(ctx, app, runner, sessionID, worktree, runDir)
	app.Store.UpdateIssue(app.Repo.FullName, issue.Number, func(state *IssueState) {
		state.Checks = checkResults
	})
	if !ok {
		return "", false, fmt.Errorf("local checks still fail after CI repair round %d", round)
	}
	if gitStatus(ctx, worktree, app.Env()) != "" {
		if err := commitAll(ctx, worktree, app.Env(), fmt.Sprintf("fix: make CI green for #%d", issue.Number)); err != nil {
			return "", false, fmt.Errorf("commit CI repair: %w", err)
		}
	}
	finalHead := headSHA(ctx, worktree, app.Env())
	if finalHead == beforeHead {
		return "", false, nil
	}
	if err := pushBranch(ctx, worktree, app.Local, st.Branch); err != nil {
		return "", false, fmt.Errorf("push CI repair: %w", err)
	}
	return finalHead, true, nil
}

// approveWaitingRuns starts workflow runs that sit at action_required, which is
// what a first-time contributor's pull request hits.
func approveWaitingRuns(ctx context.Context, app *AppContext, issue Issue, verdict ciVerdict) bool {
	approved := 0
	for _, problem := range verdict.Problems {
		if problem.RunID == 0 || problem.Detail != "action_required" {
			continue
		}
		if err := app.GH.ApproveWorkflowRun(ctx, app.Repo.FullName, problem.RunID); err != nil {
			app.Log.Warn("approve workflow run %d: %v", problem.RunID, err)
			continue
		}
		approved++
	}
	if approved == 0 {
		return false
	}
	app.Log.Info("approved %d workflow run(s) on %s#%d", approved, app.Repo.FullName, issue.Number)
	_, _ = app.GH.PostComment(ctx, app.Repo.FullName, issue.Number, app.text(issue.Number, "ci.approved", approved))
	return true
}

// waitForFreshCheckRuns gives GitHub a moment to create check runs for a fresh
// commit, so the gate does not read "not created yet" as "no CI configured".
func waitForFreshCheckRuns(ctx context.Context, app *AppContext, sha string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if runs, err := app.GH.ListCheckRuns(ctx, app.Repo.FullName, sha); err == nil && len(runs.CheckRuns) > 0 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// mergeBlockedByConflicts reports whether GitHub says the branch cannot be
// merged because it conflicts with the base branch. A draft, a missing review
// or a behind branch are not conflicts.
func mergeBlockedByConflicts(pr PullRequest) bool {
	if pr.MergeableState == "dirty" {
		return true
	}
	if pr.Mergeable == nil || *pr.Mergeable {
		return false
	}
	switch pr.MergeableState {
	case "blocked", "draft", "behind":
		return false
	}
	return true
}

// isConflictError recognizes the merge API rejecting a conflicting PR.
func isConflictError(err error) bool {
	apiErr, ok := err.(*APIError)
	if !ok {
		return false
	}
	if apiErr.Status == http.StatusConflict {
		return true
	}
	return apiErr.Status == http.StatusMethodNotAllowed &&
		strings.Contains(strings.ToLower(apiErr.Message), "conflict")
}

// waitForMergeable polls until GitHub has recomputed mergeability, so a branch
// that was just rebased is not judged by a stale "dirty".
func waitForMergeable(ctx context.Context, app *AppContext, number int, timeout time.Duration) (PullRequest, error) {
	deadline := time.Now().Add(timeout)
	for {
		pr, err := app.GH.GetPR(ctx, app.Repo.FullName, number)
		if err != nil {
			return pr, err
		}
		if pr.MergeableState != "" && pr.MergeableState != "unknown" {
			return pr, nil
		}
		if time.Now().After(deadline) {
			return pr, nil
		}
		select {
		case <-ctx.Done():
			return pr, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// resolveMergeConflicts announces and resolves one round of conflicts, then
// returns the refreshed PR. The caller decides whether to keep looping.
func resolveMergeConflicts(ctx context.Context, app *AppContext, issue Issue, st IssueState, pr PullRequest, round int) (PullRequest, error) {
	number := issue.Number
	app.Log.Warn("merge conflict for %s#%d (round %d/%d)", app.Repo.FullName, number, round, app.Cfg.MaxFixRounds)
	_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join([]string{
		fmt.Sprintf(app.text(number, "conflict.title"), round, app.Cfg.MaxFixRounds),
		"",
		fmt.Sprintf(app.text(number, "conflict.hint"), app.Local.Base, st.Branch),
	}, "\n"))
	if err := resolveConflicts(ctx, app, issue, st, round); err != nil {
		return pr, err
	}
	updated := pr
	if head := headSHA(ctx, st.Worktree, app.Env()); head != "" {
		if app.GH.HasWorkflows(ctx, app.Repo.FullName) {
			// Give GitHub a moment to create the new head's check runs, so the
			// merge gate does not read "not created yet" as "no CI".
			waitForFreshCheckRuns(ctx, app, head, 45*time.Second)
		}
		if fresh, err := waitForPRHead(ctx, app, st.PRNumber, head, 2*time.Minute); err == nil {
			updated = fresh
		}
	}
	if settled, err := waitForMergeable(ctx, app, st.PRNumber, time.Minute); err == nil {
		updated = settled
	}
	return updated, nil
}

// waitForPRHead polls until the pull request reports the commit we just pushed,
// so the CI gate does not run against the pre-resolution head.
func waitForPRHead(ctx context.Context, app *AppContext, number int, sha string, timeout time.Duration) (PullRequest, error) {
	deadline := time.Now().Add(timeout)
	for {
		pr, err := app.GH.GetPR(ctx, app.Repo.FullName, number)
		if err != nil {
			return pr, err
		}
		if pr.Head.SHA == sha {
			return pr, nil
		}
		if time.Now().After(deadline) {
			return pr, nil
		}
		select {
		case <-ctx.Done():
			return pr, ctx.Err()
		case <-time.After(3 * time.Second):
		}
	}
}

// resolveConflicts merges the base branch into the PR worktree, lets the agent
// resolve the markers, runs the checks and pushes the result.
// conflictTarget is where a conflict resolution happens and where the merged
// result is pushed. It covers both the daemon's own issue worktrees (push to
// the base repository) and pull requests from forks (push to the fork).
type conflictTarget struct {
	Number          int
	PRNumber        int
	PRURL           string
	Worktree        string
	LocalBranch     string
	RemoteURL       string
	RemoteBranch    string
	RunDir          string
	SessionID       string
	RecordChecks    bool
	Cancelled       func() bool
	RememberSession func(string)
}

func resolveConflicts(ctx context.Context, app *AppContext, issue Issue, st IssueState, round int) error {
	return resolveConflictsIn(ctx, app, conflictTarget{
		Number:       issue.Number,
		PRNumber:     st.PRNumber,
		PRURL:        st.PRURL,
		Worktree:     st.Worktree,
		LocalBranch:  st.Branch,
		RemoteURL:    app.Local.HTTPSURL,
		RemoteBranch: st.Branch,
		RunDir:       runDirFor(app, issue.Number),
		SessionID:    st.SessionID,
		RecordChecks: true,
		Cancelled:    func() bool { return issueCancelled(app, issue.Number) },
		RememberSession: func(sessionID string) {
			app.Store.UpdateIssue(app.Repo.FullName, issue.Number, func(state *IssueState) {
				state.SessionID = sessionID
			})
		},
	}, round)
}

func resolveConflictsIn(ctx context.Context, app *AppContext, target conflictTarget, round int) error {
	worktree := target.Worktree
	number := target.Number
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		return fmt.Errorf("worktree %s is gone: %w", worktree, err)
	}
	if err := fetchBase(ctx, app.Local); err != nil {
		return fmt.Errorf("fetch %s: %w", app.Local.Base, err)
	}
	merge, err := gitWorktree(ctx, worktree, app.Env(), 5*time.Minute, true, "merge", "--no-edit", "origin/"+app.Local.Base)
	if err != nil {
		return fmt.Errorf("git merge %s: %w", app.Local.Base, err)
	}
	files := conflictedFiles(ctx, worktree, app.Env())
	if merge.Code != 0 && len(files) == 0 {
		return fmt.Errorf("git merge %s: %s", app.Local.Base, tailString(merge.Stderr, 10))
	}

	runDir := target.RunDir
	runner, err := newCodingRunner(app.Repo.CodingAgent)
	if err != nil {
		return err
	}
	sessionID := target.SessionID
	if len(files) > 0 {
		app.Log.Info("resolving %d conflicted file(s) for %s#%d", len(files), app.Repo.FullName, number)
		prompt, promptErr := renderPrompt("conflict_fix", map[string]string{
			"REPO":     app.Repo.FullName,
			"BASE":     app.Local.Base,
			"NUMBER":   itoa(number),
			"PR":       fmt.Sprintf("#%d %s", target.PRNumber, target.PRURL),
			"WORKTREE": worktree,
			"RUN_DIR":  runDir,
			"FILES":    "- " + strings.Join(files, "\n- "),
			"LANGUAGE": promptLanguage(app),
		})
		if promptErr != nil {
			return promptErr
		}
		result, runErr := runner.Run(ctx, RunnerOptions{
			Cwd:       worktree,
			Prompt:    prompt,
			SessionID: sessionID,
			Model:     app.Repo.Model,
			RunDir:    runDir,
			Env:       app.Env(),
			Timeout:   app.Cfg.ImplementTimeout,
			LogFile:   filepath.Join(runDir, fmt.Sprintf("conflict-%d-%s.log", round, app.Repo.CodingAgent)),
			Log:       app.Log,
		})
		if runErr != nil {
			return fmt.Errorf("conflict agent: %w", runErr)
		}
		sessionID = firstNonEmpty(result.SessionID, sessionID)
		if sessionID != "" && target.RememberSession != nil {
			target.RememberSession(sessionID)
		}
		if remaining := conflictedFiles(ctx, worktree, app.Env()); len(remaining) > 0 {
			return fmt.Errorf("conflicts remain after the agent: %s", strings.Join(remaining, ", "))
		}
		if target.Cancelled != nil && target.Cancelled() {
			return nil
		}
	}

	checkResults, ok := runChecksWithFixes(ctx, app, runner, sessionID, worktree, runDir)
	if target.RecordChecks {
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.Checks = checkResults
		})
	}
	if !ok {
		return fmt.Errorf("checks failed after resolving conflicts with %s", app.Local.Base)
	}
	if err := commitAll(ctx, worktree, app.Env(), fmt.Sprintf("chore: resolve conflicts with %s for #%d", app.Local.Base, number)); err != nil {
		return fmt.Errorf("commit conflict resolution: %w", err)
	}
	if err := pushBranchTo(ctx, worktree, target.RemoteURL, app.Env(), target.LocalBranch, target.RemoteBranch); err != nil {
		return fmt.Errorf("push conflict resolution: %w", err)
	}
	return nil
}
func doMerge(ctx context.Context, app *AppContext, trigger Trigger, issue Issue, st IssueState) {
	number := issue.Number
	app.Log.Info("merge start for %s#%d (PR %d)", app.Repo.FullName, number, st.PRNumber)
	react(ctx, app, trigger, "hooray")
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "merging"
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, workingLabel)

	pr, err := app.GH.GetPR(ctx, app.Repo.FullName, st.PRNumber)
	if err != nil {
		blockIssue(ctx, app, number, fmt.Errorf("get PR: %w", err))
		return
	}
	if pr.State != "open" {
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) { state.Phase = "done" })
		app.GH.SetAgentState(ctx, app.Repo.FullName, number, "")
		return
	}

	conflictRounds := 0
	retryAfterConflict := false
	// skipConflictCheck avoids a second resolution when GitHub still reports the
	// pre-push "dirty" state for the head we just fixed.
	skipConflictCheck := false
	for {
		if !skipConflictCheck && (retryAfterConflict || mergeBlockedByConflicts(pr)) {
			retryAfterConflict = false
			if conflictRounds >= app.Cfg.MaxFixRounds || st.Worktree == "" {
				reason := fmt.Sprintf(app.text(number, "conflict.blocked"), app.Local.Base, conflictRounds)
				app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
					state.Phase = "blocked"
					state.BlockedReason = reason
				})
				app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
				_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number,
					fmt.Sprintf("%s\n\n%s\n\n%s", app.text(number, "merge.failed.title"), reason, app.text(number, "conflict.blocked_hint")))
				return
			}
			conflictRounds++
			updated, resolveErr := resolveMergeConflicts(ctx, app, issue, st, pr, conflictRounds)
			if resolveErr != nil {
				blockIssue(ctx, app, number, resolveErr)
				return
			}
			pr = updated
			if pr.State != "open" {
				app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) { state.Phase = "done" })
				app.GH.SetAgentState(ctx, app.Repo.FullName, number, "")
				return
			}
			skipConflictCheck = true
			continue
		}
		skipConflictCheck = false

		if pr.Head.SHA != "" {
			verdict, rounds, repairErr := ciGateWithRepairs(ctx, app, issue, st, pr)
			if repairErr != nil {
				blockIssue(ctx, app, number, repairErr)
				return
			}
			if !verdict.OK {
				reason := verdict.Note
				if rounds > 0 {
					reason += app.text(number, "ci.rounds_suffix", rounds)
				}
				app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
					state.Phase = "blocked"
					state.BlockedReason = reason
					state.BlockedByCI = true
				})
				app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
				_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number,
					fmt.Sprintf("%s\n\n%s\n\n- %s\n\n%s", app.text(number, "ci.blocked.title"), app.text(number, "ci.blocked.body"), reason, app.text(number, "ci.blocked.hint")))
				return
			}
			app.Log.Info("merge gate: %s (repair rounds: %d)", verdict.Note, rounds)
			if rounds > 0 {
				_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number,
					fmt.Sprintf("%s\n\n%s", app.text(number, "merge.ci_fixed"), app.text(number, "ci.fixed.body", rounds)))
			}
			if refreshed, refreshErr := app.GH.GetPR(ctx, app.Repo.FullName, st.PRNumber); refreshErr == nil {
				pr = refreshed
			}
		}

		merged, mergeErr := app.GH.MergePR(ctx, app.Repo.FullName, st.PRNumber)
		if mergeErr == nil && merged.Merged {
			break
		}
		if isConflictError(mergeErr) && st.Worktree != "" && conflictRounds < app.Cfg.MaxFixRounds {
			// The base branch moved between the mergeability check and the
			// merge: resolve and try again instead of parking the issue.
			app.Log.Warn("merge of %s#%d hit conflicts at merge time; resolving", app.Repo.FullName, number)
			retryAfterConflict = true
			continue
		}
		detail := merged.Message
		if mergeErr != nil {
			detail = mergeErr.Error()
		}
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.Phase = "blocked"
			state.BlockedReason = sanitize(detail)
		})
		app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
		_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number,
			fmt.Sprintf("%s\n\n```\n%s\n```\n\n%s", app.text(number, "merge.failed.title"), sanitize(detail), app.text(number, "merge.failed.hint")))
		return
	}

	_ = app.GH.DeleteBranch(ctx, app.Repo.FullName, st.Branch)
	current, err := app.GH.GetIssue(ctx, app.Repo.FullName, number)
	if err == nil && current.State == "open" {
		_ = app.GH.UpdateIssue(ctx, app.Repo.FullName, number, map[string]any{"state": "closed", "state_reason": "completed"})
	}
	if st.Worktree != "" {
		removeWorktree(ctx, app.Local, st.Worktree, true)
		deleteLocalBranch(ctx, app.Local, st.Branch)
	}

	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "done"
		state.MergedAt = nowISO()
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, "")
	_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join([]string{
		app.text(number, "merge.done.title"),
		"",
		fmt.Sprintf(app.text(number, "merge.done.pr"), st.PRNumber, app.Local.Base),
		fmt.Sprintf(app.text(number, "merge.done.branch"), st.Branch),
		"",
		app.text(number, "merge.done.footer"),
	}, "\n"))
	app.Log.Info("merged %s#%d", app.Repo.FullName, number)
}

func doCloseIssue(ctx context.Context, app *AppContext, issue Issue, st IssueState) {
	number := issue.Number
	app.Log.Info("issue %s#%d closed; closing PR and cleaning up", app.Repo.FullName, number)
	if st.PRNumber > 0 {
		_ = app.GH.ClosePR(ctx, app.Repo.FullName, st.PRNumber)
		if st.Branch != "" {
			_ = app.GH.DeleteBranch(ctx, app.Repo.FullName, st.Branch)
		}
	}
	if st.Worktree != "" {
		removeWorktree(ctx, app.Local, st.Worktree, true)
		if st.Branch != "" {
			deleteLocalBranch(ctx, app.Local, st.Branch)
		}
	}
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "cancelled"
		state.CancelledAt = nowISO()
		state.IssueClosed = true
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, "")
}

func issueCancelled(app *AppContext, number int) bool {
	return app.Store.Issue(app.Repo.FullName, number).Phase == "cancelled"
}

func doCancel(ctx context.Context, app *AppContext, trigger Trigger, issue Issue, st IssueState) {
	number := issue.Number
	app.Log.Info("cancel for %s#%d", app.Repo.FullName, number)
	react(ctx, app, trigger, "confused")
	if st.Worktree != "" {
		removeWorktree(ctx, app.Local, st.Worktree, false)
		deleteLocalBranch(ctx, app.Local, st.Branch)
	}
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "cancelled"
		state.CancelledAt = nowISO()
		state.IssueClosed = false
		state.Worktree = ""
		state.Branch = ""
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, "")
}

// postStatusComment posts a "needs you" message and remembers it, so a 👍 on it
// retries the task.
func postStatusComment(ctx context.Context, app *AppContext, number int, body string) {
	posted, err := app.GH.PostComment(ctx, app.Repo.FullName, number, body)
	if err != nil {
		app.Log.Warn("post status comment for %s#%d: %v", app.Repo.FullName, number, err)
		return
	}
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.StatusCommentID = posted.ID
	})
}

func blockIssue(ctx context.Context, app *AppContext, number int, err error) {
	if issueCancelled(app, number) {
		app.Log.Debug("suppress error for cancelled %s#%d: %v", app.Repo.FullName, number, err)
		return
	}
	message := sanitize(err.Error())
	app.Log.Error("job for %s#%d failed: %s", app.Repo.FullName, number, message)
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "blocked"
		state.BlockedReason = message
		state.BlockedByCI = false
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
	postStatusComment(ctx, app, number,
		fmt.Sprintf("%s\n\n```\n%s\n```\n\n%s", app.text(number, "error.title"), tailString(message, 12),
			fmt.Sprintf(app.text(number, "error.hint"), app.AgentLogin)))
}

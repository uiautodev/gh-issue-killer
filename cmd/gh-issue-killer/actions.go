package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type AppContext struct {
	Cfg        *Config
	Repo       RepoConfig
	RepoInfo   RepoInfo
	Local      *LocalRepo
	Identity   User
	Token      string
	Proxy      *Proxy
	GH         *GH
	Store      *Store
	Log        *Logger
	Env        []string
	AgentLogin string
}

// projectURL is shown on plan comments so readers can find the project.
const projectURL = "https://github.com/uiautodev/gh-issue-killer"

type Trigger struct {
	Type        string
	IssueNumber int
	CommentID   int64
	CommentBody string
	Sender      string
	Key         string
	DeliveryID  string
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
		Env:     app.Env,
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
		plan = plan[:55000] + "\n\n…(截断)"
	}
	if issueCancelled(app, number) {
		app.Log.Debug("skip plan comment for cancelled %s#%d", app.Repo.FullName, number)
		return
	}

	body := strings.Join([]string{
		"### 🤖 修复计划",
		"",
		plan,
		"",
		"---",
		fmt.Sprintf("🤖 Generated by [gh-issue-killer](%s) · Reply `@%s lgtm` / `@%s continue`, or add 👍 to this comment, to proceed.", projectURL, app.AgentLogin, app.AgentLogin),
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
		st.Plan = plan
		st.PlanCommentID = posted.ID
		st.PRCommentID = 0
		st.SessionID = result.SessionID
		st.LastCommentID = lastComment
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
	app.Log.Info("plan posted for %s#%d (comment %d)", app.Repo.FullName, number, posted.ID)
}

func runChecksWithFixes(ctx context.Context, app *AppContext, runner CodingRunner, sessionID, worktree, runDir string) ([]CheckResult, bool) {
	checks := loadChecks(worktree, runDir, app.Log)
	checkResults, ok := runChecks(ctx, checks, worktree, runDir, app.Env, app.Log)
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
			Env:       app.Env,
			Timeout:   app.Cfg.ImplementTimeout,
			LogFile:   filepath.Join(runDir, fmt.Sprintf("fix-%d-%s.log", attempt, app.Repo.CodingAgent)),
			Log:       app.Log,
		}); runErr != nil {
			app.Log.Error("fix round %d failed: %v", attempt, runErr)
		}
		checks = loadChecks(worktree, runDir, app.Log)
		checkResults, ok = runChecks(ctx, checks, worktree, runDir, app.Env, app.Log)
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
		"### 🤖 检查未通过，已停止",
		"",
		checksSummary(checkResults),
		"",
		strings.Join(details, "\n"),
		"",
		fmt.Sprintf("worktree 保留在 `%s`。修复后回复 `@%s continue` 重跑。", worktree, app.AgentLogin),
	}, "\n")
	_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number, body)
	app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
		state.Phase = "blocked"
		state.BlockedReason = "checks failed"
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
		Env:       app.Env,
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

	checkResults, ok := runChecksWithFixes(ctx, app, runner, sessionID, worktree, runDir)
	if !ok {
		reportCheckFailures(ctx, app, number, worktree, checkResults)
		return
	}
	if err := commitAll(ctx, worktree, app.Env, fmt.Sprintf("chore: apply feedback for #%d", number)); err != nil {
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
		fmt.Sprintf("### 🤖 已按评论更新 %s", st.PRURL),
		"",
		firstNonEmpty(manifest.Summary, "改动已推送到原分支。"),
		"",
		checksSummary(checkResults),
	}
	if shotsMD := screenshotsMarkdown(shots); shotsMD != "" {
		body = append(body, "", "## 截图", "", shotsMD)
	}
	body = append(body, "", fmt.Sprintf("确认无误回复 `@%s 合并`，或给这条评论加 👍，我会 squash merge 到主干并关闭本 issue。", app.AgentLogin))
	if posted, err := app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join(body, "\n")); err == nil {
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.PRCommentID = posted.ID
		})
	} else {
		app.Log.Warn("post revision comment: %v", err)
	}
	app.Log.Info("revision pushed for %s#%d (PR %d)", app.Repo.FullName, number, st.PRNumber)
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
		"REPO":     app.Repo.FullName,
		"WORKTREE": worktree,
		"BASE":     app.Local.Base,
		"RUN_DIR":  runDir,
		"NUMBER":   itoa(number),
		"TITLE":    issue.Title,
		"BODY":     firstNonEmpty(issue.Body, "(no description)"),
		"COMMENTS": commentsBlock(comments),
		"PLAN":     firstNonEmpty(st.Plan, "(missing plan, implement the issue directly)"),
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
		Env:     app.Env,
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

	checkResults, ok := runChecksWithFixes(ctx, app, runner, sessionID, worktree, runDir)
	if !ok {
		reportCheckFailures(ctx, app, number, worktree, checkResults)
		return
	}

	if err := commitAll(ctx, worktree, app.Env, fmt.Sprintf("chore: finalize changes for #%d", number)); err != nil {
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
		uiNote = "未涉及 UI 改动，无截图。"
	} else if len(shots) == 0 {
		uiNote = "⚠️ 本次改动涉及 UI，但没有产出 before/after 截图。"
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
	prParts := []string{"This is by an automated agent, not a human.", ""}
	if manifest.Summary != "" {
		prParts = append(prParts, "## 变更", "", manifest.Summary, "")
	}
	if len(manifest.Changes) > 0 {
		prParts = append(prParts, "### 改动")
		for _, change := range manifest.Changes {
			prParts = append(prParts, "- "+change)
		}
		prParts = append(prParts, "")
	}
	prParts = append(prParts, fmt.Sprintf("Closes #%d", number), "", "## 提交", "```", firstNonEmpty(commits, "(none)"), "```", "", "## 检查", checksSummary(checkResults), "")
	if len(manifest.Tests) > 0 {
		prParts = append(prParts, "## 功能验证")
		for _, test := range manifest.Tests {
			prParts = append(prParts, "- "+test)
		}
		prParts = append(prParts, "")
	}
	if shotsMD != "" {
		prParts = append(prParts, "## 截图", "", shotsMD, "")
	}
	if uiNote != "" {
		prParts = append(prParts, "## UI 验证", "", uiNote, "")
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

	commentParts := []string{fmt.Sprintf("### 🤖 实现完成：%s", pr.HTMLURL), "", manifest.Summary, "", checksSummary(checkResults)}
	if shotsMD != "" {
		commentParts = append(commentParts, "", "## 截图", "", shotsMD)
	}
	if uiNote != "" {
		commentParts = append(commentParts, "", "## UI 验证", "", uiNote)
	}
	commentParts = append(commentParts, "", fmt.Sprintf("确认无误回复 `@%s 合并`，或给这条评论加 👍，我会 squash merge 到主干并关闭本 issue。", app.AgentLogin))
	if posted, err := app.GH.PostComment(ctx, app.Repo.FullName, number, strings.Join(commentParts, "\n")); err == nil {
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.PRCommentID = posted.ID
		})
	}
	app.Log.Info("PR opened for %s#%d: %s", app.Repo.FullName, number, pr.HTMLURL)
}

func waitForChecks(ctx context.Context, app *AppContext, sha string, timeout time.Duration) (bool, string) {
	started := time.Now()
	for {
		runs, err := app.GH.ListCheckRuns(ctx, app.Repo.FullName, sha)
		if err != nil {
			return true, "no check-runs access, skipping CI gate"
		}
		if runs.TotalCount == 0 || len(runs.CheckRuns) == 0 {
			return true, "no CI configured"
		}
		pending := 0
		failed := make([]string, 0)
		for _, run := range runs.CheckRuns {
			if run.Status != "completed" {
				pending++
				continue
			}
			conclusion := ""
			if run.Conclusion != nil {
				conclusion = *run.Conclusion
			}
			switch conclusion {
			case "success", "neutral", "skipped":
			default:
				failed = append(failed, run.Name+": "+conclusion)
			}
		}
		if pending == 0 {
			if len(failed) > 0 {
				return false, strings.Join(failed, ", ")
			}
			return true, fmt.Sprintf("%d checks passed", len(runs.CheckRuns))
		}
		if time.Since(started) > timeout {
			return false, fmt.Sprintf("checks still pending after %d min", int(timeout.Minutes()))
		}
		app.Log.Info("waiting for %d checks...", pending)
		select {
		case <-ctx.Done():
			return false, "cancelled"
		case <-time.After(15 * time.Second):
		}
	}
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

	if pr.Head.SHA != "" {
		ok, note := waitForChecks(ctx, app, pr.Head.SHA, 10*time.Minute)
		if !ok {
			app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
				state.Phase = "blocked"
				state.BlockedReason = note
			})
			app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
			_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number,
				fmt.Sprintf("### 🤖 暂不合并\n\nCI 未通过或仍在等待：%s\n\n解决后回复 **合并** 重试。", note))
			return
		}
		app.Log.Info("merge gate: %s", note)
	}

	merged, err := app.GH.MergePR(ctx, app.Repo.FullName, st.PRNumber)
	if err != nil || !merged.Merged {
		detail := merged.Message
		if err != nil {
			detail = err.Error()
		}
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.Phase = "blocked"
			state.BlockedReason = sanitize(detail)
		})
		app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
		_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number,
			fmt.Sprintf("### 🤖 合并失败\n\n```\n%s\n```\n\n处理冲突后回复 **合并** 重试。", sanitize(detail)))
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
		"### 🤖 已完成",
		"",
		fmt.Sprintf("- PR #%d 已 squash merge 到 `%s`", st.PRNumber, app.Local.Base),
		fmt.Sprintf("- 分支 `%s` 已删除，worktree 已清理", st.Branch),
		"",
		"本 issue 由自动化流程关闭。",
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
	})
	app.GH.SetAgentState(ctx, app.Repo.FullName, number, awaitingLabel)
	_, _ = app.GH.PostComment(ctx, app.Repo.FullName, number,
		fmt.Sprintf("### 🤖 流程出错\n\n```\n%s\n```\n\n回复 `@%s continue` 重试。", tailString(message, 12), app.AgentLogin))
}

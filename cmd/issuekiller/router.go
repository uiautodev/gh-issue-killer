package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

func isMention(text, login string) bool {
	if text == "" || login == "" {
		return false
	}
	pattern := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(login) + `([^A-Za-z0-9-]|$)`)
	return pattern.MatchString(text)
}

// stripQuotedLines drops the lines a GitHub quote-reply wraps in "> " (and the
// blank lines around them), so the daemon parses what the author actually
// wrote instead of the quoted text. A quote-reply that ends in
// "@agent continue" therefore parses as a command, and a mention that only
// exists inside the quote does not count as addressing the agent. A line
// that merely looks like a quote (">5", a shell redirect) is dropped from
// parsing too; the full body is still what the coding agent sees in the
// issue comments, so nothing is lost there.
func stripQuotedLines(body string) string {
	lines := strings.Split(body, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), ">") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Trim(strings.Join(kept, "\n"), " \t\r\n")
}

// resolveCommand returns the command a comment carries. Only approvers can
// command the agent, the comment must mention it, and the author's own words
// (quoted lines stripped) must be the command itself. Prose like
// "把按钮合并到 input-group 里" therefore never merges a PR by accident.
func resolveCommand(body string, isApprover bool, agentLogin string) string {
	body = stripQuotedLines(body)
	if !isApprover || !isMention(body, agentLogin) {
		return ""
	}
	text := strings.ToLower(stripMention(body, agentLogin))
	text = strings.TrimFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, filler := range []string{"吧", "了", "谢谢", "thanks", "please", "thx"} {
		text = strings.TrimSpace(strings.TrimSuffix(text, filler))
	}
	switch text {
	case "lgtm", "continue", "继续":
		return "approve"
	case "合并", "merge", "ship":
		return "merge"
	case "cancel", "stop", "停止":
		return "cancel"
	case "review", "review pr", "cr", "审查":
		return "review"
	case "apply", "apply review", "应用":
		return "apply"
	}
	return ""
}

// approvalWords are the English words that approve a plan when they open a
// comment. The rest of the comment becomes extra instructions for the coding
// stage.
var approvalWords = []string{"okay", "continue", "approve", "lgtm", "yes", "ok", "go"}

// approvalWithNotes recognizes "<approval word> <extra instructions>" after
// the mention. The word must be the first token and must not run into another
// Latin word ("good" is not "go").
func approvalWithNotes(body, agentLogin string) (bool, string) {
	body = stripQuotedLines(body)
	if !isMention(body, agentLogin) {
		return false, ""
	}
	text := strings.TrimSpace(stripMention(body, agentLogin))
	text = strings.TrimLeft(text, " \t\r\n!！。.,，~～…、:：;；-—")
	lower := strings.ToLower(text)
	for _, word := range approvalWords {
		if !strings.HasPrefix(lower, word) {
			continue
		}
		rest := text[len(word):]
		if rest != "" {
			next := []rune(rest)[0]
			if next < 128 && (next == '_' || (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') || (next >= '0' && next <= '9')) {
				continue
			}
		}
		notes := strings.TrimLeft(rest, " \t\r\n!！。.,，~～…、:：;；-—")
		return true, strings.TrimRight(notes, " \t\r\n")
	}
	return false, ""
}

func stripMention(text, login string) string {
	if login == "" {
		return text
	}
	pattern := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(login))
	return pattern.ReplaceAllString(text, " ")
}

// looksLikeQuestion is the heuristic the fall-through reply uses to tell a
// question ("why did it fail?") from an instruction ("also fix the
// timeout"). It only decides which canned reply fits; it never starts work.
func looksLikeQuestion(text string) bool {
	trimmed := strings.TrimSpace(text)
	if strings.Contains(trimmed, "?") || strings.Contains(trimmed, "？") {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"为什么", "怎么", "怎样", "如何", "什么", "为啥",
		"why", "how", "what", "when", "where", "who", "which",
		"can ", "could ", "should ", "is ", "are ", "do ", "does ", "did "} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	for _, suffix := range []string{"吗", "呢", "么"} {
		if strings.HasSuffix(trimmed, suffix) {
			return true
		}
	}
	return false
}

// isAgentComment reports whether a comment is daemon output rather than a
// command. Comments written before the marker existed all start with the
// bot heading.
func isAgentComment(body string) bool {
	return hasAgentMarker(body) ||
		strings.HasPrefix(strings.TrimSpace(body), "### 🤖")
}

const bugLabel = "bug"

func hasLabel(issue Issue, name string) bool {
	for _, label := range issue.Labels {
		if strings.EqualFold(label.Name, name) {
			return true
		}
	}
	return false
}

func isRelevantIssue(issue Issue, agentLogin string) bool {
	if issue.PullRequest != nil {
		return false
	}
	if strings.EqualFold(issue.User.Login, agentLogin) {
		return false
	}
	for _, assignee := range issue.Assignees {
		if strings.EqualFold(assignee.Login, agentLogin) {
			return true
		}
	}
	return isMention(issue.Title+"\n"+issue.Body, agentLogin)
}

type jobStage int

const (
	stagePlan jobStage = iota
	stageWork
	stageCI
)

// ciWorkers is how many pull-request CI settles may run per repository. They
// live on their own queue so a slow CI never occupies an implementation slot.
const ciWorkers = 2

type routerJob struct {
	key string
	ctx context.Context
	fn  func(context.Context)
}

type repoPool struct {
	plan chan routerJob
	work chan routerJob
	ci   chan routerJob
}

type Router struct {
	cfg        *Config
	contexts   map[string]*AppContext
	agentLogin string
	store      *Store
	log        *Logger
	pools      map[string]*repoPool
	mu         sync.Mutex
	active     map[string]bool
	queued     map[string]bool
	cancels    map[string]context.CancelFunc
	started    map[string]time.Time
}

func NewRouter(cfg *Config, contexts map[string]*AppContext, agentLogin string, store *Store, log *Logger) *Router {
	router := &Router{
		cfg:        cfg,
		contexts:   contexts,
		agentLogin: agentLogin,
		store:      store,
		log:        log,
		pools:      map[string]*repoPool{},
		active:     map[string]bool{},
		queued:     map[string]bool{},
		cancels:    map[string]context.CancelFunc{},
		started:    map[string]time.Time{},
	}
	planWorkers := cfg.PlanParallel
	if planWorkers < 1 {
		planWorkers = 1
	}
	workWorkers := cfg.ImplementParallel
	if workWorkers < 1 {
		workWorkers = 1
	}
	// One queue pair per repository: a long implementation in one repo no
	// longer blocks another repo's work.
	for fullName := range contexts {
		pool := &repoPool{
			plan: make(chan routerJob, 32),
			work: make(chan routerJob, 32),
			ci:   make(chan routerJob, 32),
		}
		router.pools[fullName] = pool
		for i := 0; i < planWorkers; i++ {
			go router.worker(pool.plan)
		}
		for i := 0; i < workWorkers; i++ {
			go router.worker(pool.work)
		}
		for i := 0; i < ciWorkers; i++ {
			go router.worker(pool.ci)
		}
	}
	for fullName, app := range contexts {
		app.EnqueueCI = router.ciSettler(fullName, app)
	}
	return router
}

// ciSettler returns the hook the coding stages call after a push: the CI wait
// runs as its own job on the ci queue, so the implementation worker is free for
// the next issue while checks are running or being repaired.
func (r *Router) ciSettler(fullName string, app *AppContext) func(number int) {
	return func(number int) {
		if r.store == nil {
			return
		}
		key := fmt.Sprintf("%s#%d#ci", fullName, number)
		r.enqueue(fullName, key, stageCI, func(jobCtx context.Context) {
			st := r.store.Issue(fullName, number)
			if st.Phase != "pr_open" || st.PRNumber == 0 {
				return
			}
			issue, err := app.GH.GetIssue(jobCtx, fullName, number)
			if err != nil || issue.State != "open" {
				return
			}
			pr, err := app.GH.GetPR(jobCtx, fullName, st.PRNumber)
			if err != nil || pr.State != "open" {
				return
			}
			if ready, rounds := settleCIGate(jobCtx, app, issue, pr); ready {
				postMergePrompt(jobCtx, app, number, rounds)
			}
		})
	}
}

func (r *Router) worker(jobs <-chan routerJob) {
	for job := range jobs {
		r.mu.Lock()
		delete(r.queued, job.key)
		r.active[job.key] = true
		r.started[job.key] = time.Now()
		r.mu.Unlock()
		r.log.Debug("job start %s", job.key)
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					r.log.Error("job %s panicked: %v", job.key, recovered)
				}
			}()
			job.fn(job.ctx)
		}()
		r.mu.Lock()
		delete(r.active, job.key)
		delete(r.cancels, job.key)
		delete(r.started, job.key)
		r.mu.Unlock()
		r.log.Debug("job done %s", job.key)
	}
}

func (r *Router) enqueue(fullName, key string, stage jobStage, fn func(context.Context)) {
	jobCtx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	if r.active[key] || r.queued[key] {
		r.mu.Unlock()
		cancel()
		r.log.Debug("skip duplicate job %s", key)
		return
	}
	r.queued[key] = true
	r.cancels[key] = cancel
	r.mu.Unlock()
	r.log.Debug("enqueue job %s", key)
	pool := r.pools[fullName]
	if pool == nil {
		cancel()
		r.mu.Lock()
		delete(r.queued, key)
		delete(r.cancels, key)
		r.mu.Unlock()
		r.log.Warn("no job pool for repo %s; dropping %s", fullName, key)
		return
	}
	queue := pool.work
	switch stage {
	case stagePlan:
		queue = pool.plan
	case stageCI:
		queue = pool.ci
	}
	queue <- routerJob{key: key, ctx: jobCtx, fn: fn}
}

func (r *Router) cancelActive(fullName string, number int) {
	// Every key that can hold an agent for the issue: the plan/work job, its
	// CI settle, and the plan-skipping bug/build starts.
	for _, key := range issueJobKeys(fullName, number) {
		r.mu.Lock()
		cancel := r.cancels[key]
		r.mu.Unlock()
		if cancel != nil {
			r.log.Debug("cancel active job %s", key)
			cancel()
		}
	}
}

// issueJobKeys lists every job key that may hold an agent for one issue.
func issueJobKeys(fullName string, number int) []string {
	key := fmt.Sprintf("%s#%d", fullName, number)
	return []string{key, key + "#ci", key + "#bug", key + "#build"}
}

// waitCISettle makes a merge wait for the issue's background CI job, so two
// agents never edit the same worktree at once.
func (r *Router) waitCISettle(ctx context.Context, fullName string, number int) {
	key := fmt.Sprintf("%s#%d#ci", fullName, number)
	r.mu.Lock()
	active := r.active[key] || r.queued[key]
	r.mu.Unlock()
	if active {
		r.log.Info("merge for %s#%d waits for the running CI job", fullName, number)
	}
	r.waitInactive(ctx, key, 20*time.Minute)
}

// PoolDepth is the number of jobs waiting in one repository's queues.
type PoolDepth struct {
	Plan int
	Work int
	CI   int
}

type RouterSnapshot struct {
	Active map[string]bool
	Queued map[string]bool
	Pools  map[string]PoolDepth
}

// Snapshot copies the live scheduler state for the web dashboard.
func (r *Router) Snapshot() RouterSnapshot {
	r.mu.Lock()
	active := make(map[string]bool, len(r.active))
	for key := range r.active {
		active[key] = true
	}
	queued := make(map[string]bool, len(r.queued))
	for key := range r.queued {
		queued[key] = true
	}
	r.mu.Unlock()
	pools := make(map[string]PoolDepth, len(r.pools))
	for name, pool := range r.pools {
		pools[name] = PoolDepth{Plan: len(pool.plan), Work: len(pool.work), CI: len(pool.ci)}
	}
	return RouterSnapshot{Active: active, Queued: queued, Pools: pools}
}

// Idle reports whether no job is running or queued.
func (r *Router) Idle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active) == 0 && len(r.queued) == 0
}

func (r *Router) waitInactive(ctx context.Context, key string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		active := r.active[key]
		queued := r.queued[key]
		r.mu.Unlock()
		if !active && !queued {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func isApproverLogin(login string, approvers []string) bool {
	for _, candidate := range approvers {
		if strings.EqualFold(candidate, login) {
			return true
		}
	}
	return false
}

func hasThumbsUp(reactions []Reaction, login string) bool {
	for _, reaction := range reactions {
		if reaction.Content == "+1" && strings.EqualFold(reaction.User.Login, login) {
			return true
		}
	}
	return false
}

// HandleReaction turns an approver's 👍 into an action: on the plan comment
// it approves the plan, on the PR-announcement comment it merges. The
// reaction is re-read from GitHub so a relay cannot forge it.
func (r *Router) HandleReaction(ctx context.Context, fullName string, number int, commentID int64, reactor, content string) {
	app := r.contexts[fullName]
	if app == nil || number == 0 || commentID == 0 || content != "+1" {
		return
	}
	st := r.store.Issue(fullName, number)
	statusID := st.StatusCommentID
	if statusID == 0 && st.Phase == "blocked" {
		statusID = r.latestStatusCommentID(ctx, app, number)
	}
	if statusID != 0 && statusID == commentID {
		if !isApproverLogin(reactor, app.Repo.Approvers) && !strings.EqualFold(reactor, r.agentLogin) {
			r.log.Debug("ignore 👍 from non-approver @%s on %s#%d", reactor, fullName, number)
			return
		}
		login, ok := r.commentThumbsUp(ctx, app, commentID)
		if !ok {
			r.log.Debug("👍 from @%s not on comment %d yet", reactor, commentID)
			return
		}
		r.ackReaction(ctx, app, commentID)
		app.Store.UpdateIssue(fullName, number, func(state *IssueState) {
			state.StatusCommentID = 0
		})
		r.log.Info("retry accepted by 👍 from @%s on %s#%d", login, fullName, number)
		r.enqueueApprove(ctx, app, fullName, number, login)
		return
	}
	if st.ReviewCommentID != 0 && st.ReviewCommentID == commentID {
		if !isApproverLogin(reactor, app.Repo.Approvers) && !strings.EqualFold(reactor, r.agentLogin) {
			r.log.Debug("ignore 👍 from non-approver @%s on %s#%d", reactor, fullName, number)
			return
		}
		login, ok := r.commentThumbsUp(ctx, app, commentID)
		if !ok {
			r.log.Debug("👍 from @%s not on comment %d yet", reactor, commentID)
			return
		}
		r.ackReaction(ctx, app, commentID)
		r.log.Info("review accepted by 👍 from @%s on %s#%d; applying the findings", login, fullName, number)
		r.enqueueReviewFix(ctx, app, fullName, number, login)
		return
	}
	if st.PlanCommentID != commentID && st.PRCommentID != commentID {
		r.log.Debug("ignore 👍 on %s#%d comment %d (phase=%s)", fullName, number, commentID, firstNonEmpty(st.Phase, "none"))
		return
	}
	if !isApproverLogin(reactor, app.Repo.Approvers) && !strings.EqualFold(reactor, r.agentLogin) {
		r.log.Debug("ignore 👍 from non-approver @%s on %s#%d", reactor, fullName, number)
		return
	}
	login, ok := r.commentThumbsUp(ctx, app, commentID)
	if !ok {
		r.log.Debug("👍 from @%s not on comment %d yet", reactor, commentID)
		return
	}
	switch {
	case st.Phase == "awaiting_approval" && st.PlanCommentID == commentID:
		r.ackReaction(ctx, app, commentID)
		r.log.Info("plan approved by 👍 from @%s on %s#%d", login, fullName, number)
		r.enqueueApprove(ctx, app, fullName, number, login)
	case st.Phase == "pr_open" && st.PRCommentID == commentID:
		r.ackReaction(ctx, app, commentID)
		r.log.Info("merge approved by 👍 from @%s on %s#%d", login, fullName, number)
		r.enqueueMerge(app, fullName, number, login)
	default:
		r.log.Debug("ignore 👍 on %s#%d comment %d (phase=%s)", fullName, number, commentID, firstNonEmpty(st.Phase, "none"))
	}
}

// enqueueReviewFix applies the findings of the PR's latest bot review, which is
// what 👍 on the review prompt means.
func (r *Router) enqueueReviewFix(ctx context.Context, app *AppContext, fullName string, number int, login string) {
	// Forget the prompt once consumed: the 👍 stays on the comment, and the
	// poller would otherwise start the same fix on every scan.
	app.Store.UpdateIssue(fullName, number, func(state *IssueState) {
		state.ReviewCommentID = 0
	})
	key := fmt.Sprintf("%s#%d#reviewfix", fullName, number)
	r.enqueue(fullName, key, stageWork, func(jobCtx context.Context) {
		issue, err := app.GH.GetIssue(jobCtx, fullName, number)
		if err != nil || issue.State != "open" {
			return
		}
		if issue.PullRequest == nil {
			_, _ = app.GH.PostComment(jobCtx, fullName, number,
				fmt.Sprintf(app.text(number, "router.review_not_pr"), r.agentLogin))
			return
		}
		pr, err := app.GH.GetPR(jobCtx, fullName, number)
		if err != nil {
			app.Log.Warn("apply review %s#%d: %v", fullName, number, err)
			return
		}
		st := r.store.Issue(fullName, number)
		instructions, err := reviewInstructions(jobCtx, app, pr, st.ReviewID)
		if err != nil {
			app.Log.Warn("apply review %s#%d: %v", fullName, number, err)
			return
		}
		if strings.TrimSpace(instructions) == "" {
			_, _ = app.GH.PostComment(jobCtx, fullName, number, app.text(number, "review.nothing"))
			return
		}
		doModifyPR(jobCtx, app, Trigger{
			Type:        "reaction",
			IssueNumber: number,
			Sender:      login,
			Key:         fmt.Sprintf("review-%d", time.Now().UnixNano()),
		}, pr, instructions)
	})
}

// retryBlockedCI resumes a task that was parked because its pull request checks
// failed, once those checks are green. Other blocked reasons (local checks, an
// unreproducible bug, exhausted conflict rounds) stay parked for the user.
func (r *Router) retryBlockedCI(ctx context.Context, app *AppContext) {
	// Same reason as checkReactions: only a tracked, CI-blocked issue can be
	// resumed, so the scan starts from the state instead of listing the repo.
	for _, number := range app.Store.IssueNumbers(app.Repo.FullName) {
		st := r.store.Issue(app.Repo.FullName, number)
		if st.Phase != "blocked" || !st.BlockedByCI || st.PRNumber == 0 {
			continue
		}
		pr, err := app.GH.GetPR(ctx, app.Repo.FullName, st.PRNumber)
		if err != nil || pr.State != "open" || mergeBlockedByConflicts(pr) {
			continue
		}
		runs, err := app.GH.ListCheckRuns(ctx, app.Repo.FullName, pr.Head.SHA)
		if err != nil || !checksAllGreen(runs) {
			continue
		}
		r.log.Info("blocked task %s#%d has green checks; retrying the merge", app.Repo.FullName, number)
		app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
			state.BlockedByCI = false
		})
		r.enqueue(app.Repo.FullName, fmt.Sprintf("%s#%d", app.Repo.FullName, number), stageWork, func(jobCtx context.Context) {
			issue, err := app.GH.GetIssue(jobCtx, app.Repo.FullName, number)
			if err != nil || issue.State != "open" {
				return
			}
			current := r.store.Issue(app.Repo.FullName, number)
			if current.Phase != "blocked" || current.PRNumber == 0 {
				return
			}
			doMerge(jobCtx, app, Trigger{
				Type:        "retry",
				IssueNumber: number,
				Sender:      r.agentLogin,
				Key:         fmt.Sprintf("retry-%d", time.Now().UnixNano()),
			}, issue, current)
		})
	}
}

// checksAllGreen reports whether every check run finished successfully.
func checksAllGreen(runs CheckRuns) bool {
	if len(runs.CheckRuns) == 0 {
		return false
	}
	for _, run := range runs.CheckRuns {
		if run.Status != "completed" {
			return false
		}
		conclusion := ""
		if run.Conclusion != nil {
			conclusion = *run.Conclusion
		}
		switch conclusion {
		case "success", "neutral", "skipped":
		default:
			return false
		}
	}
	return true
}

// HandlePRReviewComment turns an inline review comment on a tracked PR into a
// revision round.
func (r *Router) HandlePRReviewComment(ctx context.Context, fullName string, prNumber int, comment ReviewComment) {
	text := comment.Body
	if comment.Path != "" {
		location := comment.Path
		if comment.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, comment.Line)
		}
		text = fmt.Sprintf("%s\n\n(review comment on %s)", text, location)
	}
	r.handlePRFeedback(ctx, fullName, prNumber, comment.ID, text, comment.User.Login, true)
}

// HandlePRReview handles a review body (a review with prose but no inline
// comments).
func (r *Router) HandlePRReview(ctx context.Context, fullName string, prNumber int, review Review) {
	r.handlePRFeedback(ctx, fullName, prNumber, review.ID, review.Body, review.User.Login, false)
}

func (r *Router) handlePRFeedback(ctx context.Context, fullName string, prNumber int, id int64, body, author string, inline bool) {
	app := r.contexts[fullName]
	if app == nil || prNumber == 0 || id == 0 || strings.TrimSpace(body) == "" {
		return
	}
	number, st, ok := r.store.IssueByPR(fullName, prNumber)
	if !ok {
		r.log.Debug("review feedback on %s PR #%d does not match a tracked issue", fullName, prNumber)
		return
	}
	if !isApproverLogin(author, app.Repo.Approvers) && !strings.EqualFold(author, r.agentLogin) {
		r.log.Debug("ignore review feedback from non-approver @%s on %s#%d", author, fullName, number)
		return
	}
	if r.store.IsFeedbackProcessed(fullName, number, id) {
		return
	}
	r.store.MarkFeedbackProcessed(fullName, number, id)
	if st.Phase != "pr_open" {
		r.log.Debug("review feedback on %s#%d ignored (phase=%s)", fullName, number, firstNonEmpty(st.Phase, "none"))
		return
	}
	if inline {
		_ = app.GH.ReactReviewComment(ctx, fullName, id, "eyes")
	}
	key := fmt.Sprintf("%s#%d", fullName, number)
	trigger := Trigger{
		Type:        "review",
		IssueNumber: number,
		CommentBody: body,
		Sender:      author,
		Key:         fmt.Sprintf("review-%d", id),
	}
	r.enqueue(fullName, key, stageWork, func(jobCtx context.Context) {
		issue, err := app.GH.GetIssue(jobCtx, fullName, number)
		if err != nil {
			blockIssue(jobCtx, app, number, err)
			return
		}
		if issue.State != "open" {
			return
		}
		current := r.store.Issue(fullName, number)
		if current.Phase != "pr_open" {
			return
		}
		r.log.Info("review feedback on %s#%d; revising PR #%d", fullName, number, prNumber)
		doRevise(jobCtx, app, trigger, issue, current)
	})
}

// startBugFix hands a bug-labelled issue straight to the work pool: the plan
// stage is skipped because the bug first has to be reproduced and then fixed.
func (r *Router) startBugFix(app *AppContext, fullName string, number int, trigger Trigger) {
	app.Log.Info("issue %s#%d is labelled bug; skipping the plan", fullName, number)
	app.GH.SetAgentState(context.Background(), fullName, number, workingLabel)
	key := fmt.Sprintf("%s#%d", fullName, number)
	r.enqueue(fullName, key+"#bug", stageWork, func(jobCtx context.Context) {
		issue, err := app.GH.GetIssue(jobCtx, fullName, number)
		if err != nil {
			blockIssue(jobCtx, app, number, err)
			return
		}
		if issue.State != "open" {
			return
		}
		current := r.store.Issue(fullName, number)
		switch current.Phase {
		case "implementing", "pr_open", "done", "cancelled":
			return
		}
		doImplement(jobCtx, app, trigger, issue, current)
	})
}

// routeFirstStage maps a repository's mode and the triage answer to the first
// stage of a non-bug issue. An absent or unknown mode behaves like auto, so a
// hand-written config keeps the triage instead of failing to start.
func routeFirstStage(mode string, buildDirect bool) jobStage {
	switch effectiveIssueMode(mode) {
	case modeBuild:
		return stageWork
	case modePlan:
		return stagePlan
	}
	if buildDirect {
		return stageWork
	}
	return stagePlan
}

// startPlanOrBuild chooses the first stage for a non-bug issue. In auto mode
// the coding agent triages; a failed or unreadable triage falls back to the
// plan, which keeps the approval gate as the safe default.
func (r *Router) startPlanOrBuild(ctx context.Context, app *AppContext, trigger Trigger, issue Issue) {
	buildDirect := false
	if effectiveIssueMode(app.Repo.Mode) == modeAuto {
		buildDirect = shouldBuildDirectly(ctx, app, trigger, issue)
	}
	if routeFirstStage(app.Repo.Mode, buildDirect) == stageWork {
		r.startBuild(ctx, app, app.Repo.FullName, issue.Number, trigger)
		return
	}
	doPlan(ctx, app, trigger, issue)
}

// startBuild queues an issue straight to the work pool, skipping the plan and
// its approval comment. The phase moves before the job waits for a worker so a
// poller cannot start a second first stage in the meantime.
func (r *Router) startBuild(ctx context.Context, app *AppContext, fullName string, number int, trigger Trigger) {
	if issueCancelled(app, number) {
		return
	}
	app.Log.Info("issue %s#%d builds without a plan (mode=%s)", fullName, number, effectiveIssueMode(app.Repo.Mode))
	app.Store.UpdateIssue(fullName, number, func(state *IssueState) {
		state.Phase = "implementing"
	})
	app.GH.SetAgentState(ctx, fullName, number, workingLabel)
	key := fmt.Sprintf("%s#%d", fullName, number)
	r.enqueue(fullName, key+"#build", stageWork, func(jobCtx context.Context) {
		issue, err := app.GH.GetIssue(jobCtx, fullName, number)
		if err != nil {
			blockIssue(jobCtx, app, number, err)
			return
		}
		if issue.State != "open" {
			return
		}
		current := r.store.Issue(fullName, number)
		if current.Phase != "implementing" {
			return
		}
		doImplement(jobCtx, app, trigger, issue, current)
	})
}

// requestCancel stops the issue's job (if any) and cleans up once it is gone.
// Cancel cannot go through the queue: the running job holds the same key and
// would make the enqueue a duplicate.
func (r *Router) requestCancel(ctx context.Context, app *AppContext, fullName string, number int, trigger Trigger) {
	r.log.Info("cancel requested for %s#%d; stopping the active job", fullName, number)
	app.Store.UpdateIssue(fullName, number, func(state *IssueState) {
		state.Phase = "cancelled"
		state.CancelledAt = nowISO()
		state.IssueClosed = false
	})
	app.GH.SetAgentState(ctx, fullName, number, "")
	r.cancelActive(fullName, number)
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for _, jobKey := range issueJobKeys(fullName, number) {
			r.waitInactive(jobCtx, jobKey, 60*time.Second)
		}
		issue, err := app.GH.GetIssue(jobCtx, fullName, number)
		if err != nil {
			app.Log.Warn("cancel %s#%d: %v", fullName, number, err)
			return
		}
		doCancel(jobCtx, app, trigger, issue, r.store.Issue(fullName, number))
	}()
}

// WebApprove starts the work for an issue that waits for a human decision.
func (r *Router) WebApprove(repo string, number int) error {
	app := r.contexts[repo]
	if app == nil {
		return fmt.Errorf("unknown repository %s", repo)
	}
	st := r.store.Issue(repo, number)
	switch st.Phase {
	case "awaiting_approval":
		r.enqueueApprove(context.Background(), app, repo, number, "web")
		return nil
	case "blocked":
		key := fmt.Sprintf("%s#%d", repo, number)
		trigger := Trigger{Type: "web", IssueNumber: number, Sender: "web"}
		r.enqueue(repo, key, stageWork, func(jobCtx context.Context) {
			issue, err := app.GH.GetIssue(jobCtx, repo, number)
			if err != nil {
				blockIssue(jobCtx, app, number, err)
				return
			}
			if issue.State != "open" {
				return
			}
			current := r.store.Issue(repo, number)
			if current.Phase != "blocked" {
				return
			}
			doImplement(jobCtx, app, trigger, issue, current)
		})
		return nil
	}
	return fmt.Errorf("issue #%d is %s, not waiting for an approval", number, firstNonEmpty(st.Phase, "new"))
}

// WebCancel aborts an issue from the dashboard.
func (r *Router) WebCancel(repo string, number int) error {
	app := r.contexts[repo]
	if app == nil {
		return fmt.Errorf("unknown repository %s", repo)
	}
	st := r.store.Issue(repo, number)
	switch st.Phase {
	case "new", "planning", "awaiting_approval", "blocked", "implementing", "pr_open":
		r.requestCancel(context.Background(), app, repo, number, Trigger{Type: "web", IssueNumber: number, Sender: "web"})
		return nil
	}
	return fmt.Errorf("issue #%d is %s, nothing to cancel", number, firstNonEmpty(st.Phase, "new"))
}

func (r *Router) enqueueApprove(ctx context.Context, app *AppContext, fullName string, number int, sender string) {
	key := fmt.Sprintf("%s#%d", fullName, number)
	// The owner already approved: clear the waiting label and move the phase
	// now, even if the implementation is still queued behind another job.
	// Otherwise the issue keeps looking like it needs confirmation and the
	// poller keeps re-approving the same 👍 on every scan.
	app.GH.SetAgentState(ctx, fullName, number, workingLabel)
	app.Store.UpdateIssue(fullName, number, func(state *IssueState) {
		state.Phase = "implementing"
	})
	trigger := Trigger{
		Type:        "reaction",
		IssueNumber: number,
		Sender:      sender,
		Key:         fmt.Sprintf("approve-%d", time.Now().UnixNano()),
	}
	r.enqueue(fullName, key, stageWork, func(jobCtx context.Context) {
		issue, err := app.GH.GetIssue(jobCtx, fullName, number)
		if err != nil {
			blockIssue(jobCtx, app, number, err)
			return
		}
		if issue.State != "open" {
			return
		}
		current := r.store.Issue(fullName, number)
		if current.Phase != "implementing" {
			return
		}
		doImplement(jobCtx, app, trigger, issue, current)
	})
}

func (r *Router) enqueueMerge(app *AppContext, fullName string, number int, sender string) {
	key := fmt.Sprintf("%s#%d", fullName, number)
	// Move out of pr_open now: the merge may queue behind other work and the
	// poller must not re-merge the same 👍 every 30 s.
	app.Store.UpdateIssue(fullName, number, func(state *IssueState) {
		state.Phase = "merging"
	})
	trigger := Trigger{
		Type:        "reaction",
		IssueNumber: number,
		Sender:      sender,
		Key:         fmt.Sprintf("merge-%d", time.Now().UnixNano()),
	}
	r.enqueue(fullName, key, stageWork, func(jobCtx context.Context) {
		issue, err := app.GH.GetIssue(jobCtx, fullName, number)
		if err != nil {
			blockIssue(jobCtx, app, number, err)
			return
		}
		if issue.State != "open" {
			return
		}
		current := r.store.Issue(fullName, number)
		if current.Phase != "merging" || current.PRNumber == 0 {
			return
		}
		r.waitCISettle(jobCtx, fullName, number)
		doMerge(jobCtx, app, trigger, issue, current)
	})
}

// latestStatusCommentID returns the newest comment the daemon wrote on the
// issue. A blocked issue normally carries that id in the state, but state
// written by an older build (or a crash between posting and saving) does not,
// and the 👍 already sitting on that comment must still retry the task.
func (r *Router) latestStatusCommentID(ctx context.Context, app *AppContext, number int) int64 {
	comments, err := app.GH.ListComments(ctx, app.Repo.FullName, number)
	if err != nil {
		r.log.Debug("list comments for %s#%d: %v", app.Repo.FullName, number, err)
		return 0
	}
	for i := len(comments) - 1; i >= 0; i-- {
		if hasAgentMarker(comments[i].Body) {
			return comments[i].ID
		}
	}
	return 0
}

// ackReaction answers an accepted 👍 with 👀 so the approver sees that the
// daemon picked the reaction up immediately, before the work finishes.
func (r *Router) ackReaction(ctx context.Context, app *AppContext, commentID int64) {
	if err := app.GH.ReactComment(ctx, app.Repo.FullName, commentID, "eyes"); err != nil {
		r.log.Debug("ack 👍 on comment %d: %v", commentID, err)
	}
}

// shouldSkipComment de-duplicates comment deliveries, except when the event is
// an edit that must be reconsidered.
func (r *Router) shouldSkipComment(fullName string, trigger Trigger) bool {
	return !trigger.Reprocess && r.store.IsCommentProcessed(fullName, trigger.IssueNumber, trigger.CommentID)
}

// commentThumbsUp returns an approver who reacted 👍 to the comment.
func (r *Router) commentThumbsUp(ctx context.Context, app *AppContext, commentID int64) (string, bool) {
	if commentID == 0 {
		return "", false
	}
	reactions, err := app.GH.ListCommentReactions(ctx, app.Repo.FullName, commentID)
	if err != nil {
		r.log.Debug("list reactions for comment %d: %v", commentID, err)
		return "", false
	}
	for _, reaction := range reactions {
		if reaction.Content != "+1" {
			continue
		}
		login := reaction.User.Login
		if isApproverLogin(login, app.Repo.Approvers) || strings.EqualFold(login, r.agentLogin) {
			return login, true
		}
	}
	return "", false
}

// checkReactions is the polling fallback for 👍 approvals: reactions do not
// bump an issue's updated_at, so these issues need their own scan.
// checkPRFeedback is the polling fallback for inline review comments and
// review bodies, because GitHub's review webhooks are not always delivered.
func (r *Router) checkPRFeedback(ctx context.Context, app *AppContext) {
	for _, number := range app.Store.IssueNumbers(app.Repo.FullName) {
		st := r.store.Issue(app.Repo.FullName, number)
		if st.Phase != "pr_open" || st.PRNumber == 0 {
			continue
		}
		if comments, err := app.GH.ListReviewComments(ctx, app.Repo.FullName, st.PRNumber); err == nil {
			for _, comment := range comments {
				if r.store.IsFeedbackProcessed(app.Repo.FullName, number, comment.ID) {
					continue
				}
				r.HandlePRReviewComment(ctx, app.Repo.FullName, st.PRNumber, comment)
			}
		}
		if reviews, err := app.GH.ListReviews(ctx, app.Repo.FullName, st.PRNumber); err == nil {
			for _, review := range reviews {
				if strings.TrimSpace(review.Body) == "" || review.State == "PENDING" {
					continue
				}
				if r.store.IsFeedbackProcessed(app.Repo.FullName, number, review.ID) {
					continue
				}
				r.HandlePRReview(ctx, app.Repo.FullName, st.PRNumber, review)
			}
		}
	}
}

func (r *Router) checkReactions(ctx context.Context, app *AppContext) {
	// Only tracked issues can carry a reaction target, so walk the state instead
	// of listing every open issue. The listing itself was the daemon's biggest
	// API consumer: 7 repos x all open issues x every 30 s exhausted the token's
	// hourly quota, and a rate-limited poller cannot see a 👍 at all.
	for _, number := range app.Store.IssueNumbers(app.Repo.FullName) {
		st := r.store.Issue(app.Repo.FullName, number)
		// A reviewed PR is not in a task phase, and the reaction webhook is not
		// always delivered, so check the review prompt on every scan.
		if st.ReviewCommentID != 0 {
			if login, ok := r.commentThumbsUp(ctx, app, st.ReviewCommentID); ok {
				r.ackReaction(ctx, app, st.ReviewCommentID)
				r.log.Info("review accepted by 👍 from @%s on %s#%d (poll)", login, app.Repo.FullName, number)
				r.enqueueReviewFix(ctx, app, app.Repo.FullName, number, login)
				continue
			}
		}
		if st.Phase == "blocked" {
			statusID := st.StatusCommentID
			if statusID == 0 {
				statusID = r.latestStatusCommentID(ctx, app, number)
				if statusID != 0 {
					app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
						state.StatusCommentID = statusID
					})
				}
			}
			if login, ok := r.commentThumbsUp(ctx, app, statusID); ok {
				r.ackReaction(ctx, app, statusID)
				app.Store.UpdateIssue(app.Repo.FullName, number, func(state *IssueState) {
					state.StatusCommentID = 0
				})
				r.log.Info("retry accepted by 👍 from @%s on %s#%d (poll)", login, app.Repo.FullName, number)
				r.enqueueApprove(ctx, app, app.Repo.FullName, number, login)
				continue
			}
		}
		switch st.Phase {
		case "awaiting_approval":
			if login, ok := r.commentThumbsUp(ctx, app, st.PlanCommentID); ok {
				r.ackReaction(ctx, app, st.PlanCommentID)
				r.log.Info("plan approved by 👍 from @%s on %s#%d (poll)", login, app.Repo.FullName, number)
				r.enqueueApprove(ctx, app, app.Repo.FullName, number, login)
			}
		case "pr_open":
			if login, ok := r.commentThumbsUp(ctx, app, st.PRCommentID); ok {
				r.ackReaction(ctx, app, st.PRCommentID)
				r.log.Info("merge approved by 👍 from @%s on %s#%d (poll)", login, app.Repo.FullName, number)
				r.enqueueMerge(app, app.Repo.FullName, number, login)
			}
		}
	}
}

// wasReopened reports whether an open issue left a terminal state. A done
// issue was closed by its merge, so still being open means it was reopened.
// A cancelled issue only resumes when the user had closed it (a cancel
// comment keeps the issue open).
func wasReopened(st IssueState) bool {
	return st.Phase == "done" || (st.Phase == "cancelled" && st.IssueClosed)
}

func (r *Router) HandleTrigger(ctx context.Context, fullName string, trigger Trigger) {
	app := r.contexts[fullName]
	if app == nil {
		r.log.Warn("no context for repo %s", fullName)
		return
	}
	r.log.Debug("trigger %s type=%s issue=%d sender=%s delivery=%s", fullName, trigger.Type, trigger.IssueNumber, trigger.Sender, trigger.DeliveryID)

	if trigger.DeliveryID != "" {
		if r.store.IsDeliveryProcessed(fullName, trigger.DeliveryID) {
			r.log.Debug("skip processed delivery %s", trigger.DeliveryID)
			return
		}
		r.store.MarkDeliveryProcessed(fullName, trigger.DeliveryID)
	}
	selfComment := strings.EqualFold(trigger.Sender, r.agentLogin)
	if selfComment {
		if trigger.Type != "comment" || isAgentComment(trigger.CommentBody) {
			r.log.Debug("ignore trigger from agent itself")
			return
		}
		// The bot account is also the owner's GitHub identity in practice; a
		// comment that the daemon did not write is a human command.
		r.log.Info("comment %d from the bot account has no agent marker; treating it as a user command", trigger.CommentID)
	}

	if trigger.Type == "comment" {
		if r.shouldSkipComment(fullName, trigger) {
			r.log.Debug("skip processed comment %d", trigger.CommentID)
			return
		}
		r.store.MarkCommentProcessed(fullName, trigger.IssueNumber, trigger.CommentID)

		st := r.store.Issue(fullName, trigger.IssueNumber)
		// Only the author's own words count: quoted lines (usually the
		// bot's own failure notice, which itself contains an "@agent
		// continue" hint) must not address the agent or carry a command.
		unquoted := stripQuotedLines(trigger.CommentBody)
		mentioned := isMention(unquoted, r.agentLogin)
		isApprover := selfComment
		for _, login := range app.Repo.Approvers {
			if strings.EqualFold(login, trigger.Sender) {
				isApprover = true
				break
			}
		}
		command := resolveCommand(unquoted, isApprover, r.agentLogin)
		// 👀 means "seen": every approver comment on a tracked issue gets
		// one, and so does any @-mention from anyone else. What the bot
		// writes or does beyond that still needs an approver mention.
		if !selfComment && st.Phase != "" && (isApprover || mentioned) {
			react(ctx, app, trigger, "eyes")
		}
		r.log.Info("comment trigger %s#%d from @%s mentioned=%v command=%s", fullName, trigger.IssueNumber, trigger.Sender, mentioned, firstNonEmpty(command, "-"))

		key := fmt.Sprintf("%s#%d", fullName, trigger.IssueNumber)

		if command == "cancel" && (st.Phase == "new" || st.Phase == "planning" || st.Phase == "awaiting_approval" || st.Phase == "blocked" || st.Phase == "implementing" || st.Phase == "pr_open") {
			r.requestCancel(ctx, app, fullName, trigger.IssueNumber, trigger)
			return
		}

		if command == "merge" && (st.Phase == "pr_open" || (st.Phase == "blocked" && st.PRNumber > 0)) {
			r.enqueue(fullName, key, stageWork, func(jobCtx context.Context) {
				issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(jobCtx, app, trigger.IssueNumber, err)
					return
				}
				if issue.State != "open" {
					return
				}
				r.waitCISettle(jobCtx, fullName, trigger.IssueNumber)
				doMerge(jobCtx, app, trigger, issue, st)
			})
			return
		}

		// A merge command on a pull request the daemon does not own lands it
		// directly: resolve conflicts, run the checks and squash-merge.
		if command == "merge" {
			mergeKey := fmt.Sprintf("%s#%d#mergepr", fullName, trigger.IssueNumber)
			r.enqueue(fullName, mergeKey, stageWork, func(jobCtx context.Context) {
				issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
				if err != nil || issue.State != "open" {
					return
				}
				if issue.PullRequest == nil {
					app.Log.Info("merge command on %s#%d which is not a pull request", fullName, trigger.IssueNumber)
					return
				}
				pr, err := app.GH.GetPR(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					app.Log.Warn("merge PR %s#%d: %v", fullName, trigger.IssueNumber, err)
					return
				}
				doMergePR(jobCtx, app, trigger, pr)
			})
			return
		}

		if command == "approve" && (st.Phase == "awaiting_approval" || st.Phase == "blocked") {
			app.GH.SetAgentState(ctx, fullName, trigger.IssueNumber, workingLabel)
			app.Store.UpdateIssue(fullName, trigger.IssueNumber, func(state *IssueState) {
				state.Phase = "implementing"
			})
			r.enqueue(fullName, key, stageWork, func(jobCtx context.Context) {
				issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(jobCtx, app, trigger.IssueNumber, err)
					return
				}
				if issue.State != "open" {
					return
				}
				doImplement(jobCtx, app, trigger, issue, st)
			})
			return
		}

		if command == "apply" && isApprover {
			r.enqueueReviewFix(ctx, app, fullName, trigger.IssueNumber, trigger.Sender)
			return
		}

		if command == "review" && isApprover {
			reviewKey := fmt.Sprintf("%s#%d#review", fullName, trigger.IssueNumber)
			r.enqueue(fullName, reviewKey, stagePlan, func(jobCtx context.Context) {
				issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					app.Log.Warn("review %s#%d: %v", fullName, trigger.IssueNumber, err)
					return
				}
				if issue.State != "open" {
					return
				}
				if issue.PullRequest == nil {
					_, _ = app.GH.PostComment(jobCtx, fullName, trigger.IssueNumber,
						fmt.Sprintf(app.text(trigger.IssueNumber, "router.review_not_pr"), r.agentLogin))
					return
				}
				pr, err := app.GH.GetPR(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					app.Log.Warn("review %s#%d: get PR: %v", fullName, trigger.IssueNumber, err)
					return
				}
				doReviewPR(jobCtx, app, trigger, pr)
			})
			return
		}

		if mentioned && (st.Phase == "" || st.Phase == "new") {
			r.enqueue(fullName, key, stagePlan, func(jobCtx context.Context) {
				issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(jobCtx, app, trigger.IssueNumber, err)
					return
				}
				if issue.State != "open" {
					return
				}
				if issue.PullRequest != nil {
					if !mentioned || !isApprover {
						app.Log.Info("ignoring comment on pull request %s#%d (no approver instruction)", fullName, trigger.IssueNumber)
						return
					}
					pr, prErr := app.GH.GetPR(jobCtx, fullName, trigger.IssueNumber)
					if prErr != nil {
						app.Log.Warn("modify PR %s#%d: %v", fullName, trigger.IssueNumber, prErr)
						return
					}
					doModifyPR(jobCtx, app, trigger, pr, strings.TrimSpace(stripMention(trigger.CommentBody, r.agentLogin)))
					return
				}
				if isRelevantIssue(issue, r.agentLogin) || mentioned {
					if hasLabel(issue, bugLabel) {
						r.startBugFix(app, fullName, trigger.IssueNumber, trigger)
						return
					}
					r.startPlanOrBuild(jobCtx, app, trigger, issue)
				}
			})
			return
		}

		// "<approval word> <instructions>" on a pending plan starts the work and
		// forwards the rest of the comment to the coding stage.
		if command == "" && mentioned && isApprover && (st.Phase == "awaiting_approval" || st.Phase == "blocked") {
			if approved, notes := approvalWithNotes(unquoted, r.agentLogin); approved {
				app.GH.SetAgentState(ctx, fullName, trigger.IssueNumber, workingLabel)
				app.Store.UpdateIssue(fullName, trigger.IssueNumber, func(state *IssueState) {
					state.Phase = "implementing"
				})
				r.enqueue(fullName, key, stageWork, func(jobCtx context.Context) {
					issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
					if err != nil {
						blockIssue(jobCtx, app, trigger.IssueNumber, err)
						return
					}
					if issue.State != "open" {
						return
					}
					current := r.store.Issue(fullName, trigger.IssueNumber)
					if current.Phase != "awaiting_approval" && current.Phase != "blocked" {
						return
					}
					app.Store.UpdateIssue(fullName, trigger.IssueNumber, func(state *IssueState) {
						state.ExtraNotes = notes
					})
					current = r.store.Issue(fullName, trigger.IssueNumber)
					if notes != "" {
						app.Log.Info("owner approved %s#%d with instructions: %s", fullName, trigger.IssueNumber, notes)
					}
					doImplement(jobCtx, app, trigger, issue, current)
				})
				return
			}
		}

		// Feedback on a pending plan rewrites the plan instead of being dropped.
		if mentioned && isApprover && st.Phase == "awaiting_approval" {
			r.enqueue(fullName, key, stagePlan, func(jobCtx context.Context) {
				issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(jobCtx, app, trigger.IssueNumber, err)
					return
				}
				if issue.State != "open" {
					return
				}
				current := r.store.Issue(fullName, trigger.IssueNumber)
				if current.Phase != "awaiting_approval" {
					return
				}
				r.log.Info("plan feedback on %s#%d; regenerating the plan", fullName, trigger.IssueNumber)
				doPlan(jobCtx, app, trigger, issue)
			})
			return
		}

		// A mention with prose (not a command) on an issue with an open PR is
		// feedback: keep working on the same branch instead of merging.
		if mentioned && isApprover && st.Phase == "pr_open" {
			r.enqueue(fullName, key, stageWork, func(jobCtx context.Context) {
				issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(jobCtx, app, trigger.IssueNumber, err)
					return
				}
				if issue.State != "open" {
					return
				}
				doRevise(jobCtx, app, trigger, issue, st)
			})
			return
		}

		r.handleUnroutedComment(ctx, app, fullName, trigger, st, unquoted, mentioned, isApprover)
		return
	}

	if trigger.Type == "issue" {
		st := r.store.Issue(fullName, trigger.IssueNumber)
		if st.Phase != "" && st.Phase != "new" && st.Phase != "planning" {
			r.log.Debug("issue trigger for %s#%d ignored (phase=%s)", fullName, trigger.IssueNumber, st.Phase)
			return
		}
		key := fmt.Sprintf("%s#%d", fullName, trigger.IssueNumber)
		r.enqueue(fullName, key, stagePlan, func(jobCtx context.Context) {
			issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
			if err != nil {
				blockIssue(jobCtx, app, trigger.IssueNumber, err)
				return
			}
			if issue.State != "open" {
				return
			}
			if !isRelevantIssue(issue, r.agentLogin) && st.Phase != "planning" {
				return
			}
			comments, err := app.GH.ListComments(jobCtx, fullName, trigger.IssueNumber)
			if err == nil {
				ids := make([]int64, 0, len(comments))
				last := int64(0)
				for _, comment := range comments {
					ids = append(ids, comment.ID)
					last = comment.ID
				}
				app.Store.UpdateIssue(fullName, trigger.IssueNumber, func(state *IssueState) {
					state.Phase = "new"
					state.ProcessedComments = ids
					state.LastCommentID = last
				})
			}
			if hasLabel(issue, bugLabel) {
				r.startBugFix(app, fullName, trigger.IssueNumber, trigger)
				return
			}
			r.startPlanOrBuild(jobCtx, app, trigger, issue)
		})
	}
}

// replyToComment posts a short collaborator reply on the issue. The body
// comes from the scaffold catalogs, never from the comment text, so a
// stranger's words can never steer what the bot writes.
func (r *Router) replyToComment(ctx context.Context, app *AppContext, trigger Trigger, key string, args ...any) {
	body := app.text(trigger.IssueNumber, key, args...)
	if _, err := app.GH.PostComment(ctx, app.Repo.FullName, trigger.IssueNumber, body); err != nil {
		r.log.Warn("reply to comment %d on %s#%d: %v", trigger.CommentID, app.Repo.FullName, trigger.IssueNumber, err)
		return
	}
	r.log.Info("replied to comment %d on %s#%d (%s)", trigger.CommentID, app.Repo.FullName, trigger.IssueNumber, key)
}

// handleUnroutedComment answers an approver comment that matched no command
// and no task route, so the daemon never silently ignores the owner. It only
// ever replies or files notes: it never starts work, and a non-approver
// never reaches it (their @-mentions get the 👀 above and nothing else).
// A comment that is only a quote has nothing new to answer and stays silent.
func (r *Router) handleUnroutedComment(ctx context.Context, app *AppContext, fullName string, trigger Trigger, st IssueState, unquoted string, mentioned, isApprover bool) {
	if !isApprover || !mentioned {
		r.log.Info("comment on %s#%d ignored (phase=%s)", fullName, trigger.IssueNumber, firstNonEmpty(st.Phase, "none"))
		return
	}
	if strings.TrimSpace(unquoted) == "" {
		r.log.Info("comment on %s#%d ignored (quote only)", fullName, trigger.IssueNumber)
		return
	}
	words := strings.TrimSpace(stripMention(unquoted, r.agentLogin))
	if words == "" {
		r.replyToComment(ctx, app, trigger, "router.reply.ack")
		return
	}
	switch st.Phase {
	case "blocked":
		if looksLikeQuestion(words) {
			if strings.TrimSpace(st.BlockedReason) != "" {
				r.replyToComment(ctx, app, trigger, "router.reply.blocked_reason", st.BlockedReason, r.agentLogin)
			} else {
				r.replyToComment(ctx, app, trigger, "router.reply.ack")
			}
			return
		}
		// Extra instructions for the retry: the implement prompt renders
		// ExtraNotes, so they are carried into the next run.
		note := strings.TrimSpace(words)
		app.Store.UpdateIssue(fullName, trigger.IssueNumber, func(state *IssueState) {
			if strings.TrimSpace(state.ExtraNotes) == "" {
				state.ExtraNotes = note
			} else {
				state.ExtraNotes = strings.TrimSpace(state.ExtraNotes) + "\n" + note
			}
		})
		r.replyToComment(ctx, app, trigger, "router.reply.notes_saved", r.agentLogin)
	case "planning", "implementing":
		r.replyToComment(ctx, app, trigger, "router.reply.working")
	case "merging":
		r.replyToComment(ctx, app, trigger, "router.reply.merging")
	case "done":
		r.replyToComment(ctx, app, trigger, "router.reply.done")
	case "cancelled":
		r.replyToComment(ctx, app, trigger, "router.reply.cancelled")
	default:
		r.replyToComment(ctx, app, trigger, "router.reply.ack")
	}
}

func closablePhase(phase string) bool {
	switch phase {
	case "new", "planning", "implementing", "awaiting_approval", "blocked", "pr_open":
		return true
	}
	return false
}

// HandleClosed cancels any work for an issue that was closed by the user and,
// when there is an open PR, closes it as well.
func (r *Router) HandleClosed(ctx context.Context, fullName string, issue Issue) {
	st := r.store.Issue(fullName, issue.Number)
	if !closablePhase(st.Phase) {
		r.log.Debug("closed issue %s#%d ignored (phase=%s)", fullName, issue.Number, firstNonEmpty(st.Phase, "none"))
		return
	}
	app := r.contexts[fullName]
	if app == nil {
		return
	}
	r.log.Info("issue %s#%d closed; cancelling task", fullName, issue.Number)
	app.Store.UpdateIssue(fullName, issue.Number, func(state *IssueState) {
		state.Phase = "cancelled"
		state.CancelledAt = nowISO()
	})
	r.cancelActive(fullName, issue.Number)
	// Cleanup must not queue behind long coding jobs: with maxParallel=1 the
	// PR would stay open and the label stale until the other issue finishes.
	go func() {
		jobCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		for _, jobKey := range issueJobKeys(fullName, issue.Number) {
			r.waitInactive(jobCtx, jobKey, 60*time.Second)
		}
		current := r.store.Issue(fullName, issue.Number)
		if current.Phase == "done" || current.Phase == "merging" {
			return
		}
		if fresh, err := app.GH.GetIssue(jobCtx, fullName, issue.Number); err == nil && fresh.State == "open" {
			return
		}
		doCloseIssue(jobCtx, app, issue, current)
	}()
}

// HandleReopened resumes a cancelled task. An existing plan returns to
// awaiting_approval; without a plan it starts a fresh planning pass.
func (r *Router) HandleReopened(ctx context.Context, fullName string, issue Issue) {
	st := r.store.Issue(fullName, issue.Number)
	if st.Phase != "cancelled" && st.Phase != "done" {
		return
	}
	app := r.contexts[fullName]
	if app == nil {
		return
	}
	r.log.Info("issue %s#%d reopened; resuming (was %s)", fullName, issue.Number, firstNonEmpty(st.Phase, "none"))
	if st.Phase == "done" {
		// A completed issue that is reopened starts a new round: the old PR is
		// merged, so a fresh plan and worktree are needed.
		app.Store.UpdateIssue(fullName, issue.Number, func(state *IssueState) {
			state.Phase = "new"
			state.PRNumber = 0
			state.PRURL = ""
			state.Branch = ""
			state.Worktree = ""
			state.SessionID = ""
			state.MergedAt = ""
			state.IssueClosed = false
		})
	} else if st.Plan != "" {
		app.Store.UpdateIssue(fullName, issue.Number, func(state *IssueState) {
			state.Phase = "awaiting_approval"
			state.CancelledAt = ""
			state.IssueClosed = false
		})
		app.GH.SetAgentState(ctx, fullName, issue.Number, awaitingLabel)
		return
	} else {
		app.Store.UpdateIssue(fullName, issue.Number, func(state *IssueState) {
			state.Phase = "new"
			state.CancelledAt = ""
			state.IssueClosed = false
		})
	}
	r.HandleTrigger(ctx, fullName, Trigger{
		Type:        "issue",
		IssueNumber: issue.Number,
		Sender:      issue.User.Login,
		Key:         fmt.Sprintf("reopened-%d", time.Now().UnixNano()),
	})
}

func (r *Router) StartupScan(ctx context.Context) {
	for fullName, app := range r.contexts {
		r.log.Info("startup scan %s", fullName)
		issues, err := app.GH.ListOpenIssues(ctx, fullName, nil)
		if err != nil {
			r.log.Warn("startup scan %s: %v", fullName, err)
			continue
		}
		r.log.Debug("startup scan %s: %d open issues", fullName, len(issues))
		if closedIssues, closedErr := app.GH.ListIssues(ctx, fullName, "closed", nil); closedErr == nil {
			for _, issue := range closedIssues {
				if closablePhase(r.store.Issue(fullName, issue.Number).Phase) {
					r.HandleClosed(ctx, fullName, issue)
				}
			}
		}
		for _, issue := range issues {
			number := issue.Number
			st := r.store.Issue(fullName, number)
			if wasReopened(st) {
				r.HandleReopened(ctx, fullName, issue)
				continue
			}

			// A planning phase is resumed even when the trigger was a comment:
			// isRelevantIssue only looks at the body and assignees.
			resume := st.Phase == "planning" || ((st.Phase == "" || st.Phase == "new") && isRelevantIssue(issue, r.agentLogin))
			if resume {
				if st.Phase == "" {
					comments, listErr := app.GH.ListComments(ctx, fullName, number)
					if listErr == nil {
						ids := make([]int64, 0, len(comments))
						last := int64(0)
						for _, comment := range comments {
							ids = append(ids, comment.ID)
							last = comment.ID
						}
						app.Store.UpdateIssue(fullName, number, func(state *IssueState) {
							state.Phase = "new"
							state.ProcessedComments = ids
							state.LastCommentID = last
						})
					}
				}
				r.log.Info("startup: planning %s#%d", fullName, number)
				r.HandleTrigger(ctx, fullName, Trigger{
					Type:        "issue",
					IssueNumber: number,
					Sender:      issue.User.Login,
					Key:         fmt.Sprintf("startup-%d", issue.UpdatedAt.UnixNano()),
				})
				continue
			}
			if st.Phase == "" {
				continue
			}

			// A restart kills the coding-agent process; pick the work back up
			// from the existing worktree instead of parking the issue.
			if st.Phase == "implementing" || (st.Phase == "blocked" && st.BlockedReason == "interrupted by restart") {
				r.log.Info("startup: resuming interrupted implementation for %s#%d", fullName, number)
				resumeKey := fmt.Sprintf("%s#%d", fullName, number)
				r.enqueue(fullName, resumeKey, stageWork, func(jobCtx context.Context) {
					issue, err := app.GH.GetIssue(jobCtx, fullName, number)
					if err != nil {
						blockIssue(jobCtx, app, number, err)
						return
					}
					if issue.State != "open" {
						return
					}
					current := r.store.Issue(fullName, number)
					if current.Phase != "implementing" && current.BlockedReason != "interrupted by restart" {
						return
					}
					doImplement(jobCtx, app, Trigger{
						Type:        "restart",
						IssueNumber: number,
						Sender:      r.agentLogin,
						Key:         fmt.Sprintf("resume-%d", time.Now().UnixNano()),
					}, issue, current)
				})
				continue
			}

			comments, listErr := app.GH.ListComments(ctx, fullName, number)
			if listErr != nil {
				continue
			}
			for _, comment := range comments {
				if app.Store.IsCommentProcessed(fullName, number, comment.ID) {
					continue
				}
				r.HandleTrigger(ctx, fullName, Trigger{
					Type:        "comment",
					IssueNumber: number,
					CommentID:   comment.ID,
					CommentBody: comment.Body,
					Sender:      comment.User.Login,
					Key:         fmt.Sprintf("scan-%d", comment.ID),
				})
			}
			ids := make([]int64, 0, len(comments))
			last := int64(0)
			for _, comment := range comments {
				ids = append(ids, comment.ID)
				last = comment.ID
			}
			app.Store.UpdateIssue(fullName, number, func(state *IssueState) {
				state.ProcessedComments = ids
				state.LastCommentID = last
			})
		}
		app.Store.UpdateMeta(fullName, func(meta *RepoMeta) {
			meta.LastPollAt = nowISO()
		})
	}
}

func (r *Router) PollOnce(ctx context.Context, fullName string) {
	app := r.contexts[fullName]
	if app == nil {
		return
	}
	meta := app.Store.Meta(fullName)
	var since *time.Time
	if meta.LastPollAt != "" {
		if parsed, err := time.Parse(time.RFC3339, meta.LastPollAt); err == nil {
			safe := parsed.Add(-60 * time.Second)
			since = &safe
		}
	}
	issues, err := app.GH.ListIssues(ctx, fullName, "all", since)
	if err != nil {
		r.log.Warn("poll %s: %v", fullName, err)
		return
	}
	sinceText := "-"
	if since != nil {
		sinceText = since.Format(time.RFC3339)
	}
	r.log.Debug("poll %s: %d updated issues since %s", fullName, len(issues), sinceText)
	freshComments := 0
	for _, issue := range issues {
		number := issue.Number
		st := r.store.Issue(fullName, number)
		if issue.State == "open" && wasReopened(st) {
			r.HandleReopened(ctx, fullName, issue)
			continue
		}
		if issue.State == "closed" {
			if closablePhase(st.Phase) {
				r.HandleClosed(ctx, fullName, issue)
			}
			continue
		}
		// A bug label added to a waiting issue also skips the plan.
		if st.Phase == "awaiting_approval" && hasLabel(issue, bugLabel) {
			r.startBugFix(app, fullName, number, Trigger{
				Type:        "label",
				IssueNumber: number,
				Sender:      issue.User.Login,
				Key:         fmt.Sprintf("bug-%d", issue.UpdatedAt.UnixNano()),
			})
			continue
		}
		if st.Phase == "" && isRelevantIssue(issue, r.agentLogin) {
			r.HandleTrigger(ctx, fullName, Trigger{
				Type:        "issue",
				IssueNumber: number,
				Sender:      issue.User.Login,
				Key:         fmt.Sprintf("poll-%d", issue.UpdatedAt.UnixNano()),
			})
			continue
		}
		// Even untracked issues are scanned: a mention in a comment (rather
		// than the body) is what usually starts the flow.
		comments, listErr := app.GH.ListComments(ctx, fullName, number)
		if listErr != nil {
			continue
		}
		for _, comment := range comments {
			if app.Store.IsCommentProcessed(fullName, number, comment.ID) {
				continue
			}
			freshComments++
			r.HandleTrigger(ctx, fullName, Trigger{
				Type:        "comment",
				IssueNumber: number,
				CommentID:   comment.ID,
				CommentBody: comment.Body,
				Sender:      comment.User.Login,
				Key:         fmt.Sprintf("poll-%d", comment.ID),
			})
		}
		ids := make([]int64, 0, len(comments))
		last := int64(0)
		for _, comment := range comments {
			ids = append(ids, comment.ID)
			last = comment.ID
		}
		app.Store.MarkComments(fullName, number, ids, last)
	}
	r.checkReactions(ctx, app)
	r.checkPRFeedback(ctx, app)
	r.retryBlockedCI(ctx, app)
	r.log.Debug("poll %s: handled %d new comments", fullName, freshComments)
	app.Store.UpdateMeta(fullName, func(meta *RepoMeta) {
		meta.LastPollAt = nowISO()
	})
}

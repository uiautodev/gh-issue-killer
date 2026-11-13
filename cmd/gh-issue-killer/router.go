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

// resolveCommand returns the command a comment carries. Only approvers can
// command the agent, the comment must mention it, and the whole comment must
// be the command itself. Prose like "把按钮合并到 input-group 里" therefore
// never merges a PR by accident.
func resolveCommand(body string, isApprover bool, agentLogin string) string {
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
	}
	return ""
}

func stripMention(text, login string) string {
	if login == "" {
		return text
	}
	pattern := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(login))
	return pattern.ReplaceAllString(text, " ")
}

// isAgentComment reports whether a comment is daemon output rather than a
// command. Comments written before the marker existed all start with the
// bot heading.
func isAgentComment(body string) bool {
	return strings.Contains(body, agentCommentMarker) ||
		strings.HasPrefix(strings.TrimSpace(body), "### 🤖")
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
)

type routerJob struct {
	key string
	ctx context.Context
	fn  func(context.Context)
}

type Router struct {
	cfg        *Config
	contexts   map[string]*AppContext
	agentLogin string
	store      *Store
	log        *Logger
	planJobs   chan routerJob
	workJobs   chan routerJob
	mu         sync.Mutex
	active     map[string]bool
	queued     map[string]bool
	cancels    map[string]context.CancelFunc
}

func NewRouter(cfg *Config, contexts map[string]*AppContext, agentLogin string, store *Store, log *Logger) *Router {
	router := &Router{
		cfg:        cfg,
		contexts:   contexts,
		agentLogin: agentLogin,
		store:      store,
		log:        log,
		planJobs:   make(chan routerJob, 128),
		workJobs:   make(chan routerJob, 128),
		active:     map[string]bool{},
		queued:     map[string]bool{},
		cancels:    map[string]context.CancelFunc{},
	}
	planWorkers := cfg.PlanParallel
	if planWorkers < 1 {
		planWorkers = 1
	}
	workWorkers := cfg.ImplementParallel
	if workWorkers < 1 {
		workWorkers = 1
	}
	for i := 0; i < planWorkers; i++ {
		go router.worker(router.planJobs)
	}
	for i := 0; i < workWorkers; i++ {
		go router.worker(router.workJobs)
	}
	return router
}

func (r *Router) worker(jobs <-chan routerJob) {
	for job := range jobs {
		r.mu.Lock()
		delete(r.queued, job.key)
		r.active[job.key] = true
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
		r.mu.Unlock()
		r.log.Debug("job done %s", job.key)
	}
}

func (r *Router) enqueue(key string, stage jobStage, fn func(context.Context)) {
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
	queue := r.workJobs
	if stage == stagePlan {
		queue = r.planJobs
	}
	queue <- routerJob{key: key, ctx: jobCtx, fn: fn}
}

func (r *Router) cancelActive(fullName string, number int) {
	key := fmt.Sprintf("%s#%d", fullName, number)
	r.mu.Lock()
	cancel := r.cancels[key]
	r.mu.Unlock()
	if cancel != nil {
		r.log.Debug("cancel active job %s", key)
		cancel()
	}
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
		r.log.Info("plan approved by 👍 from @%s on %s#%d", login, fullName, number)
		r.enqueueApprove(ctx, app, fullName, number, login)
	case st.Phase == "pr_open" && st.PRCommentID == commentID:
		r.log.Info("merge approved by 👍 from @%s on %s#%d", login, fullName, number)
		r.enqueueMerge(app, fullName, number, login)
	default:
		r.log.Debug("ignore 👍 on %s#%d comment %d (phase=%s)", fullName, number, commentID, firstNonEmpty(st.Phase, "none"))
	}
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
	r.enqueue(key, stageWork, func(jobCtx context.Context) {
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

func (r *Router) enqueueApprove(ctx context.Context, app *AppContext, fullName string, number int, sender string) {
	key := fmt.Sprintf("%s#%d", fullName, number)
	// The owner already approved: clear the waiting label now, even if the
	// implementation is still queued behind another job. Otherwise the issue
	// keeps looking like it needs confirmation.
	app.GH.SetWaitingLabel(ctx, fullName, number, false)
	trigger := Trigger{
		Type:        "reaction",
		IssueNumber: number,
		Sender:      sender,
		Key:         fmt.Sprintf("approve-%d", time.Now().UnixNano()),
	}
	r.enqueue(key, stageWork, func(jobCtx context.Context) {
		issue, err := app.GH.GetIssue(jobCtx, fullName, number)
		if err != nil {
			blockIssue(jobCtx, app, number, err)
			return
		}
		if issue.State != "open" {
			return
		}
		current := r.store.Issue(fullName, number)
		if current.Phase != "awaiting_approval" {
			return
		}
		doImplement(jobCtx, app, trigger, issue, current)
	})
}

func (r *Router) enqueueMerge(app *AppContext, fullName string, number int, sender string) {
	key := fmt.Sprintf("%s#%d", fullName, number)
	trigger := Trigger{
		Type:        "reaction",
		IssueNumber: number,
		Sender:      sender,
		Key:         fmt.Sprintf("merge-%d", time.Now().UnixNano()),
	}
	r.enqueue(key, stageWork, func(jobCtx context.Context) {
		issue, err := app.GH.GetIssue(jobCtx, fullName, number)
		if err != nil {
			blockIssue(jobCtx, app, number, err)
			return
		}
		if issue.State != "open" {
			return
		}
		current := r.store.Issue(fullName, number)
		if current.Phase != "pr_open" || current.PRNumber == 0 {
			return
		}
		doMerge(jobCtx, app, trigger, issue, current)
	})
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
	issues, err := app.GH.ListIssues(ctx, app.Repo.FullName, "open", nil)
	if err != nil {
		return
	}
	for _, issue := range issues {
		st := r.store.Issue(app.Repo.FullName, issue.Number)
		if st.Phase != "pr_open" || st.PRNumber == 0 {
			continue
		}
		if comments, err := app.GH.ListReviewComments(ctx, app.Repo.FullName, st.PRNumber); err == nil {
			for _, comment := range comments {
				if r.store.IsFeedbackProcessed(app.Repo.FullName, issue.Number, comment.ID) {
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
				if r.store.IsFeedbackProcessed(app.Repo.FullName, issue.Number, review.ID) {
					continue
				}
				r.HandlePRReview(ctx, app.Repo.FullName, st.PRNumber, review)
			}
		}
	}
}

func (r *Router) checkReactions(ctx context.Context, app *AppContext) {
	issues, err := app.GH.ListIssues(ctx, app.Repo.FullName, "open", nil)
	if err != nil {
		r.log.Debug("scan awaiting issues: %v", err)
		return
	}
	for _, issue := range issues {
		st := r.store.Issue(app.Repo.FullName, issue.Number)
		switch st.Phase {
		case "awaiting_approval":
			if login, ok := r.commentThumbsUp(ctx, app, st.PlanCommentID); ok {
				r.log.Info("plan approved by 👍 from @%s on %s#%d (poll)", login, app.Repo.FullName, issue.Number)
				r.enqueueApprove(ctx, app, app.Repo.FullName, issue.Number, login)
			}
		case "pr_open":
			if login, ok := r.commentThumbsUp(ctx, app, st.PRCommentID); ok {
				r.log.Info("merge approved by 👍 from @%s on %s#%d (poll)", login, app.Repo.FullName, issue.Number)
				r.enqueueMerge(app, app.Repo.FullName, issue.Number, login)
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
		if r.store.IsCommentProcessed(fullName, trigger.IssueNumber, trigger.CommentID) {
			r.log.Debug("skip processed comment %d", trigger.CommentID)
			return
		}
		r.store.MarkCommentProcessed(fullName, trigger.IssueNumber, trigger.CommentID)

		st := r.store.Issue(fullName, trigger.IssueNumber)
		mentioned := isMention(trigger.CommentBody, r.agentLogin)
		isApprover := selfComment
		for _, login := range app.Repo.Approvers {
			if strings.EqualFold(login, trigger.Sender) {
				isApprover = true
				break
			}
		}
		command := resolveCommand(trigger.CommentBody, isApprover, r.agentLogin)
		if !selfComment && isApprover && st.Phase != "" {
			react(ctx, app, trigger, "eyes")
		}
		r.log.Info("comment trigger %s#%d from @%s mentioned=%v command=%s", fullName, trigger.IssueNumber, trigger.Sender, mentioned, firstNonEmpty(command, "-"))

		key := fmt.Sprintf("%s#%d", fullName, trigger.IssueNumber)

		if command == "cancel" && (st.Phase == "new" || st.Phase == "planning" || st.Phase == "awaiting_approval" || st.Phase == "blocked" || st.Phase == "implementing" || st.Phase == "pr_open") {
			r.enqueue(key, stageWork, func(jobCtx context.Context) {
				issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(jobCtx, app, trigger.IssueNumber, err)
					return
				}
				doCancel(jobCtx, app, trigger, issue, st)
			})
			return
		}

		if command == "merge" && (st.Phase == "pr_open" || (st.Phase == "blocked" && st.PRNumber > 0)) {
			r.enqueue(key, stageWork, func(jobCtx context.Context) {
				issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(jobCtx, app, trigger.IssueNumber, err)
					return
				}
				if issue.State != "open" {
					return
				}
				doMerge(jobCtx, app, trigger, issue, st)
			})
			return
		}

		if command == "approve" && (st.Phase == "awaiting_approval" || st.Phase == "blocked") {
			app.GH.SetWaitingLabel(ctx, fullName, trigger.IssueNumber, false)
			r.enqueue(key, stageWork, func(jobCtx context.Context) {
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

		if mentioned && (st.Phase == "" || st.Phase == "new") {
			r.enqueue(key, stagePlan, func(jobCtx context.Context) {
				issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(jobCtx, app, trigger.IssueNumber, err)
					return
				}
				if issue.State != "open" {
					return
				}
				if isRelevantIssue(issue, r.agentLogin) || mentioned {
					doPlan(jobCtx, app, trigger, issue)
				}
			})
			return
		}

		// Feedback on a pending plan rewrites the plan instead of being dropped.
		if mentioned && isApprover && st.Phase == "awaiting_approval" {
			r.enqueue(key, stagePlan, func(jobCtx context.Context) {
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
			r.enqueue(key, stageWork, func(jobCtx context.Context) {
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

		r.log.Info("comment on %s#%d ignored (phase=%s)", fullName, trigger.IssueNumber, firstNonEmpty(st.Phase, "none"))
		return
	}

	if trigger.Type == "issue" {
		st := r.store.Issue(fullName, trigger.IssueNumber)
		if st.Phase != "" && st.Phase != "new" && st.Phase != "planning" {
			r.log.Debug("issue trigger for %s#%d ignored (phase=%s)", fullName, trigger.IssueNumber, st.Phase)
			return
		}
		key := fmt.Sprintf("%s#%d", fullName, trigger.IssueNumber)
		r.enqueue(key, stagePlan, func(jobCtx context.Context) {
			issue, err := app.GH.GetIssue(jobCtx, fullName, trigger.IssueNumber)
			if err != nil {
				blockIssue(jobCtx, app, trigger.IssueNumber, err)
				return
			}
			if issue.State != "open" {
				return
			}
			if !isRelevantIssue(issue, r.agentLogin) {
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
			doPlan(jobCtx, app, trigger, issue)
		})
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
	key := fmt.Sprintf("%s#%d", fullName, issue.Number)
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
		r.waitInactive(jobCtx, key, 60*time.Second)
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
		app.GH.SetWaitingLabel(ctx, fullName, issue.Number, true)
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

			if (st.Phase == "" || st.Phase == "new" || st.Phase == "planning") && isRelevantIssue(issue, r.agentLogin) {
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
				r.enqueue(resumeKey, stageWork, func(jobCtx context.Context) {
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
		app.Store.UpdateIssue(fullName, number, func(state *IssueState) {
			state.ProcessedComments = ids
			state.LastCommentID = last
		})
	}
	r.checkReactions(ctx, app)
	r.checkPRFeedback(ctx, app)
	r.log.Debug("poll %s: handled %d new comments", fullName, freshComments)
	app.Store.UpdateMeta(fullName, func(meta *RepoMeta) {
		meta.LastPollAt = nowISO()
	})
}

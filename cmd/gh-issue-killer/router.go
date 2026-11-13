package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	approveRE = regexp.MustCompile(`(?i)(^|[^a-z])(lgtm|continue)([^a-z]|$)`)
	mergeRE   = regexp.MustCompile(`(?i)合并|(^|[^a-z])(merge|ship)([^a-z]|$)`)
	cancelRE  = regexp.MustCompile(`(?i)(^|[^a-z])(cancel|stop)([^a-z]|$)|停止`)
)

func isMention(text, login string) bool {
	if text == "" || login == "" {
		return false
	}
	pattern := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(login) + `([^A-Za-z0-9-]|$)`)
	return pattern.MatchString(text)
}

func parseCommand(text string) string {
	compact := strings.Join(strings.Fields(text), " ")
	if len([]rune(compact)) > 200 {
		return ""
	}
	if cancelRE.MatchString(compact) {
		return "cancel"
	}
	if mergeRE.MatchString(compact) {
		return "merge"
	}
	if approveRE.MatchString(compact) {
		return "approve"
	}
	return ""
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
	jobs       chan routerJob
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
		jobs:       make(chan routerJob, 128),
		active:     map[string]bool{},
		queued:     map[string]bool{},
		cancels:    map[string]context.CancelFunc{},
	}
	workers := cfg.MaxParallel
	if workers < 1 {
		workers = 1
	}
	for i := 0; i < workers; i++ {
		go router.worker()
	}
	return router
}

func (r *Router) worker() {
	for job := range r.jobs {
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

func (r *Router) enqueue(key string, fn func(context.Context)) {
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
	r.jobs <- routerJob{key: key, ctx: jobCtx, fn: fn}
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
	if strings.EqualFold(trigger.Sender, r.agentLogin) {
		r.log.Debug("ignore trigger from agent itself")
		return
	}

	if trigger.Type == "comment" {
		if r.store.IsCommentProcessed(fullName, trigger.IssueNumber, trigger.CommentID) {
			r.log.Debug("skip processed comment %d", trigger.CommentID)
			return
		}
		r.store.MarkCommentProcessed(fullName, trigger.IssueNumber, trigger.CommentID)

		st := r.store.Issue(fullName, trigger.IssueNumber)
		mentioned := isMention(trigger.CommentBody, r.agentLogin)
		isApprover := false
		for _, login := range app.Repo.Approvers {
			if strings.EqualFold(login, trigger.Sender) {
				isApprover = true
				break
			}
		}
		command := ""
		if isApprover {
			command = parseCommand(trigger.CommentBody)
		}
		r.log.Info("comment trigger %s#%d from @%s mentioned=%v command=%s", fullName, trigger.IssueNumber, trigger.Sender, mentioned, firstNonEmpty(command, "-"))

		key := fmt.Sprintf("%s#%d", fullName, trigger.IssueNumber)

		if command == "cancel" && (st.Phase == "new" || st.Phase == "planning" || st.Phase == "awaiting_approval" || st.Phase == "blocked" || st.Phase == "implementing" || st.Phase == "pr_open") {
			r.enqueue(key, func(jobCtx context.Context) {
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
			r.enqueue(key, func(jobCtx context.Context) {
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
			r.enqueue(key, func(jobCtx context.Context) {
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
			r.enqueue(key, func(jobCtx context.Context) {
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
		r.enqueue(key, func(jobCtx context.Context) {
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
	r.enqueue(key+"#closed", func(jobCtx context.Context) {
		r.waitInactive(jobCtx, key, 60*time.Second)
		current := r.store.Issue(fullName, issue.Number)
		if current.Phase == "done" || current.Phase == "merging" {
			return
		}
		if fresh, err := app.GH.GetIssue(jobCtx, fullName, issue.Number); err == nil && fresh.State == "open" {
			return
		}
		doCloseIssue(jobCtx, app, issue, current)
	})
}

// HandleReopened resumes a cancelled task. An existing plan returns to
// awaiting_approval; without a plan it starts a fresh planning pass.
func (r *Router) HandleReopened(ctx context.Context, fullName string, issue Issue) {
	st := r.store.Issue(fullName, issue.Number)
	if st.Phase != "cancelled" {
		return
	}
	app := r.contexts[fullName]
	if app == nil {
		return
	}
	r.log.Info("issue %s#%d reopened; resuming", fullName, issue.Number)
	if st.Plan != "" {
		app.Store.UpdateIssue(fullName, issue.Number, func(state *IssueState) {
			state.Phase = "awaiting_approval"
			state.CancelledAt = ""
		})
		app.GH.SetPhaseLabel(ctx, fullName, issue.Number, "agent:awaiting-approval")
		return
	}
	app.Store.UpdateIssue(fullName, issue.Number, func(state *IssueState) {
		state.Phase = "new"
		state.CancelledAt = ""
	})
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

			if st.Phase == "implementing" {
				app.Store.UpdateIssue(fullName, number, func(state *IssueState) {
					state.Phase = "blocked"
					state.BlockedReason = "interrupted by restart"
				})
				app.GH.SetPhaseLabel(ctx, fullName, number, "agent:blocked")
				_, _ = app.GH.PostComment(ctx, fullName, number, "### 🤖 实现被重启中断\n\n回复 **continue** 从当前 worktree 继续修复。")
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
		if st.Phase == "" {
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
	r.log.Debug("poll %s: handled %d new comments", fullName, freshComments)
	app.Store.UpdateMeta(fullName, func(meta *RepoMeta) {
		meta.LastPollAt = nowISO()
	})
}

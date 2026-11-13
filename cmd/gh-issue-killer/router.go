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
	fn  func()
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
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					r.log.Error("job %s panicked: %v", job.key, recovered)
				}
			}()
			job.fn()
		}()
		r.mu.Lock()
		delete(r.active, job.key)
		r.mu.Unlock()
	}
}

func (r *Router) enqueue(key string, fn func()) {
	r.mu.Lock()
	if r.active[key] || r.queued[key] {
		r.mu.Unlock()
		r.log.Debug("skip duplicate job %s", key)
		return
	}
	r.queued[key] = true
	r.mu.Unlock()
	r.jobs <- routerJob{key: key, fn: fn}
}

func (r *Router) HandleTrigger(ctx context.Context, fullName string, trigger Trigger) {
	app := r.contexts[fullName]
	if app == nil {
		r.log.Warn("no context for repo %s", fullName)
		return
	}

	if trigger.DeliveryID != "" {
		if r.store.IsDeliveryProcessed(fullName, trigger.DeliveryID) {
			return
		}
		r.store.MarkDeliveryProcessed(fullName, trigger.DeliveryID)
	}
	if strings.EqualFold(trigger.Sender, r.agentLogin) {
		return
	}

	if trigger.Type == "comment" {
		if r.store.IsCommentProcessed(fullName, trigger.IssueNumber, trigger.CommentID) {
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
			r.enqueue(key, func() {
				issue, err := app.GH.GetIssue(ctx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(ctx, app, trigger.IssueNumber, err)
					return
				}
				doCancel(ctx, app, trigger, issue, st)
			})
			return
		}

		if command == "merge" && (st.Phase == "pr_open" || (st.Phase == "blocked" && st.PRNumber > 0)) {
			r.enqueue(key, func() {
				issue, err := app.GH.GetIssue(ctx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(ctx, app, trigger.IssueNumber, err)
					return
				}
				doMerge(ctx, app, trigger, issue, st)
			})
			return
		}

		if command == "approve" && (st.Phase == "awaiting_approval" || st.Phase == "blocked" || st.Phase == "cancelled") {
			r.enqueue(key, func() {
				issue, err := app.GH.GetIssue(ctx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(ctx, app, trigger.IssueNumber, err)
					return
				}
				doImplement(ctx, app, trigger, issue, st)
			})
			return
		}

		if mentioned && (st.Phase == "" || st.Phase == "new") {
			r.enqueue(key, func() {
				issue, err := app.GH.GetIssue(ctx, fullName, trigger.IssueNumber)
				if err != nil {
					blockIssue(ctx, app, trigger.IssueNumber, err)
					return
				}
				if isRelevantIssue(issue, r.agentLogin) || mentioned {
					doPlan(ctx, app, trigger, issue)
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
		r.enqueue(key, func() {
			issue, err := app.GH.GetIssue(ctx, fullName, trigger.IssueNumber)
			if err != nil {
				blockIssue(ctx, app, trigger.IssueNumber, err)
				return
			}
			if issue.State != "open" {
				return
			}
			if !isRelevantIssue(issue, r.agentLogin) {
				return
			}
			comments, err := app.GH.ListComments(ctx, fullName, trigger.IssueNumber)
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
			doPlan(ctx, app, trigger, issue)
		})
	}
}

func (r *Router) StartupScan(ctx context.Context) {
	for fullName, app := range r.contexts {
		r.log.Info("startup scan %s", fullName)
		issues, err := app.GH.ListOpenIssues(ctx, fullName, nil)
		if err != nil {
			r.log.Warn("startup scan %s: %v", fullName, err)
			continue
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
	issues, err := app.GH.ListOpenIssues(ctx, fullName, since)
	if err != nil {
		r.log.Warn("poll %s: %v", fullName, err)
		return
	}
	for _, issue := range issues {
		number := issue.Number
		st := r.store.Issue(fullName, number)
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
	app.Store.UpdateMeta(fullName, func(meta *RepoMeta) {
		meta.LastPollAt = nowISO()
	})
}

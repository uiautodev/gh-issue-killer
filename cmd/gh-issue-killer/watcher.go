package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

func missingHookEvents(have []string) []string {
	seen := map[string]bool{}
	for _, event := range have {
		seen[event] = true
	}
	missing := make([]string, 0, len(hookEvents))
	for _, event := range hookEvents {
		if !seen[event] {
			missing = append(missing, event)
		}
	}
	return missing
}

func createSmeeChannel(ctx context.Context, proxy *Proxy) (string, error) {
	client := newHTTPClient(proxy, 30*time.Second)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://smee.io/new", nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "gh-issue-killer-go/0.1")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	location := response.Header.Get("Location")
	if location == "" {
		return "", fmt.Errorf("smee.io/new returned %d without Location", response.StatusCode)
	}
	return location, nil
}

func ensureSmeeHook(ctx context.Context, app *AppContext) (channel, secret string, err error) {
	meta := app.Store.Meta(app.Repo.FullName)
	secret = meta.WebhookSecret
	if secret == "" {
		secret = app.Store.GlobalSecret()
	}
	if secret == "" {
		secret = randomHex(24)
	}
	app.Store.UpdateMeta(app.Repo.FullName, func(meta *RepoMeta) {
		if meta.WebhookSecret == "" {
			meta.WebhookSecret = secret
		}
	})

	channel = meta.SmeeChannel
	createdChannel := false
	if channel == "" {
		channel, err = createSmeeChannel(ctx, app.Proxy)
		if err != nil {
			return "", "", fmt.Errorf("create smee channel: %w", err)
		}
		createdChannel = true
		app.Store.UpdateMeta(app.Repo.FullName, func(meta *RepoMeta) {
			meta.SmeeChannel = channel
			meta.HookID = 0
		})
		meta.SmeeChannel = channel
		meta.HookID = 0
	}

	// Personal repositories cannot grant Admin to a collaborator, so gh-agent
	// can never create a webhook there. If a channel already exists but we do
	// not own a hook, assume the repo owner created the webhook manually and
	// just listen on the channel. A newly created channel still tries to make
	// a hook (works for organization-owned repos).
	if !createdChannel && meta.HookID == 0 {
		app.Log.Info("%s: using externally managed webhook for %s", app.Repo.FullName, channel)
		return channel, secret, nil
	}

	if meta.HookID != 0 {
		hook, getErr := app.GH.GetHook(ctx, app.Repo.FullName, meta.HookID)
		if getErr == nil && hook.ID != 0 && strings.TrimRight(hook.Config.URL, "/") == strings.TrimRight(channel, "/") {
			if missing := missingHookEvents(hook.Events); len(missing) > 0 {
				if updateErr := app.GH.UpdateHookEvents(ctx, app.Repo.FullName, hook.ID, hookEvents); updateErr != nil {
					app.Log.Warn("%s: update hook events: %v", app.Repo.FullName, updateErr)
				} else {
					app.Log.Info("%s: webhook now sends %s", app.Repo.FullName, strings.Join(missing, ","))
				}
			}
			return channel, secret, nil
		}
		_ = app.GH.DeleteHook(ctx, app.Repo.FullName, meta.HookID)
		app.Store.UpdateMeta(app.Repo.FullName, func(meta *RepoMeta) { meta.HookID = 0 })
	}

	hook, err := app.GH.CreateHook(ctx, app.Repo.FullName, channel, secret)
	if err != nil {
		// Still return the channel/secret: the owner may create the hook via
		// the UI and the SSE listener should work as soon as it exists.
		return channel, secret, fmt.Errorf("create webhook: %w", err)
	}
	app.Store.UpdateMeta(app.Repo.FullName, func(meta *RepoMeta) {
		meta.HookID = hook.ID
		meta.SmeeChannel = channel
		meta.WebhookSecret = secret
	})
	return channel, secret, nil
}

type smeeEnvelope struct {
	Body     json.RawMessage `json:"body"`
	Event    string          `json:"x-github-event"`
	Delivery string          `json:"x-github-delivery"`
}

type smeePayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number int `json:"number"`
	} `json:"issue"`
	Comment struct {
		ID int64 `json:"id"`
	} `json:"comment"`
	PullRequest struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	Review struct {
		ID    int64  `json:"id"`
		Body  string `json:"body"`
		State string `json:"state"`
		User  struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"review"`
	Reaction struct {
		Content string `json:"content"`
		User    struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"reaction"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

func dispatchSmee(ctx context.Context, app *AppContext, router *Router, envelope smeeEnvelope, log *Logger) {
	if len(envelope.Body) == 0 {
		return
	}
	var payload smeePayload
	if err := json.Unmarshal(envelope.Body, &payload); err != nil {
		log.Debug("smee: cannot parse body: %v", err)
		return
	}
	if payload.Issue.Number == 0 {
		return
	}
	log.Debug("smee event: type=%s action=%s delivery=%s issue=%d comment=%d", envelope.Event, payload.Action, envelope.Delivery, payload.Issue.Number, payload.Comment.ID)

	if envelope.Event == "pull_request_review_comment" {
		if payload.Action != "created" || payload.Comment.ID == 0 || payload.PullRequest.Number == 0 {
			return
		}
		comment, err := app.GH.GetReviewComment(ctx, app.Repo.FullName, payload.Comment.ID)
		if err != nil {
			log.Warn("smee: fetch review comment %d: %v", payload.Comment.ID, err)
			return
		}
		router.HandlePRReviewComment(ctx, app.Repo.FullName, payload.PullRequest.Number, comment)
		return
	}

	if envelope.Event == "pull_request_review" {
		if payload.Action != "submitted" || payload.Review.ID == 0 || payload.PullRequest.Number == 0 {
			return
		}
		review, err := app.GH.GetReview(ctx, app.Repo.FullName, payload.PullRequest.Number, payload.Review.ID)
		if err != nil {
			log.Warn("smee: fetch review %d: %v", payload.Review.ID, err)
			return
		}
		router.HandlePRReview(ctx, app.Repo.FullName, payload.PullRequest.Number, review)
		return
	}

	if envelope.Event == "reaction" {
		if payload.Action != "created" || payload.Comment.ID == 0 {
			return
		}
		reactor := payload.Reaction.User.Login
		if reactor == "" {
			reactor = payload.Sender.Login
		}
		router.HandleReaction(ctx, app.Repo.FullName, payload.Issue.Number,
			payload.Comment.ID, reactor, payload.Reaction.Content)
		return
	}

	// Re-fetch from the GitHub API so a compromised relay cannot forge a
	// command: the authoritative body/author come from GitHub, not smee.
	if payload.Comment.ID > 0 || envelope.Event == "issue_comment" {
		comment, err := app.GH.GetComment(ctx, app.Repo.FullName, payload.Comment.ID)
		if err != nil {
			log.Warn("smee: fetch comment %d: %v", payload.Comment.ID, err)
			return
		}
		if payload.Action != "" && payload.Action != "created" {
			return
		}
		if issue, issueErr := app.GH.GetIssue(ctx, app.Repo.FullName, payload.Issue.Number); issueErr == nil && issue.State != "open" {
			log.Debug("smee: ignore comment on closed issue %s#%d", app.Repo.FullName, payload.Issue.Number)
			return
		}
		router.HandleTrigger(ctx, app.Repo.FullName, Trigger{
			Type:        "comment",
			IssueNumber: payload.Issue.Number,
			CommentID:   comment.ID,
			CommentBody: comment.Body,
			Sender:      comment.User.Login,
			Key:         "smee-" + firstNonEmpty(envelope.Delivery, fmt.Sprint(comment.ID)),
			DeliveryID:  envelope.Delivery,
		})
		return
	}

	issue, err := app.GH.GetIssue(ctx, app.Repo.FullName, payload.Issue.Number)
	if err != nil {
		log.Warn("smee: fetch issue %d: %v", payload.Issue.Number, err)
		return
	}
	if issue.State == "closed" {
		router.HandleClosed(ctx, app.Repo.FullName, issue)
		return
	}
	if payload.Action == "reopened" {
		router.HandleReopened(ctx, app.Repo.FullName, issue)
		return
	}
	sender := payload.Sender.Login
	if sender == "" {
		sender = issue.User.Login
	}
	router.HandleTrigger(ctx, app.Repo.FullName, Trigger{
		Type:        "issue",
		IssueNumber: issue.Number,
		Sender:      sender,
		Key:         "smee-" + firstNonEmpty(envelope.Delivery, fmt.Sprint(issue.UpdatedAt.UnixNano())),
		DeliveryID:  envelope.Delivery,
	})
}

func startSmee(ctx context.Context, app *AppContext, router *Router, channel string, log *Logger) func() {
	client := newHTTPClient(app.Proxy, 0)
	stop := make(chan struct{})
	var once sync.Once

	go func() {
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			default:
			}
			if err := consumeSmee(ctx, app, router, client, channel, log, stop); err != nil {
				log.Warn("smee stream for %s ended: %v; reconnecting in 5s", app.Repo.FullName, err)
				select {
				case <-stop:
					return
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
		}
	}()

	return func() {
		once.Do(func() { close(stop) })
	}
}

func consumeSmee(ctx context.Context, app *AppContext, router *Router, client *http.Client, channel string, log *Logger, stop chan struct{}) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, channel, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Cache-Control", "no-cache")
	request.Header.Set("User-Agent", "gh-issue-killer-go/0.1")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("smee %d: %s", response.StatusCode, tailString(string(data), 5))
	}
	log.Info("smee stream connected for %s", app.Repo.FullName)
	log.Debug("smee stream channel=%s repo=%s", channel, app.Repo.FullName)

	reader := bufio.NewReaderSize(response.Body, 1024*1024)
	var dataLines []string
	for {
		select {
		case <-stop:
			return nil
		case <-ctx.Done():
			return nil
		default:
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return fmt.Errorf("stream closed")
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 {
				continue
			}
			data := strings.Join(dataLines, "\n")
			dataLines = dataLines[:0]
			var envelope smeeEnvelope
			if json.Unmarshal([]byte(data), &envelope) != nil {
				continue
			}
			dispatchSmee(ctx, app, router, envelope, log)
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
}

func startPoller(ctx context.Context, cfg *Config, router *Router, contexts map[string]*AppContext, log *Logger) func() {
	ticker := time.NewTicker(cfg.PollInterval)
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				ticker.Stop()
				return
			case <-ctx.Done():
				ticker.Stop()
				return
			case <-ticker.C:
				for fullName := range contexts {
					router.PollOnce(ctx, fullName)
				}
			}
		}
	}()
	log.Info("poller started, interval %s", cfg.PollInterval)
	return func() { close(stop) }
}

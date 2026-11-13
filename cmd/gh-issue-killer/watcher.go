package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

func verifySignature(secret string, body []byte, header string) bool {
	if secret == "" {
		return true
	}
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(header))
}

func startWebhookServer(cfg *Config, router *Router, contexts map[string]*AppContext, secret string, log *Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/gh", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		if !verifySignature(secret, body, r.Header.Get("X-Hub-Signature-256")) {
			log.Warn("webhook signature mismatch, ignoring")
			return
		}
		event := r.Header.Get("X-GitHub-Event")
		delivery := r.Header.Get("X-GitHub-Delivery")
		var payload struct {
			Repository struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
			Action  string  `json:"action"`
			Issue   Issue   `json:"issue"`
			Comment Comment `json:"comment"`
			Sender  User    `json:"sender"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return
		}
		app := contexts[payload.Repository.FullName]
		if app == nil {
			return
		}
		if event == "issue_comment" && payload.Action == "created" && payload.Comment.ID > 0 {
			router.HandleTrigger(context.Background(), payload.Repository.FullName, Trigger{
				Type:        "comment",
				IssueNumber: payload.Issue.Number,
				CommentID:   payload.Comment.ID,
				CommentBody: payload.Comment.Body,
				Sender:      payload.Sender.Login,
				Key:         "hook-" + delivery,
				DeliveryID:  delivery,
			})
			return
		}
		if event == "issues" && payload.Issue.Number > 0 {
			router.HandleTrigger(context.Background(), payload.Repository.FullName, Trigger{
				Type:        "issue",
				IssueNumber: payload.Issue.Number,
				Sender:      payload.Sender.Login,
				Key:         "hook-" + delivery,
				DeliveryID:  delivery,
			})
		}
	})

	address := fmt.Sprintf("%s:%d", cfg.WebhookHost, cfg.WebhookPort)
	server := &http.Server{Addr: address, Handler: mux}
	go func() {
		log.Info("webhook receiver on http://%s/gh", address)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Warn("webhook server: %v", err)
		}
	}()
	return server
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
	if channel == "" {
		channel, err = createSmeeChannel(ctx, app.Proxy)
		if err != nil {
			return "", "", fmt.Errorf("create smee channel: %w", err)
		}
		app.Store.UpdateMeta(app.Repo.FullName, func(meta *RepoMeta) {
			meta.SmeeChannel = channel
			meta.HookID = 0
		})
		meta.SmeeChannel = channel
		meta.HookID = 0
	}

	if meta.HookID != 0 {
		hook, getErr := app.GH.GetHook(ctx, app.Repo.FullName, meta.HookID)
		if getErr == nil && hook.ID != 0 && strings.TrimRight(hook.Config.URL, "/") == strings.TrimRight(channel, "/") {
			return channel, secret, nil
		}
		_ = app.GH.DeleteHook(ctx, app.Repo.FullName, meta.HookID)
		app.Store.UpdateMeta(app.Repo.FullName, func(meta *RepoMeta) { meta.HookID = 0 })
	}

	hook, err := app.GH.CreateHook(ctx, app.Repo.FullName, channel, secret)
	if err != nil {
		return "", "", fmt.Errorf("create webhook: %w", err)
	}
	app.Store.UpdateMeta(app.Repo.FullName, func(meta *RepoMeta) {
		meta.HookID = hook.ID
		meta.SmeeChannel = channel
		meta.WebhookSecret = secret
	})
	return channel, secret, nil
}

type smeeEnvelope struct {
	Body      json.RawMessage `json:"body"`
	Event     string          `json:"x-github-event"`
	Delivery  string          `json:"x-github-delivery"`
	Signature string          `json:"x-hub-signature-256"`
}

type smeePayload struct {
	Action string `json:"action"`
	Issue  struct {
		Number int `json:"number"`
	} `json:"issue"`
	Comment struct {
		ID int64 `json:"id"`
	} `json:"comment"`
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

func startSmee(ctx context.Context, app *AppContext, router *Router, channel, secret string, log *Logger) func() {
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
			if err := consumeSmee(ctx, app, router, client, channel, secret, log, stop); err != nil {
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

func consumeSmee(ctx context.Context, app *AppContext, router *Router, client *http.Client, channel, secret string, log *Logger, stop chan struct{}) error {
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

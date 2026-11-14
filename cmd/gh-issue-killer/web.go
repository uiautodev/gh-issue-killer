package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed web/index.html web/logo.svg
var webIndex embed.FS

type webServer struct {
	cfg      *Config
	router   *Router
	store    *Store
	log      *Logger
	identity string
	version  string
	started  time.Time
}

type webPool struct {
	Plan int `json:"plan"`
	Work int `json:"work"`
}

type webRepo struct {
	Name        string         `json:"name"`
	Agent       string         `json:"agent"`
	Base        string         `json:"base"`
	HookID      int64          `json:"hookId"`
	Smee        string         `json:"smee"`
	LastPoll    string         `json:"lastPoll"`
	Queue       webPool        `json:"queue"`
	Counts      map[string]int `json:"counts"`
	Issues      int            `json:"issues"`
	Approvers   []string       `json:"approvers"`
	WorktreeDir string         `json:"worktreeDir"`
}

type webTask struct {
	Repo        string `json:"repo"`
	Number      int    `json:"number"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Phase       string `json:"phase"`
	PhaseLabel  string `json:"phaseLabel"`
	Active      bool   `json:"active"`
	Queued      bool   `json:"queued"`
	StartedAt   string `json:"startedAt"`
	UpdatedAt   string `json:"updatedAt"`
	ElapsedSec  int64  `json:"elapsedSec"`
	PRNumber    int    `json:"prNumber"`
	PRURL       string `json:"prUrl"`
	Blocked     string `json:"blocked"`
	ChecksTotal int    `json:"checksTotal"`
	ChecksOK    int    `json:"checksOk"`
	Shots       int    `json:"shots"`
}

type webSnapshot struct {
	Time       string    `json:"time"`
	Version    string    `json:"version"`
	PID        int       `json:"pid"`
	UptimeSec  int64     `json:"uptimeSec"`
	ConfigFile string    `json:"configFile"`
	LogPath    string    `json:"logPath"`
	StatePath  string    `json:"statePath"`
	Identity   string    `json:"identity"`
	Webhook    bool      `json:"webhook"`
	PlanLimit  int       `json:"planLimit"`
	WorkLimit  int       `json:"workLimit"`
	Active     int       `json:"active"`
	Queued     int       `json:"queued"`
	Repos      []webRepo `json:"repos"`
	Tasks      []webTask `json:"tasks"`
}

func startWeb(cfg *Config, router *Router, store *Store, log *Logger, identity, version string) (*http.Server, error) {
	if cfg.WebPort <= 0 {
		return nil, nil
	}
	web := &webServer{
		cfg: cfg, router: router, store: store, log: log,
		identity: identity, version: version, started: time.Now(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", web.handleIndex)
	mux.HandleFunc("/api/state", web.handleState)
	mux.HandleFunc("/api/issue", web.handleIssue)
	mux.HandleFunc("/api/logs", web.handleLogs)
	mux.HandleFunc("/api/action", web.handleAction)
	mux.HandleFunc("/logo.svg", web.handleLogo)

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", cfg.WebPort))
	if err != nil {
		return nil, err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Warn("web UI: %v", err)
		}
	}()
	return server, nil
}

func (w *webServer) handleIndex(rw http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		http.NotFound(rw, req)
		return
	}
	data, err := webIndex.ReadFile("web/index.html")
	if err != nil {
		http.Error(rw, "missing web/index.html", http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	_, _ = rw.Write(data)
}

func (w *webServer) handleLogo(rw http.ResponseWriter, req *http.Request) {
	data, err := webIndex.ReadFile("web/logo.svg")
	if err != nil {
		http.NotFound(rw, req)
		return
	}
	rw.Header().Set("Content-Type", "image/svg+xml")
	rw.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = rw.Write(data)
}

// handleAction approves or cancels a task. The dashboard binds to loopback
// only, but browsers can still be tricked into cross-origin POSTs, so require
// a JSON content type and a loopback Origin.
func (w *webServer) handleAction(rw http.ResponseWriter, req *http.Request) {
	writeJSON := func(status int, ok bool, message string) {
		rw.Header().Set("Content-Type", "application/json; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-store")
		rw.WriteHeader(status)
		payload := map[string]any{"ok": ok}
		if message != "" {
			payload["error"] = message
		}
		_ = json.NewEncoder(rw).Encode(payload)
	}
	if req.Method != http.MethodPost {
		writeJSON(http.StatusMethodNotAllowed, false, "POST required")
		return
	}
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		writeJSON(http.StatusUnsupportedMediaType, false, "application/json required")
		return
	}
	if !w.loopbackOrigin(req) {
		writeJSON(http.StatusForbidden, false, "forbidden origin")
		return
	}
	var body struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(rw, req.Body, 4096)).Decode(&body); err != nil {
		writeJSON(http.StatusBadRequest, false, "bad request")
		return
	}
	var err error
	switch body.Action {
	case "approve":
		err = w.router.WebApprove(body.Repo, body.Number)
	case "cancel":
		err = w.router.WebCancel(body.Repo, body.Number)
	default:
		writeJSON(http.StatusBadRequest, false, "unknown action")
		return
	}
	if err != nil {
		writeJSON(http.StatusConflict, false, err.Error())
		return
	}
	w.log.Info("web action %s on %s#%d", body.Action, body.Repo, body.Number)
	writeJSON(http.StatusOK, true, "")
}

func (w *webServer) loopbackOrigin(req *http.Request) bool {
	origin := req.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" {
		return false
	}
	return parsed.Port() == strconv.Itoa(w.cfg.WebPort)
}

func (w *webServer) handleState(rw http.ResponseWriter, req *http.Request) {
	snapshot := w.snapshot()
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(rw).Encode(snapshot)
}

func (w *webServer) handleIssue(rw http.ResponseWriter, req *http.Request) {
	repo := strings.TrimSpace(req.URL.Query().Get("repo"))
	number, err := strconv.Atoi(req.URL.Query().Get("number"))
	if repo == "" || err != nil || number <= 0 {
		http.Error(rw, "repo and number are required", http.StatusBadRequest)
		return
	}
	state := w.store.Issue(repo, number)
	if state.Phase == "" {
		http.Error(rw, "unknown issue", http.StatusNotFound)
		return
	}
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(rw).Encode(state)
}

func (w *webServer) handleLogs(rw http.ResponseWriter, req *http.Request) {
	lines := 200
	if raw := req.URL.Query().Get("lines"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 2000 {
			lines = parsed
		}
	}
	tail, err := tailLines(daemonLogPath(), lines)
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	_, _ = rw.Write([]byte(strings.Join(tail, "\n")))
}

func (w *webServer) snapshot() webSnapshot {
	now := time.Now()
	routerState := w.router.Snapshot()
	issued := w.store.Snapshot()

	snapshot := webSnapshot{
		Time:       now.UTC().Format(time.RFC3339),
		Version:    w.version,
		PID:        os.Getpid(),
		UptimeSec:  int64(now.Sub(w.started).Seconds()),
		ConfigFile: w.cfg.File,
		LogPath:    daemonLogPath(),
		StatePath:  stateFile(),
		Identity:   w.identity,
		Webhook:    w.cfg.Webhook,
		PlanLimit:  w.cfg.PlanParallel,
		WorkLimit:  w.cfg.ImplementParallel,
	}

	repoEntries := make(map[string]*webRepo, len(w.cfg.Repos))
	repoOrder := make([]string, 0, len(w.cfg.Repos))
	for _, repo := range w.cfg.Repos {
		meta := w.store.Meta(repo.FullName)
		base := ""
		trees := repo.WorktreeRoot
		if app := w.router.contexts[repo.FullName]; app != nil && app.Local != nil {
			base = app.Local.Base
			if trees == "" {
				trees = app.Local.WorktreeRoot
			}
		}
		depth := routerState.Pools[repo.FullName]
		repoEntries[repo.FullName] = &webRepo{
			Name:        repo.FullName,
			Agent:       repo.CodingAgent,
			Base:        base,
			HookID:      meta.HookID,
			Smee:        meta.SmeeChannel,
			LastPoll:    meta.LastPollAt,
			Queue:       webPool{Plan: depth.Plan, Work: depth.Work},
			Counts:      map[string]int{},
			Approvers:   repo.Approvers,
			WorktreeDir: trees,
		}
		repoOrder = append(repoOrder, repo.FullName)
	}

	for _, item := range issued {
		state := item.State
		// Issues without a phase are only comment watermarks (the poller now
		// records them for untracked issues too); they are not tasks.
		if state.Phase == "" {
			continue
		}
		key := fmt.Sprintf("%s#%d", item.Repo, item.Number)
		active, queued := routerState.Active[key], routerState.Queued[key]
		// Old watermarks were stored with phase "new" by UpdateIssue; hide
		// those unless there is real work behind them.
		if state.Phase == "new" && !active && !queued && state.Plan == "" && state.PRNumber == 0 {
			continue
		}
		phase := firstNonEmpty(state.Phase, "new")
		task := webTask{
			Repo:        item.Repo,
			Number:      item.Number,
			Phase:       state.Phase,
			PhaseLabel:  phaseLabel(phase),
			Active:      active,
			Queued:      queued,
			StartedAt:   state.StartedAt,
			UpdatedAt:   state.UpdatedAt,
			PRNumber:    state.PRNumber,
			PRURL:       state.PRURL,
			Blocked:     state.BlockedReason,
			ChecksTotal: len(state.Checks),
			Shots:       len(state.Screenshots),
		}
		for _, check := range state.Checks {
			if check.OK {
				task.ChecksOK++
			}
		}
		if state.Issue != nil {
			task.Title = state.Issue.Title
			task.URL = state.Issue.URL
		}
		if task.Title == "" {
			task.Title = fmt.Sprintf("issue #%d", item.Number)
		}
		if task.Active && task.StartedAt != "" {
			task.ElapsedSec = secondsSince(task.StartedAt, now)
		} else if task.UpdatedAt != "" {
			task.ElapsedSec = secondsSince(task.UpdatedAt, now)
		}
		if entry, ok := repoEntries[item.Repo]; ok {
			entry.Issues++
			entry.Counts[phase]++
		}
		snapshot.Tasks = append(snapshot.Tasks, task)
	}

	for _, name := range repoOrder {
		snapshot.Repos = append(snapshot.Repos, *repoEntries[name])
	}
	sortTasks(snapshot.Tasks)
	snapshot.Active = len(routerState.Active)
	snapshot.Queued = len(routerState.Queued)
	return snapshot
}

func sortTasks(tasks []webTask) {
	rank := func(task webTask) int {
		switch {
		case task.Active:
			return 0
		case task.Queued:
			return 1
		case task.Phase == "awaiting_approval":
			return 2
		case task.Phase == "blocked":
			return 3
		case task.Phase == "pr_open":
			return 4
		case task.Phase == "done" || task.Phase == "cancelled":
			return 9
		default:
			return 5
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		ri, rj := rank(tasks[i]), rank(tasks[j])
		if ri != rj {
			return ri < rj
		}
		return tasks[i].UpdatedAt > tasks[j].UpdatedAt
	})
}

func phaseLabel(phase string) string {
	switch phase {
	case "", "new":
		return "待处理"
	case "planning":
		return "规划中"
	case "awaiting_approval":
		return "等待确认"
	case "implementing":
		return "实现中"
	case "blocked":
		return "阻塞"
	case "pr_open":
		return "PR 待合并"
	case "merging":
		return "合并中"
	case "done":
		return "已完成"
	case "cancelled":
		return "已取消"
	}
	return phase
}

func secondsSince(raw string, now time.Time) int64 {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0
	}
	seconds := int64(now.Sub(parsed).Seconds())
	if seconds < 0 {
		return 0
	}
	return seconds
}

// shutdownWeb stops the dashboard listener on daemon exit.
func shutdownWeb(server *http.Server) {
	if server == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

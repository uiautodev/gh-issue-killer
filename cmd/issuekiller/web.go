package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The dashboard is a SvelteKit SPA built by `npm run build` in frontend/ into
// web/dist. The placeholder file keeps `go build` working on a checkout that
// has never installed node; such a binary serves the "build the frontend"
// page instead of the app.
//
//go:embed all:web/dist
var webDist embed.FS

const webDistRoot = "web/dist"

// embeddedWeb returns the built app rooted at web/dist.
func embeddedWeb() fs.FS {
	sub, err := fs.Sub(webDist, webDistRoot)
	if err != nil {
		panic("web/dist: " + err.Error())
	}
	return sub
}

const webBuildHint = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><title>issuekiller</title></head>
<body style="font:14px/1.6 ui-monospace,SFMono-Regular,Menlo,monospace;max-width:42rem;margin:4rem auto;padding:0 1rem">
<h1>请先构建前端 / Build the frontend first</h1>
<p>这个二进制里没有打包好的看板。在仓库根目录运行：</p>
<pre>npm --prefix frontend install
npm --prefix frontend run build
go build ./cmd/issuekiller</pre>
<p>The dashboard is not embedded in this binary. Run the commands above from
the repository root so <code>cmd/issuekiller/web/dist</code> contains the
SvelteKit build output.</p>
</body></html>
`

type webOptions struct {
	identity    string
	version     string
	embedded    bool
	configError string
	// reload restarts the engine: a config save, a dashboard restart or a
	// finished self-update all apply through it. Nil disables those buttons.
	reload func()
}

type webServer struct {
	cfg         *Config
	router      *Router
	store       *Store
	log         *Logger
	identity    string
	version     string
	started     time.Time
	embedded    bool
	configError string
	reload      func()
	// assets is the built dashboard; tests inject a fs.FS. Nil means the
	// embedded web/dist.
	assets fs.FS
	// latestRelease is replaced in tests so /api/update never hits GitHub.
	latestRelease func(ctx context.Context) (*releaseInfo, error)
	updateMu      sync.Mutex
	// authMu guards the in-flight Device Flow session; the token file itself is
	// written atomically by saveTokenRecord.
	authMu     sync.Mutex
	deviceAuth *deviceSession
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
	Time       string `json:"time"`
	Version    string `json:"version"`
	Platform   string `json:"platform"`
	PID        int    `json:"pid"`
	UptimeSec  int64  `json:"uptimeSec"`
	ConfigFile string `json:"configFile"`
	LogPath    string `json:"logPath"`
	StatePath  string `json:"statePath"`
	Identity   string `json:"identity"`
	Webhook    bool   `json:"webhook"`
	Embedded   bool   `json:"embedded"`
	// ConfigError is set while the engine runs without a usable config (first
	// run or a broken edit): the dashboard then opens the settings panel.
	ConfigError string    `json:"configError"`
	PlanLimit   int       `json:"planLimit"`
	WorkLimit   int       `json:"workLimit"`
	Active      int       `json:"active"`
	Queued      int       `json:"queued"`
	Waiting     int       `json:"waiting"`
	Repos       []webRepo `json:"repos"`
	Tasks       []webTask `json:"tasks"`
}

func startWeb(cfg *Config, router *Router, store *Store, log *Logger, opts webOptions) (*http.Server, error) {
	if cfg.WebPort <= 0 {
		return nil, nil
	}
	web := &webServer{
		cfg: cfg, router: router, store: store, log: log,
		identity: opts.identity, version: opts.version, started: time.Now(),
		embedded: opts.embedded, configError: opts.configError, reload: opts.reload,
		assets: embeddedWeb(),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", web.handleStatic)
	mux.HandleFunc("/api/state", web.handleState)
	mux.HandleFunc("/api/issue", web.handleIssue)
	mux.HandleFunc("/api/logs", web.handleLogs)
	mux.HandleFunc("/api/action", web.handleAction)
	mux.HandleFunc("/api/open-url", web.handleOpenURL)
	mux.HandleFunc("/api/config", web.handleConfig)
	mux.HandleFunc("/api/auth", web.handleAuth)
	mux.HandleFunc("/api/auth/device", web.handleAuthDevice)
	mux.HandleFunc("/api/auth/token", web.handleAuthToken)
	mux.HandleFunc("/api/detect", web.handleDetect)
	mux.HandleFunc("/api/restart", web.handleRestart)
	mux.HandleFunc("/api/update", web.handleUpdate)

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

// handleStatic serves the built SPA. Files are looked up by exact name; a
// missing asset is a 404, while any other path falls back to index.html so
// client-side routes (and a stale bookmark) keep working. `/api/` paths never
// fall back: an unknown endpoint must not return HTML.
func (w *webServer) handleStatic(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		rw.Header().Set("Allow", "GET, HEAD")
		http.Error(rw, "GET required", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(req.URL.Path, "/api/") {
		http.NotFound(rw, req)
		return
	}
	files := w.assets
	if files == nil {
		files = embeddedWeb()
	}

	name := strings.TrimPrefix(path.Clean("/"+req.URL.Path), "/")
	if name == "" || name == "." {
		name = "index.html"
	}
	if strings.HasPrefix(name, "..") || strings.HasPrefix(name, "/") {
		http.NotFound(rw, req)
		return
	}
	if name == "index.html" {
		w.serveIndex(rw, req, files)
		return
	}

	if data, ok := readWebFile(files, name); ok {
		serveWebBytes(rw, req, name, data)
		return
	}
	// A missing hashed asset is a real 404; the browser would otherwise parse
	// the HTML fallback as JavaScript.
	if strings.HasPrefix(req.URL.Path, "/assets/") || filepath.Ext(name) != "" {
		http.NotFound(rw, req)
		return
	}
	w.serveIndex(rw, req, files)
}

// readWebFile reads a regular file; directories are not static content.
func readWebFile(files fs.FS, name string) ([]byte, bool) {
	info, err := fs.Stat(files, name)
	if err != nil || info.IsDir() {
		return nil, false
	}
	data, err := fs.ReadFile(files, name)
	if err != nil {
		return nil, false
	}
	return data, true
}

func serveWebBytes(rw http.ResponseWriter, req *http.Request, name string, data []byte) {
	rw.Header().Set("Content-Type", webContentType(name))
	if strings.HasPrefix(name, "assets/") {
		// SvelteKit hashes these filenames, so they never change in place.
		rw.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		rw.Header().Set("Cache-Control", "public, max-age=3600")
	}
	if req.Method == http.MethodHead {
		rw.Header().Set("Content-Length", strconv.Itoa(len(data)))
		rw.WriteHeader(http.StatusOK)
		return
	}
	_, _ = rw.Write(data)
}

func (w *webServer) serveIndex(rw http.ResponseWriter, req *http.Request, files fs.FS) {
	data, ok := readWebFile(files, "index.html")
	if !ok {
		rw.Header().Set("Content-Type", "text/html; charset=utf-8")
		rw.Header().Set("Cache-Control", "no-store")
		if req.Method == http.MethodHead {
			rw.WriteHeader(http.StatusOK)
			return
		}
		_, _ = rw.Write([]byte(webBuildHint))
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	if req.Method == http.MethodHead {
		rw.WriteHeader(http.StatusOK)
		return
	}
	_, _ = rw.Write(data)
}

// webContentType covers the handful of extensions SvelteKit emits even where
// the host has no mime table (Windows) and keeps charset handling explicit.
func webContentType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	case ".woff":
		return "font/woff"
	case ".png":
		return "image/png"
	case ".ico":
		return "image/x-icon"
	}
	if detected := mime.TypeByExtension(filepath.Ext(name)); detected != "" {
		return detected
	}
	return "application/octet-stream"
}

// handleAction approves or cancels a task. The dashboard binds to loopback
// only, but browsers can still be tricked into cross-origin POSTs, so require
// a JSON content type and a loopback Origin.
func (w *webServer) handleAction(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeWebJSON(rw, http.StatusMethodNotAllowed, false, "POST required")
		return
	}
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		writeWebJSON(rw, http.StatusUnsupportedMediaType, false, "application/json required")
		return
	}
	if !w.loopbackOrigin(req) {
		writeWebJSON(rw, http.StatusForbidden, false, "forbidden origin")
		return
	}
	var body struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(rw, req.Body, 4096)).Decode(&body); err != nil {
		writeWebJSON(rw, http.StatusBadRequest, false, "bad request")
		return
	}
	var err error
	switch body.Action {
	case "approve":
		if w.router == nil {
			writeWebJSON(rw, http.StatusConflict, false, "setup mode: save a config first")
			return
		}
		err = w.router.WebApprove(body.Repo, body.Number)
	case "cancel":
		if w.router == nil {
			writeWebJSON(rw, http.StatusConflict, false, "setup mode: save a config first")
			return
		}
		err = w.router.WebCancel(body.Repo, body.Number)
	default:
		writeWebJSON(rw, http.StatusBadRequest, false, "unknown action")
		return
	}
	if err != nil {
		writeWebJSON(rw, http.StatusConflict, false, err.Error())
		return
	}
	w.log.Info("web action %s on %s#%d", body.Action, body.Repo, body.Number)
	writeWebJSON(rw, http.StatusOK, true, "")
}

// handleOpenURL opens an external link from the dashboard in the system
// browser. The desktop window is a WKWebView, which ignores target="_blank"
// and cannot start a browser itself, so the page posts the click here. The
// same JSON + loopback Origin checks as /api/action apply: a hostile page must
// not be able to spray windows at the user.
func (w *webServer) handleOpenURL(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeWebJSON(rw, http.StatusMethodNotAllowed, false, "POST required")
		return
	}
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		writeWebJSON(rw, http.StatusUnsupportedMediaType, false, "application/json required")
		return
	}
	if !w.loopbackOrigin(req) {
		writeWebJSON(rw, http.StatusForbidden, false, "forbidden origin")
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(rw, req.Body, 4096)).Decode(&body); err != nil {
		writeWebJSON(rw, http.StatusBadRequest, false, "bad request")
		return
	}
	target, err := url.Parse(strings.TrimSpace(body.URL))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		writeWebJSON(rw, http.StatusBadRequest, false, "only http(s) URLs can be opened")
		return
	}
	if err := openExternalURL(target.String()); err != nil {
		writeWebJSON(rw, http.StatusInternalServerError, false, "open browser: "+err.Error())
		return
	}
	w.log.Info("web open external link on %s", target.Host)
	writeWebJSON(rw, http.StatusOK, true, "")
}

// writeWebJSON keeps API errors in one shape: {"ok":false,"error":"..."}.
func writeWebJSON(rw http.ResponseWriter, status int, ok bool, message string) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(status)
	payload := map[string]any{"ok": ok}
	if message != "" {
		payload["error"] = message
	}
	_ = json.NewEncoder(rw).Encode(payload)
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
	routerState := RouterSnapshot{Active: map[string]bool{}, Queued: map[string]bool{}, Pools: map[string]PoolDepth{}}
	if w.router != nil {
		routerState = w.router.Snapshot()
	}
	issued := w.store.Snapshot()

	snapshot := webSnapshot{
		Time:        now.UTC().Format(time.RFC3339),
		Version:     w.version,
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		PID:         os.Getpid(),
		UptimeSec:   int64(now.Sub(w.started).Seconds()),
		ConfigFile:  w.cfg.File,
		LogPath:     daemonLogPath(),
		StatePath:   stateFile(),
		Identity:    w.identity,
		Webhook:     w.cfg.Webhook,
		Embedded:    w.embedded,
		ConfigError: w.configError,
		PlanLimit:   w.cfg.PlanParallel,
		WorkLimit:   w.cfg.ImplementParallel,
		Repos:       []webRepo{},
		Tasks:       []webTask{},
	}

	repoEntries := make(map[string]*webRepo, len(w.cfg.Repos))
	repoOrder := make([]string, 0, len(w.cfg.Repos))
	for _, repo := range w.cfg.Repos {
		meta := w.store.Meta(repo.FullName)
		base := ""
		trees := repo.WorktreeRoot
		if w.router != nil {
			if app := w.router.contexts[repo.FullName]; app != nil && app.Local != nil {
				base = app.Local.Base
				if trees == "" {
					trees = app.Local.WorktreeRoot
				}
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
	for _, task := range snapshot.Tasks {
		if phaseNeedsUser(task.Phase) {
			snapshot.Waiting++
		}
	}
	return snapshot
}

// phaseNeedsUser reports whether a task is waiting for the approver: a plan to
// approve, a merge/feedback decision, or a blocked task to unblock.
func phaseNeedsUser(phase string) bool {
	switch phase {
	case "awaiting_approval", "pr_open", "blocked":
		return true
	}
	return false
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

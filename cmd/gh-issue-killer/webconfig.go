package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The desktop shell has no terminal, so the dashboard doubles as the setup
// wizard: GET/PUT /api/config edits the same config.json the daemon watches,
// /api/detect reuses the wizard probes for the dropdowns, and /api/restart and
// /api/update apply a change without touching the shell.

func (w *webServer) writeJSON(rw http.ResponseWriter, status int, payload any) {
	rw.Header().Set("Content-Type", "application/json; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.WriteHeader(status)
	_ = json.NewEncoder(rw).Encode(payload)
}

// requireJSONPost enforces the same rules as /api/action: a JSON body and a
// loopback Origin. The dashboard listens on 127.0.0.1 only, but any page in the
// browser can POST to it.
func (w *webServer) requireJSONPost(rw http.ResponseWriter, req *http.Request, limit int64) bool {
	if req.Method != http.MethodPost {
		w.writeJSON(rw, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "POST required"})
		return false
	}
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		w.writeJSON(rw, http.StatusUnsupportedMediaType, map[string]any{"ok": false, "error": "application/json required"})
		return false
	}
	if !w.loopbackOrigin(req) {
		w.writeJSON(rw, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden origin"})
		return false
	}
	if limit > 0 {
		req.Body = http.MaxBytesReader(rw, req.Body, limit)
	}
	return true
}

// requestRestart delays the reload so the HTTP response reaches the browser
// before the listener goes away.
func (w *webServer) requestRestart() {
	if w.reload == nil {
		return
	}
	time.AfterFunc(300*time.Millisecond, w.reload)
}

func (w *webServer) handleConfig(rw http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		w.writeJSON(rw, http.StatusOK, w.configPayload())
	case http.MethodPut:
		w.saveConfig(rw, req)
	default:
		rw.Header().Set("Allow", "GET, PUT")
		w.writeJSON(rw, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "GET or PUT required"})
	}
}

// configPayload returns the raw file (not the normalized config) so the form
// can round-trip keys this build does not know about.
func (w *webServer) configPayload() map[string]any {
	path := w.cfg.File
	if path == "" {
		path = defaultConfigPath()
	}
	doc := map[string]any{}
	exists := false
	loadError := ""
	if data, err := os.ReadFile(path); err == nil {
		exists = true
		if err := json.Unmarshal(data, &doc); err != nil {
			loadError = fmt.Sprintf("config is not valid JSON: %v", err)
			doc = map[string]any{}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		loadError = err.Error()
	}
	return map[string]any{
		"path":        path,
		"exists":      exists,
		"config":      doc,
		"defaults":    w.configDefaults(),
		"version":     w.version,
		"platform":    runtime.GOOS + "/" + runtime.GOARCH,
		"embedded":    w.embedded,
		"configError": w.configError,
		"error":       loadError,
	}
}

// configDefaults are the values the daemon would use when the key is absent.
func (w *webServer) configDefaults() map[string]any {
	cfg := w.cfg
	return map[string]any{
		"codingAgent":         cfg.CodingAgent,
		"model":               cfg.Model,
		"proxy":               cfg.Proxy,
		"updateProxy":         cfg.UpdateProxy,
		"pollIntervalSec":     int(cfg.PollInterval / time.Second),
		"webhook":             cfg.Webhook,
		"maxFixRounds":        cfg.MaxFixRounds,
		"planParallel":        cfg.PlanParallel,
		"implementParallel":   cfg.ImplementParallel,
		"planTimeoutMin":      int(cfg.PlanTimeout / time.Minute),
		"implementTimeoutMin": int(cfg.ImplementTimeout / time.Minute),
		"webPort":             cfg.WebPort,
		"selfUpdate":          cfg.SelfUpdate,
		"language":            cfg.Language,
	}
}

func (w *webServer) saveConfig(rw http.ResponseWriter, req *http.Request) {
	if !strings.HasPrefix(req.Header.Get("Content-Type"), "application/json") {
		w.writeJSON(rw, http.StatusUnsupportedMediaType, map[string]any{"ok": false, "error": "application/json required"})
		return
	}
	if !w.loopbackOrigin(req) {
		w.writeJSON(rw, http.StatusForbidden, map[string]any{"ok": false, "error": "forbidden origin"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(rw, req.Body, 1<<20))
	if err != nil {
		w.writeJSON(rw, http.StatusBadRequest, map[string]any{"ok": false, "error": "body too large"})
		return
	}
	doc, err := configDocument(body)
	if err != nil {
		w.writeJSON(rw, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	path := w.cfg.File
	if path == "" {
		path = defaultConfigPath()
	}
	if err := normalizeConfigDocument(doc, path); err != nil {
		w.writeJSON(rw, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		w.writeJSON(rw, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	data = append(data, '\n')
	if err := atomicWriteFile(path, data); err != nil {
		w.writeJSON(rw, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.log.Info("web: wrote config %s", path)
	w.writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "path": path, "restarting": w.reload != nil})
	w.requestRestart()
}

// configDocument accepts either the config object itself or {"config": {...}}.
func configDocument(body []byte) (map[string]any, error) {
	var envelope struct {
		Config json.RawMessage `json:"config"`
	}
	raw := body
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Config) > 0 {
		raw = envelope.Config
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("config must be a JSON object: %v", err)
	}
	if doc == nil {
		return nil, errors.New("config must be a JSON object")
	}
	return doc, nil
}

// normalizeConfigDocument rejects values the daemon would silently rewrite and
// then runs the same loader the daemon uses, so a saved file is always one the
// next start accepts.
func normalizeConfigDocument(doc map[string]any, file string) error {
	if value, ok := doc["proxy"]; ok {
		raw, ok := value.(string)
		if !ok {
			return errors.New("proxy must be a string")
		}
		if _, err := parseProxy(raw); err != nil {
			return err
		}
	}
	if value, ok := doc["updateProxy"]; ok {
		raw, ok := value.(string)
		if !ok {
			return errors.New("updateProxy must be a string")
		}
		mirror, err := normalizeMirror(raw)
		if err != nil {
			return err
		}
		if mirror == "" {
			delete(doc, "updateProxy")
		} else {
			doc["updateProxy"] = mirror
		}
	}
	if value, ok := doc["webPort"]; ok {
		port, err := numberAsInt(value)
		if err != nil || port < 0 || port > 65535 {
			return errors.New("webPort must be an integer between 0 and 65535")
		}
		doc["webPort"] = port
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(file); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	tmp := file + ".validate"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	defer os.Remove(tmp)
	if _, err := loadConfig(tmp); err != nil {
		return err
	}
	return nil
}

func numberAsInt(value any) (int, error) {
	switch typed := value.(type) {
	case float64:
		if typed != math.Trunc(typed) {
			return 0, errors.New("not an integer")
		}
		return int(typed), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(typed))
	case nil:
		return 0, nil
	}
	return 0, errors.New("not a number")
}

type detectAgent struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Binary  string `json:"binary"`
	Path    string `json:"path"`
	Version string `json:"version"`
}

type detectRepo struct {
	Repo         string `json:"repo"`
	Permission   string `json:"permission"`
	WebhookReady bool   `json:"webhookReady"`
	Error        string `json:"error,omitempty"`
}

type detectPayload struct {
	Agents []detectAgent `json:"agents"`
	Models []string      `json:"models"`
	Proxy  string        `json:"proxy"`
	Repos  []detectRepo  `json:"repos"`
	Error  string        `json:"error,omitempty"`
}

// handleDetect runs the wizard probes on demand. Everything degrades to an
// empty list: a missing CLI, an unreachable proxy or a missing token must not
// break the settings panel.
func (w *webServer) handleDetect(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.writeJSON(rw, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "GET required"})
		return
	}
	query := req.URL.Query()
	payload := detectPayload{Agents: []detectAgent{}, Models: []string{}, Repos: []detectRepo{}}
	for _, agent := range wizardDetectAgents() {
		payload.Agents = append(payload.Agents, detectAgent{
			ID: agent.Info.ID, Name: agent.Info.Name, Binary: agent.Info.Binary,
			Path: agent.Path, Version: agent.Version,
		})
	}
	agent := strings.ToLower(strings.TrimSpace(query.Get("agent")))
	if agent == "" {
		agent = w.cfg.CodingAgent
	}
	if agent != "" {
		payload.Models = append(payload.Models, wizardModelList(agent)...)
	}
	if query.Get("proxy") == "1" {
		payload.Proxy = wizardDetectProxy()
	}
	if raw := strings.TrimSpace(query.Get("repos")); raw != "" {
		repos, err := parseRepoList(raw)
		if err != nil {
			payload.Error = err.Error()
		} else {
			if len(repos) > 10 {
				repos = repos[:10]
			}
			proxy, proxyErr := parseProxy(w.cfg.Proxy)
			if proxyErr != nil {
				payload.Error = proxyErr.Error()
			} else if results, checkErr := wizardRepoCheck(proxy, repos); checkErr != nil {
				payload.Error = checkErr.Error()
			} else {
				for _, result := range results {
					payload.Repos = append(payload.Repos, detectRepo{
						Repo: result.Repo, Permission: result.Permission,
						WebhookReady: result.WebhookReady, Error: result.Err,
					})
				}
			}
		}
	}
	w.writeJSON(rw, http.StatusOK, payload)
}

func (w *webServer) handleRestart(rw http.ResponseWriter, req *http.Request) {
	if !w.requireJSONPost(rw, req, 4096) {
		return
	}
	w.writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "restarting": w.reload != nil})
	w.log.Info("web: restart requested")
	w.requestRestart()
}

type updatePayload struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	URL       string `json:"url"`
	Notes     string `json:"notes"`
	Installed bool   `json:"installed"`
	Error     string `json:"error,omitempty"`
}

// latestReleaseInfo lets tests replace the GitHub lookup.
func (w *webServer) latestReleaseInfo(ctx context.Context) (*releaseInfo, error) {
	if w.latestRelease != nil {
		return w.latestRelease(ctx)
	}
	return lookupLatestRelease(ctx, w.cfg.Proxy)
}

func (w *webServer) handleUpdate(rw http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet, http.MethodPost:
	default:
		w.writeJSON(rw, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "GET or POST required"})
		return
	}
	apply := req.Method == http.MethodPost
	if apply && !w.requireJSONPost(rw, req, 4096) {
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 10*time.Minute)
	defer cancel()
	payload := updatePayload{Current: w.version}
	rel, err := w.latestReleaseInfo(ctx)
	if err != nil {
		payload.Error = err.Error()
		w.writeJSON(rw, http.StatusOK, payload)
		return
	}
	payload.Latest = rel.TagName
	payload.URL = rel.HTMLURL
	payload.Notes = rel.Body
	payload.Available = isNewerVersion(rel.TagName, w.version)
	if !apply || !payload.Available {
		w.writeJSON(rw, http.StatusOK, payload)
		return
	}
	if !w.updateMu.TryLock() {
		w.writeJSON(rw, http.StatusConflict, map[string]any{"ok": false, "error": "an update is already running"})
		return
	}
	defer w.updateMu.Unlock()
	if err := installRelease(ctx, w.log, rel, w.cfg.Proxy, w.cfg.UpdateProxy); err != nil {
		payload.Error = err.Error()
		w.writeJSON(rw, http.StatusInternalServerError, payload)
		return
	}
	payload.Installed = true
	w.writeJSON(rw, http.StatusOK, payload)
	w.requestRestart()
}

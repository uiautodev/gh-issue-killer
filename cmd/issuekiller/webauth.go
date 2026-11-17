package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

// The dashboard is the first-run UI for the desktop app, so GitHub Device Flow
// lives here: POST /api/auth/device starts it, GET /api/auth/device polls, and
// POST /api/auth/token accepts a pasted personal access token as a fallback.

const deviceLoginPollFloor = 5 * time.Second

type deviceSession struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresAt       time.Time
	Interval        time.Duration
	NextPoll        time.Time
}

type devicePayload struct {
	UserCode        string `json:"userCode"`
	VerificationURI string `json:"verificationUri"`
	ExpiresIn       int    `json:"expiresIn"`
	Interval        int    `json:"interval"`
}

type authPayload struct {
	Authenticated bool           `json:"authenticated"`
	Login         string         `json:"login,omitempty"`
	Source        string         `json:"source,omitempty"`
	ExpiresAt     string         `json:"expiresAt,omitempty"`
	Refreshable   bool           `json:"refreshable"`
	Pending       bool           `json:"pending,omitempty"`
	Device        *devicePayload `json:"device,omitempty"`
	Error         string         `json:"error,omitempty"`
}

func devicePayloadFor(session *deviceSession) *devicePayload {
	if session == nil {
		return nil
	}
	return &devicePayload{
		UserCode:        session.UserCode,
		VerificationURI: session.VerificationURI,
		ExpiresIn:       int(time.Until(session.ExpiresAt).Seconds()),
		Interval:        int(session.Interval.Seconds()),
	}
}

func (w *webServer) authClient() *http.Client {
	proxyRaw := w.cfg.Proxy
	if proxyRaw == "" && w.cfg.File != "" {
		// Setup mode starts with a placeholder config; an existing file may
		// still carry the proxy needed to reach github.com.
		if loaded, err := loadConfig(w.cfg.File); err == nil {
			proxyRaw = loaded.Proxy
		}
	}
	proxy, _ := parseProxy(proxyRaw)
	return newHTTPClient(proxy, 30*time.Second)
}

func (w *webServer) authStatus() authPayload {
	payload := authPayload{}
	if strings.TrimSpace(os.Getenv("GH_AGENT_TOKEN")) != "" {
		payload.Authenticated = true
		payload.Source = "env"
	} else if record, err := loadTokenRecord(); err == nil && record.AccessToken != "" {
		expires := parseTokenTime(record.ExpiresAt)
		// A token that is already past its expiry only means the startup refresh
		// failed (network, revoked refresh token): ask the user to log in again.
		payload.Authenticated = expires.IsZero() || time.Now().Before(expires)
		payload.Source = "token"
		payload.Login = record.Login
		payload.ExpiresAt = record.ExpiresAt
		payload.Refreshable = record.RefreshToken != ""
	} else if token, err := tokenFromHostsYAML(); err == nil && token != "" {
		payload.Authenticated = true
		payload.Source = "hosts"
	}
	w.authMu.Lock()
	defer w.authMu.Unlock()
	if w.deviceAuth != nil {
		if time.Now().After(w.deviceAuth.ExpiresAt) {
			w.deviceAuth = nil
		} else {
			payload.Pending = true
			payload.Device = devicePayloadFor(w.deviceAuth)
		}
	}
	return payload
}

func (w *webServer) handleAuth(rw http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.writeJSON(rw, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "GET required"})
		return
	}
	w.writeJSON(rw, http.StatusOK, w.authStatus())
}

func (w *webServer) startDeviceLogin() (authPayload, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	code, err := requestDeviceCode(ctx, w.authClient(), oauthClientID())
	if err != nil {
		return authPayload{}, err
	}
	interval := time.Duration(code.Interval) * time.Second
	if interval < deviceLoginPollFloor {
		interval = deviceLoginPollFloor
	}
	session := &deviceSession{
		DeviceCode:      code.DeviceCode,
		UserCode:        code.UserCode,
		VerificationURI: firstNonEmpty(code.VerificationURIComplete, code.VerificationURI),
		ExpiresAt:       time.Now().Add(time.Duration(code.ExpiresIn) * time.Second),
		Interval:        interval,
		NextPoll:        time.Now().Add(interval),
	}
	w.authMu.Lock()
	w.deviceAuth = session
	w.authMu.Unlock()
	return authPayload{Pending: true, Device: devicePayloadFor(session)}, nil
}

func (w *webServer) pollDeviceLogin() (authPayload, error) {
	w.authMu.Lock()
	session := w.deviceAuth
	if session == nil {
		w.authMu.Unlock()
		return authPayload{}, errNoDeviceLogin
	}
	now := time.Now()
	if now.After(session.ExpiresAt) {
		w.deviceAuth = nil
		w.authMu.Unlock()
		return authPayload{}, errDeviceCodeExpired
	}
	if now.Before(session.NextPoll) {
		payload := authPayload{Pending: true, Device: devicePayloadFor(session)}
		w.authMu.Unlock()
		return payload, nil
	}
	deviceCode := session.DeviceCode
	w.authMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	record, status, err := pollDeviceToken(ctx, w.authClient(), oauthClientID(), deviceCode)
	if err != nil {
		w.authMu.Lock()
		if w.deviceAuth == session {
			w.deviceAuth = nil
		}
		w.authMu.Unlock()
		return authPayload{}, err
	}

	w.authMu.Lock()
	if w.deviceAuth != session {
		w.authMu.Unlock()
		return w.authStatus(), nil
	}
	switch status {
	case devicePending:
		session.NextPoll = time.Now().Add(session.Interval)
		payload := authPayload{Pending: true, Device: devicePayloadFor(session)}
		w.authMu.Unlock()
		return payload, nil
	case deviceSlowDown:
		session.Interval += 5 * time.Second
		session.NextPoll = time.Now().Add(session.Interval)
		payload := authPayload{Pending: true, Device: devicePayloadFor(session)}
		w.authMu.Unlock()
		return payload, nil
	}
	w.deviceAuth = nil
	w.authMu.Unlock()

	// Resolve the bot login for the header; a network hiccup here must not
	// discard a token that GitHub already issued.
	if user, userErr := fetchUserWithToken(ctx, w.authClient(), record.AccessToken); userErr == nil {
		record.Login = user.Login
	}
	if err := saveTokenRecord(record); err != nil {
		return authPayload{}, err
	}
	w.log.Info("web: GitHub device login succeeded as @%s", firstNonEmpty(record.Login, "unknown"))
	w.requestRestart()
	return w.authStatus(), nil
}

func (w *webServer) handleAuthDevice(rw http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodPost:
		if !w.requireJSONPost(rw, req, 4096) {
			return
		}
		payload, err := w.startDeviceLogin()
		if err != nil {
			w.writeJSON(rw, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		payload.Pending = true
		w.writeJSON(rw, http.StatusOK, payload)
	case http.MethodGet:
		payload, err := w.pollDeviceLogin()
		if err != nil {
			status := http.StatusConflict
			if errors.Is(err, errDeviceCodeExpired) {
				status = http.StatusGone
			}
			w.writeJSON(rw, status, map[string]any{"ok": false, "authenticated": false, "error": err.Error()})
			return
		}
		w.writeJSON(rw, http.StatusOK, payload)
	default:
		rw.Header().Set("Allow", "GET, POST")
		w.writeJSON(rw, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "GET or POST required"})
	}
}

func (w *webServer) handleAuthToken(rw http.ResponseWriter, req *http.Request) {
	if !w.requireJSONPost(rw, req, 1<<20) {
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		w.writeJSON(rw, http.StatusBadRequest, map[string]any{"ok": false, "error": "bad request"})
		return
	}
	token := strings.TrimSpace(body.Token)
	if token == "" {
		w.writeJSON(rw, http.StatusBadRequest, map[string]any{"ok": false, "error": "token is required"})
		return
	}
	ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
	defer cancel()
	user, err := fetchUserWithToken(ctx, w.authClient(), token)
	if err != nil {
		w.writeJSON(rw, http.StatusBadRequest, map[string]any{"ok": false, "error": "GitHub rejected the token: " + err.Error()})
		return
	}
	record := tokenRecord{AccessToken: token, TokenType: "bearer", Login: user.Login}
	if err := saveTokenRecord(record); err != nil {
		w.writeJSON(rw, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	w.log.Info("web: saved a pasted GitHub token for @%s", user.Login)
	w.writeJSON(rw, http.StatusOK, map[string]any{"ok": true, "authenticated": true, "login": user.Login})
	w.requestRestart()
}

func fetchUserWithToken(ctx context.Context, client *http.Client, token string) (User, error) {
	gh := &GH{
		Token:   token,
		APIBase: firstNonEmpty(strings.TrimSpace(os.Getenv("GITHUB_API_URL")), "https://api.github.com"),
		Client:  client,
		Log:     NewSilentLogger(),
	}
	return gh.GetUser(ctx)
}

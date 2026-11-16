package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func isolateTokenState(t *testing.T) {
	t.Helper()
	oldState := stateDirOverride
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = oldState })

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GH_AGENT_TOKEN", "")
	t.Setenv("GH_CONFIG_DIR", filepath.Join(home, "gh-agent"))
	t.Setenv("GITHUB_API_URL", "")
	t.Setenv("GHIK_OAUTH_CLIENT_ID", "")
}

func withOAuthServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	oldDevice, oldToken := oauthDeviceCodeURL, oauthAccessTokenURL
	oauthDeviceCodeURL = server.URL + "/login/device/code"
	oauthAccessTokenURL = server.URL + "/login/oauth/access_token"
	t.Cleanup(func() {
		oauthDeviceCodeURL, oauthAccessTokenURL = oldDevice, oldToken
		server.Close()
	})
	return server
}

func oauthJSON(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func TestTokenFileUses0600AndRoundTrips(t *testing.T) {
	isolateTokenState(t)
	record := tokenRecord{
		AccessToken:           "gho_access",
		RefreshToken:          "ghr_refresh",
		ExpiresAt:             time.Now().Add(8 * time.Hour).UTC().Format(time.RFC3339),
		RefreshTokenExpiresAt: time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339),
		Login:                 "bot",
	}
	if err := saveTokenRecord(record); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(tokenFile())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	loaded, err := loadTokenRecord()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != record.AccessToken || loaded.RefreshToken != record.RefreshToken || loaded.Login != "bot" {
		t.Fatalf("loaded = %+v", loaded)
	}
}

func TestPollDeviceTokenPendingThenSuccess(t *testing.T) {
	isolateTokenState(t)
	calls := 0
	withOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/device/code" {
			oauthJSON(w, deviceCode{DeviceCode: "dev", UserCode: "ABCD-1234", VerificationURI: "https://github.com/login/device", ExpiresIn: 900, Interval: 5})
			return
		}
		calls++
		if calls == 1 {
			oauthJSON(w, tokenResponse{Error: "authorization_pending"})
			return
		}
		oauthJSON(w, tokenResponse{AccessToken: "gho_new", TokenType: "bearer", RefreshToken: "ghr_new", ExpiresIn: 28800, RefreshTokenExpiresIn: 15897600})
	})
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	client := server.Client()

	record, status, err := pollDeviceToken(context.Background(), client, "client", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if status != devicePending || record.AccessToken != "" {
		t.Fatalf("first poll = %q %+v, want pending", status, record)
	}

	record, status, err = pollDeviceToken(context.Background(), client, "client", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if status != deviceSuccess || record.AccessToken != "gho_new" || record.RefreshToken != "ghr_new" {
		t.Fatalf("second poll = %q %+v", status, record)
	}
	if !tokenNeedsRefresh(record, time.Now().Add(7*time.Hour+56*time.Minute)) {
		t.Fatal("token should be refreshed inside the 5 minute skew near expiry")
	}
	if tokenNeedsRefresh(record, time.Now()) {
		t.Fatal("fresh token should not be refreshed immediately")
	}
}

func TestPollDeviceTokenSlowDownAndExpired(t *testing.T) {
	isolateTokenState(t)
	responses := []tokenResponse{{Error: "slow_down"}, {Error: "expired_token"}}
	index := 0
	withOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		oauthJSON(w, responses[index])
		index++
	})
	client := httptest.NewServer(http.NotFoundHandler()).Client()

	_, status, err := pollDeviceToken(context.Background(), client, "client", "dev")
	if err != nil || status != deviceSlowDown {
		t.Fatalf("slow_down = %q, %v", status, err)
	}
	_, _, err = pollDeviceToken(context.Background(), client, "client", "dev")
	if !errors.Is(err, errDeviceCodeExpired) {
		t.Fatalf("expired error = %v", err)
	}
}

func TestRefreshAccessTokenRotatesAndSaves(t *testing.T) {
	isolateTokenState(t)
	if err := saveTokenRecord(tokenRecord{AccessToken: "old", RefreshToken: "old-refresh", Login: "bot"}); err != nil {
		t.Fatal(err)
	}
	var form string
	withOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		form = r.Form.Encode()
		oauthJSON(w, tokenResponse{
			AccessToken: "new", TokenType: "bearer", RefreshToken: "rotated",
			ExpiresIn: 28800, RefreshTokenExpiresIn: 15897600,
		})
	})
	client := httptest.NewServer(http.NotFoundHandler()).Client()

	updated, err := refreshAccessToken(context.Background(), client, "client", tokenRecord{AccessToken: "old", RefreshToken: "old-refresh", Login: "bot"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AccessToken != "new" || updated.RefreshToken != "rotated" || updated.Login != "bot" {
		t.Fatalf("updated = %+v", updated)
	}
	if !strings.Contains(form, "grant_type=refresh_token") || !strings.Contains(form, "refresh_token=old-refresh") {
		t.Fatalf("form = %s", form)
	}
	loaded, err := loadTokenRecord()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "new" || loaded.RefreshToken != "rotated" {
		t.Fatalf("saved = %+v", loaded)
	}
}

func TestRefreshAccessTokenFailureKeepsStoredToken(t *testing.T) {
	isolateTokenState(t)
	if err := saveTokenRecord(tokenRecord{AccessToken: "old", RefreshToken: "old-refresh"}); err != nil {
		t.Fatal(err)
	}
	withOAuthServer(t, func(w http.ResponseWriter, r *http.Request) {
		oauthJSON(w, tokenResponse{Error: "bad_refresh_token", ErrorDescription: "expired"})
	})
	client := httptest.NewServer(http.NotFoundHandler()).Client()

	if _, err := refreshAccessToken(context.Background(), client, "client", tokenRecord{AccessToken: "old", RefreshToken: "old-refresh"}); err == nil {
		t.Fatal("expected refresh failure")
	}
	loaded, err := loadTokenRecord()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AccessToken != "old" {
		t.Fatalf("stored token changed on failure: %+v", loaded)
	}
}

func TestLoadAgentTokenPrefersEnvThenTokenFileThenHosts(t *testing.T) {
	isolateTokenState(t)
	if _, err := loadAgentTokenSource(nil); err == nil || !strings.Contains(err.Error(), "log in from the dashboard") {
		t.Fatalf("missing token error = %v", err)
	}

	hostsDir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", hostsDir)
	if err := os.WriteFile(filepath.Join(hostsDir, "hosts.yml"), []byte("github.com:\n    oauth_token: legacy-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := loadAgentTokenSource(nil)
	if err != nil || source.Token != "legacy-token" || source.Source != "hosts" {
		t.Fatalf("hosts source = %+v, %v", source, err)
	}

	if err := saveTokenRecord(tokenRecord{AccessToken: "file-token", RefreshToken: "refresh"}); err != nil {
		t.Fatal(err)
	}
	source, err = loadAgentTokenSource(nil)
	if err != nil || source.Token != "file-token" || source.Source != "token" || source.Refresh == nil {
		t.Fatalf("file source = %+v, %v", source, err)
	}

	t.Setenv("GH_AGENT_TOKEN", "env-token")
	source, err = loadAgentTokenSource(nil)
	if err != nil || source.Token != "env-token" || source.Source != "env" || source.Refresh != nil {
		t.Fatalf("env source = %+v, %v", source, err)
	}
}

func TestGHRefreshesOnceOnUnauthorized(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		attempt := calls
		mu.Unlock()
		if attempt == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		oauthJSON(w, User{Login: "bot", ID: 7})
	}))
	defer server.Close()

	gh := &GH{Token: "old", APIBase: server.URL, Client: server.Client(), Log: NewSilentLogger()}
	gh.refresh = func(context.Context) (string, error) {
		gh.Token = "new"
		return "new", nil
	}
	user, err := gh.GetUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if user.Login != "bot" || gh.Token != "new" {
		t.Fatalf("user = %+v token = %q", user, gh.Token)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestOAuthClientIDCanBeOverridden(t *testing.T) {
	isolateTokenState(t)
	if oauthClientID() != defaultOAuthClientID {
		t.Fatalf("client id = %q", oauthClientID())
	}
	t.Setenv("GHIK_OAUTH_CLIENT_ID", "custom")
	if oauthClientID() != "custom" {
		t.Fatalf("client id = %q", oauthClientID())
	}
}

// --- dashboard auth endpoints ---------------------------------------------

func newAuthTestWeb(t *testing.T) (*webServer, string) {
	t.Helper()
	isolateTokenState(t)
	dir := t.TempDir()
	web := &webServer{
		cfg: &Config{File: filepath.Join(dir, "config.json"), WebPort: 8787},
		log: NewSilentLogger(),
	}
	return web, dir
}

func postAuthDevice(t *testing.T, web *webServer) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/api/auth/device", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	web.handleAuthDevice(recorder, request)
	return recorder
}

func TestDeviceLoginEndToEndSavesToken(t *testing.T) {
	web, _ := newAuthTestWeb(t)
	var apiCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/device/code":
			oauthJSON(w, deviceCode{DeviceCode: "dev", UserCode: "ABCD-1234", VerificationURI: "https://github.com/login/device", ExpiresIn: 900, Interval: 5})
		case "/login/oauth/access_token":
			apiCalls++
			if apiCalls == 1 {
				oauthJSON(w, tokenResponse{Error: "authorization_pending"})
				return
			}
			oauthJSON(w, tokenResponse{AccessToken: "gho_bot", TokenType: "bearer", RefreshToken: "ghr_bot", ExpiresIn: 28800})
		case "/user":
			oauthJSON(w, User{Login: "bot", ID: 42})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	oldDevice, oldToken := oauthDeviceCodeURL, oauthAccessTokenURL
	oauthDeviceCodeURL, oauthAccessTokenURL = server.URL+"/login/device/code", server.URL+"/login/oauth/access_token"
	t.Setenv("GITHUB_API_URL", server.URL)
	t.Cleanup(func() { oauthDeviceCodeURL, oauthAccessTokenURL = oldDevice, oldToken })

	recorder := postAuthDevice(t, web)
	if recorder.Code != http.StatusOK {
		t.Fatalf("start status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var started authPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if !started.Pending || started.Device == nil || started.Device.UserCode != "ABCD-1234" {
		t.Fatalf("start payload = %+v", started)
	}

	web.authMu.Lock()
	web.deviceAuth.NextPoll = time.Now().Add(-time.Second)
	web.authMu.Unlock()
	poll := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		web.handleAuthDevice(rec, httptest.NewRequest(http.MethodGet, "/api/auth/device", nil))
		return rec
	}
	if recorder := poll(); recorder.Code != http.StatusOK {
		t.Fatalf("pending status = %d: %s", recorder.Code, recorder.Body.String())
	}
	web.authMu.Lock()
	web.deviceAuth.NextPoll = time.Now().Add(-time.Second)
	web.authMu.Unlock()
	recorder = poll()
	if recorder.Code != http.StatusOK {
		t.Fatalf("success status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var done authPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &done); err != nil {
		t.Fatal(err)
	}
	if !done.Authenticated || done.Login != "bot" {
		t.Fatalf("done payload = %+v", done)
	}
	record, err := loadTokenRecord()
	if err != nil {
		t.Fatal(err)
	}
	if record.AccessToken != "gho_bot" || record.Login != "bot" || record.RefreshToken != "ghr_bot" {
		t.Fatalf("token record = %+v", record)
	}
}

func TestPasteTokenVerifiesAndSaves(t *testing.T) {
	web, _ := newAuthTestWeb(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" || r.Header.Get("Authorization") != "Bearer ghp_pasted" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		oauthJSON(w, User{Login: "bot", ID: 9})
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	request := httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"ghp_pasted"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	web.handleAuthToken(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	record, err := loadTokenRecord()
	if err != nil {
		t.Fatal(err)
	}
	if record.AccessToken != "ghp_pasted" || record.Login != "bot" {
		t.Fatalf("record = %+v", record)
	}
}

func TestPasteTokenRejectsBadToken(t *testing.T) {
	web, _ := newAuthTestWeb(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL)

	request := httptest.NewRequest(http.MethodPost, "/api/auth/token", strings.NewReader(`{"token":"bad"}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	web.handleAuthToken(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	if _, err := loadTokenRecord(); err == nil {
		t.Fatal("a rejected token must not be stored")
	}
}

func TestAuthStatusWithoutToken(t *testing.T) {
	web, _ := newAuthTestWeb(t)
	recorder := httptest.NewRecorder()
	web.handleAuth(recorder, httptest.NewRequest(http.MethodGet, "/api/auth", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var payload authPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Authenticated || payload.Source != "" || payload.Pending {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestWebIndexHasLoginPanel(t *testing.T) {
	data, err := webIndex.ReadFile("web/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, want := range []string{
		`id="auth-panel"`, `data-i18n="auth.hint"`, `id="auth-code"`,
		`/api/auth/device`, `/api/auth/token`, `"auth.hint"`, `"auth.login"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("index.html is missing %s", want)
		}
	}
}

func TestDeviceLoginExpires(t *testing.T) {
	web, _ := newAuthTestWeb(t)
	web.deviceAuth = &deviceSession{DeviceCode: "dev", UserCode: "ABCD", ExpiresAt: time.Now().Add(-time.Minute)}
	recorder := httptest.NewRecorder()
	web.handleAuthDevice(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/device", nil))
	if recorder.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410 (%s)", recorder.Code, recorder.Body.String())
	}
	if web.deviceAuth != nil {
		t.Fatal("expired session must be cleared")
	}
}

func TestDeviceLoginPollRespectsInterval(t *testing.T) {
	web, _ := newAuthTestWeb(t)
	web.deviceAuth = &deviceSession{
		DeviceCode: "dev", UserCode: "ABCD",
		ExpiresAt: time.Now().Add(time.Minute), Interval: 5 * time.Second,
		NextPoll: time.Now().Add(time.Minute),
	}
	// The poll must answer pending without touching GitHub.
	old := oauthAccessTokenURL
	oauthAccessTokenURL = "http://127.0.0.1:1/unreachable"
	t.Cleanup(func() { oauthAccessTokenURL = old })
	recorder := httptest.NewRecorder()
	web.handleAuthDevice(recorder, httptest.NewRequest(http.MethodGet, "/api/auth/device", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var payload authPayload
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.Pending || payload.Device == nil {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestGHRefreshIsSerializedAcrossPollers(t *testing.T) {
	var mu sync.Mutex
	refreshCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		oauthJSON(w, User{Login: "bot", ID: 7})
	}))
	defer server.Close()

	gh := &GH{Token: "old", APIBase: server.URL, Client: server.Client(), Log: NewSilentLogger()}
	gh.refresh = func(context.Context) (string, error) {
		mu.Lock()
		refreshCalls++
		mu.Unlock()
		return "new", nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := gh.GetUser(context.Background()); err != nil {
				t.Errorf("GetUser: %v", err)
			}
		}()
	}
	wg.Wait()
	if refreshCalls != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshCalls)
	}
}

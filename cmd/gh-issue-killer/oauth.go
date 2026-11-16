package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The OAuth app is registered by the project owner with Device Flow enabled and
// a 8h access-token lifetime, so the daemon can refresh without asking again.
// GHIK_OAUTH_CLIENT_ID overrides it for forks and local testing.
const defaultOAuthClientID = "Ov23linflGhn5OPmkRSm"

// repo covers contents/issues/PRs, admin:repo_hook creates the webhook, and
// read:user gives the dashboard the bot's login after a device login.
const oauthScope = "repo admin:repo_hook read:user"

// Refresh a little before expiry so a long poll cycle or a clone never starts
// with a token that dies mid-request.
const tokenRefreshSkew = 5 * time.Minute

// Variables, not constants, so oauth_test can point them at an httptest server.
var (
	oauthDeviceCodeURL  = "https://github.com/login/device/code"
	oauthAccessTokenURL = "https://github.com/login/oauth/access_token"
)

var (
	errDeviceCodeExpired = errors.New("the login code expired; start the login again")
	errDeviceCodeDenied  = errors.New("GitHub login was denied")
	errNoDeviceLogin     = errors.New("no GitHub login in progress")
)

// tokenRecord is the on-disk shape of ~/.gh-issue-killer/token. expires_at and
// refresh_token_expires_at are RFC3339 so the file stays readable by hand.
type tokenRecord struct {
	AccessToken           string `json:"access_token"`
	TokenType             string `json:"token_type,omitempty"`
	Scope                 string `json:"scope,omitempty"`
	RefreshToken          string `json:"refresh_token,omitempty"`
	ExpiresAt             string `json:"expires_at,omitempty"`
	RefreshTokenExpiresAt string `json:"refresh_token_expires_at,omitempty"`
	Login                 string `json:"login,omitempty"`
}

func tokenFile() string { return filepath.Join(stateDir(), "token") }

func oauthClientID() string {
	if value := strings.TrimSpace(os.Getenv("GHIK_OAUTH_CLIENT_ID")); value != "" {
		return value
	}
	return defaultOAuthClientID
}

func parseTokenTime(raw string) time.Time {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func tokenNeedsRefresh(record tokenRecord, now time.Time) bool {
	expires := parseTokenTime(record.ExpiresAt)
	if expires.IsZero() {
		return false
	}
	return !now.Add(tokenRefreshSkew).Before(expires)
}

// atomicWriteFileMode keeps the token file at 0600 instead of the 0644 used for
// configs: it is a credential, not something other local users should read.
func atomicWriteFileMode(file string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", file, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, file)
}

func loadTokenRecord() (tokenRecord, error) {
	var record tokenRecord
	data, err := os.ReadFile(tokenFile())
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	return record, nil
}

func saveTokenRecord(record tokenRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFileMode(tokenFile(), data, 0o600)
}

type deviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	TokenType             string `json:"token_type"`
	Scope                 string `json:"scope"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
	Error                 string `json:"error"`
	ErrorDescription      string `json:"error_description"`
}

type devicePollStatus string

const (
	devicePending  devicePollStatus = "pending"
	deviceSlowDown devicePollStatus = "slow_down"
	deviceSuccess  devicePollStatus = "success"
)

func oauthForm(ctx context.Context, client *http.Client, endpoint string, form url.Values, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "gh-issue-killer-go/0.1")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode >= 400 {
		return fmt.Errorf("github oauth %d: %s", response.StatusCode, tailString(string(data), 3))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode oauth response: %w", err)
	}
	return nil
}

func requestDeviceCode(ctx context.Context, client *http.Client, clientID string) (deviceCode, error) {
	var code deviceCode
	form := url.Values{"client_id": {clientID}, "scope": {oauthScope}}
	if err := oauthForm(ctx, client, oauthDeviceCodeURL, form, &code); err != nil {
		return code, err
	}
	if code.DeviceCode == "" || code.UserCode == "" {
		return code, errors.New("github did not return a device code")
	}
	if code.VerificationURI == "" {
		code.VerificationURI = "https://github.com/login/device"
	}
	if code.ExpiresIn <= 0 {
		code.ExpiresIn = 900
	}
	if code.Interval <= 0 {
		code.Interval = 5
	}
	return code, nil
}

// tokenRecordFrom keeps the refresh token and login when GitHub omits them in a
// refresh response; a rotated refresh token replaces the stored one.
func tokenRecordFrom(response tokenResponse, previous tokenRecord, now time.Time) tokenRecord {
	record := tokenRecord{
		AccessToken:  response.AccessToken,
		TokenType:    response.TokenType,
		Scope:        response.Scope,
		RefreshToken: previous.RefreshToken,
		Login:        previous.Login,
	}
	if response.RefreshToken != "" {
		record.RefreshToken = response.RefreshToken
	}
	if response.ExpiresIn > 0 {
		record.ExpiresAt = now.Add(time.Duration(response.ExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	}
	if response.RefreshTokenExpiresIn > 0 {
		record.RefreshTokenExpiresAt = now.Add(time.Duration(response.RefreshTokenExpiresIn) * time.Second).UTC().Format(time.RFC3339)
	} else {
		record.RefreshTokenExpiresAt = previous.RefreshTokenExpiresAt
	}
	return record
}

func pollDeviceToken(ctx context.Context, client *http.Client, clientID, deviceCodeValue string) (tokenRecord, devicePollStatus, error) {
	var response tokenResponse
	form := url.Values{
		"client_id":   {clientID},
		"device_code": {deviceCodeValue},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}
	if err := oauthForm(ctx, client, oauthAccessTokenURL, form, &response); err != nil {
		return tokenRecord{}, "", err
	}
	switch response.Error {
	case "":
		if response.AccessToken == "" {
			return tokenRecord{}, "", errors.New("github returned no access token")
		}
		return tokenRecordFrom(response, tokenRecord{}, time.Now()), deviceSuccess, nil
	case "authorization_pending":
		return tokenRecord{}, devicePending, nil
	case "slow_down":
		return tokenRecord{}, deviceSlowDown, nil
	case "expired_token":
		return tokenRecord{}, "", errDeviceCodeExpired
	case "access_denied":
		return tokenRecord{}, "", errDeviceCodeDenied
	default:
		message := firstNonEmpty(response.ErrorDescription, response.Error)
		return tokenRecord{}, "", fmt.Errorf("github login failed: %s", message)
	}
}

func refreshAccessToken(ctx context.Context, client *http.Client, clientID string, record tokenRecord) (tokenRecord, error) {
	if record.RefreshToken == "" {
		return record, errors.New("token cannot be refreshed; log in again")
	}
	var response tokenResponse
	form := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {record.RefreshToken},
	}
	if err := oauthForm(ctx, client, oauthAccessTokenURL, form, &response); err != nil {
		return record, err
	}
	if response.Error != "" {
		return record, fmt.Errorf("github token refresh: %s", firstNonEmpty(response.ErrorDescription, response.Error))
	}
	if response.AccessToken == "" {
		return record, errors.New("github returned no access token on refresh")
	}
	updated := tokenRecordFrom(response, record, time.Now())
	if err := saveTokenRecord(updated); err != nil {
		return record, err
	}
	return updated, nil
}

// tokenSource is what NewGH needs: the token to use now plus, for a token-file
// login that carries a refresh token, a way to replace it on expiry or a 401.
type tokenSource struct {
	Token     string
	Source    string
	ExpiresAt time.Time
	Refresh   func(ctx context.Context) (string, error)
}

func tokenFromHostsYAML() (string, error) {
	configDir := strings.TrimSpace(os.Getenv("GH_CONFIG_DIR"))
	if configDir == "" {
		configDir = filepath.Join(homeDir(), ".config", "gh-agent")
	}
	file := filepath.Join(configDir, "hosts.yml")
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	host := firstNonEmpty(strings.TrimSpace(os.Getenv("GH_HOST")), "github.com")
	inHost := false
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(trimmed, ":") {
			inHost = strings.TrimSuffix(trimmed, ":") == host
			continue
		}
		if !inHost {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "oauth_token", "token":
			token := strings.Trim(strings.TrimSpace(value), `"'`)
			if token != "" {
				return token, nil
			}
		}
	}
	return "", errors.New("no token in " + file)
}

// loadAgentTokenSource resolves the credential in this order: GH_AGENT_TOKEN,
// the built-in token file, then the old gh-agent hosts.yml. The last one is
// read-only compatibility; nothing creates or rewrites it any more.
func loadAgentTokenSource(proxy *Proxy) (tokenSource, error) {
	if value := strings.TrimSpace(os.Getenv("GH_AGENT_TOKEN")); value != "" {
		return tokenSource{Token: value, Source: "env"}, nil
	}
	if record, err := loadTokenRecord(); err == nil && record.AccessToken != "" {
		client := newHTTPClient(proxy, 30*time.Second)
		if tokenNeedsRefresh(record, time.Now()) && record.RefreshToken != "" {
			if updated, refreshErr := refreshAccessToken(context.Background(), client, oauthClientID(), record); refreshErr == nil {
				record = updated
			}
		}
		source := tokenSource{
			Token:     record.AccessToken,
			Source:    "token",
			ExpiresAt: parseTokenTime(record.ExpiresAt),
		}
		if record.RefreshToken != "" {
			source.Refresh = func(ctx context.Context) (string, error) {
				current, err := loadTokenRecord()
				if err != nil || current.AccessToken == "" {
					current = record
				}
				updated, err := refreshAccessToken(ctx, client, oauthClientID(), current)
				if err != nil {
					return "", err
				}
				return updated.AccessToken, nil
			}
		}
		return source, nil
	}
	if token, err := tokenFromHostsYAML(); err == nil && token != "" {
		return tokenSource{Token: token, Source: "hosts"}, nil
	}
	return tokenSource{}, errors.New("no GitHub token: log in from the dashboard (recommended: a separate bot account), set GH_AGENT_TOKEN, or run `gh-issue-killer -init`")
}

func loadAgentToken() (string, error) {
	source, err := loadAgentTokenSource(nil)
	if err != nil {
		return "", err
	}
	return source.Token, nil
}

// loginDeviceFlowInteractive walks the same Device Flow in a terminal so
// `gh-issue-killer -init` can log in without gh or a wrapper.
func loginDeviceFlowInteractive(ctx context.Context, proxy *Proxy, out io.Writer) (tokenRecord, error) {
	client := newHTTPClient(proxy, 30*time.Second)
	code, err := requestDeviceCode(ctx, client, oauthClientID())
	if err != nil {
		return tokenRecord{}, err
	}
	uri := firstNonEmpty(code.VerificationURIComplete, code.VerificationURI)
	fmt.Fprintf(out, "\nGitHub login / GitHub 登录\n")
	fmt.Fprintf(out, "  1. Open / 打开: %s\n", uri)
	fmt.Fprintf(out, "  2. Enter code / 输入代码: %s\n", code.UserCode)
	fmt.Fprintf(out, "  Use the bot account, not your main account. / 请用 GitHub 小号，不要用主账号。\n")
	fmt.Fprintf(out, "Waiting for authorization / 等待授权…\n")

	interval := time.Duration(code.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 5 * time.Second
	}
	expires := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)
	for {
		if time.Now().After(expires) {
			return tokenRecord{}, errDeviceCodeExpired
		}
		select {
		case <-ctx.Done():
			return tokenRecord{}, ctx.Err()
		case <-time.After(interval):
		}
		record, status, pollErr := pollDeviceToken(ctx, client, oauthClientID(), code.DeviceCode)
		if pollErr != nil {
			return tokenRecord{}, pollErr
		}
		switch status {
		case devicePending:
			continue
		case deviceSlowDown:
			interval += 5 * time.Second
			continue
		}
		if err := saveTokenRecord(record); err != nil {
			return tokenRecord{}, err
		}
		fmt.Fprintf(out, "Logged in / 已登录。\n")
		return record, nil
	}
}

func tokenLoginStatus() (tokenRecord, bool) {
	record, err := loadTokenRecord()
	if err != nil || record.AccessToken == "" {
		return tokenRecord{}, false
	}
	return record, true
}

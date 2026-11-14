package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github api %d: %s", e.Status, e.Message)
}

func isStatus(err error, status int) bool {
	apiErr, ok := err.(*APIError)
	return ok && apiErr.Status == status
}

type User struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (u User) DisplayName() string {
	if strings.TrimSpace(u.Name) != "" {
		return u.Name
	}
	return u.Login
}

func (u User) NoreplyEmail() string {
	return fmt.Sprintf("%d+%s@users.noreply.github.com", u.ID, u.Login)
}

type RepoInfo struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions struct {
		Admin    bool `json:"admin"`
		Maintain bool `json:"maintain"`
		Push     bool `json:"push"`
		Triage   bool `json:"triage"`
		Pull     bool `json:"pull"`
	} `json:"permissions"`
}

type Label struct {
	Name string `json:"name"`
}

type Issue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	HTMLURL     string    `json:"html_url"`
	User        User      `json:"user"`
	Assignees   []User    `json:"assignees"`
	Labels      []Label   `json:"labels"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Reaction struct {
	Content string `json:"content"`
	User    User   `json:"user"`
}

type ReviewComment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	Path      string    `json:"path"`
	Line      int       `json:"line"`
	CreatedAt time.Time `json:"created_at"`
}

type Review struct {
	ID          int64     `json:"id"`
	Body        string    `json:"body"`
	User        User      `json:"user"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
}

type Comment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	User      User      `json:"user"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PullRequest struct {
	Number         int    `json:"number"`
	HTMLURL        string `json:"html_url"`
	State          string `json:"state"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	User           User   `json:"user"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
}

type CheckRun struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Conclusion *string    `json:"conclusion"`
	DetailsURL string     `json:"details_url"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at"`
}

type CheckRuns struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []CheckRun `json:"check_runs"`
}

type Hook struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Active bool     `json:"active"`
	Events []string `json:"events"`
	Config struct {
		URL         string `json:"url"`
		ContentType string `json:"content_type"`
	} `json:"config"`
}

type GH struct {
	Token      string
	APIBase    string
	UploadBase string
	Client     *http.Client
	Upload     *http.Client
	Log        *Logger
	Agent      User
}

func loadAgentToken() (string, error) {
	if value := strings.TrimSpace(os.Getenv("GH_AGENT_TOKEN")); value != "" {
		return value, nil
	}
	configDir := strings.TrimSpace(os.Getenv("GH_CONFIG_DIR"))
	if configDir == "" {
		configDir = filepath.Join(homeDir(), ".config", "gh-agent")
	}
	file := filepath.Join(configDir, "hosts.yml")
	if data, err := os.ReadFile(file); err == nil {
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
	}

	// Newer gh versions keep the OAuth token in the system keyring and only
	// leave the account name in hosts.yml. Ask gh-agent for the token in that
	// case. Never fall back to plain gh: that would silently use the human's
	// account.
	if token, err := tokenFromCLI(configDir); err == nil && token != "" {
		return token, nil
	}
	return "", fmt.Errorf("no gh-agent token found: set GH_AGENT_TOKEN, or run `GH_CONFIG_DIR=%s gh auth login`", configDir)
}

func tokenFromCLI(configDir string) (string, error) {
	env := mergeEnv(os.Environ(), map[string]string{"GH_CONFIG_DIR": configDir}, "GH_TOKEN", "GITHUB_TOKEN")
	type candidate struct {
		binary string
		args   []string
	}
	candidates := make([]candidate, 0, 2)
	seen := map[string]bool{}
	if path, err := exec.LookPath("gh-agent"); err == nil {
		candidates = append(candidates, candidate{binary: path, args: []string{"auth", "token"}})
		seen[filepath.Clean(path)] = true
	}
	// ~/.local/bin is not always on PATH of a background process.
	local := filepath.Join(homeDir(), ".local", "bin", "gh-agent")
	if info, statErr := os.Stat(local); statErr == nil && !info.IsDir() && !seen[filepath.Clean(local)] {
		candidates = append(candidates, candidate{binary: local, args: []string{"auth", "token"}})
	}
	for _, item := range candidates {
		result, err := runCmd(context.Background(), item.binary, item.args, CmdOptions{
			Env:          env,
			Timeout:      15 * time.Second,
			AllowFailure: true,
		})
		if err != nil || result.Code != 0 {
			continue
		}
		if token := strings.TrimSpace(result.Stdout); token != "" {
			return token, nil
		}
	}
	return "", fmt.Errorf("gh-agent auth token unavailable")
}

func NewGH(proxy *Proxy, log *Logger) (*GH, error) {
	return NewGHWithTimeout(proxy, log, 120*time.Second)
}

func NewGHWithTimeout(proxy *Proxy, log *Logger, timeout time.Duration) (*GH, error) {
	token, err := loadAgentToken()
	if err != nil {
		return nil, err
	}
	gh := &GH{
		Token:      token,
		APIBase:    firstNonEmpty(strings.TrimSpace(os.Getenv("GITHUB_API_URL")), "https://api.github.com"),
		UploadBase: "https://uploads.github.com",
		Client:     newHTTPClient(proxy, timeout),
		Upload:     newHTTPClient(proxy, 300*time.Second),
		Log:        log,
	}
	user, err := gh.GetUser(context.Background())
	if err != nil {
		return nil, fmt.Errorf("verify gh-agent token: %w", err)
	}
	gh.Agent = user
	return gh, nil
}

func (g *GH) do(ctx context.Context, method, path string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	endpoint := path
	if !strings.HasPrefix(path, "http://") && !strings.HasPrefix(path, "https://") {
		endpoint = g.APIBase + path
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+g.Token)
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		request.Header.Set("User-Agent", "gh-issue-killer-go/0.1")
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}

		response, err := g.Client.Do(request)
		if err != nil {
			lastErr = err
			if attempt < 3 {
				time.Sleep(time.Duration(attempt) * time.Second)
				continue
			}
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
		response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			lastErr = &APIError{Status: response.StatusCode, Message: tailString(string(data), 10)}
			if g.Log != nil {
				g.Log.Debug("github %s %s -> %d (retry %d)", method, path, response.StatusCode, attempt)
			}
			if attempt < 3 {
				time.Sleep(time.Duration(attempt*2) * time.Second)
				continue
			}
			return lastErr
		}
		if g.Log != nil {
			g.Log.Debug("github %s %s -> %d", method, path, response.StatusCode)
		}
		if response.StatusCode >= 400 {
			return &APIError{Status: response.StatusCode, Message: tailString(string(data), 20)}
		}
		if out != nil && len(data) > 0 {
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode %s %s: %w", method, path, err)
			}
		}
		return nil
	}
	return lastErr
}

func (g *GH) GetUser(ctx context.Context) (User, error) {
	var user User
	err := g.do(ctx, http.MethodGet, "/user", nil, &user)
	return user, err
}

func (g *GH) GetRepo(ctx context.Context, fullName string) (RepoInfo, error) {
	var repo RepoInfo
	err := g.do(ctx, http.MethodGet, "/repos/"+fullName, nil, &repo)
	return repo, err
}

func (g *GH) GetIssue(ctx context.Context, fullName string, number int) (Issue, error) {
	var issue Issue
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", fullName, number), nil, &issue)
	return issue, err
}

func (g *GH) ListOpenIssues(ctx context.Context, fullName string, since *time.Time) ([]Issue, error) {
	return g.ListIssues(ctx, fullName, "open", since)
}

func (g *GH) ListIssues(ctx context.Context, fullName, state string, since *time.Time) ([]Issue, error) {
	var all []Issue
	for page := 1; page <= 10; page++ {
		path := fmt.Sprintf("/repos/%s/issues?state=%s&per_page=100&sort=updated&direction=desc&page=%d", fullName, url.QueryEscape(state), page)
		if since != nil {
			path += "&since=" + url.QueryEscape(since.UTC().Format(time.RFC3339))
		}
		var pageIssues []Issue
		if err := g.do(ctx, http.MethodGet, path, nil, &pageIssues); err != nil {
			return nil, err
		}
		all = append(all, pageIssues...)
		if len(pageIssues) < 100 {
			break
		}
	}
	filtered := all[:0]
	for _, issue := range all {
		if issue.PullRequest == nil {
			filtered = append(filtered, issue)
		}
	}
	return filtered, nil
}

func (g *GH) ListComments(ctx context.Context, fullName string, number int) ([]Comment, error) {
	var all []Comment
	for page := 1; page <= 10; page++ {
		var comments []Comment
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&sort=created&direction=asc&page=%d", fullName, number, page)
		if err := g.do(ctx, http.MethodGet, path, nil, &comments); err != nil {
			return nil, err
		}
		all = append(all, comments...)
		if len(comments) < 100 {
			break
		}
	}
	return all, nil
}

func (g *GH) GetComment(ctx context.Context, fullName string, id int64) (Comment, error) {
	var comment Comment
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/comments/%d", fullName, id), nil, &comment)
	return comment, err
}

// agentCommentMarker tags every comment the daemon writes. A comment on the
// bot account that lacks this marker was written by a human and may carry a
// command, while the daemon's own status comments are ignored.
const agentCommentMarker = "<!-- ghik:agent -->"

var hookEvents = []string{"issues", "issue_comment", "reaction", "pull_request_review", "pull_request_review_comment"}

func (g *GH) PostComment(ctx context.Context, fullName string, number int, body string) (Comment, error) {
	var comment Comment
	err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", fullName, number),
		map[string]string{"body": body + "\n\n" + agentCommentMarker}, &comment)
	return comment, err
}

func (g *GH) UpdateIssue(ctx context.Context, fullName string, number int, patch map[string]any) error {
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", fullName, number), patch, nil)
}

func (g *GH) ReactIssue(ctx context.Context, fullName string, number int, content string) error {
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/reactions", fullName, number), map[string]string{"content": content}, nil)
}

func (g *GH) ReactComment(ctx context.Context, fullName string, commentID int64, content string) error {
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/comments/%d/reactions", fullName, commentID), map[string]string{"content": content}, nil)
}

func (g *GH) GetReviewComment(ctx context.Context, fullName string, commentID int64) (ReviewComment, error) {
	var comment ReviewComment
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/comments/%d", fullName, commentID), nil, &comment)
	return comment, err
}

func (g *GH) ListReviewComments(ctx context.Context, fullName string, prNumber int) ([]ReviewComment, error) {
	var all []ReviewComment
	for page := 1; page <= 5; page++ {
		var comments []ReviewComment
		path := fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=100&sort=created&direction=asc&page=%d", fullName, prNumber, page)
		if err := g.do(ctx, http.MethodGet, path, nil, &comments); err != nil {
			return nil, err
		}
		all = append(all, comments...)
		if len(comments) < 100 {
			break
		}
	}
	return all, nil
}

func (g *GH) GetReview(ctx context.Context, fullName string, prNumber int, reviewID int64) (Review, error) {
	var review Review
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/reviews/%d", fullName, prNumber, reviewID), nil, &review)
	return review, err
}

type PRFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
	Patch    string `json:"patch"`
}

// ListPRFiles returns the changed files of a pull request, including the diff
// hunks, which decide where an inline review comment may be attached.
func (g *GH) ListPRFiles(ctx context.Context, fullName string, number int) ([]PRFile, error) {
	var files []PRFile
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=100", fullName, number), nil, &files)
	return files, err
}

type PRReviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side,omitempty"`
	Body string `json:"body"`
}

// CreatePRReview submits a review. Inline comments are optional: a caller can
// retry without them when GitHub rejects a line that is not in the diff.
func (g *GH) CreatePRReview(ctx context.Context, fullName string, number int, commitID, body, event string, comments []PRReviewComment) (Review, error) {
	var review Review
	payload := map[string]any{
		"body":  body,
		"event": event,
	}
	if commitID != "" {
		payload["commit_id"] = commitID
	}
	if len(comments) > 0 {
		payload["comments"] = comments
	}
	err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/%d/reviews", fullName, number), payload, &review)
	return review, err
}

func (g *GH) ListReviews(ctx context.Context, fullName string, prNumber int) ([]Review, error) {
	var reviews []Review
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", fullName, prNumber), nil, &reviews)
	return reviews, err
}

func (g *GH) ReactReviewComment(ctx context.Context, fullName string, commentID int64, content string) error {
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls/comments/%d/reactions", fullName, commentID), map[string]string{"content": content}, nil)
}

// The daemon manages exactly two lifecycle labels: one while the flow waits
// for the user, one while the agent works. They are mutually exclusive.
const (
	awaitingLabel = "agent:awaiting-approval"
	workingLabel  = "agent:working"
)

var agentLabelSpecs = [][3]string{
	{awaitingLabel, "fbca04", "waiting for user confirmation"},
	{workingLabel, "1d76db", "the agent is working, no action needed"},
}

func (g *GH) EnsureLabels(ctx context.Context, fullName string) {
	for _, spec := range agentLabelSpecs {
		name, color, description := spec[0], spec[1], spec[2]
		err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/labels/%s", fullName, url.PathEscape(name)), nil, nil)
		if err == nil {
			continue
		}
		if !isStatus(err, http.StatusNotFound) {
			continue
		}
		_ = g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/labels", fullName), map[string]string{
			"name":        name,
			"color":       color,
			"description": description,
		}, nil)
	}
}

// SetAgentState puts exactly one lifecycle label on the issue, or none, and
// leaves every other label untouched.
// IssueLabels lists the labels currently on an issue.
func (g *GH) IssueLabels(ctx context.Context, fullName string, number int) ([]Label, error) {
	var labels []Label
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d/labels", fullName, number), nil, &labels)
	return labels, err
}

// SetAgentState enforces exactly one lifecycle label. It reads the current
// labels first: re-applying a state that is already set must not delete and
// re-add the label, which GitHub shows as visible churn in the issue timeline.
func (g *GH) SetAgentState(ctx context.Context, fullName string, number int, label string) {
	labels, err := g.IssueLabels(ctx, fullName, number)
	if err != nil {
		// A read failure must not leave a stale label behind.
		g.setAgentStateUnconditional(ctx, fullName, number, label)
		return
	}
	present := make(map[string]bool, len(labels))
	for _, item := range labels {
		present[item.Name] = true
	}
	for _, name := range []string{awaitingLabel, workingLabel} {
		if name == label || !present[name] {
			continue
		}
		_ = g.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/issues/%d/labels/%s", fullName, number, url.PathEscape(name)), nil, nil)
	}
	if label != "" && !present[label] {
		_ = g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/labels", fullName, number), map[string][]string{"labels": {label}}, nil)
	}
}

// setAgentStateUnconditional is the pre-idempotent behaviour, used only when
// the current labels cannot be read.
func (g *GH) setAgentStateUnconditional(ctx context.Context, fullName string, number int, label string) {
	for _, name := range []string{awaitingLabel, workingLabel} {
		_ = g.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/issues/%d/labels/%s", fullName, number, url.PathEscape(name)), nil, nil)
	}
	if label == "" {
		return
	}
	_ = g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/labels", fullName, number), map[string][]string{"labels": {label}}, nil)
}

func (g *GH) CreatePR(ctx context.Context, fullName, title, head, base, body string) (PullRequest, error) {
	var pr PullRequest
	err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/pulls", fullName), map[string]any{
		"title": title,
		"head":  head,
		"base":  base,
		"body":  body,
	}, &pr)
	if err == nil {
		return pr, nil
	}
	if !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return pr, err
	}
	owner := strings.SplitN(fullName, "/", 2)[0]
	var existing []PullRequest
	listPath := fmt.Sprintf("/repos/%s/pulls?state=open&per_page=10&head=%s", fullName, url.QueryEscape(owner+":"+head))
	if listErr := g.do(ctx, http.MethodGet, listPath, nil, &existing); listErr != nil || len(existing) == 0 {
		return pr, err
	}
	pr = existing[0]
	if patchErr := g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/pulls/%d", fullName, pr.Number), map[string]string{
		"title": title,
		"body":  body,
	}, &pr); patchErr != nil {
		return pr, patchErr
	}
	return pr, nil
}

func (g *GH) GetPR(ctx context.Context, fullName string, number int) (PullRequest, error) {
	var pr PullRequest
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/pulls/%d", fullName, number), nil, &pr)
	return pr, err
}

func (g *GH) ClosePR(ctx context.Context, fullName string, number int) error {
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/pulls/%d", fullName, number), map[string]string{"state": "closed"}, nil)
}

type mergeResult struct {
	Merged  bool   `json:"merged"`
	Message string `json:"message"`
}

func (g *GH) MergePR(ctx context.Context, fullName string, number int) (mergeResult, error) {
	var result mergeResult
	err := g.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/pulls/%d/merge", fullName, number), map[string]string{
		"merge_method": "squash",
	}, &result)
	return result, err
}

func (g *GH) DeleteBranch(ctx context.Context, fullName, branch string) error {
	return g.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/git/refs/heads/%s", fullName, branch), nil, nil)
}

func (g *GH) ListCheckRuns(ctx context.Context, fullName, sha string) (CheckRuns, error) {
	var runs CheckRuns
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=100", fullName, sha), nil, &runs)
	return runs, err
}

// HasWorkflows reports whether the repository has any GitHub Actions workflow,
// so a repository without CI does not wait for check runs that never appear.
func (g *GH) HasWorkflows(ctx context.Context, fullName string) bool {
	var payload struct {
		TotalCount int `json:"total_count"`
	}
	if err := g.do(ctx, http.MethodGet, "/repos/"+fullName+"/actions/workflows", nil, &payload); err != nil {
		return false
	}
	return payload.TotalCount > 0
}

// JobLogTail returns the last lines of an Actions job log. Queued jobs have no
// log yet, so an empty string is normal rather than an error.
func (g *GH) JobLogTail(ctx context.Context, fullName string, jobID int64, lines int) string {
	if jobID == 0 {
		return ""
	}
	endpoint := fmt.Sprintf("%s/repos/%s/actions/jobs/%d/logs", g.APIBase, fullName, jobID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	request.Header.Set("Authorization", "Bearer "+g.Token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "gh-issue-killer-go/0.1")
	response, err := g.Client.Do(request)
	if err != nil {
		return ""
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024))
	response.Body.Close()
	if readErr != nil || response.StatusCode >= 400 {
		return ""
	}
	return tailString(string(data), lines)
}

// ApproveWorkflowRun starts a workflow run that is waiting for maintainer
// approval, which is what a first-time contributor's pull request hits.
func (g *GH) ApproveWorkflowRun(ctx context.Context, fullName string, runID int64) error {
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/actions/runs/%d/approve", fullName, runID), nil, nil)
}

// RerunFailedJobs asks GitHub to re-run the failed jobs of a workflow run, for
// failures that were infrastructure rather than code.
func (g *GH) RerunFailedJobs(ctx context.Context, fullName string, runID int64) error {
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/actions/runs/%d/rerun-failed-jobs", fullName, runID), nil, nil)
}

// RequiredChecks returns the status check names a protected branch requires
// before merging. A nil slice means the branch has no required checks (or no
// protection at all, which the API reports as 404), so every check run is
// treated as a gate.
func (g *GH) RequiredChecks(ctx context.Context, fullName, branch string) ([]string, error) {
	if branch == "" {
		return nil, nil
	}
	var payload struct {
		Contexts []string `json:"contexts"`
		Checks   []struct {
			Context string `json:"context"`
		} `json:"checks"`
	}
	path := fmt.Sprintf("/repos/%s/branches/%s/protection/required_status_checks", fullName, url.PathEscape(branch))
	if err := g.do(ctx, http.MethodGet, path, nil, &payload); err != nil {
		if isStatus(err, http.StatusNotFound) || isStatus(err, http.StatusForbidden) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(payload.Contexts)+len(payload.Checks))
	names = append(names, payload.Contexts...)
	for _, check := range payload.Checks {
		names = append(names, check.Context)
	}
	return names, nil
}

func (g *GH) ListHooks(ctx context.Context, fullName string) ([]Hook, error) {
	var hooks []Hook
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/hooks?per_page=100", fullName), nil, &hooks)
	return hooks, err
}

func (g *GH) GetHook(ctx context.Context, fullName string, id int64) (Hook, error) {
	var hook Hook
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/hooks/%d", fullName, id), nil, &hook)
	return hook, err
}

func (g *GH) CreateHook(ctx context.Context, fullName, smeeURL, secret string) (Hook, error) {
	var hook Hook
	err := g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/hooks", fullName), map[string]any{
		"name":   "web",
		"active": true,
		"events": hookEvents,
		"config": map[string]string{
			"url":          smeeURL,
			"content_type": "json",
			"secret":       secret,
		},
	}, &hook)
	return hook, err
}

func (g *GH) DeleteHook(ctx context.Context, fullName string, id int64) error {
	return g.do(ctx, http.MethodDelete, fmt.Sprintf("/repos/%s/hooks/%d", fullName, id), nil, nil)
}

// UpdateHookEvents keeps the webhook subscription in sync with the events the
// daemon understands, e.g. adding "reaction" to a hook created by an older
// build.
func (g *GH) UpdateHookEvents(ctx context.Context, fullName string, id int64, events []string) error {
	return g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/hooks/%d", fullName, id),
		map[string]any{"events": events}, nil)
}

func (g *GH) ListCommentReactions(ctx context.Context, fullName string, commentID int64) ([]Reaction, error) {
	var reactions []Reaction
	err := g.do(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/issues/comments/%d/reactions?per_page=100", fullName, commentID), nil, &reactions)
	return reactions, err
}

func mimeForFile(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4":
		return "video/mp4"
	}
	return mime.TypeByExtension(filepath.Ext(name))
}

// UploadAttachment posts a screenshot to GitHub's user-attachments endpoint
// (the same one the web UI uses for drag & drop). Pure net/http, no curl.
func (g *GH) UploadAttachment(ctx context.Context, repoID int64, file string) (string, error) {
	handle, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer handle.Close()
	info, err := handle.Stat()
	if err != nil {
		return "", err
	}

	contentType := mimeForFile(file)
	endpoint := fmt.Sprintf("%s/user-attachments/assets?name=%s&content_type=%s&repository_id=%d",
		g.UploadBase, url.QueryEscape(filepath.Base(file)), url.QueryEscape(contentType), repoID)

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, handle)
	if err != nil {
		return "", err
	}
	request.ContentLength = info.Size()
	request.Header.Set("Authorization", "Bearer "+g.Token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("User-Agent", "gh-issue-killer-go/0.1")

	response, err := g.Upload.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1024*1024))
	if response.StatusCode >= 400 {
		return "", &APIError{Status: response.StatusCode, Message: tailString(string(data), 10)}
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("decode upload response: %w (%s)", err, tailString(string(data), 5))
	}
	if payload.URL == "" {
		return "", fmt.Errorf("upload response missing url: %s", tailString(string(data), 5))
	}
	return payload.URL, nil
}

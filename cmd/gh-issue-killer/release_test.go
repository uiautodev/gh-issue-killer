package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func newReleaseTestStore(t *testing.T) *Store {
	t.Helper()
	old := stateDirOverride
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = old })
	store, err := LoadStore()
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func releaseTestApp(gh *GH, store *Store, release ReleaseConfig) *AppContext {
	return &AppContext{
		Repo:  RepoConfig{FullName: "acme/app", Release: release},
		Local: &LocalRepo{Base: "main"},
		GH:    gh,
		Store: store,
		Log:   NewSilentLogger(),
	}
}

func TestVersionFromTag(t *testing.T) {
	cases := []struct {
		tag     string
		version releaseVersion
		ok      bool
	}{
		{"v1.2.3", releaseVersion{1, 2, 3}, true},
		{"V2.0.7", releaseVersion{2, 0, 7}, true},
		{"1.4.0", releaseVersion{1, 4, 0}, true},
		{"v1.2.3-rc1", releaseVersion{1, 2, 3}, true},
		{"v1.two.3", releaseVersion{}, false},
		{"release", releaseVersion{}, false},
		{"", releaseVersion{}, false},
	}
	for _, c := range cases {
		version, ok := versionFromTag(c.tag)
		if ok != c.ok || version != c.version {
			t.Fatalf("versionFromTag(%q) = %+v, %v; want %+v, %v", c.tag, version, ok, c.version, c.ok)
		}
	}
	if got := (releaseVersion{1, 2, 3}).String(); got != "v1.2.3" {
		t.Fatalf("String() = %q", got)
	}
}

func TestBumpVersion(t *testing.T) {
	cases := []struct {
		part string
		want releaseVersion
	}{
		{bumpPatch, releaseVersion{1, 2, 4}},
		{bumpMinor, releaseVersion{1, 3, 0}},
		{bumpMajor, releaseVersion{2, 0, 0}},
		{"", releaseVersion{1, 2, 4}},
	}
	for _, c := range cases {
		if got := bumpVersion(releaseVersion{1, 2, 3}, c.part); got != c.want {
			t.Fatalf("bumpVersion(%q) = %+v, want %+v", c.part, got, c.want)
		}
	}
}

func TestLatestVersionTag(t *testing.T) {
	tags := []Tag{{Name: "v1.2.3"}, {Name: "nightly"}, {Name: "v1.10.0"}, {Name: "v1.9.9"}, {Name: "v1.2.3-rc1"}}
	version, name, ok := latestVersionTag(tags)
	if !ok || name != "v1.10.0" || version != (releaseVersion{1, 10, 0}) {
		t.Fatalf("latestVersionTag = %+v, %q, %v", version, name, ok)
	}
	if _, _, ok := latestVersionTag([]Tag{{Name: "nightly"}}); ok {
		t.Fatal("a non-version tag must not count as a release")
	}
	if _, _, ok := latestVersionTag(nil); ok {
		t.Fatal("an empty tag list has no version")
	}
}

func TestClassifyCommit(t *testing.T) {
	features := []string{"feat: add an export button", "feat(web): add a button", "feat!: drop Go 1.20", "Feature: add", "  FEAT: uppercase  "}
	for _, title := range features {
		if got := classifyCommit(title); got != commitFeature {
			t.Fatalf("classifyCommit(%q) = %q, want feature", title, got)
		}
	}
	fixes := []string{"fix: a crash", "fix(ci): a job", "chore: bump deps", "refactor: split the router", "Fix the typo", ""}
	for _, title := range fixes {
		if got := classifyCommit(title); got != commitFix {
			t.Fatalf("classifyCommit(%q) = %q, want fix", title, got)
		}
	}
}

func TestNextReleaseVersion(t *testing.T) {
	current := releaseVersion{1, 2, 3}
	if got := nextReleaseVersion(current, []string{"fix: a", "chore: b"}); got != (releaseVersion{1, 2, 4}) {
		t.Fatalf("fixes bumped to %+v", got)
	}
	if got := nextReleaseVersion(current, []string{"fix: a", "feat: b"}); got != (releaseVersion{1, 3, 0}) {
		t.Fatalf("feature bumped to %+v", got)
	}
	if got := nextReleaseVersion(current, []string{"feat(ui): add a button"}); got != (releaseVersion{1, 3, 0}) {
		t.Fatalf("scoped feature bumped to %+v", got)
	}
	if got := nextReleaseVersion(current, nil); got != (releaseVersion{1, 2, 4}) {
		t.Fatalf("empty batch bumped to %+v", got)
	}
}

func TestInitialRelease(t *testing.T) {
	if got := initialRelease.String(); got != "v0.1.0" {
		t.Fatalf("initialRelease = %q, want v0.1.0", got)
	}
}

func TestReleaseDue(t *testing.T) {
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	if !releaseDue("", 1, now) {
		t.Fatal("a repo that was never checked is due")
	}
	if !releaseDue("yesterday", 1, now) {
		t.Fatal("an unparsable timestamp must not block releases forever")
	}
	cases := []struct {
		last     string
		days     int
		expected bool
	}{
		{now.Add(-23 * time.Hour).Format(time.RFC3339), 1, false},
		{now.Add(-25 * time.Hour).Format(time.RFC3339), 1, true},
		{now.Add(-47 * time.Hour).Format(time.RFC3339), 2, false},
		{now.Add(-49 * time.Hour).Format(time.RFC3339), 2, true},
		{now.Add(-time.Hour).Format(time.RFC3339), 0, false},
	}
	for _, c := range cases {
		if got := releaseDue(c.last, c.days, now); got != c.expected {
			t.Fatalf("releaseDue(%q, %d) = %v, want %v", c.last, c.days, got, c.expected)
		}
	}
}

func TestCheckReleaseBumpsMinorForFeature(t *testing.T) {
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/app/tags":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"name": "v1.2.3"},
				{"name": "nightly"},
				{"name": "v1.1.0"},
			})
		case "/repos/acme/app/compare/v1.2.3...main":
			_ = json.NewEncoder(w).Encode(map[string]any{"commits": []map[string]any{
				{"sha": "abcdef1234567890", "commit": map[string]string{"message": "feat: add an export button\n\nlonger body"}},
				{"sha": "1234567890abcdef", "commit": map[string]string{"message": "fix: a crash"}},
			}})
		case "/repos/acme/app/releases":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"tag_name": "v1.3.0", "html_url": "https://github.com/acme/app/releases/tag/v1.3.0"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	store := newReleaseTestStore(t)
	app := releaseTestApp(gh, store, ReleaseConfig{Enabled: true, IntervalDays: 1})
	if err := checkRelease(context.Background(), app); err != nil {
		t.Fatal(err)
	}

	var posted string
	for _, call := range calls.snapshot() {
		if call.method == http.MethodPost && call.path == "/repos/acme/app/releases" {
			posted = call.body
		}
	}
	if posted == "" {
		t.Fatal("no release was created")
	}
	var payload struct {
		TagName         string `json:"tag_name"`
		Name            string `json:"name"`
		Body            string `json:"body"`
		Draft           bool   `json:"draft"`
		Prerelease      bool   `json:"prerelease"`
		TargetCommitish string `json:"target_commitish"`
	}
	if err := json.Unmarshal([]byte(posted), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.TagName != "v1.3.0" || payload.Name != "v1.3.0" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Draft || payload.Prerelease || payload.TargetCommitish != "main" {
		t.Fatalf("payload flags = %+v", payload)
	}
	for _, want := range []string{
		"- feat: add an export button (abcdef12)",
		"- fix: a crash (12345678)",
		"https://github.com/acme/app/compare/v1.2.3...v1.3.0",
	} {
		if !strings.Contains(payload.Body, want) {
			t.Fatalf("body = %q, missing %q", payload.Body, want)
		}
	}
	if store.Meta("acme/app").LastReleaseCheckAt == "" {
		t.Fatal("a successful check must record its timestamp")
	}
}

func TestCheckReleaseBumpsPatchForFixes(t *testing.T) {
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/app/tags":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "v1.2.3"}})
		case "/repos/acme/app/compare/v1.2.3...main":
			_ = json.NewEncoder(w).Encode(map[string]any{"commits": []map[string]any{
				{"sha": "abcdef1234567890", "commit": map[string]string{"message": "fix: a crash"}},
				{"sha": "1234567890abcdef", "commit": map[string]string{"message": "docs: a typo"}},
			}})
		case "/repos/acme/app/releases":
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	store := newReleaseTestStore(t)
	app := releaseTestApp(gh, store, ReleaseConfig{Enabled: true, IntervalDays: 1})
	if err := checkRelease(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	if posted := releasePostBody(calls.snapshot()); !strings.Contains(posted, `"tag_name":"v1.2.4"`) {
		t.Fatalf("release body = %q, want v1.2.4", posted)
	}
}

func TestCheckReleaseFirstReleaseIsZeroOneZero(t *testing.T) {
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/acme/app/tags":
			_ = json.NewEncoder(w).Encode([]map[string]string{})
		case r.URL.Path == "/repos/acme/app/commits":
			if r.URL.Query().Get("sha") != "main" {
				t.Errorf("commits sha = %q, want main", r.URL.Query().Get("sha"))
			}
			// Fix commits only: the first release must still be v0.1.0, not
			// v0.0.1, because there is no published version to patch.
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"sha": "abcdef1234567890", "commit": map[string]string{"message": "fix: the first change"}},
			})
		case r.URL.Path == "/repos/acme/app/releases":
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	store := newReleaseTestStore(t)
	app := releaseTestApp(gh, store, ReleaseConfig{Enabled: true, IntervalDays: 1})
	if err := checkRelease(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	posted := releasePostBody(calls.snapshot())
	if !strings.Contains(posted, `"tag_name":"v0.1.0"`) {
		t.Fatalf("release body = %q, want v0.1.0", posted)
	}
	if strings.Contains(posted, "Full Changelog") {
		t.Fatalf("the first release has nothing to compare to: %q", posted)
	}
}

func TestCheckReleaseSkipsWithoutCommits(t *testing.T) {
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/app/tags":
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "v1.0.0"}})
		case "/repos/acme/app/compare/v1.0.0...main":
			_ = json.NewEncoder(w).Encode(map[string]any{"commits": []map[string]any{}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	store := newReleaseTestStore(t)
	app := releaseTestApp(gh, store, ReleaseConfig{Enabled: true, IntervalDays: 1})
	if err := checkRelease(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	for _, call := range calls.snapshot() {
		if call.method == http.MethodPost {
			t.Fatalf("nothing changed, but the daemon posted %+v", call)
		}
	}
	if store.Meta("acme/app").LastReleaseCheckAt == "" {
		t.Fatal("a check without commits still counts as done")
	}
}

func TestCheckReleaseSkipsWhenNotDue(t *testing.T) {
	gh, calls := newFakeAPI(t, nil)
	store := newReleaseTestStore(t)
	store.UpdateMeta("acme/app", func(meta *RepoMeta) {
		meta.LastReleaseCheckAt = time.Now().UTC().Format(time.RFC3339)
	})
	app := releaseTestApp(gh, store, ReleaseConfig{Enabled: true, IntervalDays: 3})
	if err := checkRelease(context.Background(), app); err != nil {
		t.Fatal(err)
	}
	if got := calls.snapshot(); len(got) != 0 {
		t.Fatalf("not due, but the daemon called GitHub: %+v", got)
	}
}

func TestCheckReleaseFailureKeepsTimestamp(t *testing.T) {
	gh, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		// 404 is not retried, so the test stays fast.
		w.WriteHeader(http.StatusNotFound)
	})
	store := newReleaseTestStore(t)
	app := releaseTestApp(gh, store, ReleaseConfig{Enabled: true, IntervalDays: 1})
	if err := checkRelease(context.Background(), app); err == nil {
		t.Fatal("expected the tag lookup to fail")
	}
	if store.Meta("acme/app").LastReleaseCheckAt != "" {
		t.Fatal("a failed check must not record a timestamp")
	}
}

func TestCheckReleaseDisabledDoesNothing(t *testing.T) {
	app := &AppContext{Repo: RepoConfig{FullName: "acme/app"}}
	if err := checkRelease(context.Background(), app); err != nil {
		t.Fatalf("a disabled repo must not touch anything: %v", err)
	}
}

func TestStartReleaseCheckerOnlyChecksEnabledRepos(t *testing.T) {
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{})
	})
	store := newReleaseTestStore(t)
	enabled := releaseTestApp(gh, store, ReleaseConfig{Enabled: true, IntervalDays: 1})
	disabled := releaseTestApp(gh, store, ReleaseConfig{IntervalDays: 1})
	disabled.Repo.FullName = "acme/other"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startReleaseChecker(ctx, map[string]*AppContext{"acme/app": enabled, "acme/other": disabled}, NewSilentLogger())
	stop()

	sawEnabled, sawDisabled := false, false
	for _, call := range calls.snapshot() {
		if strings.HasPrefix(call.path, "/repos/acme/app/") {
			sawEnabled = true
		}
		if strings.HasPrefix(call.path, "/repos/acme/other/") {
			sawDisabled = true
		}
	}
	if !sawEnabled {
		t.Fatal("the enabled repo was never checked")
	}
	if sawDisabled {
		t.Fatal("a disabled repo was checked")
	}
}

// releasePostBody returns the body of the release POST, if any.
func releasePostBody(calls []apiCall) string {
	for _, call := range calls {
		if call.method == http.MethodPost {
			return call.body
		}
	}
	return ""
}

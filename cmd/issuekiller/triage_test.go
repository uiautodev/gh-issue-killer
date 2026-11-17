package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRouteFirstStage(t *testing.T) {
	cases := []struct {
		mode        string
		buildDirect bool
		want        jobStage
	}{
		{"", false, stagePlan},
		{"", true, stageWork},
		{modeAuto, false, stagePlan},
		{modeAuto, true, stageWork},
		{modePlan, true, stagePlan},
		{modeBuild, false, stageWork},
		{"AUTO", true, stageWork},
		// A hand-written or older config without a valid mode keeps the
		// triage behaviour instead of failing to start the issue.
		{"bogus", false, stagePlan},
		{"bogus", true, stageWork},
	}
	for _, c := range cases {
		if got := routeFirstStage(c.mode, c.buildDirect); got != c.want {
			t.Errorf("routeFirstStage(%q, %v) = %v, want %v", c.mode, c.buildDirect, got, c.want)
		}
	}
}

func TestParseTriageVerdict(t *testing.T) {
	cases := []struct {
		text  string
		want  string
		known bool
	}{
		{"build", triageBuild, true},
		{"Plan\n\nmore words after the answer", triagePlan, true},
		{"`build`", triageBuild, true},
		{"```\nbuild\n```", triageBuild, true},
		{"build.", triageBuild, true},
		{"**PLAN**", triagePlan, true},
		{"\n\n  plan  ", triagePlan, true},
		{"I would build this", "", false},
		{"maybe", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, known := parseTriageVerdict(c.text)
		if got != c.want || known != c.known {
			t.Errorf("parseTriageVerdict(%q) = %q, %v; want %q, %v", c.text, got, known, c.want, c.known)
		}
	}
}

func TestExplicitPlanRequest(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"chinese plan first", "先出个计划再改", true},
		{"chinese plan spelling", "@bot 先 plan 再动代码", true},
		{"chinese proposal", "先给方案，确认后再写", true},
		{"english plan first", "Please plan first, then implement.", true},
		{"english before coding", "I want a plan before coding", true},
		{"english first plan", "first give me a plan", true},
		{"plain instruction", "把按钮改成圆角就行", false},
		{"existing plan feedback", "计划里的第二步写错了", false},
		{"agent output", agentCommentMarker + "\n先 plan 再动", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := explicitPlanRequest([]Comment{{Body: c.body}}); got != c.want {
				t.Fatalf("explicitPlanRequest(%q) = %v, want %v", c.body, got, c.want)
			}
		})
	}
}

func TestLoadConfigModes(t *testing.T) {
	cases := []struct {
		body     string
		wantErr  bool
		wantMode string
		repoMode string
	}{
		{`{"repos":["me/app"]}`, false, modeAuto, modeAuto},
		{`{"repos":["me/app"],"mode":"plan"}`, false, modePlan, modePlan},
		{`{"repos":["me/app"],"mode":"BUILD"}`, false, modeBuild, modeBuild},
		{`{"repos":{"me/app":{"mode":"plan"}}}`, false, modeAuto, modePlan},
		{`{"repos":["me/app"],"mode":"sometimes"}`, true, "", ""},
		{`{"repos":{"me/app":{"mode":"sometimes"}}}`, true, "", ""},
	}
	for _, c := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.json")
		if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := loadConfig(path)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%s: expected an unsupported mode error", c.body)
			}
			if !strings.Contains(err.Error(), "mode") {
				t.Fatalf("%s: error = %v", c.body, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", c.body, err)
		}
		if cfg.Mode != c.wantMode || cfg.Repos[0].Mode != c.repoMode {
			t.Fatalf("%s: config mode=%q repo mode=%q, want %q/%q", c.body, cfg.Mode, cfg.Repos[0].Mode, c.wantMode, c.repoMode)
		}
	}
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return &Store{
		file:  filepath.Join(t.TempDir(), "state.json"),
		state: State{Version: 1, Repos: map[string]*RepoState{}},
	}
}

// startBuild must not post a plan comment and must move straight to the
// implementing phase, even while the work job is still waiting for a worker.
func TestStartBuildSkipsThePlan(t *testing.T) {
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/issues/1":
			_ = json.NewEncoder(w).Encode(Issue{Number: 1, State: "closed"})
		case r.Method == http.MethodGet:
			_, _ = w.Write([]byte("[]"))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	})
	store := newTestStore(t)
	app := &AppContext{
		Cfg:        &Config{PlanTimeout: time.Minute},
		Repo:       RepoConfig{FullName: "o/r", Name: "r", Mode: modeBuild},
		GH:         gh,
		Store:      store,
		Log:        NewSilentLogger(),
		AgentLogin: "bot",
	}
	router := NewRouter(&Config{PlanParallel: 1, ImplementParallel: 1}, map[string]*AppContext{"o/r": app}, "bot", store, NewSilentLogger())

	router.startBuild(context.Background(), app, "o/r", 1, Trigger{Type: "issue", IssueNumber: 1, Key: "test"})
	if phase := store.Issue("o/r", 1).Phase; phase != "implementing" {
		t.Fatalf("phase = %q, want implementing", phase)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	router.waitInactive(ctx, "o/r#1#build", 2*time.Second)

	for _, call := range calls.snapshot() {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/comments") {
			t.Fatalf("startBuild posted a comment: %+v", call)
		}
	}
}

// Cancel must also reach a plan-skipping build job, otherwise cleanup removes
// the worktree while the agent is still editing it.
func TestCancelActiveStopsBuildJob(t *testing.T) {
	router := NewRouter(
		&Config{PlanParallel: 1, ImplementParallel: 1},
		map[string]*AppContext{"o/r": {}},
		"bot", nil, NewSilentLogger(),
	)
	started := make(chan struct{})
	stopped := make(chan struct{})
	router.enqueue("o/r", "o/r#1#build", stageWork, func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(stopped)
	})
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("build job did not start")
	}
	router.cancelActive("o/r", 1)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelActive did not cancel the build job")
	}
	router.waitInactive(context.Background(), "o/r#1#build", 2*time.Second)
}

// fakeRunner stands in for a coding-agent CLI: triage only needs its text.
type fakeRunner struct {
	text  string
	calls int
}

func (f *fakeRunner) ID() string { return "fake" }

func (f *fakeRunner) Run(context.Context, RunnerOptions) (RunnerResult, error) {
	f.calls++
	return RunnerResult{Text: f.text}, nil
}

// triageTestRepo creates a bare remote plus a clone so fetchBase and the
// detached triage worktree exercise the real git path.
func triageTestRepo(t *testing.T) (*LocalRepo, []string) {
	t.Helper()
	ctx := context.Background()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	clone := filepath.Join(root, "clone")
	gitIn := func(dir string, args ...string) {
		t.Helper()
		if _, err := gitWorktree(ctx, dir, env, 30*time.Second, false, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	gitIn(root, "init", "-q", "--bare", remote)
	gitIn(root, "init", "-q", "-b", "master", work)
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(work, "add", "a.txt")
	gitIn(work, "commit", "-q", "-m", "one")
	gitIn(work, "remote", "add", "origin", remote)
	gitIn(work, "push", "-q", "origin", "master")
	gitIn(root, "clone", "-q", remote, clone)
	return &LocalRepo{Dir: clone, HTTPSURL: remote, Base: "master", WorktreeRoot: filepath.Join(root, "trees"), Env: env}, env
}

func triageTestApp(t *testing.T, local *LocalRepo, env []string, comments []Comment) (*AppContext, *callLog) {
	t.Helper()
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/comments") {
			if comments == nil {
				comments = []Comment{}
			}
			_ = json.NewEncoder(w).Encode(comments)
			return
		}
		_, _ = w.Write([]byte("[]"))
	})
	store := newTestStore(t)
	return &AppContext{
		Cfg:        &Config{PlanTimeout: time.Minute},
		Repo:       RepoConfig{FullName: "o/r", Name: "app", CodingAgent: "opencode", Mode: modeAuto},
		Local:      local,
		GH:         gh,
		Store:      store,
		Log:        NewSilentLogger(),
		Env:        env,
		AgentLogin: "bot",
	}, calls
}

func TestShouldBuildDirectlyFollowsTheTriageAnswer(t *testing.T) {
	oldDir := stateDirOverride
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = oldDir })

	local, env := triageTestRepo(t)
	app, _ := triageTestApp(t, local, env, nil)
	runner := &fakeRunner{text: "build"}
	oldRunner := runnerFor
	runnerFor = func(string) (CodingRunner, error) { return runner, nil }
	t.Cleanup(func() { runnerFor = oldRunner })

	issue := Issue{Number: 1, Title: "add a flag", Body: "add --json to the list command"}
	trigger := Trigger{Type: "issue", IssueNumber: 1, Key: "triage"}
	if !shouldBuildDirectly(context.Background(), app, trigger, issue) {
		t.Fatal("triage build should skip the plan")
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls = %d, want 1", runner.calls)
	}

	runner.text = "plan"
	if shouldBuildDirectly(context.Background(), app, trigger, issue) {
		t.Fatal("triage plan must keep the plan stage")
	}
	runner.text = "I think both are fine"
	if shouldBuildDirectly(context.Background(), app, trigger, issue) {
		t.Fatal("an unreadable triage answer must fall back to planning")
	}
}

func TestShouldBuildDirectlyHonoursAnExplicitPlanRequest(t *testing.T) {
	oldDir := stateDirOverride
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = oldDir })

	local, env := triageTestRepo(t)
	app, _ := triageTestApp(t, local, env, []Comment{{Body: "先出个计划，确认后再实现"}})
	runner := &fakeRunner{text: "build"}
	oldRunner := runnerFor
	runnerFor = func(string) (CodingRunner, error) { return runner, nil }
	t.Cleanup(func() { runnerFor = oldRunner })

	if shouldBuildDirectly(context.Background(), app, Trigger{Key: "triage"}, Issue{Number: 1, Title: "t", Body: "b"}) {
		t.Fatal("an explicit plan request must not be overridden by triage")
	}
	if runner.calls != 0 {
		t.Fatalf("runner calls = %d, want 0 (triage must be skipped)", runner.calls)
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func conclusion(value string) *string { return &value }

func testCheckRun(name, status string, concl *string, created time.Time, started *time.Time) CheckRun {
	return CheckRun{Name: name, Status: status, Conclusion: concl, CreatedAt: created, StartedAt: started}
}

// newCheckApp serves check runs per ref and the required-status-checks
// endpoint, so waitForChecks can be exercised without GitHub.
func newCheckApp(t *testing.T, runsByRef map[string]CheckRuns, required []string) *AppContext {
	t.Helper()
	gh, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/app/branches/master/protection/required_status_checks" {
			if required == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"contexts": required})
			return
		}
		const prefix = "/repos/acme/app/commits/"
		suffix := "/check-runs"
		ref := ""
		if len(r.URL.Path) > len(prefix)+len(suffix) &&
			r.URL.Path[:len(prefix)] == prefix && r.URL.Path[len(r.URL.Path)-len(suffix):] == suffix {
			ref = r.URL.Path[len(prefix) : len(r.URL.Path)-len(suffix)]
		}
		runs, ok := runsByRef[ref]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(runs)
	})
	return &AppContext{
		Repo: RepoConfig{FullName: "acme/app"},
		GH:   gh,
		Log:  NewSilentLogger(),
	}
}

func TestWaitForChecksPassesWhenGreen(t *testing.T) {
	now := time.Now()
	app := newCheckApp(t, map[string]CheckRuns{
		"sha1": {TotalCount: 2, CheckRuns: []CheckRun{
			testCheckRun("tests", "completed", conclusion("success"), now, &now),
			testCheckRun("lint", "completed", conclusion("neutral"), now, &now),
		}},
	}, nil)

	verdict := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if !verdict.OK {
		t.Fatalf("OK = false, note = %q", verdict.Note)
	}
	if verdict.Note != "2 checks passed" {
		t.Fatalf("note = %q, want 2 checks passed", verdict.Note)
	}
	if len(verdict.Problems) != 0 {
		t.Fatalf("problems = %+v, want none", verdict.Problems)
	}
}

func TestWaitForChecksFailsFastWhileOtherChecksAreRunning(t *testing.T) {
	now := time.Now()
	app := newCheckApp(t, map[string]CheckRuns{
		"sha1": {TotalCount: 2, CheckRuns: []CheckRun{
			testCheckRun("Ubuntu / 3.8", "completed", conclusion("failure"), now, &now),
			testCheckRun("macOS /", "in_progress", nil, now, &now),
		}},
	}, nil)

	started := time.Now()
	verdict := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if verdict.OK {
		t.Fatal("OK = true, want failure")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("waited %s, want an immediate failure", elapsed)
	}
	if want := "Ubuntu / 3.8: failure"; verdict.Note != want {
		t.Fatalf("note = %q, want %q", verdict.Note, want)
	}
	if len(verdict.Problems) != 1 || verdict.Problems[0].Name != "Ubuntu / 3.8" {
		t.Fatalf("problems = %+v, want the one failed check", verdict.Problems)
	}
}

func TestWaitForChecksTreatsLongQueuedJobAsStuck(t *testing.T) {
	now := time.Now()
	queuedAt := now.Add(-checkStuckAfter - time.Minute)
	app := newCheckApp(t, map[string]CheckRuns{
		"sha1": {TotalCount: 2, CheckRuns: []CheckRun{
			testCheckRun("tests", "completed", conclusion("success"), now, &now),
			testCheckRun("macOS /", "queued", nil, queuedAt, nil),
		}},
	}, nil)

	started := time.Now()
	verdict := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if verdict.OK {
		t.Fatal("OK = true, want failure")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("waited %s, want an immediate stuck verdict", elapsed)
	}
	if want := "macOS /: queued with no runner (check the workflow's runner label)"; verdict.Note != want {
		t.Fatalf("note = %q, want %q", verdict.Note, want)
	}
}

func TestWaitForChecksOnlyGatesRequiredChecks(t *testing.T) {
	now := time.Now()
	app := newCheckApp(t, map[string]CheckRuns{
		"sha1": {TotalCount: 2, CheckRuns: []CheckRun{
			testCheckRun("tests", "completed", conclusion("success"), now, &now),
			testCheckRun("macOS /", "completed", conclusion("failure"), now, &now),
		}},
	}, []string{"tests"})

	verdict := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if !verdict.OK {
		t.Fatalf("OK = false, note = %q", verdict.Note)
	}
	if verdict.Note != "1 checks passed" {
		t.Fatalf("note = %q, want 1 checks passed", verdict.Note)
	}
}

func TestWaitForChecksMarksBaseBranchFailures(t *testing.T) {
	now := time.Now()
	app := newCheckApp(t, map[string]CheckRuns{
		"sha1": {TotalCount: 2, CheckRuns: []CheckRun{
			testCheckRun("Ubuntu / 3.8", "completed", conclusion("failure"), now, &now),
			testCheckRun("macOS /", "queued", nil, now.Add(-checkStuckAfter-time.Minute), nil),
		}},
		"master": {TotalCount: 2, CheckRuns: []CheckRun{
			testCheckRun("Ubuntu / 3.8", "completed", conclusion("failure"), now, &now),
			testCheckRun("macOS /", "queued", nil, now, nil),
		}},
	}, nil)

	verdict := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if verdict.OK {
		t.Fatal("OK = true, want failure")
	}
	want := "Ubuntu / 3.8: failure (also failing on master), " +
		"macOS /: queued with no runner (check the workflow's runner label) (also failing on master)"
	if verdict.Note != want {
		t.Fatalf("note = %q, want %q", verdict.Note, want)
	}
	for _, problem := range verdict.Problems {
		if !problem.Preexisting {
			t.Fatalf("%s marked as not pre-existing", problem.Name)
		}
	}
}

func TestWaitForChecksWithoutCheckRunsSkipsGate(t *testing.T) {
	app := newCheckApp(t, map[string]CheckRuns{"sha1": {TotalCount: 0}}, nil)

	verdict := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if !verdict.OK || verdict.Note != "no CI configured" {
		t.Fatalf("OK = %v, note = %q", verdict.OK, verdict.Note)
	}
}

func TestParseIDFromURL(t *testing.T) {
	url := "https://github.com/acme/app/actions/runs/43556544848/job/132330566763"
	if got := parseIDFromURL(url, "/runs/"); got != 43556544848 {
		t.Fatalf("run id = %d, want 43556544848", got)
	}
	if got := parseIDFromURL(url, "/job/"); got != 132330566763 {
		t.Fatalf("job id = %d, want 132330566763", got)
	}
	if got := parseIDFromURL("https://example.com/x", "/job/"); got != 0 {
		t.Fatalf("missing marker = %d, want 0", got)
	}
}

func TestApproveWaitingRuns(t *testing.T) {
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/repos/acme/app/issues/7/comments" {
			w.WriteHeader(http.StatusCreated)
		}
	})
	app := &AppContext{
		Repo: RepoConfig{FullName: "acme/app"},
		GH:   gh,
		Log:  NewSilentLogger(),
	}
	verdict := ciVerdict{Problems: []ciProblem{
		{Name: "Ubuntu / 3.11", Detail: "action_required", RunID: 111},
		{Name: "macOS / 3.11", Detail: "failure", RunID: 222},
	}}

	if !approveWaitingRuns(context.Background(), app, Issue{Number: 7}, verdict) {
		t.Fatal("approveWaitingRuns = false, want true")
	}
	approved := false
	for _, call := range *calls {
		if call.method == http.MethodPost && call.path == "/repos/acme/app/actions/runs/111/approve" {
			approved = true
		}
		if call.path == "/repos/acme/app/actions/runs/222/approve" {
			t.Fatal("approved a run that did not need approval")
		}
	}
	if !approved {
		t.Fatalf("calls = %+v, want a POST to approve run 111", *calls)
	}
}

func TestCIFailureReportIncludesLogsAndBaseFlag(t *testing.T) {
	gh, _ := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/app/actions/jobs/999/logs" {
			_, _ = w.Write([]byte("line one\nVersion 3.8 was not found for Ubuntu 26.04\n"))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	app := &AppContext{
		Repo:  RepoConfig{FullName: "acme/app"},
		GH:    gh,
		Log:   NewSilentLogger(),
		Local: &LocalRepo{Base: "master"},
	}
	verdict := ciVerdict{Problems: []ciProblem{
		{Name: "Ubuntu / 3.8", Detail: "failure", URL: "https://github.com/acme/app/actions/runs/1/job/999", JobID: 999, Preexisting: true},
		{Name: "macOS / 3.11", Detail: "queued with no runner (check the workflow's runner label)"},
	}}

	report := ciFailureReport(context.Background(), app, verdict)
	for _, want := range []string{
		"### Ubuntu / 3.8: failure (also failing on master)",
		"Version 3.8 was not found for Ubuntu 26.04",
		"### macOS / 3.11: queued with no runner",
		"https://github.com/acme/app/actions/runs/1/job/999",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report missing %q:\n%s", want, report)
		}
	}
}

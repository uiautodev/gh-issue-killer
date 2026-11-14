package main

import (
	"context"
	"encoding/json"
	"net/http"
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

	ok, note := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if !ok {
		t.Fatalf("ok = false, note = %q", note)
	}
	if note != "2 checks passed" {
		t.Fatalf("note = %q, want 2 checks passed", note)
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
	ok, note := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if ok {
		t.Fatal("ok = true, want failure")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("waited %s, want an immediate failure", elapsed)
	}
	if want := "Ubuntu / 3.8: failure"; note != want {
		t.Fatalf("note = %q, want %q", note, want)
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
	ok, note := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if ok {
		t.Fatal("ok = true, want failure")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("waited %s, want an immediate stuck verdict", elapsed)
	}
	if want := "macOS /: queued with no runner (check the workflow's runner label)"; note != want {
		t.Fatalf("note = %q, want %q", note, want)
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

	ok, note := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if !ok {
		t.Fatalf("ok = false, note = %q", note)
	}
	if note != "1 checks passed" {
		t.Fatalf("note = %q, want 1 checks passed", note)
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

	ok, note := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if ok {
		t.Fatal("ok = true, want failure")
	}
	want := "Ubuntu / 3.8: failure (also failing on master), " +
		"macOS /: queued with no runner (check the workflow's runner label) (also failing on master)"
	if note != want {
		t.Fatalf("note = %q, want %q", note, want)
	}
}

func TestWaitForChecksWithoutCheckRunsSkipsGate(t *testing.T) {
	app := newCheckApp(t, map[string]CheckRuns{"sha1": {TotalCount: 0}}, nil)

	ok, note := waitForChecks(context.Background(), app, "sha1", "master", time.Minute)
	if !ok || note != "no CI configured" {
		t.Fatalf("ok = %v, note = %q", ok, note)
	}
}

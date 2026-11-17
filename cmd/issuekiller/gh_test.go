package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

type apiCall struct {
	method string
	path   string
	body   string
}

// callLog records fake-API requests. A test can enqueue a background job that
// keeps calling the API after checkReactions returns, so appends must be
// synchronized or the race detector flags overlapping handler goroutines.
type callLog struct {
	mu    sync.Mutex
	calls []apiCall
}

func (c *callLog) add(call apiCall) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, call)
}

func (c *callLog) snapshot() []apiCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]apiCall(nil), c.calls...)
}

func newFakeAPI(t *testing.T, handler http.HandlerFunc) (*GH, *callLog) {
	t.Helper()
	calls := &callLog{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls.add(apiCall{method: r.Method, path: r.URL.Path, body: string(body)})
		if handler != nil {
			handler(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return &GH{APIBase: srv.URL, Client: srv.Client()}, calls
}

func issueLabelsAPI(t *testing.T, labels []string) (*GH, *callLog) {
	t.Helper()
	return newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/repos/acme/app/issues/7/labels" {
			payload := make([]map[string]string, 0, len(labels))
			for _, name := range labels {
				payload = append(payload, map[string]string{"name": name})
			}
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func TestSetAgentState(t *testing.T) {
	awaitingPath := "/repos/acme/app/issues/7/labels/" + url.PathEscape(awaitingLabel)
	workingPath := "/repos/acme/app/issues/7/labels/" + url.PathEscape(workingLabel)

	t.Run("already in the requested state writes nothing", func(t *testing.T) {
		gh, calls := issueLabelsAPI(t, []string{"bug", workingLabel})
		gh.SetAgentState(context.Background(), "acme/app", 7, workingLabel)
		got := calls.snapshot()
		if len(got) != 1 || got[0].method != http.MethodGet {
			t.Fatalf("calls = %+v, want a single GET", got)
		}
	})

	t.Run("switching states only touches the stale label", func(t *testing.T) {
		gh, calls := issueLabelsAPI(t, []string{awaitingLabel})
		gh.SetAgentState(context.Background(), "acme/app", 7, workingLabel)
		got := calls.snapshot()[1:]
		if len(got) != 2 {
			t.Fatalf("writes = %+v, want DELETE awaiting then POST working", got)
		}
		if got[0].method != http.MethodDelete || got[0].path != awaitingPath {
			t.Fatalf("first write = %+v, want DELETE %s", got[0], awaitingPath)
		}
		if got[1].method != http.MethodPost || got[1].path != "/repos/acme/app/issues/7/labels" {
			t.Fatalf("second write = %+v, want POST labels", got[1])
		}
		var payload struct {
			Labels []string `json:"labels"`
		}
		if err := json.Unmarshal([]byte(got[1].body), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Labels) != 1 || payload.Labels[0] != workingLabel {
			t.Fatalf("posted labels = %v, want only %s", payload.Labels, workingLabel)
		}
	})

	t.Run("clearing removes the current label", func(t *testing.T) {
		gh, calls := issueLabelsAPI(t, []string{workingLabel})
		gh.SetAgentState(context.Background(), "acme/app", 7, "")
		got := calls.snapshot()[1:]
		if len(got) != 1 || got[0].method != http.MethodDelete || got[0].path != workingPath {
			t.Fatalf("writes = %+v, want only DELETE %s", got, workingPath)
		}
	})

	t.Run("no lifecycle label adds the requested one", func(t *testing.T) {
		gh, calls := issueLabelsAPI(t, []string{"bug"})
		gh.SetAgentState(context.Background(), "acme/app", 7, awaitingLabel)
		got := calls.snapshot()[1:]
		if len(got) != 1 || got[0].method != http.MethodPost {
			t.Fatalf("writes = %+v, want only POST", got)
		}
	})

	t.Run("read failure falls back to the unconditional update", func(t *testing.T) {
		gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		gh.SetAgentState(context.Background(), "acme/app", 7, workingLabel)
		got := calls.snapshot()[1:]
		if len(got) != 3 {
			t.Fatalf("writes = %+v, want DELETE awaiting, DELETE working, POST", got)
		}
		if got[0].method != http.MethodDelete || got[0].path != awaitingPath {
			t.Fatalf("first write = %+v, want DELETE %s", got[0], awaitingPath)
		}
		if got[1].method != http.MethodDelete || got[1].path != workingPath {
			t.Fatalf("second write = %+v, want DELETE %s", got[1], workingPath)
		}
	})
}

func TestEnsureLabelsCreatesLifecycleLabels(t *testing.T) {
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	gh.EnsureLabels(context.Background(), "acme/app")

	got := calls.snapshot()
	if len(got) != 4 {
		t.Fatalf("calls = %+v, want GET+POST for two labels", got)
	}
	names := map[string]string{}
	for _, call := range got {
		if call.method != http.MethodPost {
			continue
		}
		var payload struct {
			Name  string `json:"name"`
			Color string `json:"color"`
		}
		if err := json.Unmarshal([]byte(call.body), &payload); err != nil {
			t.Fatal(err)
		}
		names[payload.Name] = payload.Color
	}
	if names[awaitingLabel] != "fbca04" || names[workingLabel] != "1d76db" {
		t.Fatalf("created labels = %+v", names)
	}
}

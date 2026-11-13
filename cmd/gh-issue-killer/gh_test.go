package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

type apiCall struct {
	method string
	path   string
	body   string
}

func newFakeAPI(t *testing.T, handler http.HandlerFunc) (*GH, *[]apiCall) {
	t.Helper()
	var calls []apiCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, apiCall{method: r.Method, path: r.URL.Path, body: string(body)})
		if handler != nil {
			handler(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return &GH{APIBase: srv.URL, Client: srv.Client()}, &calls
}

func TestSetAgentState(t *testing.T) {
	awaitingPath := "/repos/acme/app/issues/7/labels/" + url.PathEscape(awaitingLabel)
	workingPath := "/repos/acme/app/issues/7/labels/" + url.PathEscape(workingLabel)

	checkDeletes := func(t *testing.T, got []apiCall) {
		t.Helper()
		if len(got) < 2 || got[0].method != http.MethodDelete || got[0].path != awaitingPath {
			t.Fatalf("first call = %+v, want DELETE %s", got, awaitingPath)
		}
		if got[1].method != http.MethodDelete || got[1].path != workingPath {
			t.Fatalf("second call = %+v, want DELETE %s", got[1], workingPath)
		}
	}

	t.Run("no state removes both labels", func(t *testing.T) {
		gh, calls := newFakeAPI(t, nil)
		gh.SetAgentState(context.Background(), "acme/app", 7, "")
		got := *calls
		checkDeletes(t, got)
		if len(got) != 2 {
			t.Fatalf("calls = %+v, want only the two DELETEs", got)
		}
	})

	for _, label := range []string{awaitingLabel, workingLabel} {
		t.Run(label, func(t *testing.T) {
			gh, calls := newFakeAPI(t, nil)
			gh.SetAgentState(context.Background(), "acme/app", 7, label)
			got := *calls
			checkDeletes(t, got)
			if len(got) != 3 {
				t.Fatalf("calls = %+v, want two DELETEs then POST", got)
			}
			add := got[2]
			if add.method != http.MethodPost || add.path != "/repos/acme/app/issues/7/labels" {
				t.Fatalf("third call = %+v, want POST labels", add)
			}
			var payload struct {
				Labels []string `json:"labels"`
			}
			if err := json.Unmarshal([]byte(add.body), &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Labels) != 1 || payload.Labels[0] != label {
				t.Fatalf("posted labels = %v, want only %s", payload.Labels, label)
			}
		})
	}
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

	got := *calls
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

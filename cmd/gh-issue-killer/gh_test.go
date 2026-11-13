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

func TestSetWaitingLabel(t *testing.T) {
	labelPath := "/repos/acme/app/issues/7/labels/" + url.PathEscape(awaitingLabel)

	t.Run("not waiting removes the label and adds nothing", func(t *testing.T) {
		gh, calls := newFakeAPI(t, nil)
		gh.SetWaitingLabel(context.Background(), "acme/app", 7, false)

		got := *calls
		if len(got) != 1 || got[0].method != http.MethodDelete || got[0].path != labelPath {
			t.Fatalf("calls = %+v, want a single DELETE %s", got, labelPath)
		}
	})

	t.Run("waiting adds only the awaiting label", func(t *testing.T) {
		gh, calls := newFakeAPI(t, nil)
		gh.SetWaitingLabel(context.Background(), "acme/app", 7, true)

		got := *calls
		if len(got) != 2 {
			t.Fatalf("calls = %+v, want DELETE then POST", got)
		}
		del, add := got[0], got[1]
		if del.method != http.MethodDelete || del.path != labelPath {
			t.Fatalf("first call = %+v, want DELETE %s", del, labelPath)
		}
		if add.method != http.MethodPost || add.path != "/repos/acme/app/issues/7/labels" {
			t.Fatalf("second call = %+v, want POST labels", add)
		}
		var payload struct {
			Labels []string `json:"labels"`
		}
		if err := json.Unmarshal([]byte(add.body), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Labels) != 1 || payload.Labels[0] != awaitingLabel {
			t.Fatalf("posted labels = %v, want only %s", payload.Labels, awaitingLabel)
		}
	})
}

func TestEnsureLabelsCreatesOnlyAwaitingLabel(t *testing.T) {
	gh, calls := newFakeAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	gh.EnsureLabels(context.Background(), "acme/app")

	got := *calls
	if len(got) != 2 || got[0].method != http.MethodGet || got[1].method != http.MethodPost {
		t.Fatalf("calls = %+v, want GET then POST", got)
	}
	var payload struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(got[1].body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Name != awaitingLabel || payload.Color != "fbca04" || payload.Description != "waiting for user confirmation" {
		t.Fatalf("created label = %+v", payload)
	}
}

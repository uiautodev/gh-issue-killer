package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchConfigFiresOnValidChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"repos":["acme/app"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := watchConfig(ctx, path, NewSilentLogger(), 10*time.Millisecond, 20*time.Millisecond)

	if err := os.WriteFile(path, []byte(`{"repos":["acme/app","acme/api"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("valid config change did not trigger a reload")
	}
}

func TestWatchConfigIgnoresInvalidChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"repos":["acme/app"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := watchConfig(ctx, path, NewSilentLogger(), 10*time.Millisecond, 20*time.Millisecond)

	if err := os.WriteFile(path, []byte(`{"repos":`), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
		t.Fatal("invalid config must not trigger a reload")
	case <-time.After(200 * time.Millisecond):
	}

	if err := os.WriteFile(path, []byte(`{"repos":["acme/app","acme/api"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("a later valid change should still reload")
	}
}

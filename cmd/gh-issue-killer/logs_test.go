package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a bytes.Buffer that is safe to read while followFile writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	var builder strings.Builder
	for i := 1; i <= 100; i++ {
		builder.WriteString("line-")
		builder.WriteString(strings.Repeat("x", i%5))
		builder.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(builder.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := tailLines(path, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 5 {
		t.Fatalf("got %d lines", len(lines))
	}
	if lines[4] != "line-"+strings.Repeat("x", 0) {
		// The last line is generated with i=100, 100%5 == 0.
		t.Fatalf("last line = %q", lines[4])
	}
}

func waitForText(t *testing.T, buffer *syncBuffer, text string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(buffer.String(), text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in %q", text, buffer.String())
}

func TestFollowFileAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	done := make(chan struct{})
	go func() {
		_ = followFile(ctx, path, &out, 10, 10*time.Millisecond)
		close(done)
	}()
	waitForText(t, &out, "first", 2*time.Second)

	handle, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := handle.WriteString("second\n"); err != nil {
		handle.Close()
		t.Fatal(err)
	}
	handle.Close()
	waitForText(t, &out, "second", 2*time.Second)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("followFile did not stop")
	}
}

func TestFollowFileRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	done := make(chan struct{})
	go func() {
		_ = followFile(ctx, path, &out, 10, 10*time.Millisecond)
		close(done)
	}()
	waitForText(t, &out, "old", 2*time.Second)

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForText(t, &out, "new", 2*time.Second)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("followFile did not stop")
	}
}

func TestFollowFileWaitsForFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	done := make(chan struct{})
	go func() {
		_ = followFile(ctx, path, &out, 10, 10*time.Millisecond)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	if err := os.WriteFile(path, []byte("created\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitForText(t, &out, "created", 2*time.Second)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("followFile did not stop")
	}
}

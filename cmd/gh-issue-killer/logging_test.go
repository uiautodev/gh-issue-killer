package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRotatingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	writer, err := openRotatingFile(path, 50, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	if _, err := writer.Write([]byte(strings.Repeat("a", 40))); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("unexpected rotation after one write: %v", err)
	}

	for i := 0; i < 5; i++ {
		if _, err := writer.Write([]byte(strings.Repeat("b", 40))); err != nil {
			t.Fatal(err)
		}
	}
	for _, suffix := range []string{".1", ".2", ".3"} {
		if _, err := os.Stat(path + suffix); err != nil {
			t.Fatalf("missing rotated file %s: %v", suffix, err)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Fatalf("kept too many rotated files: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 40 {
		t.Fatalf("current log size = %d", len(data))
	}
}

func TestRotatingFileReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	writer, err := openRotatingFile(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("hello\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	writer2, err := openRotatingFile(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer writer2.Close()
	if _, err := writer2.Write([]byte("world\n")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello\nworld\n" {
		t.Fatalf("log = %q", data)
	}
}

func TestRotatingFileConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.log")
	writer, err := openRotatingFile(path, 1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			line := []byte(strings.Repeat("x", 64) + "\n")
			for j := 0; j < 50; j++ {
				if _, err := writer.Write(line); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	total := int64(0)
	for _, file := range []string{path, path + ".1", path + ".2", path + ".3"} {
		if info, err := os.Stat(file); err == nil {
			total += info.Size()
		}
	}
	if total == 0 {
		t.Fatal("no log bytes written")
	}
}

func TestShouldDaemonize(t *testing.T) {
	if !shouldDaemonize(false, false, false, false, false, false) {
		t.Fatal("default should daemonize")
	}
	cases := []struct {
		name string
		call func() bool
	}{
		{"foreground", func() bool { return shouldDaemonize(true, false, false, false, false, false) }},
		{"init", func() bool { return shouldDaemonize(false, true, false, false, false, false) }},
		{"once", func() bool { return shouldDaemonize(false, false, true, false, false, false) }},
		{"stop", func() bool { return shouldDaemonize(false, false, false, true, false, false) }},
		{"status", func() bool { return shouldDaemonize(false, false, false, false, true, false) }},
		{"child", func() bool { return shouldDaemonize(false, false, false, false, false, true) }},
	}
	for _, testCase := range cases {
		if testCase.call() {
			t.Fatalf("%s should not daemonize", testCase.name)
		}
	}
}

func TestColorizeLogLine(t *testing.T) {
	line := "2026-11-13T05:52:45Z INFO  hello"
	if got := colorizeLogLine(line, false); got != line {
		t.Fatalf("plain = %q", got)
	}
	colored := colorizeLogLine(line, true)
	for _, want := range []string{colorDim, colorGreen, "INFO", colorReset} {
		if !strings.Contains(colored, want) {
			t.Fatalf("missing %q in %q", want, colored)
		}
	}
	cases := map[string]string{
		"2026-11-13T05:52:45Z DEBUG x": colorGray,
		"2026-11-13T05:52:45Z WARN  x": colorYellow,
		"2026-11-13T05:52:45Z ERROR x": colorRed,
	}
	for input, wantColor := range cases {
		if got := colorizeLogLine(input, true); !strings.Contains(got, wantColor) {
			t.Fatalf("colorize %q = %q, want %q", input, got, wantColor)
		}
	}
	if got := colorizeLogLine("not a log line", true); got != "not a log line" {
		t.Fatalf("malformed = %q", got)
	}
}

func TestDefaultLoggerIsDebug(t *testing.T) {
	old := stateDirOverride
	stateDirOverride = t.TempDir()
	t.Cleanup(func() { stateDirOverride = old })

	logger := NewLogger("daemon")
	if logger.level != levelDebug {
		t.Fatalf("default level = %v, want debug", logger.level)
	}
	var out bytes.Buffer
	logger.out = &out
	logger.Debug("hello debug")
	if !strings.Contains(out.String(), "DEBUG hello debug") {
		t.Fatalf("output = %q", out.String())
	}
	if logger.file != nil {
		_ = logger.file.Close()
	}
}

func TestLogsColorDisabledForNonTTY(t *testing.T) {
	var out bytes.Buffer
	if logsColorEnabled(&out) {
		t.Fatal("buffer should not get color")
	}
}

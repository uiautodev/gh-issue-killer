package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}

func envDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func stateDir() string {
	return envDefault("GWIK_STATE_DIR", filepath.Join(homeDir(), ".gh-issue-killer"))
}

func logsDir() string   { return filepath.Join(stateDir(), "logs") }
func runsDir() string   { return filepath.Join(stateDir(), "runs") }
func reposDir() string  { return filepath.Join(stateDir(), "repos") }
func treesDir() string  { return filepath.Join(stateDir(), "worktrees") }
func tmpDir() string    { return filepath.Join(stateDir(), "tmp") }
func lockFile() string  { return filepath.Join(stateDir(), "daemon.lock") }
func stateFile() string { return filepath.Join(stateDir(), "state.json") }

func ensureDirs() error {
	for _, dir := range []string{stateDir(), logsDir(), runsDir(), reposDir(), treesDir(), tmpDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func expandHome(value string) string {
	if value == "~" {
		return homeDir()
	}
	if strings.HasPrefix(value, "~/") {
		return filepath.Join(homeDir(), value[2:])
	}
	return value
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func isoPtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func atomicWriteFile(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp-%d", file, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func atomicWriteJSON(file string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return atomicWriteFile(file, data)
}

func readJSON(file string, value any) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}

func readJSONIfExists(file string, value any) bool {
	if err := readJSON(file, value); err != nil {
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// logger
// ---------------------------------------------------------------------------

type Level int

const (
	levelDebug Level = iota
	levelInfo
	levelWarn
	levelError
)

type Logger struct {
	mu    sync.Mutex
	level Level
	out   io.Writer
	file  *os.File
}

func NewLogger(name string) *Logger {
	level := levelInfo
	switch strings.ToLower(os.Getenv("GWIK_LOG_LEVEL")) {
	case "debug":
		level = levelDebug
	case "warn":
		level = levelWarn
	case "error":
		level = levelError
	}
	logger := &Logger{level: level, out: os.Stdout}
	if err := os.MkdirAll(logsDir(), 0o755); err == nil {
		if file, err := os.OpenFile(filepath.Join(logsDir(), name+".log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			logger.file = file
		}
	}
	return logger
}

// NewSilentLogger is used by short-lived setup checks that should not create
// or pollute the daemon log.
func NewSilentLogger() *Logger {
	return &Logger{level: levelError, out: io.Discard}
}

func (l *Logger) logf(level Level, label, format string, args ...any) {
	if level < l.level {
		return
	}
	line := fmt.Sprintf("%s %-5s %s", nowISO(), label, fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintln(l.out, line)
	if l.file != nil {
		fmt.Fprintln(l.file, line)
	}
}

func (l *Logger) Debug(format string, args ...any) { l.logf(levelDebug, "DEBUG", format, args...) }
func (l *Logger) Info(format string, args ...any)  { l.logf(levelInfo, "INFO", format, args...) }
func (l *Logger) Warn(format string, args ...any)  { l.logf(levelWarn, "WARN", format, args...) }
func (l *Logger) Error(format string, args ...any) { l.logf(levelError, "ERROR", format, args...) }

// ---------------------------------------------------------------------------
// command runner
// ---------------------------------------------------------------------------

type CmdOptions struct {
	Cwd          string
	Env          []string
	Timeout      time.Duration
	AllowFailure bool
	Stdin        io.Reader
	MaxOutput    int
}

type CmdResult struct {
	Stdout   string
	Stderr   string
	Code     int
	TimedOut bool
}

var ErrCommandFailed = errors.New("command failed")

func runCmd(ctx context.Context, name string, args []string, opts CmdOptions) (CmdResult, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = opts.Cwd
	if opts.Env != nil {
		cmd.Env = opts.Env
	}
	cmd.Stdin = opts.Stdin
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	maxOutput := opts.MaxOutput
	if maxOutput <= 0 {
		maxOutput = 2 * 1024 * 1024
	}
	var stdout, stderr limitedBuffer
	stdout.limit = maxOutput
	stderr.limit = maxOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := CmdResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.Code = 124
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		if !opts.AllowFailure {
			return result, fmt.Errorf("%s %s timed out after %s", name, strings.Join(args, " "), opts.Timeout)
		}
		return result, nil
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.Code = exitErr.ExitCode()
			if opts.AllowFailure {
				return result, nil
			}
			return result, fmt.Errorf("%s %s failed (exit %d): %s", name, strings.Join(args, " "), result.Code, tailString(result.Stderr, 30))
		}
		return result, err
	}
	return result, nil
}

type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len() < b.limit {
		remaining := b.limit - b.buf.Len()
		if len(p) > remaining {
			b.buf.Write(p[:remaining])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return b.buf.String() }

func runShell(ctx context.Context, command string, opts CmdOptions) (CmdResult, error) {
	return runCmd(ctx, "/bin/sh", []string{"-c", command}, opts)
}

func tailString(text string, lines int) string {
	if text == "" {
		return ""
	}
	parts := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(parts) <= lines {
		return strings.Join(parts, "\n")
	}
	return strings.Join(parts[len(parts)-lines:], "\n")
}

func shortID(value string) string {
	var hash uint32 = 2166136261
	for i := 0; i < len(value); i++ {
		hash ^= uint32(value[i])
		hash *= 16777619
	}
	return strconv.FormatUint(uint64(hash), 36)
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

func randomHex(bytesLen int) string {
	buf := make([]byte, bytesLen)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(buf)
}

func mergeEnv(base []string, overrides map[string]string, remove ...string) []string {
	values := make(map[string]string, len(base)+len(overrides))
	order := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = value
	}
	for _, key := range remove {
		delete(values, key)
	}
	for key, value := range overrides {
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for _, key := range order {
		if value, ok := values[key]; ok {
			result = append(result, key+"="+value)
		}
	}
	return result
}

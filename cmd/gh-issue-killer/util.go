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

// stateDirOverride lets tests isolate state without introducing a public
// environment variable. The daemon always uses ~/.gh-issue-killer.
var stateDirOverride string

func stateDir() string {
	if stateDirOverride != "" {
		return stateDirOverride
	}
	return filepath.Join(homeDir(), ".gh-issue-killer")
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
	file  io.WriteCloser
}

const (
	logRotateSizeMB = 10
	logRotateKeep   = 5
)

func daemonLogPath() string {
	return filepath.Join(logsDir(), "daemon.log")
}

func NewLogger(name string) *Logger {
	logger := &Logger{level: levelInfo, out: os.Stdout}
	if err := os.MkdirAll(logsDir(), 0o755); err == nil {
		if writer, err := openRotatingFile(filepath.Join(logsDir(), name+".log"), logRotateSizeMB*1024*1024, logRotateKeep); err == nil {
			logger.file = writer
		}
	}
	return logger
}

// NewSilentLogger is used by short-lived setup checks that should not create
// or pollute the daemon log.
func NewSilentLogger() *Logger {
	return &Logger{level: levelError, out: io.Discard}
}

// rotatingFile is a small size-based rotating writer. It keeps at most
// maxFiles rotated files (path.1 ... path.N) and is safe for concurrent use.
type rotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	file     *os.File
	size     int64
}

func openRotatingFile(path string, maxBytes int64, maxFiles int) (*rotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}
	return &rotatingFile{
		path:     path,
		maxBytes: maxBytes,
		maxFiles: maxFiles,
		file:     file,
		size:     info.Size(),
	}, nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.maxBytes > 0 && r.size > 0 && r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) rotateLocked() error {
	if err := r.file.Close(); err != nil {
		return err
	}
	if r.maxFiles > 0 {
		oldest := fmt.Sprintf("%s.%d", r.path, r.maxFiles)
		_ = os.Remove(oldest)
		for index := r.maxFiles - 1; index >= 1; index-- {
			from := fmt.Sprintf("%s.%d", r.path, index)
			to := fmt.Sprintf("%s.%d", r.path, index+1)
			_ = os.Rename(from, to)
		}
		_ = os.Rename(r.path, r.path+".1")
	}
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	r.file = file
	r.size = 0
	return nil
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
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

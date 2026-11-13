package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// CodingAgentInfo describes a supported coding-agent CLI.
type CodingAgentInfo struct {
	ID          string
	Name        string
	Binary      string
	InstallHint string
}

var codingAgentRegistry = []CodingAgentInfo{
	{ID: "opencode", Name: "opencode", Binary: "opencode", InstallHint: "https://opencode.ai"},
	{ID: "claude", Name: "Claude Code", Binary: "claude", InstallHint: "https://docs.anthropic.com/en/docs/claude-code"},
	{ID: "codex", Name: "Codex CLI", Binary: "codex", InstallHint: "https://github.com/openai/codex"},
}

func codingAgentInfoByID(id string) (CodingAgentInfo, bool) {
	for _, info := range codingAgentRegistry {
		if strings.EqualFold(info.ID, id) {
			return info, true
		}
	}
	return CodingAgentInfo{}, false
}

type DetectedCodingAgent struct {
	Info    CodingAgentInfo
	Path    string
	Version string
}

// detectCodingAgents reports the supported CLIs that are currently on PATH.
func detectCodingAgents() []DetectedCodingAgent {
	detected := make([]DetectedCodingAgent, 0, len(codingAgentRegistry))
	for _, info := range codingAgentRegistry {
		path, err := exec.LookPath(info.Binary)
		if err != nil {
			continue
		}
		version := ""
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		result, runErr := runCmd(ctx, path, []string{"--version"}, CmdOptions{
			Timeout:      4 * time.Second,
			AllowFailure: true,
		})
		cancel()
		if runErr == nil && result.Code == 0 {
			version = strings.SplitN(strings.TrimSpace(result.Stdout), "\n", 2)[0]
		}
		detected = append(detected, DetectedCodingAgent{Info: info, Path: path, Version: strings.TrimSpace(version)})
	}
	return detected
}

type RunnerOptions struct {
	Cwd       string
	Prompt    string
	SessionID string
	Model     string
	RunDir    string
	Env       []string
	Timeout   time.Duration
	LogFile   string
	Log       *Logger
}

type RunnerResult struct {
	SessionID string
	Text      string
	Code      int
	TimedOut  bool
	Stderr    string
	ErrText   string
}

type CodingRunner interface {
	ID() string
	Run(ctx context.Context, opts RunnerOptions) (RunnerResult, error)
}

func newCodingRunner(id string) (CodingRunner, error) {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case "", "opencode":
		return &opencodeRunner{}, nil
	case "claude":
		return &claudeRunner{}, nil
	case "codex":
		return &codexRunner{}, nil
	default:
		return nil, fmt.Errorf("unsupported codingAgent %q (supported: %s)", id, supportedCodingAgentList())
	}
}

type processResult struct {
	Stdout   string
	Stderr   string
	Code     int
	TimedOut bool
}

// runProcess runs one coding-agent CLI. stdout is scanned line by line so a
// runner can parse its event stream, and both streams are appended to LogFile.
func runProcess(ctx context.Context, binary string, args []string, opts RunnerOptions, onStdout func(string)) (processResult, error) {
	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = opts.Env
	cmd.Stdin = nil // /dev/null: non-interactive CLIs must not wait for input
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return processResult{}, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return processResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return processResult{}, err
	}

	var logFile *os.File
	if opts.LogFile != "" {
		_ = os.MkdirAll(filepath.Dir(opts.LogFile), 0o755)
		logFile, _ = os.OpenFile(opts.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if logFile != nil {
			defer logFile.Close()
		}
	}
	writeLog := func(line string) {
		if logFile != nil {
			_, _ = io.WriteString(logFile, line+"\n")
		}
	}

	var stdoutBuf, stderrBuf limitedBuffer
	stdoutBuf.limit = 8 * 1024 * 1024
	stderrBuf.limit = 2 * 1024 * 1024

	done := make(chan error, 2)
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			stdoutBuf.Write([]byte(line + "\n"))
			writeLog(line)
			if onStdout != nil {
				onStdout(line)
			}
		}
		done <- scanner.Err()
	}()
	go func() {
		scanner := bufio.NewScanner(stderrPipe)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			stderrBuf.Write([]byte(line + "\n"))
			writeLog("[stderr] " + line)
		}
		done <- scanner.Err()
	}()

	waitErr := cmd.Wait()
	<-done
	<-done

	result := processResult{Stdout: stdoutBuf.String(), Stderr: stderrBuf.String()}
	if cmd.ProcessState != nil {
		result.Code = cmd.ProcessState.ExitCode()
	}
	if runCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return result, fmt.Errorf("coding agent timed out after %s", opts.Timeout)
	}
	if waitErr != nil {
		return result, fmt.Errorf("coding agent exited %d: %s", result.Code, tailString(result.Stderr, 20))
	}
	return result, nil
}

func runnerBinary(info CodingAgentInfo) (string, error) {
	path, err := exec.LookPath(info.Binary)
	if err != nil {
		return "", fmt.Errorf("%s is not installed or not on PATH; install it from %s", info.Binary, info.InstallHint)
	}
	return path, nil
}

func newUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("gwik-%d", time.Now().UnixNano())
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}

// ---------------------------------------------------------------------------
// opencode
// ---------------------------------------------------------------------------

type opencodeRunner struct{}

func (r *opencodeRunner) ID() string { return "opencode" }

func opencodeArgs(opts RunnerOptions) []string {
	args := []string{"run"}
	if opts.SessionID != "" {
		args = append(args, "--session", opts.SessionID)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	args = append(args, "--format", "json", "--auto", opts.Prompt)
	return args
}

func (r *opencodeRunner) Run(ctx context.Context, opts RunnerOptions) (RunnerResult, error) {
	info, _ := codingAgentInfoByID("opencode")
	binary, err := runnerBinary(info)
	if err != nil {
		return RunnerResult{}, err
	}

	result := RunnerResult{SessionID: opts.SessionID}
	var texts []string
	var eventErr string

	proc, runErr := runProcess(ctx, binary, opencodeArgs(opts), opts, func(line string) {
		var event opencodeEvent
		if json.Unmarshal([]byte(line), &event) != nil {
			return
		}
		if event.SessionID != "" {
			result.SessionID = event.SessionID
		}
		if event.Type == "text" && event.Part.Text != "" {
			texts = append(texts, event.Part.Text)
		}
		if event.Type == "error" && event.Error.Message != "" {
			eventErr = event.Error.Message
		}
	})
	// Join with a blank line: text parts are separated messages, not one stream,
	// so "I'll explore..." followed by "## 问题分析" must not become one line.
	result.Text = strings.TrimSpace(strings.Join(texts, "\n\n"))
	result.Stderr = tailString(proc.Stderr, 50)
	result.Code = proc.Code
	result.TimedOut = proc.TimedOut
	result.ErrText = eventErr
	if runErr != nil {
		return result, runErr
	}
	if result.Text == "" && eventErr != "" {
		return result, fmt.Errorf("opencode failed: %s", eventErr)
	}
	return result, nil
}

type opencodeEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		Text string `json:"text"`
	} `json:"part"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ---------------------------------------------------------------------------
// Claude Code
// ---------------------------------------------------------------------------

type claudeRunner struct{}

func (r *claudeRunner) ID() string { return "claude" }

func claudeArgs(opts RunnerOptions, sessionID string) []string {
	args := []string{
		"-p", opts.Prompt,
		"--output-format", "json",
		"--permission-mode", "bypassPermissions",
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.RunDir != "" {
		args = append(args, "--add-dir", opts.RunDir)
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
	} else if sessionID != "" {
		args = append(args, "--session-id", sessionID)
	}
	return args
}

type claudeResult struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
}

func (r *claudeRunner) Run(ctx context.Context, opts RunnerOptions) (RunnerResult, error) {
	info, _ := codingAgentInfoByID("claude")
	binary, err := runnerBinary(info)
	if err != nil {
		return RunnerResult{}, err
	}
	sessionID := ""
	if opts.SessionID == "" {
		sessionID = newUUID()
	}
	proc, runErr := runProcess(ctx, binary, claudeArgs(opts, sessionID), opts, nil)

	result := RunnerResult{
		SessionID: firstNonEmpty(opts.SessionID, sessionID),
		Code:      proc.Code,
		TimedOut:  proc.TimedOut,
		Stderr:    tailString(proc.Stderr, 50),
	}
	var parsed claudeResult
	if json.Unmarshal([]byte(proc.Stdout), &parsed) == nil {
		if parsed.SessionID != "" {
			result.SessionID = parsed.SessionID
		}
		result.Text = strings.TrimSpace(parsed.Result)
		if parsed.IsError {
			result.ErrText = parsed.Result
		}
	} else {
		result.Text = strings.TrimSpace(proc.Stdout)
	}
	if runErr != nil {
		return result, runErr
	}
	if result.Text == "" && result.ErrText != "" {
		return result, fmt.Errorf("claude failed: %s", result.ErrText)
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Codex
// ---------------------------------------------------------------------------

type codexRunner struct{}

func (r *codexRunner) ID() string { return "codex" }

func codexArgs(opts RunnerOptions, lastMessageFile string) []string {
	args := []string{
		"exec", opts.Prompt,
		"--cd", opts.Cwd,
		"--full-auto",
		"--color", "never",
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.RunDir != "" {
		args = append(args, "--add-dir", opts.RunDir)
	}
	if lastMessageFile != "" {
		args = append(args, "--output-last-message", lastMessageFile)
	}
	return args
}

func (r *codexRunner) Run(ctx context.Context, opts RunnerOptions) (RunnerResult, error) {
	info, _ := codingAgentInfoByID("codex")
	binary, err := runnerBinary(info)
	if err != nil {
		return RunnerResult{}, err
	}
	lastFile := ""
	if opts.RunDir != "" {
		lastFile = filepath.Join(opts.RunDir, fmt.Sprintf("codex-last-%d.txt", time.Now().UnixNano()))
	}
	proc, runErr := runProcess(ctx, binary, codexArgs(opts, lastFile), opts, nil)

	result := RunnerResult{
		Code:     proc.Code,
		TimedOut: proc.TimedOut,
		Stderr:   tailString(proc.Stderr, 50),
	}
	if lastFile != "" {
		if data, readErr := os.ReadFile(lastFile); readErr == nil {
			result.Text = strings.TrimSpace(string(data))
		}
	}
	if result.Text == "" {
		result.Text = strings.TrimSpace(proc.Stdout)
	}
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

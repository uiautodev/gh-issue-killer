package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type OpencodeOptions struct {
	Cwd       string
	Prompt    string
	SessionID string
	Agent     string
	Model     string
	Env       []string
	Timeout   time.Duration
	LogFile   string
	Log       *Logger
}

type OpencodeResult struct {
	SessionID string
	Text      string
	Code      int
	TimedOut  bool
	Stderr    string
	ErrText   string
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

func opencodeBinary() (string, error) {
	if value := strings.TrimSpace(os.Getenv("OPENCODE_BIN")); value != "" {
		return value, nil
	}
	path, err := exec.LookPath("opencode")
	if err != nil {
		return "", fmt.Errorf("opencode not found in PATH: %w", err)
	}
	return path, nil
}

func runOpencode(ctx context.Context, opts OpencodeOptions) (OpencodeResult, error) {
	binary, err := opencodeBinary()
	if err != nil {
		return OpencodeResult{}, err
	}
	args := []string{"run"}
	if opts.SessionID != "" {
		args = append(args, "--session", opts.SessionID)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Agent != "" && opts.Agent != "opencode" {
		args = append(args, "--agent", opts.Agent)
	}
	args = append(args, "--format", "json", "--auto", opts.Prompt)

	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, binary, args...)
	cmd.Dir = opts.Cwd
	cmd.Env = opts.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return OpencodeResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return OpencodeResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return OpencodeResult{}, err
	}

	var logFile *os.File
	if opts.LogFile != "" {
		_ = os.MkdirAll(filepath.Dir(opts.LogFile), 0o755)
		logFile, _ = os.OpenFile(opts.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if logFile != nil {
			defer logFile.Close()
		}
	}

	result := OpencodeResult{SessionID: opts.SessionID}
	var texts []string

	done := make(chan error, 2)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			if logFile != nil {
				fmt.Fprintln(logFile, line)
			}
			var event opencodeEvent
			if json.Unmarshal([]byte(line), &event) != nil {
				continue
			}
			if event.SessionID != "" {
				result.SessionID = event.SessionID
			}
			if event.Type == "text" && event.Part.Text != "" {
				texts = append(texts, event.Part.Text)
			}
			if event.Type == "error" && event.Error.Message != "" {
				result.ErrText = event.Error.Message
			}
		}
		done <- scanner.Err()
	}()
	go func() {
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if logFile != nil && strings.TrimSpace(line) != "" {
				fmt.Fprintln(logFile, line)
			}
			result.Stderr += line + "\n"
		}
		done <- scanner.Err()
	}()

	waitErr := cmd.Wait()
	<-done
	<-done

	result.Text = strings.TrimSpace(strings.Join(texts, ""))
	result.Stderr = tailString(result.Stderr, 50)
	if cmd.ProcessState != nil {
		result.Code = cmd.ProcessState.ExitCode()
	}
	if runCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		return result, fmt.Errorf("opencode timed out after %s", opts.Timeout)
	}
	if waitErr != nil {
		detail := result.Stderr
		if detail == "" {
			detail = result.ErrText
		}
		return result, fmt.Errorf("opencode exited %d: %s", result.Code, tailString(detail, 20))
	}
	if result.Text == "" && result.ErrText != "" {
		return result, fmt.Errorf("opencode failed: %s", result.ErrText)
	}
	return result, nil
}

package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// exitRestartEngine matches exitRestart in cmd/gh-issue-killer: the engine exits
// with it after a config save or an installed update and expects to be started
// again with the same arguments.
const exitRestartEngine = 75

const (
	// engineStartTimeout is generous: the first start resolves the repository
	// clone and the GitHub identity before the dashboard answers.
	engineStartTimeout = 45 * time.Second
	enginePollEvery    = 150 * time.Millisecond
	engineStopTimeout  = 10 * time.Second
	maxEngineRestarts  = 5
)

// supervisor owns exactly one engine process. It restarts the engine on
// exitRestartEngine, retries a crashed engine a few times, and stops the child
// when the window closes.
type supervisor struct {
	enginePath string
	configPath string
	port       int
	logPath    string
	logFile    *os.File

	mu       sync.Mutex
	cmd      *exec.Cmd
	stopped  bool
	failures int
}

func newSupervisor() (*supervisor, error) {
	enginePath, err := engineBinary()
	if err != nil {
		return nil, err
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	configPath := os.Getenv("GHIK_CONFIG")
	if configPath == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, homeErr
		}
		configPath = filepath.Join(home, ".config", "gh-issue-killer", "config.json")
	}
	if err := os.MkdirAll(filepath.Join(homeStateDir(), "logs"), 0o755); err != nil {
		return nil, err
	}
	logPath := filepath.Join(homeStateDir(), "logs", "desktop-engine.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		logFile.Close()
		return nil, err
	}
	return &supervisor{
		enginePath: enginePath,
		configPath: configPath,
		port:       port,
		logPath:    logPath,
		logFile:    logFile,
	}, nil
}

func homeStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".gh-issue-killer"
	}
	return filepath.Join(home, ".gh-issue-killer")
}

// engineBinary looks for the engine next to the shell, then on PATH.
// GHIK_ENGINE overrides both for development.
func engineBinary() (string, error) {
	name := "gh-issue-killer"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if override := os.Getenv("GHIK_ENGINE"); override != "" {
		if info, err := os.Stat(override); err == nil && !info.IsDir() {
			return override, nil
		}
		return "", fmt.Errorf("GHIK_ENGINE=%s is not a file", override)
	}
	if self, err := os.Executable(); err == nil {
		for _, candidate := range []string{
			filepath.Join(filepath.Dir(self), name),
			// macOS bundles may put the engine in Resources instead.
			filepath.Join(filepath.Dir(self), "..", "Resources", name),
		} {
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	if found, err := exec.LookPath(name); err == nil {
		return found, nil
	}
	return "", fmt.Errorf("%s not found next to the desktop app; install it beside the app or set GHIK_ENGINE", name)
}

// freePort binds port 0 and returns the port the OS picked. The engine starts a
// moment later, so the port can in theory be taken in between; the engine then
// fails to listen and the start retry picks a new port.
func freePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func dashboardURL(port int) string {
	return "http://127.0.0.1:" + strconv.Itoa(port) + "/"
}

// Start launches the engine and waits for /api/state. The returned URL is what
// the window loads.
func (s *supervisor) Start() (string, error) {
	s.mu.Lock()
	if s.cmd != nil {
		url := dashboardURL(s.port)
		s.mu.Unlock()
		return url, nil
	}
	url, err := s.startLocked()
	s.mu.Unlock()
	return url, err
}

// startLocked must be called with s.mu held.
func (s *supervisor) startLocked() (string, error) {
	args := []string{
		"-embedded",
		"-web-port", strconv.Itoa(s.port),
		"-config", s.configPath,
	}
	cmd := exec.Command(s.enginePath, args...)
	cmd.Env = append(os.Environ(), "GHIK_DESKTOP=1")
	cmd.Dir = filepath.Dir(s.configPath)
	cmd.Stdin = nil
	cmd.Stdout = s.logFile
	cmd.Stderr = s.logFile
	cmd.SysProcAttr = engineProcAttr()
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", s.enginePath, err)
	}
	s.cmd = cmd
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(engineStartTimeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			s.cmd = nil
			return "", fmt.Errorf("engine exited during startup: %v\n%s", err, s.logTail())
		case <-time.After(enginePollEvery):
		}
		if engineReady(s.port) {
			go s.watch(cmd, exited)
			return dashboardURL(s.port), nil
		}
	}
	_ = terminateEngine(cmd.Process.Pid)
	return "", fmt.Errorf("engine did not answer on %s\n%s", dashboardURL(s.port), s.logTail())
}

// watch restarts the engine after it exits. A restart request (exit 75) is
// expected; a crash is retried with a growing delay and finally reported in the
// log instead of looping forever.
func (s *supervisor) watch(cmd *exec.Cmd, exited <-chan error) {
	waitErr := <-exited
	code := exitCode(waitErr)

	s.mu.Lock()
	if s.stopped || s.cmd != cmd {
		s.mu.Unlock()
		return
	}
	s.cmd = nil
	restart := code == exitRestartEngine
	if restart {
		s.failures = 0
	} else {
		s.failures++
	}
	failures := s.failures
	s.mu.Unlock()

	if !restart && failures > maxEngineRestarts {
		fmt.Fprintf(s.logFile, "engine stopped (%v); giving up after %d restarts\n", waitErr, maxEngineRestarts)
		return
	}
	delay := 400 * time.Millisecond
	if !restart {
		delay = time.Duration(failures) * time.Second
	}
	time.Sleep(delay)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	if _, err := s.startLocked(); err != nil {
		fmt.Fprintf(s.logFile, "engine restart failed: %v\n", err)
	}
}

// Stop ends the engine the shell started. Work in progress is interrupted: the
// state file is written on shutdown and the next start resumes the worktree.
func (s *supervisor) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cmd := s.cmd
	s.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		pid := cmd.Process.Pid
		_ = terminateEngine(pid)
		deadline := time.Now().Add(engineStopTimeout)
		for time.Now().Before(deadline) {
			if !processAliveLocal(pid) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if processAliveLocal(pid) {
			killEngineTree(pid)
		}
	}
	if s.logFile != nil {
		s.logFile.Close()
	}
}

func engineReady(port int) bool {
	client := &http.Client{Timeout: 800 * time.Millisecond}
	response, err := client.Get(dashboardURL(port) + "api/state")
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = response.Body.Read(make([]byte, 512))
	return response.StatusCode == http.StatusOK
}

// logTail shows the engine's last lines in the startup error dialog.
func (s *supervisor) logTail() string {
	data, err := os.ReadFile(s.logPath)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

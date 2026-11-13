package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type CheckSpec struct {
	Name       string `json:"name"`
	Cmd        string `json:"cmd"`
	Cwd        string `json:"cwd"`
	TimeoutSec int    `json:"timeoutSec"`
}

type ChecksFile struct {
	Checks []CheckSpec `json:"checks"`
}

var safeFileName = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func detectDefaultChecks(worktree string) []CheckSpec {
	checks := make([]CheckSpec, 0, 4)
	if fileExists(filepath.Join(worktree, "go.mod")) {
		checks = append(checks,
			CheckSpec{Name: "go test", Cmd: "go test ./...", Cwd: ".", TimeoutSec: 900},
			CheckSpec{Name: "go vet", Cmd: "go vet ./...", Cwd: ".", TimeoutSec: 300},
		)
	}
	if fileExists(filepath.Join(worktree, "frontend", "package.json")) {
		checks = append(checks, CheckSpec{
			Name: "frontend install", Cmd: "npm install --no-audit --no-fund", Cwd: "frontend", TimeoutSec: 900,
		})
		checks = append(checks,
			CheckSpec{Name: "frontend check", Cmd: "npm run check", Cwd: "frontend", TimeoutSec: 600},
			CheckSpec{Name: "frontend build", Cmd: "npm run build", Cwd: "frontend", TimeoutSec: 900},
		)
	} else if path := filepath.Join(worktree, "package.json"); fileExists(path) {
		var pkg struct {
			Scripts map[string]string `json:"scripts"`
		}
		if readJSONIfExists(path, &pkg) && pkg.Scripts["test"] != "" {
			checks = append(checks, CheckSpec{Name: "npm test", Cmd: "npm test", Cwd: ".", TimeoutSec: 900})
		}
	}
	return checks
}

func loadChecks(worktree, runDir string, log *Logger) []CheckSpec {
	var file ChecksFile
	if readJSONIfExists(filepath.Join(runDir, "checks.json"), &file) && len(file.Checks) > 0 {
		checks := make([]CheckSpec, 0, len(file.Checks))
		for _, check := range file.Checks {
			if strings.TrimSpace(check.Cmd) == "" {
				continue
			}
			if check.Name == "" {
				check.Name = check.Cmd
			}
			if check.Cwd == "" {
				check.Cwd = "."
			}
			if check.TimeoutSec <= 0 {
				check.TimeoutSec = 600
			}
			checks = append(checks, check)
		}
		if len(checks) > 0 {
			return checks
		}
	}
	checks := detectDefaultChecks(worktree)
	if len(checks) == 0 {
		checks = []CheckSpec{{Name: "no checks detected", Cmd: "true", Cwd: ".", TimeoutSec: 30}}
	}
	if log != nil {
		names := make([]string, 0, len(checks))
		for _, check := range checks {
			names = append(names, check.Cmd)
		}
		log.Warn("checks.json missing, using detected defaults: %s", strings.Join(names, " | "))
	}
	return checks
}

func runChecks(ctx context.Context, checks []CheckSpec, worktree, runDir string, env []string, log *Logger) ([]CheckResult, bool) {
	logDir := filepath.Join(runDir, "checks")
	_ = os.MkdirAll(logDir, 0o755)
	results := make([]CheckResult, 0, len(checks))
	allOK := true

	for _, check := range checks {
		cwd := filepath.Join(worktree, check.Cwd)
		if log != nil {
			log.Info("check start: %s (%s)", check.Name, check.Cmd)
		}
		started := time.Now()
		result, err := runShell(ctx, check.Cmd, CmdOptions{
			Cwd:          cwd,
			Env:          env,
			Timeout:      time.Duration(check.TimeoutSec) * time.Second,
			AllowFailure: true,
		})
		duration := int(time.Since(started).Seconds())
		ok := err == nil && result.Code == 0
		if !ok {
			allOK = false
		}
		_ = os.WriteFile(
			filepath.Join(logDir, safeFileName.ReplaceAllString(check.Name, "_")+".log"),
			[]byte(fmt.Sprintf("$ %s\ncwd: %s\nexit: %d\n\n%s\n%s\n", check.Cmd, check.Cwd, result.Code, result.Stdout, result.Stderr)),
			0o644,
		)
		if log != nil {
			status := "pass"
			if !ok {
				status = "FAIL"
			}
			log.Info("check %s: %s (%ds)", status, check.Name, duration)
		}
		results = append(results, CheckResult{
			Name:        check.Name,
			Cmd:         check.Cmd,
			Cwd:         check.Cwd,
			OK:          ok,
			Code:        result.Code,
			TimedOut:    result.TimedOut,
			DurationSec: duration,
			OutputTail:  tailString(result.Stdout+"\n"+result.Stderr, 60),
		})
	}
	return results, allOK
}

// checksSummary is the one-line form used in comments and PR bodies: counts
// only, no raw commands (absolute paths in them are unreadable noise).
func checksSummary(results []CheckResult) string {
	if len(results) == 0 {
		return ""
	}
	failed := 0
	for _, item := range results {
		if !item.OK {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Sprintf("❌ 检查未通过（%d/%d 项）", failed, len(results))
	}
	return fmt.Sprintf("✅ 检查通过（%d 项）", len(results))
}

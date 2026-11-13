package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// commit is injected by the self-update build (-X main.commit=...). It lets a
// fresh start notice that the running binary is older than master. Local
// builds leave it empty and simply baseline at startup.
var commit = ""

const (
	selfRepoURL = "https://github.com/uiautodev/gh-issue-killer.git"
	selfBranch  = "master"
	selfCheck   = 30 * time.Second
	selfDelay   = 5 * time.Second
)

// watchSelfUpdates polls the project's main branch and rebuilds the running
// binary when it moves. It fires once after a successful rebuild; the caller
// waits for idle jobs and re-execs.
func watchSelfUpdates(ctx context.Context, log *Logger, env []string) <-chan struct{} {
	ready := make(chan struct{}, 1)
	go func() {
		head := remoteHead(ctx, env)
		if head == "" {
			log.Warn("self-update: cannot read %s from %s; disabled", selfBranch, selfRepoURL)
			return
		}
		last := head
		if commit != "" && commit != head {
			log.Info("self-update: running %s but %s is %s; rebuilding", shortSHA(commit), selfBranch, shortSHA(head))
			last = commit
		} else {
			log.Debug("self-update: watching %s at %s", selfBranch, shortSHA(head))
		}
		ticker := time.NewTicker(selfCheck)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			head := remoteHead(ctx, env)
			if head == "" || head == last {
				continue
			}
			log.Info("self-update: new %s commit %s; rebuilding in %s", selfBranch, shortSHA(head), selfDelay)
			time.Sleep(selfDelay)
			if settled := remoteHead(ctx, env); settled != "" {
				head = settled
			}
			if err := rebuildSelf(ctx, log, env, head); err != nil {
				log.Error("self-update: %v (keeping the running binary)", err)
				last = head
				continue
			}
			last = head
			select {
			case ready <- struct{}{}:
			default:
			}
			return
		}
	}()
	return ready
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func remoteHead(ctx context.Context, env []string) string {
	git, err := gitBinary()
	if err != nil {
		return ""
	}
	result, err := runCmd(ctx, git, []string{"ls-remote", selfRepoURL, "refs/heads/" + selfBranch}, CmdOptions{
		Env:          env,
		Timeout:      30 * time.Second,
		AllowFailure: true,
	})
	if err != nil || result.Code != 0 {
		return ""
	}
	return headFromLSRemote(result.Stdout)
}

func headFromLSRemote(out string) string {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// rebuildSelf checks out the latest main in a dedicated clone and builds the
// binary over the running executable. The build goes to a temporary file
// first, so a failed build never replaces a working binary.
func rebuildSelf(ctx context.Context, log *Logger, env []string, head string) error {
	git, err := gitBinary()
	if err != nil {
		return err
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("go not found in PATH: %w", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		exe = resolved
	}
	dir := filepath.Join(stateDir(), "self")
	if !fileExists(filepath.Join(dir, ".git")) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		log.Info("self-update: cloning %s into %s", selfRepoURL, dir)
		if _, err := runCmd(ctx, git, []string{"clone", "--depth", "1", "--branch", selfBranch, selfRepoURL, dir}, CmdOptions{
			Env:     env,
			Timeout: 5 * time.Minute,
		}); err != nil {
			return fmt.Errorf("clone: %w", err)
		}
	}
	if _, err := runCmd(ctx, git, []string{"-C", dir, "fetch", "--depth", "1", "origin",
		"+refs/heads/" + selfBranch + ":refs/remotes/origin/" + selfBranch}, CmdOptions{
		Env:     env,
		Timeout: 5 * time.Minute,
	}); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if _, err := runCmd(ctx, git, []string{"-C", dir, "reset", "--hard", "origin/" + selfBranch}, CmdOptions{
		Env:     env,
		Timeout: time.Minute,
	}); err != nil {
		return fmt.Errorf("checkout: %w", err)
	}

	tmp := exe + ".new"
	if _, err := runCmd(ctx, goBin, []string{"build", "-trimpath",
		"-ldflags", "-X main.commit=" + head,
		"-o", tmp, "./cmd/gh-issue-killer"}, CmdOptions{
		Cwd:     dir,
		Env:     env,
		Timeout: 10 * time.Minute,
	}); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("go build: %w", err)
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	log.Info("self-update: rebuilt %s", exe)
	return nil
}

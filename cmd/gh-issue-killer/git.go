package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type LocalRepo struct {
	Dir          string
	HTTPSURL     string
	Base         string
	WorktreeRoot string
	Env          []string
}

func gitBinary() (string, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git not found in PATH: %w", err)
	}
	return path, nil
}

func buildGitEnv(proxy *Proxy, token string, identity User) []string {
	overrides := map[string]string{"GIT_TERMINAL_PROMPT": "0"}
	entries := make([][2]string, 0, 3)
	if token != "" {
		basic := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
		entries = append(entries, [2]string{"http.https://github.com/.extraheader", "AUTHORIZATION: basic " + basic})
	}
	entries = append(entries, gitProxyEntries(proxy)...)
	overrides["GIT_CONFIG_COUNT"] = itoa(len(entries))
	for index, entry := range entries {
		overrides[fmt.Sprintf("GIT_CONFIG_KEY_%d", index)] = entry[0]
		overrides[fmt.Sprintf("GIT_CONFIG_VALUE_%d", index)] = entry[1]
	}
	if identity.Login != "" {
		overrides["GIT_AUTHOR_NAME"] = identity.DisplayName()
		overrides["GIT_AUTHOR_EMAIL"] = identity.NoreplyEmail()
		overrides["GIT_COMMITTER_NAME"] = identity.DisplayName()
		overrides["GIT_COMMITTER_EMAIL"] = identity.NoreplyEmail()
	}
	return mergeEnv(os.Environ(), overrides, "GH_TOKEN", "GITHUB_TOKEN")
}

func resolveRepo(ctx context.Context, repo RepoConfig, info RepoInfo, identity User, token string, proxy *Proxy) (*LocalRepo, error) {
	env := buildGitEnv(proxy, token, identity)
	httpsURL := fmt.Sprintf("https://github.com/%s.git", repo.FullName)
	cloneDir := filepath.Join(reposDir(), repo.Owner+"-"+repo.Name)
	if err := os.MkdirAll(reposDir(), 0o755); err != nil {
		return nil, err
	}

	if _, err := os.Stat(filepath.Join(cloneDir, ".git")); err != nil {
		if entries, readErr := os.ReadDir(cloneDir); readErr == nil && len(entries) > 0 {
			return nil, fmt.Errorf("managed clone path exists but is not a git repository: %s", cloneDir)
		}
		git, gitErr := gitBinary()
		if gitErr != nil {
			return nil, gitErr
		}
		result, runErr := runCmd(ctx, git, []string{"clone", httpsURL, cloneDir}, CmdOptions{
			Cwd:     reposDir(),
			Env:     env,
			Timeout: 15 * time.Minute,
		})
		if runErr != nil {
			return nil, fmt.Errorf("clone %s: %w (%s)", repo.FullName, runErr, tailString(result.Stderr, 10))
		}
	} else {
		git, err := gitBinary()
		if err != nil {
			return nil, err
		}
		_, _ = runCmd(ctx, git, []string{"remote", "set-url", "origin", httpsURL}, CmdOptions{
			Cwd:          cloneDir,
			Env:          env,
			Timeout:      30 * time.Second,
			AllowFailure: true,
		})
	}

	worktreeRoot := repo.WorktreeRoot
	if worktreeRoot == "" {
		worktreeRoot = filepath.Join(treesDir(), repo.Owner+"-"+repo.Name)
	}
	if err := os.MkdirAll(worktreeRoot, 0o755); err != nil {
		return nil, err
	}

	return &LocalRepo{
		Dir:          cloneDir,
		HTTPSURL:     httpsURL,
		Base:         info.DefaultBranch,
		WorktreeRoot: worktreeRoot,
		Env:          env,
	}, nil
}

func gitRun(ctx context.Context, local *LocalRepo, timeout time.Duration, allowFailure bool, args ...string) (CmdResult, error) {
	git, err := gitBinary()
	if err != nil {
		return CmdResult{}, err
	}
	return runCmd(ctx, git, args, CmdOptions{
		Cwd:          local.Dir,
		Env:          local.Env,
		Timeout:      timeout,
		AllowFailure: allowFailure,
	})
}

func fetchBase(ctx context.Context, local *LocalRepo) error {
	_, err := gitRun(ctx, local, 5*time.Minute, false,
		"fetch", local.HTTPSURL, fmt.Sprintf("+%s:refs/remotes/origin/%s", local.Base, local.Base))
	return err
}

func createWorktree(ctx context.Context, local *LocalRepo, issueNumber int, key string) (string, string, error) {
	branch := fmt.Sprintf("agent/issue-%d-%s", issueNumber, key)
	dir := filepath.Join(local.WorktreeRoot, fmt.Sprintf("issue-%d-%s", issueNumber, key))
	if err := os.MkdirAll(local.WorktreeRoot, 0o755); err != nil {
		return "", "", err
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return dir, branch, nil
	}
	exists, _ := gitRun(ctx, local, 30*time.Second, true, "rev-parse", "--verify", "refs/heads/"+branch)
	if exists.Code == 0 {
		_, err := gitRun(ctx, local, 2*time.Minute, false, "worktree", "add", dir, branch)
		return dir, branch, err
	}
	_, err := gitRun(ctx, local, 2*time.Minute, false, "worktree", "add", "-b", branch, dir, "origin/"+local.Base)
	return dir, branch, err
}

func createDetachedWorktree(ctx context.Context, local *LocalRepo, suffix string) (string, error) {
	dir := filepath.Join(tmpDir(), fmt.Sprintf("plan-%s-%d", suffix, time.Now().UnixNano()))
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	_, err := gitRun(ctx, local, 2*time.Minute, false, "worktree", "add", "--detach", dir, "origin/"+local.Base)
	return dir, err
}

func removeWorktree(ctx context.Context, local *LocalRepo, dir string, force bool) {
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, dir)
	_, _ = gitRun(ctx, local, 2*time.Minute, true, args...)
	// git can refuse while a killed agent's children still hold files; make
	// sure a cancelled issue does not leave its checkout behind.
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		root := filepath.Clean(treesDir()) + string(os.PathSeparator)
		if strings.HasPrefix(filepath.Clean(dir)+string(os.PathSeparator), root) {
			_ = os.RemoveAll(dir)
		}
	}
	_, _ = gitRun(ctx, local, 1*time.Minute, true, "worktree", "prune")
}

func gitStatus(ctx context.Context, dir string, env []string) string {
	git, err := gitBinary()
	if err != nil {
		return ""
	}
	result, _ := runCmd(ctx, git, []string{"status", "--porcelain"}, CmdOptions{
		Cwd:          dir,
		Env:          env,
		Timeout:      30 * time.Second,
		AllowFailure: true,
	})
	return strings.TrimSpace(result.Stdout)
}

func commitAll(ctx context.Context, dir string, env []string, message string) error {
	if gitStatus(ctx, dir, env) == "" {
		return nil
	}
	git, err := gitBinary()
	if err != nil {
		return err
	}
	if _, err := runCmd(ctx, git, []string{"add", "-A"}, CmdOptions{Cwd: dir, Env: env, Timeout: time.Minute}); err != nil {
		return err
	}
	_, err = runCmd(ctx, git, []string{"commit", "-m", message}, CmdOptions{Cwd: dir, Env: env, Timeout: time.Minute})
	return err
}

func gitOutput(ctx context.Context, dir string, env []string, timeout time.Duration, args ...string) string {
	git, err := gitBinary()
	if err != nil {
		return ""
	}
	result, _ := runCmd(ctx, git, args, CmdOptions{
		Cwd:          dir,
		Env:          env,
		Timeout:      timeout,
		AllowFailure: true,
	})
	return strings.TrimSpace(result.Stdout)
}

func logSinceBase(ctx context.Context, dir string, local *LocalRepo) string {
	return gitOutput(ctx, dir, local.Env, time.Minute, "log", "--oneline", "origin/"+local.Base+"..HEAD")
}

func diffStat(ctx context.Context, dir string, local *LocalRepo) string {
	return gitOutput(ctx, dir, local.Env, time.Minute, "diff", "--stat", "origin/"+local.Base+"...HEAD")
}

func changedFiles(ctx context.Context, dir string, local *LocalRepo) []string {
	output := gitOutput(ctx, dir, local.Env, time.Minute, "diff", "--name-only", "origin/"+local.Base+"...HEAD")
	if output == "" {
		return nil
	}
	lines := strings.Split(output, "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			files = append(files, strings.TrimSpace(line))
		}
	}
	return files
}

func pushBranch(ctx context.Context, dir string, local *LocalRepo, branch string) error {
	git, err := gitBinary()
	if err != nil {
		return err
	}
	_, err = runCmd(ctx, git, []string{"push", local.HTTPSURL, branch + ":" + branch}, CmdOptions{
		Cwd:     dir,
		Env:     local.Env,
		Timeout: 10 * time.Minute,
	})
	return err
}

func deleteLocalBranch(ctx context.Context, local *LocalRepo, branch string) {
	_, _ = gitRun(ctx, local, time.Minute, true, "branch", "-D", branch)
}

func headSHA(ctx context.Context, dir string, env []string) string {
	return gitOutput(ctx, dir, env, time.Minute, "rev-parse", "HEAD")
}

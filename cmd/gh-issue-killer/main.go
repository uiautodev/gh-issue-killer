package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func acquireLock(log *Logger) (func(), error) {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return nil, err
	}
	if data, err := os.ReadFile(lockFile()); err == nil {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
		if parseErr == nil && pid > 0 {
			if killErr := syscall.Kill(pid, 0); killErr == nil {
				return nil, fmt.Errorf("another daemon is already running (pid %d)", pid)
			}
		}
		log.Warn("removing stale lock file")
		_ = os.Remove(lockFile())
	}
	if err := os.WriteFile(lockFile(), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return nil, err
	}
	return func() { _ = os.Remove(lockFile()) }, nil
}

func stopDaemon(log *Logger) {
	data, err := os.ReadFile(lockFile())
	if err != nil {
		log.Info("no running daemon (lock file not found)")
		return
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil || pid <= 0 {
		log.Warn("invalid lock file, removing it")
		_ = os.Remove(lockFile())
		return
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		log.Info("daemon pid %d is not running, removing stale lock", pid)
		_ = os.Remove(lockFile())
		return
	}
	for i := 0; i < 50; i++ {
		if err := syscall.Kill(pid, 0); err != nil {
			log.Info("daemon pid %d stopped", pid)
			_ = os.Remove(lockFile())
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	log.Warn("daemon pid %d did not exit after 10s", pid)
}

func readLockPID() (int, bool) {
	data, err := os.ReadFile(lockFile())
	if err != nil {
		return 0, false
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil || pid <= 0 {
		return 0, false
	}
	if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
		return pid, true
	}
	return pid, false
}

func shouldDaemonize(foreground, initConfig, once, stop, status, child bool) bool {
	if foreground || initConfig || once || stop || status || child {
		return false
	}
	return true
}

const daemonChildEnv = "GHIK_DAEMON_CHILD"

func isDaemonChild() bool {
	return os.Getenv(daemonChildEnv) == "1"
}

func printStatus(configPath string) {
	configFile := firstNonEmpty(configPath, defaultConfigPath())
	if pid, running := readLockPID(); running {
		fmt.Printf("daemon: running (pid %d)\n", pid)
	} else {
		fmt.Printf("daemon: stopped\n")
	}
	fmt.Printf("config: %s\n", configFile)
	fmt.Printf("log:    %s (rotate %d MB, keep %d)\n", daemonLogPath(), logRotateSizeMB, logRotateKeep)
	fmt.Printf("state:  %s\n", stateFile())
	cfg, err := loadConfig(configFile)
	if err != nil {
		fmt.Printf("repos:  (config error: %v)\n", err)
		return
	}
	if cfg.Webhook {
		fmt.Printf("webhook: smee (polling fallback)\n")
	} else {
		fmt.Printf("webhook: polling only\n")
	}
	for _, repo := range cfg.Repos {
		fmt.Printf("repo:   %s (%s)\n", repo.FullName, repo.CodingAgent)
	}
}

// startBackground re-execs this binary in a new session. The child is marked
// with GHIK_DAEMON_CHILD=1 so it runs the daemon loop instead of forking again.
func startBackground(configPath string) error {
	if pid, running := readLockPID(); running {
		fmt.Printf("already running (pid %d)\n", pid)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := make([]string, 0, 2)
	if configPath != "" {
		args = append(args, "-config", configPath)
	}
	command := exec.Command(exe, args...)
	command.Env = append(os.Environ(), daemonChildEnv+"=1")
	if cwd, err := os.Getwd(); err == nil {
		command.Dir = cwd
	}
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := command.Start(); err != nil {
		return err
	}
	pid := command.Process.Pid
	alive := false
	for attempt := 0; attempt < 20; attempt++ {
		if lockPID, _ := readLockPID(); lockPID == pid {
			alive = true
			break
		}
		if err := syscall.Kill(pid, 0); err != nil && !errors.Is(err, syscall.EPERM) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !alive {
		alive = syscall.Kill(pid, 0) == nil || errors.Is(syscall.Kill(pid, 0), syscall.EPERM)
	}
	_ = command.Process.Release()
	if !alive {
		fmt.Fprintf(os.Stderr, "failed to start gh-issue-killer; see %s\n", daemonLogPath())
		if data, err := os.ReadFile(daemonLogPath()); err == nil {
			fmt.Fprintln(os.Stderr, tailString(string(data), 15))
		}
		return errors.New("daemon failed to start")
	}

	fmt.Printf("started gh-issue-killer (pid %d)\n", pid)
	fmt.Printf("log: %s (rotate %d MB, keep %d)\n", daemonLogPath(), logRotateSizeMB, logRotateKeep)
	fmt.Printf("stop: gh-issue-killer -stop\n")
	return nil
}

func main() {
	configPath := flag.String("config", "", "path to config.json")
	initConfig := flag.Bool("init", false, "run the interactive setup wizard and write config.json")
	once := flag.Bool("once", false, "run startup scan and one poll, then exit")
	foreground := flag.Bool("foreground", false, "run attached to the terminal (default is background)")
	flag.BoolVar(foreground, "f", false, "shorthand for -foreground")
	stop := flag.Bool("stop", false, "stop the running daemon and exit")
	status := flag.Bool("status", false, "show daemon status and paths")
	logs := flag.Bool("logs", false, "follow the daemon log in real time")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "gh-issue-killer - GitHub issue watcher\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Usage:\n  gh-issue-killer [flags]\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nWithout flags the daemon runs in the background.\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Examples:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  gh-issue-killer               # start in the background\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  gh-issue-killer -f            # run attached, Ctrl+C to stop\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  gh-issue-killer -logs         # follow the daemon log\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  gh-issue-killer -status       # pid, config, log and state paths\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  gh-issue-killer -stop         # stop the running daemon\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  gh-issue-killer -init         # guided config.json setup\n")
	}
	flag.Parse()

	if *logs {
		if err := followDaemonLog(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *initConfig {
		if err := runSetupWizard(*configPath, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if *status {
		printStatus(*configPath)
		return
	}
	if *stop {
		if err := ensureDirs(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		stopDaemon(NewLogger("daemon"))
		return
	}

	configFile := firstNonEmpty(*configPath, defaultConfigPath())
	child := isDaemonChild()

	if _, err := os.Stat(configFile); errors.Is(err, fs.ErrNotExist) {
		if isTerminal(os.Stdin) && !child {
			if wizardErr := runSetupWizard(configFile, os.Stdin, os.Stdout); wizardErr != nil {
				fmt.Fprintln(os.Stderr, wizardErr)
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "no config at %s: run `gh-issue-killer -init` to create one\n", configFile)
			os.Exit(1)
		}
	}

	if shouldDaemonize(*foreground, *initConfig, *once, *stop, *status, child) {
		if err := startBackground(*configPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if err := ensureDirs(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log := NewLogger("daemon")
	log.Info("log file %s (rotate %d MB, keep %d)", daemonLogPath(), logRotateSizeMB, logRotateKeep)
	if *foreground {
		log.Info("foreground mode: attached to this terminal, Ctrl+C to stop")
	}
	if child {
		log.Info("daemon child mode: detached")
	}
	cfg, err := loadConfig(configFile)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			log.Error("no config at %s: run `gh-issue-killer -init` to create one", configFile)
		} else {
			log.Error("%v", err)
		}
		os.Exit(1)
	}
	proxy, err := parseProxy(cfg.Proxy)
	if err != nil {
		log.Error("%v", err)
		os.Exit(1)
	}
	release, err := acquireLock(log)
	if err != nil {
		log.Error("%v", err)
		os.Exit(1)
	}
	defer release()

	log.Info("gh-issue-killer (go) starting, config=%s proxy=%s", cfg.File, firstNonEmpty(cfg.Proxy, "direct"))
	log.Debug("runtime: codingAgent=%s model=%s poll=%s webhook=%v maxParallel=%d maxFixRounds=%d",
		cfg.CodingAgent, firstNonEmpty(cfg.Model, "(default)"), cfg.PollInterval, cfg.Webhook, cfg.MaxParallel, cfg.MaxFixRounds)
	log.Debug("paths: config=%s state=%s log=%s", cfg.File, stateFile(), daemonLogPath())
	for _, repo := range cfg.Repos {
		log.Debug("repo config: %s codingAgent=%s model=%s approvers=%s", repo.FullName, repo.CodingAgent, firstNonEmpty(repo.Model, "(default)"), strings.Join(repo.Approvers, ","))
	}

	if path, created, err := ensureGhAgentWrapper(); err != nil {
		log.Warn("gh-agent wrapper: %v", err)
	} else if created {
		log.Info("created gh-agent wrapper at %s", path)
	}

	gh, err := NewGH(proxy, log)
	if err != nil {
		log.Error("github client: %v", err)
		os.Exit(1)
	}
	token := gh.Token
	identity := gh.Agent
	log.Info("agent identity: @%s (%s)", identity.Login, identity.NoreplyEmail())
	if cfg.DeprecatedAgent != "" {
		log.Warn("config.agent is deprecated and ignored; choose the CLI with config.codingAgent (%s)", supportedCodingAgentList())
	}
	checkedAgents := map[string]bool{}
	for _, repo := range cfg.Repos {
		if checkedAgents[repo.CodingAgent] {
			continue
		}
		info, ok := codingAgentInfoByID(repo.CodingAgent)
		if !ok {
			log.Error("repo %s: unsupported codingAgent %q", repo.FullName, repo.CodingAgent)
			os.Exit(1)
		}
		if _, err := runnerBinary(info); err != nil {
			log.Error("%v", err)
			os.Exit(1)
		}
		checkedAgents[repo.CodingAgent] = true
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	contexts := map[string]*AppContext{}
	for _, repo := range cfg.Repos {
		info, err := gh.GetRepo(ctx, repo.FullName)
		if err != nil {
			log.Error("repo %s: %v", repo.FullName, err)
			cancel()
			os.Exit(1)
		}
		if !info.Permissions.Push {
			log.Warn("%s: gh-agent has no push access; PRs/merges will fail", repo.FullName)
		}
		local, err := resolveRepo(ctx, repo, info, identity, token, proxy)
		if err != nil {
			log.Error("repo %s: %v", repo.FullName, err)
			cancel()
			os.Exit(1)
		}
		gh.EnsureLabels(ctx, repo.FullName)

		envOverrides := goProxyEnv(proxy)
		envOverrides["GH_CONFIG_DIR"] = filepath.Join(homeDir(), ".config", "gh-agent")
		childEnv := mergeEnv(buildGitEnv(proxy, token, identity), envOverrides, "GH_TOKEN", "GITHUB_TOKEN")

		contexts[repo.FullName] = &AppContext{
			Cfg:        cfg,
			Repo:       repo,
			RepoInfo:   info,
			Local:      local,
			Identity:   identity,
			Token:      token,
			Proxy:      proxy,
			GH:         gh,
			Store:      nil,
			Log:        log,
			Env:        childEnv,
			AgentLogin: identity.Login,
		}
		log.Info("repo ready: %s clone=%s base=%s worktrees=%s approvers=%s", repo.FullName, local.Dir, local.Base, local.WorktreeRoot, strings.Join(repo.Approvers, ","))
	}

	store, err := LoadStore()
	if err != nil {
		log.Error("state: %v", err)
		os.Exit(1)
	}
	for _, app := range contexts {
		app.Store = store
	}

	router := NewRouter(cfg, contexts, identity.Login, store, log)
	router.StartupScan(ctx)

	if *once {
		for fullName := range contexts {
			router.PollOnce(ctx, fullName)
		}
		_ = store.Save()
		return
	}

	if cfg.Webhook && store.GlobalSecret() == "" {
		store.SetGlobalSecret(randomHex(24))
	}

	var stops []func()
	for fullName, app := range contexts {
		if !cfg.Webhook {
			break
		}
		channel, secret, err := ensureSmeeHook(ctx, app)
		if err != nil {
			log.Warn("%s: webhook management failed (%v); still listening if a channel exists", fullName, err)
		}
		if channel == "" || secret == "" {
			meta := app.Store.Meta(fullName)
			channel = firstNonEmpty(channel, meta.SmeeChannel)
			secret = firstNonEmpty(secret, meta.WebhookSecret)
		}
		if channel == "" || secret == "" {
			log.Warn("%s: no smee channel available; polling only", fullName)
			continue
		}
		log.Info("%s: smee channel %s (hook %d)", fullName, channel, app.Store.Meta(fullName).HookID)
		stops = append(stops, startSmee(ctx, app, router, channel, log))
	}
	stops = append(stops, startPoller(ctx, cfg, router, contexts, log))

	stopSignals := make(chan os.Signal, 1)
	signal.Notify(stopSignals, syscall.SIGINT, syscall.SIGTERM)
	<-stopSignals
	log.Info("shutting down")
	cancel()
	for _, stop := range stops {
		stop()
	}
	_ = store.Save()
	log.Info("bye")
}

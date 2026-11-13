package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
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

func main() {
	configPath := flag.String("config", "", "path to config.json")
	initConfig := flag.Bool("init", false, "run the interactive setup wizard and write config.json")
	once := flag.Bool("once", false, "run startup scan and one poll, then exit")
	foreground := flag.Bool("foreground", false, "run attached to the terminal (default behaviour, logs stream to stdout)")
	flag.BoolVar(foreground, "f", false, "shorthand for -foreground")
	stop := flag.Bool("stop", false, "stop the running daemon and exit")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "gh-issue-killer - GitHub issue watcher\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Usage:\n  gh-issue-killer [flags]\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Flags:\n")
		flag.PrintDefaults()
		fmt.Fprintf(flag.CommandLine.Output(), "\nExamples:\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  gh-issue-killer -init         # guided config.json setup\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  gh-issue-killer -foreground   # run attached, Ctrl+C to stop\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  gh-issue-killer -f            # shorthand\n")
		fmt.Fprintf(flag.CommandLine.Output(), "  gh-issue-killer -stop         # stop the running daemon\n")
	}
	flag.Parse()

	if *initConfig {
		if err := runSetupWizard(*configPath, os.Stdin, os.Stdout); err != nil {
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

	if *stop {
		stopDaemon(log)
		return
	}
	if *foreground {
		log.Info("foreground mode: attached to this terminal, Ctrl+C to stop")
	}

	configFile := firstNonEmpty(*configPath, defaultConfigPath())
	cfg, err := loadConfig(configFile)
	if err != nil && errors.Is(err, fs.ErrNotExist) && isTerminal(os.Stdin) {
		log.Info("no config at %s, starting the setup wizard", configFile)
		if wizardErr := runSetupWizard(configFile, os.Stdin, os.Stdout); wizardErr != nil {
			log.Error("setup: %v", wizardErr)
			os.Exit(1)
		}
		cfg, err = loadConfig(configFile)
	}
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
		envOverrides["GWIK_AGENT"] = identity.Login
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

	var server *http.Server
	if cfg.Webhook {
		secret := store.GlobalSecret()
		if secret == "" {
			secret = randomHex(24)
			store.SetGlobalSecret(secret)
		}
		server = startWebhookServer(cfg, router, contexts, secret, log)
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
		stops = append(stops, startSmee(ctx, app, router, channel, secret, log))
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
	if server != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = server.Shutdown(shutdownCtx)
		shutdownCancel()
	}
	_ = store.Save()
	log.Info("bye")
}

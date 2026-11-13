package main

import (
	"context"
	"flag"
	"fmt"
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

func main() {
	configPath := flag.String("config", "", "path to config.json")
	once := flag.Bool("once", false, "run startup scan and one poll, then exit")
	flag.Parse()

	if err := ensureDirs(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	log := NewLogger("daemon")
	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Error("%v", err)
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
			log.Warn("%s: real-time webhook unavailable (%v); polling only", fullName, err)
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

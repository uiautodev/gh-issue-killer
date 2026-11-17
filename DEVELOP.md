# DEVELOP.md — developer guide

How the daemon is built and how to extend it. User-facing behaviour lives in
[README.md](README.md) / [README.zh-CN.md](README.zh-CN.md); installation and
operations live in [DEPLOY.md](DEPLOY.md).

> **This file is part of the code.** When a change alters a stage, a phase, a
> queue, a prompt, a check, a label or a config key, update the matching section
> here in the same commit. §19 has the mapping.

---

## 1. Build, test, run

```bash
go build -o bin/gh-issue-killer ./cmd/gh-issue-killer
go vet ./...
go test ./...
gofmt -l .            # must print nothing
GOOS=windows GOARCH=amd64 go build ./cmd/gh-issue-killer   # platform layer still compiles
pre-commit install    # optional: gofmt, vet, test, build, 1 MB file guard
```

The dashboard is a SvelteKit SPA in `frontend/`; `go build` embeds
`cmd/gh-issue-killer/web/dist`. Build it once before building a release binary
(and always after changing `frontend/`), otherwise the binary serves a "build
the frontend" page:

```bash
npm --prefix frontend ci
npm --prefix frontend run check
npm --prefix frontend run build   # writes cmd/gh-issue-killer/web/dist
go build -o bin/gh-issue-killer ./cmd/gh-issue-killer
```

The desktop shell is a separate module so the daemon keeps its single
dependency (`fsnotify`):

```bash
cd desktop && go build .        # Wails 3 window; needs the wails v3 module
desktop/build/package.sh v1.2.3 # macOS .app + .dmg / Linux tarball; .ps1 on Windows
```

Go 1.27 (see `go.mod`). The only direct dependency is `github.com/fsnotify/fsnotify`;
everything else is the standard library, including Git invocation (`os/exec`),
Markdown, image composition and the HTTP API client.

Run it while developing:

```bash
./bin/gh-issue-killer -f        # foreground, Ctrl+C to stop
./bin/gh-issue-killer -once     # startup scan + one poll, then exit
./bin/gh-issue-killer -status   # pid, config, log, state paths
./bin/gh-issue-killer -logs     # follow the daemon log
```

End-to-end smoke test against a scratch repository:

```bash
scripts/smoke-test.sh owner/repo
```

It drives a real issue through plan → approve → implement → merge using the human
`gh` account while the daemon uses the bot token from the dashboard login (or
`GH_AGENT_TOKEN` / the legacy `gh-agent` hosts file).

## 2. Runtime layout

Everything lives under `~/.gh-issue-killer` (`stateDir()` in `util.go`):

| path | contents |
| --- | --- |
| `state.json` | the single source of task state (`state.go`), atomically rewritten |
| `daemon.lock` | pid of the running daemon; `-stop`/`-status` read it |
| `token` | built-in GitHub login (`oauth.go`): access token, expiry and refresh token, mode 0600 |
| `logs/daemon.log` | rotating daemon log (10 MB × 5) |
| `repos/<owner>-<repo>/` | managed clone per watched repository |
| `worktrees/<owner>-<repo>/issue-<n>-<id>/` | one implementation worktree per issue |
| `tmp/` | detached plan worktrees and downloaded update archives |
| `runs/<owner>-<repo>/issue-<n>/` | per-run artifacts: `plan-*.log`, `implement-*.log`, `revise-*.log`, `ci-fix-*.log`, `checks.json`, `checks/`, `result.json`, `screenshots/` |
| `logs/desktop-engine.log` | stdout/stderr of the engine started by the desktop shell (created by `desktop/`) |

Only the daemon writes `state.json`; the web UI and CLIs read it. Treat
`Store` as the single writer (see §13).

## 3. Startup sequence (`main.go`)

1. Flags; a bare run daemonizes itself (`startBackground` child mode).
   `-embedded` is the desktop shell's engine: foreground, no detach, no
   process-group tricks on Windows, and an exit code 75 (`exitRestart`) when a
   config save or an update asks for a restart. `-web-port` overrides
   `config.webPort` for that run.
2. `ensureDirs`, logger, `loadConfig`, `parseProxy`.
3. `acquireLock` on `daemon.lock` — one daemon per machine, desktop engine
   included.
4. `NewGH`: resolves the credential in `GH_AGENT_TOKEN` → `~/.gh-issue-killer/token`
   → the old `gh-agent` `hosts.yml` (read-only compatibility), refreshes it when
   it is close to expiry, and verifies the identity (never the human account).
5. For each repo: `GetRepo`, `resolveRepo` (managed clone under `repos/`),
   `EnsureLabels`, build the child env (proxy + `GH_TOKEN`/`GITHUB_TOKEN` so the
   agent's own `gh` calls use the bot).
6. `LoadStore`, `NewRouter` (queues + worker goroutines), optional web server.
7. `router.StartupScan`: resumes interrupted work, applies pending closes,
   and picks up issues that already mention the agent.
8. `watchConfig` (fsnotify + debounce + 30 s safety poll) and, when
   `selfUpdate` is on, `watchSelfUpdates` (release polling).
9. Smee webhook per repo (`ensureSmeeHook`) plus one poller goroutine per
   repository. Each repo has its own 30 s ticker, so a slow or stalled API call
   in one repository does not delay the others. The SSE stream is recycled every
   `smeeRecycle` (30 min) because an idle connection can die silently and then
   every webhook is missed while the poller carries the load.
10. `startReleaseChecker`: one more goroutine per repo with `release.enabled`,
    checking at startup and hourly (the config interval throttles the API calls).
11. `SIGINT`/`SIGTERM` → wait for `router.Idle()` → shut down → `reexec` for
    config reloads and self-updates (pid is kept, so the lock stays valid).

**Setup mode.** `-embedded` with a missing or invalid config, or without a
GitHub token, does not exit and does not start a daemon loop: `serveSetup` runs
the dashboard on the requested port with an empty router, `configError` in
`/api/state`, and the settings + login panels. Saving a valid config or
finishing the login exits with 75 so the shell starts the real engine.

**Platform layer.** `process_unix.go` / `process_windows.go` own every
`syscall.Kill`, `Setsid`, `Setpgid`, `exec` and shell invocation:
`processAlive`, `terminateProcess`, `detachSysProcAttr`, `childSysProcAttr`,
`killChildTree`, `shellCommand`, `canExecInPlace` + `execInPlace`. The old
`gh-agent` wrapper is no longer created. Windows uses `OpenProcess`/`GetExitCodeProcess`,
`taskkill /T /F` for process trees, `cmd /C` for checks, and “start a new
process, then exit” instead of `syscall.Exec`.

## 4. Package map

All Go code is `package main` in `cmd/gh-issue-killer`; `prompts` embeds the
Markdown prompts; `web` embeds the SvelteKit build in `web/dist`. `frontend/`
is the SvelteKit/Tailwind/shadcn-svelte source for that build, and `desktop/` is
a separate Go module (Wails 3) so the daemon does not inherit the GUI
dependencies.

| file | responsibility |
| --- | --- |
| `main.go` | flags, startup order, daemonize, lock, shutdown/reexec |
| `process_unix.go`, `process_windows.go` | the platform layer: liveness, terminate/process tree, detach, shell, `exec` |
| `config.go` | config parsing, normalization, per-repo defaults |
| `router.go` | event handling, commands, state machine, job scheduler (`plan`/`work`/`ci` queues), startup scan |
| `actions.go` | the stages: plan, implement, revise, CI settle, merge, and their helpers |
| `checks.go` | `checks.json` detection/loading/execution and the summary line |
| `gh.go` | GitHub REST client: issues, comments, PRs, labels, reviews, check runs, workflow runs/jobs, hooks, tags, commits, releases, asset uploads; token resolution and the 401 refresh retry |
| `oauth.go` | built-in GitHub Device Flow, token file (`~/.gh-issue-killer/token`, 0600), expiry/refresh, legacy `hosts.yml` reader |
| `git.go` | managed clone, worktrees, commit/push, diff/stat, env |
| `runner.go` | coding-agent registry (`opencode`, `claude`, `codex`) and process/JSON output handling |
| `state.go` | `Store`, `IssueState`, `RepoState`, atomic persistence |
| `watcher.go`, `watch.go` | config reload (fsnotify + digest polling) |
| `release.go` | opt-in periodic releases: version tag parsing, commit classification, bumping and publishing |
| `selfupdate.go` | release asset selection, checksum verification, binary replacement, 30 min polling |
| `logs.go` | rotating log file, `-logs` follower |
| `shots.go` | screenshot collection, before/after composition, upload |
| `proxy.go` | SOCKS5/HTTP proxies for HTTP clients, git and child processes |
| `wizard.go` | `-init` interactive setup |
| `web.go`, `web/dist` | loopback dashboard; embeds the SvelteKit build and serves it with an SPA fallback: `/api/state`, `/api/issue`, `/api/logs`, `/api/action`, `/api/open-url`, `/api/auth*` |
| `webauth.go` | dashboard login endpoints: Device Flow start/poll and pasted-token fallback |
| `openurl.go` | opens an external http(s) link in the platform browser (`open`/`xdg-open`/`rundll32`) |
| `webconfig.go` | settings API: `/api/config`, `/api/detect`, `/api/restart`, `/api/update` |
| `messages.go` | scaffolding text (headings, hints, footers) in Chinese/English, `app.text`, language detection |
| `util.go` | paths, logger, atomic writes, `tailString`, small helpers |
| `prompts/*.md` | stage prompts (embedded via `prompts/embed.go`) |
| `docs/testerhome.md` | Chinese promo article for testerhome.com (not part of the build) |
| `docs/v2ex-share.md` | Chinese V2EX sharing draft plus a manual publishing checklist (not part of the build) |
| `docs/images/dashboard-*.png` | dashboard screenshots referenced by the V2EX draft, captured with sample data (not part of the build) |
| `assets/sponsor.png` | WeChat reward QR code shown at the end of `README.zh-CN.md` (not part of the build) |
| `desktop/` | separate Go module: Wails 3 shell (`main.go`, `engine.go`, `process_*.go`) and `build/` packaging (icon rendered from `frontend/static/logo.svg`, `.app`, DMG) |
| `.agents/skills/modern-web-guidance/`, `skills-lock.json` | vendored GoogleChrome modern-web-guidance skill (SKILL.md + `guides/`) and its pinned commit, Apache-2.0 / CC-BY-4.0; search it before changing dashboard CSS, layout, tables, dialogs or theme handling |
| `frontend/` | SvelteKit SPA for the dashboard: Tailwind v4 + shadcn-svelte tokens, zh/en strings, routes and `$lib` API/i18n/theme modules (`dialog.ts` wraps native dialogs and the light-dismiss fallback); `npm run build` writes `cmd/gh-issue-killer/web/dist` |

## 5. State model and phases

`IssueState` (`state.go`) is keyed by issue number inside
`Repos[fullName].Issues["<n>"]`. Important fields: `Phase`, `Plan`,
`SessionID` (coding-agent session), `Branch`, `Worktree`, `PRNumber`, `PRURL`,
`PRCommentID` (the 👍 target), `Checks`, `Screenshots`, `BlockedReason`,
`ProcessedComments`/`ProcessedFeedback`, `MergedAt`/`CancelledAt`.

| phase | meaning | who sets it |
| --- | --- | --- |
| `new` | mention/assignment seen, nothing started | router |
| `planning` | plan agent running in a detached worktree | `doPlan` |
| `awaiting_approval` | plan comment posted, waiting for lgtm | `doPlan` |
| `implementing` | coding agent running in the issue worktree | `doImplement` / `doRevise` |
| `pr_open` | PR open, waiting for the merge command (CI may be settling/repairing) | `doImplement` / `doRevise` / `settleCIGate` |
| `merging` | merge command accepted, re-checking CI | `doMerge` |
| `blocked` | needs a human: failed checks, exhausted repair budget, missing worktree, unreproducible bug | several stages |
| `done` | squash-merged, issue closed, worktree removed | `doMerge` |
| `cancelled` | issue closed by a human; PR closed, worktree removed | `HandleClosed` / `doCancel` |

Lifecycle labels (`gh.go`) are exactly two and mutually exclusive:
`agent:awaiting-approval` when the flow waits for the user, `agent:working`
while an agent runs. There is no separate blocked label. `SetAgentState` is
idempotent: it reads the issue's labels and only deletes the stale lifecycle
label and adds the wanted one, so re-applying the same state never shows up as
add → remove → add in the issue timeline. A failed read falls back to the
unconditional update so a stale label cannot survive a transient error.

## 6. Events, commands, feedback

Sources: smee relay webhook (primary) and a 30 s poller (fallback). Every event
is re-read from the API before acting, and deliveries are de-duplicated.

`resolveCommand` (`router.go`) accepts an approver mention followed by:

| input | command |
| --- | --- |
| `lgtm`, `continue`, `继续`, 👍 reaction on the plan/PR comment | approve / merge |
| `合并`, `merge`, `ship` | merge (the issue's PR, or a pull request the daemon does not own) |
| `cancel`, `stop`, `停止` | cancel |
| `review`, `review pr`, `cr`, `审查` (the target is a pull request) | review the PR and post a GitHub review |
| `apply`, `apply review`, `应用` (the target is a pull request) | apply the latest bot review's findings without a new review |
| `<approval word> <free text>` (`ok`, `yes`, `go`, …) | approve **and** forward the rest as `ExtraNotes` to the implementation |

Other rules:

- A mention on an issue in phase `new`/empty starts planning; `bug`-labelled
  issues skip the plan and go straight to implementation with reproduction
  instructions (`bugInstructions`). A mention on a pull request never plans.
- The repo's `mode` decides the first stage for a non-bug issue: `plan` always
  posts the plan comment, `build` goes straight to implementation, and the
  default `auto` asks the coding agent (`triage.md`) whether the request needs
  a plan. In `auto`, an explicit "plan first" comment also forces the plan, and
  a failed triage falls back to it. A build start sets `implementing` before the
  job is queued, so the poller cannot start a second first stage.
- `review` is the only command that targets a PR directly. It works for any PR
  in a watched repo, not only the daemon's own, and runs on the plan queue with
  its own job key (`<repo>#<pr>#review`).
- A non-command mention from an approver on a pull request the daemon does not
  own is a **modification request** (`doModifyPR`): edit the PR head, run the
  checks and push back to the head branch. Same-repository branches are pushed
  directly; a fork is pushed only when the PR has
  `maintainer_can_modify`, otherwise the daemon replies instead of failing.
- A non-command comment that mentions the agent, an inline review comment or a
  review body on an open PR becomes `doRevise` (feedback on the same branch).
- An `edited` issue comment is reprocessed only when its new body mentions the
  agent (`Trigger.Reprocess`), so adding the mention after the fact still
  reaches the daemon; other edits stay silent.
- Closing an issue cancels the task and closes the PR; reopening resumes. A
  reopened `done` issue starts from `new`.
- 👍 on the stored `PRCommentID` merges, but only while phase is `pr_open`.
- Accepting an approval or merge moves the phase immediately (`awaiting_approval`
  → `implementing`, `pr_open` → `merging`) before the job runs. The worker is
  often busy with another task, and without this the dashboard kept showing
  “waiting for confirmation” while the label already said working, and the
  poller re-approved the same 👍 on every scan.
- 👎 is ignored; 👍 on the review prompt comment (`ReviewCommentID`) means
  “apply this review”: the daemon rebuilds the instructions from the review body
  and its inline comments (`reviewInstructions`) and runs `doModifyPR` on the
  PR. This exists because GitHub has no reactions API for review bodies, so the
  daemon posts a small follow-up comment that carries the thumbs-up. The prompt
  id is cleared when consumed, so the poller does not restart the fix on every
  scan.
- Reaction events are not always delivered, so `checkReactions` (30 s poller)
  re-checks 👍 on the plan comment, the PR announcement and the review prompt;
  reviewed PRs are not in a task phase, so the review check runs for any open
  issue with a stored `ReviewCommentID`.

## 7. Job scheduler (`router.go`)

Per repository there are three queues, each with its own worker goroutines:

| stage | workers | used for |
| --- | --- | --- |
| `stagePlan` | `planParallel` (5) | planning (detached worktree) |
| `stageWork` | `implementParallel` (1) | implement, revise, merge |
| `stageCI` | `ciWorkers` (2) | post-push CI settle, including repairs |

Rules that keep it safe:

- A job key is `<repo>#<issue>` (dedupe: a second command for a busy issue is
  skipped). CI jobs use `<repo>#<issue>#ci`; the plan-skipping starts use
  `<repo>#<issue>#bug` and `<repo>#<issue>#build`, so they still run on the
  work queue under the implementation slot.
- `cancelActive` cancels every key from `issueJobKeys` (plan/work, CI and the
  plan-skipping `#bug`/`#build` starts); `HandleClosed` waits for all of them
  before the worktree is removed, so cleanup never races a running agent.
- A merge waits for the issue's CI job (`waitCISettle`) before touching the
  worktree. Two agents never edit one worktree at the same time.
- The CI queue exists so a slow gate never occupies the implementation slot;
  the coding stage only enqueues and returns.

**Release schedule** (`release.go`) is not a queue: every repo with
`release.enabled` gets one goroutine started from `main.go`. It checks at
startup and then every `releaseTick` (one hour); `release.intervalDays` decides
whether that tick may call GitHub, and `RepoMeta.LastReleaseCheckAt` records a
successful check (including “no commits”), so a failed request retries on the
next tick instead of waiting a whole interval. Publishing never enqueues a job
and never touches an issue worktree.

## 8. Stages (`actions.go`)

Each stage gets `jobCtx` from the scheduler and writes `RunDir` artifacts under
`runs/<repo>/issue-<n>/`.

**Triage** (`shouldBuildDirectly`) — only in `mode=auto`. Prompt `triage.md`,
detached read-only worktree from the base branch, `planTimeoutMin`. The first
line of the answer must be `plan` or `build`; decorations and trailing
punctuation are tolerated, anything else falls back to the plan. A human
comment that asks to plan first (`planFirstRe`) skips the agent run entirely.
The triage worktree is removed afterwards. `mode=plan`/`build` skip this stage.

**Plan** (`doPlan`) — prompt `plan.md`, detached worktree from the base branch,
posts the plan comment, stores `Plan`, `PlanCommentID`, `SessionID`, sets
`awaiting_approval`.

**Implement** (`doImplement`) — creates/reuses the worktree and branch, prompt
`implement.md` (includes comments, plan, `NOTES` from approval text and
`BUG_INSTRUCTIONS`), then:

1. `loadManifest(runDir)` reads `result.json` (summary, changes, tests,
   screenshots, `abort`).
2. `runChecksWithFixes` runs `checks.json` with up to `maxFixRounds` repair
   rounds using `fix.md`.
3. Commit, push, create the PR (`Closes #n` in the body), post the “实现完成”
   comment, then hand CI to the `ci` queue (`app.EnqueueCI`).

**Revise** (`doRevise`) — same worktree and agent session, prompt `revise.md`
with the feedback text, local checks with fixes, commit/push, comment, then the
same CI settle.

**Review** (`doReviewPR`) — read-only and independent of the issue flow. It
fetches `refs/pull/<n>/head` (works for fork PRs), checks it out in a detached
worktree, runs the agent with `review.md` and reads `review.json`
(`verdict`, `summary`, `comments[]`), then posts a GitHub review
(`POST /pulls/{n}/reviews`) with up to 10 inline comments. Findings that point
at a file or line outside the diff cannot be attached inline (GitHub answers
422 “Path could not be resolved”), so they are listed in the review body
instead. It then posts a “Review 已提交” comment and stores its id plus the
review id in the PR's `IssueState`, so a 👍 on that comment applies the
findings through `doModifyPR`. Nothing is ever pushed by the review itself and
the worktree is removed afterwards.

**Merge PR** (`doMergePR`) — lands a PR the daemon does not own: fetch the head
into a branch worktree, resolve conflicts with `conflict_fix.md` (merge the base,
agent resolves, checks run, push to the head repo), wait for GitHub checks and
squash-merge. Uses `conflictTarget` so the same resolution code works for issue
worktrees (push to the base repo) and fork PRs (push to the fork).

**Modify PR** (`doModifyPR`) — applies an approver's instruction to a PR the
daemon does not own. It fetches the PR head into a branch worktree, runs the
agent with `modify_pr.md`, runs `checks.json` (with repair rounds), commits and
pushes to `pr.Head.Ref` on `pr.Head.Repo` (`pushBranchTo`). `canPushToPR`
allows same-repository branches and forks with `maintainer_can_modify`; without
it the daemon comments that it cannot push. The worktree and local branch are
removed afterwards.

**CI settle** (`settleCIGate`, runs on the CI queue) — if the repo has no
workflows it returns immediately; otherwise it waits briefly for the new check
runs, posts a “正在等待 CI” comment while they are pending, runs the gate with
repairs (§9), and then either posts the merge prompt or parks the issue as
`blocked`.

**Merge** (`doMerge`) — a loop that keeps the branch landable:

1. **Conflicts** — when `mergeBlockedByConflicts` sees `mergeable_state: dirty`
   (or an unmergeable PR), `resolveConflicts` fetches the base, runs
   `git merge origin/<base>` in the worktree and, if git left markers, runs the
   agent with `conflict_fix.md`, then the usual local checks, commit and push.
   `resolveMergeConflicts` announces each round; at most `maxFixRounds`. After a
   push it waits up to 45 s for the new head's check runs to appear (when the
   repo has workflows) so the gate sees them instead of “no CI”.
2. **CI** — `ciGateWithRepairs` (§9) on the (possibly new) head.
3. **Merge** — `MergePR`; a 405/409 “merge conflicts” at this point means the
   base moved after the check, so the loop resolves once more and retries
   (`isConflictError`). Other failures park the issue.

On success: squash-merge, delete the branch, close the issue, remove the
worktree and local branch, set `done`.

## 9. CI gate and repair (`actions.go`)

`waitForChecks` returns a `ciVerdict`:

- **fail fast** — a completed non-success check fails the gate immediately; it
  is no longer hidden behind a permanently pending one.
- **stuck detection** — a check that is still queued with no `started_at` after
  `checkStuckAfter` (5 min) is treated as a job that will never run (retired
  runner label). Message: `queued with no runner (check the workflow's runner label)`.
- **required checks only** — when the base branch has branch protection, only
  its required status checks gate; without protection every check gates.
- **base comparison** — failures that also fail on the base branch are marked
  `(also failing on <base>)`.

`ciGateWithRepairs` loops up to `maxFixRounds`:

1. `action_required` runs (first-time contributor approval) are approved via
   `ApproveWorkflowRun` and re-checked.
2. Otherwise `ciRepairRound` runs the coding agent in the same worktree with
   `prompts/ci_fix.md` — the blocking checks plus the tail (40 lines) of up to
   four failed job logs. The prompt tells the agent to fix the workflow when
   the same check fails on the base branch (retired runner, missing Python
   version, deprecated actions) and the code otherwise.
3. Local checks must pass, then the result is committed, pushed and re-gated.
   A round counts as changed when the worktree is dirty **or** the agent
   committed on its own (`HEAD` moved), so an agent that runs `git commit`
   cannot be mistaken for “nothing happened”. Before gating, a worktree that is
   ahead of the PR head (a stranded commit from an earlier round) is pushed
   first, otherwise the gate would keep checking the old head.

A round that truly produces no change annotates the verdict with
`（自动修复未产生改动）` and ends the loop. After a push the gate waits for the
PR head to report the new commit (`waitForPRHead`) before concluding that
nothing moved; a single read is racy. Exhausting the budget parks the issue
as `blocked` and the message says how many attempts ran.

CI-blocked tasks are marked with `BlockedByCI: true`. `retryBlockedCI`, part of
the 30 s poll, watches them: when the PR is open, not conflicting, and every
check run finished green, it clears the flag and re-runs the merge path, so a
transient CI failure does not need a manual `合并`. Other blocked reasons (local
checks, an unreproducible bug, exhausted conflict rounds) never resume on their
own.

## 10. Checks, manifest and screenshots

`checks.json` (written by the agent) wins; otherwise `detectDefaultChecks`
picks `go test`/`go vet`, `frontend/*` npm scripts, or `npm test`. Each check
runs with its own timeout, output tail and exit code, collected into
`[]CheckResult` and summarised by `checksSummary`.

`result.json` is the agent’s report (`uiChanged`, `summary`, `changes`,
`tests`, `screenshots`, `prTitle`, `prBody`, `abort`).

Screenshots are only expected for user-visible UI changes: the agent captures
the changed region before/after, `shots.go` stacks them (before above after) with a
divider, uploads through GitHub’s user-attachments endpoint, and the result is
embedded in the PR and the comment. Backend/docs/test changes must not produce
screenshots.

UI verification is tool-agnostic on purpose: prompts ask for real browser or
desktop automation (Playwright, cua-driver, the repo’s own E2E harness — whatever
is available) and never a specific API, so desktop apps and repositories without
Playwright can still prove a UI change. The agent exercises the changed control
and checks the console before writing `result.json`.

## 11. GitHub client and labels (`gh.go`)

`NewGH` builds an `http.Client` with the configured proxy, resolves the bot
token (env → token file → legacy hosts file, never the human’s), and exposes
typed helpers for the REST endpoints the
daemon needs. Retries: 429/5xx ×3 with backoff; a 401 from a refreshable token
file triggers one refresh and retry; other 4xx return `*APIError` (use
`isStatus`). Logging of every request is DEBUG level. The release checker
reads tags (`/repos/{repo}/tags`), compares a tag with the default branch
(`/repos/{repo}/compare/{base}...{head}`) or lists the branch’s commits when
there is no tag yet, and publishes with `POST /repos/{repo}/releases`
(`tag_name`, `name`, `body`, `target_commitish`, never a draft).

Hooks are created per repo (`hookEvents`): `issues`, `issue_comment`,
`reaction`, `pull_request_review`, `pull_request_review_comment`. When webhooks
are unavailable the poller is the fallback; both funnel into `Router.Handle*`.

## 12. Git and worktrees (`git.go`)

- Managed clone per repo under `repos/`; `git fetch` before every plan and
  implementation.
- Implementation worktree: `worktrees/<owner>-<repo>/issue-<n>-<short id>` on
  branch `agent/issue-<n>-<short id>`; removed with `--force` after merge or
  cancel.
- Commits use the bot identity (`<id>+<login>@users.noreply.github.com`).
- Push uses a one-shot `http.extraheader` with the token, built into
  `LocalRepo.Env`; nothing is written to any gitconfig.
- `plan` uses a temporary detached worktree under `tmp/`.

## 13. Coding agents (`runner.go`)

Registry: `opencode`, `claude`, `codex`, `pi`, `omp`; chosen per repo via
`codingAgent`, model via `model`. `Runner.Run` starts the CLI in the worktree,
streams the log file, enforces the stage timeout and parses the JSON event
stream for the final text and session id. Sessions are reused across
implement → revise → CI repair so the agent keeps context. Prompts are never
passed through the shell; they are a single argv entry. Pi and omp share one
JSONL parser (`piJSONAccumulator`): both emit a session header first, then
`message_end` events carrying the assistant text, and `auto_retry_end` with
`finalError` on a failed retry cap. Pi is always run with `--session-id`
(a fresh UUID, then the stored id): its `--resume` opens the interactive
session picker in print mode instead of resuming. omp has no `--session-id`
flag, so a fresh run takes its id from the session header and later stages
continue it with `--resume`. `agent_end` is one JSON line holding every message
of the run, so `runProcess` never stops at an oversized record: the line is cut
at 8MB, drained and the scan continues, and a truncated record is logged as a
WARN. A pi/omp run that decodes no JSON event at all fails the stage even when
the CLI exited 0 (`piStreamError`), because `omp --resume` can fall back to the
interactive session picker; text is not required, so a terse agent still
passes.

The streaming runners (opencode, pi, omp) also arm a stall watchdog:
`runProcess` records when the newest stdout or stderr line arrived and cancels
the run when none arrives for `runnerStallTimeout` (15 minutes), then kills the
process group before draining. That is the fix for an agent stuck in a tool
call (a Playwright click that never resolves): the total stage timeout alone
would leave the issue at `agent:working` for the whole `implementTimeoutMin`
(60 minutes). A stalled run fails with `coding agent stalled: no output for
15m0s`, which goes through `blockIssue` — phase `blocked`, label
`agent:awaiting-approval` and the usual error comment whose hint is `continue` —
so the owner sees the hang and can resume. The threshold is a constant, not a
config key. claude (`--output-format json` writes one record at exit) and codex
stay exempt: for them a long generation looks exactly like a hang.

## 14. Config

Parsed in `config.go`, documented in DEPLOY.md §4.1. Defaults that matter to
the code: `pollIntervalSec` 30, `webhook` true, `maxFixRounds` 3 (local checks
**and** CI repair rounds), `planParallel` 5, `implementParallel` 1,
`planTimeoutMin` 15, `implementTimeoutMin` 60, `webPort` 0 (off), `selfUpdate`
false, `updateProxy` empty (direct downloads only; a wrong or unreachable
mirror is ignored), `worktreeRoot` = state dir, `language` `auto` (the coding
agent writes in the issue's or PR's language; a language tag forces one and is
passed to the prompts as `{{LANGUAGE}}`), `mode` `auto` (`plan` always posts a
plan comment, `build` implements directly, `auto` lets the coding agent triage;
an unknown mode is rejected at load time). `repos` also accepts an object for
per-repo `codingAgent`/`model`/`approvers`/`worktreeRoot`/`language`/`mode`/
`release`.

`release` is an optional per-repo block — `enabled` (false) and `intervalDays`
(1). The bump part is not a config key: `release.go` bumps minor when the batch
contains a `feat`/`feature` commit subject and patch otherwise, and never
publishes a major automatically. The newest `vMAJOR.MINOR.PATCH` tag is the
version source; a repo without one publishes `v0.1.0` first (the bump rule only
applies afterwards). Older files may still carry `bugBump`/`featureBump`; the
decoder ignores them and a dashboard save drops them. Publishing creates the tag
and the release in one call (`target_commitish` = default branch), so the token
needs `contents: write`, which the push-capable bot token already has.

The dashboard's settings panel edits the same block: `/api/config` sends the
one-day default as `defaults.release` and renders one row per repo with the
switch and the interval; the PUT rejects `intervalDays` below 1 (or a
non-integer) before the temp-file `loadConfig`, instead of letting the loader
clamp the value, and strips the removed bump keys. A row left off with the
defaults is removed from the saved file. `repos` may be an array of names, an
array of objects (`{"repo": "owner/name", ...overrides}`) or a keyed object; the
panel keeps the existing shape and the overrides of repos that stay.

A valid `config.json` save triggers a graceful re-exec: jobs finish, state is
saved, `syscall.Exec` replaces the process (same pid). Under `-embedded` the
process exits with 75 instead, because the desktop shell owns the child handle
and starts it again.

## 15. Web dashboard

Loopback only. Routes: `/`, `/api/state`, `/api/issue`, `/api/logs`,
`/api/action` (approve/cancel), `/api/open-url` (open a dashboard link in the
system browser), `/api/auth` (login status), `/api/auth/device` (POST starts
Device Flow, GET polls), `/api/auth/token` (paste a PAT), `/api/config` (GET raw
file + defaults, PUT
validated save), `/api/detect` (installed agents, model list, optional proxy and
repo-access probes), `/api/restart`, `/api/update` (GET checks
`releases/latest`, POST installs). The mutating routes require
a JSON content type and a loopback `Origin`; `/api/config` runs
`loadConfig` against a temp file before it writes, so an invalid agent, repo,
port, proxy, mirror, release interval or bump returns 400 and nothing is saved.
`/api/open-url` accepts only absolute http(s) URLs and starts the platform
helper (`open`/`xdg-open`/`rundll32`) without a shell. Queue depth in
`/api/state` includes `plan`, `work` and `ci`. `waiting` counts tasks whose phase
needs the approver (`awaiting_approval`, `pr_open`, `blocked`, see
`phaseNeedsUser`); the board can be filtered by phase, by project (the repo
dropdown) and by free text.

The page is a client-rendered SvelteKit SPA in `frontend/` (Svelte 5 runes,
Tailwind v4, shadcn-svelte CSS tokens). `npm run build` writes it to
`cmd/gh-issue-killer/web/dist`, which `web.go` embeds with
`//go:embed all:web/dist`; `dist` is not committed, so a release build must run
the frontend build first (CI and the release workflow do). `web.go` serves
`index.html` with `no-store`, hashed files under `/assets/` with an immutable
cache, other static files (for example `/logo.svg`) for an hour, and falls back
to `index.html` for unknown non-asset paths. A missing `/assets/*` file is a
404, and so is an unknown `/api/*` route. If `dist` contains no `index.html`
the server returns a page that explains how to build the frontend.

The layout is intentionally a two-row sticky masthead above a board: brand and
primary actions (settings, GitHub, language, theme) on the first row, the live
counters on the second. Version, pid, identity, transport and the config path
moved into the settings dialog's System card so the header stays quiet. The
masthead switches between Chinese (default) and English (`localStorage` key
`ghik-lang`) and links to the project on GitHub. The theme toggle (`ghik-theme`)
offers auto/light/dark: `auto` follows
`prefers-color-scheme`; `theme.svelte.ts` flips the `.dark` class and the
`<meta name="color-scheme">` hint together. `app.css` declares the palette once
with `light-dark()` and switches `color-scheme` on `<html>`, so a token has a
single definition and native controls (scrollbars, checkboxes) follow the same
theme. Without JS a `prefers-color-scheme` fallback still picks the dark
scheme. Every external link goes through `openExternal`: a normal browser gets
`window.open`, while the desktop shell loads the page with `?surface=desktop`
and the click is posted to `/api/open-url`, because Wails' WKWebView drops
`target="_blank"`. Every string, including phases, filters, the drawer, the
settings panel and the empty states, lives in the `zh`/`en` tables in
`$lib/i18n.svelte.ts`; `/api/logs` stays plain text and the console colours
INFO/WARN/ERROR/DEBUG in the browser.

The board toolbar merges the phase filters, the hide-finished toggle, the repo
dropdown, the search field and the visible task count into one wrapping row.
The task table keeps semantic `<table>`/`<thead>`/`<th scope="row">` markup and
a sticky header inside its scroll region; the scroll region is a named
container, so below 760 px the same table becomes stacked cards with the
column labels injected from CSS variables (translated by the current language)
instead of shrinking the columns. Repo overview (LINES) and the console are
collapsible cards: the first screen keeps only the counters and the board, and
the console still polls `/api/logs` only after it is expanded.
`planParallel` and `implementParallel` size each repository's own pool, so the
PLAN/WORK meters are rendered per repository line rather than globally. A board
toggle hides `done` and `cancelled` tasks on top of the other filters; it is on
by default, so the board opens on active work.

The task drawer and the settings panel are native `<dialog>` elements opened
with `showModal()`: the browser owns focus containment, `Esc`, focus return and
the top layer, and `dialog.ts` only adds the outside-click fallback where
`closedby="any"` is missing (WebKit in the desktop shell). `app.css` also
contains scroll chaining for the top layer. The settings button opens the
dialog with a section nav (login / config / updates), a scrolling body and a
sticky action bar: repos (array, array of objects or object form, with per-repo
overrides preserved), one release row per repo (`enabled`, `intervalDays`),
coding agent (populated from `/api/detect`), model, proxy, `webPort`,
`selfUpdate` and `updateProxy`, plus the version card, the update buttons and
the System card. The first block is the GitHub login: it shows the one-time
code and authorization link, polls `/api/auth/device`, and keeps a paste-token
fallback; a successful login writes `~/.gh-issue-killer/token` and restarts
into the engine. When `/api/state` reports `configError` (first run, missing
token or a broken edit) the panel opens by itself and the save button starts
the real engine.

## 16. Reload and self-update

`watchConfig` watches the config file with fsnotify plus a 30 s digest safety
poll and a 500 ms debounce; invalid edits are ignored with a warning.
`watchMaster` (`followMaster: true`) is the development counterpart of release
updates: it polls `master` every 30 s, rebuilds in a dedicated clone under
`$STATE/self` (frontend build included, `npm ci` only when the lockfile moves)
and fires a restart; it takes precedence over `selfUpdate`. The
dashboard's PUT reuses the same loader and triggers the same reload, as do
`/api/restart` and a finished update. Both end in `reexec`, so the pid and the
lock stay valid; interrupted implementations resume from their worktree on the
next startup scan. Under `-embedded`, `reexec` exits with 75 and the desktop
shell restarts the engine with the same `-config` / `-web-port`.

`watchSelfUpdates` polls `releases/latest` (every 30 min, unauthenticated GitHub
API limits) and only acts when the tag parses and is newer than `version`; a
`dev`/local build never auto-installs (use the dashboard button). The installer:

1. picks `gh-issue-killer_<tag>_<goos>_<goarch>.tar.gz` (`.zip` on Windows); the
   `gh-issue-killer_` prefix keeps the desktop bundles out;
2. downloads `checksums.txt` and refuses to continue when it has no entry for
   the asset or the sha256 does not match — the running binary is untouched;
3. extracts only a regular file named `gh-issue-killer` (`gh-issue-killer.exe`);
4. renames it over the executable, or `.old` first on Windows where a running
   image cannot be replaced;
5. signals a restart. Release downloads try github.com directly and retry
   `<updateProxy>/<url>` when that fails; the API lookup only uses `proxy`.

In the desktop bundle this replaces the engine binary inside
`gh-issue-killer.app/Contents/MacOS/` (or next to `gh-issue-killer-desktop.exe`)
and the shell restarts it; the thin shell keeps its version. Replacing the file
invalidates a macOS signature, so a
`xattr -dr com.apple.quarantine` / re-download is needed if Gatekeeper refuses a
locally modified bundle.

The release workflow (`v*` tag) builds the daemon archives for Linux, macOS and
Windows, the desktop bundles per runner (macOS ships both a zip and a DMG, both
listed in `checksums.txt`), and publishes `checksums.txt` with
everything. A failed build in the desktop matrix blocks the release. The macOS
arm64 bundle comes from `macos-15` (the `macos-14` image was retired); the Linux
leg runs on `ubuntu-24.04` because Wails v3 links GTK4 and webkitgtk-6.0, which
Ubuntu 22.04 does not package. Linux ships only the tarball — the AppImage step
was best-effort and added a second toolchain for no extra coverage.

## 17. Testing

Unit tests live next to the code (`*_test.go`), all with in-package fakes —
`newFakeAPI` (`gh_test.go`) provides a `GH` pointed at `httptest`, `NewSilentLogger`
keeps tests quiet. Notable coverage:

| file | covers |
| --- | --- |
| `waitforchecks_test.go` | verdict, fail-fast, stuck, required checks, base flags, approval, log report |
| `conflict_test.go` | conflict detection, merge-API error classification, `conflictedFiles` on a real repo |
| `cirepair_test.go` | worktree-ahead detection used to publish a stranded repair commit |
| `review_test.go` | review command parsing, `review.json` loading, inline filtering, fallback body, review API payload |
| `modifypr_test.go` | which PR heads can be pushed (same repo, fork with allow-edits-by-maintainers) |
| `comment_edit_test.go` | edited comments are reprocessed, plain repeats stay deduped |
| `approve_phase_test.go` | 👍 and merge move the phase as soon as they are accepted |
| `messages_test.go` | scaffolding catalogs, `app.text` resolution, language detection |
| `reactions_test.go` | polled 👍 on the review prompt (approver only, consumed once) |
| `blocked_ci_test.go` | green-check detection, auto-resume of CI-blocked tasks, other reasons untouched |
| `router_ci_test.go` | the CI queue does not block work; cancel stops CI jobs |
| `gh_test.go` | label transitions and GitHub helpers |
| `release_test.go` | version tag parsing, bump rules, commit classification, due check, release request body, disabled/not-due/no-commit skips |
| `main_test.go` | proxy/env parsing, repo normalization (names, objects, object arrays), mentions and commands, closable phases |
| `prompts_test.go` | every embedded prompt loads and keeps its placeholders |
| `triage_test.go` | mode normalization, plan/build routing, triage answer parsing, explicit plan requests, direct-build start |
| `runner_test.go` | per-agent argv construction, overlong-record draining, stage timeout and stall kill |
| `shots_test.go` | before/after composition and screenshot markdown |
| `plan_test.go` | plan text trimming |
| `watch_test.go` | config reload fires on valid edits, ignores invalid ones |
| `watcher_test.go` | `Store` round-trip |
| `web_test.go` | dashboard snapshot only lists tracked tasks, `/api/open-url` validation and browser handoff, embedded SPA serving (index, hashed assets, cache headers, 404s, SPA fallback, unbuilt-frontend hint) |
| `oauth_test.go` | Device Flow pending/slow_down/expired, token file 0600, refresh success/failure, credential precedence, 401 retry, login/paste endpoints, login panel markup |
| `openurl_test.go` | per-platform browser command, no shell, injected runner |
| `webconfig_test.go` | `/api/config` validation/round-trip/origin check, `release` defaults and rejection, `/api/detect` degradation, `/api/update` payload, snapshot without a router |
| `selfupdate_test.go` | version ordering, asset selection, mirror/digest parsing, archive extraction, checksum mismatch keeps the binary |
| `wizard_test.go` | `-init` setup wizard flows |
| `logs_test.go`, `logging_test.go` | log rotation, follower, daemonize helpers |

Add a test for every new branch in the gate, a new command keyword, a new
phase transition, or a scheduler invariant. `go test ./...` and `go vet ./...`
must stay clean; `gofmt -l .` must print nothing.

The dashboard is tested with its own toolchain: `npm --prefix frontend run check`
(svelte-check) and `npm --prefix frontend run build`. Before changing styles,
layout, tables, dialogs or theme handling, search the vendored
`.agents/skills/modern-web-guidance/` skill (for example
`npx -y modern-web-guidance@latest search "responsive data table"` then
`retrieve "responsive-table"`): its guides are the local standard and keep the
dashboard free of a UI dependency. Any change to `frontend/`
or to the dashboard handlers in `web.go` must also be driven with real browser or
desktop automation (Playwright, cua-driver, the repo’s own E2E setup — whatever
is available) before it is called done — open the page against a
running engine, exercise the changed component (both themes when colours
changed), read the browser console, and keep the before/after screenshots. A
green unit check does not cover the UI; skipping this step is not allowed.

## 18. Invariants and pitfalls

- One daemon per machine (lock file). Multiple daemons corrupt `state.json`.
  The desktop app runs the same engine, so it takes the same lock: never run
  the desktop app and the background daemon together.
- Conflicts are resolved by merging the base into the PR branch, never by
  rebasing or force-pushing; the merge commit is what keeps the branch honest.
- `Store` is the only writer of state; stages mutate it through
  `UpdateIssue`.
- Never run two agents in one worktree: the merge path waits for the CI job,
  and cancel/close waits for both queues.
- A prompt placeholder (`{{NAME}}`) must exist in the prompt and be provided by
  the caller, or the agent sees the raw token.
- Prompts are embedded (`prompts/embed.go`); a new file must match `*.md` and be
  added to `prompts_test.go` and to the doc.
- User-facing scaffolding never hardcodes a language: every heading, hint and
  footer goes through `app.text(number, "key")`, with the key present in both
  `scaffoldZH` and `scaffoldEN` in `messages.go`. The language comes from
  `language` (explicit) or from the issue/PR text (`detectLanguage`).
- GitHub check runs are not the same as workflow runs: check runs come from
  `/commits/{sha}/check-runs`, job logs from `/actions/jobs/{id}/logs` (the id
  is in the check run’s `details_url`).
- `action_required` means “waiting for maintainer approval”, not “failed”: it
  is approved through `/actions/runs/{id}/approve`.
- Never `pkill -f` a pattern from a prompt: the prompt is in the agent’s own
  argv.
- POSIX-only calls do not belong in shared files: every signal, process group,
  `exec` and shell invocation goes through `process_unix.go` /
  `process_windows.go`, or `GOOS=windows go build ./cmd/gh-issue-killer` breaks.

## 19. Keeping this document in sync

| when you change… | update |
| --- | --- |
| a phase, its transitions, or `IssueState` fields | §5 |
| issue routing (`mode`, auto triage, plan vs build) | §5, §8, §14 and DEPLOY.md §4.1 |
| a command keyword, 👍 handling, lifecycle rules | §6 |
| the PR review command, `review.md`, review posting | §6, §8 and §17 |
| the PR modify flow, `modify_pr.md`, push-to-head rules | §6, §8 and §17 |
| thumbs-up semantics (`ReviewCommentID`) | §6 and §8 |
| queue stages, worker counts, job keys, cancel/wait rules | §7 |
| a stage’s steps or post conditions | §8 |
| the CI gate, stuck detection, repair loop, `ci_fix` prompt, CI auto-resume | §9 |
| conflict detection, `conflict_fix` prompt, merge retry rules | §8 and §9 |
| `checks.json` / `result.json` / screenshot rules | §10 |
| new GitHub endpoints, labels, hook events | §11 |
| worktree layout, branch naming, commit/push | §12 |
| agent CLIs, sessions, timeouts | §13 |
| a config key or default | §14 and DEPLOY.md §4.1 |
| the periodic release checker, version bumps, release notes | §7, §14 and §17 |
| a user-facing comment string | §18 (goes through `app.text` and `messages.go`) |
| web routes or API payloads | §15 |
| the dashboard frontend, its build output or `web.go` static serving | §1, §4, §15 and §17 |
| dashboard design tokens, layout primitives or the vendored modern-web-guidance skill | §15, §17 and AGENTS.md “Dashboard design work” |
| the login flow, token file or credential precedence | §3, §11, §15 and DEPLOY.md §2 |
| reload/self-update behaviour | §16 |
| new or renamed test files | §17 |
| startup order, flags, daemonizing, `-embedded`, platform process helpers | §3 and §1 |
| web routes or API payloads | §15 |
| the dashboard frontend, its build output or `web.go` static serving | §1, §4, §15 and §17 |
| the login flow, token file or credential precedence | §3, §11, §15 and DEPLOY.md §2 |
| the desktop shell, packaging, release assets | §1, §4, §15 and §16 |
| the file map itself | §4 |

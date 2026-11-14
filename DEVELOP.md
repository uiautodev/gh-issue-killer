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
pre-commit install    # optional: gofmt, vet, test, build, 1 MB file guard
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
`gh` account while the daemon uses `gh-agent`.

## 2. Runtime layout

Everything lives under `~/.gh-issue-killer` (`stateDir()` in `util.go`):

| path | contents |
| --- | --- |
| `state.json` | the single source of task state (`state.go`), atomically rewritten |
| `daemon.lock` | pid of the running daemon; `-stop`/`-status` read it |
| `logs/daemon.log` | rotating daemon log (10 MB × 5) |
| `repos/<owner>-<repo>/` | managed clone per watched repository |
| `worktrees/<owner>-<repo>/issue-<n>-<id>/` | one implementation worktree per issue |
| `tmp/` | detached plan worktrees and self-update clones |
| `runs/<owner>-<repo>/issue-<n>/` | per-run artifacts: `plan-*.log`, `implement-*.log`, `revise-*.log`, `ci-fix-*.log`, `checks.json`, `checks/`, `result.json`, `screenshots/` |
| `self/` | self-update build cache |

Only the daemon writes `state.json`; the web UI and CLIs read it. Treat
`Store` as the single writer (see §13).

## 3. Startup sequence (`main.go`)

1. Flags; a bare run daemonizes itself (`startBackground` child mode).
2. `ensureDirs`, logger, `loadConfig`, `parseProxy`.
3. `acquireLock` on `daemon.lock` — one daemon per machine.
4. `NewGH`: reads the `gh-agent` token and identity (never the human account).
5. For each repo: `GetRepo`, `resolveRepo` (managed clone under `repos/`),
   `EnsureLabels`, build the child env (proxy + `GH_CONFIG_DIR`).
6. `LoadStore`, `NewRouter` (queues + worker goroutines), optional web server.
7. `router.StartupScan`: resumes interrupted work, applies pending closes,
   and picks up issues that already mention the agent.
8. `watchConfig` (fsnotify + debounce + 30 s safety poll) and, when
   `selfUpdate` is on, `watchSelfUpdates`.
9. Smee webhook per repo (`ensureSmeeHook`) plus `startPoller`.
10. `SIGINT`/`SIGTERM` → wait for `router.Idle()` → shut down → `reexec` for
    config reloads and self-updates (pid is kept, so the lock stays valid).

## 4. Package map

All Go code is `package main` in `cmd/gh-issue-killer`; `prompts` embeds the
Markdown prompts; `web` embeds `index.html` and `logo.svg`.

| file | responsibility |
| --- | --- |
| `main.go` | flags, startup order, daemonize, lock, shutdown/reexec |
| `config.go` | config parsing, normalization, per-repo defaults |
| `router.go` | event handling, commands, state machine, job scheduler (`plan`/`work`/`ci` queues), startup scan |
| `actions.go` | the stages: plan, implement, revise, CI settle, merge, and their helpers |
| `checks.go` | `checks.json` detection/loading/execution and the summary line |
| `gh.go` | GitHub REST client: issues, comments, PRs, labels, reviews, check runs, workflow runs/jobs, hooks, asset uploads |
| `git.go` | managed clone, worktrees, commit/push, diff/stat, env |
| `runner.go` | coding-agent registry (`opencode`, `claude`, `codex`) and process/JSON output handling |
| `state.go` | `Store`, `IssueState`, `RepoState`, atomic persistence |
| `watcher.go`, `watch.go` | config reload (fsnotify + digest polling) |
| `selfupdate.go` | rebuild the binary when `master` moves, then restart |
| `logs.go` | rotating log file, `-logs` follower |
| `shots.go` | screenshot collection, before/after composition, upload |
| `proxy.go` | SOCKS5/HTTP proxies for HTTP clients, git and child processes |
| `wizard.go` | `-init` interactive setup |
| `web.go`, `web/` | loopback dashboard and its JSON API |
| `util.go` | paths, logger, atomic writes, `tailString`, small helpers |
| `prompts/*.md` | stage prompts (embedded via `prompts/embed.go`) |

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
| `合并`, `merge`, `ship` | merge |
| `cancel`, `stop`, `停止` | cancel |
| `review`, `review pr`, `cr`, `审查` (the target is a pull request) | review the PR and post a GitHub review |
| `<approval word> <free text>` (`ok`, `yes`, `go`, …) | approve **and** forward the rest as `ExtraNotes` to the implementation |

Other rules:

- A mention on an issue in phase `new`/empty starts planning; `bug`-labelled
  issues skip the plan and go straight to implementation with reproduction
  instructions (`bugInstructions`). A mention on a pull request never plans.
- `review` is the only command that targets a PR directly. It works for any PR
  in a watched repo, not only the daemon's own, and runs on the plan queue with
  its own job key (`<repo>#<pr>#review`).
- A non-command comment that mentions the agent, an inline review comment or a
  review body on an open PR becomes `doRevise` (feedback on the same branch).
- Closing an issue cancels the task and closes the PR; reopening resumes. A
  reopened `done` issue starts from `new`.
- 👍 on the stored `PRCommentID` merges, but only while phase is `pr_open`.

## 7. Job scheduler (`router.go`)

Per repository there are three queues, each with its own worker goroutines:

| stage | workers | used for |
| --- | --- | --- |
| `stagePlan` | `planParallel` (5) | planning (detached worktree) |
| `stageWork` | `implementParallel` (1) | implement, revise, merge |
| `stageCI` | `ciWorkers` (2) | post-push CI settle, including repairs |

Rules that keep it safe:

- A job key is `<repo>#<issue>` (dedupe: a second command for a busy issue is
  skipped). CI jobs use `<repo>#<issue>#ci`.
- `cancelActive` cancels both keys; `HandleClosed` waits for both before the
  worktree is removed, so cleanup never races a running agent.
- A merge waits for the issue's CI job (`waitCISettle`) before touching the
  worktree. Two agents never edit one worktree at the same time.
- The CI queue exists so a slow gate never occupies the implementation slot;
  the coding stage only enqueues and returns.

## 8. Stages (`actions.go`)

Each stage gets `jobCtx` from the scheduler and writes `RunDir` artifacts under
`runs/<repo>/issue-<n>/`.

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
(`POST /pulls/{n}/reviews`) with up to 10 inline comments. If GitHub rejects a
line that is not in the diff, it retries with the findings listed in the body.
Nothing is ever pushed and the worktree is removed afterwards.

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
`（自动修复未产生改动）` and ends the loop. Exhausting the budget parks the issue
as `blocked` and the message says how many attempts ran.

## 10. Checks, manifest and screenshots

`checks.json` (written by the agent) wins; otherwise `detectDefaultChecks`
picks `go test`/`go vet`, `frontend/*` npm scripts, or `npm test`. Each check
runs with its own timeout, output tail and exit code, collected into
`[]CheckResult` and summarised by `checksSummary`.

`result.json` is the agent’s report (`uiChanged`, `summary`, `changes`,
`tests`, `screenshots`, `prTitle`, `prBody`, `abort`).

Screenshots are only expected for user-visible UI changes: the agent captures
the changed region before/after, `shots.go` composes them side by side with a
divider, uploads through GitHub’s user-attachments endpoint, and the result is
embedded in the PR and the comment. Backend/docs/test changes must not produce
screenshots.

## 11. GitHub client and labels (`gh.go`)

`NewGH` builds an `http.Client` with the configured proxy, reads the `gh-agent`
token (never the human’s), and exposes typed helpers for the REST endpoints the
daemon needs. Retries: 429/5xx ×3 with backoff; other 4xx return `*APIError`
(use `isStatus`). Logging of every request is DEBUG level.

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

Registry: `opencode`, `claude`, `codex`; chosen per repo via `codingAgent`,
model via `model`. `Runner.Run` starts the CLI in the worktree, streams the
log file, enforces the stage timeout and parses the JSON event stream for the
final text and session id. Sessions are reused across implement → revise →
CI repair so the agent keeps context. Prompts are never passed through the
shell; they are a single argv entry.

## 14. Config

Parsed in `config.go`, documented in DEPLOY.md §4.1. Defaults that matter to
the code: `pollIntervalSec` 30, `webhook` true, `maxFixRounds` 3 (local checks
**and** CI repair rounds), `planParallel` 5, `implementParallel` 1,
`planTimeoutMin` 15, `implementTimeoutMin` 60, `webPort` 0 (off), `selfUpdate`
false, `worktreeRoot` = state dir. `repos` also accepts an object for per-repo
`codingAgent`/`model`/`approvers`/`worktreeRoot`.

A valid `config.json` save triggers a graceful re-exec: jobs finish, state is
saved, `syscall.Exec` replaces the process (same pid).

## 15. Web dashboard

Loopback only. Routes: `/`, `/api/state`, `/api/issue`, `/api/logs`,
`/api/action` (approve/cancel; requires JSON content type and a loopback
origin), `/logo.svg`. Queue depth in `/api/state` includes `plan`, `work` and
`ci`.

## 16. Reload and self-update

`watchConfig` watches the config file with fsnotify plus a 30 s digest safety
poll and a 500 ms debounce; invalid edits are ignored with a warning.
`watchSelfUpdates` polls `master`, rebuilds in a throwaway clone, and signals a
restart. Both end in `reexec`, so the pid and the lock stay valid; interrupted
implementations resume from their worktree on the next startup scan.

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
| `router_ci_test.go` | the CI queue does not block work; cancel stops CI jobs |
| `gh_test.go` | label transitions and GitHub helpers |
| `main_test.go` | proxy/env parsing, repo normalization, mentions and commands, closable phases |
| `prompts_test.go` | every embedded prompt loads and keeps its placeholders |
| `runner_test.go` | per-agent argv construction |
| `shots_test.go` | before/after composition and screenshot markdown |
| `plan_test.go` | plan text trimming |
| `watch_test.go` | config reload fires on valid edits, ignores invalid ones |
| `watcher_test.go` | `Store` round-trip |
| `web_test.go` | dashboard snapshot only lists tracked tasks |
| `wizard_test.go` | `-init` setup wizard flows |
| `logs_test.go`, `logging_test.go` | log rotation, follower, daemonize helpers |

Add a test for every new branch in the gate, a new command keyword, a new
phase transition, or a scheduler invariant. `go test ./...` and `go vet ./...`
must stay clean; `gofmt -l .` must print nothing.

## 18. Invariants and pitfalls

- One daemon per machine (lock file). Multiple daemons corrupt `state.json`.
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
- GitHub check runs are not the same as workflow runs: check runs come from
  `/commits/{sha}/check-runs`, job logs from `/actions/jobs/{id}/logs` (the id
  is in the check run’s `details_url`).
- `action_required` means “waiting for maintainer approval”, not “failed”: it
  is approved through `/actions/runs/{id}/approve`.
- Never `pkill -f` a pattern from a prompt: the prompt is in the agent’s own
  argv.

## 19. Keeping this document in sync

| when you change… | update |
| --- | --- |
| a phase, its transitions, or `IssueState` fields | §5 |
| a command keyword, 👍 handling, lifecycle rules | §6 |
| the PR review command, `review.md`, review posting | §6, §8 and §17 |
| queue stages, worker counts, job keys, cancel/wait rules | §7 |
| a stage’s steps or post conditions | §8 |
| the CI gate, stuck detection, repair loop, `ci_fix` prompt | §9 |
| conflict detection, `conflict_fix` prompt, merge retry rules | §8 and §9 |
| `checks.json` / `result.json` / screenshot rules | §10 |
| new GitHub endpoints, labels, hook events | §11 |
| worktree layout, branch naming, commit/push | §12 |
| agent CLIs, sessions, timeouts | §13 |
| a config key or default | §14 and DEPLOY.md §4.1 |
| web routes or API payloads | §15 |
| reload/self-update behaviour | §16 |
| new or renamed test files | §17 |
| startup order, flags, daemonizing | §3 and §1 |
| the file map itself | §4 |

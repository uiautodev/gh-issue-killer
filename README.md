# gh-issue-killer

Watch GitHub issues, let a coding agent (opencode, Claude Code, or Codex) plan → implement → test → screenshot → PR → squash-merge them, driven entirely from issue comments.

```
issue @agent / assigned        plan comment
owner: @agent lgtm       →     worktree + code + unit/E2E checks + before/after screenshot + PR
owner: @agent <feedback>  →     keep working on the open PR
owner: @agent 合并        →     wait CI → squash merge → close issue/PR → clean worktree
owner: @agent cancel     →     abort, keep the issue open
```

> Running it for real? [DEPLOY.md](DEPLOY.md) is a step-by-step deployment
> runbook written for an AI operator: preflight checks, agent account and
> token, non-interactive config, verification, upgrades, failure → action.

## Deploy it without reading anything

Copy this into Claude Code, Codex or opencode **inside this repository**. It
follows `DEPLOY.md`, picks every default itself, and asks at most one question.

```text
Deploy gh-issue-killer on this machine by following DEPLOY.md §0–§5 (skip §6
unless I ask for boot persistence). Work autonomously and ask at most one
question, only if a value truly cannot be inferred.

Defaults, use them without confirming:
- repos: the origin remote of this git checkout; if there is none, ask me once
- approver: my GitHub login from `gh api user --jq .login`
- codingAgent: the first of opencode, claude, codex found on PATH
- model: the CLI default (leave it empty)
- proxy: $https_proxy or $ALL_PROXY if set, otherwise none
- binary: ~/.local/bin/gh-issue-killer, config: ~/.config/gh-issue-killer/config.json

Do not create GitHub accounts or tokens. Granting this bot push/admin on the
repos above with my gh is allowed (DEPLOY.md §2). If no gh-agent credential
exists (~/.config/gh-agent or $GH_AGENT_TOKEN), stop after printing the exact
single command I must run, then continue from §2.

Finish with the §10 checks and paste the status lines. Do not ask me to confirm
steps you can verify yourself.
```

## Why Go, no curl/ffmpeg

The daemon is a single static Go binary. It talks to GitHub over `net/http`, uploads screenshots through GitHub's user-attachments endpoint with a plain request body, composes before/after images with `image/draw`, and implements the SOCKS5 client itself. At runtime it only shells out to the tools that are inherently part of the job:

- `git` — clone / worktree / push
- one coding-agent CLI — `opencode`, `claude` (Claude Code), or `codex` (Codex CLI)
- `/bin/sh -c` — the check commands the agent asks the harness to run

No `curl`, no `gh`/`gh-agent` binary, no `ffmpeg`, no ImageMagick.

## Build

```bash
go build -o bin/gh-issue-killer ./cmd/gh-issue-killer
go test ./...
```

Requirements: Go 1.27+, `git`, at least one of `opencode` / `claude` / `codex`, and a gh-agent credential (`GH_AGENT_TOKEN`, `~/.config/gh-agent/hosts.yml`, or the OS keyring via a one-time `gh-agent auth token` lookup at startup).

## Configure

Run the wizard — it detects the coding agents installed on `PATH`, asks for the repos (one per line), model, approvers and proxy (plus optional advanced settings), pre-fills every answer from an existing config, checks gh-agent's permissions per repo, verifies that the proxy can actually reach `api.github.com`, and only writes the file after you confirm:

```bash
./bin/gh-issue-killer -init                                    # writes ./config.json
./bin/gh-issue-killer -init -config ~/elsewhere/config.json    # or another path
```

Starting the daemon without a config, in an interactive terminal, launches the same wizard automatically. `-config` selects a different config path.

Or edit `config.json` by hand (gitignored, copy from `config.example.json`):

```json
{
  "repos": [
    "codeskyblue/weektodo",
    "uiautodev-plugins/adb-connect"
  ],
  "codingAgent": "opencode",
  "model": "magpie/group/deepseek",
  "approvers": ["codeskyblue"],
  "proxy": "socks5h://127.0.0.1:10808"
}
```

| key | required | default | meaning |
| --- | --- | --- | --- |
| `repos` | ✅ | – | `"owner/repo"` list, or an object for per-repo overrides (`{"me/app": {"codingAgent": "claude"}}`) |
| `codingAgent` | | `opencode` | coding-agent CLI: `opencode`, `claude`, or `codex` |
| `model` | | – (CLI default) | optional model passed to the selected CLI |
| `approvers` | | repo owner | logins allowed to say `lgtm` / `合并` |
| `proxy` | | direct | `socks5h://host:port` (remote DNS for git, `socks5://` for Go clients), `socks5://`, `http://`, or `https://` |

Optional: `pollIntervalSec` (30), `webhook` (true), `maxFixRounds` (3), `maxParallel` (1), `planTimeoutMin` (15), `implementTimeoutMin` (60).

Per-repo override: the `repos` object form can set `codingAgent`, `model`, `approvers`, or `worktreeRoot` for one repo.

## Coding agents

The wizard auto-detects installed CLIs and only lists what it finds:

| `codingAgent` | CLI | non-interactive invocation |
| --- | --- | --- |
| `opencode` (default) | opencode | `opencode run --format json --auto [--model] [--session]` |
| `claude` | Claude Code | `claude -p --output-format json --permission-mode bypassPermissions [--model] [--resume/--session-id] --add-dir <runDir>` |
| `codex` | Codex CLI | `codex exec --cd <worktree> --full-auto [--model] [--add-dir <runDir>] --output-last-message <file>` |

`agent` is no longer a config field. It used to select an opencode sub-agent; current configs can still contain it, but the daemon ignores it with a warning and the wizard removes it. Sessions are reused for fix rounds when the CLI supports it (opencode `--session`, Claude `--resume`, Codex falls back to a fresh run with the full context).

## Accounts

The daemon reads the token from `GH_AGENT_TOKEN`, `~/.config/gh-agent/hosts.yml`, or (when the token lives in the OS keyring, which newer gh versions do) asks `gh-agent auth token` once at startup. It only ever acts as **gh-agent**. Your own `gh` is never used by the daemon; it appears only in `scripts/smoke-test.sh`, which simulates you as the issue author/approver.

`gh-agent` needs on each watched repo:

- **Write** — comment, label, push branches, open PRs, squash merge.
- **Admin + `admin:repo_hook` scope** — optional, enables real-time webhooks. Without it the daemon polls every 30 s and logs how to enable webhooks:

```bash
# one-time, as the human:
gh api -X PUT repos/<owner>/<repo>/collaborators/<agent-login> -f permission=admin
# then refresh the agent token (authorize in a private window logged in as the agent):
GH_CONFIG_DIR="$HOME/.config/gh-agent" gh auth refresh -h github.com -s admin:repo_hook
```

> Personal-account repositories cannot grant **Admin** to a collaborator. On those repos, either create the webhook once with the owner account (Settings → Webhooks, pointing at the daemon's smee channel) or leave webhooks off and use polling. The wizard reports which mode each repo will use.

## Real-time webhooks

When the token can manage hooks, the daemon:

1. creates a `smee.io` channel (`GET https://smee.io/new`, no redirect following),
2. creates a repository webhook (`issues`, `issue_comment`) pointing at that channel with a random secret,
3. keeps an SSE long connection to the channel, and
4. **re-fetches every event from the GitHub API before acting on it**, so the relay can never forge a comment, command, or author. The channel URL is the capability; the HMAC is not trusted because the relay re-encodes JSON.

If hook creation fails (403/404) it falls back to the 30 s poller automatically.

## Worktrees

Every watched repo gets its own managed clone at `~/.gh-issue-killer/repos/<owner>-<repo>`. It is cloned once, then `git fetch` is run before every plan/implementation to update `origin/<base>`. Your working clones are never scanned or touched.

Implementation worktrees live at `~/.gh-issue-killer/worktrees/<owner>-<repo>/issue-<n>-<id>` on branch `agent/issue-<n>-<id>`. Planning uses a temporary detached worktree under `~/.gh-issue-killer/tmp/` that is removed afterwards. `worktreeRoot` can override the location.

Git auth uses a one-shot `http.extraheader` with the gh-agent token (never written to any gitconfig); commits are authored as the gh-agent noreply identity.

## Checks, screenshots

The implement agent writes `~/.gh-issue-killer/runs/<repo>/issue-<n>/checks.json`; the daemon executes those commands itself (install / lint / typecheck / unit / e2e) and only ships when they all pass. Failing checks are fed back to the agent for up to `maxFixRounds` fix rounds.

Screenshots are only required for user-visible UI changes. The agent captures just the changed component/region (tight crop, not the full page) in both its base version and its changed version; the daemon composes them side by side into one image (12 px white gap, padded to a common height, pure `image/draw`) and uploads it via GitHub's user-attachments endpoint. Backend/docs/test-only PRs get no screenshots and say so explicitly.

## Run

```bash
./bin/gh-issue-killer -init          # guided config.json setup
./bin/gh-issue-killer                # start in the background (default)
./bin/gh-issue-killer -foreground    # attached: logs stream to the terminal, Ctrl+C to stop
./bin/gh-issue-killer -f             # shorthand for -foreground
./bin/gh-issue-killer -logs          # follow daemon.log in real time
./bin/gh-issue-killer -status        # pid, config, log and state paths
./bin/gh-issue-killer -stop          # stop the running daemon and clean the lock
./bin/gh-issue-killer -once          # startup scan + one poll, then exit (smoke check)
scripts/smoke-test.sh <owner/repo>   # full loop using YOUR gh as the human
```

The default is a background daemon; it re-execs itself in a new session, writes the lock, and prints the pid plus the log path. No `nohup`, `launchd`, or `logrotate` is required. `-foreground` keeps the process attached for debugging. The single-instance lock still applies, so run `-stop` first if a copy is already running.

## Logs

The daemon log has a fixed location and rotation:

```
~/.gh-issue-killer/logs/daemon.log     # 10 MB × 5, pure Go rotation
~/.gh-issue-killer/logs/daemon.log.1
...
~/.gh-issue-killer/logs/daemon.log.5
```

The resolved path and rotation policy are printed at startup, in `-status`, and in the `-init` summary. Per-issue coding-agent output lives under `~/.gh-issue-killer/runs/<owner>-<repo>/issue-<n>/` (`plan-*.log`, `implement-*.log`, `fix-*.log`, `checks/*.log`). There is no `daemon.out`; background stdout/stderr go to `/dev/null` and everything goes through the rotating log file.

Logs are written at DEBUG level by default, so `-logs` shows API calls, poll cycles, webhook events and job lifecycle. The viewer colorizes the timestamp and level when stdout is a TTY (INFO green, WARN yellow, ERROR red, DEBUG gray); set `NO_COLOR=1` or redirect the output to keep it plain.

```bash
./bin/gh-issue-killer -logs   # follow daemon.log (last 50 lines, rotation-aware, Ctrl+C to stop)
```

State: `~/.gh-issue-killer/state.json` · logs: `~/.gh-issue-killer/logs/daemon.log` · single instance enforced by `daemon.lock`.

## Commands the daemon understands

Commands must come from an approver, mention the agent, and be the **whole comment** (`@my-agent lgtm`). Words inside a sentence never count, so "把按钮合并到 input-group 里" does not merge anything.

| comment | effect |
| --- | --- |
| `@agent lgtm`, `@agent continue`, `@agent 继续` | approve the plan (or retry after blocked) |
| `@agent 合并`, `@agent merge`, `@agent ship` | squash merge the PR, close issue + PR, clean the worktree |
| `@agent cancel`, `@agent stop`, `@agent 停止` | abort the current task |
| 👍 reaction on the plan comment | same as `@agent lgtm` |
| 👍 reaction on the PR-announcement comment | same as `@agent 合并` |

A daemon restart resumes any interrupted implementation from its worktree automatically. Every approver comment on a tracked issue gets a 👀 reaction. Work adds 🚀 (implement/revision), 🎉 (merged) or 😕 (cancelled). A comment that mentions the agent but is not a command is treated as feedback: on `pr_open` the agent keeps working on the same branch, and reopening a `done` issue starts a fresh plan.

Anyone can `@<agent-login>` an issue (or assign it) to request a plan. The agent never reacts to its own generated comments and never treats its own issues as triggers.

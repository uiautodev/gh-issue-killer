# Deploying gh-issue-killer — runbook for an AI agent

Audience: an AI agent operating this machine (a human can copy-paste the same
commands). Work through the steps in order. Every step ends with a **verify**
command and the output you should see; if it does not match, go to
[Failure → action](#8-failure--action) before continuing.

Markers:

- **ASK** — only when a value cannot be inferred. Prefer a default and say what
  you assumed.
- **ENSURE** — safe to re-run; make the state match.
- **NEVER** — an invariant; violating it breaks the deployment.

Ask the human **at most once**: for the repositories when they cannot be
inferred, or for the bot token/login when no credential exists. Everything else
is detectable.

Behaviour facts you may need: one static Go binary, outbound HTTPS only (no
inbound port), one instance per state directory, and the workflow commands are
`@agent lgtm` / `@agent 合并` / `@agent cancel` (plus 👍 reactions).

## 0. Variables

Set these once in the shell that runs every step. Fill them from §0.1; ask the
human only for `REPOS` when the current checkout has no GitHub origin and for
`AGENT_LOGIN` when no gh-agent credential exists yet.

```bash
AGENT_LOGIN=yourname-agent                       # dedicated bot account
REPOS="your-org/app your-org/api"                # watched repos
BIN_DIR="$HOME/.local/bin"                       # install location
CONFIG="$HOME/.config/gh-issue-killer/config.json"
STATE="$HOME/.gh-issue-killer"
LOG="$STATE/logs/daemon.log"
HB="gh-issue-killer"                             # binary name on PATH
```

## 0.1 Defaults — infer, do not ask

| value | default |
| --- | --- |
| `REPOS` | `git remote get-url origin` of the current checkout, if it is a GitHub repo |
| `AGENT_LOGIN` | `gh-agent api user --jq .login` |
| approver | the human's login from `gh api user --jq .login` |
| `codingAgent` | first of `opencode`, `claude`, `codex` on `PATH` |
| model | empty (CLI default) |
| proxy | `$https_proxy` / `$ALL_PROXY` if set, otherwise none |
| `BIN_DIR` | `$HOME/.local/bin` |
| `CONFIG` | `$HOME/.config/gh-issue-killer/config.json` |

Write down every assumption in the final report instead of asking about it.

## 1. Preflight — ENSURE

```bash
uname -s
go version                                   # need go1.27+
git --version                                # need 2.5+
command -v opencode claude codex             # at least one
[ -n "$GH_AGENT_TOKEN" ] && echo token:env || echo token:unset
```

| result | action |
| --- | --- |
| Go or git missing | install with the platform package manager; if that needs sudo, **ASK** once and include the exact command |
| no coding-agent CLI | install `opencode` by default and say so; **ASK** only if the human has a stated preference |
| token unset | check `~/.config/gh-agent/hosts.yml`; if absent → step 2 (this is the one allowed question) |
| OS not macOS/Linux | stop: unsupported |

The coding agent must work unattended on this machine, as the same user
(`opencode run`, `claude -p`, `codex exec`). If unsure, run its help command;
do not start the daemon until one of them exists.

## 2. Agent identity and access — ENSURE

The daemon acts only as the bot account. The bot must not be the human's own
account, and it must not create its own trigger issues.

```bash
# token source 1: environment (survives into the background process)
export GH_AGENT_TOKEN=...            # NEVER write this into the repo

# token source 2: gh CLI config dir (hosts.yml or OS keyring)
GH_CONFIG_DIR="$HOME/.config/gh-agent" gh auth login
GH_CONFIG_DIR="$HOME/.config/gh-agent" gh auth refresh -h github.com -s admin:repo_hook
```

Required scopes: `repo`, `read:org`; `admin:repo_hook` only for real-time
webhooks (without it the daemon polls every 30 s — acceptable).

The daemon (and `-init`) creates a two-line wrapper at `~/.local/bin/gh-agent`
when none is on `PATH`:

```sh
#!/bin/sh
GH_CONFIG_DIR="$HOME/.config/gh-agent" exec gh "$@"
```

It never overwrites an existing file and never falls back to the human's
`gh auth token`; a missing bot credential is a hard error.

Verify identity and per-repo permission:

```bash
gh-agent api user --jq .login                # must print $AGENT_LOGIN
for r in $REPOS; do gh-agent api "repos/$r" --jq '{repo:.full_name,push:.permissions.push,admin:.permissions.admin}'; done
```

`push:true` is mandatory. If `push:false`, run the grant yourself with the
human's `gh` (setup is allowed to use it; runtime is not). If it fails, **ASK**
once and print the exact command:

```bash
gh api -X PUT "repos/<owner>/<repo>/collaborators/$AGENT_LOGIN" -f permission=admin
```

Notes: personal repos cannot grant Admin to a collaborator — the daemon then
creates no webhook and polls. Organization repos can. Do not grant more repos
than $REPOS.

## 3. Install — ENSURE

```bash
git clone <this-repo> gh-issue-killer && cd gh-issue-killer
go build -o bin/gh-issue-killer ./cmd/gh-issue-killer
go test ./...
install -m 0755 bin/gh-issue-killer "$BIN_DIR/gh-issue-killer"
```

**Verify**

```bash
"$BIN_DIR/gh-issue-killer" -h | head -3      # prints usage
```

Prompts are embedded in the binary, so a copied binary is self-contained. A
`prompts/` directory next to the binary (or in the working directory) still
overrides the embedded copies for development.

## 4. Configure — do not use `-init` in automation

`-init` is an interactive TTY wizard. When driving this from an agent, write
the file directly (idempotent, preserves nothing else):

```bash
mkdir -p "$(dirname "$CONFIG")"
cat > "$CONFIG" <<'JSON'
{
  "repos": ["your-org/app", "your-org/api"],
  "codingAgent": "opencode",
  "approvers": ["your-login"],
  "proxy": ""
}
JSON
```

Rules:

- `repos` holds `owner/repo` strings, or an object for per-repo overrides
  (`{"me/app": {"codingAgent": "claude"}}`).
- `approvers` are human logins allowed to command the bot. Never list the bot.
- `proxy` is optional; `socks5h://host:port` also does remote DNS for git.
- Always pass `-config "$CONFIG"`. Without it the daemon reads `./config.json`
  from its current directory.
- The wizard is still the right tool when a human runs it: it detects agents,
  checks permissions and proxy reachability.

**Verify** (startup scan + one poll, then exit; proves token, repos, CLI):

```bash
gh-issue-killer -once -config "$CONFIG"; echo "exit=$?"
grep -E 'agent identity|repo ready' "$LOG" | tail -5
```

Expected: `agent identity: @<bot>` and one `repo ready:` line per repo.
Non-zero exit or missing lines → see failures.

### 4.1 Configuration reference

| key | required | default | meaning |
| --- | --- | --- | --- |
| `repos` | ✅ | – | `"owner/repo"` list, or an object for per-repo overrides (`{"me/app": {"codingAgent": "claude"}}`) |
| `codingAgent` | | `opencode` | coding-agent CLI: `opencode`, `claude`, or `codex` |
| `model` | | – (CLI default) | optional model passed to the selected CLI |
| `approvers` | | repo owner | logins allowed to command the bot |
| `proxy` | | direct | `socks5h://host:port` (remote DNS for git, `socks5://` for Go clients), `socks5://`, `http://`, or `https://` |

Optional: `pollIntervalSec` (30), `webhook` (true), `maxFixRounds` (3),
`planParallel` (5) and `implementParallel` (1), both **per repository**,
`selfUpdate` (false), `webPort` (0 = off; `8787` serves the dashboard on
127.0.0.1), `planTimeoutMin` (15), `implementTimeoutMin` (60),
`worktreeRoot` (state dir), `replyLanguage` (`auto`).

`replyLanguage` controls the language the coding agent writes in — plans,
summaries, PR bodies and review findings. The default `auto` follows the
language of the issue or pull request. Set it to a language name or tag
(`en`, `zh-CN`, `ja`, …) to force one; the daemon's own fixed comments stay in
Chinese regardless.

Per-repo overrides: the `repos` object form can set `codingAgent`, `model`,
`approvers`, `worktreeRoot`, or `replyLanguage` for one repository.

The running daemon watches the config file with the OS notification API
(inotify / kqueue via fsnotify, debounced, with a 30 s safety poll). A valid save
triggers a graceful re-exec: it waits for running jobs, then replaces itself
with the new config, keeping the PID and the lock. An invalid save is logged
and ignored, so the old settings stay active.

### 4.2 Coding agents

| `codingAgent` | CLI | non-interactive invocation |
| --- | --- | --- |
| `opencode` (default) | opencode | `opencode run --format json --auto [--model] [--session]` |
| `claude` | Claude Code | `claude -p --output-format json --permission-mode bypassPermissions [--model] [--resume/--session-id] --add-dir <runDir>` |
| `codex` | Codex CLI | `codex exec --cd <worktree> --full-auto [--model] [--add-dir <runDir>] --output-last-message <file>` |

`agent` is no longer a config field; an old value is ignored with a warning.
Sessions are reused for fix rounds where the CLI supports it (opencode
`--session`, Claude `--resume`; Codex starts a fresh run with full context).

## 5. Start and verify

```bash
gh-issue-killer -config "$CONFIG"            # background, prints pid + log path
gh-issue-killer -config "$CONFIG" -status
```

**Verify**

```bash
gh-issue-killer -config "$CONFIG" -status | grep -E 'daemon: running|webhook:|repo:'
grep -E 'poller started|smee channel|polling only' "$LOG" | tail -3
```

Definition of done:

- `daemon: running (pid N)`
- every repo listed
- `webhook: smee (polling fallback)` or `polling only`
- one `smee stream connected` per repo, or `polling only` without webhook

`-foreground` runs attached for debugging; the single-instance lock still
applies, so `-stop` first.

## 6. Optional: boot persistence — ASK first

Only if the human wants it. The daemon already self-backgrounds; no `launchd`,
`nohup` or `logrotate` is needed.

```ini
# ~/.config/systemd/user/gh-issue-killer.service
[Unit]
Description=gh-issue-killer
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=%h/.local/bin/gh-issue-killer -foreground -config %h/.config/gh-issue-killer/config.json
Restart=on-failure
RestartSec=10
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
# include the coding agent's directory too (e.g. nvm's node bin)
# Environment=GH_AGENT_TOKEN=...

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now gh-issue-killer
systemctl --user status gh-issue-killer --no-pager
```

macOS: start it once after login, or add it to login items. Nothing else.

## 7. Operations cheat sheet

| task | command |
| --- | --- |
| status / paths | `gh-issue-killer -config "$CONFIG" -status` |
| dashboard | set `webPort` and open `http://127.0.0.1:<port>` (loopback only, no auth; approve/cancel are POST-only with an Origin check) |
| follow logs | `gh-issue-killer -config "$CONFIG" -logs` |
| stop | `gh-issue-killer -config "$CONFIG" -stop` |
| apply a config change | save the file; the daemon reloads in place when jobs finish (same PID) |
| self-update | with `"selfUpdate": true`, poll the project's `master` every 30 s, rebuild 5 s after a change, then re-exec (needs Go and a writable binary) |
| restart after upgrade | stop → rebuild → start |
| smoke test | `gh-issue-killer -config "$CONFIG" -once` |
| full loop with the human's `gh` | `scripts/smoke-test.sh <owner/repo>` |

State layout (all under `$STATE`): `repos/` managed clones, `worktrees/`
per-issue trees (auto-removed after merge/cancel), `runs/` agent logs and
screenshots (safe to prune), `logs/daemon.log*` (10 MB × 5), `state.json`
(tracking; deleting it re-plans open issues), `daemon.lock`.

Upgrade:

```bash
gh-issue-killer -config "$CONFIG" -stop
git pull && go build -o bin/gh-issue-killer ./cmd/gh-issue-killer && go test ./...
gh-issue-killer -config "$CONFIG"
```

Interrupted implementations resume from their worktree on the next start.

## 8. Failure → action

| symptom (log or command) | action |
| --- | --- |
| `no gh-agent token found` | set `GH_AGENT_TOKEN`, or `GH_CONFIG_DIR=~/.config/gh-agent gh auth login` |
| `gh-agent has no push access` | grant collaborator push/admin (step 2) |
| `unsupported codingAgent` / `runnerBinary` error | install that CLI and make sure it is on PATH of the daemon |
| `create webhook: 403/404` | expected on personal repos; polling continues. Do not retry blindly |
| `-status` says `polling only` | no webhook; still functional, 30 s latency |
| `fetch base` / `create worktree` errors | git auth or proxy; re-check scope and `proxy` |
| checks failed, issue `blocked` | inspect `$STATE/runs/<owner>-<repo>/issue-<n>/checks/`; fix in the kept worktree, then `@agent continue` |
| new issue produces nothing | issue must @ the bot or assign it; the author must not be the bot |
| 👍 on the plan does nothing | reaction must be on the plan comment by an approver; the poll checks every 30 s |
| stale `agent:*` labels | only `agent:awaiting-approval` is still managed; delete the leftover old labels by hand in the repo settings |
| `already running (pid N)` | use `-status` / `-stop`; never start a second instance |

## 9. Invariants — NEVER

- NEVER let the daemon run as the human's account, and never use the human's
  `gh` for runtime operations (only for human actions like approving/merging
  and for `scripts/smoke-test.sh`).
- NEVER write the token to `config.json`, a gitconfig, a commit, or a log.
- NEVER run two daemons against the same state directory.
- NEVER delete `state.json` unless the human asks; it resets tracking.
- NEVER assume webhooks exist; polling is the guaranteed path.
- NEVER run `-init` from a non-interactive session; write `config.json`.
- NEVER grant the bot admin on repos outside `$REPOS`.
- Treat `daemon.log` as sensitive: it contains issue text, commands and paths.

## 10. Post-deploy check (one block)

```bash
gh-issue-killer -config "$CONFIG" -status | tee /tmp/ghik-status.txt
grep -q 'daemon: running' /tmp/ghik-status.txt && echo "STATUS ok"
for r in $REPOS; do gh-agent api "repos/$r" --jq '.permissions.push' | grep -q true && echo "ACCESS $r ok"; done
grep -q 'repo ready' "$LOG" && echo "REPOS ok"
grep -Eq 'smee stream connected|polling only' "$LOG" && echo "TRANSPORT ok"
```

All four `ok` lines present → deployment complete. Anything else → step 8.

## Appendix A: internals

**Webhooks.** When the token can manage hooks, the daemon creates a `smee.io`
channel, registers a repository webhook (`issues`, `issue_comment`, `reaction`,
`pull_request_review`, `pull_request_review_comment`)
pointing at it, and keeps an SSE connection open. Every event is re-fetched from
the GitHub API before a command, author or body is trusted, so the relay can
never forge one. Hook failures fall back to polling. Reactions on comments
(👍) and review comments/review bodies are also polled from the API, because
GitHub does not always deliver those events.

**Worktrees.** Each watched repo has a managed clone at
`$STATE/repos/<owner>-<repo>`; `git fetch` runs before every plan and
implementation. Implementation worktrees live at
`$STATE/worktrees/<owner>-<repo>/issue-<n>-<id>` on branch
`agent/issue-<n>-<id>`. Planning uses a temporary detached worktree under
`$STATE/tmp/`. Git auth uses a one-shot `http.extraheader` with the bot token,
never a gitconfig; commits use the bot's noreply identity.

**Checks and screenshots.** The implement agent writes
`$STATE/runs/<repo>/issue-<n>/checks.json`; the daemon runs those commands
itself and only pushes when they all pass. Failing checks are fed back for up
to `maxFixRounds` rounds, then the worktree is kept and the issue is blocked.
PR checks are held to the same bar, and are watched from the moment the PR
opens on a dedicated CI queue, so a slow gate never occupies an implementation
slot; a short "waiting for CI" comment lists the checks. A red or stuck check
(queued on a retired runner, missing Python on the image, `action_required`) is
handed to the agent with the failing job logs, which fixes the workflow or the
code in the same worktree and pushes again; runs waiting for approval are
approved first. The merge invitation is only posted once the checks are green,
and an exhausted budget parks the merge.

A branch that conflicts with the base is not a dead end: `resolveConflicts`
merges the base into the PR branch, the agent resolves the markers with
`prompts/conflict_fix.md`, the local checks run, and the result is pushed and
merged. A conflict that appears at merge time (the base moved after the gate) is
caught from the 405/409 response and resolved the same way.
For user-visible UI changes the agent captures the changed region (tight crop)
before and after; the daemon composes them side by side with a divider and
uploads the image through GitHub's user-attachments endpoint. Backend, docs and
test-only PRs get no screenshots and say so.

**Logs.** `$STATE/logs/daemon.log`, rotated at 10 MB × 5 by the daemon itself
(pure Go). DEBUG level by default; `-logs` follows it and colorizes on a TTY
(`NO_COLOR=1` disables). Per-issue agent output is under
`$STATE/runs/<owner>-<repo>/issue-<n>/` (`plan-*.log`, `implement-*.log`,
`revise-*.log`, `fix-*.log`, `checks/*.log`). There is no `daemon.out`.

**Flags.** `-init` wizard, `-config` path, `-foreground`/`-f` attached,
`-logs` follow, `-status`, `-stop`, `-once` (startup scan + one poll, then exit).

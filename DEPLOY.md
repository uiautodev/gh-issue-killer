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
| Windows | supported: the binary builds and runs there (`process_windows.go`), but `scripts/smoke-test.sh` needs a POSIX shell. Prefer the desktop app below |

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

Prebuilt archives (no Go needed): download the matching
`gh-issue-killer_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) and
`checksums.txt` from
[Releases](https://github.com/uiautodev/gh-issue-killer/releases), verify with
`sha256sum -c` (or `shasum -a 256 -c`), then put the binary on `PATH`.

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

### 3.1 Desktop app — optional

The desktop app is a thin Wails 3 window over the same engine: no separate
daemon, no terminal wizard. Download the bundle for the platform:

| platform | asset | contents |
| --- | --- | --- |
| macOS | `gh-issue-killer-desktop_<version>_darwin_<arch>.zip` or `.dmg` | `gh-issue-killer.app` with `AppIcon.icns` (shell + engine inside `Contents/MacOS/`); the DMG adds an `/Applications` shortcut |
| Linux | `gh-issue-killer-desktop_<version>_linux_<arch>.tar.gz` | `gh-issue-killer-desktop` + `gh-issue-killer` |
| Windows | `gh-issue-killer-desktop_<version>_windows_amd64.zip` | `gh-issue-killer-desktop.exe` + `gh-issue-killer.exe` |

Open the DMG and drag the app onto `/Applications`, or unzip the archive and
launch it. On first run there is no config, so the window opens on the
settings panel: fill in repos, coding agent, model, proxy (and optionally the
update mirror `gh-proxy.com`), save, and the engine restarts with it. Closing
the window stops the engine — nothing is left in the background.
Links in the dashboard open in the system browser, including when the window
is the embedded desktop one.

- Config file: `~/.config/gh-issue-killer/config.json` (`GHIK_CONFIG` overrides).
- State/logs: `~/.gh-issue-killer` — the same as the daemon.
- The engine still takes `daemon.lock`, so do not run the desktop app and the
  background daemon at the same time.
- Updates replace the engine binary inside the bundle (the shell is a thin
  window and rarely changes); the engine restarts into the new version when the
  download and checksum succeed, and the old binary stays on any failure.
- `GHIK_ENGINE` points the shell at a different engine binary (development).
- Build from source: `cd desktop && go build .`, then
  `desktop/build/package.sh vX.Y.Z` (macOS/Linux; macOS also gets a DMG) or
  `desktop/build/package.ps1 -Version vX.Y.Z` (Windows). `wails3` is not needed:
  there is no frontend build, the window loads the engine's dashboard.
- The macOS bundle is not signed or notarised. If Gatekeeper refuses the first
  launch, use Open from the Finder context menu or
  `xattr -dr com.apple.quarantine gh-issue-killer.app`.

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
`selfUpdate` (false), `updateProxy` (empty = direct only), `webPort` (0 = off;
`8787` serves the dashboard on 127.0.0.1), `planTimeoutMin` (15),
`implementTimeoutMin` (60), `worktreeRoot` (state dir), `language` (`auto`).

`updateProxy` is a download mirror prefix for release assets, not an HTTP
proxy: when the direct download from github.com fails, the daemon retries
`<updateProxy>/<original asset url>`. `gh-proxy.com` is the usual value for
mainland China. The GitHub API lookup itself still goes through `proxy`.

`language` controls the language the coding agent writes in — plans,
summaries, PR bodies and review findings — and the daemon's own scaffolding
(headings, hints, merge prompts). The default `auto` follows the language of the
issue or pull request. Set it to a language name or tag (`en`, `zh-CN`, `ja`, …)
to force one. Scaffolding has Chinese and English catalogs; other languages fall
back to English while the agent still writes in the chosen language.

Per-repo overrides: the `repos` object form can set `codingAgent`, `model`,
`approvers`, `worktreeRoot`, `language`, or a `release` block for one
repository.

The running daemon watches the config file with the OS notification API
(inotify / kqueue via fsnotify, debounced, with a 30 s safety poll). A valid save
triggers a graceful re-exec: it waits for running jobs, then replaces itself
with the new config, keeping the PID and the lock. An invalid save is logged
and ignored, so the old settings stay active.

### 4.1.1 Periodic releases (optional, per repo)

```json
{
  "repos": {
    "me/app": {
      "release": {
        "enabled": true,
        "intervalDays": 1
      }
    }
  }
}
```

| key | default | meaning |
| --- | --- | --- |
| `enabled` | `false` | publish releases for this repository |
| `intervalDays` | `1` | minimum days between checks |

The dashboard shows the same block: **Settings** has one row per repository with
the switch and the interval, and saving writes them back into `repos` (keeping
`codingAgent`, `model`, `language` and the other overrides) before restarting
the daemon. A row left off with the default values is not written, so the file
stays free of no-op `release` blocks.

The bump part is not configurable because it follows the conventional commits:
a `feat`/`feature` subject (including `feat(scope):`) bumps the minor part,
anything else bumps the patch part. A **major** release is a deliberate human
decision and is never published automatically — cut the `vX.0.0` tag yourself
when you mean it. The newest `vMAJOR.MINOR.PATCH` tag is the version source. A
repository without a version tag publishes `v0.1.0` as its first release, since
a bump describes a change relative to a published version. A check lists the
commits on the default branch since that tag, bumps once, then creates the tag
and the release on the default branch. The release body lists those commit
subjects and a compare link.

Older configs may still contain `bugBump`/`featureBump`; both keys are ignored
now and a save from the dashboard removes them.

Checks run at startup and then hourly, but at most once per `intervalDays`. A
check with no new commits records the timestamp and publishes nothing; a failed
request is logged and retried on the next tick without moving the timestamp.
`-once` does not run release checks. This is opt-in because it creates public
tags and releases: the first run of a repository covers every commit since its
newest version tag, or the whole history when there is none, and publishes
`v0.1.0`. The token needs `contents: write`, which a push-capable gh-agent
token already has.

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
| self-update | with `"selfUpdate": true`, poll `releases/latest` every 30 min, verify `checksums.txt`, replace the binary and re-exec (no Go toolchain needed) |
| install a release now | dashboard → 设置 → 检查更新 → 立即升级, or download the archive and replace the binary |
| update behind the GFW | set `"updateProxy": "gh-proxy.com"`; release downloads fall back to it |
| release check | `grep 'release' "$LOG"`; a due check publishes a tag + release, a failure logs `release check:` and retries next hour |
| bump the next release | nothing to configure: `feat`/`feature` commits bump minor and everything else patch; tag a major (`vX.0.0`) by hand when intended. The version comes from the newest `v*` tag |
| smoke test | `gh-issue-killer -config "$CONFIG" -once` |
| full loop with the human's `gh` | `scripts/smoke-test.sh <owner/repo>` |

State layout (all under `$STATE`): `repos/` managed clones, `worktrees/`
per-issue trees (auto-removed after merge/cancel), `runs/` agent logs and
screenshots (safe to prune), `logs/daemon.log*` (10 MB × 5), `state.json`
(tracking; deleting it re-plans open issues), `daemon.lock`.

Upgrade (either path keeps `state.json`, so nothing is re-planned):

```bash
# release archive
sha256sum -c checksums.txt --ignore-missing
install -m 0755 gh-issue-killer "$BIN_DIR/gh-issue-killer"
gh-issue-killer -config "$CONFIG" -stop
gh-issue-killer -config "$CONFIG"

# or from source
gh-issue-killer -config "$CONFIG" -stop
git pull && go build -o bin/gh-issue-killer ./cmd/gh-issue-killer && go test ./...
gh-issue-killer -config "$CONFIG"
```

A failed self-update (no asset for the platform, bad checksum, mirror and
GitHub both unreachable) leaves the running binary untouched and logs why.
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
| desktop window says the engine exited | read `~/.gh-issue-killer/logs/desktop-engine.log`; the usual cause is a daemon (or a second desktop) already holding `daemon.lock` |
| update fails with `checksum mismatch` | do not bypass it; the release asset or the mirror is corrupt. Retry, or install the archive by hand after `sha256sum -c` |
| `release check:` warning repeats | check the token's `contents: write` and that `v*` tags are not protected; the next hourly tick retries without moving the timestamp |

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
before and after; the daemon stacks them (before on top) with a divider and
uploads the image through GitHub's user-attachments endpoint. Backend, docs and
test-only PRs get no screenshots and say so.

**Logs.** `$STATE/logs/daemon.log`, rotated at 10 MB × 5 by the daemon itself
(pure Go). DEBUG level by default; `-logs` follows it and colorizes on a TTY
(`NO_COLOR=1` disables). Per-issue agent output is under
`$STATE/runs/<owner>-<repo>/issue-<n>/` (`plan-*.log`, `implement-*.log`,
`revise-*.log`, `fix-*.log`, `checks/*.log`). There is no `daemon.out`.

**Flags.** `-init` wizard, `-config` path, `-foreground`/`-f` attached,
`-logs` follow, `-status`, `-stop`, `-once` (startup scan + one poll, then exit).

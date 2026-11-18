# Deploying issuekiller — runbook for an AI agent

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
`AGENT_LOGIN` when no bot token exists yet.

```bash
AGENT_LOGIN=yourname-agent                       # dedicated bot account
REPOS="your-org/app your-org/api"                # watched repos
BIN_DIR="$HOME/.local/bin"                       # install location
CONFIG="$HOME/.config/issuekiller/config.json"
STATE="$HOME/.issuekiller"
LOG="$STATE/logs/daemon.log"
HB="issuekiller"                                 # binary name on PATH
```

## 0.1 Defaults — infer, do not ask

| value | default |
| --- | --- |
| `REPOS` | `git remote get-url origin` of the current checkout, if it is a GitHub repo |
| `AGENT_LOGIN` | the bot login from the dashboard (`~/.issuekiller/token`), `GH_AGENT_TOKEN`, or the legacy `gh-agent` hosts file |
| approver | the human's login from `gh api user --jq .login` |
| `codingAgent` | first of `opencode`, `claude`, `codex`, `pi`, `omp` on `PATH` |
| model | empty (CLI default) |
| proxy | `$https_proxy` / `$ALL_PROXY` if set, otherwise none |
| `BIN_DIR` | `$HOME/.local/bin` |
| `CONFIG` | `$HOME/.config/issuekiller/config.json` |

Write down every assumption in the final report instead of asking about it.

## 1. Preflight — ENSURE

```bash
uname -s
go version                                   # need go1.27+
git --version                                # need 2.5+
command -v opencode claude codex pi omp       # at least one
[ -n "$GH_AGENT_TOKEN" ] && echo token:env
[ -f "$HOME/.issuekiller/token" ] && echo token:file || echo token:file-missing
```

| result | action |
| --- | --- |
| Go or git missing | install with the platform package manager; if that needs sudo, **ASK** once and include the exact command |
| no coding-agent CLI | install `opencode` by default and say so; **ASK** only if the human has a stated preference |
| token unset | open the desktop app or dashboard and log in with the bot account (step 2); `~/.config/gh-agent/hosts.yml` is still read as a fallback |
| Windows | supported: the binary builds and runs there (`process_windows.go`), but `scripts/smoke-test.sh` needs a POSIX shell. Prefer the desktop app below |

The coding agent must work unattended on this machine, as the same user
(`opencode run`, `claude -p`, `codex exec`, `pi -p`, `omp -p`). If unsure, run its help command;
do not start the daemon until one of them exists.

## 2. Agent identity and access — ENSURE

The daemon acts only as the bot account. The bot must not be the human's own
account, and it must not create its own trigger issues.

The built-in login is a GitHub Device Flow, the same idea as `gh auth login`:
the dashboard shows a one-time code and a link, and the daemon stores the token
in `~/.issuekiller/token` (mode 0600) with its expiry and refresh token.
When the token is close to expiry it refreshes automatically, so a token-expiry
policy on the OAuth app does not interrupt the daemon. The desktop app opens
the login panel on first run; for a headless daemon, set `webPort`, open
`http://127.0.0.1:<port>/`, click **Settings → Log in with GitHub**, and use the
bot account (not your main account).

```bash
# optional: bypass the login UI for automation (never write it into the repo)
export GH_AGENT_TOKEN=...             # still accepted, no refresh support

# optional: paste-token fallback in the dashboard for a PAT with repo +
# admin:repo_hook scopes; the OAuth Device Flow requests repo,
# admin:repo_hook and read:user itself.
```

`ISSUEKILLER_OAUTH_CLIENT_ID` overrides the built-in OAuth app client ID for forks
(the pre-rename `GHIK_OAUTH_CLIENT_ID` is still read).
The legacy `~/.config/gh-agent/hosts.yml` is read if neither the token file nor
`GH_AGENT_TOKEN` exists, but nothing creates or rewrites it any more and the
daemon never falls back to the human's `gh auth token`.

Verify identity and per-repo permission (the token file is JSON):

```bash
AGENT_TOKEN="${GH_AGENT_TOKEN:-$(jq -r .access_token "$HOME/.issuekiller/token" 2>/dev/null || true)}"
[ -n "$AGENT_TOKEN" ] || { echo "no bot token: log in from the dashboard first" >&2; exit 1; }
GH_TOKEN="$AGENT_TOKEN" gh api user --jq .login                # must print $AGENT_LOGIN
for r in $REPOS; do GH_TOKEN="$AGENT_TOKEN" gh api "repos/$r" --jq '{repo:.full_name,push:.permissions.push,admin:.permissions.admin}'; done
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
`issuekiller_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) and
`checksums.txt` from
[Releases](https://github.com/uiautodev/issuekiller/releases), verify with
`sha256sum -c` (or `shasum -a 256 -c`), then put the binary on `PATH`.

```bash
git clone <this-repo> issuekiller && cd issuekiller
npm --prefix frontend ci
npm --prefix frontend run build   # writes cmd/issuekiller/web/dist (embedded)
go build -o bin/issuekiller ./cmd/issuekiller
go test ./...
install -m 0755 bin/issuekiller "$BIN_DIR/issuekiller"
```

**Verify**

```bash
"$BIN_DIR/issuekiller" -h | head -3      # prints usage
```

Prompts are embedded in the binary, so a copied binary is self-contained. A
`prompts/` directory next to the binary (or in the working directory) still
overrides the embedded copies for development.

### 3.1 Desktop app — optional

The desktop app is a thin Wails 3 window over the same engine: no separate
daemon, no terminal wizard. Download the bundle for the platform:

| platform | asset | contents |
| --- | --- | --- |
| macOS | `issuekiller-desktop_<version>_darwin_<arch>.zip` or `.dmg` | `issuekiller.app` with `AppIcon.icns` (shell + engine inside `Contents/MacOS/`); the DMG adds an `/Applications` shortcut |
| Linux | `issuekiller-desktop_<version>_linux_<arch>.tar.gz` | `issuekiller-desktop` + `issuekiller` |
| Windows | `issuekiller-desktop_<version>_windows_amd64.zip` | `issuekiller-desktop.exe` + `issuekiller.exe` |

Open the DMG and drag the app onto `/Applications`, or unzip the archive and
launch it. On first run there is no config, so the window opens on the
settings panel with the GitHub login on top: use **Log in with GitHub** with a
bot account (the device code and authorization link are shown in the panel),
then fill in repos, coding agent, model, proxy (and optionally the update
mirror `gh-proxy.com`), save, and the engine restarts with it. Closing the
window stops the engine — nothing is left in the background.
Links in the dashboard open in the system browser, including when the window
is the embedded desktop one.

- Config file: `~/.config/issuekiller/config.json` (`ISSUEKILLER_CONFIG` overrides).
- State/logs: `~/.issuekiller` — the same as the daemon.
- Upgrading from the old `gh-issue-killer` bundle: the first start moves
  `~/.config/gh-issue-killer` and `~/.gh-issue-killer` to their new names, so the
  config, token and worktrees carry over; `GHIK_CONFIG` and `GHIK_ENGINE` are
  still read as a fallback.
- The engine still takes `daemon.lock`, so do not run the desktop app and the
  background daemon at the same time.
- Updates replace the engine binary inside the bundle (the shell is a thin
  window and rarely changes); the engine restarts into the new version when the
  download and checksum succeed, and the old binary stays on any failure.
- `ISSUEKILLER_ENGINE` points the shell at a different engine binary (development).
- Build from source: `cd desktop && go build .`, then
  `desktop/build/package.sh vX.Y.Z` (macOS/Linux; macOS also gets a DMG) or
  `desktop/build/package.ps1 -Version vX.Y.Z` (Windows). Build the dashboard
  first (`npm --prefix frontend ci && npm --prefix frontend run build`), since
  the engine embeds `cmd/issuekiller/web/dist`; `wails3` itself is not
  needed, the window loads the engine's dashboard.
- The macOS bundle is not signed or notarised. If Gatekeeper refuses the first
  launch, use Open from the Finder context menu or
  `xattr -dr com.apple.quarantine issuekiller.app`.

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
issuekiller -once -config "$CONFIG"; echo "exit=$?"
grep -E 'agent identity|repo ready' "$LOG" | tail -5
```

Expected: `agent identity: @<bot>` and one `repo ready:` line per repo.
Non-zero exit or missing lines → see failures.

### 4.1 Configuration reference

| key | required | default | meaning |
| --- | --- | --- | --- |
| `repos` | ✅ | – | `"owner/repo"` list, or an object for per-repo overrides (`{"me/app": {"codingAgent": "claude"}}`) |
| `codingAgent` | | `opencode` | coding-agent CLI: `opencode`, `claude`, `codex`, `pi`, or `omp` |
| `model` | | – (CLI default) | optional model passed to the selected CLI |
| `approvers` | | repo owner | logins allowed to command the bot |
| `proxy` | | direct | `socks5h://host:port` (remote DNS for git, `socks5://` for Go clients), `socks5://`, `http://`, or `https://` |

Optional: `pollIntervalSec` (30), `webhook` (true), `maxFixRounds` (3),
`planParallel` (5) and `implementParallel` (1), both **per repository**,
`selfUpdate` (false), `followMaster` (false), `updateProxy` (empty = direct only), `webPort` (0 = off;
`8787` serves the dashboard on 127.0.0.1), `planTimeoutMin` (15),
`implementTimeoutMin` (60), `worktreeRoot` (state dir), `language` (`auto`),
`mode` (`auto`).

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

`mode` decides how a non-`bug` issue starts:

| value | behaviour |
| --- | --- |
| `plan` | always post a plan comment and wait for approval |
| `build` | implement immediately, without a plan comment |
| `auto` (default) | the coding agent triages the issue: a detailed or simple request goes straight to implementation, a vague one, one with several reasonable implementations, or one that explicitly asks to plan first gets a plan comment |

Triage is read-only and uses `planTimeoutMin`. A failed or unreadable triage
falls back to the plan, so an issue never gets stuck; in `auto` an explicit
“先 plan” / “plan first” comment also forces the plan. Unknown values are
rejected when the config loads. `bug`-labelled issues always skip the plan.

Per-repo overrides: the `repos` object form can set `codingAgent`, `model`,
`approvers`, `worktreeRoot`, `language`, `mode`, or a `release` block for one
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
        "intervalDays": 1,
        "time": "09:00"
      }
    }
  }
}
```

| key | default | meaning |
| --- | --- | --- |
| `enabled` | `false` | publish releases for this repository |
| `intervalDays` | `1` | minimum days between checks |
| `time` | `"09:00"` | local publish time of day (`HH:MM`, daemon host time zone) |

The dashboard shows the same block: **Settings → Repositories** picks a project
from the dropdown and gives it a card with the release switch, the interval and
the time; saving writes them back into `repos` (keeping `codingAgent`, `model`,
`language` and the other overrides) before restarting the daemon. A project left
on the default values writes no `release` block, so the file stays free of
no-op blocks. An unusable `time` is rejected at load time and by the dashboard
save instead of falling back silently.

The same project card sets that repository's coding agent, model and language.
Each has a "follow global" choice that removes the key instead of pinning
today's value, so only a deliberate override lands in the file
(`{"repos": {"me/app": {"codingAgent": "claude", "model": "opus"}}}`); a plain
`"owner/repo"` list is only rewritten as objects when at least one repository
overrides something. The proxy and update mirror stay global and are edited in
the config tab, which the card's "edit defaults" button opens.

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

Checks run at startup and then hourly, but a repository publishes at most once
per `intervalDays`, at `time` on the local date that follows the last successful
check. The tick is hourly, so the actual release lands within an hour after the
configured time; a repo that was never checked waits for today's `time`. A check
with no new commits records the timestamp and publishes nothing; a failed
request is logged and retried on the next tick without moving the timestamp.
`-once` does not run release checks. This is opt-in because it creates public
tags and releases: the first run of a repository covers every commit since its
newest version tag, or the whole history when there is none, and publishes
`v0.1.0`. The token needs `contents: write`, which a push-capable bot
token already has.

### 4.2 Coding agents

| `codingAgent` | CLI | non-interactive invocation |
| --- | --- | --- |
| `opencode` (default) | opencode | `opencode run --format json --auto [--model] [--session]` |
| `claude` | Claude Code | `claude -p --output-format json --permission-mode bypassPermissions [--model] [--resume/--session-id] --add-dir <runDir>` |
| `codex` | Codex CLI | `codex exec --cd <worktree> --full-auto [--model] [--add-dir <runDir>] --output-last-message <file>` |
| `pi` | Pi | `pi -p --mode json [--model] --session-id <id>` |
| `omp` | Oh My Pi | `omp -p --mode json [--model] [--resume <id>]` |

`agent` is no longer a config field; an old value is ignored with a warning.
Sessions are reused for fix rounds where the CLI supports it (opencode
`--session`, Claude `--resume`, Pi `--session-id`, omp `--resume`; Codex starts
a fresh run with full context). Pi's `--resume` is not used: in print mode it
opens the interactive session picker instead of resuming, so the daemon always
passes `--session-id` for Pi, reusing the stored id on later stages.

Pi and omp read their model providers from their own config files, not from
the daemon config: `~/.pi/agent/models.json` for Pi,
`~/.omp/agent/models.yml` for omp. Both take an OpenAI-compatible endpoint
with the model id, for example a relay gateway:

```json
// ~/.pi/agent/models.json — apiKey uses $NAME environment interpolation
{
  "providers": {
    "my-gateway": {
      "baseUrl": "https://gateway.example.com/v1",
      "api": "openai-completions",
      "apiKey": "$MY_GATEWAY_API_KEY",
      "authHeader": true,
      "models": [{ "id": "my-model", "contextWindow": 128000, "maxTokens": 16384 }]
    }
  }
}
```

```yaml
# ~/.omp/agent/models.yml — apiKey is a bare environment variable name
providers:
  my-gateway:
    baseUrl: https://gateway.example.com/v1
    api: openai-completions
    apiKey: MY_GATEWAY_API_KEY
    authHeader: true
    models:
      - id: my-model
        contextWindow: 128000
        maxTokens: 16384
```

The key stays out of the file: it is resolved from the environment at request
time, so the daemon must run with the variable set (for example in the systemd
unit from §6). A custom header a gateway requires (such as a device id) goes in
the provider's `headers` with the same interpolation. `model` in the daemon
config is passed through as `--model`, using the `provider/id` form when the
model id alone is ambiguous.

## 5. Start and verify

```bash
issuekiller -config "$CONFIG"            # background, prints pid + log path
issuekiller -config "$CONFIG" -status
```

**Verify**

```bash
issuekiller -config "$CONFIG" -status | grep -E 'daemon: running|webhook:|repo:'
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
# ~/.config/systemd/user/issuekiller.service
[Unit]
Description=issuekiller
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=%h/.local/bin/issuekiller -foreground -config %h/.config/issuekiller/config.json
Restart=on-failure
RestartSec=10
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
# include the coding agent's directory too (e.g. nvm's node bin)
# Environment=GH_AGENT_TOKEN=...   # optional; the dashboard login is preferred

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now issuekiller
systemctl --user status issuekiller --no-pager
```

macOS: start it once after login, or add it to login items. Nothing else.

## 7. Operations cheat sheet

| task | command |
| --- | --- |
| status / paths | `issuekiller -config "$CONFIG" -status` |
| dashboard | set `webPort` and open `http://127.0.0.1:<port>` (loopback only, no auth; approve/cancel are POST-only with an Origin check) |
| follow logs | `issuekiller -config "$CONFIG" -logs` |
| stop | `issuekiller -config "$CONFIG" -stop` |
| apply a config change | save the file; the daemon reloads in place when jobs finish (same PID) |
| self-update | with `"selfUpdate": true`, poll `releases/latest` every 30 min, verify `checksums.txt`, replace the binary and re-exec (no Go toolchain needed) |
| follow master | with `"followMaster": true`, poll `master` every 30 s and rebuild the binary from source (`git`, `go` and `npm` on PATH; the dashboard is rebuilt too), then re-exec. Takes precedence over `selfUpdate`. For development machines: a push reaches the daemon in about a minute. |
| install a release now | dashboard → 设置 → 检查更新 → 立即升级, or download the archive and replace the binary |
| update behind the GFW | set `"updateProxy": "gh-proxy.com"`; release downloads fall back to it |
| release check | `grep 'release' "$LOG"`; a due check at `release.time` publishes a tag + release, a failure logs `release check:` and retries next hour |
| bump the next release | nothing to configure: `feat`/`feature` commits bump minor and everything else patch; tag a major (`vX.0.0`) by hand when intended. The version comes from the newest `v*` tag |
| smoke test | `issuekiller -config "$CONFIG" -once` |
| full loop with the human's `gh` | `scripts/smoke-test.sh <owner/repo>` |

State layout (all under `$STATE`): `repos/` managed clones, `worktrees/`
per-issue trees (auto-removed after merge/cancel), `runs/` agent logs and
screenshots (safe to prune), `logs/daemon.log*` (10 MB × 5), `state.json`
(tracking; deleting it re-plans open issues), `daemon.lock`.

Upgrade (either path keeps `state.json`, so nothing is re-planned):

```bash
# release archive
sha256sum -c checksums.txt --ignore-missing
install -m 0755 issuekiller "$BIN_DIR/issuekiller"
issuekiller -config "$CONFIG" -stop
issuekiller -config "$CONFIG"

# or from source
issuekiller -config "$CONFIG" -stop
git pull && go build -o bin/issuekiller ./cmd/issuekiller && go test ./...
issuekiller -config "$CONFIG"
```

Upgrading from the pre-rename `gh-issue-killer` needs one manual install: the
old build looks for `gh-issue-killer_*` release assets and the renamed releases
only publish `issuekiller_*`, so its self-update cannot cross the rename. After
the manual swap the first start moves `~/.gh-issue-killer` to `~/.issuekiller`,
which keeps the token, `state.json`, clones and worktrees; `GHIK_*` variables
still work as a fallback. Stop the old daemon first — it would otherwise keep
writing into the directory that is being moved.

A failed self-update (no asset for the platform, bad checksum, mirror and
GitHub both unreachable) leaves the running binary untouched and logs why.
Interrupted implementations resume from their worktree on the next start.

## 8. Failure → action

| symptom (log or command) | action |
| --- | --- |
| `no GitHub token` | log in from the dashboard with the bot account, set `GH_AGENT_TOKEN`, or run `issuekiller -init` |
| `the GitHub token has no push access` | grant collaborator push/admin (step 2) |
| login code expired or `access_denied` | click **Log in with GitHub** again; the code is single-use and short-lived |
| token refresh fails repeatedly | log in again from the dashboard; the refresh token expired or was revoked |
| `unsupported codingAgent` / `runnerBinary` error | install that CLI and make sure it is on PATH of the daemon |
| `create webhook: 403/404` | expected on personal repos; polling continues. Do not retry blindly |
| `-status` says `polling only` | no webhook; still functional, 30 s latency |
| `fetch base` / `create worktree` errors | git auth or proxy; re-check scope and `proxy` |
| checks failed, issue `blocked` | inspect `$STATE/runs/<owner>-<repo>/issue-<n>/checks/`; fix in the kept worktree, then `@agent continue` |
| new issue produces nothing | issue must @ the bot or assign it; the author must not be the bot |
| 👍 on the plan does nothing | reaction must be on the plan comment by an approver; the poll checks every 30 s |
| 👍 on a blocked comment does nothing | the 👍 must be on the latest `Something went wrong` / blocked message and come from an approver; older status comments are forgotten |
| every poll fails with `API rate limit exceeded` | the token burned its 5000/h core quota; polls and reactions stay blind until the window resets (`gh api rate_limit`). Check for repeated daemon starts (each runs a startup scan) |
| stale `agent:*` labels | only `agent:awaiting-approval` is still managed; delete the leftover old labels by hand in the repo settings |
| `already running (pid N)` | use `-status` / `-stop`; never start a second instance |
| desktop window says the engine exited | read `~/.issuekiller/logs/desktop-engine.log`; the usual cause is a daemon (or a second desktop) already holding `daemon.lock` |
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
issuekiller -config "$CONFIG" -status | tee /tmp/issuekiller-status.txt
grep -q 'daemon: running' /tmp/issuekiller-status.txt && echo "STATUS ok"
for r in $REPOS; do GH_TOKEN="$AGENT_TOKEN" gh api "repos/$r" --jq '.permissions.push' | grep -q true && echo "ACCESS $r ok"; done
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

**Status comments.** Every “needs you” message (`Something went wrong`, `checks
failed`, `CI blocked`, `merge failed`) is stored as `statusCommentId`; an
approver's 👍 on it retries the task and gets an 👀 ack. `@agent continue` does
the same thing.

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

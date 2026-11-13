# Deploying gh-issue-killer — runbook for an AI agent

Audience: an AI agent operating this machine (a human can copy-paste the same
commands). Work through the steps in order. Every step ends with a **verify**
command and the output you should see; if it does not match, go to
[Failure → action](#8-failure--action) before continuing.

Markers:

- **ASK** — stop and ask the human (permissions, credentials, optional extras).
- **ENSURE** — safe to re-run; make the state match.
- **NEVER** — an invariant; violating it breaks the deployment.

Behaviour facts you may need: one static Go binary, outbound HTTPS only (no
inbound port), one instance per state directory, and the workflow commands are
`@agent lgtm` / `@agent 合并` / `@agent cancel` (plus 👍 reactions).

## 0. Variables

Set these once in the shell that runs every step. Ask the human for the values
you do not know.

```bash
AGENT_LOGIN=yourname-agent                       # dedicated bot account
REPOS="your-org/app your-org/api"                # watched repos
BIN_DIR="$HOME/.local/bin"                       # install location
CONFIG="$HOME/.config/gh-issue-killer/config.json"
STATE="$HOME/.gh-issue-killer"
LOG="$STATE/logs/daemon.log"
HB="gh-issue-killer"                             # binary name on PATH
```

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
| Go or git missing | **ASK** the human to install them |
| no coding-agent CLI | **ASK** which one to install; do not pick silently |
| token unset | check `~/.config/gh-agent/hosts.yml`; if absent → step 2 |
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

Verify identity and per-repo permission:

```bash
gh-agent api user --jq .login                # must print $AGENT_LOGIN
for r in $REPOS; do gh-agent api "repos/$r" --jq '{repo:.full_name,push:.permissions.push,admin:.permissions.admin}'; done
```

`push:true` is mandatory. If `push:false`, **ASK** the human to run:

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

Keep `prompts/` next to the binary if the repository is not the working
directory: the binary looks for `./prompts` first, then `<exe-dir>/prompts`.

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
| follow logs | `gh-issue-killer -config "$CONFIG" -logs` |
| stop | `gh-issue-killer -config "$CONFIG" -stop` |
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
| stale `agent:*` label after closing | close the issue again; cleanup runs in its own goroutine |
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

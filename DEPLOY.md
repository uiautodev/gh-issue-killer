# Deploying gh-issue-killer

This is the operational guide: a dedicated agent account, a stable install,
where the state lives, how to keep it running, and what to check when nothing
happens. See [README.md](README.md) for how the workflow itself behaves.

The daemon runs on one machine, makes **outbound** HTTPS requests only, and
needs no inbound port, database, or service manager.

## Requirements

| | |
| --- | --- |
| OS | macOS or Linux |
| Go | 1.27+ (only to build; the result is one static binary) |
| git | 2.5+ (`git worktree`) |
| coding agent CLI | one of `opencode`, `claude`, `codex`, already authenticated for non-interactive runs |
| GitHub credential | a **separate** account for the agent, see below |
| network | outbound 443 to `github.com`, `api.github.com`, `smee.io` (or your proxy) |

The coding-agent CLI must work unattended: the daemon invokes it with its
non-interactive mode (`opencode run`, `claude -p`, `codex exec`) and no stdin.
Test that on the machine that will run the daemon, as the same user.

## 1. Prepare the agent account

Use a dedicated GitHub account (for example `yourname-agent`) so approvals,
commits and comments are clearly machine-authored.

```bash
# as the human, per repository
gh api -X PUT repos/<owner>/<repo>/collaborators/<agent-login> \
  -f permission=admin        # push is enough; admin also lets the daemon create webhooks
```

For organization repositories `admin` is fine. Personal repositories cannot
grant Admin to a collaborator, so the daemon falls back to polling unless you
create the webhook once with the owner account.

Give the daemon a token in one of these ways:

```bash
# 1. environment variable (simplest for a service)
export GH_AGENT_TOKEN=ghp_...

# 2. gh CLI config dir (hosts.yml or OS keyring)
GH_CONFIG_DIR="$HOME/.config/gh-agent" gh auth login

# scopes: repo, read:org; add admin:repo_hook for real-time webhooks
GH_CONFIG_DIR="$HOME/.config/gh-agent" gh auth refresh -h github.com -s admin:repo_hook
```

The daemon only ever acts as this account. It also points the coding agent at
`GH_CONFIG_DIR=~/.config/gh-agent`, so `gh` inside a worktree is the agent too.

## 2. Install

```bash
git clone <this-repo> gh-issue-killer
cd gh-issue-killer
go build -o bin/gh-issue-killer ./cmd/gh-issue-killer
go test ./...

# optional: put it on PATH
install -m 0755 bin/gh-issue-killer /usr/local/bin/gh-issue-killer
```

The binary resolves `prompts/` relative to the working directory first, then
next to the executable. If you move the binary without the repository, copy
`prompts/` beside it.

## 3. Configure

Pick one stable config path and always pass it with `-config`. Without the flag
the daemon reads `./config.json` from its **current directory**, which is easy
to lose when starting it from another shell.

```bash
mkdir -p ~/.config/gh-issue-killer
gh-issue-killer -init -config ~/.config/gh-issue-killer/config.json
```

The wizard detects installed coding agents, asks for repositories, model,
approvers and proxy, verifies gh-agent's permissions per repo and that the
proxy can reach `api.github.com`, then writes a minimal config. Re-running it
pre-fills every answer from the existing file.

Minimal config, if you prefer to write it yourself:

```json
{
  "repos": ["your-org/app", "your-org/api"],
  "codingAgent": "opencode",
  "approvers": ["your-login"],
  "proxy": ""
}
```

Per-repo overrides and every optional key are documented in
[README.md](README.md#configure). Set `worktreeRoot` if the default state
directory is on a small disk.

## 4. Smoke test

```bash
gh-issue-killer -once -config ~/.config/gh-issue-killer/config.json
```

`-once` runs the startup scan plus one poll and exits — it proves the token,
repo access and agent CLI work without leaving a process behind. Then watch the
log while opening a real issue that mentions the agent:

```bash
gh-issue-killer -logs
```

`scripts/smoke-test.sh <owner/repo>` runs the full loop using **your** `gh` as
the human (`lgtm`, merge) against a throwaway issue.

## 5. Start and operate

```bash
gh-issue-killer -config ~/.config/gh-issue-killer/config.json            # background (default)
gh-issue-killer -config ~/.config/gh-issue-killer/config.json -status    # pid, config, log, state
gh-issue-killer -config ~/.config/gh-issue-killer/config.json -logs      # follow the log
gh-issue-killer -config ~/.config/gh-issue-killer/config.json -stop      # stop and clean the lock
gh-issue-killer -config ~/.config/gh-issue-killer/config.json -foreground # attached, for debugging
```

The background process re-execs itself in a new session, inheriting the
environment of the shell that started it (PATH, `GH_AGENT_TOKEN`, proxy vars).
Start it from a shell where the coding-agent CLI is reachable. Only one
instance can run per state directory (`daemon.lock`).

## 6. Boot persistence (optional)

The daemon backgrounds itself; it does not need `launchd`, `systemd`, `nohup`
or `logrotate`. If you want it back after a reboot on Linux, a user unit is
enough:

```ini
# ~/.config/systemd/user/gh-issue-killer.service
[Unit]
Description=gh-issue-killer
Wants=network-online.target
After=network-online.target

[Service]
Type=simple
ExecStart=%h/.local/bin/gh-issue-killer -foreground -config %h/.config/gh-issue-killer/config.json
# adjust ExecStart to wherever the binary and config actually live
Restart=on-failure
RestartSec=10
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
# add the coding agent's directory too (e.g. %h/.nvm/versions/node/vXX/bin)
# Environment=GH_AGENT_TOKEN=...

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload
systemctl --user enable --now gh-issue-killer
journalctl --user -u gh-issue-killer -f
```

On macOS, start it once after login from a shell, or add it to your login items.
Nothing else is required.

## 7. State, disk and cleanup

Everything lives under `~/.gh-issue-killer`:

| path | what | safe to delete? |
| --- | --- | --- |
| `repos/<owner>-<repo>` | managed clone per watched repo | no (it is re-cloned, but loses nothing) |
| `worktrees/<owner>-<repo>/issue-*` | per-issue working tree | only when its issue is not active |
| `runs/<owner>-<repo>/issue-*` | agent logs, `checks/`, screenshots | yes, after the PR is merged |
| `tmp/plan-*` | temporary plan worktrees | yes, when no plan is running |
| `logs/daemon.log*` | rotated 10 MB × 5 | yes |
| `state.json` | issues, phases, PR numbers, processed comments | deleting resets tracking |

Deleting `state.json` does not lose code, but the daemon forgets which issues
it already handled; open issues mentioning the agent are planned again.

Routine cleanup:

```bash
gh-issue-killer -stop
find ~/.gh-issue-killer/runs -type d -name 'issue-*' -mtime +14 -exec rm -rf {} +
# worktrees are removed automatically on merge/cancel; list leftovers with:
git -C ~/.gh-issue-killer/repos/<owner>-<repo> worktree list
```

## 8. Upgrade

```bash
gh-issue-killer -stop
git pull
go build -o bin/gh-issue-killer ./cmd/gh-issue-killer
gh-issue-killer -config ~/.config/gh-issue-killer/config.json
```

State survives upgrades (new fields are optional). Implementations that were
running when the old process stopped resume automatically from their
worktrees on the next start. Re-running `-init` after an upgrade rewrites the
config without losing per-repo overrides.

## 9. Network and proxy

Outbound only:

- `api.github.com` / `github.com` — issues, comments, PRs, uploads, git
- `smee.io` — the webhook relay (real-time mode)
- your proxy, if configured: `socks5h://`, `socks5://`, `http://`, `https://`

No listening port. Webhooks are relayed through `smee.io`, so the machine can
sit behind NAT; the SSE connection is outbound. Git operations honour the same
proxy (remote DNS for `socks5h`).

## 10. Security notes

- Treat the agent token as a push-capable credential; scope it to the repos you
  actually want automated.
- Commands require an **approver** and an explicit `@agent` mention; 👍
  reactions are re-read from the GitHub API before acting.
- Webhook payloads are never trusted: every event is re-fetched from GitHub
  before a command, author or issue body is used. The smee channel URL acts as
  a capability, so keep it private.
- Tokens are passed to git through a one-shot `http.extraheader`, never written
  into a gitconfig.
- `daemon.log` is DEBUG by default and contains issue text and commands; treat
  it as sensitive.

## 11. Troubleshooting

| symptom | check |
| --- | --- |
| new issue produces nothing | issue must @ the agent or assign it; the author cannot be the agent account |
| `-status` says `polling only` | hook creation was refused (personal repo, no `admin:repo_hook`); polling still works |
| `gh-agent has no push access` | collaborator permission on that repo |
| coding agent not found | install it on `PATH` of the shell that started the daemon, then restart |
| checks keep failing | fix in the kept worktree, then `@agent continue`; logs in `runs/.../checks/` |
| nothing after a restart | `-logs` for `resuming interrupted implementation` |
| proxy errors | the wizard's connectivity check; `socks5h` for git/remote DNS |
| stale `agent:*` label after closing | close the issue again; cleanup runs in its own goroutine, not behind coding jobs |
| no reaction to 👍 on the plan | the plan comment must be the one reacted to, and the account must be an approver; poll checks every 30 s |

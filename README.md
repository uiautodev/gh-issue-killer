# gh-issue-killer

Watch GitHub issues, let a coding agent (opencode, Claude Code, or Codex) plan → implement → test → screenshot → PR → squash-merge them, driven entirely from issue comments.

```
issue @agent / assigned        👀 reaction + plan comment
owner: lgtm / continue   →     worktree + code + unit/E2E checks + before/after screenshot + PR
owner: 合并 / merge      →     wait CI → squash merge → close issue/PR → clean worktree
owner: cancel / 停止     →     abort, keep the issue open
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

Starting the daemon without a config, in an interactive terminal, launches the same wizard automatically. `-config` and `GWIK_CONFIG` select the path.

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

Optional: `pollIntervalSec` (30), `webhookPort` (8788), `webhook` (true), `maxFixRounds` (3), `maxParallel` (1), `planTimeoutMin` (15), `implementTimeoutMin` (60).

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
./bin/gh-issue-killer -foreground   # attached: logs stream to the terminal, Ctrl+C to stop
./bin/gh-issue-killer -f            # shorthand for -foreground
./bin/gh-issue-killer               # same as -foreground (kept for compatibility)
./bin/gh-issue-killer -stop         # stop the running daemon and clean the lock
./bin/gh-issue-killer -once         # startup scan + one poll, then exit (smoke check)
scripts/smoke-test.sh <owner/repo>  # full loop using YOUR gh as the human
```

`-foreground` prints a banner and keeps the process attached to the terminal; it is the recommended mode for debugging. The single-instance lock still applies, so run `-stop` first if a background copy is running. In background you can use `nohup ./bin/gh-issue-killer >> ~/.gh-issue-killer/logs/daemon.out 2>&1 &`, and stop it with `-stop`.

State: `~/.gh-issue-killer/state.json` · logs: `~/.gh-issue-killer/logs/daemon.log` · single instance enforced by `daemon.lock`.

## Commands the daemon understands

| comment (by an approver) | effect |
| --- | --- |
| `lgtm`, `continue` | approve the plan (or retry after blocked) |
| `合并`, `merge`, `ship` | squash merge the PR, close issue + PR, clean the worktree |
| `cancel`, `stop`, `停止` | abort the current task |

Anyone can `@<agent-login>` an issue (or assign it) to request a plan. The agent never reacts to its own comments and never treats its own issues as triggers.

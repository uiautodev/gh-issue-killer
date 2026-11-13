# gh-issue-killer

Watch GitHub issues and let a coding agent (opencode, Claude Code, or Codex)
plan → implement → test → screenshot → open a PR → squash-merge them, driven
entirely from issue comments.

```
issue @agent / assigned        plan comment
owner: @agent lgtm       →     worktree + code + checks + before/after screenshots + PR
owner: @agent <feedback>  →     keep working on the open PR
owner: @agent 合并        →     wait CI → squash merge → close issue/PR → clean worktree
owner: @agent cancel     →     abort, keep the issue open
👍 on the plan / PR comment    same as “lgtm” / “合并”
```

## What it does

- **Plan first.** A mention (or assignment) produces an implementation plan as
  a comment, with an isolated planning worktree; nothing is written until you
  approve.
- **Isolated implementation.** Each issue gets its own git worktree and branch;
  your working clone is never touched. Commits are authored as the bot.
- **Checks and screenshots.** The coding agent writes the commands that prove
  its work (`checks.json`); the daemon runs them, gives the agent up to
  `maxFixRounds` repair rounds, and only then pushes. UI changes come with
  before/after screenshots, tightly cropped and split by a visible divider.
- **PR and merge.** The daemon opens the PR, annotates it with checks and
  screenshots, waits for CI, and squash-merges on command — then closes the
  issue and cleans the worktree.
- **Feedback loop.** A non-command comment that mentions the agent on an open
  PR is treated as review feedback: the agent keeps working on the same branch.
  Closing an issue cancels the task and closes the PR; reopening resumes it.
- **Real-time, with a fallback.** Webhooks arrive through a `smee.io` relay
  (no inbound port); every event is re-read from the GitHub API before acting.
  Without hook permissions the daemon simply polls every 30 s.
- **Crash-safe.** A restart resumes interrupted implementations from their
  worktree instead of parking the issue.
- **Multi-repo, per-repo agent.** Watch several repositories, each with its own
  `codingAgent` / model / approvers.
- **One static binary.** Outbound HTTPS only; no `curl`, `ffmpeg`, ImageMagick,
  or `gh` at runtime. It shells out to `git`, one coding-agent CLI, and `/bin/sh`.

## Install with a coding agent

Give Claude Code, Codex or opencode this repository and paste this into it:

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

The agent follows [DEPLOY.md](DEPLOY.md): it installs the binary, creates the
dedicated `gh-agent` wrapper if needed, writes the config, runs the smoke test,
starts the daemon and reports the status lines. It stops only when it needs a
bot token, which only you can log in for.

**Manual install.** Requirements: Go 1.27+, `git`, one coding-agent CLI, and a
GitHub bot account with a token (`GH_AGENT_TOKEN` or
`~/.config/gh-agent/hosts.yml`).

```bash
go build -o bin/gh-issue-killer ./cmd/gh-issue-killer
./bin/gh-issue-killer -init      # wizard: repos, agent, approvers, proxy
./bin/gh-issue-killer            # background daemon
./bin/gh-issue-killer -status    # pid, config, log and state paths
```

Full deployment, operations, upgrades and troubleshooting:
[DEPLOY.md](DEPLOY.md).

## Daily use

Open an issue that mentions the bot (or assign it to the bot). Then:

| comment | effect |
| --- | --- |
| `@agent lgtm`, `@agent continue`, `@agent 继续` | approve the plan (or retry after blocked) |
| `@agent 合并`, `@agent merge`, `@agent ship` | squash merge the PR, close issue + PR, clean the worktree |
| `@agent cancel`, `@agent stop`, `@agent 停止` | abort the current task |
| 👍 on the plan comment | same as `@agent lgtm` |
| 👍 on the PR-announcement comment | same as `@agent 合并` |

Commands must come from an approver, mention the agent, and be the **whole
comment** — “把按钮合并到 input-group 里” never merges anything. Any other
comment that mentions the agent is treated as feedback. A daemon restart
resumes interrupted work automatically; the agent never triggers on its own
comments or issues.

## Configuration

`-init` writes this for you; a hand-written config is the same shape:

```json
{
  "repos": ["your-org/app", "your-org/api"],
  "codingAgent": "opencode",
  "approvers": ["your-login"],
  "proxy": ""
}
```

`codingAgent` is `opencode`, `claude` or `codex`. `approvers` are human logins
allowed to command the bot. `proxy` accepts `socks5h://`, `socks5://`,
`http://` or `https://` and applies to both the API and git.

The full key reference, per-repo overrides and the coding-agent invocation
details live in [DEPLOY.md](DEPLOY.md#41-configuration-reference).

## Development

```bash
pre-commit install    # gofmt, go vet, go test, go build on every commit
go test ./...
```

Tag a release with `v*` (for example `git tag v0.1.0 && git push --tags`):
GitHub Actions runs the tests, builds linux/darwin amd64+arm64 archives with
the version injected, and publishes them with checksums to the GitHub release.
The prompts are embedded in the binary, so the archives are self-contained.

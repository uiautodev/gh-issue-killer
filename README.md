# gh-issue-killer

Watch GitHub issues, let an opencode agent plan → implement → test → screenshot → PR → squash-merge them, driven entirely from issue comments.

```
issue @agent / assigned        👀 reaction + plan comment
owner: lgtm / continue   →     worktree + code + unit/E2E checks + screenshots + PR
owner: 合并 / merge      →     wait CI → squash merge → close issue/PR → clean worktree
owner: cancel / 停止     →     abort, keep the issue open
```

## Install

```bash
npm install        # no runtime dependencies, just locks nothing
npm test
```

Requirements on this machine: `node >= 22`, `gh`, `gh-agent` (a second gh config dir, see below), `git`, `opencode`, `curl`, `git-remote` access to github.com (optionally via a SOCKS proxy).

## Configure

`config.json` (gitignored, copy from `config.example.json`):

```json
{
  "repos": ["codeskyblue/weektodo"],
  "agent": "opencode",
  "model": "magpie/group/deepseek",
  "approvers": ["codeskyblue"],
  "proxy": "socks5h://127.0.0.1:10808"
}
```

| key | required | default | meaning |
| --- | --- | --- | --- |
| `repos` | ✅ | – | `"owner/repo"` list, or an object for per-repo overrides (`{"me/app": {"agent": "build"}}`) |
| `agent` | | `opencode` | opencode agent name; `opencode` means the default agent |
| `model` | | `magpie/group/deepseek` | `--model` passed to `opencode run` |
| `approvers` | | repo owner | logins allowed to say `lgtm` / `合并` |
| `proxy` | | direct | applied to git (`socks5h`), gh/opencode (`socks5://`), curl uploads |

Optional: `pollIntervalSec` (30), `webhookPort` (8788), `webhook` (true), `maxFixRounds` (3), `planTimeoutMin` (15), `implementTimeoutMin` (60).

Per-issue override: add an issue label like `agent:build` and that agent name wins over the config.

## Accounts

The daemon **only ever runs `gh-agent`** (`GH_CONFIG_DIR=~/.config/gh-agent`). Your own `gh` is never called by the daemon; it appears only in `scripts/smoke-test.sh`, which simulates you as the issue author and approver.

`gh-agent` needs on each watched repo:

- **Write** — comment, label, push branches, open PRs, squash merge.
- **Admin + `admin:repo_hook` scope** — optional, enables real-time webhooks (`gh webhook forward` via the GitHub CLI relay). Without it the daemon polls every 30 s and prints how to enable webhooks:

```bash
# one-time, as the human:
gh api -X PUT repos/<owner>/<repo>/collaborators/<agent-login> -f permission=admin
# then refresh the agent token (authorize in a private window logged in as the agent):
GH_CONFIG_DIR="$HOME/.config/gh-agent" gh auth refresh -h github.com -s admin:repo_hook
```

## Worktrees

The daemon never looks at or touches your working clones. Every watched repo gets a managed clone at `~/.gh-issue-killer/repos/<owner>-<repo>`; on startup it is cloned if missing, otherwise fetched to the latest base branch. Implementation worktrees live at `~/.gh-issue-killer/worktrees/<owner>-<repo>/issue-<n>-<id>` on branch `agent/issue-<n>-<id>`; a temporary detached worktree under `~/.gh-issue-killer/tmp/` is used for the read-only planning pass. `worktreeRoot` in the config can override the worktree location.

Git auth uses a one-shot `http.extraheader` with the gh-agent token (never written to any gitconfig), and commits are authored as the gh-agent noreply identity.

## Checks, screenshots

The implement agent writes `~/.gh-issue-killer/runs/<repo>/issue-<n>/checks.json`; the daemon executes those commands itself (install / lint / typecheck / unit / e2e) and only ships when they all pass. Failing checks are fed back to the agent for up to `maxFixRounds` fix rounds.

Screenshots are only required for user-visible UI changes. The agent captures just the changed component/region (tight crop, not the full page) in both its base version and its changed version, and the daemon composes them side by side into a single image (12 px white gap, padded to a common height with `ffmpeg`) before uploading it via GitHub's user-attachments endpoint. Backend/docs/test-only PRs get no screenshots and say so explicitly.

## Run

```bash
npm start                       # foreground
scripts/smoke-test.sh <owner/repo>   # full loop using YOUR gh as the human
```

State: `~/.gh-issue-killer/state.json` · logs: `~/.gh-issue-killer/logs/daemon.log` · single instance enforced by `daemon.lock`.

## Commands the daemon understands

| comment (by an approver) | effect |
| --- | --- |
| `lgtm`, `continue` | approve the plan (or retry after blocked) |
| `合并`, `merge`, `ship` | squash merge the PR, close issue + PR, clean the worktree |
| `cancel`, `stop`, `停止` | abort the current task |

Anyone can `@<agent-login>` an issue (or assign it) to request a plan. The agent never reacts to its own comments and never treats its own issues as triggers.

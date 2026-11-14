# AGENTS.md

Guidance for coding agents working in this repository.

## What this is

`gh-issue-killer` is a single Go binary that watches GitHub issues, runs a
coding agent (opencode / Claude Code / Codex) in isolated git worktrees, runs
checks, opens PRs, repairs CI and squash-merges on command. All Go code is
`package main` under `cmd/gh-issue-killer`.

## Commands

```bash
go build ./...                 # must pass
go vet ./...                   # must pass
go test ./...                  # must pass
gofmt -l .                     # must print nothing
go build -o bin/gh-issue-killer ./cmd/gh-issue-killer
```

End-to-end: `scripts/smoke-test.sh owner/repo` (needs `gh` and `gh-agent`).
Never start two daemons: they share `~/.gh-issue-killer/state.json`.

## Documentation must move with the code

**`DEVELOP.md` is part of the code and must be updated in the same commit as any
behaviour change.** Its §19 is an explicit mapping: a phase change updates §5, a
queue change §7, a stage §8, the CI gate §9, a config key §14, a new test file
§17, and so on. If you add a file, add it to §4; if you add a prompt, add it to
§4/§9/§17 and to `prompts_test.go`.

Also keep in sync:

- `README.md` **and** `README.zh-CN.md` for user-visible behaviour (both
  languages, same content).
- `DEPLOY.md` for config keys, installation and operations (config reference is
  §4.1 there).
- `prompts/*.md` placeholders must match the `renderPrompt` call sites; every
  prompt is embedded via `prompts/embed.go` and covered by `prompts_test.go`.

A change that alters behaviour but leaves these files stale is incomplete.

## Code conventions

- Standard library first. The only direct dependency is `fsnotify`; do not add
  another without a strong reason.
- `gofmt` formatting, tabs, `go vet` clean.
- Comments explain *why* (trade-offs, GitHub quirks, race conditions), not
  *what*. Exported helpers get a one-line doc comment.
- Keep error messages actionable; user-facing strings are Chinese in GitHub
  comments (matching the existing stages) and English in code and logs.
- Every new branch in the CI gate, command keyword, phase transition or
  scheduler rule needs a unit test next to the code (`newFakeAPI` for GitHub,
  `NewSilentLogger` to keep tests quiet).
- Time-based logic must be testable: prefer injectable `now`/constants over
  `time.Sleep` in tests.

## Invariants (details in DEVELOP.md §18)

- One writer for `state.json`: the `Store`, through `UpdateIssue`.
- One agent per worktree. A merge waits for the issue's CI job; cancel/close
  cancels and waits for both job keys before the worktree is removed.
- Job keys: `<repo>#<issue>` for plan/work, `<repo>#<issue>#ci` for the CI
  settle. Do not run long waits on the `work` queue.
- GitHub check runs ≠ workflow runs: check runs by sha, job logs by job id
  parsed from `details_url`, `action_required` means “needs maintainer
  approval”.
- Prompts are one argv entry, never shell-interpolated.

## Git and commits

- Conventional prefixes: `feat:`, `fix:`, `ci:`, `docs:`, `refactor:`,
  `chore:`.
- Commit in the repository's existing style: imperative subject, body explains
  the problem and the trade-off.
- Never commit `bin/`, logs, `state.json`, screenshots or other artifacts
  (`bin/` and the root binary are already ignored).
- Do not rewrite published history or force-push `master`.

## Working with an issue

When fixing a bug, reproduce it first; if it cannot be reproduced, say so
instead of guessing. When changing the daemon's behaviour, walk the affected
stage end to end (plan → implement → CI settle → merge) and check the state
transitions and labels it writes.

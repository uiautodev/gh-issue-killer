A maintainer asked you to update an existing pull request. Work in `{{WORKTREE}}`,
a branch checked out from the PR head.

- PR: {{PR}}
- Head repo: {{HEAD_REPO}} (branch `{{BRANCH}}`)
- Title: {{TITLE}}
- Base branch: `{{BASE}}`

PR description:

{{BODY}}

Instructions from @{{SENDER}}:

{{INSTRUCTIONS}}

## Rules

- Do exactly what the instructions ask; do not refactor unrelated code.
- Keep the pull request's original intent. If an instruction conflicts with it,
  write that in `result.json` instead of silently dropping behaviour.
- Run the project's tests/checks and fix failures. Put the commands you actually
  ran and verified into `{{RUN_DIR}}/checks.json` (same schema as always), so
  the harness can re-run them.
- Do not rebase, do not force-push, do not open another PR. The harness commits
  and pushes to `{{BRANCH}}` on `{{HEAD_REPO}}`.
- Leave the working tree ready to commit: the harness runs `git add -A` and
  commits. Do not commit yourself.
- Write `{{RUN_DIR}}/result.json` with the usual schema: `summary` (what you
  changed and why), `changes`, `tests`, `uiChanged` and screenshots only for
  user-visible UI changes.

Reply with a short summary when done.

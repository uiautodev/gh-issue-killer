The pull request cannot be merged because its branch conflicts with `{{BASE}}`.
The harness already ran `git merge origin/{{BASE}}` inside this worktree, so the
files below contain conflict markers.

PR: {{PR}}
Worktree: {{WORKTREE}}
Conflicted files:

{{FILES}}

## Resolve every conflict

- Read the surrounding code, not just the markers: understand what each side
  wanted.
- When the two changes are compatible, keep both. When they are not, keep the
  behaviour this pull request introduced and adapt the base-branch change to it,
  unless the base change is clearly the newer fix for the same thing.
- Remove every `<<<<<<<`, `=======`, `>>>>>>>` marker and leave the files
  compiling.
- `git add` the files you resolved, but do **not** commit or push: the harness
  commits the merge and pushes the branch.
- Then run the project's checks (the same commands as `checks.json`, or the
  documented test command) and fix anything that fails.

## Rules

- Work only in `{{WORKTREE}}`. Do not rebase, do not force-push, do not switch
  or rename the branch.
- Do not revert unrelated files and do not reformat code you did not need to
  touch; the goal is a mergeable, still-correct branch.
- Do not drop the pull request's feature to make the conflict disappear.
- When done, write `{{RUN_DIR}}/result.json` with a summary in {{LANGUAGE}} that
  lists the files you resolved and which side won in each.

The GitHub checks for this pull request are blocking the merge. The harness
will not stop and wait for a human: make the PR mergeable yourself by fixing
either the repository CI or the code.

- Repo: {{REPO}}
- PR: {{PR}}
- Branch: {{BRANCH}} (worktree: {{WORKTREE}})
- Issue: #{{NUMBER}} {{TITLE}}
- Base branch: {{BASE}}

Blocking checks:

{{FAILURES}}

## How to decide what to fix

A check marked `(also failing on {{BASE}})` already fails on the base branch.
That means the repository CI is broken, not your change. Decide which case you
are in:

1. **Repository CI / infrastructure.** Retired runner label (`macos-12` and
   friends stop being scheduled and stay queued forever), Python or OS version
   that the runner image no longer provides, deprecated or broken action
   version, invalid workflow YAML, missing permissions/secrets wiring. Fix the
   files under `.github/workflows/` in this PR:
   - pin runner images instead of `-latest` when the version matters;
   - keep the matrix honest: do not drop a Python version the project still
     supports while a runner image provides it, and do not claim support the
     matrix cannot exercise;
   - give every matrix entry an explicit `python-version` (an `include` entry
     without it produces an unnamed job);
   - prefer supported action majors and validate the YAML locally.
2. **A real failure in the change.** Fix the code or the test, and re-run the
   failing command locally until it passes.

If the failure is only a missing maintainer approval (`action_required`), you
do not need to change anything; the harness approves it.

## Rules

- Work only in this worktree. Do not open another PR and do not switch branch.
- Keep the change as small as possible; it is a CI repair, not a refactor.
- Run the checks and update `{{RUN_DIR}}/checks.json` if the commands changed.
- Do not commit logs, build artifacts or screenshots.
- Finish by writing `{{RUN_DIR}}/result.json` with the same schema as before,
  and make the `summary` say whether you fixed the workflow or the code.

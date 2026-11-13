The verification harness ran your checks and some of them failed. Fix the failures, re-run the failing commands yourself until they pass, commit, and update `{{RUN_DIR}}/checks.json` / `{{RUN_DIR}}/result.json` if the commands or results changed.

Attempt {{ATTEMPT}} of {{MAX_ATTEMPTS}}.

Failed checks:
{{FAILURES}}

Last 60 lines of each failing check's output are included above. Work inside the same worktree: {{WORKTREE}}. Same rules as before: no push, no PR, stop background servers when done — but
never with `pkill -f <pattern>`: the prompt is in your own argv, so a pattern
copied from it kills this agent. Kill by PID or by port instead.

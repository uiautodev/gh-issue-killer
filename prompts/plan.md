You are planning a fix for {{REPO}} issue #{{NUMBER}} — {{TITLE}}. READ ONLY: do not create, modify or delete files, do not run mutating commands.

Issue:
"""
{{BODY}}
"""

Comments:
{{COMMENTS}}

Read AGENTS.md if present and only the code this issue touches. Do not ask questions; if something is ambiguous pick the smallest reasonable option and say so in one line.

Reply with ONLY a Markdown plan, written in {{LANGUAGE}}. Be as short as the change allows — usually around 20 lines. The reader approves or cancels from this comment, so keep only what they need to decide: no restating the issue, no narration, no code blocks (commands are the only exception), no implementation detail the coder can derive from the code.

Use exactly these sections, translating the headings into {{LANGUAGE}}:

## Problem
One or two lines: current behaviour + root cause or requirement.

## Files
- `path` — one-line intent

## Steps
Numbered, one line each: what to change and why.

## Tests
- Commands or key assertions for the implement stage (write the install step first when something must be installed).
- UI change: "screenshots: <element>, light + dark"; no UI change: "no UI change, no screenshots".

## Risks
Only add this section when the reviewer must know a real risk (data loss, incompatible behavior, hidden coupling, a test that cannot run here). One line, no lists. Otherwise omit the whole section — most plans do not need it.

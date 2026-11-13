#!/usr/bin/env bash
# End-to-end smoke test for gh-issue-killer.
#
# IMPORTANT: this script intentionally uses the HUMAN `gh` account to simulate
# the repo owner (create issue, approve, request merge). The daemon itself only
# ever uses gh-agent.
#
# Usage:
#   scripts/smoke-test.sh <owner/repo> [issue-title]
#
# Environment:
#   SMOKE_APPROVE=yes   also run the lgtm step (default: yes)
#   SMOKE_MERGE=yes     also run the merge step (default: yes)
#   SMOKE_KEEP=yes      do not close the test issue at the end

set -euo pipefail

REPO="${1:?usage: smoke-test.sh <owner/repo> [title]}"
TITLE="${2:-[smoke] gh-issue-killer end-to-end test}"
POLL_SECONDS="${SMOKE_POLL_SECONDS:-10}"
TIMEOUT_SECONDS="${SMOKE_TIMEOUT_SECONDS:-900}"
: "${SMOKE_APPROVE:=yes}"
: "${SMOKE_MERGE:=yes}"

AGENT_LOGIN="$(GH_CONFIG_DIR="$HOME/.config/gh-agent" gh api user --jq .login)"

elapsed=0
wait_for() {
  local description="$1" predicate="$2" found=""
  elapsed=0
  while [ "$elapsed" -lt "$TIMEOUT_SECONDS" ]; do
    found="$(eval "$predicate" 2>/dev/null || true)"
    if [ -n "$found" ]; then
      echo "✅ $description (after ${elapsed}s)"
      echo "$found"
      return 0
    fi
    sleep "$POLL_SECONDS"
    elapsed=$((elapsed + POLL_SECONDS))
  done
  echo "❌ timed out waiting for: $description" >&2
  return 1
}

echo "==> creating smoke issue in $REPO (mentioning @$AGENT_LOGIN)"
ISSUE_URL="$(
  gh issue create --repo "$REPO" \
    --title "$TITLE" \
    --body "Smoke test for gh-issue-killer. @$AGENT_LOGIN please add a file named SMOKE.md containing the single line \`smoke\` at the repo root, and remove nothing else." \
    --assignee "@me"
)"
ISSUE="$(basename "$ISSUE_URL")"
echo "    issue: $ISSUE_URL"

echo "==> waiting for the agent plan comment"
wait_for "plan comment" \
  "gh issue view $ISSUE --repo $REPO --json comments --jq '.comments[] | select(.author.login == \"$AGENT_LOGIN\" and (.body | contains(\"修复计划\"))) | .url' | head -1"

if [ "$SMOKE_APPROVE" = "yes" ]; then
  echo "==> approving with lgtm"
  gh issue comment "$ISSUE" --repo "$REPO" --body "lgtm"
  echo "==> waiting for the PR comment"
  PR_URL="$(wait_for "PR opened" \
    "gh issue view $ISSUE --repo $REPO --json comments --jq '.comments[] | select(.author.login == \"$AGENT_LOGIN\") | .body' | grep -o 'https://github.com/.*/pull/[0-9]*' | head -1" | tail -1)"
  echo "    pr: $PR_URL"

  if [ "$SMOKE_MERGE" = "yes" ] && [ -n "$PR_URL" ]; then
    echo "==> requesting merge"
    gh issue comment "$ISSUE" --repo "$REPO" --body "合并"
    wait_for "issue closed" \
      "gh issue view $ISSUE --repo $REPO --json state --jq 'select(.state == \"closed\") | .state'"
  fi
fi

if [ "${SMOKE_KEEP:-no}" != "yes" ]; then
  gh issue close "$ISSUE" --repo "$REPO" --reason completed >/dev/null 2>&1 || true
fi

echo "==> smoke test finished for $ISSUE_URL"

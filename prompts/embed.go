// Package prompts embeds the markdown prompts used by the agent stages so the
// binary works without a prompts/ directory beside it.
package prompts

import "embed"

// FS holds plan.md, implement.md, fix.md, revise.md, ci_fix.md,
// conflict_fix.md, review.md and modify_pr.md.
//
//go:embed *.md
var FS embed.FS

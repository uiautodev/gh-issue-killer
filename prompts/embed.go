// Package prompts embeds the markdown prompts used by the agent stages so the
// binary works without a prompts/ directory beside it.
package prompts

import "embed"

// FS holds plan.md, implement.md, fix.md, revise.md and ci_fix.md.
//
//go:embed *.md
var FS embed.FS

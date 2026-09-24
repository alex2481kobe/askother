// Package assets embeds the small marks used by AskOther's local UI.
package assets

import "embed"

// FS contains the AskOther, OpenAI, and Claude marks.
//
//go:embed askother-mark.png openai-mark.png claude-mark.png favicon-32.png
var FS embed.FS

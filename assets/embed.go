// Package assets embeds the small marks used by Orca's local UI.
package assets

import "embed"

// FS contains the Orca, OpenAI, and Claude marks.
//
//go:embed orca-mark.png openai-mark.png claude-mark.png favicon-32.png
var FS embed.FS

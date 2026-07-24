// Package prompts embeds the benchmark prompt suite so `brokemode bench`
// works from any working directory.
package prompts

import "embed"

// FS holds the prompt suite text files.
//
//go:embed *.txt
var FS embed.FS

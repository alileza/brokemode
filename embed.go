// Package brokemode embeds repo-level defaults into the binary so a
// release download is fully self-contained: web dashboard (web/), bench
// prompts (bench/prompts/), and the model registry below.
package brokemode

import _ "embed"

// DefaultModelsYAML is the registry compiled into the binary. It is the
// fallback when no models.yaml exists in the CWD, next to the binary, or
// in ~/.brokemode — and the source install.sh exports on a fresh machine.
//
//go:embed models.yaml
var DefaultModelsYAML []byte

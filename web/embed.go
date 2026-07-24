// Package web embeds the built Vite dashboard so `brokemode serve` works
// with no node_modules (or node at all) present at runtime.
//
// `make build` runs the Vite build first; dist/ must exist when the Go
// binary is compiled.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built SPA rooted at its index.html.
func Dist() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}

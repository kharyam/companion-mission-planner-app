// Package web bundles the kam-transfer admin UI into the Go binary.
// The HTML/CSS/JS lives in static/; go:embed packages it at compile
// time so the released binary has no external runtime assets.
package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var assets embed.FS

// StaticFS exposes the embedded /static tree, rooted at "static/", so
// callers can mount it directly under a URL prefix.
func StaticFS() (fs.FS, error) {
	return fs.Sub(assets, "static")
}

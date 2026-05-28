// Package webui exposes the compiled Vue admin console as an embedded
// filesystem so the Tring API process can serve it on the same port as
// /v1/*. Build-time only — the dist/ subtree is populated by
// `npm run build` in ../../web (or via the Node stage in the Dockerfile).
//
// Loosely coupled: if dist/ is empty (someone built the Go binary without
// running the Vue build), FS() returns (nil, false) and server.go skips
// the /admin/* mount entirely. Backend-only deploys "just work" without
// any flag or build tag — the UI simply isn't there.
package webui

import (
	"embed"
	"io/fs"
)

// The all: prefix includes dotfiles (.gitkeep keeps the directory alive
// when no real build has run yet, so the embed pattern always matches).
//
//go:embed all:dist
var files embed.FS

// FS returns the dist subtree as an io/fs.FS rooted at the dist dir, plus
// a boolean indicating whether the UI was actually built. Callers should
// gate route registration on the boolean.
func FS() (fs.FS, bool) {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		return nil, false
	}
	// Treat "no index.html" as "no UI built" — even with an empty
	// dist/.gitkeep present, there's nothing meaningful to serve.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}

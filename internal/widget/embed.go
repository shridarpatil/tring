// Package widget exposes the compiled browser widget bundles (voice.js,
// agent.js, tring.js) as an embedded filesystem so the Tring API process
// can serve them on the same origin as /v1/*. This means a self-hoster
// needs no CDN or separate static host: the embed snippet's
// `<script src="<your-api>/sdk/voice.js">` just works.
//
// Build-time only — the dist/ subtree is populated by `make sdk` (or the
// Node stage in the Dockerfile), which copies sdk/js/dist/*.iife.js here
// under their served names. Mirrors internal/webui: if dist/ holds only
// the .gitkeep placeholder, FS() returns (nil, false) and server.go skips
// the /sdk/* mount — a Go-only build "just works" without the bundles.
package widget

import (
	"embed"
	"io/fs"
)

// all: includes the .gitkeep so the embed pattern always matches even
// before `make sdk` has populated the real bundles.
//
//go:embed all:dist
var files embed.FS

// FS returns the dist subtree rooted at dist/, plus whether the widget
// bundles were actually built. Callers gate the /sdk/* route on the bool.
func FS() (fs.FS, bool) {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		return nil, false
	}
	// voice.js is the flagship bundle; its presence means a real build ran.
	if _, err := fs.Stat(sub, "voice.js"); err != nil {
		return nil, false
	}
	return sub, true
}

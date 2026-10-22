//go:build opus

package sfu

import (
	"gopkg.in/hraban/opus.v2"
)

// Built with `-tags opus` (and libopus installed), ports can mix: see mix.go.
// Add `nolibopusfile` too: the mixer needs only libopus, not libopusfile.
func init() {
	newOpusDecoder = func() (opusDecoder, error) {
		return opus.NewDecoder(48000, 1)
	}
	newOpusEncoder = func() (opusEncoder, error) {
		return opus.NewEncoder(48000, 1, opus.AppVoIP)
	}
}

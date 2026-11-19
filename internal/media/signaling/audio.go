package signaling

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/shridarpatil/tring/internal/media/prompt"
	"github.com/shridarpatil/tring/internal/media/room"
)

// phoneCodec is the codec of a phone room Tring dials out from (click-to-call,
// connect, campaigns): there is no carrier offer yet to take the law from.
// A-law is what Indian (and most non-US) carriers use; FreeSWITCH's carrier
// bridge falls back to µ-law, at the cost of a cheap A↔µ transcode there.
// Inbound phone rooms take the carrier's own law instead (pstn.go).
const phoneCodec = room.PCMA

// g711CacheMax bounds the decoded files kept. Prompts and hold music are a
// tenant's sound library, a few dozen files; past this the cache starts over.
const g711CacheMax = 256

// g711Cache keeps audio files decoded to G.711 frames. Prompts and hold music
// are stored as Ogg/Opus, and decoding them runs ffmpeg — fine once per file,
// not once per call when a hundred queued callers hear the same hold music.
// The zero value is ready to use.
type g711Cache struct {
	mu sync.Mutex
	m  map[g711Key][][]byte
}

// g711Key changes when the file does, so a replaced upload is decoded again.
type g711Key struct {
	path  string
	codec room.Codec
	mod   time.Time
	size  int64
}

// loadAudio loads an audio file under dir as frames in codec c: Opus packets
// straight from the Ogg file, or G.711 frames decoded once through the cache.
// file may be an absolute sound-library path (<dir>/<tenant>/<uuid>.ogg) or a
// relative custom path; either way the open is confined to dir (no traversal
// out). Shared by IVR prompts, hold music and the transfer messages.
func loadAudio(cache *g711Cache, dir, file string, c room.Codec) ([][]byte, error) {
	path := filepath.Join(dir, filepath.Clean("/"+strings.TrimPrefix(file, dir)))
	if !c.G711() {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return prompt.LoadOggOpus(f)
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	key := g711Key{path: path, codec: c, mod: st.ModTime(), size: st.Size()}
	cache.mu.Lock()
	frames, ok := cache.m[key]
	cache.mu.Unlock()
	if ok {
		return frames, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if frames, err = prompt.LoadG711(ctx, path, c); err != nil {
		return nil, err
	}
	cache.mu.Lock()
	if cache.m == nil || len(cache.m) >= g711CacheMax {
		cache.m = make(map[g711Key][][]byte)
	}
	cache.m[key] = frames
	cache.mu.Unlock()
	return frames, nil
}

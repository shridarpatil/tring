package dbtest

import (
	"crypto/sha256"
	"encoding/hex"
)

// sha256Hex mirrors api.HashKey but lives here to avoid an import cycle
// (api package imports dbtest in *_test.go files).
func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

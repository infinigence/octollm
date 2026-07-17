package cache

import (
	"crypto/sha256"
	"encoding/hex"
)

// KeyForURL returns a stable path-safe key (SHA-256 hex) for a remote image URL.
func KeyForURL(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:])
}

// KeyDeriver maps a remote image URL to a stable cache key. Implementations must be
// pure functions of rawURL: the same URL must always yield the same key, so that the
// same image shares one cache entry regardless of which request references it.
type KeyDeriver interface {
	Key(rawURL string) string
}

// URLKeyDeriver is the default deriver: plain SHA-256 of the raw URL with no
// canonicalization. Used when no custom KeyDeriver is supplied.
type URLKeyDeriver struct{}

func (URLKeyDeriver) Key(rawURL string) string { return KeyForURL(rawURL) }

var _ KeyDeriver = URLKeyDeriver{}

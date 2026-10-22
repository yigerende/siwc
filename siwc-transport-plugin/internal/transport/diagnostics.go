package transport

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

const recentCacheKeyLimit = 30

// Cache key sources reported in diagnostics.
const (
	CacheKeySourceClient      = "client"
	CacheKeySourceSynthesized = "synthesized"
	CacheKeySourceNone        = "none"
)

// CacheKeySample records which prompt_cache_key a SIWC request was sent with.
// Only a short digest of the key is kept so the status page never exposes it.
type CacheKeySample struct {
	At     int64  `json:"at"`
	Source string `json:"source"`
	Key8   string `json:"key8,omitempty"`
	Items  int64  `json:"items"`
}

func newCacheKeySample(source, key string, items int64) CacheKeySample {
	sample := CacheKeySample{At: time.Now().Unix(), Source: source, Items: items}
	if key != "" {
		digest := sha256.Sum256([]byte(key))
		sample.Key8 = hex.EncodeToString(digest[:4])
	}
	return sample
}

// cacheKeyRing keeps the most recent samples, oldest first on snapshot.
type cacheKeyRing struct {
	mu      sync.Mutex
	samples []CacheKeySample
	next    int
}

func (ring *cacheKeyRing) add(sample CacheKeySample) {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	if len(ring.samples) < recentCacheKeyLimit {
		ring.samples = append(ring.samples, sample)
		return
	}
	ring.samples[ring.next] = sample
	ring.next = (ring.next + 1) % recentCacheKeyLimit
}

func (ring *cacheKeyRing) snapshot() []CacheKeySample {
	ring.mu.Lock()
	defer ring.mu.Unlock()
	out := make([]CacheKeySample, 0, len(ring.samples))
	out = append(out, ring.samples[ring.next:]...)
	return append(out, ring.samples[:ring.next]...)
}

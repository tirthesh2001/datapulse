package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type entry struct {
	body    []byte
	expires time.Time
}

// Response caches raw HTML/JSON response bodies with TTL.
type Response struct {
	mu       sync.Mutex
	ttl      time.Duration
	entries  map[string]entry
}

func NewResponse(ttl time.Duration) *Response {
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &Response{ttl: ttl, entries: make(map[string]entry)}
}

func (c *Response) Get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || time.Now().After(e.expires) {
		delete(c.entries, key)
		return nil, false
	}
	return e.body, true
}

func (c *Response) Set(key string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]byte, len(body))
	copy(cp, body)
	c.entries[key] = entry{body: cp, expires: time.Now().Add(c.ttl)}
}

func (c *Response) InvalidatePrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.entries {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(c.entries, k)
		}
	}
}

func (c *Response) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]entry)
}

// Key builds a stable cache key from parts.
func Key(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

package api

import (
	"sync"
	"time"
)

// loginGuard is a lightweight in-memory failed-login tracker. After too many
// failures for a given key (username) within the window, further attempts are
// refused for a lockout period — a per-process brute-force backstop on top of
// the per-IP rate limiter (which an attacker with many source IPs can spread
// across). It is intentionally simple: state is per-process and not shared
// across replicas; a persistent/clustered store would be the next step.
type loginGuard struct {
	mu          sync.Mutex
	records     map[string]*attemptRecord
	maxFailures int
	lockout     time.Duration
	window      time.Duration
}

type attemptRecord struct {
	failures    int
	first       time.Time
	lockedUntil time.Time
}

func newLoginGuard() *loginGuard {
	g := &loginGuard{
		records:     make(map[string]*attemptRecord),
		maxFailures: 10,
		lockout:     15 * time.Minute,
		window:      15 * time.Minute,
	}
	go g.reap()
	return g
}

// blocked reports whether the key is currently locked out.
func (g *loginGuard) blocked(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	r, ok := g.records[key]
	if !ok {
		return false
	}
	return time.Now().Before(r.lockedUntil)
}

// fail records a failed attempt and returns true if the key is now locked out.
func (g *loginGuard) fail(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	r, ok := g.records[key]
	if !ok || now.Sub(r.first) > g.window {
		r = &attemptRecord{first: now}
		g.records[key] = r
	}
	r.failures++
	if r.failures >= g.maxFailures {
		r.lockedUntil = now.Add(g.lockout)
		return true
	}
	return false
}

// reset clears the record for a key (call on successful login).
func (g *loginGuard) reset(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.records, key)
}

func (g *loginGuard) reap() {
	ticker := time.NewTicker(g.window)
	defer ticker.Stop()
	for range ticker.C {
		g.mu.Lock()
		now := time.Now()
		for k, r := range g.records {
			if now.After(r.lockedUntil) && now.Sub(r.first) > g.window {
				delete(g.records, k)
			}
		}
		g.mu.Unlock()
	}
}

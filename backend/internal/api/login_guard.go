package api

import (
	"sync"
	"time"
)

// loginGuard is a lightweight in-memory failed-login tracker, a per-process
// brute-force backstop on top of the per-IP rate limiter. State is per-process
// and not shared across replicas; a persistent/clustered store would be the
// next step.
//
// Two layers, so that an attacker cannot lock a victim (e.g. the admin) out by
// deliberately failing logins with their username:
//
//   - per (username, client IP): after maxFailures failures within the window
//     that pair is locked for the lockout period. Other sources are unaffected.
//   - per username, across all IPs: a much higher ceiling (globalThreshold)
//     for distributed guessing. Beyond it the username is never hard-locked;
//     instead attempts are throttled with exponential backoff (capped at
//     maxBackoff), so the legitimate user can still get in while a botnet is
//     slowed to a crawl.
type loginGuard struct {
	mu      sync.Mutex
	pairs   map[string]*attemptRecord // username + "\x00" + ip
	globals map[string]*attemptRecord // username

	maxFailures     int
	lockout         time.Duration
	window          time.Duration
	globalThreshold int
	baseBackoff     time.Duration
	maxBackoff      time.Duration
}

type attemptRecord struct {
	failures    int
	first       time.Time
	lockedUntil time.Time
}

func newLoginGuard() *loginGuard {
	g := defaultLoginGuard()
	go g.reap()
	return g
}

func defaultLoginGuard() *loginGuard {
	return &loginGuard{
		pairs:           make(map[string]*attemptRecord),
		globals:         make(map[string]*attemptRecord),
		maxFailures:     10,
		lockout:         15 * time.Minute,
		window:          15 * time.Minute,
		globalThreshold: 100,
		baseBackoff:     time.Second,
		maxBackoff:      time.Minute,
	}
}

func pairKey(username, ip string) string { return username + "\x00" + ip }

// blocked reports whether a login for username from ip must be refused now.
func (g *loginGuard) blocked(username, ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if r, ok := g.pairs[pairKey(username, ip)]; ok && now.Before(r.lockedUntil) {
		return true
	}
	if r, ok := g.globals[username]; ok && now.Before(r.lockedUntil) {
		return true
	}
	return false
}

// fail records a failed attempt and returns true if this source is now locked
// out (or the username is being throttled).
func (g *loginGuard) fail(username, ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()

	pr := g.record(g.pairs, pairKey(username, ip), now)
	pr.failures++
	locked := false
	if pr.failures >= g.maxFailures {
		pr.lockedUntil = now.Add(g.lockout)
		locked = true
	}

	gr := g.record(g.globals, username, now)
	gr.failures++
	if over := gr.failures - g.globalThreshold; over >= 0 {
		backoff := g.maxBackoff
		if over < 32 {
			if b := g.baseBackoff << uint(over); b > 0 && b < g.maxBackoff {
				backoff = b
			}
		}
		gr.lockedUntil = now.Add(backoff)
		locked = true
	}
	return locked
}

func (g *loginGuard) record(m map[string]*attemptRecord, key string, now time.Time) *attemptRecord {
	r, ok := m[key]
	if !ok || (now.Sub(r.first) > g.window && !now.Before(r.lockedUntil)) {
		r = &attemptRecord{first: now}
		m[key] = r
	}
	return r
}

// reset clears the (username, ip) record after a successful login. The
// per-username counter is deliberately left to age out, so a distributed
// attacker cannot reset it by timing guesses around the real user's logins.
func (g *loginGuard) reset(username, ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.pairs, pairKey(username, ip))
}

func (g *loginGuard) reap() {
	ticker := time.NewTicker(g.window)
	defer ticker.Stop()
	for range ticker.C {
		g.mu.Lock()
		now := time.Now()
		for _, m := range []map[string]*attemptRecord{g.pairs, g.globals} {
			for k, r := range m {
				if now.After(r.lockedUntil) && now.Sub(r.first) > g.window {
					delete(m, k)
				}
			}
		}
		g.mu.Unlock()
	}
}

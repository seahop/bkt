package api

import (
	"crypto/sha256"
	"log"
	"strings"
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
//     that pair is HARD locked for the lockout period (refused without even
//     checking the password). Other sources are unaffected.
//   - per username, across all IPs: a much higher ceiling (globalThreshold)
//     for distributed guessing. Beyond it the username is THROTTLED with
//     exponential backoff (capped at maxBackoff). The throttle never blocks a
//     correct password: while it is active the password is still verified,
//     a correct one logs in and a wrong one is answered 429. To keep the
//     throttle meaningful against a botnet, each source gets only
//     throttledMaxFailures wrong guesses (instead of maxFailures) before its
//     (username, IP) pair is hard-locked. A (username, IP) pair that logged in
//     successfully within trustTTL is exempt from the throttle altogether.
//     A successful login never resets the per-username counter (it ages out
//     with the window), so an attacker cannot clear it by timing guesses
//     around the real user's logins — or by knowing one valid password.
//
// Memory is bounded: usernames are stored only as a SHA-256 of the
// normalized (trimmed, lower-cased) name, IPs are length-capped, and every map
// holds at most maxEntries records (expired ones are purged first, then the
// oldest unlocked record is evicted; if everything is locked the new attempt
// is simply not tracked).
type loginGuard struct {
	mu      sync.Mutex
	pairs   map[guardPair]*attemptRecord // (username hash, ip)
	globals map[guardUser]*attemptRecord // username hash
	trusted map[guardPair]time.Time      // last successful login per (username, ip)

	maxFailures          int
	throttledMaxFailures int
	lockout              time.Duration
	window               time.Duration
	globalThreshold      int
	baseBackoff          time.Duration
	maxBackoff           time.Duration
	trustTTL             time.Duration
	maxEntries           int
}

// guardUser is the SHA-256 of the normalized username; the guard never keeps
// the (attacker-supplied, possibly large) name itself.
type guardUser [sha256.Size]byte

type guardPair struct {
	user guardUser
	ip   string
}

// maxGuardIPLen bounds the IP component of a key (ClientIP yields validated
// addresses, this is defence in depth).
const maxGuardIPLen = 64

func newGuardUser(username string) guardUser {
	return sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(username))))
}

func newGuardPair(username, ip string) guardPair {
	if len(ip) > maxGuardIPLen {
		ip = ip[:maxGuardIPLen]
	}
	return guardPair{user: newGuardUser(username), ip: ip}
}

// guardDecision is the pre-authentication verdict for a login attempt.
type guardDecision int

const (
	// guardAllow: verify the password normally.
	guardAllow guardDecision = iota
	// guardThrottled: the per-username throttle is active. Verify the
	// password; admit a correct one, answer a wrong one with 429.
	guardThrottled
	// guardLocked: the (username, IP) pair is hard-locked; refuse outright.
	guardLocked
)

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
		pairs:                make(map[guardPair]*attemptRecord),
		globals:              make(map[guardUser]*attemptRecord),
		trusted:              make(map[guardPair]time.Time),
		maxFailures:          10,
		throttledMaxFailures: 3,
		lockout:              15 * time.Minute,
		window:               15 * time.Minute,
		globalThreshold:      100,
		baseBackoff:          time.Second,
		maxBackoff:           time.Minute,
		trustTTL:             30 * 24 * time.Hour,
		maxEntries:           100_000,
	}
}

// check returns the verdict for a login attempt for username from ip, before
// the password is verified.
func (g *loginGuard) check(username, ip string) guardDecision {
	id := newGuardPair(username, ip)
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if r, ok := g.pairs[id]; ok && now.Before(r.lockedUntil) {
		return guardLocked
	}
	if g.throttledLocked(id, now) {
		return guardThrottled
	}
	return guardAllow
}

// throttledLocked reports whether the per-username throttle applies to this
// (username, ip) pair right now. Caller holds g.mu.
func (g *loginGuard) throttledLocked(id guardPair, now time.Time) bool {
	r, ok := g.globals[id.user]
	if !ok || !now.Before(r.lockedUntil) {
		return false
	}
	if last, ok := g.trusted[id]; ok && now.Sub(last) < g.trustTTL {
		return false // this source has logged in as this user before
	}
	return true
}

// fail records a failed attempt and returns true if this source is now locked
// out (or the username is being throttled).
func (g *loginGuard) fail(username, ip string) bool {
	id := newGuardPair(username, ip)
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()

	// Under a distributed attack each source gets fewer guesses.
	threshold := g.maxFailures
	if g.throttledLocked(id, now) && g.throttledMaxFailures > 0 && g.throttledMaxFailures < threshold {
		threshold = g.throttledMaxFailures
	}

	locked := false
	if pr := trackRecord(g, g.pairs, id, now); pr != nil {
		pr.failures++
		if pr.failures >= threshold {
			pr.lockedUntil = now.Add(g.lockout)
			locked = true
		}
	}

	if gr := trackRecord(g, g.globals, id.user, now); gr != nil {
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
	}
	return locked
}

// trackRecord returns the live record for key, creating (or restarting an
// expired) one. It returns nil when the map is full of locked records and the
// attempt cannot be tracked. Caller holds g.mu.
func trackRecord[K comparable](g *loginGuard, m map[K]*attemptRecord, key K, now time.Time) *attemptRecord {
	r, ok := m[key]
	if ok && (now.Sub(r.first) <= g.window || now.Before(r.lockedUntil)) {
		return r
	}
	if !ok && !makeRoom(g, m, now) {
		return nil
	}
	r = &attemptRecord{first: now}
	m[key] = r
	return r
}

// makeRoom ensures m can take one more record: expired records are purged
// first, then the oldest unlocked record is evicted. Locked records are never
// evicted (that would let an attacker flush their own lock by flooding the
// map). Returns false if no room could be made. Caller holds g.mu.
func makeRoom[K comparable](g *loginGuard, m map[K]*attemptRecord, now time.Time) bool {
	if g.maxEntries <= 0 || len(m) < g.maxEntries {
		return true
	}
	purgeExpired(g, m, now)
	if len(m) < g.maxEntries {
		return true
	}
	var oldestKey K
	var oldest *attemptRecord
	for k, r := range m {
		if now.Before(r.lockedUntil) {
			continue
		}
		if oldest == nil || r.first.Before(oldest.first) {
			oldestKey, oldest = k, r
		}
	}
	if oldest == nil {
		log.Printf("login guard: tracking table full (%d locked entries); attempt not tracked", len(m))
		return false
	}
	delete(m, oldestKey)
	return true
}

func purgeExpired[K comparable](g *loginGuard, m map[K]*attemptRecord, now time.Time) {
	for k, r := range m {
		if !now.Before(r.lockedUntil) && now.Sub(r.first) > g.window {
			delete(m, k)
		}
	}
}

// succeed clears the (username, ip) failure record after a successful login
// and remembers the pair as trusted (exempt from the per-username throttle for
// trustTTL). The per-username counter is deliberately left to age out, so a
// distributed attacker cannot reset it by timing guesses around the real
// user's logins.
func (g *loginGuard) succeed(username, ip string) {
	id := newGuardPair(username, ip)
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	delete(g.pairs, id)
	if _, ok := g.trusted[id]; !ok && g.maxEntries > 0 && len(g.trusted) >= g.maxEntries {
		g.purgeTrusted(now)
		if len(g.trusted) >= g.maxEntries {
			var oldestKey guardPair
			var oldest time.Time
			first := true
			for k, t := range g.trusted {
				if first || t.Before(oldest) {
					oldestKey, oldest, first = k, t, false
				}
			}
			delete(g.trusted, oldestKey)
		}
	}
	g.trusted[id] = now
}

func (g *loginGuard) purgeTrusted(now time.Time) {
	for k, t := range g.trusted {
		if now.Sub(t) >= g.trustTTL {
			delete(g.trusted, k)
		}
	}
}

func (g *loginGuard) reap() {
	ticker := time.NewTicker(g.window)
	defer ticker.Stop()
	for range ticker.C {
		g.mu.Lock()
		now := time.Now()
		purgeExpired(g, g.pairs, now)
		purgeExpired(g, g.globals, now)
		g.purgeTrusted(now)
		g.mu.Unlock()
	}
}

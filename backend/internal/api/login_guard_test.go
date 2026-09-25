package api

import (
	"fmt"
	"testing"
	"time"
)

func testGuard() *loginGuard {
	g := defaultLoginGuard()
	g.maxFailures = 3
	g.lockout = time.Hour
	g.window = time.Hour
	g.globalThreshold = 10
	g.baseBackoff = time.Hour // make throttling observable without sleeping
	g.maxBackoff = time.Hour
	return g
}

func TestLoginGuardLocksOutAfterThreshold(t *testing.T) {
	g := testGuard()
	user, ip := "alice", "203.0.113.5"
	if g.blocked(user, ip) {
		t.Fatal("should not start blocked")
	}
	// First maxFailures-1 fails do not lock.
	for i := 0; i < 2; i++ {
		if locked := g.fail(user, ip); locked {
			t.Fatalf("locked too early on attempt %d", i+1)
		}
	}
	// The threshold failure locks.
	if locked := g.fail(user, ip); !locked {
		t.Fatal("expected lockout at threshold")
	}
	if !g.blocked(user, ip) {
		t.Fatal("expected key to be blocked after lockout")
	}
	// Reset clears it.
	g.reset(user, ip)
	if g.blocked(user, ip) {
		t.Fatal("reset should clear the lockout")
	}
}

// An attacker failing logins for "admin" from their own IP must not lock the
// real admin out when they log in from elsewhere.
func TestLoginGuardLockoutIsPerSource(t *testing.T) {
	g := testGuard()
	for i := 0; i < 5; i++ {
		g.fail("admin", "198.51.100.66")
	}
	if !g.blocked("admin", "198.51.100.66") {
		t.Fatal("attacker source should be locked")
	}
	if g.blocked("admin", "192.0.2.10") {
		t.Fatal("legitimate admin from another IP must not be locked out")
	}
}

// Distributed guessing across many IPs trips the per-username ceiling, which
// throttles (temporary backoff) rather than permanently locking.
func TestLoginGuardGlobalCeilingThrottles(t *testing.T) {
	g := testGuard()
	for i := 0; i < 9; i++ {
		g.fail("admin", fmt.Sprintf("10.0.0.%d", i))
	}
	if g.blocked("admin", "192.0.2.10") {
		t.Fatal("below the global ceiling nobody else is affected")
	}
	g.fail("admin", "10.0.1.1")
	if !g.blocked("admin", "192.0.2.10") {
		t.Fatal("above the global ceiling the username is throttled")
	}

	// Backoff is bounded: with a short maxBackoff the throttle lapses.
	g2 := testGuard()
	g2.baseBackoff = time.Millisecond
	g2.maxBackoff = 20 * time.Millisecond
	for i := 0; i < 40; i++ {
		g2.fail("admin", fmt.Sprintf("10.0.2.%d", i))
	}
	time.Sleep(30 * time.Millisecond)
	if g2.blocked("admin", "192.0.2.10") {
		t.Fatal("throttle must be temporary (capped backoff), not a hard lock")
	}
}

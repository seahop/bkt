package api

import (
	"testing"
	"time"
)

func TestLoginGuardLocksOutAfterThreshold(t *testing.T) {
	g := &loginGuard{
		records:     make(map[string]*attemptRecord),
		maxFailures: 3,
		lockout:     time.Hour,
		window:      time.Hour,
	}
	key := "alice"
	if g.blocked(key) {
		t.Fatal("should not start blocked")
	}
	// First maxFailures-1 fails do not lock.
	for i := 0; i < 2; i++ {
		if locked := g.fail(key); locked {
			t.Fatalf("locked too early on attempt %d", i+1)
		}
	}
	// The threshold failure locks.
	if locked := g.fail(key); !locked {
		t.Fatal("expected lockout at threshold")
	}
	if !g.blocked(key) {
		t.Fatal("expected key to be blocked after lockout")
	}
	// Reset clears it.
	g.reset(key)
	if g.blocked(key) {
		t.Fatal("reset should clear the lockout")
	}
}

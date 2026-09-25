package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
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
	if g.check(user, ip) != guardAllow {
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
	if g.check(user, ip) != guardLocked {
		t.Fatal("expected key to be hard-locked after lockout")
	}
	// A successful login clears it.
	g.succeed(user, ip)
	if g.check(user, ip) != guardAllow {
		t.Fatal("success should clear the lockout")
	}
}

// An attacker failing logins for "admin" from their own IP must not lock the
// real admin out when they log in from elsewhere.
func TestLoginGuardLockoutIsPerSource(t *testing.T) {
	g := testGuard()
	for i := 0; i < 5; i++ {
		g.fail("admin", "198.51.100.66")
	}
	if g.check("admin", "198.51.100.66") != guardLocked {
		t.Fatal("attacker source should be locked")
	}
	if g.check("admin", "192.0.2.10") != guardAllow {
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
	if g.check("admin", "192.0.2.10") != guardAllow {
		t.Fatal("below the global ceiling nobody else is affected")
	}
	g.fail("admin", "10.0.1.1")
	if g.check("admin", "192.0.2.10") != guardThrottled {
		t.Fatal("above the global ceiling the username is throttled (not hard-locked)")
	}

	// Backoff is bounded: with a short maxBackoff the throttle lapses.
	g2 := testGuard()
	g2.baseBackoff = time.Millisecond
	g2.maxBackoff = 20 * time.Millisecond
	for i := 0; i < 40; i++ {
		g2.fail("admin", fmt.Sprintf("10.0.2.%d", i))
	}
	time.Sleep(30 * time.Millisecond)
	if g2.check("admin", "192.0.2.10") != guardAllow {
		t.Fatal("throttle must be temporary (capped backoff), not a hard lock")
	}
}

// The attack from the review: ~10 IPs keep the global per-username throttle
// armed forever. The real admin must still get in with the correct password
// (the handler verifies it on guardThrottled), and once they have logged in
// from a source that source is exempt from the throttle.
func TestLoginGuardThrottleDoesNotLockOutCorrectPassword(t *testing.T) {
	g := testGuard()
	for i := 0; i < 20; i++ {
		g.fail("admin", fmt.Sprintf("10.0.3.%d", i%10))
	}
	adminIP := "192.0.2.10"
	if d := g.check("admin", adminIP); d != guardThrottled {
		t.Fatalf("expected throttled (password still verified), got %v", d)
	}
	// Correct password → handler calls succeed; the pair becomes trusted.
	g.succeed("admin", adminIP)
	if d := g.check("admin", adminIP); d != guardAllow {
		t.Fatalf("trusted source must bypass the throttle, got %v", d)
	}
	// The per-username counter was NOT reset: other sources stay throttled.
	if d := g.check("admin", "198.51.100.1"); d != guardThrottled {
		t.Fatalf("success must not reset the global throttle, got %v", d)
	}
	// Trust expires.
	g.trustTTL = time.Nanosecond
	time.Sleep(time.Millisecond)
	if d := g.check("admin", adminIP); d != guardThrottled {
		t.Fatalf("expired trust should no longer bypass, got %v", d)
	}
}

// While the throttle is active each source gets only throttledMaxFailures
// wrong guesses before a hard lock, so verifying passwords under the throttle
// does not hand a botnet full-speed guessing.
func TestLoginGuardThrottleTightensPerSourceLimit(t *testing.T) {
	g := testGuard()
	g.maxFailures = 10
	g.throttledMaxFailures = 2
	for i := 0; i < 10; i++ {
		g.fail("admin", fmt.Sprintf("10.0.4.%d", i))
	}
	ip := "198.51.100.7"
	if g.check("admin", ip) != guardThrottled {
		t.Fatal("expected throttle")
	}
	g.fail("admin", ip)
	if g.check("admin", ip) != guardThrottled {
		t.Fatal("one failure should not hard-lock")
	}
	g.fail("admin", ip)
	if g.check("admin", ip) != guardLocked {
		t.Fatal("throttledMaxFailures failures should hard-lock the source")
	}
}

func TestLoginGuardKeysAreHashedAndNormalized(t *testing.T) {
	g := testGuard()
	long := strings.Repeat("x", 10000)
	g.fail(long, "10.0.0.1")
	for k := range g.globals {
		if len(k) != 32 {
			t.Fatalf("key length %d", len(k))
		}
	}
	for k := range g.pairs {
		if strings.Contains(k.ip, "x") {
			t.Fatal("username leaked into pair key")
		}
	}
	// "  Admin" and "admin" share a record.
	for i := 0; i < 3; i++ {
		g.fail("  Admin", "10.0.0.2")
	}
	if g.check("admin", "10.0.0.2") != guardLocked {
		t.Fatal("normalization: case/space variants must share the lock")
	}
	// Oversized IPs are truncated.
	g.fail("bob", strings.Repeat("1", 1000))
	for k := range g.pairs {
		if len(k.ip) > maxGuardIPLen {
			t.Fatalf("ip key not bounded: %d", len(k.ip))
		}
	}
}

func TestLoginGuardMapsAreBounded(t *testing.T) {
	g := testGuard()
	g.maxEntries = 5
	for i := 0; i < 50; i++ {
		g.fail(fmt.Sprintf("user%d", i), "10.0.0.1")
	}
	if len(g.pairs) > 5 || len(g.globals) > 5 {
		t.Fatalf("maps grew past cap: pairs=%d globals=%d", len(g.pairs), len(g.globals))
	}

	// Locked records are never evicted to make room.
	g2 := testGuard()
	g2.maxEntries = 3
	g2.maxFailures = 1
	for i := 0; i < 3; i++ {
		g2.fail("victim", fmt.Sprintf("10.9.0.%d", i))
	}
	for i := 0; i < 20; i++ {
		g2.fail(fmt.Sprintf("flood%d", i), "10.8.0.1")
	}
	for i := 0; i < 3; i++ {
		if g2.check("victim", fmt.Sprintf("10.9.0.%d", i)) != guardLocked {
			t.Fatalf("flooding evicted a locked record (%d)", i)
		}
	}

	// Trusted set is bounded too.
	g3 := testGuard()
	g3.maxEntries = 4
	for i := 0; i < 20; i++ {
		g3.succeed("u", fmt.Sprintf("10.7.0.%d", i))
	}
	if len(g3.trusted) > 4 {
		t.Fatalf("trusted grew past cap: %d", len(g3.trusted))
	}
}

func TestTruncateForAudit(t *testing.T) {
	if got := truncateForAudit("alice"); got != "alice" {
		t.Fatal(got)
	}
	s := strings.Repeat("é", 300) // 600 bytes
	got := truncateForAudit(s)
	if len(got) > maxAuditUsernameLen || !utf8.ValidString(got) {
		t.Fatalf("bad truncation: len=%d valid=%v", len(got), utf8.ValidString(got))
	}
}

func TestLoginRejectsOversizedInputBeforeDB(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AuthHandler{loginGuard: defaultLoginGuard()}
	r := gin.New()
	r.POST("/login", h.Login)
	cases := map[string]string{
		"long username": `{"username":"` + strings.Repeat("a", 256) + `","password":"x"}`,
		"long password": `{"username":"a","password":"` + strings.Repeat("p", 1025) + `"}`,
		"huge body":     `{"username":"a","password":"b","pad":"` + strings.Repeat("z", 100<<10) + `"}`,
	}
	for name, body := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, w.Code)
		}
	}
	if len(h.loginGuard.pairs) != 0 || len(h.loginGuard.globals) != 0 {
		t.Fatal("rejected requests must not be tracked")
	}
}

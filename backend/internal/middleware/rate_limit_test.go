package middleware

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsUpToRateThenDenies(t *testing.T) {
	rl := NewRateLimiter(3, time.Minute)
	ip := "1.2.3.4"
	for i := 0; i < 3; i++ {
		if !rl.Allow(ip) {
			t.Fatalf("request %d should have been allowed", i+1)
		}
	}
	if rl.Allow(ip) {
		t.Fatal("4th request should have been denied")
	}
	// A different key has its own bucket.
	if !rl.Allow("5.6.7.8") {
		t.Fatal("distinct key should be allowed")
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	rl := NewRateLimiter(60, time.Minute) // 1 token/sec
	ip := "9.9.9.9"
	// Drain the bucket.
	for i := 0; i < 60; i++ {
		rl.Allow(ip)
	}
	if rl.Allow(ip) {
		t.Fatal("bucket should be drained")
	}
	// Simulate ~2 seconds passing by rewinding lastRefill.
	rl.mu.Lock()
	rl.clients[ip].lastRefill = time.Now().Add(-2 * time.Second)
	rl.mu.Unlock()
	if !rl.Allow(ip) {
		t.Fatal("should have refilled at least one token after 2s")
	}
}

package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// maxRateLimiterClients bounds the client map so a flood of distinct keys
// (e.g. spoofed source addresses) cannot exhaust memory. When the cap is hit we
// evict stale entries inline before admitting a new one.
const maxRateLimiterClients = 50000

// RateLimiter implements a simple token bucket rate limiter
type RateLimiter struct {
	mu       sync.RWMutex
	clients  map[string]*bucket
	rate     int           // requests per window
	window   time.Duration // time window
	cleanup  time.Duration // cleanup interval
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// NewRateLimiter creates a new rate limiter
// rate: maximum number of requests per window
// window: time window duration (e.g., 1 minute)
func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		clients: make(map[string]*bucket),
		rate:    rate,
		window:  window,
		cleanup: window * 2, // cleanup stale entries periodically
	}

	// Start background cleanup goroutine
	go rl.cleanupRoutine()

	return rl
}

// Allow checks if a request from the given IP should be allowed
func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	// Get or create bucket for this IP
	b, exists := rl.clients[ip]
	if !exists {
		if len(rl.clients) >= maxRateLimiterClients {
			rl.evictForSpaceLocked(now)
		}
		b = &bucket{
			tokens:     float64(rl.rate) - 1, // consume one token immediately
			lastRefill: now,
		}
		rl.clients[ip] = b
		return true
	}

	// Proportional token-bucket refill: add rate tokens per full window, scaled
	// by the fraction of a window that has elapsed (so a burst can't cross the
	// window boundary and get a full double allowance).
	elapsed := now.Sub(b.lastRefill)
	if elapsed > 0 {
		b.tokens += float64(rl.rate) * (elapsed.Seconds() / rl.window.Seconds())
		if b.tokens > float64(rl.rate) {
			b.tokens = float64(rl.rate)
		}
		b.lastRefill = now
	}

	if b.tokens >= 1 {
		b.tokens--
		return true
	}

	return false
}

// evictForSpaceLocked frees at least one map slot so the cap is a real bound
// even under a live flood of distinct client keys: it drops every stale entry
// and, when none are stale, the least-recently-refilled entry. The caller must
// hold rl.mu.
func (rl *RateLimiter) evictForSpaceLocked(now time.Time) {
	var oldestIP string
	var oldestSeen time.Time
	evicted := false
	for ip, b := range rl.clients {
		if now.Sub(b.lastRefill) > rl.cleanup {
			delete(rl.clients, ip)
			evicted = true
			continue
		}
		if oldestIP == "" || b.lastRefill.Before(oldestSeen) {
			oldestIP, oldestSeen = ip, b.lastRefill
		}
	}
	if !evicted && oldestIP != "" {
		delete(rl.clients, oldestIP)
	}
}

// cleanupRoutine periodically removes stale entries
func (rl *RateLimiter) cleanupRoutine() {
	ticker := time.NewTicker(rl.cleanup)
	defer ticker.Stop()

	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, b := range rl.clients {
			// Remove entries that haven't been accessed in 2x the window
			if now.Sub(b.lastRefill) > rl.cleanup {
				delete(rl.clients, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// RateLimitMiddleware creates a Gin middleware for rate limiting
// Common configurations:
// - Login: 5 requests per minute per IP
// - API: 100 requests per minute per IP
// - General: 60 requests per minute per IP
func RateLimitMiddleware(rate int, window time.Duration) gin.HandlerFunc {
	limiter := NewRateLimiter(rate, window)

	return func(c *gin.Context) {
		// Get client IP (consider X-Forwarded-For if behind proxy)
		ip := c.ClientIP()

		if !limiter.Allow(ip) {
			c.JSON(http.StatusTooManyRequests, gin.H{
				"error":   "Rate limit exceeded",
				"message": "Too many requests. Please try again later.",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

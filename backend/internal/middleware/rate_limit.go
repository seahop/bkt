package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// RateLimiter implements a simple token bucket rate limiter
type RateLimiter struct {
	mu       sync.RWMutex
	clients  map[string]*bucket
	rate     int           // requests per window
	window   time.Duration // time window
	cleanup  time.Duration // cleanup interval
}

type bucket struct {
	tokens     int
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
		b = &bucket{
			tokens:     rl.rate - 1, // consume one token immediately
			lastRefill: now,
		}
		rl.clients[ip] = b
		return true
	}

	// Calculate tokens to add based on time elapsed
	elapsed := now.Sub(b.lastRefill)
	tokensToAdd := int(elapsed / rl.window * time.Duration(rl.rate))

	if tokensToAdd > 0 {
		b.tokens = min(rl.rate, b.tokens+tokensToAdd)
		b.lastRefill = now
	}

	// Check if we have tokens available
	if b.tokens > 0 {
		b.tokens--
		return true
	}

	return false
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
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

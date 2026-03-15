package gateway

import (
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
)

const maxVisitors = 100_000

type visitor struct {
	tokens     float64
	lastSeen   time.Time
	maxTokens  float64
	refillRate float64
}

type RateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rps      float64
	burst    int
	exempt   map[string]bool
	done     chan struct{}
}

func NewRateLimiter(rps int, burst int, exempt []string) *RateLimiter {
	exemptMap := make(map[string]bool, len(exempt))
	for _, m := range exempt {
		exemptMap[m] = true
	}
	rl := &RateLimiter{
		visitors: make(map[string]*visitor),
		rps:      float64(rps),
		burst:    burst,
		exempt:   exemptMap,
		done:     make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *RateLimiter) Stop() {
	close(rl.done)
}

func (rl *RateLimiter) Allow(clientIP, method string) bool {
	if rl.rps <= 0 {
		return true
	}
	if rl.exempt[method] {
		return true
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, ok := rl.visitors[clientIP]
	now := time.Now()
	if !ok {
		if len(rl.visitors) >= maxVisitors {
			log.Printf("[conduit] rate limiter: visitor map at capacity (%d), rejecting new client %s", maxVisitors, clientIP)
			return false
		}
		v = &visitor{
			tokens:     float64(rl.burst),
			maxTokens:  float64(rl.burst),
			refillRate: rl.rps,
			lastSeen:   now,
		}
		rl.visitors[clientIP] = v
	}

	elapsed := now.Sub(v.lastSeen).Seconds()
	v.tokens += elapsed * v.refillRate
	if v.tokens > v.maxTokens {
		v.tokens = v.maxTokens
	}
	v.lastSeen = now

	if v.tokens < 1 {
		return false
	}
	v.tokens--
	return true
}

func (rl *RateLimiter) Middleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			method := c.Param("method")
			if !rl.Allow(c.RealIP(), method) {
				return c.JSON(http.StatusTooManyRequests, map[string]any{
					"result":     nil,
					"error_code": "ERR-RATE-LIMIT",
					"error_info": "Too many requests. Please slow down.",
				})
			}
			return next(c)
		}
	}
}

func (rl *RateLimiter) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-rl.done:
			return
		case <-ticker.C:
			rl.mu.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			for ip, v := range rl.visitors {
				if v.lastSeen.Before(cutoff) {
					delete(rl.visitors, ip)
				}
			}
			rl.mu.Unlock()
		}
	}
}

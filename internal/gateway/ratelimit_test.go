package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestNewRateLimiter_ExemptMethods(t *testing.T) {
	rl := NewRateLimiter(10, 5, []string{"ping", "pong"})
	defer rl.Stop()

	if !rl.exempt["ping"] || !rl.exempt["pong"] {
		t.Error("exempt methods not set correctly")
	}
	if rl.exempt["other"] {
		t.Error("unexpected exempt method")
	}
}

func TestAllow_DisabledWhenRPSZero(t *testing.T) {
	rl := NewRateLimiter(0, 10, nil)
	defer rl.Stop()

	for i := 0; i < 100; i++ {
		if !rl.Allow("1.2.3.4", "any.method") {
			t.Fatal("should always allow when rps=0")
		}
	}
}

func TestAllow_ExemptMethodAlwaysAllowed(t *testing.T) {
	rl := NewRateLimiter(1, 1, []string{"conduit.ping"})
	defer rl.Stop()

	for i := 0; i < 100; i++ {
		if !rl.Allow("1.2.3.4", "conduit.ping") {
			t.Fatal("exempt method should always be allowed")
		}
	}
}

func TestAllow_BurstThenReject(t *testing.T) {
	burst := 5
	rl := NewRateLimiter(1, burst, nil)
	defer rl.Stop()

	for i := 0; i < burst; i++ {
		if !rl.Allow("10.0.0.1", "test.method") {
			t.Fatalf("request %d should be allowed within burst", i)
		}
	}

	if rl.Allow("10.0.0.1", "test.method") {
		t.Error("should be rejected after burst exhausted")
	}
}

func TestAllow_DifferentClientsIndependent(t *testing.T) {
	rl := NewRateLimiter(1, 2, nil)
	defer rl.Stop()

	rl.Allow("client-a", "m")
	rl.Allow("client-a", "m")

	if !rl.Allow("client-b", "m") {
		t.Error("client-b should not be affected by client-a's usage")
	}
}

func TestAllow_ConcurrentAccess(t *testing.T) {
	rl := NewRateLimiter(1000, 100, nil)
	defer rl.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.Allow("concurrent-client", "method")
		}()
	}
	wg.Wait()
}

func TestMiddleware_AllowsRequest(t *testing.T) {
	rl := NewRateLimiter(100, 100, nil)
	defer rl.Stop()

	e := echo.New()
	mw := rl.Middleware()

	called := false
	handler := mw(func(c echo.Context) error {
		called = true
		return c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/api/test.method", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("method")
	c.SetParamValues("test.method")

	if err := handler(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("next handler was not called")
	}
}

func TestMiddleware_RejectsWhenLimited(t *testing.T) {
	rl := NewRateLimiter(1, 1, nil)
	defer rl.Stop()

	e := echo.New()
	mw := rl.Middleware()

	handler := mw(func(c echo.Context) error {
		return c.String(http.StatusOK, "ok")
	})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/test.method", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("method")
		c.SetParamValues("test.method")
		_ = handler(c)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/test.method", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("method")
	c.SetParamValues("test.method")

	_ = handler(c)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusTooManyRequests)
	}
}

func TestStop(t *testing.T) {
	rl := NewRateLimiter(10, 5, nil)
	rl.Stop()
}

func TestAllow_VisitorMapCapacity(t *testing.T) {
	rl := NewRateLimiter(1, 1, nil)
	defer rl.Stop()

	rl.mu.Lock()
	for i := 0; i < maxVisitors; i++ {
		ip := fmt.Sprintf("10.%d.%d.%d", (i>>16)&0xff, (i>>8)&0xff, i&0xff)
		rl.visitors[ip] = &visitor{
			tokens:     1,
			maxTokens:  1,
			refillRate: 1,
		}
	}
	rl.mu.Unlock()

	if rl.Allow("new-unique-client-255.255.255.255", "method") {
		t.Error("should reject when visitor map is at capacity")
	}
}

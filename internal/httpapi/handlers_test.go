package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/soulteary/gorge-conduit/internal/gateway"

	"github.com/labstack/echo/v4"
)

func newTestDeps(upstreamURL, token string, rl *gateway.RateLimiter) *Deps {
	return &Deps{
		Proxy:       gateway.NewProxy(upstreamURL, 5),
		RateLimiter: rl,
		Token:       token,
	}
}

func TestHealthPing_Root(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("http://localhost", "", nil)
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %q, want %q", body["status"], "ok")
	}
}

func TestHealthPing_Healthz(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("http://localhost", "", nil)
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestServiceTokenAuth_NoTokenConfigured(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer upstream.Close()

	e := echo.New()
	deps := newTestDeps(upstream.URL, "", nil)
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodPost, "/api/conduit.ping", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (no token required)", rec.Code, http.StatusOK)
	}
}

func TestServiceTokenAuth_ValidHeaderToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer upstream.Close()

	e := echo.New()
	deps := newTestDeps(upstream.URL, "secret123", nil)
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodPost, "/api/conduit.ping", nil)
	req.Header.Set("X-Service-Token", "secret123")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestServiceTokenAuth_ValidQueryToken(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer upstream.Close()

	e := echo.New()
	deps := newTestDeps(upstream.URL, "secret123", nil)
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodPost, "/api/conduit.ping?token=secret123", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestServiceTokenAuth_MissingToken(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("http://localhost", "secret123", nil)
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodPost, "/api/conduit.ping", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if !strings.Contains(rec.Body.String(), "ERR-CONDUIT-AUTH") {
		t.Errorf("body missing error code, got: %s", rec.Body.String())
	}
}

func TestServiceTokenAuth_InvalidToken(t *testing.T) {
	e := echo.New()
	deps := newTestDeps("http://localhost", "secret123", nil)
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodPost, "/api/conduit.ping", nil)
	req.Header.Set("X-Service-Token", "wrong-token")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestProxyHandler_ForwardsToUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/test.method" {
			t.Errorf("upstream path = %q, want /api/test.method", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"success"}`))
	}))
	defer upstream.Close()

	e := echo.New()
	deps := newTestDeps(upstream.URL, "", nil)
	RegisterRoutes(e, deps)

	req := httptest.NewRequest(http.MethodPost, "/api/test.method", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "success") {
		t.Errorf("body = %q, want to contain 'success'", rec.Body.String())
	}
}

func TestRateLimiter_IntegratedWithRoutes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer upstream.Close()

	rl := gateway.NewRateLimiter(1, 1, nil)
	defer rl.Stop()

	e := echo.New()
	deps := newTestDeps(upstream.URL, "", rl)
	RegisterRoutes(e, deps)

	req1 := httptest.NewRequest(http.MethodPost, "/api/test.method", nil)
	rec1 := httptest.NewRecorder()
	e.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Errorf("first request: status = %d, want %d", rec1.Code, http.StatusOK)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/test.method", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("second request: status = %d, want %d", rec2.Code, http.StatusTooManyRequests)
	}
}

func TestRegisterRoutes_MultipleHTTPMethods(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"ok"}`))
	}))
	defer upstream.Close()

	e := echo.New()
	deps := newTestDeps(upstream.URL, "", nil)
	RegisterRoutes(e, deps)

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/conduit.ping", nil)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("%s /api/conduit.ping: status = %d, want %d", method, rec.Code, http.StatusOK)
		}
	}
}

package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestNewProxy_TrimsTrailingSlash(t *testing.T) {
	p := NewProxy("http://example.com/", 5)
	if p.upstream != "http://example.com" {
		t.Errorf("upstream = %q, want trailing slash trimmed", p.upstream)
	}
}

func TestNewProxy_SetsTimeout(t *testing.T) {
	p := NewProxy("http://example.com", 15)
	if p.client.Timeout.Seconds() != 15 {
		t.Errorf("timeout = %v, want 15s", p.client.Timeout)
	}
}

func TestHandle_NoMethod(t *testing.T) {
	p := NewProxy("http://localhost", 5)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("method")
	c.SetParamValues("")

	err := p.Handle(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "ERR-CONDUIT-CORE") {
		t.Errorf("body missing error code, got: %s", rec.Body.String())
	}
}

func TestHandle_ProxiesRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/conduit.ping" {
			t.Errorf("upstream path = %q, want /api/conduit.ping", r.URL.Path)
		}
		if r.Header.Get("X-Conduit-Gateway") != "go-conduit" {
			t.Error("missing X-Conduit-Gateway header")
		}
		if r.Header.Get("X-Custom") != "value" {
			t.Error("custom header not forwarded")
		}
		w.Header().Set("X-Upstream-Response", "ok")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":"pong"}`))
	}))
	defer upstream.Close()

	p := NewProxy(upstream.URL, 5)

	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/conduit.ping", strings.NewReader("body"))
	req.Header.Set("X-Custom", "value")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("method")
	c.SetParamValues("conduit.ping")

	err := p.Handle(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("X-Upstream-Response") != "ok" {
		t.Error("upstream response header not copied")
	}
	if !strings.Contains(rec.Body.String(), "pong") {
		t.Errorf("body = %q, want to contain 'pong'", rec.Body.String())
	}
}

func TestHandle_PreservesQueryString(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "foo=bar&baz=1" {
			t.Errorf("query = %q, want foo=bar&baz=1", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	p := NewProxy(upstream.URL, 5)
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/test.method?foo=bar&baz=1", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("method")
	c.SetParamValues("test.method")

	if err := p.Handle(c); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandle_UpstreamError(t *testing.T) {
	p := NewProxy("http://127.0.0.1:1", 1)

	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/api/test.fail", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("method")
	c.SetParamValues("test.fail")

	err := p.Handle(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadGateway)
	}
	if !strings.Contains(rec.Body.String(), "ERR-CONDUIT-PROXY") {
		t.Errorf("body missing error code, got: %s", rec.Body.String())
	}
}

func TestCopyHeaders_SkipsHopByHop(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Set("Connection", "keep-alive")
	src.Set("Transfer-Encoding", "chunked")
	src.Set("X-Custom", "value")

	dst := http.Header{}
	copyHeaders(src, dst)

	if dst.Get("Content-Type") != "application/json" {
		t.Error("Content-Type not copied")
	}
	if dst.Get("X-Custom") != "value" {
		t.Error("X-Custom not copied")
	}
	if dst.Get("Connection") != "" {
		t.Error("Connection should be skipped")
	}
	if dst.Get("Transfer-Encoding") != "" {
		t.Error("Transfer-Encoding should be skipped")
	}
}

func TestCopyHeaders_MultipleValues(t *testing.T) {
	src := http.Header{}
	src.Add("X-Multi", "a")
	src.Add("X-Multi", "b")

	dst := http.Header{}
	copyHeaders(src, dst)

	vals := dst.Values("X-Multi")
	if len(vals) != 2 {
		t.Fatalf("expected 2 values, got %d", len(vals))
	}
}

func TestHandle_UpstreamStatusCodes(t *testing.T) {
	codes := []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusCreated}
	for _, code := range codes {
		statusCode := code
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(statusCode)
			_, _ = io.WriteString(w, "resp")
		}))

		p := NewProxy(upstream.URL, 5)
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
		rec := httptest.NewRecorder()
		c := e.NewContext(req, rec)
		c.SetParamNames("method")
		c.SetParamValues("test")

		if err := p.Handle(c); err != nil {
			t.Fatalf("unexpected error for status %d: %v", statusCode, err)
		}
		if rec.Code != statusCode {
			t.Errorf("status = %d, want %d", rec.Code, statusCode)
		}
		upstream.Close()
	}
}

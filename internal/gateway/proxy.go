package gateway

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
)

type Proxy struct {
	upstream string
	client   *http.Client
}

func NewProxy(upstreamURL string, timeoutSec int) *Proxy {
	upstream := strings.TrimRight(upstreamURL, "/")
	return &Proxy{
		upstream: upstream,
		client: &http.Client{
			Timeout: time.Duration(timeoutSec) * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (p *Proxy) Handle(c echo.Context) error {
	method := c.Param("method")
	if method == "" {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"result":     nil,
			"error_code": "ERR-CONDUIT-CORE",
			"error_info": "No Conduit method specified in URI.",
		})
	}

	req := c.Request()
	targetURL := fmt.Sprintf("%s/api/%s", p.upstream, method)
	if rawQ := req.URL.RawQuery; rawQ != "" {
		targetURL += "?" + rawQ
	}
	start := time.Now()

	proxyReq, err := http.NewRequestWithContext(req.Context(), req.Method, targetURL, req.Body)
	if err != nil {
		log.Printf("[conduit] proxy request build error: method=%s err=%v", method, err)
		return c.JSON(http.StatusBadGateway, map[string]any{
			"result":     nil,
			"error_code": "ERR-CONDUIT-PROXY",
			"error_info": "Failed to construct upstream request.",
		})
	}

	copyHeaders(req.Header, proxyReq.Header)

	proxyReq.Header.Set("X-Forwarded-For", c.RealIP())
	proxyReq.Header.Set("X-Forwarded-Proto", c.Scheme())
	proxyReq.Header.Set("X-Conduit-Gateway", "go-conduit")

	resp, err := p.client.Do(proxyReq)
	if err != nil {
		log.Printf("[conduit] upstream error: method=%s duration=%s err=%v",
			method, time.Since(start), err)
		return c.JSON(http.StatusBadGateway, map[string]any{
			"result":     nil,
			"error_code": "ERR-CONDUIT-PROXY",
			"error_info": "Upstream request failed.",
		})
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			log.Printf("[conduit] response body close error: method=%s err=%v", method, err)
		}
	}()

	log.Printf("[conduit] method=%s status=%d duration=%s client=%s",
		method, resp.StatusCode, time.Since(start), c.RealIP())

	for k, vv := range resp.Header {
		for _, v := range vv {
			c.Response().Header().Add(k, v)
		}
	}
	c.Response().WriteHeader(resp.StatusCode)
	if _, err := io.Copy(c.Response().Writer, resp.Body); err != nil {
		log.Printf("[conduit] response copy error: method=%s err=%v", method, err)
	}
	return nil
}

var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func copyHeaders(src, dst http.Header) {
	for k, vv := range src {
		if hopByHopHeaders[k] {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

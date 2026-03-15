package httpapi

import (
	"crypto/subtle"
	"net/http"

	"gorge-conduit/internal/gateway"

	"github.com/labstack/echo/v4"
)

type Deps struct {
	Proxy       *gateway.Proxy
	RateLimiter *gateway.RateLimiter
	Token       string
}

func RegisterRoutes(e *echo.Echo, deps *Deps) {
	e.GET("/", healthPing())
	e.GET("/healthz", healthPing())

	g := e.Group("/api/:method")
	g.Use(serviceTokenAuth(deps))
	if deps.RateLimiter != nil {
		g.Use(deps.RateLimiter.Middleware())
	}
	g.Any("", proxyHandler(deps))
}

func serviceTokenAuth(deps *Deps) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if deps.Token == "" {
				return next(c)
			}
			token := c.Request().Header.Get("X-Service-Token")
			if token == "" {
				token = c.QueryParam("token")
			}
			if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(deps.Token)) != 1 {
				return c.JSON(http.StatusUnauthorized, map[string]any{
					"result":     nil,
					"error_code": "ERR-CONDUIT-AUTH",
					"error_info": "Missing or invalid service token.",
				})
			}
			return next(c)
		}
	}
}

func healthPing() echo.HandlerFunc {
	return func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	}
}

func proxyHandler(deps *Deps) echo.HandlerFunc {
	return func(c echo.Context) error {
		return deps.Proxy.Handle(c)
	}
}

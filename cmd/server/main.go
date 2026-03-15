package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"

	"gorge-conduit/internal/config"
	"gorge-conduit/internal/gateway"
	"gorge-conduit/internal/httpapi"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
)

func main() {
	cfg := config.LoadFromEnv()

	proxy := gateway.NewProxy(cfg.UpstreamURL, cfg.ProxyTimeoutSec)

	var rl *gateway.RateLimiter
	if cfg.RateLimitRPS > 0 {
		rl = gateway.NewRateLimiter(cfg.RateLimitRPS, cfg.RateLimitBurst, cfg.RateLimitExempt)
	}

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	if cfg.MaxBodySize != "" {
		e.Use(middleware.BodyLimit(cfg.MaxBodySize))
	}

	httpapi.RegisterRoutes(e, &httpapi.Deps{
		Proxy:       proxy,
		RateLimiter: rl,
		Token:       cfg.ServiceToken,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := e.Start(cfg.ListenAddr); err != nil {
			log.Printf("[conduit] server stopped: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("[conduit] shutting down gracefully...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if rl != nil {
		rl.Stop()
	}

	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Printf("[conduit] forced shutdown: %v", err)
	}
}

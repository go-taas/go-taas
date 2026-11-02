// Command model-router is the data plane's model selector.
//
// It fronts the serving gateway for the OpenAI-compatible chat endpoint and
// forwards a request either to the control plane's provider proxy (for a model
// the platform sells from a provider) or to the serving gateway (for a model an
// inference service serves). Authentication, rate limits and metering stay on
// the gateway, which runs before this hop.
package main

import (
	"context"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/go-taas/go-taas/pkg/logger"
	"github.com/go-taas/go-taas/pkg/modelrouter"

	"net/http"
)

var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

var _, _, _ = version, commit, buildTime

func main() {
	cfg := modelrouter.Config{
		ServerURL:       envOr("TAAS_SERVER_URL", ""),
		ServerToken:     envOr("TAAS_SERVER_TOKEN", ""),
		GatewayURL:      envOr("TAAS_GATEWAY_URL", ""),
		RefreshInterval: time.Duration(envInt("TAAS_MODEL_REFRESH_SECONDS", 30)) * time.Second,
		RequestTimeout:  time.Duration(envInt("TAAS_REQUEST_TIMEOUT_SECONDS", 600)) * time.Second,
	}
	listen := envOr("TAAS_LISTEN_ADDR", ":8000")

	router, err := modelrouter.New(cfg)
	if err != nil {
		logger.S().Fatalw("model router: invalid configuration", "err", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := router.Run(ctx); err != nil {
			logger.S().Errorw("model router: refresh loop exited", "err", err)
		}
	}()

	server := &http.Server{
		Addr:              listen,
		Handler:           router.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	logger.S().Infow("model router listening",
		"addr", listen, "version", version, "commit", commit, "built", buildTime,
		"control_plane", cfg.ServerURL, "gateway", cfg.GatewayURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.S().Fatalw("model router: serve failed", "err", err)
	}
}

// envOr returns the environment value or a fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envInt returns the integer environment value or a fallback.
func envInt(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return v
}

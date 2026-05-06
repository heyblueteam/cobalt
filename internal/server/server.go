// Package server is the cobalt daemon. It exposes the HTTP API the CLI talks
// to, receives GitHub webhooks, drives the deployment flow, and reconciles
// Caddy and Docker Swarm.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/middleware"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// Config is the daemon's runtime configuration. It comes entirely from CLI
// flags and environment variables — there is no config file.
type Config struct {
	Addr    string
	DataDir string
}

// Run starts the HTTP server and blocks until ctx is canceled. It returns the
// first non-nil error from startup or shutdown.
func Run(ctx context.Context, cfg Config) error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("cobalt starting", "addr", cfg.Addr, "data_dir", cfg.DataDir)

	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	mux := http.NewServeMux()
	// Public endpoints.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, cobaltapi.Health{Status: "ok"})
	})
	// Authed endpoints will be registered under /api/ once individual handlers exist.
	mux.Handle("/api/", middleware.BearerAuth(db.DB, log)(http.HandlerFunc(notFound)))

	handler := middleware.RequestID(
		middleware.Recover(log)(
			middleware.Logger(log)(mux),
		),
	)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		log.Info("cobalt shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func notFound(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "not found", http.StatusNotFound)
}

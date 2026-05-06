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

	"github.com/heyblueteam/cobalt/internal/server/api"
	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/middleware"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/internal/server/worker"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// Config is the daemon's runtime configuration. It comes entirely from CLI
// flags and environment variables — there is no config file.
type Config struct {
	// Addr is the HTTP listen address (e.g., ":80").
	Addr string

	// DataDir is the writable root for sqlite, BuildKit cache, deployment
	// logs, repo workspaces, and the static-sites tree. Mounted as a
	// volume in the daemon container.
	DataDir string

	// CaddySocket is the unix socket path for Caddy's admin API. Empty
	// uses caddy.DefaultSocketPath.
	CaddySocket string

	// PublicHost is the daemon's public hostname (e.g. "cobalt.blue.cc").
	// Used to build manifest URLs the user opens in a browser. Empty
	// falls back to the request's Host header — fine for dev, but
	// production should set this.
	PublicHost string
}

// Run starts every daemon subsystem (storage, scheduler, dispatcher, HTTP
// server) and blocks until ctx is canceled. Returns the first non-nil
// error from startup or shutdown.
//
// The wiring order is: open store → recover any in-flight deploys from
// the previous daemon process → start dispatcher → start scheduler →
// start HTTP. Shutdown reverses.
func Run(ctx context.Context, cfg Config) error {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("cobalt starting", "addr", cfg.Addr, "data_dir", cfg.DataDir)

	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := deploy.RecoverOnBoot(ctx, db, log); err != nil {
		log.Error("recover-on-boot failed", "error", err)
		// Non-fatal: we couldn't reconcile in-flight rows from a previous
		// crashed daemon, but new deploys should still work.
	}

	dockerCli := docker.New()
	caddyCli := newCaddyClient(cfg)
	githubCli := github.NewClient(nil)

	tokens := deploy.NewDBTokenProvider(db, githubCli, time.Now)
	preparer := deploy.NewPreparer(cfg.DataDir, tokens, deploy.ExecGit{})
	builder := deploy.NewBuilder(dockerCli, db, cfg.DataDir)
	orchestrator := &deploy.Orchestrator{
		DB:       db,
		Docker:   dockerCli,
		Caddy:    caddyCli,
		Preparer: preparer,
		Builder:  builder,
		DataDir:  cfg.DataDir,
		Log:      log,
	}

	queue := deploy.NewQueue(db)
	dispatcher := deploy.NewDispatcher(db, orchestrator, log, deploy.DispatcherOpts{})
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	sched := worker.NewScheduler(log)
	registerScheduledJobs(sched, log, db, dockerCli, caddyCli, cfg.DataDir)
	sched.Start(ctx)
	defer sched.Stop()

	apiMux := http.NewServeMux()
	apiHandler := api.NewHandler(api.HandlerOpts{
		DB:         db,
		Caddy:      caddyCli,
		Docker:     dockerCli,
		GitHub:     githubCli,
		Queue:      queue,
		Dispatcher: dispatcher,
		Log:        log,
		DataDir:    cfg.DataDir,
		PublicHost: cfg.PublicHost,
	})
	apiHandler.Register(apiMux)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, cobaltapi.Health{Status: "ok"})
	})
	apiHandler.RegisterPublic(mux)
	mux.Handle("/api/", middleware.BearerAuth(db.DB, log)(apiMux))

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

// newCaddyClient constructs the production Caddy admin client, defaulting
// the socket path to caddy.DefaultSocketPath when not explicitly set.
func newCaddyClient(cfg Config) *caddy.Client {
	socket := cfg.CaddySocket
	if socket == "" {
		socket = caddy.DefaultSocketPath
	}
	return caddy.NewUnixSocketClient(socket)
}

// registerScheduledJobs wires every periodic task into the scheduler.
// Each Schedule call registers under a stable name so future code can
// remove or replace jobs by id.
//
// Cadence:
//   - image cleanup           hourly  (prune docker images for inactive deploys)
//   - pending app cleanup     10m     (drop expired manifest-flow rows)
//   - caddy reconcile         30s     (root fix for upstream issue #97)
//   - deploy log rotation     daily   (gzip > 30d, purge gz > 1y)
func registerScheduledJobs(
	sched *worker.Scheduler,
	log *slog.Logger,
	db *store.DB,
	dockerCli *docker.Client,
	caddyCli *caddy.Client,
	dataDir string,
) {
	_ = sched.Schedule("image-cleanup", "@hourly", func(ctx context.Context) {
		if _, err := worker.CleanupImages(ctx, log, db, db, dockerCli); err != nil {
			log.Warn("image cleanup failed", "error", err)
		}
	})
	_ = sched.Schedule("pending-apps-cleanup", "@every 10m", func(ctx context.Context) {
		if _, err := worker.CleanupExpiredPendingApps(ctx, log, db, time.Now()); err != nil {
			log.Warn("pending-apps cleanup failed", "error", err)
		}
	})
	_ = sched.Schedule("caddy-reconcile", "@every 30s", func(ctx context.Context) {
		if _, err := worker.ReconcileCaddyState(ctx, log, db, caddyCli); err != nil {
			log.Warn("caddy reconcile failed", "error", err)
		}
	})
	_ = sched.Schedule("deploy-log-rotation", "@daily", func(ctx context.Context) {
		if _, _, err := worker.RotateDeployLogs(ctx, log, dataDir, 0, 0, time.Now()); err != nil {
			log.Warn("deploy log rotation failed", "error", err)
		}
	})
}


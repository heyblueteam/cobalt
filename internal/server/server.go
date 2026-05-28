// Package server is the cobalt daemon. It exposes the HTTP API the CLI talks
// to, receives GitHub webhooks, drives the deployment flow, and reconciles
// Caddy and Docker Swarm.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
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

	// RqliteURL is the URL of the rqlite node (e.g., "http://localhost:4001").
	// Cobalt connects to this URL for persistence. When using the sidecar
	// model (default), this is "http://localhost:4001".
	RqliteURL string

	// RqlitedPath is the path to the rqlited binary. If empty and sidecar
	// mode is enabled (the default), cobalt will look for "rqlited" in $PATH.
	// Set to empty string to connect to an externally-managed rqlite node.
	RqlitedPath string

	// DataDir is the writable root for BuildKit cache, deployment
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

	// Version is the daemon's build-time version (e.g. "v1.2.3").
	// Surfaced via GET /api/meta/info. Empty defaults to "dev".
	Version string

	// EncryptionKeyPath is where the daemon reads its 32-byte AES key
	// for env-var encryption at rest. Empty defaults to
	// encryption.DefaultKeyPath ("/run/secrets/cobalt_encryption_key"),
	// which is what the embedded compose bind-mounts. Override for
	// alternative deployments (manual installs, tests against a
	// host-side path, etc.).
	EncryptionKeyPath string

	// ImageRetention is the number of recent successful deployments per
	// project whose container images we keep on disk past the active
	// window so `cobalt rollback` can target them. 0 = match the
	// historical behavior (keep only active deploys).
	ImageRetention int
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
	log.Info("cobalt starting", "addr", cfg.Addr, "rqlite_url", cfg.RqliteURL)

	// Start rqlited sidecar if no external URL is provided
	var sidecarStop func()
	if cfg.RqlitedPath != "none" && looksLikeLocalURL(cfg.RqliteURL) {
		var rqlitedPath string
		if cfg.RqlitedPath != "" {
			rqlitedPath = cfg.RqlitedPath
		} else {
			var err error
			rqlitedPath, err = findRqlited()
			if err != nil {
				return err
			}
		}

		sidecar, err := startRqlitedSidecar(log, rqlitedPath, cfg.RqliteURL, cfg.DataDir)
		if err != nil {
			return err
		}
		sidecarStop = sidecar.stop
		defer sidecarStop()
	}

	db, err := store.Open(cfg.RqliteURL)
	if err != nil {
		return err
	}

	if err := db.InitSchema(ctx); err != nil {
		return err
	}

	// Mark any upgrade rows still in `running` status from before this
	// boot as failed. Without this, a daemon crash mid-upgrade (or a
	// helper container that exited without calling SetUpgradeStatus,
	// e.g. earlier-version bugs) leaves the row stuck and the
	// single-flight 409 blocks every future `cobalt upgrade` call.
	// 30 min is a generous upper bound on any real upgrade.
	if n, err := db.SweepStaleUpgrades(ctx, 30*time.Minute); err != nil {
		log.Warn("sweep stale upgrades failed", "error", err)
	} else if n > 0 {
		log.Info("swept stale upgrade rows", "count", n)
	}

	// Wire the env-var encryption cipher BEFORE any code path that
	// touches env_vars. The key is bind-mounted into the daemon at
	// /run/secrets/cobalt_encryption_key by the embedded compose;
	// `cobalt init` generates and places the host-side file.
	if err := configureEnvCipher(ctx, db, cfg.EncryptionKeyPath, log); err != nil {
		return err
	}

	if err := ensureBootstrapKey(ctx, db, cfg.DataDir, log); err != nil {
		return fmt.Errorf("bootstrap key: %w", err)
	}

	if err := deploy.RecoverOnBoot(ctx, db, log); err != nil {
		log.Error("recover-on-boot failed", "error", err)
		// Non-fatal: we couldn't reconcile in-flight rows from a previous
		// crashed daemon, but new deploys should still work.
	}

	dockerCli := docker.New()
	caddyCli := newCaddyClient(cfg)
	githubCli := github.NewClient(nil)

	// Bootstrap the buildx builder we use for every deploy. Failure here
	// is non-fatal: the daemon can still serve API traffic without it,
	// only deploys will fail clearly at build time.
	if err := dockerCli.EnsureBuildxBuilder(ctx, docker.BuildxBuilderName); err != nil {
		log.Warn("ensure buildx builder failed", "name", docker.BuildxBuilderName, "error", err)
	} else {
		log.Info("buildx builder ready", "name", docker.BuildxBuilderName)
	}

	tokens := deploy.NewDBTokenProvider(db, githubCli, time.Now)
	preparer := deploy.NewPreparer(cfg.DataDir, tokens, deploy.ExecGit{})
	builder := deploy.NewBuilder(dockerCli, db, db, cfg.DataDir)

	sched := worker.NewScheduler(log)
	cronMgr := worker.NewCronManager(sched, dockerCli, db, log)

	orchestrator := &deploy.Orchestrator{
		DB:          db,
		Docker:      dockerCli,
		Caddy:       caddyCli,
		Preparer:    preparer,
		Builder:     builder,
		DataDir:     cfg.DataDir,
		Log:         log,
		CronManager: cronMgr,
	}

	queue := deploy.NewQueue(db)
	dispatcher := deploy.NewDispatcher(db, orchestrator, log, deploy.DispatcherOpts{})
	dispatcher.Start(ctx)
	defer dispatcher.Stop()

	registerScheduledJobs(sched, log, db, dockerCli, caddyCli, cfg.DataDir, cfg.ImageRetention)
	sched.Start(ctx)
	defer sched.Stop()
	cronMgr.ReconcileAll(ctx, db, db)

	apiMux := http.NewServeMux()
	apiHandler := api.NewHandler(api.HandlerOpts{
		DB:          db,
		Caddy:       caddyCli,
		Docker:      dockerCli,
		GitHub:      githubCli,
		Queue:       queue,
		Dispatcher:  dispatcher,
		Log:         log,
		DataDir:     cfg.DataDir,
		PublicHost:  cfg.PublicHost,
		Version:     cfg.Version,
		CronManager: cronManagerAdapter{cronMgr},
	})
	apiHandler.Register(apiMux)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, cobaltapi.Health{Status: "ok"})
	})
	apiHandler.RegisterPublic(mux)
	mux.Handle("/api/", middleware.BearerAuth(db.Client, log)(apiMux))

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
//   - network cleanup         hourly  (prune overlay networks for inactive deploys)
//   - pending app cleanup     10m     (drop expired manifest-flow rows)
//   - caddy reconcile         30s     (root fix for upstream issue #97)
//   - caddy watchdog          30s     (auto-restart Caddy if admin wedges)
//   - deploy log rotation     daily   (gzip > 30d, purge gz > 1y)
func registerScheduledJobs(
	sched *worker.Scheduler,
	log *slog.Logger,
	db *store.DB,
	dockerCli *docker.Client,
	caddyCli *caddy.Client,
	dataDir string,
	imageRetention int,
) {
	_ = sched.Schedule("image-cleanup", "@hourly", func(ctx context.Context) {
		if _, err := worker.CleanupImages(ctx, log, db, db, dockerCli, imageRetention); err != nil {
			log.Warn("image cleanup failed", "error", err)
		}
	})
	_ = sched.Schedule("network-cleanup", "@hourly", func(ctx context.Context) {
		if _, err := worker.CleanupNetworks(ctx, log, db, db, dockerCli); err != nil {
			log.Warn("network cleanup failed", "error", err)
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
	caddyWatchdog := worker.NewCaddyWatchdog(caddyCli, dockerCli, worker.DefaultCaddyServiceName, log)
	_ = sched.Schedule("caddy-watchdog", "@every 30s", caddyWatchdog.Tick)
	_ = sched.Schedule("deploy-log-rotation", "@daily", func(ctx context.Context) {
		if _, _, err := worker.RotateDeployLogs(ctx, log, dataDir, 0, 0, time.Now()); err != nil {
			log.Warn("deploy log rotation failed", "error", err)
		}
	})
}

// looksLikeLocalURL returns true if the URL looks like a local sidecar URL
// (localhost or no host specified).
func looksLikeLocalURL(url string) bool {
	return url == "" || url == "http://localhost:4001" || url == "http://127.0.0.1:4001"
}

// findRqlited searches for rqlited in PATH.
func findRqlited() (string, error) {
	path, err := exec.LookPath("rqlited")
	if err == nil {
		return path, nil
	}
	return "", errors.New("rqlited not found in PATH; set --rqlited-path or install rqlited")
}

// sidecar manages a rqlited subprocess.
type sidecar struct {
	cmd    *exec.Cmd
	stopFn func()
	log    *slog.Logger
}

// startRqlitedSidecar starts rqlited as a subprocess and waits for it to be ready.
// The dataDir is used for rqlite's persistent state.
func startRqlitedSidecar(log *slog.Logger, rqlitedPath, rqliteURL, dataDir string) (*sidecar, error) {
	// Extract host:port from URL for binding
	host, port, err := splitHostPort(rqliteURL)
	if err != nil {
		return nil, errors.New("invalid rqlite URL: " + rqliteURL)
	}

	// Ensure data directory exists
	rqliteDataDir := filepath.Join(dataDir, "rqlite-data")
	if err := os.MkdirAll(rqliteDataDir, 0o755); err != nil {
		return nil, errors.New("create rqlite data dir: " + err.Error())
	}

	bindAddr := net.JoinHostPort(host, port)
	advAddr := "localhost:" + port

	cmd := exec.Command(
		rqlitedPath,
		"-http-addr", bindAddr,
		"-http-adv-addr", advAddr,
		rqliteDataDir,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// Detach so rqlited survives when cobalt exits (for restarts)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, errors.New("start rqlited: " + err.Error())
	}

	log.Info("rqlited sidecar started", "pid", cmd.Process.Pid, "data_dir", rqliteDataDir)

	// Wait for rqlited to be ready
	if !waitForRqlited(rqliteURL, 10*time.Second) {
		cmd.Process.Kill()
		return nil, errors.New("rqlited sidecar not ready in 10s")
	}

	sc := &sidecar{cmd: cmd, log: log}
	sc.stopFn = func() {
		// Kill the process group to ensure rqlited dies
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		cmd.Wait()
		log.Info("rqlited sidecar stopped")
	}

	return sc, nil
}

func (s *sidecar) stop() {
	s.stopFn()
}

// waitForRqlited polls the rqlite URL until it's ready.
func waitForRqlited(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		db, err := store.Open(url)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		err = db.Ping(context.Background())
		_ = db.Close()
		if err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// splitHostPort extracts host and port from a URL like "http://localhost:4001".
func splitHostPort(urlStr string) (host, port string, err error) {
	u, err := url.Parse("http://" + urlStr)
	if err != nil {
		u, err = url.Parse(urlStr)
		if err != nil {
			return "", "", err
		}
	}
	if u.Host == "" {
		return "localhost", "4001", nil
	}
	host, port, err = net.SplitHostPort(u.Host)
	if err != nil {
		return "localhost", "4001", nil
	}
	return host, port, nil
}

// cronManagerAdapter adapts *worker.CronManager to the
// api.CronManager interface so the api package doesn't need to
// import worker (which would risk a cycle).
type cronManagerAdapter struct{ inner *worker.CronManager }

func (a cronManagerAdapter) ListForProject(projectName string) []api.CronView {
	views := a.inner.ListForProject(projectName)
	out := make([]api.CronView, 0, len(views))
	for _, v := range views {
		out = append(out, api.CronView{
			ServiceName:      v.ServiceName,
			Schedule:         v.Schedule,
			Command:          v.Command,
			DeploymentNumber: v.DeploymentNumber,
			NextFireAt:       v.NextFireAt,
		})
	}
	return out
}

func (a cronManagerAdapter) RemoveAllForProject(projectName string) error {
	return a.inner.RemoveAllForProject(projectName)
}

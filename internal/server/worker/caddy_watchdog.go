package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// CaddyPinger is the admin-liveness probe the watchdog calls each tick.
type CaddyPinger interface {
	Ping(ctx context.Context) error
}

// CaddyServiceRestarter restarts the swarm service running Caddy.
type CaddyServiceRestarter interface {
	RestartService(ctx context.Context, name string) error
}

// DefaultCaddyServiceName is the swarm service the watchdog restarts when
// Caddy's admin endpoint goes unresponsive. `docker stack deploy` prefixes
// service names with the stack name, so the cobalt stack's caddy service is
// "cobalt_caddy".
const DefaultCaddyServiceName = "cobalt_caddy"

const (
	// defaultWatchdogThreshold is how many consecutive failed probes must
	// accumulate before the watchdog restarts Caddy. At the @every 30s tick
	// this is ~90s of sustained unresponsiveness — long enough to ride out a
	// transient blip, short enough to recover before deploys pile up.
	defaultWatchdogThreshold = 3
	// defaultWatchdogCooldown is the minimum gap between auto-restarts.
	// Restarting Caddy is a ~1-2s interruption across ALL sites, so if a
	// restart doesn't fix the wedge we back off rather than thrash a shared
	// proxy.
	defaultWatchdogCooldown = 5 * time.Minute
)

// CaddyWatchdog probes Caddy's admin endpoint and force-restarts the Caddy
// swarm service when it goes unresponsive for several consecutive ticks.
//
// Why this exists: Caddy's data plane keeps serving traffic while its admin
// API (a unix socket) wedges — at which point cobalt can no longer swap
// upstreams and every deploy hangs or fails its verify. Recovery used to be
// a manual SSH `docker restart`; this automates it. Because restarting Caddy
// briefly interrupts every site, the watchdog is deliberately conservative:
// it acts only after `threshold` consecutive failures and never more often
// than `cooldown`.
type CaddyWatchdog struct {
	ping        CaddyPinger
	docker      CaddyServiceRestarter
	serviceName string
	log         *slog.Logger
	threshold   int
	cooldown    time.Duration
	now         func() time.Time

	mu          sync.Mutex
	fails       int
	lastRestart time.Time
}

// NewCaddyWatchdog builds a watchdog with production defaults. An empty
// serviceName falls back to DefaultCaddyServiceName.
func NewCaddyWatchdog(ping CaddyPinger, dkr CaddyServiceRestarter, serviceName string, log *slog.Logger) *CaddyWatchdog {
	if serviceName == "" {
		serviceName = DefaultCaddyServiceName
	}
	if log == nil {
		log = slog.Default()
	}
	return &CaddyWatchdog{
		ping:        ping,
		docker:      dkr,
		serviceName: serviceName,
		log:         log,
		threshold:   defaultWatchdogThreshold,
		cooldown:    defaultWatchdogCooldown,
		now:         time.Now,
	}
}

// Tick is one watchdog cycle, suitable for the scheduler. It probes the
// admin endpoint and, on sustained failure, restarts the Caddy service.
func (w *CaddyWatchdog) Tick(ctx context.Context) {
	err := w.ping.Ping(ctx)

	w.mu.Lock()
	defer w.mu.Unlock()

	if err == nil {
		if w.fails > 0 {
			w.log.Info("caddy watchdog: admin recovered", "after_failures", w.fails)
		}
		w.fails = 0
		return
	}

	w.fails++
	w.log.Warn("caddy watchdog: admin probe failed",
		"consecutive", w.fails, "threshold", w.threshold, "error", err)
	if w.fails < w.threshold {
		return
	}

	if !w.lastRestart.IsZero() {
		if since := w.now().Sub(w.lastRestart); since < w.cooldown {
			w.log.Warn("caddy watchdog: restart suppressed by cooldown",
				"since_last_restart", since, "cooldown", w.cooldown)
			return
		}
	}

	w.log.Error("🚨 caddy watchdog: admin unresponsive, restarting service",
		"service", w.serviceName, "consecutive", w.fails)
	if err := w.docker.RestartService(ctx, w.serviceName); err != nil {
		// Leave fails high so we retry on the next tick; don't set
		// lastRestart, so the cooldown doesn't gate a restart that never
		// actually happened.
		w.log.Error("caddy watchdog: service restart failed",
			"service", w.serviceName, "error", err)
		return
	}
	w.lastRestart = w.now()
	w.fails = 0
}

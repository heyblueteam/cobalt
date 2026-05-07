package deploy

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// Dispatcher runs at most one in-flight deployment per project. Calls to
// Notify wake the dispatcher; it scans the queued rows in the DB, picks
// the newest queued row per idle project (marking older queued rows
// skipped), and hands them to the Runner.
//
// SQLite's single-writer model makes "claiming" a queued row safe without
// explicit locking: the entire scan-and-claim pass runs against the same
// DB connection, serialized with everything else.
type Dispatcher struct {
	db     *store.DB
	runner Runner
	log    *slog.Logger

	// pollInterval is the periodic sweep frequency; covers the case where
	// a Notify was missed (e.g., enqueue happened during shutdown).
	pollInterval time.Duration

	mu       sync.Mutex
	inflight map[int64]context.CancelFunc // projectID -> cancel for the running deploy

	wake   chan struct{}
	stopFn context.CancelFunc
	wg     sync.WaitGroup
}

// DispatcherOpts are constructor options for NewDispatcher.
type DispatcherOpts struct {
	// PollInterval is how often the dispatcher sweeps the queue even
	// without an explicit Notify. Defaults to 30s.
	PollInterval time.Duration
}

// NewDispatcher returns a stopped dispatcher. Call Start to begin running
// deploys.
func NewDispatcher(db *store.DB, runner Runner, log *slog.Logger, opts DispatcherOpts) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 30 * time.Second
	}
	return &Dispatcher{
		db:           db,
		runner:       runner,
		log:          log,
		pollInterval: opts.PollInterval,
		inflight:     map[int64]context.CancelFunc{},
		// Buffered so concurrent Notify calls don't block.
		wake: make(chan struct{}, 1),
	}
}

// Start begins the dispatcher loop in a background goroutine.
func (d *Dispatcher) Start(ctx context.Context) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopFn != nil {
		return
	}
	loopCtx, cancel := context.WithCancel(ctx)
	d.stopFn = cancel
	d.wg.Add(1)
	go d.loop(loopCtx)
	d.log.Info("dispatcher: started")
}

// Stop signals the dispatcher to halt and waits for the loop to return.
// In-flight deploys are also signaled to cancel via context; their cancel
// transitions are recorded by their goroutines on exit.
func (d *Dispatcher) Stop() {
	d.mu.Lock()
	stop := d.stopFn
	d.stopFn = nil
	// Cancel every in-flight deploy by canceling its context.
	for _, cancel := range d.inflight {
		cancel()
	}
	d.mu.Unlock()
	if stop == nil {
		return
	}
	stop()
	d.wg.Wait()
	d.log.Info("dispatcher: stopped")
}

// Notify wakes the dispatcher to scan the queue. Safe to call concurrently
// and from any goroutine. If a wake is already pending, Notify is a no-op.
func (d *Dispatcher) Notify() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Cancel cancels an in-flight deploy by deployment id. Returns nil if the
// deploy was queued (Queue.Cancel handles that path).
func (d *Dispatcher) Cancel(ctx context.Context, deploymentID int64) error {
	dep, err := d.db.GetDeployment(ctx, deploymentID)
	if err != nil {
		return err
	}
	if !dep.Status.IsActive() {
		return ErrNotInFlight
	}
	d.mu.Lock()
	cancel, ok := d.inflight[dep.ProjectID]
	d.mu.Unlock()
	if !ok {
		return ErrNotTracked
	}
	cancel()
	return nil
}

func (d *Dispatcher) loop(ctx context.Context) {
	defer d.wg.Done()
	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()
	// Sweep once immediately so daemon-restart-recovery can work without
	// waiting for the first wake.
	d.advance(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.wake:
			d.advance(ctx)
		case <-ticker.C:
			d.advance(ctx)
		}
	}
}

// advance scans all queued deploys, picks the newest per idle project,
// marks the rest skipped, and starts the chosen ones.
func (d *Dispatcher) advance(ctx context.Context) {
	queued, err := d.db.QueuedDeployments(ctx)
	if err != nil {
		d.log.Error("dispatcher: list queued", "error", err)
		return
	}
	if len(queued) == 0 {
		return
	}

	// Group by project_id.
	byProject := map[int64][]store.Deployment{}
	for _, q := range queued {
		byProject[q.ProjectID] = append(byProject[q.ProjectID], q)
	}

	for projectID, deps := range byProject {
		d.mu.Lock()
		_, busy := d.inflight[projectID]
		d.mu.Unlock()
		if busy {
			continue
		}

		// Newest is last (rows are ORDER BY project_id, number).
		newest := deps[len(deps)-1]
		// Skip everything older.
		for _, older := range deps[:len(deps)-1] {
			if err := d.db.SetDeploymentStatus(ctx, older.ID, cobaltapi.StateSkipped); err != nil {
				d.log.Error("dispatcher: mark skipped",
					"deployment_id", older.ID, "error", err)
			} else {
				d.log.Info("dispatcher: skipped",
					"deployment_id", older.ID,
					"project_id", older.ProjectID,
					"superseded_by", newest.ID,
				)
			}
		}
		d.startDeployment(ctx, newest)
	}
}

func (d *Dispatcher) startDeployment(parent context.Context, dep store.Deployment) {
	runCtx, cancel := context.WithCancel(parent)
	d.mu.Lock()
	d.inflight[dep.ProjectID] = cancel
	d.mu.Unlock()

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		defer func() {
			d.mu.Lock()
			delete(d.inflight, dep.ProjectID)
			d.mu.Unlock()
			// After every deploy, re-notify so any queued deploys for the
			// same project (or others) get picked up immediately.
			d.Notify()
		}()

		// Move to fetching first; this stamps started_at and prevents
		// the dispatcher from picking the same row again on the next sweep.
		if err := d.db.SetDeploymentStatus(parent, dep.ID, cobaltapi.StateFetching); err != nil {
			d.log.Error("dispatcher: mark fetching",
				"deployment_id", dep.ID, "error", err)
			cancel()
			return
		}
		// Refresh the row so the runner sees the right status.
		dep.Status = cobaltapi.StateFetching

		runErr := d.runner.Run(runCtx, dep)

		// Map error → terminal state. ctx canceled → canceled, anything
		// else → failed. Use parent context for the DB write so the row
		// transitions even though runCtx is canceled.
		var terminal cobaltapi.State
		switch {
		case runErr == nil:
			terminal = cobaltapi.StateSuccess
		case errors.Is(runErr, context.Canceled), errors.Is(runCtx.Err(), context.Canceled):
			terminal = cobaltapi.StateCanceled
		default:
			terminal = cobaltapi.StateFailed
		}

		if err := d.db.SetDeploymentStatus(parent, dep.ID, terminal); err != nil {
			d.log.Error("dispatcher: mark terminal",
				"deployment_id", dep.ID, "status", terminal, "error", err)
		}

		if runErr != nil {
			d.log.Warn("dispatcher: deploy ended",
				"deployment_id", dep.ID, "status", terminal, "error", runErr)
		} else {
			d.log.Info("dispatcher: deploy succeeded", "deployment_id", dep.ID)
		}
	}()
}

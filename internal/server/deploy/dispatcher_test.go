package deploy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// recordingRunner captures every Run call. It blocks on a channel so the
// test can keep a deploy "in-flight" deterministically.
type recordingRunner struct {
	mu      sync.Mutex
	calls   []store.Deployment
	gate    chan struct{} // close to release Run
	returns map[int64]error
}

func newRecordingRunner() *recordingRunner {
	return &recordingRunner{
		gate:    make(chan struct{}),
		returns: map[int64]error{},
	}
}

func (r *recordingRunner) Run(ctx context.Context, d store.Deployment) error {
	r.mu.Lock()
	r.calls = append(r.calls, d)
	r.mu.Unlock()
	select {
	case <-r.gate:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.returns[d.ID]
}

func (r *recordingRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestDispatcher_PicksUpQueuedAndMarksSuccess(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	id, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})

	runner := newRecordingRunner()
	close(runner.gate) // immediate return

	d := NewDispatcher(db, runner, quietLogger(), DispatcherOpts{PollInterval: 100 * time.Millisecond})
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		dep, _ := db.GetDeployment(context.Background(), id)
		if dep.Status == cobaltapi.StateSuccess {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	dep, _ := db.GetDeployment(context.Background(), id)
	t.Fatalf("status: got %q, want success", dep.Status)
}

func TestDispatcher_RunnerErrorMarksFailed(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	id, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})

	runner := newRecordingRunner()
	runner.returns[id] = errors.New("boom")
	close(runner.gate)

	d := NewDispatcher(db, runner, quietLogger(), DispatcherOpts{PollInterval: 100 * time.Millisecond})
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		dep, _ := db.GetDeployment(context.Background(), id)
		if dep.Status == cobaltapi.StateFailed {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("deploy never marked failed")
}

func TestDispatcher_NewerSupersedesOlder(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	older, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})
	newer, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})

	runner := newRecordingRunner()
	close(runner.gate)

	d := NewDispatcher(db, runner, quietLogger(), DispatcherOpts{PollInterval: 50 * time.Millisecond})
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		olderRow, _ := db.GetDeployment(context.Background(), older)
		newerRow, _ := db.GetDeployment(context.Background(), newer)
		if olderRow.Status == cobaltapi.StateSkipped && newerRow.Status == cobaltapi.StateSuccess {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	olderRow, _ := db.GetDeployment(context.Background(), older)
	newerRow, _ := db.GetDeployment(context.Background(), newer)
	t.Fatalf("expected older=skipped & newer=success, got older=%s newer=%s",
		olderRow.Status, newerRow.Status)
}

func TestDispatcher_OnlyOneInFlightPerProject(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	id1, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})

	runner := newRecordingRunner()
	// Don't close the gate — first deploy hangs in Runner.Run.

	d := NewDispatcher(db, runner, quietLogger(), DispatcherOpts{PollInterval: 50 * time.Millisecond})
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	// Wait for first deploy to enter fetching.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dep, _ := db.GetDeployment(context.Background(), id1)
		if dep.Status == cobaltapi.StateFetching {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if runner.callCount() != 1 {
		t.Fatalf("call count: got %d, want 1 before second enqueue", runner.callCount())
	}

	// Enqueue a second one; the dispatcher should NOT pick it up while the
	// first is in flight.
	id2, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})
	d.Notify()
	time.Sleep(300 * time.Millisecond)
	if runner.callCount() != 1 {
		t.Errorf("call count: got %d, want 1 (second deploy must wait)", runner.callCount())
	}
	dep2, _ := db.GetDeployment(context.Background(), id2)
	if dep2.Status != cobaltapi.StateQueued {
		t.Errorf("second deploy status: %q, want queued", dep2.Status)
	}
}

func TestDispatcher_CancelInFlight(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	id, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})

	runner := newRecordingRunner()
	d := NewDispatcher(db, runner, quietLogger(), DispatcherOpts{PollInterval: 50 * time.Millisecond})
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	// Wait for in-flight.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dep, _ := db.GetDeployment(context.Background(), id)
		if dep.Status == cobaltapi.StateFetching {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := d.Cancel(context.Background(), id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dep, _ := db.GetDeployment(context.Background(), id)
		if dep.Status == cobaltapi.StateCanceled {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	dep, _ := db.GetDeployment(context.Background(), id)
	t.Fatalf("status: got %q, want canceled", dep.Status)
}

func TestDispatcher_DifferentProjectsRunInParallel(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	api := newProject(t, db, "api")
	web := newProject(t, db, "web")
	idA, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: api})
	idW, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: web})

	runner := newRecordingRunner()
	d := NewDispatcher(db, runner, quietLogger(), DispatcherOpts{PollInterval: 50 * time.Millisecond})
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	// Wait for both to be in flight before we release the gate.
	var seenAPI, seenWeb atomic.Bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !(seenAPI.Load() && seenWeb.Load()) {
		if dep, _ := db.GetDeployment(context.Background(), idA); dep.Status == cobaltapi.StateFetching {
			seenAPI.Store(true)
		}
		if dep, _ := db.GetDeployment(context.Background(), idW); dep.Status == cobaltapi.StateFetching {
			seenWeb.Store(true)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !(seenAPI.Load() && seenWeb.Load()) {
		t.Fatal("both projects should be in flight in parallel")
	}
	if runner.callCount() != 2 {
		t.Errorf("calls: got %d, want 2", runner.callCount())
	}
	close(runner.gate)
}

// TestDispatcher_CancelDuringSwapping_Refused asserts the safety guard:
// once a deploy reaches StateSwapping (cutover), Cancel returns
// ErrCancelDuringCutover and does not interrupt the runner. Cancelling
// mid-cutover can leave Docker Swarm and Caddy in inconsistent state.
func TestDispatcher_CancelDuringSwapping_Refused(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	q := NewQueue(db)
	pid := newProject(t, db, "api")
	id, _, _ := q.Enqueue(context.Background(), EnqueueRequest{ProjectID: pid})

	runner := newRecordingRunner()
	d := NewDispatcher(db, runner, quietLogger(), DispatcherOpts{PollInterval: 50 * time.Millisecond})
	d.Start(context.Background())
	t.Cleanup(d.Stop)

	// Wait for the runner to be invoked (deploy is in fetching).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && runner.callCount() == 0 {
		time.Sleep(10 * time.Millisecond)
	}

	// Manually transition to swapping to simulate cutover; the runner is
	// still gated, so it stays "in flight" until we close the gate.
	if err := db.SetDeploymentStatus(context.Background(), id, cobaltapi.StateSwapping); err != nil {
		t.Fatalf("set swapping: %v", err)
	}

	if err := d.Cancel(context.Background(), id); !errors.Is(err, ErrCancelDuringCutover) {
		t.Fatalf("Cancel during swapping: got %v, want ErrCancelDuringCutover", err)
	}

	// Release the runner so the deploy completes naturally; the cancel
	// must not have terminated it early.
	close(runner.gate)
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dep, _ := db.GetDeployment(context.Background(), id)
		if dep.Status.IsTerminal() {
			if dep.Status == cobaltapi.StateCanceled {
				t.Fatalf("deploy was canceled despite refusal during swapping: %+v", dep)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("deploy never reached terminal state after gate release")
}

func TestRecoverOnBoot(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	pid := newProject(t, db, "api")
	ctx := context.Background()

	// Four deploys: queued, building, swapping, success.
	queuedID, _, _ := NewQueue(db).Enqueue(ctx, EnqueueRequest{ProjectID: pid})
	buildingID, _, _ := NewQueue(db).Enqueue(ctx, EnqueueRequest{ProjectID: pid})
	_ = db.SetDeploymentStatus(ctx, buildingID, cobaltapi.StateBuilding)
	swappingID, _, _ := NewQueue(db).Enqueue(ctx, EnqueueRequest{ProjectID: pid})
	_ = db.SetDeploymentStatus(ctx, swappingID, cobaltapi.StateSwapping)
	successID, _, _ := NewQueue(db).Enqueue(ctx, EnqueueRequest{ProjectID: pid})
	_ = db.SetDeploymentStatus(ctx, successID, cobaltapi.StateSuccess)

	if err := RecoverOnBoot(ctx, db, quietLogger()); err != nil {
		t.Fatalf("RecoverOnBoot: %v", err)
	}

	cases := []struct {
		id   int64
		want cobaltapi.State
	}{
		{queuedID, cobaltapi.StateQueued},   // untouched
		{buildingID, cobaltapi.StateFailed}, // recovered
		{swappingID, cobaltapi.StateFailed}, // recovered (crashed mid-cutover)
		{successID, cobaltapi.StateSuccess}, // untouched
	}
	for _, c := range cases {
		dep, err := db.GetDeployment(ctx, c.id)
		if err != nil {
			t.Fatalf("get %d: %v", c.id, err)
		}
		if dep.Status != c.want {
			t.Errorf("id %d: status got %q, want %q", c.id, dep.Status, c.want)
		}
	}
}

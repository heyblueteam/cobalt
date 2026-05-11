package deploy

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// recoveryHarness creates a project + N deployments with the given
// statuses, then returns the DB so the caller can run RecoverOnBoot.
func recoveryHarness(t *testing.T, statuses ...cobaltapi.State) (*store.DB, int64, []int64) {
	t.Helper()
	db := openTestDB(t)
	pid := newProject(t, db, "api")
	ctx := context.Background()

	ids := make([]int64, 0, len(statuses))
	for i, status := range statuses {
		id, err := db.CreateDeployment(ctx, store.Deployment{
			ProjectID: pid, Number: i + 1, Status: cobaltapi.StateQueued,
		})
		if err != nil {
			t.Fatalf("CreateDeployment: %v", err)
		}
		// Transition to target status (CreateDeployment forces Queued via SQL).
		if status != cobaltapi.StateQueued {
			if err := db.SetDeploymentStatus(ctx, id, status); err != nil {
				t.Fatalf("SetDeploymentStatus: %v", err)
			}
		}
		ids = append(ids, id)
	}
	return db, pid, ids
}

func statusOf(t *testing.T, db *store.DB, id int64) cobaltapi.State {
	t.Helper()
	d, err := db.GetDeployment(context.Background(), id)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	return d.Status
}

func TestRecoverOnBoot_NoActiveIsNoop(t *testing.T) {
	t.Parallel()
	db, _, ids := recoveryHarness(t,
		cobaltapi.StateSuccess,
		cobaltapi.StateFailed,
		cobaltapi.StateCanceled,
	)
	if err := RecoverOnBoot(context.Background(), db, quietLog()); err != nil {
		t.Fatalf("RecoverOnBoot: %v", err)
	}
	// All terminal deployments must be untouched.
	for i, want := range []cobaltapi.State{
		cobaltapi.StateSuccess, cobaltapi.StateFailed, cobaltapi.StateCanceled,
	} {
		if got := statusOf(t, db, ids[i]); got != want {
			t.Errorf("deployment %d: got %q, want %q (terminal must not be rewritten)", i, got, want)
		}
	}
}

// TestRecoverOnBoot_QueuedIsLeftAlone — queued is not "active"; the
// dispatcher picks it up on its first sweep. The recovery sweep must not
// rewrite it to failed.
func TestRecoverOnBoot_QueuedIsLeftAlone(t *testing.T) {
	t.Parallel()
	db, _, ids := recoveryHarness(t, cobaltapi.StateQueued)
	if err := RecoverOnBoot(context.Background(), db, quietLog()); err != nil {
		t.Fatalf("RecoverOnBoot: %v", err)
	}
	if got := statusOf(t, db, ids[0]); got != cobaltapi.StateQueued {
		t.Errorf("queued deploy rewritten to %q (must stay queued)", got)
	}
}

// TestRecoverOnBoot_MarksAllActiveStatesFailed — every state the
// dispatcher could have left mid-flight must end up failed.
func TestRecoverOnBoot_MarksAllActiveStatesFailed(t *testing.T) {
	t.Parallel()
	db, _, ids := recoveryHarness(t,
		cobaltapi.StateFetching,
		cobaltapi.StateBuilding,
		cobaltapi.StateSwapping,
	)
	if err := RecoverOnBoot(context.Background(), db, quietLog()); err != nil {
		t.Fatalf("RecoverOnBoot: %v", err)
	}
	for i, id := range ids {
		if got := statusOf(t, db, id); got != cobaltapi.StateFailed {
			t.Errorf("active deploy %d ended at %q, want failed", i, got)
		}
	}
}

// TestRecoverOnBoot_MarksFinishedAtOnFail — SetDeploymentStatus writes
// finished_at when transitioning to terminal. Recovery must produce a
// row that looks like a normal failed deploy (so `cobalt deployments
// list` shows it as resolved, not eternally in-flight).
func TestRecoverOnBoot_StampsFinishedAt(t *testing.T) {
	t.Parallel()
	db, _, ids := recoveryHarness(t, cobaltapi.StateBuilding)
	if err := RecoverOnBoot(context.Background(), db, quietLog()); err != nil {
		t.Fatalf("RecoverOnBoot: %v", err)
	}
	d, err := db.GetDeployment(context.Background(), ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if d.FinishedAt == nil {
		t.Error("recovered deploy missing finished_at — list views will show it as in-flight forever")
	}
}

// TestRecoverOnBoot_LogsCountAndPerRow — operators rely on the boot
// log to notice "we crashed and marked N deploys failed". The Info line
// must fire when count > 0, and per-row Warn lines must include the
// previous status (so a postmortem can see whether we died fetching,
// building, or swapping).
func TestRecoverOnBoot_LogsContainPreviousStatus(t *testing.T) {
	t.Parallel()
	db, _, _ := recoveryHarness(t,
		cobaltapi.StateBuilding,
		cobaltapi.StateSwapping,
	)
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := RecoverOnBoot(context.Background(), db, log); err != nil {
		t.Fatalf("RecoverOnBoot: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "previous_status=building") {
		t.Errorf("log missing previous_status=building: %s", out)
	}
	if !strings.Contains(out, "previous_status=swapping") {
		t.Errorf("log missing previous_status=swapping: %s", out)
	}
	if !strings.Contains(out, "in-flight deploys reconciled") {
		t.Errorf("log missing summary line: %s", out)
	}
	if !strings.Contains(out, "count=2") {
		t.Errorf("log missing count=2: %s", out)
	}
}

// TestRecoverOnBoot_NilLoggerOK — RecoverOnBoot is called from main()
// which may pass nil. It must fall back to slog.Default rather than
// panic.
func TestRecoverOnBoot_NilLoggerOK(t *testing.T) {
	t.Parallel()
	db, _, ids := recoveryHarness(t, cobaltapi.StateBuilding)
	if err := RecoverOnBoot(context.Background(), db, nil); err != nil {
		t.Fatalf("RecoverOnBoot(nil log): %v", err)
	}
	if got := statusOf(t, db, ids[0]); got != cobaltapi.StateFailed {
		t.Errorf("status with nil log: got %q, want failed", got)
	}
}

// TestRecoverOnBoot_MixedBatch — realistic scenario: some terminal,
// some queued (waiting), some mid-flight. Only mid-flight transitions.
func TestRecoverOnBoot_MixedBatch(t *testing.T) {
	t.Parallel()
	db, _, ids := recoveryHarness(t,
		cobaltapi.StateSuccess,  // 0 — keep
		cobaltapi.StateQueued,   // 1 — keep (dispatcher will pick up)
		cobaltapi.StateFetching, // 2 — fail
		cobaltapi.StateBuilding, // 3 — fail
		cobaltapi.StateSwapping, // 4 — fail
		cobaltapi.StateFailed,   // 5 — keep
	)
	if err := RecoverOnBoot(context.Background(), db, quietLog()); err != nil {
		t.Fatalf("RecoverOnBoot: %v", err)
	}
	want := []cobaltapi.State{
		cobaltapi.StateSuccess,
		cobaltapi.StateQueued,
		cobaltapi.StateFailed,
		cobaltapi.StateFailed,
		cobaltapi.StateFailed,
		cobaltapi.StateFailed,
	}
	for i, w := range want {
		if got := statusOf(t, db, ids[i]); got != w {
			t.Errorf("deployment %d: got %q, want %q", i, got, w)
		}
	}
}

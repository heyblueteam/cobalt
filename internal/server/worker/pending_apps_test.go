package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePendingApps struct {
	deletedBefore int64
	rowsDeleted   int64
	err           error
}

func (f *fakePendingApps) DeleteExpiredPendingApps(_ context.Context, nowUnix int64) (int64, error) {
	f.deletedBefore = nowUnix
	return f.rowsDeleted, f.err
}

func TestCleanupExpiredPendingApps(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	store := &fakePendingApps{rowsDeleted: 3}

	n, err := CleanupExpiredPendingApps(context.Background(), quietLogger(), store, now)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if n != 3 {
		t.Errorf("count: got %d, want 3", n)
	}
	if store.deletedBefore != now.Unix() {
		t.Errorf("now passed to store: got %d, want %d", store.deletedBefore, now.Unix())
	}
}

func TestCleanupExpiredPendingApps_NoRowsIsFine(t *testing.T) {
	t.Parallel()
	store := &fakePendingApps{rowsDeleted: 0}
	if _, err := CleanupExpiredPendingApps(context.Background(), quietLogger(), store, time.Now()); err != nil {
		t.Errorf("err: %v", err)
	}
}

func TestCleanupExpiredPendingApps_StoreErrorBubbles(t *testing.T) {
	t.Parallel()
	store := &fakePendingApps{err: errors.New("db down")}
	if _, err := CleanupExpiredPendingApps(context.Background(), quietLogger(), store, time.Now()); err == nil {
		t.Error("expected error")
	}
}

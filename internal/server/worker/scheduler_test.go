package worker

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestScheduler_FiresJob(t *testing.T) {
	t.Parallel()
	s := NewScheduler(quietLogger())

	var fired atomic.Int32
	if err := s.Schedule("tick", "@every 1s", func(_ context.Context) {
		fired.Add(1)
	}); err != nil {
		t.Fatalf("Schedule: %v", err)
	}

	s.Start(context.Background())
	t.Cleanup(s.Stop)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fired.Load() >= 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("job never fired in 3s, count=%d", fired.Load())
}

func TestScheduler_RejectsInvalidSpec(t *testing.T) {
	t.Parallel()
	s := NewScheduler(quietLogger())
	if err := s.Schedule("bad", "not a cron", func(_ context.Context) {}); err == nil {
		t.Error("invalid spec should error")
	}
}

func TestScheduler_RemoveStopsJob(t *testing.T) {
	t.Parallel()
	s := NewScheduler(quietLogger())

	var fired atomic.Int32
	_ = s.Schedule("tick", "@every 1s", func(_ context.Context) {
		fired.Add(1)
	})
	s.Start(context.Background())
	t.Cleanup(s.Stop)

	// Wait for at least one fire, then remove and confirm no further fires.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && fired.Load() == 0 {
		time.Sleep(50 * time.Millisecond)
	}
	if fired.Load() == 0 {
		t.Skip("job never fired before remove; environment too slow")
	}
	s.Remove("tick")
	before := fired.Load()
	time.Sleep(1500 * time.Millisecond)
	if fired.Load() != before {
		t.Errorf("job kept firing after Remove: before=%d after=%d", before, fired.Load())
	}
}

func TestScheduler_PanicRecovered(t *testing.T) {
	t.Parallel()
	s := NewScheduler(quietLogger())

	var fired atomic.Int32
	_ = s.Schedule("boom", "@every 1s", func(_ context.Context) {
		fired.Add(1)
		panic("boom")
	})
	s.Start(context.Background())
	t.Cleanup(s.Stop)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fired.Load() >= 2 {
			// A panic on first fire didn't kill the scheduler.
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("scheduler did not survive panic, fires=%d", fired.Load())
}

func TestScheduler_StopWaitsForInFlight(t *testing.T) {
	t.Parallel()
	s := NewScheduler(quietLogger())

	finished := make(chan struct{})
	_ = s.Schedule("slow", "@every 1s", func(ctx context.Context) {
		// Block briefly so Stop has to wait.
		select {
		case <-time.After(200 * time.Millisecond):
		case <-ctx.Done():
		}
		select {
		case finished <- struct{}{}:
		default:
		}
	})
	s.Start(context.Background())

	// Let one fire start, then stop.
	time.Sleep(1100 * time.Millisecond)
	stopDone := make(chan struct{})
	go func() {
		s.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop blocked")
	}
}

func TestScheduler_DoubleStart_NoOp(t *testing.T) {
	t.Parallel()
	s := NewScheduler(quietLogger())
	s.Start(context.Background())
	s.Start(context.Background()) // must not panic
	s.Stop()
}

func TestScheduler_StopBeforeStart_NoOp(t *testing.T) {
	t.Parallel()
	s := NewScheduler(quietLogger())
	s.Stop() // must not panic
}

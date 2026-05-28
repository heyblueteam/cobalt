package worker

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakePinger struct{ err error }

func (f *fakePinger) Ping(_ context.Context) error { return f.err }

type fakeRestarter struct {
	calls int
	names []string
	err   error
}

func (f *fakeRestarter) RestartService(_ context.Context, name string) error {
	f.calls++
	f.names = append(f.names, name)
	return f.err
}

func TestCaddyWatchdog_HealthyNeverRestarts(t *testing.T) {
	t.Parallel()
	r := &fakeRestarter{}
	w := NewCaddyWatchdog(&fakePinger{}, r, "", quietLogger())
	for i := 0; i < 10; i++ {
		w.Tick(context.Background())
	}
	if r.calls != 0 {
		t.Fatalf("restart calls: %d, want 0 (admin always healthy)", r.calls)
	}
}

func TestCaddyWatchdog_RestartsAfterThreshold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &fakeRestarter{}
	w := NewCaddyWatchdog(&fakePinger{err: errors.New("admin unreachable")}, r, "", quietLogger())

	w.Tick(ctx)
	w.Tick(ctx)
	if r.calls != 0 {
		t.Fatalf("restart calls after 2 failures: %d, want 0 (threshold is 3)", r.calls)
	}
	w.Tick(ctx) // 3rd consecutive failure → restart
	if r.calls != 1 {
		t.Fatalf("restart calls after 3 failures: %d, want 1", r.calls)
	}
	if len(r.names) != 1 || r.names[0] != DefaultCaddyServiceName {
		t.Errorf("restarted service: %v, want [%s]", r.names, DefaultCaddyServiceName)
	}
	// The success path resets the failure count, so the very next failing
	// tick must NOT immediately restart again.
	w.Tick(ctx)
	if r.calls != 1 {
		t.Errorf("restart calls after one post-restart failure: %d, want 1 (counter reset)", r.calls)
	}
}

func TestCaddyWatchdog_CooldownSuppressesThenAllows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := time.Now()
	r := &fakeRestarter{}
	w := NewCaddyWatchdog(&fakePinger{err: errors.New("down")}, r, "", quietLogger())
	w.now = func() time.Time { return clock }

	for i := 0; i < 3; i++ { // first restart
		w.Tick(ctx)
	}
	if r.calls != 1 {
		t.Fatalf("restart calls: %d, want 1", r.calls)
	}
	for i := 0; i < 3; i++ { // sustained failure, still within cooldown
		w.Tick(ctx)
	}
	if r.calls != 1 {
		t.Fatalf("restart calls during cooldown: %d, want 1 (suppressed)", r.calls)
	}
	clock = clock.Add(6 * time.Minute) // past the 5m cooldown
	w.Tick(ctx)
	if r.calls != 2 {
		t.Fatalf("restart calls after cooldown elapsed: %d, want 2", r.calls)
	}
}

func TestCaddyWatchdog_RecoveryResetsCounter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := &fakeRestarter{}
	p := &fakePinger{err: errors.New("down")}
	w := NewCaddyWatchdog(p, r, "", quietLogger())

	w.Tick(ctx)
	w.Tick(ctx) // 2 consecutive failures
	p.err = nil
	w.Tick(ctx) // recovery resets the counter
	p.err = errors.New("down again")
	w.Tick(ctx)
	w.Tick(ctx) // only 2 fresh failures
	if r.calls != 0 {
		t.Fatalf("restart calls: %d, want 0 (recovery reset the counter)", r.calls)
	}
	w.Tick(ctx) // 3rd consecutive → restart
	if r.calls != 1 {
		t.Fatalf("restart calls: %d, want 1", r.calls)
	}
}

func TestCaddyWatchdog_RestartErrorRetriesNextTick(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := time.Now()
	r := &fakeRestarter{err: errors.New("docker unreachable")}
	w := NewCaddyWatchdog(&fakePinger{err: errors.New("down")}, r, "", quietLogger())
	w.now = func() time.Time { return clock }

	for i := 0; i < 3; i++ {
		w.Tick(ctx)
	}
	if r.calls != 1 {
		t.Fatalf("restart attempts: %d, want 1", r.calls)
	}
	// A failed restart must not start the cooldown clock — otherwise a
	// recovery that never happened would gate the retry. Next failing tick
	// should attempt again immediately.
	w.Tick(ctx)
	if r.calls != 2 {
		t.Fatalf("restart attempts after a failed restart: %d, want 2 (retried)", r.calls)
	}
}

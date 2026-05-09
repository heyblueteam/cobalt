package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// fakeProber records every Exec call and returns a configurable result
// per call. Used to drive waitHTTPReady through "not ready, not ready,
// ready" sequences without real Docker.
type fakeProber struct {
	mu    sync.Mutex
	calls int32
	// results[i] is the error (or nil) returned for call i. Calls past
	// the slice length reuse the last entry.
	results []error
	// stderrs[i] is what gets written to stderr for call i.
	stderrs []string
	// recordCmd captures the exact argv each call; useful for asserting
	// the probe targets the right container/port.
	recordCmd []string
}

// FindContainerByLabel satisfies the ReadinessProber interface.
// Tests don't exercise the lookup path; return the compose-mode
// fallback name so probeTCP behaves as before.
func (f *fakeProber) FindContainerByLabel(_ context.Context, _ string) (string, error) {
	return "cobalt-caddy", nil
}

func (f *fakeProber) Exec(ctx context.Context, container string, cmd []string, stdout, stderr io.Writer) error {
	idx := atomic.AddInt32(&f.calls, 1) - 1
	f.mu.Lock()
	f.recordCmd = append([]string(nil), cmd...)
	pick := func(idx int32, slice []error) error {
		if int(idx) < len(slice) {
			return slice[idx]
		}
		if len(slice) == 0 {
			return nil
		}
		return slice[len(slice)-1]
	}
	pickStr := func(idx int32, slice []string) string {
		if int(idx) < len(slice) {
			return slice[idx]
		}
		return ""
	}
	res := pick(idx, f.results)
	se := pickStr(idx, f.stderrs)
	f.mu.Unlock()
	if se != "" {
		_, _ = stderr.Write([]byte(se))
	}
	return res
}

func cfWithWeb(port int) *cobaltfile.Cobaltfile {
	return &cobaltfile.Cobaltfile{
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Port: port},
		},
	}
}

func TestWaitHTTPReady_SucceedsImmediately(t *testing.T) {
	t.Parallel()
	p := &fakeProber{results: []error{nil}}
	var out bytes.Buffer
	err := waitHTTPReady(context.Background(), p,
		store.Project{Name: "demo"}, store.Deployment{Number: 1},
		cfWithWeb(3000), &out)
	if err != nil {
		t.Fatalf("waitHTTPReady: %v", err)
	}
	if !strings.Contains(out.String(), "web is listening on :3000") {
		t.Errorf("expected success log, got: %s", out.String())
	}
	// Probe must target the correct service:port via nc.
	got := strings.Join(p.recordCmd, " ")
	if !strings.Contains(got, "demo-1-web 3000") {
		t.Errorf("probe targeted wrong service:port: %q", got)
	}
}

func TestWaitHTTPReady_RetriesUntilReady(t *testing.T) {
	t.Parallel()
	p := &fakeProber{
		results: []error{
			errors.New("nc: connect refused"),
			errors.New("nc: connect refused"),
			nil,
		},
		stderrs: []string{"nc: connect refused\n", "nc: connect refused\n"},
	}
	// Speed up the test by overriding poll cadence via context deadline.
	// We don't expose a knob — instead, the test relies on the "fast
	// enough to land within 1s of test setup" property by giving 3
	// retries with a 2s real poll. Use a 10s test deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out bytes.Buffer
	if err := waitHTTPReady(ctx, p,
		store.Project{Name: "demo"}, store.Deployment{Number: 1},
		cfWithWeb(3000), &out); err != nil {
		t.Fatalf("waitHTTPReady: %v", err)
	}
	if got := atomic.LoadInt32(&p.calls); got < 3 {
		t.Errorf("expected at least 3 probes, got %d", got)
	}
}

func TestWaitHTTPReady_TimesOut(t *testing.T) {
	t.Parallel()
	p := &fakeProber{
		results: []error{errors.New("nc: connect refused")},
		stderrs: []string{"nc: connect refused\n"},
	}
	// Keep the test fast by canceling well before the real timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	err := waitHTTPReady(ctx, p,
		store.Project{Name: "demo"}, store.Deployment{Number: 1},
		cfWithWeb(3000), &out)
	if err == nil {
		t.Fatal("expected timeout/cancel error, got nil")
	}
	// Either ctx.Err() or our detailed timeout message — both indicate
	// the probe correctly didn't claim success.
	if !errors.Is(err, context.DeadlineExceeded) &&
		!strings.Contains(err.Error(), "did not accept connections") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWaitHTTPReady_SkipsNonContainerWeb(t *testing.T) {
	t.Parallel()
	p := &fakeProber{}
	cf := &cobaltfile.Cobaltfile{
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeStatic},
		},
	}
	var out bytes.Buffer
	if err := waitHTTPReady(context.Background(), p,
		store.Project{Name: "demo"}, store.Deployment{Number: 1},
		cf, &out); err != nil {
		t.Fatalf("waitHTTPReady (static): %v", err)
	}
	if got := atomic.LoadInt32(&p.calls); got != 0 {
		t.Errorf("static web should not be probed; got %d calls", got)
	}
}

func TestWaitHTTPReady_SkipsWhenNoWeb(t *testing.T) {
	t.Parallel()
	p := &fakeProber{}
	cf := &cobaltfile.Cobaltfile{
		Services: map[string]cobaltfile.Service{
			"worker": {Type: cobaltfile.TypeContainer, Port: 0},
		},
	}
	var out bytes.Buffer
	if err := waitHTTPReady(context.Background(), p,
		store.Project{Name: "demo"}, store.Deployment{Number: 1},
		cf, &out); err != nil {
		t.Fatalf("waitHTTPReady (no web): %v", err)
	}
	if got := atomic.LoadInt32(&p.calls); got != 0 {
		t.Errorf("no-web project should not be probed; got %d calls", got)
	}
}

func TestWaitHTTPReady_DefaultsPortWhenZero(t *testing.T) {
	t.Parallel()
	p := &fakeProber{results: []error{nil}}
	cf := &cobaltfile.Cobaltfile{
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Port: 0},
		},
	}
	var out bytes.Buffer
	if err := waitHTTPReady(context.Background(), p,
		store.Project{Name: "demo"}, store.Deployment{Number: 1},
		cf, &out); err != nil {
		t.Fatalf("waitHTTPReady: %v", err)
	}
	want := fmt.Sprintf("%d", cobaltfile.DefaultPort)
	got := strings.Join(p.recordCmd, " ")
	if !strings.Contains(got, " "+want) {
		t.Errorf("probe should default to DefaultPort=%s, got cmd: %q", want, got)
	}
}

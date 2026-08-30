package worker

import (
	"context"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// fakeDataPlaneProber returns a canned ServedDeployment for every domain.
type fakeDataPlaneProber struct {
	served string
	status int
	err    error
}

func (f fakeDataPlaneProber) ServedDeployment(context.Context, string) (string, int, error) {
	return f.served, f.status, f.err
}

type domainProbeResult struct {
	served string
	status int
	err    error
}

type domainDataPlaneProber map[string]domainProbeResult

func (f domainDataPlaneProber) ServedDeployment(_ context.Context, domain string) (string, int, error) {
	result := f[domain]
	return result.served, result.status, result.err
}

// fakeReaper records RemoveService calls and serves a fixed service list.
type fakeReaper struct {
	services []docker.ServiceInfo
	removed  []string
	listErr  error
}

func (f *fakeReaper) ListServicesForProject(context.Context, int64) ([]docker.ServiceInfo, error) {
	return f.services, f.listErr
}

func (f *fakeReaper) RemoveService(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

// inSyncFixture builds a store + caddy where the admin config tree is correct
// (upstream == api-7-web): the case where only a data-plane probe can reveal a
// lagging compiled router. The live deployment's cutover (FinishedAt) is set
// well past reapMinAge by default, so tests exercising confirmation/repair
// behavior aren't incidentally blocked by the grace-floor check — tests that
// specifically want to exercise the floor build their own fixture.
func inSyncFixture(t *testing.T) (*fakeReconcileStore, *fakeReconcileCaddy) {
	t.Helper()
	return inSyncFixtureFinishedAgo(t, reapMinAge*10)
}

// inSyncFixtureFinishedAgo is inSyncFixture parameterized on how long ago the
// live deployment's cutover completed, for tests exercising reapMinAge.
func inSyncFixtureFinishedAgo(t *testing.T, ago time.Duration) (*fakeReconcileStore, *fakeReconcileCaddy) {
	t.Helper()
	cf := mustCobaltfileJSON(t, &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	})
	finishedAt := time.Now().Add(-ago).Unix()
	st := &fakeReconcileStore{
		projects: []store.Project{{ID: 1, Name: "api"}},
		lastByID: map[int64]*store.Deployment{
			1: {
				ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf), FinishedAt: &finishedAt,
			},
		},
		domains: map[int64][]string{1: {"api.example.com"}},
	}
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = true
	cy.upstream[1] = "api-7-web" // admin tree is correct
	return st, cy
}

func TestReconcile_DataPlaneServesOldDeployment_ForcesRepair(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixture(t)
	dp := fakeDataPlaneProber{served: "5", status: 200} // router still on #5

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, nil)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 1 {
		t.Errorf("corrected: got %d, want 1", corrected)
	}
	if len(cy.serveCalls) != 1 || cy.serveCalls[0].Container != "api-7-web" {
		t.Errorf("expected one ServeService repair to api-7-web, got %+v", cy.serveCalls)
	}
}

func TestReconcile_DataPlaneGatewayError_ForcesRepair(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixture(t)
	dp := fakeDataPlaneProber{served: "", status: 502} // dialing a dead upstream

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, nil)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 1 {
		t.Errorf("corrected: got %d, want 1 (502 = divergence)", corrected)
	}
	if len(cy.serveCalls) != 1 {
		t.Errorf("expected one ServeService repair, got %d", len(cy.serveCalls))
	}
}

func TestReconcile_DataPlaneNoHeader_NoRepair(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixture(t)
	dp := fakeDataPlaneProber{served: "", status: 200} // pre-header handler / unknown

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, nil)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 || len(cy.serveCalls) != 0 {
		t.Errorf("served=\"\" must be treated as unknown, not drift: corrected=%d serveCalls=%d",
			corrected, len(cy.serveCalls))
	}
}

func TestReconcile_DataPlaneProbeInconclusive_NoRepair(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixture(t)
	dp := fakeDataPlaneProber{err: context.DeadlineExceeded} // caddy unreachable

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, nil)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 || len(cy.serveCalls) != 0 {
		t.Errorf("inconclusive probe must not force a repair: corrected=%d serveCalls=%d",
			corrected, len(cy.serveCalls))
	}
}

func TestReconcile_DataPlaneHealthy_ReapsSupersededWebOnly(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixture(t)
	dp := fakeDataPlaneProber{served: "7", status: 200} // router confirmed on #7
	reaper := &fakeReaper{services: []docker.ServiceInfo{
		{Name: "api-7-web"},    // current — keep
		{Name: "api-6-web"},    // superseded web — reap
		{Name: "api-6-worker"}, // worker — not our concern here
		{Name: "other-6-web"},  // different project — ignore
	}}

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, reaper)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 || len(cy.serveCalls) != 0 {
		t.Errorf("healthy data plane must not repair: corrected=%d serveCalls=%d", corrected, len(cy.serveCalls))
	}
	if len(reaper.removed) != 1 || reaper.removed[0] != "api-6-web" {
		t.Errorf("expected to reap only api-6-web, got %v", reaper.removed)
	}
}

func TestReconcile_InconclusiveDomain_TriesNextDomain(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixture(t)
	st.domains[1] = []string{"legacy.example.com", "api.example.com"}
	dp := domainDataPlaneProber{
		"legacy.example.com": {err: context.DeadlineExceeded},
		"api.example.com":    {served: "7", status: 200},
	}
	reaper := &fakeReaper{services: []docker.ServiceInfo{
		{Name: "api-7-web"},
		{Name: "api-6-web"},
	}}

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, reaper)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 || len(cy.serveCalls) != 0 {
		t.Errorf("healthy fallback domain must not repair: corrected=%d serveCalls=%d",
			corrected, len(cy.serveCalls))
	}
	if len(reaper.removed) != 1 || reaper.removed[0] != "api-6-web" {
		t.Errorf("expected confirmation from api.example.com to permit reap, got %v", reaper.removed)
	}
}

// TestReconcile_NilProber_NeverReapsOrRepairs replaces what was previously
// TestReconcile_NilProber_StillReapsButNeverDataPlaneRepairs, which asserted
// the 2026-07-14 incident's actual bug as intended behavior: a disabled
// prober (dp == nil) is "unknown," not confirmation, and must never license a
// reap — only a genuinely-confirmed data-plane probe may.
func TestReconcile_NilProber_NeverReapsOrRepairs(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixture(t)
	reaper := &fakeReaper{services: []docker.ServiceInfo{
		{Name: "api-7-web"},
		{Name: "api-6-web"},
	}}

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, nil, reaper)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 || len(cy.serveCalls) != 0 {
		t.Errorf("nil prober must not repair: corrected=%d serveCalls=%d", corrected, len(cy.serveCalls))
	}
	if len(reaper.removed) != 0 {
		t.Errorf("nil prober is unknown, not confirmed — must not reap: got %v", reaper.removed)
	}
}

// TestReconcile_ProbeInconclusive_NeverReaps covers the exact incident shape:
// a probe error (Caddy unreachable / timeout) must not license a reap, same
// as a nil prober.
func TestReconcile_ProbeInconclusive_NeverReaps(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixture(t)
	dp := fakeDataPlaneProber{err: context.DeadlineExceeded}
	reaper := &fakeReaper{services: []docker.ServiceInfo{
		{Name: "api-7-web"},
		{Name: "api-6-web"},
	}}

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, reaper)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 || len(cy.serveCalls) != 0 {
		t.Errorf("inconclusive probe must not repair: corrected=%d serveCalls=%d", corrected, len(cy.serveCalls))
	}
	if len(reaper.removed) != 0 {
		t.Errorf("inconclusive probe is unknown, not confirmed — must not reap: got %v", reaper.removed)
	}
}

// TestReconcile_NoHeader_NeverReaps covers served=="" (a pre-header handler):
// treated as unknown, so it must not repair (already covered above) and must
// also not reap.
func TestReconcile_NoHeader_NeverReaps(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixture(t)
	dp := fakeDataPlaneProber{served: "", status: 200}
	reaper := &fakeReaper{services: []docker.ServiceInfo{
		{Name: "api-7-web"},
		{Name: "api-6-web"},
	}}

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, reaper)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 || len(cy.serveCalls) != 0 {
		t.Errorf("no-header must not repair: corrected=%d serveCalls=%d", corrected, len(cy.serveCalls))
	}
	if len(reaper.removed) != 0 {
		t.Errorf("no-header is unknown, not confirmed — must not reap: got %v", reaper.removed)
	}
}

// TestReconcile_ConfirmedButWithinGraceFloor_DoesNotReapYet is the direct
// regression test for the 2026-07-14 incident: even with a genuinely
// confirmed data-plane probe, a generation reaped within reapMinAge of its
// own cutover is exactly the window where a Cloudflare-pooled connection can
// still be pinned to the old Docker DNS name.
func TestReconcile_ConfirmedButWithinGraceFloor_DoesNotReapYet(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixtureFinishedAgo(t, 10*time.Second) // well under reapMinAge
	dp := fakeDataPlaneProber{served: "7", status: 200}   // genuinely confirmed
	reaper := &fakeReaper{services: []docker.ServiceInfo{
		{Name: "api-7-web"},
		{Name: "api-6-web"},
	}}

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, reaper)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 || len(cy.serveCalls) != 0 {
		t.Errorf("confirmed healthy data plane must not repair: corrected=%d serveCalls=%d", corrected, len(cy.serveCalls))
	}
	if len(reaper.removed) != 0 {
		t.Errorf("confirmed but within reapMinAge of cutover — must not reap yet: got %v", reaper.removed)
	}
}

// TestReconcile_ConfirmedAndPastGraceFloor_Reaps confirms reaping still
// happens once both conditions hold: genuine confirmation AND enough time
// elapsed since cutover.
func TestReconcile_ConfirmedAndPastGraceFloor_Reaps(t *testing.T) {
	t.Parallel()
	st, cy := inSyncFixtureFinishedAgo(t, reapMinAge+time.Minute)
	dp := fakeDataPlaneProber{served: "7", status: 200}
	reaper := &fakeReaper{services: []docker.ServiceInfo{
		{Name: "api-7-web"},
		{Name: "api-6-web"},
	}}

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, reaper)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 || len(cy.serveCalls) != 0 {
		t.Errorf("confirmed healthy data plane must not repair: corrected=%d serveCalls=%d", corrected, len(cy.serveCalls))
	}
	if len(reaper.removed) != 1 || reaper.removed[0] != "api-6-web" {
		t.Errorf("expected to reap api-6-web once confirmed and past grace floor, got %v", reaper.removed)
	}
}

// TestReconcile_NoFinishedAt_NeverReaps guards the defensive nil-check: a
// deployment row missing a cutover timestamp must not be reaped from, rather
// than guessing at its age.
func TestReconcile_NoFinishedAt_NeverReaps(t *testing.T) {
	t.Parallel()
	cf := mustCobaltfileJSON(t, &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	})
	st := &fakeReconcileStore{
		projects: []store.Project{{ID: 1, Name: "api"}},
		lastByID: map[int64]*store.Deployment{
			1: {
				ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf), FinishedAt: nil,
			},
		},
		domains: map[int64][]string{1: {"api.example.com"}},
	}
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = true
	cy.upstream[1] = "api-7-web"
	dp := fakeDataPlaneProber{served: "7", status: 200}
	reaper := &fakeReaper{services: []docker.ServiceInfo{
		{Name: "api-7-web"},
		{Name: "api-6-web"},
	}}

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy, dp, reaper)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 || len(cy.serveCalls) != 0 {
		t.Errorf("confirmed healthy data plane must not repair: corrected=%d serveCalls=%d", corrected, len(cy.serveCalls))
	}
	if len(reaper.removed) != 0 {
		t.Errorf("missing FinishedAt must not reap: got %v", reaper.removed)
	}
}

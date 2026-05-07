package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// fakeReconcileStore is a hand-rolled in-memory store for the reconciler
// tests — keeps the assertion code tight.
type fakeReconcileStore struct {
	projects   []store.Project
	lastByID   map[int64]*store.Deployment
	domains    map[int64][]string
	listErr    error
	domainsErr error
}

func (f *fakeReconcileStore) ListProjects(_ context.Context) ([]store.Project, error) {
	return f.projects, f.listErr
}
func (f *fakeReconcileStore) GetLastSuccessfulDeployment(_ context.Context, projectID int64) (*store.Deployment, error) {
	d := f.lastByID[projectID]
	if d == nil {
		return nil, store.ErrNotFound
	}
	return d, nil
}
func (f *fakeReconcileStore) ListDomainsForProject(_ context.Context, projectID int64) ([]string, error) {
	return f.domains[projectID], f.domainsErr
}

// fakeReconcileCaddy records what the reconciler asks Caddy to do, plus
// lets tests stub responses to ProjectRouteExists and CurrentUpstream.
type fakeReconcileCaddy struct {
	mu sync.Mutex

	routeExists   map[int64]bool
	routeExistsErr error
	upstream      map[int64]string
	domainsErr    error

	addCalls    []int64
	serveCalls  []serveServiceCall
	staticCalls []serveStaticCall
	domainCalls []domainCall
}

type serveServiceCall struct {
	ProjectID int64
	Container string
	Port      int
}
type serveStaticCall struct {
	ProjectID        int64
	ProjectName      string
	DeploymentNumber int
}
type domainCall struct {
	ProjectID int64
	Domains   []string
}

func newFakeReconcileCaddy() *fakeReconcileCaddy {
	return &fakeReconcileCaddy{
		routeExists: map[int64]bool{},
		upstream:    map[int64]string{},
	}
}

func (f *fakeReconcileCaddy) ProjectRouteExists(_ context.Context, id int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.routeExists[id], f.routeExistsErr
}
func (f *fakeReconcileCaddy) AddProjectRoute(_ context.Context, id int64, _ []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.addCalls = append(f.addCalls, id)
	f.routeExists[id] = true
	return nil
}
func (f *fakeReconcileCaddy) CurrentUpstream(_ context.Context, id int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.upstream[id], nil
}
func (f *fakeReconcileCaddy) ServeService(_ context.Context, id int64, container string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serveCalls = append(f.serveCalls, serveServiceCall{id, container, port})
	f.upstream[id] = container
	return nil
}
func (f *fakeReconcileCaddy) ServeStaticSite(_ context.Context, id int64, name string, n int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staticCalls = append(f.staticCalls, serveStaticCall{id, name, n})
	return nil
}
func (f *fakeReconcileCaddy) SetDomainsForProject(_ context.Context, id int64, domains []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.domainCalls = append(f.domainCalls, domainCall{id, domains})
	return f.domainsErr
}

// helper builders

func mustCobaltfileJSON(t *testing.T, cf *cobaltfile.Cobaltfile) string {
	t.Helper()
	b, err := json.Marshal(cf)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}


// --- tests ---

func TestReconcile_NoCorrectionWhenInSync(t *testing.T) {
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
			1: {ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		domains: map[int64][]string{1: {"api.example.com"}},
	}
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = true
	cy.upstream[1] = "api-7-web" // matches expected ServiceName

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 {
		t.Errorf("corrected: got %d, want 0", corrected)
	}
	if len(cy.serveCalls) != 0 {
		t.Errorf("ServeService called %d times when in sync", len(cy.serveCalls))
	}
}

func TestReconcile_PatchesUpstreamOnDrift(t *testing.T) {
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
			1: {ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		domains: map[int64][]string{1: {"api.example.com"}},
	}
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = true
	cy.upstream[1] = "api-5-web" // drifted to old deploy

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 1 {
		t.Errorf("corrected: got %d, want 1", corrected)
	}
	if len(cy.serveCalls) != 1 {
		t.Fatalf("ServeService calls: %d, want 1", len(cy.serveCalls))
	}
	if cy.serveCalls[0].Container != "api-7-web" {
		t.Errorf("ServeService container: got %q, want api-7-web", cy.serveCalls[0].Container)
	}
	if cy.serveCalls[0].Port != 3000 {
		t.Errorf("ServeService port: got %d, want 3000", cy.serveCalls[0].Port)
	}
}

func TestReconcile_RecreatesMissingRoute(t *testing.T) {
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
			1: {ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		domains: map[int64][]string{1: {"api.example.com"}},
	}
	cy := newFakeReconcileCaddy()
	// Route doesn't exist (Caddy was wiped).
	cy.routeExists[1] = false

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 1 {
		t.Errorf("corrected: %d, want 1", corrected)
	}
	if len(cy.addCalls) != 1 || cy.addCalls[0] != 1 {
		t.Errorf("AddProjectRoute calls: %v", cy.addCalls)
	}
	if len(cy.serveCalls) != 1 {
		t.Errorf("ServeService calls: %d, want 1", len(cy.serveCalls))
	}
}

func TestReconcile_StaticSiteRecreatesViaServeStaticSite(t *testing.T) {
	t.Parallel()
	cf := mustCobaltfileJSON(t, &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeStatic, Image: "default", Port: 8000, PublicPath: "dist"},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	})
	st := &fakeReconcileStore{
		projects: []store.Project{{ID: 1, Name: "blog"}},
		lastByID: map[int64]*store.Deployment{
			1: {ID: 10, ProjectID: 1, Number: 4, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		domains: map[int64][]string{1: {"blog.example.com"}},
	}
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = false

	corrected, _ := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if corrected != 1 {
		t.Errorf("corrected: %d, want 1", corrected)
	}
	if len(cy.staticCalls) != 1 {
		t.Fatalf("ServeStaticSite calls: %d, want 1", len(cy.staticCalls))
	}
	if cy.staticCalls[0].DeploymentNumber != 4 {
		t.Errorf("ServeStaticSite deploy: got %d, want 4", cy.staticCalls[0].DeploymentNumber)
	}
}

func TestReconcile_StaticSiteSkipsDriftCheck(t *testing.T) {
	t.Parallel()
	cf := mustCobaltfileJSON(t, &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeStatic, Image: "default", Port: 8000, PublicPath: "dist"},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	})
	st := &fakeReconcileStore{
		projects: []store.Project{{ID: 1, Name: "blog"}},
		lastByID: map[int64]*store.Deployment{
			1: {ID: 10, ProjectID: 1, Number: 4, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		domains: map[int64][]string{1: {"blog.example.com"}},
	}
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = true

	corrected, _ := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if corrected != 0 {
		t.Errorf("static site existing route should NOT be corrected: %d", corrected)
	}
}

func TestReconcile_NoLastSuccessSkipsProject(t *testing.T) {
	t.Parallel()
	st := &fakeReconcileStore{
		projects: []store.Project{{ID: 1, Name: "api"}}, // never deployed
		lastByID: map[int64]*store.Deployment{},
	}
	cy := newFakeReconcileCaddy()
	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 0 {
		t.Errorf("corrected: %d, want 0", corrected)
	}
}

func TestReconcile_MissingResolvedCobaltfileSkips(t *testing.T) {
	t.Parallel()
	st := &fakeReconcileStore{
		projects: []store.Project{{ID: 1, Name: "api"}},
		lastByID: map[int64]*store.Deployment{
			// Pre-§8d row: ResolvedCobaltfile is the zero NullString.
			1: {ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess},
		},
	}
	cy := newFakeReconcileCaddy()
	corrected, _ := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if corrected != 0 {
		t.Errorf("corrected: %d, want 0 (no resolved cobaltfile)", corrected)
	}
}

func TestReconcile_NoDomainsSkips(t *testing.T) {
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
			1: {ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		// no domains
	}
	cy := newFakeReconcileCaddy()
	corrected, _ := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if corrected != 0 {
		t.Errorf("corrected: %d, want 0", corrected)
	}
}

func TestReconcile_NoWebServiceSkipsCaddy(t *testing.T) {
	t.Parallel()
	cf := mustCobaltfileJSON(t, &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"nightly": {Type: cobaltfile.TypeCron, Image: "default", Port: cobaltfile.DefaultPort,
				Schedule: "0 0 * * *", Command: "node nightly.js"},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	})
	st := &fakeReconcileStore{
		projects: []store.Project{{ID: 1, Name: "jobs"}},
		lastByID: map[int64]*store.Deployment{
			1: {ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		domains: map[int64][]string{1: {"jobs.example.com"}},
	}
	cy := newFakeReconcileCaddy()
	corrected, _ := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if corrected != 0 {
		t.Errorf("cron-only project should not trigger Caddy correction: %d", corrected)
	}
}

func TestReconcile_PerProjectFailureContinuesSweep(t *testing.T) {
	t.Parallel()
	cf := mustCobaltfileJSON(t, &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	})
	bad := mustCobaltfileJSON(t, &cobaltfile.Cobaltfile{Version: "1.0"}) // legal, no services

	st := &fakeReconcileStore{
		projects: []store.Project{
			{ID: 1, Name: "api"},
			{ID: 2, Name: "broken"},
		},
		lastByID: map[int64]*store.Deployment{
			1: {ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
			2: {ID: 20, ProjectID: 2, Number: 1, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr("not valid json")},
		},
		domains: map[int64][]string{1: {"api.example.com"}},
	}
	_ = bad
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = true
	cy.upstream[1] = "api-5-web" // drifted

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 1 {
		t.Errorf("corrected: %d, want 1 (broken project should be skipped)", corrected)
	}
}

func TestReconcile_ListProjectsErrorBubbles(t *testing.T) {
	t.Parallel()
	st := &fakeReconcileStore{listErr: errors.New("db down")}
	if _, err := ReconcileCaddyState(context.Background(), quietLogger(), st, newFakeReconcileCaddy()); err == nil {
		t.Error("expected error from project list failure")
	}
}

func TestReconcile_CaddyRouteMissingButPriorDeploy_CallsAddProjectRouteFirst(t *testing.T) {
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
			1: {ID: 10, ProjectID: 1, Number: 3, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		domains: map[int64][]string{1: {"api.example.com"}},
	}
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = false

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if corrected != 1 {
		t.Errorf("corrected: %d, want 1", corrected)
	}
	if len(cy.addCalls) != 1 {
		t.Errorf("AddProjectRoute calls: %d, want 1", len(cy.addCalls))
	}
	if len(cy.serveCalls) != 1 {
		t.Errorf("ServeService calls: %d, want 1", len(cy.serveCalls))
	}
	if len(cy.domainCalls) != 0 {
		t.Errorf("SetDomainsForProject should NOT be called in route-missing path: got %d calls", len(cy.domainCalls))
	}
}

func TestReconcile_DomainsDrifted_CallsSetDomainsEvenWhenUpstreamInSync(t *testing.T) {
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
			1: {ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		domains: map[int64][]string{1: {"api.example.com", "api-staging.example.com"}},
	}
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = true
	cy.upstream[1] = "api-7-web" // correct upstream

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// corrected=0 because upstream in sync (ServeService not called).
	// SetDomainsForProject IS still called to sync domain list.
	if len(cy.domainCalls) != 1 {
		t.Fatalf("SetDomainsForProject calls: %d, want 1", len(cy.domainCalls))
	}
	if len(cy.serveCalls) != 0 {
		t.Errorf("ServeService should NOT be called when upstream in sync: got %d calls", len(cy.serveCalls))
	}
	if corrected != 0 {
		t.Errorf("corrected: %d, want 0 (domains drifted but upstream in sync; SetDomains called, ServeService not)", corrected)
	}
}

func TestReconcile_SetDomainsFails_DoesNotHaltSweep(t *testing.T) {
	t.Parallel()
	cf := mustCobaltfileJSON(t, &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	})
	st := &fakeReconcileStore{
		projects: []store.Project{
			{ID: 1, Name: "api"},
			{ID: 2, Name: "www"},
		},
		lastByID: map[int64]*store.Deployment{
			1: {ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
			2: {ID: 20, ProjectID: 2, Number: 3, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		domains: map[int64][]string{
			1: {"api.example.com"},
			2: {"www.example.com"},
		},
	}
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = true
	cy.routeExists[2] = true
	cy.upstream[1] = "api-7-web"
	cy.upstream[2] = "www-3-web"
	cy.domainsErr = errors.New("caddy unreachable")

	corrected, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if err != nil {
		t.Fatalf("Reconcile: error should be nil (per-project failure absorbed), got: %v", err)
	}
	if corrected != 0 {
		t.Errorf("corrected: %d, want 0 (set domains failed so no correction counted)", corrected)
	}
}

func TestReconcile_ProjectRouteExistsError_DoesNotHaltSweep(t *testing.T) {
	t.Parallel()
	cf := mustCobaltfileJSON(t, &cobaltfile.Cobaltfile{
		Version: "1.0",
		Services: map[string]cobaltfile.Service{
			"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
		},
		Images: map[string]cobaltfile.Image{"default": {Dockerfile: "Dockerfile", Context: "."}},
	})
	st := &fakeReconcileStore{
		projects: []store.Project{
			{ID: 1, Name: "api"},
			{ID: 2, Name: "www"},
		},
		lastByID: map[int64]*store.Deployment{
			1: {ID: 10, ProjectID: 1, Number: 7, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
			2: {ID: 20, ProjectID: 2, Number: 3, Status: cobaltapi.StateSuccess,
				ResolvedCobaltfile: nullStr(cf)},
		},
		domains: map[int64][]string{
			1: {"api.example.com"},
			2: {"www.example.com"},
		},
	}
	cy := newFakeReconcileCaddy()
	cy.routeExists[1] = true
	cy.routeExists[2] = true
	cy.upstream[1] = "api-7-web"
	cy.upstream[2] = "www-3-web"
	cy.routeExistsErr = errors.New("caddy admin API down")

	_, err := ReconcileCaddyState(context.Background(), quietLogger(), st, cy)
	if err != nil {
		t.Fatalf("Reconcile: error should be nil (per-project failure absorbed), got: %v", err)
	}
}

// helper to build a *string for cobaltfile json (the resolved-cobaltfile
// column is nullable; nil = "no resolved cobaltfile yet").
func nullStr(raw string) *string {
	return &raw
}

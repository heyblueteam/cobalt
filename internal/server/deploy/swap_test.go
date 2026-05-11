package deploy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// fakeSwapCaddy records every call so swap tests can assert exact
// dispatch (container path vs static-site path vs no-op).
type fakeSwapCaddy struct {
	mu                sync.Mutex
	setDomainsCalls   int
	setDomainsArgs    [][]string
	verifyCalls       []verifyCall
	serveSvcCalls     []serveSvcCall
	staticCalls       []staticCall
	setDomainsErr     error
	verifyErr         error
	serveSvcErr       error
	staticErr         error
}

type verifyCall struct {
	projectID int64
	container string
	port      int
}
type serveSvcCall struct {
	projectID int64
	container string
	port      int
}
type staticCall struct {
	projectID        int64
	projectName      string
	deploymentNumber int
}

func (f *fakeSwapCaddy) SetDomainsForProject(_ context.Context, projectID int64, domains []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setDomainsCalls++
	f.setDomainsArgs = append(f.setDomainsArgs, domains)
	return f.setDomainsErr
}

func (f *fakeSwapCaddy) VerifyServeService(_ context.Context, projectID int64, container string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verifyCalls = append(f.verifyCalls, verifyCall{projectID, container, port})
	return f.verifyErr
}

func (f *fakeSwapCaddy) ServeService(_ context.Context, projectID int64, container string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.serveSvcCalls = append(f.serveSvcCalls, serveSvcCall{projectID, container, port})
	return f.serveSvcErr
}

func (f *fakeSwapCaddy) ServeStaticSite(_ context.Context, projectID int64, projectName string, deploymentNumber int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staticCalls = append(f.staticCalls, staticCall{projectID, projectName, deploymentNumber})
	return f.staticErr
}

type fakeSwapStore struct {
	primaryDomains []string
	primaryErr     error

	lastSuccess    *store.Deployment
	lastSuccessErr error
}

func (f *fakeSwapStore) ListPrimaryDomainsForProject(_ context.Context, _ int64) ([]string, error) {
	return f.primaryDomains, f.primaryErr
}
func (f *fakeSwapStore) GetLastSuccessfulDeployment(_ context.Context, _ int64) (*store.Deployment, error) {
	return f.lastSuccess, f.lastSuccessErr
}

func newSwapFixtures() (store.Project, store.Deployment) {
	return store.Project{ID: 7, Name: "api"},
		store.Deployment{ID: 1001, ProjectID: 7, Number: 3}
}

// commitCaddySwap -------------------------------------------------------

func TestCommitCaddySwap_NoWebIsNoop(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{}
	st := &fakeSwapStore{}
	project, dep := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"nightly": {Type: cobaltfile.TypeCron, Schedule: "@hourly", Command: "x"},
	}}

	if err := commitCaddySwap(context.Background(), cy, st, project, dep, cf); err != nil {
		t.Fatalf("commitCaddySwap: %v", err)
	}
	if cy.setDomainsCalls != 0 || len(cy.verifyCalls) != 0 || len(cy.staticCalls) != 0 {
		t.Errorf("Caddy was touched for a cron-only project: %+v", cy)
	}
}

func TestCommitCaddySwap_ContainerHappyPath(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{}
	st := &fakeSwapStore{primaryDomains: []string{"api.example.com", "alt.example.com"}}
	project, dep := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
	}}

	if err := commitCaddySwap(context.Background(), cy, st, project, dep, cf); err != nil {
		t.Fatalf("commitCaddySwap: %v", err)
	}
	if cy.setDomainsCalls != 1 {
		t.Errorf("SetDomains calls: %d, want 1", cy.setDomainsCalls)
	}
	if got := cy.setDomainsArgs[0]; len(got) != 2 || got[0] != "api.example.com" {
		t.Errorf("SetDomains args: %v", got)
	}
	if len(cy.verifyCalls) != 1 {
		t.Fatalf("VerifyServeService calls: %d, want 1", len(cy.verifyCalls))
	}
	got := cy.verifyCalls[0]
	if got.container != "api-3-web" || got.port != 3000 || got.projectID != 7 {
		t.Errorf("verify args: %+v", got)
	}
	if len(cy.staticCalls) != 0 {
		t.Error("ServeStaticSite called on container deploy")
	}
}

func TestCommitCaddySwap_StaticAndGeneratorRouteToStaticSite(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		typ  cobaltfile.ServiceType
	}{
		{"static", cobaltfile.TypeStatic},
		{"generator", cobaltfile.TypeGenerator},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cy := &fakeSwapCaddy{}
			st := &fakeSwapStore{primaryDomains: []string{"site.example.com"}}
			project, dep := newSwapFixtures()
			cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
				"web": {Type: c.typ, PublicPath: "dist"},
			}}
			if err := commitCaddySwap(context.Background(), cy, st, project, dep, cf); err != nil {
				t.Fatalf("commitCaddySwap: %v", err)
			}
			if len(cy.staticCalls) != 1 {
				t.Fatalf("static calls: %d, want 1", len(cy.staticCalls))
			}
			if got := cy.staticCalls[0]; got.projectName != "api" || got.deploymentNumber != 3 {
				t.Errorf("static args: %+v", got)
			}
			if len(cy.verifyCalls) != 0 {
				t.Error("VerifyServeService called on static deploy")
			}
		})
	}
}

func TestCommitCaddySwap_UnsupportedTypeErrors(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{}
	st := &fakeSwapStore{primaryDomains: []string{"x.example.com"}}
	project, dep := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: "bogus"},
	}}
	err := commitCaddySwap(context.Background(), cy, st, project, dep, cf)
	if err == nil || !strings.Contains(err.Error(), "unsupported web type") {
		t.Errorf("expected unsupported-type error, got %v", err)
	}
}

func TestCommitCaddySwap_ListDomainsErrorPropagates(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{}
	st := &fakeSwapStore{primaryErr: errors.New("rqlite down")}
	project, dep := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeContainer, Port: 3000},
	}}
	err := commitCaddySwap(context.Background(), cy, st, project, dep, cf)
	if err == nil || !strings.Contains(err.Error(), "rqlite down") {
		t.Errorf("expected rqlite error, got %v", err)
	}
	if cy.setDomainsCalls != 0 {
		t.Error("SetDomains called despite ListPrimaryDomains failure")
	}
}

func TestCommitCaddySwap_SetDomainsErrorAbortsBeforeUpstream(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{setDomainsErr: errors.New("caddy 500")}
	st := &fakeSwapStore{primaryDomains: []string{"x.example.com"}}
	project, dep := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeContainer, Port: 3000},
	}}
	err := commitCaddySwap(context.Background(), cy, st, project, dep, cf)
	if err == nil || !strings.Contains(err.Error(), "set domains") {
		t.Errorf("expected set-domains error, got %v", err)
	}
	// Upstream swap must not be attempted if domains failed — would leave
	// project pointing at a new container with no host matcher.
	if len(cy.verifyCalls) != 0 {
		t.Error("VerifyServeService called despite SetDomains failure")
	}
}

func TestCommitCaddySwap_VerifyErrorPropagates(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{verifyErr: errors.New("upstream not reflected")}
	st := &fakeSwapStore{primaryDomains: []string{"x.example.com"}}
	project, dep := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeContainer, Port: 3000},
	}}
	err := commitCaddySwap(context.Background(), cy, st, project, dep, cf)
	if err == nil || !strings.Contains(err.Error(), "upstream not reflected") {
		t.Errorf("expected verify error, got %v", err)
	}
}

func TestCommitCaddySwap_StaticErrorPropagates(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{staticErr: errors.New("static config broken")}
	st := &fakeSwapStore{primaryDomains: []string{"x.example.com"}}
	project, dep := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeStatic, PublicPath: "dist"},
	}}
	err := commitCaddySwap(context.Background(), cy, st, project, dep, cf)
	if err == nil || !strings.Contains(err.Error(), "serve static") {
		t.Errorf("expected static error, got %v", err)
	}
}

// revertCaddySwap -------------------------------------------------------

func captureLog() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})), buf
}

func TestRevertCaddySwap_NoPriorIsLoggedNoop(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{}
	st := &fakeSwapStore{lastSuccessErr: store.ErrNotFound}
	project, _ := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeContainer, Port: 3000},
	}}
	log, buf := captureLog()
	revertCaddySwap(context.Background(), log, cy, st, project, cf)
	if !strings.Contains(buf.String(), "no prior success") {
		t.Errorf("log missing first-deploy revert message: %s", buf.String())
	}
	if len(cy.serveSvcCalls) != 0 || len(cy.staticCalls) != 0 {
		t.Error("Caddy touched despite no prior success")
	}
}

func TestRevertCaddySwap_NoWebIsNoop(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{}
	st := &fakeSwapStore{lastSuccess: &store.Deployment{ID: 1, Number: 1}}
	project, _ := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"nightly": {Type: cobaltfile.TypeCron, Schedule: "@hourly", Command: "x"},
	}}
	log, _ := captureLog()
	revertCaddySwap(context.Background(), log, cy, st, project, cf)
	if len(cy.serveSvcCalls) != 0 || len(cy.staticCalls) != 0 {
		t.Error("Caddy touched on no-web revert")
	}
}

func TestRevertCaddySwap_ContainerRevertsToPriorDeploymentNumber(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{}
	prior := &store.Deployment{ID: 100, ProjectID: 7, Number: 2}
	st := &fakeSwapStore{lastSuccess: prior}
	project, _ := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeContainer, Image: "default", Port: 3000},
	}}
	log, _ := captureLog()
	revertCaddySwap(context.Background(), log, cy, st, project, cf)
	if len(cy.serveSvcCalls) != 1 {
		t.Fatalf("ServeService calls: %d, want 1", len(cy.serveSvcCalls))
	}
	got := cy.serveSvcCalls[0]
	// Must point at the PRIOR deployment's container, not the failed
	// new one — that's the entire point of revert.
	if got.container != "api-2-web" {
		t.Errorf("revert container: %q, want api-2-web", got.container)
	}
	if got.port != 3000 {
		t.Errorf("revert port: %d, want 3000", got.port)
	}
}

func TestRevertCaddySwap_ContainerRevertErrorLoggedNotPropagated(t *testing.T) {
	t.Parallel()
	// Revert is best-effort: the daemon's 8d Caddy reconciler will fix
	// drift later, so a failed revert must NOT panic or escalate — it
	// just logs loud so an operator can investigate.
	cy := &fakeSwapCaddy{serveSvcErr: errors.New("caddy 500")}
	st := &fakeSwapStore{lastSuccess: &store.Deployment{ID: 1, Number: 2}}
	project, _ := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeContainer, Port: 3000},
	}}
	log, buf := captureLog()
	// Must not panic.
	revertCaddySwap(context.Background(), log, cy, st, project, cf)
	if !strings.Contains(buf.String(), "container revert failed") {
		t.Errorf("log missing failure message: %s", buf.String())
	}
}

func TestRevertCaddySwap_StaticRevertsToPriorDeploymentNumber(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		typ  cobaltfile.ServiceType
	}{
		{"static", cobaltfile.TypeStatic},
		{"generator", cobaltfile.TypeGenerator},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cy := &fakeSwapCaddy{}
			st := &fakeSwapStore{lastSuccess: &store.Deployment{ID: 100, ProjectID: 7, Number: 2}}
			project, _ := newSwapFixtures()
			cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
				"web": {Type: c.typ, PublicPath: "dist"},
			}}
			log, _ := captureLog()
			revertCaddySwap(context.Background(), log, cy, st, project, cf)
			if len(cy.staticCalls) != 1 {
				t.Fatalf("static calls: %d, want 1", len(cy.staticCalls))
			}
			if got := cy.staticCalls[0]; got.deploymentNumber != 2 {
				t.Errorf("static revert number: %d, want 2", got.deploymentNumber)
			}
		})
	}
}

func TestRevertCaddySwap_StaticErrorLoggedNotPropagated(t *testing.T) {
	t.Parallel()
	cy := &fakeSwapCaddy{staticErr: errors.New("symlink broken")}
	st := &fakeSwapStore{lastSuccess: &store.Deployment{ID: 1, Number: 2}}
	project, _ := newSwapFixtures()
	cf := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeStatic, PublicPath: "dist"},
	}}
	log, buf := captureLog()
	revertCaddySwap(context.Background(), log, cy, st, project, cf)
	if !strings.Contains(buf.String(), "static revert failed") {
		t.Errorf("log missing failure message: %s", buf.String())
	}
}

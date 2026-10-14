package deploy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

type retirementFixture struct {
	live       store.Deployment
	successors map[int]*store.Deployment
	services   []docker.ServiceInfo
	removed    []string
	served     string
	upstream   string
	status     int
	retireErr  error
	cancel     context.CancelFunc
}

func (f *retirementFixture) GetLastSuccessfulDeployment(context.Context, int64) (*store.Deployment, error) {
	return &f.live, nil
}

func (f *retirementFixture) FirstSuccessfulDeploymentAfter(_ context.Context, _ int64, n, _ int) (*store.Deployment, error) {
	if d := f.successors[n]; d != nil {
		return d, nil
	}
	return nil, store.ErrNotFound
}

func (f *retirementFixture) ListPrimaryDomainsForProject(context.Context, int64) ([]string, error) {
	return []string{"example.com"}, nil
}

func (f *retirementFixture) CurrentUpstream(context.Context, int64) (string, error) {
	return f.upstream, nil
}

func (f *retirementFixture) ServedDeployment(context.Context, string) (string, int, error) {
	if f.cancel != nil {
		f.cancel()
	}
	return f.served, f.status, nil
}

func (f *retirementFixture) ListServicesForProject(context.Context, int64) ([]docker.ServiceInfo, error) {
	return f.services, nil
}

func (f *retirementFixture) RetireService(_ context.Context, name string) error {
	if f.retireErr != nil {
		return f.retireErr
	}
	f.removed = append(f.removed, name)
	for i, s := range f.services {
		if s.Name == name {
			f.services = append(f.services[:i], f.services[i+1:]...)
			break
		}
	}
	return nil
}

func retirementTestFixture(t *testing.T, ago time.Duration) (*retirementFixture, *cobaltfile.Cobaltfile) {
	t.Helper()
	raw := `{"version":"1.0","services":{"web":{"port":3000}}}`
	cf, err := cobaltfile.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	finished := time.Now().Add(-ago).Unix()
	live := store.Deployment{ProjectID: 1, Number: 3, FinishedAt: &finished, ResolvedCobaltfile: &raw}
	return &retirementFixture{live: live, successors: map[int]*store.Deployment{2: &live}, services: []docker.ServiceInfo{{Name: "api-2-web"}, {Name: "api-3-web"}}, upstream: "api-3-web", served: "3", status: 200}, cf
}

func runRetirementWait(ctx context.Context, f *retirementFixture, cf *cobaltfile.Cobaltfile) error {
	return waitForWebCapacity(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), f, f, f, f, store.Project{ID: 1, Name: "api"}, cf, io.Discard, time.Millisecond)
}

func TestWebCapacityRetiresBeforeAdmittingReplacement(t *testing.T) {
	f, cf := retirementTestFixture(t, 6*time.Minute)
	if err := runRetirementWait(context.Background(), f, cf); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.removed, ",") != "api-2-web" || len(f.services) != 1 {
		t.Fatalf("retirement: %+v", f)
	}
}

func TestWebCapacityPreservesGraceAndFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name             string
		ago              time.Duration
		served, upstream string
		status           int
	}{
		{"grace", time.Minute, "3", "api-3-web", 200},
		{"unknown", 6 * time.Minute, "", "api-3-web", 200},
		{"old router", 6 * time.Minute, "2", "api-3-web", 200},
		{"gateway", 6 * time.Minute, "3", "api-3-web", 502},
		{"config drift", 6 * time.Minute, "3", "api-2-web", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, cf := retirementTestFixture(t, tc.ago)
			f.served = tc.served
			f.upstream = tc.upstream
			f.status = tc.status
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			err := runRetirementWait(ctx, f, cf)
			if err == nil || len(f.removed) != 0 || len(f.services) != 2 {
				t.Fatalf("err=%v removed=%v services=%v", err, f.removed, f.services)
			}
		})
	}
}

func TestWebCapacityRetirementFailureBlocksReplacement(t *testing.T) {
	f, cf := retirementTestFixture(t, 6*time.Minute)
	f.retireErr = errors.New("tasks still stopping")
	if err := runRetirementWait(context.Background(), f, cf); err == nil {
		t.Fatal("admitted despite failed retirement")
	}
}

func TestWebCapacityConfigurableLimit(t *testing.T) {
	f, cf := retirementTestFixture(t, time.Minute)
	limit := 3
	cf.WebRetirement = &cobaltfile.WebRetirement{MaxGenerations: &limit}
	if err := runRetirementWait(context.Background(), f, cf); err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 0 {
		t.Fatal("unnecessary retirement")
	}
}

func TestWebCapacityLaterDeployDoesNotResetOldGrace(t *testing.T) {
	f, cf := retirementTestFixture(t, time.Minute)
	earlier := time.Now().Add(-6 * time.Minute).Unix()
	f.successors[1] = &store.Deployment{Number: 2, FinishedAt: &earlier}
	f.services = append(f.services, docker.ServiceInfo{Name: "api-1-web"})
	// Limit 3 permits generation 4 only after generation 1 stops; generation 2
	// must retain the grace that started one minute ago.
	limit := 3
	cf.WebRetirement = &cobaltfile.WebRetirement{MaxGenerations: &limit}
	if err := runRetirementWait(context.Background(), f, cf); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.removed, ",") != "api-1-web" {
		t.Fatalf("removed=%v", f.removed)
	}
}

func TestWebCapacityRetainsHistoricalGraceAcrossConfigurationChange(t *testing.T) {
	f, cf := retirementTestFixture(t, 2*time.Minute)
	grace := 60
	cf.WebRetirement = &cobaltfile.WebRetirement{GracePeriodSeconds: &grace}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := runRetirementWait(ctx, f, cf); err == nil || len(f.removed) != 0 {
		t.Fatalf("err=%v removed=%v", err, f.removed)
	}
}

func TestWebCapacityMissingRetirementTimestampFailsClosed(t *testing.T) {
	f, cf := retirementTestFixture(t, 6*time.Minute)
	f.live.FinishedAt = nil
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := runRetirementWait(ctx, f, cf); err == nil || len(f.removed) != 0 {
		t.Fatalf("err=%v removed=%v", err, f.removed)
	}
}

func TestWebCapacityStableEndpointRevert(t *testing.T) {
	f, cf := retirementTestFixture(t, 6*time.Minute)
	f.services[0].Name = docker.StablePublicWebServiceName(1)
	if err := runRetirementWait(context.Background(), f, cf); err != nil {
		t.Fatal(err)
	}
	if len(f.removed) != 1 || f.removed[0] != docker.StablePublicWebServiceName(1) {
		t.Fatalf("removed=%v", f.removed)
	}
}

func TestWebCapacityNonPublicServicesDoNotGate(t *testing.T) {
	for _, typ := range []cobaltfile.ServiceType{cobaltfile.TypeStatic, cobaltfile.TypeGenerator} {
		f, cf := retirementTestFixture(t, time.Minute)
		cf.Services["web"] = cobaltfile.Service{Type: typ}
		if err := runRetirementWait(context.Background(), f, cf); err != nil {
			t.Fatal(err)
		}
	}
	f, cf := retirementTestFixture(t, time.Minute)
	cf.Services["web"] = cobaltfile.Service{Type: cobaltfile.TypeContainer, ExposedInternally: true}
	if err := runRetirementWait(context.Background(), f, cf); err != nil {
		t.Fatal(err)
	}
}

package store

// coverage_test.go bundles tests for the parts of the store package
// that were uncovered before — apikeys, command_runs, pending_apps,
// upgrades, and the long tail of deployments/domains/env_vars helpers.
// Tests are grouped by file to keep the rqlited startup cost amortized
// across many assertions.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// helpers --------------------------------------------------------------

func mustCreateProject(t *testing.T, db *DB, name string) int64 {
	t.Helper()
	id, err := db.CreateProject(context.Background(), Project{
		Name: name, GithubRepo: "h/" + name, Branch: "main",
	})
	if err != nil {
		t.Fatalf("CreateProject(%s): %v", name, err)
	}
	return id
}

func mustCreateDeployment(t *testing.T, db *DB, projectID int64, number int, status cobaltapi.State) int64 {
	t.Helper()
	id, err := db.CreateDeployment(context.Background(), Deployment{
		ProjectID: projectID, Number: number, Status: cobaltapi.StateQueued,
	})
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if status != cobaltapi.StateQueued {
		if err := db.SetDeploymentStatus(context.Background(), id, status); err != nil {
			t.Fatalf("SetDeploymentStatus: %v", err)
		}
	}
	return id
}

// apikeys.go -----------------------------------------------------------

func TestAPIKeys_CRUD(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	id1, err := db.CreateAPIKey(ctx, "hash1", "ci-server")
	if err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	id2, err := db.CreateAPIKey(ctx, "hash2", "laptop")
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	if id1 == id2 || id1 == 0 {
		t.Errorf("ids: %d %d", id1, id2)
	}

	keys, err := db.ListAPIKeys(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("len: %d, want 2", len(keys))
	}

	got, err := db.GetAPIKeyByID(ctx, id1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "ci-server" {
		t.Errorf("name: %q", got.Name)
	}

	if err := db.DeleteAPIKey(ctx, id1); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := db.GetAPIKeyByID(ctx, id1); !errors.Is(err, ErrNotFound) {
		t.Errorf("post-delete Get: %v", err)
	}
	if err := db.DeleteAPIKey(ctx, id1); !errors.Is(err, ErrNotFound) {
		t.Errorf("double-delete: %v, want ErrNotFound", err)
	}
}

// command_runs.go ------------------------------------------------------

func TestCommandRuns_LifecycleAndListOrder(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	pid := mustCreateProject(t, db, "api")
	apiKeyID, _ := db.CreateAPIKey(ctx, "h", "test")

	id1, err := db.CreateCommandRun(ctx, pid, apiKeyID, "web", "ls -la", false)
	if err != nil {
		t.Fatalf("Create 1: %v", err)
	}
	// Ensure created_at differs across rows so ORDER BY is deterministic.
	time.Sleep(1100 * time.Millisecond)
	id2, err := db.CreateCommandRun(ctx, pid, apiKeyID, "worker", "rake -T", true)
	if err != nil {
		t.Fatalf("Create 2: %v", err)
	}

	if err := db.FinishCommandRun(ctx, id1, 0); err != nil {
		t.Fatalf("Finish 1: %v", err)
	}
	if err := db.FinishCommandRun(ctx, id2, 137); err != nil {
		t.Fatalf("Finish 2: %v", err)
	}

	runs, err := db.ListCommandRunsForProject(ctx, pid, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("len: %d, want 2", len(runs))
	}
	// Newest-first: id2 created later than id1.
	if runs[0].ID != id2 {
		t.Errorf("ordering: first is %d, want %d", runs[0].ID, id2)
	}
	if runs[0].Status != CommandRunStatusFinished || runs[0].ExitCode != 137 {
		t.Errorf("finished row: %+v", runs[0])
	}
	if !runs[0].TTY {
		t.Error("TTY flag not preserved")
	}

	// Limit cap respected.
	short, _ := db.ListCommandRunsForProject(ctx, pid, 1)
	if len(short) != 1 {
		t.Errorf("limit=1: %d rows", len(short))
	}

	// Idempotent FinishCommandRun.
	if err := db.FinishCommandRun(ctx, id1, 1); err != nil {
		t.Errorf("second finish: %v", err)
	}
}

// pending_apps.go ------------------------------------------------------

func TestPendingApps_CRUDAndExpiry(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	now := time.Now().Unix()

	id, state, err := db.CreatePendingApp(ctx, "acme", now+3600)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if state == "" {
		t.Error("state empty")
	}

	got, err := db.GetPendingApp(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Organization != "acme" || got.State != state {
		t.Errorf("got %+v", got)
	}

	// Add a second, already-expired row to exercise sweep.
	expiredID, _, _ := db.CreatePendingApp(ctx, "stale-co", now-3600)
	deleted, err := db.DeleteExpiredPendingApps(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted: %d, want 1", deleted)
	}
	if _, err := db.GetPendingApp(ctx, expiredID); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired row still readable: %v", err)
	}
	// Non-expired row survives.
	if _, err := db.GetPendingApp(ctx, id); err != nil {
		t.Errorf("live row lost: %v", err)
	}

	if err := db.DeletePendingApp(ctx, id); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := db.DeletePendingApp(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("double-delete: %v", err)
	}
}

// upgrades.go ----------------------------------------------------------

func TestUpgrades_Lifecycle(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	u := Upgrade{
		ID:            "up-1",
		TargetImage:   "ghcr.io/x/cobalt:1.2.3",
		TargetVersion: "1.2.3",
		FromVersion:   "1.2.2",
		LogPath:       "/var/log/cobalt/upgrade.log",
	}
	if err := db.CreateUpgrade(ctx, u); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := db.GetUpgrade(ctx, u.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != UpgradeStatusRunning {
		t.Errorf("default status: %q", got.Status)
	}
	if got.IsTerminal() {
		t.Error("running upgrade reported terminal")
	}

	// LatestRunning picks this row up.
	latest, err := db.LatestRunningUpgrade(ctx)
	if err != nil {
		t.Fatalf("LatestRunning: %v", err)
	}
	if latest.ID != u.ID {
		t.Errorf("latest ID: %q", latest.ID)
	}

	if err := db.SetUpgradeStatus(ctx, u.ID, UpgradeStatusSucceeded, ""); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	got, _ = db.GetUpgrade(ctx, u.ID)
	if !got.IsTerminal() || got.EndedAt == nil {
		t.Errorf("post-success: %+v", got)
	}

	// No more running upgrades ⇒ LatestRunning returns ErrNotFound.
	if _, err := db.LatestRunningUpgrade(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("LatestRunning after success: %v", err)
	}

	// SetStatus on unknown id ⇒ ErrNotFound.
	if err := db.SetUpgradeStatus(ctx, "missing", UpgradeStatusFailed, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetStatus(missing): %v", err)
	}
}

func TestUpgrades_ValidateRequiredFields(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	cases := []Upgrade{
		{TargetImage: "x", LogPath: "/p"}, // no ID
		{ID: "x", LogPath: "/p"},          // no TargetImage
		{ID: "x", TargetImage: "x"},       // no LogPath
	}
	for _, u := range cases {
		if err := db.CreateUpgrade(ctx, u); err == nil {
			t.Errorf("expected error for %+v", u)
		}
	}
}

func TestUpgrades_SweepStaleMarksOldRunningAsFailed(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	// Backdate StartedAt by passing it explicitly.
	stale := Upgrade{
		ID: "old", TargetImage: "img", LogPath: "/log",
		StartedAt: time.Now().Add(-2 * time.Hour).Unix(),
	}
	if err := db.CreateUpgrade(ctx, stale); err != nil {
		t.Fatalf("Create stale: %v", err)
	}
	fresh := Upgrade{
		ID: "new", TargetImage: "img", LogPath: "/log",
		// default StartedAt = now
	}
	if err := db.CreateUpgrade(ctx, fresh); err != nil {
		t.Fatalf("Create fresh: %v", err)
	}

	n, err := db.SweepStaleUpgrades(ctx, time.Hour)
	if err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if n != 1 {
		t.Errorf("swept: %d, want 1", n)
	}
	got, _ := db.GetUpgrade(ctx, "old")
	if got.Status != UpgradeStatusFailed {
		t.Errorf("stale: %q, want failed", got.Status)
	}
	got, _ = db.GetUpgrade(ctx, "new")
	if got.Status != UpgradeStatusRunning {
		t.Errorf("fresh swept: %q", got.Status)
	}
}

func TestUpgrade_IsTerminal_AllStatuses(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		UpgradeStatusRunning:    false,
		UpgradeStatusSucceeded:  true,
		UpgradeStatusFailed:     true,
		UpgradeStatusRolledBack: true,
		"unknown":               false,
	}
	for status, want := range cases {
		got := Upgrade{Status: status}.IsTerminal()
		if got != want {
			t.Errorf("%q: got %v, want %v", status, got, want)
		}
	}
}

// deployments.go (uncovered helpers) -----------------------------------

func TestDeployments_NextDeploymentNumber(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	pid := mustCreateProject(t, db, "api")

	n, err := db.NextDeploymentNumber(ctx, pid)
	if err != nil {
		t.Fatalf("Next on empty: %v", err)
	}
	if n != 1 {
		t.Errorf("empty: got %d, want 1", n)
	}
	mustCreateDeployment(t, db, pid, 1, cobaltapi.StateSuccess)
	mustCreateDeployment(t, db, pid, 2, cobaltapi.StateFailed)
	n, _ = db.NextDeploymentNumber(ctx, pid)
	if n != 3 {
		t.Errorf("after two deploys: got %d, want 3", n)
	}
}

func TestDeployments_ListAndQueriesForProject(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	pid := mustCreateProject(t, db, "api")

	// Mix: 3 successful, 1 failed, 1 queued, 1 building.
	mustCreateDeployment(t, db, pid, 1, cobaltapi.StateSuccess)
	mustCreateDeployment(t, db, pid, 2, cobaltapi.StateFailed)
	mustCreateDeployment(t, db, pid, 3, cobaltapi.StateSuccess)
	mustCreateDeployment(t, db, pid, 4, cobaltapi.StateQueued)
	buildingID := mustCreateDeployment(t, db, pid, 5, cobaltapi.StateBuilding)
	mustCreateDeployment(t, db, pid, 6, cobaltapi.StateSuccess)

	// ListDeploymentsForProject default ordering: newest first.
	all, err := db.ListDeploymentsForProject(ctx, pid, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 6 {
		t.Fatalf("len: %d", len(all))
	}
	if all[0].Number != 6 {
		t.Errorf("first: %d, want 6", all[0].Number)
	}

	// limit > 0 path.
	page, _ := db.ListDeploymentsForProject(ctx, pid, 2)
	if len(page) != 2 {
		t.Errorf("limit=2: %d", len(page))
	}

	// ActiveDeploymentForProject returns the building row.
	active, err := db.ActiveDeploymentForProject(ctx, pid)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active.ID != buildingID {
		t.Errorf("active id: %d, want %d", active.ID, buildingID)
	}

	// QueuedDeployments includes the queued row.
	queued, err := db.QueuedDeployments(ctx)
	if err != nil {
		t.Fatalf("Queued: %v", err)
	}
	if len(queued) != 1 || queued[0].Number != 4 {
		t.Errorf("queued: %+v", queued)
	}

	// ActiveDeployments (all projects).
	allActive, err := db.ActiveDeployments(ctx)
	if err != nil {
		t.Fatalf("ActiveDeployments: %v", err)
	}
	if len(allActive) != 1 {
		t.Errorf("global active: %d, want 1", len(allActive))
	}

	// GetDeploymentByNumber.
	byNum, err := db.GetDeploymentByNumber(ctx, pid, 6)
	if err != nil {
		t.Fatalf("ByNumber: %v", err)
	}
	if byNum.Number != 6 {
		t.Errorf("byNumber: %+v", byNum)
	}
	if _, err := db.GetDeploymentByNumber(ctx, pid, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("byNumber missing: %v", err)
	}

	// PreviousSuccessfulDeployment excludes the latest.
	latest, _ := db.GetLastSuccessfulDeployment(ctx, pid)
	prev, err := db.PreviousSuccessfulDeployment(ctx, pid, latest.ID)
	if err != nil {
		t.Fatalf("Previous: %v", err)
	}
	if prev.Number != 3 {
		t.Errorf("previous: %d, want 3", prev.Number)
	}

	// RecentSuccessfulDeploymentNumbers — newest-first, limit-capped.
	nums, err := db.RecentSuccessfulDeploymentNumbers(ctx, pid, 2)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(nums) != 2 || nums[0] != 6 || nums[1] != 3 {
		t.Errorf("recent: %v", nums)
	}
	// limit <= 0 returns nil.
	if nums, _ := db.RecentSuccessfulDeploymentNumbers(ctx, pid, 0); nums != nil {
		t.Errorf("limit=0 returned %v", nums)
	}
}

func TestDeployments_SetAndReadResolvedCobaltfile(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	pid := mustCreateProject(t, db, "api")

	depID := mustCreateDeployment(t, db, pid, 1, cobaltapi.StateSuccess)
	raw := `{"version":"1.0","services":{"web":{"type":"container","port":3000}}}`
	if err := db.SetResolvedCobaltfile(ctx, depID, raw); err != nil {
		t.Fatalf("SetResolved: %v", err)
	}
	dep, cf, err := db.LastSuccessfulCobaltfile(ctx, pid)
	if err != nil {
		t.Fatalf("LastSuccessfulCobaltfile: %v", err)
	}
	if dep.ID != depID || cf == nil {
		t.Errorf("got dep=%+v cf=%v", dep, cf)
	}
	if _, ok := cf.Services["web"]; !ok {
		t.Errorf("web service missing from parsed cobaltfile")
	}
}

func TestDeployments_LastSuccessfulCobaltfile_ParseError(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	pid := mustCreateProject(t, db, "api")

	depID := mustCreateDeployment(t, db, pid, 1, cobaltapi.StateSuccess)
	_ = db.SetResolvedCobaltfile(ctx, depID, "not json")
	_, cf, err := db.LastSuccessfulCobaltfile(ctx, pid)
	if err == nil {
		t.Error("expected parse error")
	}
	if cf != nil {
		t.Error("cf should be nil on parse failure")
	}
}

// domains.go (uncovered helpers) ---------------------------------------

func TestDomain_IsRedirect(t *testing.T) {
	t.Parallel()
	empty := ""
	target := "primary.example.com"
	cases := []struct {
		d    Domain
		want bool
	}{
		{Domain{}, false},
		{Domain{RedirectTo: &empty}, false},
		{Domain{RedirectTo: &target}, true},
	}
	for _, c := range cases {
		if got := c.d.IsRedirect(); got != c.want {
			t.Errorf("IsRedirect(%+v): got %v, want %v", c.d, got, c.want)
		}
	}
}

func TestDomains_PrimaryAndRedirectsAndCascade(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	pid := mustCreateProject(t, db, "api")

	if err := db.AddDomain(ctx, pid, "api.example.com"); err != nil {
		t.Fatalf("AddDomain primary: %v", err)
	}
	if err := db.AddDomainRedirect(ctx, pid, "www.api.example.com", "api.example.com"); err != nil {
		t.Fatalf("AddDomainRedirect: %v", err)
	}

	primaries, err := db.ListPrimaryDomainsForProject(ctx, pid)
	if err != nil {
		t.Fatalf("ListPrimary: %v", err)
	}
	if len(primaries) != 1 || primaries[0] != "api.example.com" {
		t.Errorf("primaries: %v", primaries)
	}
	full, err := db.ListDomainsFullForProject(ctx, pid)
	if err != nil {
		t.Fatalf("Full: %v", err)
	}
	if len(full) != 2 {
		t.Fatalf("full: %d, want 2", len(full))
	}
	// Redirect row carries RedirectTo set.
	var sawRedirect bool
	for _, d := range full {
		if d.Name == "www.api.example.com" {
			sawRedirect = true
			if !d.IsRedirect() || *d.RedirectTo != "api.example.com" {
				t.Errorf("redirect row: %+v", d)
			}
		}
	}
	if !sawRedirect {
		t.Error("redirect row missing from Full")
	}

	// Cascade: removing the primary takes its redirects with it and
	// returns their ids for Caddy cleanup.
	ids, err := db.RemoveDomainAndRedirects(ctx, pid, "api.example.com")
	if err != nil {
		t.Fatalf("Cascade: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("cascade ids: %v (expected one redirect)", ids)
	}
	left, _ := db.ListDomainsFullForProject(ctx, pid)
	if len(left) != 0 {
		t.Errorf("expected no domains left, got %d", len(left))
	}
	if _, err := db.RemoveDomainAndRedirects(ctx, pid, "missing.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("cascade-missing: %v", err)
	}
}

// env_vars.go (uncovered helpers) --------------------------------------

func TestEnvVars_SetGetListMapDelete(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	pid := mustCreateProject(t, db, "api")

	if err := db.SetEnvVar(ctx, pid, "FOO", "bar"); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}
	got, err := db.GetEnvVar(ctx, pid, "FOO")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Value != "bar" {
		t.Errorf("value: %q", got.Value)
	}

	if err := db.SetEnvVars(ctx, pid, map[string]string{
		"A": "1", "B": "2",
	}); err != nil {
		t.Fatalf("SetEnvVars: %v", err)
	}
	list, err := db.ListEnvVars(ctx, pid)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("list len: %d, want 3", len(list))
	}
	m, err := db.EnvVarMap(ctx, pid)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if m["A"] != "1" || m["B"] != "2" || m["FOO"] != "bar" {
		t.Errorf("map: %+v", m)
	}

	if err := db.DeleteEnvVar(ctx, pid, "A"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := db.GetEnvVar(ctx, pid, "A"); !errors.Is(err, ErrNotFound) {
		t.Errorf("post-delete Get: %v", err)
	}
	if err := db.DeleteEnvVar(ctx, pid, "A"); !errors.Is(err, ErrNotFound) {
		t.Errorf("double-delete: %v", err)
	}
}

func TestEnvVars_SetVarsEmptyIsNoop(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	pid := mustCreateProject(t, db, "api")
	if err := db.SetEnvVars(context.Background(), pid, nil); err != nil {
		t.Errorf("nil map: %v", err)
	}
}

func TestEnvVars_SetVarsRejectsInvalidKey(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	pid := mustCreateProject(t, db, "api")
	err := db.SetEnvVars(context.Background(), pid, map[string]string{
		"BAD-KEY": "x", // hyphens not allowed in env keys
	})
	if err == nil {
		t.Error("expected validation error")
	}
}

// github_apps.go (installation flow) ----------------------------------

func TestGithubAppInstallations_CRUD(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	db.SetCipher(newTestCipher(t))
	ctx := context.Background()

	appRowID, err := db.CreateGithubApp(ctx, GithubApp{
		AppID: 42, Owner: "acme", PrivateKey: samplePEM, WebhookSecret: sampleWebhookSecret,
	})
	if err != nil {
		t.Fatalf("CreateGithubApp: %v", err)
	}

	instRowID, err := db.CreateGithubAppInstallation(ctx, GithubAppInstallation{
		AppID: appRowID, InstallationID: 999, AccountLogin: "acme",
	})
	if err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}

	// Lookup by row id.
	got, err := db.GetGithubAppInstallation(ctx, instRowID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if got.InstallationID != 999 || got.AccountLogin != "acme" {
		t.Errorf("got %+v", got)
	}
	if got.AccessToken.Valid {
		t.Error("AccessToken should default to NULL")
	}

	// Lookup by GitHub installation_id.
	byInst, err := db.GetGithubAppInstallationByInstallationID(ctx, 999)
	if err != nil {
		t.Fatalf("GetByInstallationID: %v", err)
	}
	if byInst.ID != instRowID {
		t.Errorf("ID drift: got %d, want %d", byInst.ID, instRowID)
	}

	// SetInstallationToken populates access_token + expiry.
	expiry := time.Now().Add(time.Hour).Unix()
	if err := db.SetInstallationToken(ctx, instRowID, "ghs_xxx", expiry); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	got, _ = db.GetGithubAppInstallation(ctx, instRowID)
	if !got.AccessToken.Valid || got.AccessToken.String != "ghs_xxx" {
		t.Errorf("token: %+v", got.AccessToken)
	}
	if !got.AccessTokenExpiresAt.Valid || got.AccessTokenExpiresAt.Int64 != expiry {
		t.Errorf("expiry: %+v", got.AccessTokenExpiresAt)
	}

	// SetInstallationToken on missing row ⇒ ErrNotFound.
	if err := db.SetInstallationToken(ctx, 99999, "x", expiry); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetToken missing: %v", err)
	}

	// ListApps + ListInstallations.
	apps, err := db.ListGithubApps(ctx)
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if len(apps) != 1 || apps[0].ID != appRowID {
		t.Errorf("apps: %+v", apps)
	}
	insts, err := db.ListGithubAppInstallations(ctx, appRowID)
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	if len(insts) != 1 || insts[0].ID != instRowID {
		t.Errorf("installations: %+v", insts)
	}

	// Delete installation ⇒ subsequent reads return ErrNotFound.
	if err := db.DeleteGithubAppInstallation(ctx, instRowID); err != nil {
		t.Fatalf("DeleteInstallation: %v", err)
	}
	if _, err := db.GetGithubAppInstallation(ctx, instRowID); !errors.Is(err, ErrNotFound) {
		t.Errorf("post-delete get: %v", err)
	}
	if err := db.DeleteGithubAppInstallation(ctx, instRowID); !errors.Is(err, ErrNotFound) {
		t.Errorf("double-delete: %v", err)
	}

	// Delete the app row itself.
	if err := db.DeleteGithubApp(ctx, appRowID); err != nil {
		t.Fatalf("DeleteApp: %v", err)
	}
	if err := db.DeleteGithubApp(ctx, appRowID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteApp twice: %v", err)
	}
}

// github_repos.go ------------------------------------------------------

func TestGithubAppRepos_CRUDAndUpsert(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	db.SetCipher(newTestCipher(t))
	ctx := context.Background()

	appRowID, _ := db.CreateGithubApp(ctx, GithubApp{
		AppID: 1, Owner: "acme", PrivateKey: samplePEM, WebhookSecret: sampleWebhookSecret,
	})
	instRowID, _ := db.CreateGithubAppInstallation(ctx, GithubAppInstallation{
		AppID: appRowID, InstallationID: 100, AccountLogin: "acme",
	})

	rowID, err := db.AddGithubAppRepo(ctx, GithubAppRepo{
		InstallationID: instRowID, RepoID: 555, FullName: "acme/api",
		Private: true, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	if rowID == 0 {
		t.Error("rowID 0")
	}

	got, err := db.GetGithubRepoByFullName(ctx, "acme/api")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.RepoID != 555 || got.DefaultBranch != "main" || !got.Private {
		t.Errorf("got %+v", got)
	}

	// Upsert: same repo_id, new branch.
	if _, err := db.AddGithubAppRepo(ctx, GithubAppRepo{
		InstallationID: instRowID, RepoID: 555, FullName: "acme/api",
		Private: false, DefaultBranch: "trunk",
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, _ = db.GetGithubRepoByFullName(ctx, "acme/api")
	if got.DefaultBranch != "trunk" || got.Private {
		t.Errorf("upsert did not apply: %+v", got)
	}

	// ListByFullName + ListForInstallation.
	byName, err := db.ListGithubReposByFullName(ctx, "acme/api")
	if err != nil {
		t.Fatalf("ListByFullName: %v", err)
	}
	if len(byName) != 1 {
		t.Errorf("ListByFullName: %d", len(byName))
	}
	byInst, err := db.ListGithubReposForInstallation(ctx, instRowID)
	if err != nil {
		t.Fatalf("ListForInstallation: %v", err)
	}
	if len(byInst) != 1 {
		t.Errorf("ListForInstallation: %d", len(byInst))
	}

	// Remove.
	if err := db.RemoveGithubAppRepo(ctx, 555); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := db.GetGithubRepoByFullName(ctx, "acme/api"); !errors.Is(err, ErrNotFound) {
		t.Errorf("post-remove: %v", err)
	}
	if err := db.RemoveGithubAppRepo(ctx, 555); !errors.Is(err, ErrNotFound) {
		t.Errorf("double-remove: %v", err)
	}
}

// projects.go (uncovered helpers) --------------------------------------

func TestGetProjectByID(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()
	id := mustCreateProject(t, db, "api")

	p, err := db.GetProjectByID(ctx, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if p.Name != "api" {
		t.Errorf("name: %q", p.Name)
	}
	if _, err := db.GetProjectByID(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
}

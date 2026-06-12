package cobaltfile

import (
	"testing"
)

func TestValidate_VersionRequired(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		src  string
	}{
		{"missing", `{"services":{"web":{}}}`},
		{"empty", `{"version":"","services":{"web":{}}}`},
		{"unsupported", `{"version":"2.0","services":{"web":{}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.src))
			mustContain(t, err, "version")
		})
	}
}

func TestValidate_InvalidServiceType(t *testing.T) {
	t.Parallel()
	src := `{"version":"1.0","services":{"web":{"type":"daemon"}}}`
	_, err := Parse([]byte(src))
	mustContain(t, err, "invalid type")
}

func TestValidate_HookMustBeCommand(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {
            "web": {},
            "hook:deploy:start:before": {"type": "container"}
        }
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "must be type=command")
}

func TestValidate_HookNeedsCommand(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {
            "hook:deploy:start:after": {"type": "command"}
        }
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "requires a command")
}

func TestValidate_PortRange(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		port int
	}{
		{"negative", -1},
		{"too-high", 70000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cf := Default()
			s := cf.Services["web"]
			s.Port = tc.port
			cf.Services["web"] = s
			if err := cf.Validate(); err == nil {
				t.Error("want error for invalid port")
			}
		})
	}
}

func TestValidate_MinReplicasNegative(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {"minReplicas": -1}}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "minReplicas")
}

func TestValidate_MinReplicasContainerOnly(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"worker": {
            "type": "cron",
            "schedule": "* * * * *",
            "command": "echo hi",
            "minReplicas": 2
        }}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "minReplicas only valid for type=container")
}

func TestValidate_MinReplicasAccepted(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {"minReplicas": 4}}
    }`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cf.Services["web"].MinReplicas; got != 4 {
		t.Errorf("MinReplicas = %d, want 4", got)
	}
}

func TestValidate_PublishedPortRange(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {
            "publishedPorts": [
                {"publishedAs": 0, "fromContainerPort": 8000}
            ]
        }}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "publishedAs")
}

func TestValidate_PublishedPortProtocol(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {
            "publishedPorts": [
                {"publishedAs": 8080, "fromContainerPort": 80, "protocol": "icmp"}
            ]
        }}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "protocol")
}

func TestValidate_VolumePathMustBeAbsolute(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {
            "volumes": [{"name": "data", "destinationPath": "var/data"}]
        }}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "must be absolute")
}

func TestValidate_VolumeNameRequired(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {
            "volumes": [{"name": " ", "destinationPath": "/data"}]
        }}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "name is required")
}

func TestValidate_HealthRequiresCommand(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {"health": {"command": ""}}}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "health.command")
}

func TestValidate_CronSchedule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		schedule string
		ok       bool
	}{
		{"every minute", "* * * * *", true},
		{"every 5 min", "*/5 * * * *", true},
		{"specific time", "30 4 * * *", true},
		{"range", "0 9-17 * * 1-5", true},
		{"list", "0,15,30,45 * * * *", true},
		{"step in range", "10-50/5 * * * *", true},
		{"too few fields", "* * *", false},
		{"too many fields", "* * * * * *", false},
		{"garbage", "not a cron", false},
		{"trailing comma", "5, * * * *", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCronSchedule(tc.schedule)
			if tc.ok && err != nil {
				t.Errorf("schedule %q: expected ok, got %v", tc.schedule, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("schedule %q: expected error, got nil", tc.schedule)
			}
		})
	}
}

func TestValidate_CronServiceNeedsCommand(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"nightly": {"type": "cron", "schedule": "0 0 * * *"}}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "requires a command")
}

// TestValidate_PrebuiltImageReference proves we accept a service whose
// image string is NOT a key in `cf.Images` — it's a pre-built docker
// registry reference (e.g. `postgres:14-alpine`, `redis/redis-stack:7.4.0-v8`).
// Matches disco's resolution rule in utils/docker.py:1218-1228: "in
// disco_file.images? build it. else? pass through to docker pull." A bad
// ref surfaces at deploy time, not at parse time.
func TestValidate_PrebuiltImageReference(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {"image": "postgres:14-alpine"}}
    }`
	if _, err := Parse([]byte(src)); err != nil {
		t.Errorf("pre-built image reference should be accepted: %v", err)
	}
}

// TestValidate_PrebuiltImageReference_WithRegistryPath covers the
// org/repo:tag shape (full registry path with a slash) — distinct from
// the library-image shape (`postgres:14-alpine`). We treat both
// identically.
func TestValidate_PrebuiltImageReference_WithRegistryPath(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"redis": {"image": "redis/redis-stack:7.4.0-v8"}}
    }`
	if _, err := Parse([]byte(src)); err != nil {
		t.Errorf("registry/org image reference should be accepted: %v", err)
	}
}

// TestValidate_EmptyImageFilledByDefaults documents the
// belt-and-braces: an explicit `image: ""` goes through applyDefaults,
// gets backfilled to `DefaultImageName`, and validation passes. We don't
// surface an error for this because it's unreachable in the Parse flow
// (defaults always run first) and matching disco's parse-time
// permissiveness is the point of this whole change.
func TestValidate_EmptyImageFilledByDefaults(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {"image": ""}}
    }`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("empty image should be backfilled, not error: %v", err)
	}
	if got := cf.Services["web"].Image; got != DefaultImageName {
		t.Errorf("web.Image = %q, want backfilled %q", got, DefaultImageName)
	}
}

func TestValidate_StaticServiceCanReferenceMissingImage(t *testing.T) {
	t.Parallel()
	// A static-only service doesn't need an image; the unknown reference
	// should NOT be flagged.
	src := `{
        "version": "1.0",
        "services": {"web": {"type": "static", "image": "would-be-ghost", "publicPath": "out"}}
    }`
	if _, err := Parse([]byte(src)); err != nil {
		t.Errorf("static-only service should not need image: %v", err)
	}
}

func TestValidate_BuildOverrideSkipsImageCheck(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {"image": "ghost", "build": "make build"}}
    }`
	if _, err := Parse([]byte(src)); err != nil {
		t.Errorf("build: override should skip image check: %v", err)
	}
}

func TestValidate_StopFirstContainerOnly(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"worker": {
            "type": "cron",
            "schedule": "* * * * *",
            "command": "echo hi",
            "stopFirst": true
        }}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "stopFirst only valid for type=container")
}

func TestValidate_StopFirstRejectedOnPublicWeb(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {"stopFirst": true}}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "stopFirst would break zero-downtime cutover")
}

func TestValidate_StopFirstAllowedOnInternalServices(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {
            "web": {"exposedInternally": true, "stopFirst": true},
            "smtp": {"type": "container", "stopFirst": true}
        }
    }`
	if _, err := Parse([]byte(src)); err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

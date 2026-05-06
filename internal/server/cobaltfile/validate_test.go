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

func TestValidate_UnknownImageReference(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {"image": "ghost"}}
    }`
	_, err := Parse([]byte(src))
	mustContain(t, err, "unknown image")
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

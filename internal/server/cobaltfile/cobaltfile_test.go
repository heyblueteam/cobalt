package cobaltfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_Empty(t *testing.T) {
	t.Parallel()
	cf, err := Parse(nil)
	if err != nil {
		t.Fatalf("Parse(nil): %v", err)
	}
	want := Default()
	if cf.Version != want.Version {
		t.Errorf("Version: got %q, want %q", cf.Version, want.Version)
	}
	if _, ok := cf.Services["web"]; !ok {
		t.Error("default cobaltfile must have a web service")
	}
	if _, ok := cf.Images[DefaultImageName]; !ok {
		t.Error("default cobaltfile must have a default image")
	}
}

func TestParse_BlueAPIShape(t *testing.T) {
	t.Parallel()
	// Mirrors api/cobalt.json shape today (per docs/disco.md).
	src := `{
        "version": "1.0",
        "services": {
            "web": {
                "port": 4000,
                "extraSwarmParams": "--host host.docker.internal:host-gateway"
            }
        }
    }`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	web := cf.Services["web"]
	if web.Port != 4000 {
		t.Errorf("port: got %d, want 4000", web.Port)
	}
	if web.Type != TypeContainer {
		t.Errorf("type: got %q, want container", web.Type)
	}
	if web.Image != DefaultImageName {
		t.Errorf("image: got %q, want default", web.Image)
	}
	if web.ExtraSwarmParams != "--host host.docker.internal:host-gateway" {
		t.Errorf("extraSwarmParams: got %q", web.ExtraSwarmParams)
	}
	if _, ok := cf.Images[DefaultImageName]; !ok {
		t.Error("default image should auto-inject for container services")
	}
}

func TestParse_AppliesAllDefaults(t *testing.T) {
	t.Parallel()
	cf, err := Parse([]byte(`{"version":"1.0","services":{"web":{}}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	web := cf.Services["web"]
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"type", string(web.Type), string(DefaultServiceType)},
		{"image", web.Image, DefaultImageName},
		{"port", web.Port, DefaultPort},
		{"publicPath", web.PublicPath, DefaultPublicPath},
		{"schedule", web.Schedule, DefaultSchedule},
		{"timeout", web.Timeout, DefaultTimeout},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParse_PortProtocolDefaults(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {
            "web": {
                "publishedPorts": [
                    {"publishedAs": 8080, "fromContainerPort": 8000}
                ]
            }
        }
    }`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cf.Services["web"].PublishedPorts[0].Protocol != DefaultProtocol {
		t.Errorf("default protocol not applied")
	}
}

func TestParse_ImageOverrides(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {"image": "alt"}},
        "images": {
            "alt": {"dockerfile": "Dockerfile.prod", "context": "src"}
        }
    }`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cf.Images["alt"].Dockerfile != "Dockerfile.prod" {
		t.Errorf("Dockerfile: got %q", cf.Images["alt"].Dockerfile)
	}
	// Default image should NOT be auto-added since web doesn't reference it.
	if _, ok := cf.Images[DefaultImageName]; ok {
		t.Error("default image should not be auto-added when no service uses it")
	}
}

func TestParse_StaticOnlyDoesNotInjectDefaultImage(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {
            "web": {"type": "static", "publicPath": "out"}
        }
    }`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := cf.Images[DefaultImageName]; ok {
		t.Error("static-only web service should not trigger default image auto-inject")
	}
}

func TestParse_BuildOverrideSkipsDefaultImage(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {"web": {"build": "./scripts/build.sh"}}
    }`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if _, ok := cf.Images[DefaultImageName]; ok {
		t.Error("service with build: should not trigger default image auto-inject")
	}
}

func TestParse_HookCommand(t *testing.T) {
	t.Parallel()
	src := `{
        "version": "1.0",
        "services": {
            "web": {},
            "hook:deploy:start:before": {"type": "command", "command": "npx prisma migrate deploy"}
        }
    }`
	cf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	hook := cf.Services["hook:deploy:start:before"]
	if hook.Command != "npx prisma migrate deploy" {
		t.Errorf("hook command: got %q", hook.Command)
	}
	if hook.Type != TypeCommand {
		t.Errorf("hook type: got %q, want command", hook.Type)
	}
}

func TestParse_RejectsUnknownFields(t *testing.T) {
	t.Parallel()
	src := `{"version":"1.0","sevices":{}}` // typo: sevices
	if _, err := Parse([]byte(src)); err == nil {
		t.Error("expected error for unknown field, got nil")
	}
}

func TestParse_RejectsInvalidJSON(t *testing.T) {
	t.Parallel()
	if _, err := Parse([]byte(`{bad`)); err == nil {
		t.Error("expected JSON parse error")
	}
}

func TestParseFile_MissingReturnsDefault(t *testing.T) {
	t.Parallel()
	cf, err := ParseFile(filepath.Join(t.TempDir(), "no-such.json"))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if cf.Version != SupportedVersion {
		t.Errorf("Version: got %q", cf.Version)
	}
}

func TestParseFile_ReadsAndParses(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "cobalt.json")
	src := `{"version":"1.0","name":"myapp","services":{"web":{"port":3000}}}`
	if err := writeFile(path, src); err != nil {
		t.Fatal(err)
	}
	cf, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if cf.Name != "myapp" {
		t.Errorf("Name: got %q, want myapp", cf.Name)
	}
	if cf.Services["web"].Port != 3000 {
		t.Errorf("port: got %d, want 3000", cf.Services["web"].Port)
	}
}

// helpers

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

func mustContain(t *testing.T, err error, sub string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), sub) {
		t.Fatalf("error %q does not contain %q", err.Error(), sub)
	}
}

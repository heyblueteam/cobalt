//go:build integration

package docker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run against a development Docker daemon:
// COBALT_BUILD_CACHE_INTEGRATION=1 go test -tags integration ./internal/server/docker -run '^TestBuildCache_RealBuildKit$' -v
func TestBuildCache_RealBuildKit(t *testing.T) {
	if os.Getenv("COBALT_BUILD_CACHE_INTEGRATION") != "1" {
		t.Skip("requires opt-in development Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	builder := fmt.Sprintf("cobalt-cache-test-%d", time.Now().UnixNano())
	project := builder
	command := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v\n%s", args, err, out)
		}
		return out
	}
	command("buildx", "create", "--name", builder, "--driver", "docker-container", "--bootstrap")
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		for _, args := range [][]string{
			{"buildx", "rm", "--force", builder},
			{"image", "rm", InternalImageName(project, "default", 1), InternalImageName(project, "default", 2), InternalImageName(project, "default", 3)},
		} {
			if out, err := exec.CommandContext(cleanupCtx, "docker", args...).CombinedOutput(); err != nil {
				t.Errorf("cleanup docker %v: %v\n%s", args, err, out)
			}
		}
	})
	contextDir := t.TempDir()
	cacheDir := filepath.Join(t.TempDir(), "cache")
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile"), []byte("FROM scratch\nCOPY payload /payload\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writePayload := func(text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(contextDir, "payload"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writePayload("first build")
	// Seed a real export without Cobalt cleanup, as on an existing daemon.
	command("buildx", "build", "--builder", builder, "--load", "-t", InternalImageName(project, "default", 1), "--cache-to", "type=local,dest="+cacheDir+",mode=max", contextDir)
	old, err := os.ReadDir(filepath.Join(cacheDir, "blobs", "sha256"))
	if err != nil {
		t.Fatal(err)
	}
	client := New()
	for _, imageManifest := range []bool{false, true} {
		// Exercise the legacy OCI index and newer OCI manifest exporter layouts.
		writePayload(fmt.Sprintf("second build, image manifest = %t", imageManifest))
		command("buildx", "build", "--builder", builder, "--load", "-t", InternalImageName(project, "default", 2), "--cache-from", "type=local,src="+cacheDir, "--cache-to", fmt.Sprintf("type=local,dest=%s,mode=max,image-manifest=%t,oci-mediatypes=true", cacheDir, imageManifest), contextDir)
		removed, _, err := pruneLocalBuildCache(ctx, cacheDir)
		if err != nil || removed == 0 {
			t.Fatalf("real export cleanup: removed=%d err=%v", removed, err)
		}
		t.Logf("image-manifest=%t: removed %d stale blobs (first export had %d)", imageManifest, removed, len(old))
		// Prove the retained export still works without the builder's warm cache.
		command("buildx", "prune", "--builder", builder, "--all", "--force")
		imported := command("buildx", "build", "--progress=plain", "--builder", builder, "--load", "-t", InternalImageName(project, "default", 3), "--cache-from", "type=local,src="+cacheDir, contextDir)
		if !strings.Contains(string(imported), "CACHED") {
			t.Fatalf("cold builder did not reuse retained export:\n%s", imported)
		}
		var out bytes.Buffer
		if _, err := client.Build(ctx, BuildOpts{ProjectName: project, ImageName: "default", DeploymentNumber: 3, BuilderName: builder, Context: contextDir, CacheDir: cacheDir, Output: &out}); err != nil {
			t.Fatalf("build from pruned export: %v\n%s", err, &out)
		}
		if strings.Contains(out.String(), "Warning: build cache cleanup failed") {
			t.Fatalf("cache import/cleanup failed:\n%s", &out)
		}
	}
}

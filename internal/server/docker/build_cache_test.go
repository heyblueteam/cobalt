package docker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	cacheOCIIndex    = "application/vnd.oci.image.index.v1+json"
	cacheOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	cacheConfigType  = "application/vnd.buildkit.cacheconfig.v0"
	cacheLayerType   = "application/vnd.oci.image.layer.v1.tar+gzip"
)

type cacheFixture struct {
	t   *testing.T
	dir string
}

func newCacheFixture(t *testing.T) cacheFixture {
	t.Helper()
	f := cacheFixture{t: t, dir: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(f.dir, "blobs", "sha256"), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f cacheFixture) write(path string, data []byte) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, path), data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f cacheFixture) blob(mediaType string, data []byte) cacheDescriptor {
	f.t.Helper()
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	f.write("blobs/sha256/"+h, data)
	return cacheDescriptor{MediaType: mediaType, Digest: "sha256:" + h, Size: int64(len(data))}
}

func (f cacheFixture) manifest(mediaType string, refs ...cacheDescriptor) cacheDescriptor {
	f.t.Helper()
	m := cacheManifest{SchemaVersion: 2, MediaType: mediaType}
	if cacheIndexType(mediaType) {
		m.Manifests = refs
	} else {
		config := f.blob(cacheConfigType, []byte(`{"layers":[],"records":[]}`))
		m.Config = &config
		m.Layers = refs
	}
	data, err := json.Marshal(m)
	if err != nil {
		f.t.Fatal(err)
	}
	return f.blob(mediaType, data)
}

func (f cacheFixture) index(refs ...cacheDescriptor) {
	f.t.Helper()
	data, err := json.Marshal(cacheManifest{SchemaVersion: 2, MediaType: cacheOCIIndex, Manifests: refs})
	if err != nil {
		f.t.Fatal(err)
	}
	f.write("index.json", data)
}

func (f cacheFixture) path(d cacheDescriptor) string {
	return filepath.Join(f.dir, "blobs", "sha256", strings.TrimPrefix(d.Digest, "sha256:"))
}

func TestPruneLocalBuildCache_RetainsAllCurrentReferences(t *testing.T) {
	t.Parallel()
	f := newCacheFixture(t)
	oldLayer := f.blob(cacheLayerType, []byte("old compiled objects"))
	oldManifest := f.manifest(cacheOCIManifest, oldLayer)
	f.index(oldManifest)
	shared := f.blob(cacheLayerType, []byte("shared compiled dependencies"))
	latest := f.manifest(cacheOCIManifest, shared)
	otherLayer := f.blob("application/vnd.oci.image.layer.v1.tar+zstd", []byte("other platform"))
	other := f.manifest("application/vnd.docker.distribution.manifest.v2+json", shared, otherLayer)
	nested := f.manifest("application/vnd.docker.distribution.manifest.list.v2+json", other)
	// Older BuildKit versions export layers/config directly in an OCI index.
	legacyLayer := f.blob(cacheLayerType, []byte("legacy layer"))
	legacyConfig := f.blob(cacheConfigType, []byte("legacy config"))
	legacy := f.manifest(cacheOCIIndex, legacyLayer, legacyConfig)
	f.index(latest, nested, legacy)
	before, err := os.ReadFile(filepath.Join(f.dir, "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	removed, freed, err := pruneLocalBuildCache(context.Background(), f.dir)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 || freed != oldLayer.Size+oldManifest.Size {
		t.Fatalf("removed %d blobs/%d bytes, want 2/%d", removed, freed, oldLayer.Size+oldManifest.Size)
	}
	for _, d := range []cacheDescriptor{shared, latest, otherLayer, other, nested, legacyLayer, legacyConfig, legacy} {
		if _, err := os.Stat(f.path(d)); err != nil {
			t.Errorf("referenced blob %s removed: %v", d.Digest, err)
		}
	}
	for _, d := range []cacheDescriptor{oldLayer, oldManifest} {
		if _, err := os.Stat(f.path(d)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("old blob %s still present: %v", d.Digest, err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(f.dir, "index.json"))
	if !bytes.Equal(before, after) {
		t.Error("index changed")
	}
	if removed, freed, err := pruneLocalBuildCache(context.Background(), f.dir); err != nil || removed != 0 || freed != 0 {
		t.Fatalf("second cleanup: %d/%d/%v", removed, freed, err)
	}
}

func TestPruneLocalBuildCache_InvalidGraphDeletesNothing(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"missing index", "malformed index", "empty index", "missing blob", "wrong size", "bad manifest hash", "invalid manifest JSON", "invalid schema", "incomplete manifest", "unknown media type", "conflicting descriptors", "traversal digest", "symlink reference"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newCacheFixture(t)
			old := f.blob(cacheLayerType, []byte("old data to preserve on errors"))
			layer := f.blob(cacheLayerType, []byte("current layer"))
			manifest := f.manifest(cacheOCIManifest, layer)
			f.index(manifest)
			switch scenario {
			case "missing index":
				if err := os.Remove(filepath.Join(f.dir, "index.json")); err != nil {
					t.Fatal(err)
				}
			case "malformed index":
				f.write("index.json", []byte("{"))
			case "empty index":
				f.index()
			case "missing blob":
				if err := os.Remove(f.path(layer)); err != nil {
					t.Fatal(err)
				}
			case "wrong size":
				f.write(strings.TrimPrefix(f.path(layer), f.dir+string(os.PathSeparator)), []byte("wrong size"))
			case "bad manifest hash":
				data, err := os.ReadFile(f.path(manifest))
				if err != nil {
					t.Fatal(err)
				}
				data[0] = ' '
				f.write(strings.TrimPrefix(f.path(manifest), f.dir+string(os.PathSeparator)), data)
			case "invalid manifest JSON":
				f.index(f.blob(cacheOCIManifest, []byte("{")))
			case "invalid schema":
				f.index(f.blob(cacheOCIManifest, []byte(`{"schemaVersion":1,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)))
			case "incomplete manifest":
				f.index(f.blob(cacheOCIManifest, []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`)))
			case "unknown media type":
				manifest.MediaType = "application/unknown"
				f.index(manifest)
			case "conflicting descriptors":
				conflict := manifest
				conflict.MediaType = cacheLayerType
				f.index(conflict, manifest)
			case "traversal digest":
				manifest.Digest = "sha256:../../outside"
				f.index(manifest)
			case "symlink reference":
				if err := os.Remove(f.path(layer)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(f.path(old), f.path(layer)); err != nil {
					t.Fatal(err)
				}
			}
			removed, _, err := pruneLocalBuildCache(context.Background(), f.dir)
			if err == nil || removed != 0 {
				t.Fatalf("removed=%d err=%v, want error and no deletion", removed, err)
			}
			if _, err := os.Stat(f.path(old)); err != nil {
				t.Fatalf("old blob deleted on invalid graph: %v", err)
			}
		})
	}
}

func TestPruneLocalBuildCache_LeavesOtherFilesAndProjects(t *testing.T) {
	t.Parallel()
	f := newCacheFixture(t)
	other := newCacheFixture(t)
	layer := f.blob(cacheLayerType, []byte("current"))
	f.index(f.manifest(cacheOCIManifest, layer))
	old := f.blob(cacheLayerType, []byte("old"))
	otherBlob := other.blob(cacheLayerType, []byte("old"))
	f.write("blobs/sha256/notes", []byte("leave alone"))
	f.write("oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`))
	link := filepath.Join(f.dir, "blobs", "sha256", strings.Repeat("a", 64))
	if err := os.Symlink(other.path(otherBlob), link); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.dir, "blobs", "sha256", strings.Repeat("b", 64))
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pruneLocalBuildCache(context.Background(), f.dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.path(old)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old blob not removed: %v", err)
	}
	for _, path := range []string{other.path(otherBlob), link, dir, filepath.Join(f.dir, "blobs", "sha256", "notes"), filepath.Join(f.dir, "oci-layout")} {
		if _, err := os.Lstat(path); err != nil {
			t.Errorf("unrelated path removed: %s: %v", path, err)
		}
	}
}

type cacheExportRunner struct {
	*fakeRunner
	export func()
}

func (r cacheExportRunner) RunWithEnv(ctx context.Context, env map[string]string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	err := r.fakeRunner.RunWithEnv(ctx, env, args, stdin, stdout, stderr)
	r.export()
	return err
}

func TestBuild_PrunesOnlyAfterSuccessfulExport(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"success", "failed build", "canceled", "invalid export"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newCacheFixture(t)
			old := f.blob(cacheLayerType, []byte("previous cache layer"))
			f.index(f.manifest(cacheOCIManifest, old))
			runner := newFakeRunner()
			if scenario == "failed build" {
				runner.answerErr("buildx build", staticErr("export failed"))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := cacheExportRunner{fakeRunner: runner, export: func() {
				if _, err := os.Stat(f.path(old)); err != nil {
					t.Fatalf("cleanup ran before export: %v", err)
				}
				layer := f.blob(cacheLayerType, []byte("new cache layer"))
				f.index(f.manifest(cacheOCIManifest, layer))
				if scenario == "canceled" {
					cancel()
				}
				if scenario == "invalid export" {
					f.write("index.json", []byte("bad export"))
				}
			}}
			var out bytes.Buffer
			tag, err := NewWithRunner(r).Build(ctx, BuildOpts{ProjectName: "api", ImageName: "default", DeploymentNumber: 2, CacheDir: f.dir, Output: &out})
			if scenario == "failed build" {
				if err == nil {
					t.Fatal("build error lost")
				}
			} else if err != nil || tag != "cobalt/project-api-default:2" {
				t.Fatalf("successful build failed: tag=%s err=%v", tag, err)
			}
			_, statErr := os.Stat(f.path(old))
			if scenario == "success" {
				if !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("old blob remains: %v", statErr)
				}
				if !strings.Contains(out.String(), "Build cache cleanup: removed") {
					t.Fatal("cleanup result missing from deployment output")
				}
			} else {
				if statErr != nil {
					t.Fatalf("old cache deleted: %v", statErr)
				}
				if scenario != "failed build" && !strings.Contains(out.String(), "Warning: build cache cleanup failed") {
					t.Fatal("cleanup warning missing")
				}
			}
		})
	}
}

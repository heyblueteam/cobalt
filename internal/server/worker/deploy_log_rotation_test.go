package worker

import (
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLog(t *testing.T, dir, project, name, content string, mtime time.Time) string {
	t.Helper()
	parent := filepath.Join(dir, "logs", "deployments", project)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRotateDeployLogs_GzipsOldLogs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// Recent — should NOT be rotated.
	freshPath := writeLog(t, dir, "api", "10.log", "fresh content", now.Add(-24*time.Hour))
	// Old — should be rotated.
	oldPath := writeLog(t, dir, "api", "9.log", "old content", now.Add(-31*24*time.Hour))

	rotated, purged, err := RotateDeployLogs(context.Background(), quietLogger(), dir,
		30*24*time.Hour, 365*24*time.Hour, now)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rotated != 1 {
		t.Errorf("rotated: got %d, want 1", rotated)
	}
	if purged != 0 {
		t.Errorf("purged: got %d, want 0", purged)
	}

	// Old .log gone, .log.gz exists.
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("old .log should have been removed")
	}
	if _, err := os.Stat(oldPath + ".gz"); err != nil {
		t.Errorf("old .log.gz missing: %v", err)
	}
	// Fresh untouched.
	if _, err := os.Stat(freshPath); err != nil {
		t.Errorf("fresh .log was removed: %v", err)
	}
}

func TestRotateDeployLogs_PurgesOldGzips(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	// Newer gz — keep.
	keep := writeLog(t, dir, "api", "9.log.gz", "fake gz", now.Add(-30*24*time.Hour))
	// Old gz — purge.
	purge := writeLog(t, dir, "api", "1.log.gz", "fake gz", now.Add(-400*24*time.Hour))

	_, purged, err := RotateDeployLogs(context.Background(), quietLogger(), dir,
		30*24*time.Hour, 365*24*time.Hour, now)
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if purged != 1 {
		t.Errorf("purged: got %d, want 1", purged)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("keep file gone: %v", err)
	}
	if _, err := os.Stat(purge); !os.IsNotExist(err) {
		t.Error("old gz should have been purged")
	}
}

func TestRotateDeployLogs_GzipPreservesContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now()
	content := strings.Repeat("hello deploy log\n", 100)
	writeLog(t, dir, "api", "5.log", content, now.Add(-31*24*time.Hour))

	if _, _, err := RotateDeployLogs(context.Background(), quietLogger(), dir,
		30*24*time.Hour, 365*24*time.Hour, now); err != nil {
		t.Fatalf("Rotate: %v", err)
	}

	// Read the .gz, decompress, compare.
	gzPath := filepath.Join(dir, "logs", "deployments", "api", "5.log.gz")
	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("Open gz: %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer gr.Close()
	got, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != content {
		t.Errorf("decompressed mismatch: len got %d, want %d", len(got), len(content))
	}
}

func TestRotateDeployLogs_MissingRootIsFine(t *testing.T) {
	t.Parallel()
	// No logs dir created.
	dir := t.TempDir()
	if _, _, err := RotateDeployLogs(context.Background(), quietLogger(), dir,
		30*24*time.Hour, 365*24*time.Hour, time.Now()); err != nil {
		t.Errorf("missing root should not error: %v", err)
	}
}

func TestRotateDeployLogs_DefaultsApplied(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Now()
	// Just under default rotate age — should NOT rotate.
	writeLog(t, dir, "api", "1.log", "x", now.Add(-29*24*time.Hour))
	rotated, _, _ := RotateDeployLogs(context.Background(), quietLogger(), dir, 0, 0, now)
	if rotated != 0 {
		t.Errorf("rotated %d files within default age", rotated)
	}
}

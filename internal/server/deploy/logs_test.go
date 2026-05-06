package deploy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployLogPath(t *testing.T) {
	t.Parallel()
	got := DeployLogPath("/data", "myapp", 7)
	want := "/data/logs/deployments/myapp/7.log"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestOpenDeployLog_CreatesFileAndAppends(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	w, err := OpenDeployLog(dir, "myapp", 1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := w.Write([]byte("first\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	_ = w.Close()

	// Open again, append more — verify file isn't truncated.
	w2, err := OpenDeployLog(dir, "myapp", 1)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if _, err := w2.Write([]byte("second\n")); err != nil {
		t.Fatalf("Write 2: %v", err)
	}
	_ = w2.Close()

	got, err := os.ReadFile(filepath.Join(dir, "logs", "deployments", "myapp", "1.log"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(got), "first") || !strings.Contains(string(got), "second") {
		t.Errorf("appended content lost: %q", string(got))
	}
}

func TestOpenDeployLog_CreatesParentDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, err := OpenDeployLog(dir, "deeply/nested/name", 1)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = w.Close()
}

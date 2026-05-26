// Package storetest provides a shared test harness for spawning a real
// rqlited container per test, so packages outside store/ can write
// integration tests against a live database without each one re-implementing
// the docker-orchestration boilerplate.
//
// This package is test-only by convention but lives outside _test.go files
// so it can be imported across packages. Importing it pulls in os/exec and
// net, so production callers should never depend on it directly.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/store"
)

// OpenDB starts a fresh rqlited container, opens a *store.DB connected to
// it, runs InitSchema, and registers cleanup. Each call gets its own
// container so tests don't share state. Tests that t.Parallel() are safe.
//
// Requires Docker on PATH and the rqlite/rqlite:latest image. If unavailable,
// the test fails fast with a clear message rather than hanging.
func OpenDB(t *testing.T) *store.DB {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH; storetest tests require docker")
	}

	dir := t.TempDir()
	port := chooseUnusedPort(t)
	containerName := "cobalt-test-rqlite-" + sanitize(t.Name()) + "-" + randomSuffix()

	// In case a prior crashed run left a container with the same name,
	// remove it before trying to create.
	stopRqlitedContainer(containerName)

	if err := startRqlitedContainer(containerName, dir, port); err != nil {
		t.Fatalf("start rqlited container: %v", err)
	}

	url := "http://localhost:" + port
	if !waitForRqlite(url, 15*time.Second) {
		stopRqlitedContainer(containerName)
		t.Fatalf("rqlited not ready at %s", url)
	}

	db, err := store.Open(url)
	if err != nil {
		stopRqlitedContainer(containerName)
		t.Fatalf("store.Open: %v", err)
	}

	if err := db.InitSchema(context.Background()); err != nil {
		_ = db.Close()
		stopRqlitedContainer(containerName)
		t.Fatalf("InitSchema: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Close()
		stopRqlitedContainer(containerName)
	})

	return db
}

func startRqlitedContainer(name, dataDir, hostPort string) error {
	// Match the host UID/GID so files rqlite writes into the
	// bind-mounted dataDir (t.TempDir) are owned by the test process
	// and can be cleaned up. The official rqlite image runs as a
	// non-root user by default; on Linux CI that left t.TempDir
	// unable to unlinkat the wsnapshots/*.crc32 files at test end
	// (root- or container-user-owned, not test-user-owned), failing
	// every rqlite-backed test with "permission denied".
	cmd := exec.Command(
		"docker", "run",
		"--name", name,
		"--rm",
		"--detach",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-v", dataDir+":/rqlite/data",
		"-p", hostPort+":4001",
		"rqlite/rqlite:latest",
		"rqlited",
		"-http-addr", "0.0.0.0:4001",
		"-http-adv-addr", "localhost:4001",
		"/rqlite/data",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func stopRqlitedContainer(name string) {
	_ = exec.Command("docker", "stop", name).Run()
}

func chooseUnusedPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	addr := l.Addr().String()
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}
	return p
}

func waitForRqlite(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		db, err := store.Open(url)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = db.Ping(ctx)
		cancel()
		_ = db.Close()
		if err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// sanitize turns t.Name() into a docker-safe container suffix:
// strips slashes (subtests use "/"), lowercases, length-caps.
func sanitize(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ToLower(name)
	if len(name) > 50 {
		name = name[:50]
	}
	return name
}

// randomSuffix returns 8 hex chars. Appended to container names so two
// `go test`-runs against the same test (or stuck containers from a prior
// crashed run) don't collide.
func randomSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

package store

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
)

// openTestDB starts a fresh rqlited container, opens a *DB connected to it,
// runs InitSchema, and registers cleanup. Mirrors storetest.OpenDB but
// lives in this package to avoid the import cycle (storetest imports store,
// so store can't import storetest in its tests).
func openTestDB(t *testing.T) *DB {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH; openTestDB tests require docker")
	}

	dir := t.TempDir()
	port := chooseUnusedPort(t)
	containerName := "cobalt-test-rqlite-" + sanitize(t.Name()) + "-" + randomSuffix()

	stopRqlitedContainer(containerName)

	if err := startRqlitedContainer(containerName, dir, port); err != nil {
		t.Fatalf("start rqlited container: %v", err)
	}

	url := "http://localhost:" + port
	if !waitForRqlite(url, 15*time.Second) {
		stopRqlitedContainer(containerName)
		t.Fatalf("rqlited not ready at %s", url)
	}

	db, err := Open(url)
	if err != nil {
		stopRqlitedContainer(containerName)
		t.Fatalf("Open: %v", err)
	}

	if err := db.InitSchema(context.Background()); err != nil {
		_ = db.Client.Close()
		stopRqlitedContainer(containerName)
		t.Fatalf("InitSchema: %v", err)
	}

	t.Cleanup(func() {
		_ = db.Client.Close()
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
	cmd := exec.Command("docker", "run",
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
		db, err := Open(url)
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err = db.Ping(ctx)
		cancel()
		_ = db.Client.Close()
		if err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func sanitize(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ToLower(name)
	if len(name) > 50 {
		name = name[:50]
	}
	return name
}

func randomSuffix() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

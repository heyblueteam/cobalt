package store

import (
	"context"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()

	dir := t.TempDir()
	port := chooseUnusedPort(t)
	containerName := "cobalt-test-rqlite-" + t.Name()

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
		db.Client.Close()
		stopRqlitedContainer(containerName)
		t.Fatalf("InitSchema: %v", err)
	}

	t.Cleanup(func() {
		db.Client.Close()
		stopRqlitedContainer(containerName)
	})

	return db
}

func startRqlitedContainer(name, dataDir, hostPort string) error {
	cmd := exec.Command("docker", "run",
		"--name", name,
		"--rm",
		"--user", "root",
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
	return cmd.Start()
}

func stopRqlitedContainer(name string) {
	exec.Command("docker", "stop", name).Run()
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
		db.Client.Close()
		if err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

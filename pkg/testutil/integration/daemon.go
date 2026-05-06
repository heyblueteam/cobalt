//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type DaemonOptions struct {
	CaddyAdminURL  string
	DockerHost     string
	NetworkName    string
	BinaryPath     string
	Port           int
}

const defaultDaemonPort = 8080

func StartDaemon(ctx context.Context, t testing.TB, opts DaemonOptions) (baseURL string, stop func()) {
	t.Helper()

	port := opts.Port
	if port == 0 {
		port = defaultDaemonPort
	}

	dockerHost := opts.DockerHost
	if dockerHost == "" {
		dockerHost = "unix:///var/run/docker.sock"
	}

	networkName := opts.NetworkName
	if networkName == "" {
		networkName = "cobalt-integration"
	}

	binaryPath := opts.BinaryPath
	if binaryPath == "" {
		binaryPath = buildBinary(ctx, t)
	}

	caddyURL := opts.CaddyAdminURL
	if caddyURL == "" {
		caddyURL = "http://caddy:2019"
	}

	dataDir := t.TempDir()

	containerName := "cobalt-test-daemon-" + randomID()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			t.Fatalf("docker %s: %v", args, err)
		}
	}

	run("network", "create", "-d", "bridge", networkName)

	run("run", "-d",
		"--name", containerName,
		"--network", networkName,
		"--hostname", "cobalt",
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", dataDir+":/data",
		"-v", binaryPath+":/cobalt",
		"-p", fmt.Sprintf("%d:80", port),
		"--env", "DOCKER_HOST="+dockerHost,
		"--env", "CADDY_ADMIN_URL="+caddyURL,
		"--entrypoint", "/cobalt",
		"debian:stable-slim",
		"server", "--addr", ":80", "--data-dir", "/data",
	)

	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)

	ctxCaddy, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !waitForDaemon(ctxCaddy, t, baseURL) {
		logs := dockerLogs(ctx, t, containerName)
		dockerRM(ctx, t, containerName)
		dockerNetworkRM(ctx, t, networkName)
		t.Fatalf("daemon did not become healthy at %s. Logs:\n%s", baseURL, logs)
	}

	stop = func() {
		dockerRM(ctx, t, containerName)
		dockerNetworkRM(ctx, t, networkName)
	}

	return baseURL, stop
}

func buildBinary(ctx context.Context, t testing.TB) string {
	t.Helper()

	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "cobalt")

	cmd := exec.CommandContext(ctx, "go", "build",
		"-o", outPath,
		"github.com/heyblueteam/cobalt/cmd/cobalt",
	)
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Skipf("go build: %v (is Go installed?)", err)
	}

	return outPath
}

func waitForDaemon(ctx context.Context, t testing.TB, baseURL string) bool {
	t.Helper()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				io.ReadAll(resp.Body)
				resp.Body.Close()
				return resp.StatusCode == 200
			}
		}
	}
}

func dockerLogs(ctx context.Context, t testing.TB, container string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "docker", "logs", container)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, _ := cmd.Output()
	return string(out) + string(stderr.Bytes())
}

func TriggerReconcile(ctx context.Context, t testing.TB, baseURL string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/internal/reconcile", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("trigger reconcile: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("trigger reconcile: status %d: %s", resp.StatusCode, string(body))
	}
}

func randomID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
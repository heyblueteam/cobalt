//go:build integration

package integration

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func StartCaddy(ctx context.Context, t testing.TB, networkName string) (adminBaseURL string, stop func()) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Stderr = io.Discard
		if err := cmd.Run(); err != nil {
			t.Fatalf("docker %s: %v", args, err)
		}
	}

	dockerNetworkCreate(ctx, t, networkName)

	const name = "cobalt-test-caddy"
	run("rm", "-f", name)
	run("run", "-d",
		"--name", name,
		"--network", networkName,
		"caddy:3-alpine",
		"caddy", "run",
		"--adapter", "caddyfile",
		"--config", "/etc/caddy/Caddyfile",
	)

	stop = func() {
		_ = exec.Command("docker", "rm", "-f", name).Run()
		_ = exec.Command("docker", "network", "rm", networkName).Run()
	}

	return "http://caddy:2019", stop
}

func WaitForCaddyReady(ctx context.Context, t testing.TB, baseURL string, timeout time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatalf("Caddy admin API at %s did not become available within %v", baseURL, timeout)
		case <-ticker.C:
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/config/", nil)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				io.ReadAll(resp.Body)
				resp.Body.Close()
				return
			}
		}
	}
}

func caddyAdminRequest(ctx context.Context, t testing.TB, baseURL, method, path string, body []byte) {
	t.Helper()
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bodyReader)
	if err != nil {
		t.Fatalf("caddy request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("caddy %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		t.Fatalf("caddy %s %s: status %d", method, path, resp.StatusCode)
	}
}

func dockerExec(ctx context.Context, t testing.TB, container string, args ...string) string {
	t.Helper()
	dockerArgs := []string{"exec", container}
	dockerArgs = append(dockerArgs, args...)
	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("docker exec %s %s: %v (stderr: %s)", container, strings.Join(args, " "), err, stderr.String())
	}
	return string(bytes.TrimSpace(out))
}

func dockerKill(ctx context.Context, t testing.TB, container string) {
	t.Helper()
	_ = exec.CommandContext(ctx, "docker", "kill", container).Run()
}

func dockerRM(ctx context.Context, t testing.TB, container string) {
	t.Helper()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", container).Run()
}

func dockerPull(ctx context.Context, t testing.TB, image string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "docker", "pull", image)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		t.Skipf("docker pull %s: %v", image, err)
	}
}

func dockerNetworkCreate(ctx context.Context, t testing.TB, name string) {
	t.Helper()
	_ = exec.CommandContext(ctx, "docker", "network", "create", "-d", "bridge", name).Run()
}

func dockerNetworkRM(ctx context.Context, t testing.TB, name string) {
	t.Helper()
	_ = exec.CommandContext(ctx, "docker", "network", "rm", name).Run()
}
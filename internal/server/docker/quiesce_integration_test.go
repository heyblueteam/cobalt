//go:build integration

package docker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Requires a disposable daemon: the delayed writer holds its file lock after
// SIGTERM. An accepted scale request must not release the replacement early.
func TestQuiesceService_RealSwarm(t *testing.T) {
	if os.Getenv("COBALT_DISPOSABLE_SWARM") != "1" {
		t.Skip("requires a disposable Docker daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("swarm", "init")
	t.Cleanup(func() { _ = exec.Command("docker", "swarm", "leave", "--force").Run() })
	volume := fmt.Sprintf("stopfirst-test-%d", time.Now().UnixNano())
	run("volume", "create", volume)
	t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", volume).Run() })
	run("pull", "python:3.12-alpine")
	script := `import fcntl,pathlib,signal,time,sys
f=open("/data/lock","w")
fcntl.flock(f,fcntl.LOCK_EX|fcntl.LOCK_NB)
pathlib.Path("/data/ready").write_text(sys.argv[1])
def stop(*args):
 time.sleep(3)
 sys.exit(0)
signal.signal(signal.SIGTERM,stop)
while True: time.sleep(.1)
`
	c := New()
	for gen := 1; gen <= 2; gen++ {
		name := ServiceName("stopfirst-test", gen, "web")
		opts := ServiceCreateOpts{ProjectID: 1, ProjectName: "stopfirst-test", ServiceName: "web", DeploymentNumber: gen, Image: "python:3.12-alpine", Command: "python -u -c '" + script + "' " + fmt.Sprint(gen), Volumes: []ServiceVolume{{VolumeName: volume, DestinationPath: "/data"}}}
		if err := c.CreateService(ctx, opts); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = exec.Command("docker", "service", "rm", name).Run() })
		deadline := time.Now().Add(30 * time.Second)
		for {
			ready := run("run", "--rm", "-v", volume+":/data", "python:3.12-alpine", "python", "-c", `import pathlib; p=pathlib.Path("/data/ready"); print(p.read_text() if p.exists() else "")`)
			if ready == fmt.Sprint(gen) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("writer %d failed to acquire lock", gen)
			}
			time.Sleep(100 * time.Millisecond)
		}
		started := time.Now()
		if err := c.SetServiceReplicas(ctx, name, 0); err != nil {
			t.Fatal(err)
		}
		if err := c.WaitForServiceStopped(ctx, name); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed < 3*time.Second {
			t.Fatalf("returned before delayed writer exited: %s", elapsed)
		}
		if _, err := c.ServiceReplicas(ctx, name); err != nil {
			t.Fatal("old service spec removed:", err)
		}
	}
}

package docker

import (
	"bytes"
	"context"
	"testing"
)

func TestCreateVolume_IdempotentWhenExists(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("volume ls", "cobalt-volume-7-data\n")
	c := NewWithRunner(r)
	if err := c.CreateVolume(context.Background(), 7, "api", "data"); err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	// Should NOT have called `volume create` — only `volume ls`.
	for _, call := range r.calls {
		if argSequence(call.Args, "volume", "create") {
			t.Errorf("volume create called when volume exists: %v", call.Args)
		}
	}
}

func TestCreateVolume_CreatesIfMissing(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("volume ls", "")
	c := NewWithRunner(r)
	if err := c.CreateVolume(context.Background(), 7, "api", "data"); err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	last := r.lastCall().Args
	if !argSequence(last, "volume", "create") {
		t.Errorf("expected volume create call: %v", last)
	}
	if last[len(last)-1] != "cobalt-volume-7-data" {
		t.Errorf("name: %q", last[len(last)-1])
	}
	for _, want := range []string{"cobalt.project.id=7", "cobalt.project.name=api"} {
		if !argHas(last, want) {
			t.Errorf("missing label %q", want)
		}
	}
}

func TestListVolumesForProject(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("volume ls", "cobalt-volume-7-data\ncobalt-volume-7-uploads\n")
	c := NewWithRunner(r)
	vols, err := c.ListVolumesForProject(context.Background(), 7)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(vols) != 2 || vols[0] != "cobalt-volume-7-data" {
		t.Errorf("got %v", vols)
	}
	args := r.lastCall().Args
	// Filter by name prefix (canonical) rather than label — see
	// ListVolumesForProject's docstring for why labels are
	// unreliable for this query.
	if !argSequence(args, "--filter", "name=cobalt-volume-7-") {
		t.Errorf("filter args: %v", args)
	}
}

func TestExportVolume(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout("run --rm --mount type=volume,source=cobalt-volume-7-data,destination=/data",
		"<tar-bytes>\n")
	c := NewWithRunner(r)
	var buf bytes.Buffer
	if err := c.ExportVolume(context.Background(), "cobalt-volume-7-data", &buf); err != nil {
		t.Fatalf("ExportVolume: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("<tar-bytes>")) {
		t.Errorf("output: %q", buf.String())
	}
	args := r.lastCall().Args
	for _, w := range []string{"run", "--rm", "tar", "-cf", "-"} {
		if !argHas(args, w) {
			t.Errorf("missing %q in %v", w, args)
		}
	}
}

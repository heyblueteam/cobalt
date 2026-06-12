package sysstats

import (
	"errors"
	"math"
	"testing"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 0.01 }

// fakeStatfs returns fixed numbers for "/" and errors for anything else,
// exercising both the happy path and the skip-on-error behaviour.
func fakeStatfs(path string) (uint64, uint64, error) {
	if path == "/" {
		return 64 << 30, 100 << 30, nil
	}
	return 0, 0, errors.New("no such mount")
}

func TestSample(t *testing.T) {
	s := &Sampler{
		ProcRoot: "testdata/proc1",
		Mounts:   []string{"/", "/var/lib/missing"},
		Statfs:   fakeStatfs,
	}
	sess := s.Session()

	first, err := sess.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if first.CPUPercent != 0 {
		t.Errorf("first sample CPUPercent = %v, want 0 (no prior sample)", first.CPUPercent)
	}
	if first.CPUCount != 8 {
		t.Errorf("CPUCount = %d, want 8", first.CPUCount)
	}
	if want := uint64(32000000) * 1024; first.MemTotalBytes != want {
		t.Errorf("MemTotalBytes = %d, want %d", first.MemTotalBytes, want)
	}
	// Used follows free(1): MemTotal − MemAvailable, not MemTotal − MemFree.
	if want := uint64(32000000-11000000) * 1024; first.MemUsedBytes != want {
		t.Errorf("MemUsedBytes = %d, want %d", first.MemUsedBytes, want)
	}
	if want := uint64(8000000-6500000) * 1024; first.SwapUsedBytes != want {
		t.Errorf("SwapUsedBytes = %d, want %d", first.SwapUsedBytes, want)
	}
	if !almostEqual(first.Load1, 1.23) || !almostEqual(first.Load5, 0.98) || !almostEqual(first.Load15, 0.75) {
		t.Errorf("load = %v %v %v, want 1.23 0.98 0.75", first.Load1, first.Load5, first.Load15)
	}
	if first.UptimeSeconds != 3613441 {
		t.Errorf("UptimeSeconds = %d, want 3613441", first.UptimeSeconds)
	}
	if len(first.Disks) != 1 || first.Disks[0].Mount != "/" {
		t.Fatalf("Disks = %+v, want exactly the readable mount /", first.Disks)
	}
	if first.Disks[0].UsedBytes != 64<<30 || first.Disks[0].TotalBytes != 100<<30 {
		t.Errorf("disk usage = %d/%d, want %d/%d", first.Disks[0].UsedBytes, first.Disks[0].TotalBytes, uint64(64<<30), uint64(100<<30))
	}

	// Second sample from advanced counters: Δbusy=5100, Δtotal=11300.
	s.ProcRoot = "testdata/proc2"
	second, err := sess.Sample()
	if err != nil {
		t.Fatal(err)
	}
	if want := 100 * 5100.0 / 11300.0; !almostEqual(second.CPUPercent, want) {
		t.Errorf("second sample CPUPercent = %v, want %v", second.CPUPercent, want)
	}
}

// Sessions must not share delta state: a second consumer sampling in
// between (another dashboard, a --json poller) used to shrink the first
// consumer's measurement window to near zero, degrading CPU% to noise.
func TestSessionsAreIndependent(t *testing.T) {
	s := &Sampler{ProcRoot: "testdata/proc1", Statfs: fakeStatfs}
	a, b := s.Session(), s.Session()

	if _, err := a.Sample(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Sample(); err != nil {
		t.Fatal(err)
	}

	s.ProcRoot = "testdata/proc2"
	want := 100 * 5100.0 / 11300.0
	for name, sess := range map[string]*Session{"a": a, "b": b} {
		got, err := sess.Sample()
		if err != nil {
			t.Fatal(err)
		}
		if !almostEqual(got.CPUPercent, want) {
			t.Errorf("session %s CPUPercent = %v, want %v (delta window corrupted by the other session)", name, got.CPUPercent, want)
		}
	}
}

func TestCPUPercentEdgeCases(t *testing.T) {
	cases := []struct {
		name       string
		prev, cur  cpuTimes
		wantedZero bool
	}{
		{"no prior sample", cpuTimes{}, cpuTimes{busy: 10, total: 100}, true},
		{"counter wrap", cpuTimes{busy: 90, total: 100}, cpuTimes{busy: 5, total: 50}, true},
		{"stalled clock", cpuTimes{busy: 10, total: 100}, cpuTimes{busy: 10, total: 100}, true},
	}
	for _, c := range cases {
		if got := cpuPercent(c.prev, c.cur); (got == 0) != c.wantedZero {
			t.Errorf("%s: cpuPercent = %v", c.name, got)
		}
	}
}

func TestParseCPUTimesMalformed(t *testing.T) {
	if _, _, err := parseCPUTimes([]byte("intr 1 2 3\n")); err == nil {
		t.Error("want error for /proc/stat without a cpu line")
	}
	if _, _, err := parseCPUTimes([]byte("cpu  1 2\n")); err == nil {
		t.Error("want error for truncated cpu line")
	}
}

func TestParseMemInfoMissingTotal(t *testing.T) {
	if err := parseMemInfo([]byte("MemFree: 10 kB\n"), &cobaltapi.SystemStats{}); err == nil {
		t.Error("want error when MemTotal is absent")
	}
}

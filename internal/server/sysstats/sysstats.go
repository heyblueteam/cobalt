// Package sysstats samples host-level statistics — CPU, memory, load,
// uptime, disk — from the Linux /proc filesystem and statfs(2).
//
// The parsers take raw bytes and the Sampler reads from an injectable
// root directory, so tests run against fixture files on any OS; only the
// default statfs implementation touches the real system.
package sysstats

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// Sampler is the host-stats configuration: where procfs lives and which
// mounts to report. It holds no sampling state — CPU utilisation is a
// delta between consecutive reads, so each consumer takes its own
// Session; sharing one would interleave every consumer's measurement
// windows (two dashboards sampling the same state see windows of
// near-zero jiffies, and the CPU number degrades to 0 or noise).
type Sampler struct {
	// ProcRoot is the procfs mount to read from. Defaults to /proc;
	// tests point it at a fixture directory.
	ProcRoot string
	// Mounts are the filesystems to report disk usage for.
	Mounts []string
	// Statfs resolves a mount's usage. Defaults to the real statfs(2);
	// tests inject a fake.
	Statfs func(path string) (used, total uint64, err error)
}

// New returns a Sampler reading the real /proc and the given mounts.
func New(mounts ...string) *Sampler {
	return &Sampler{ProcRoot: "/proc", Mounts: mounts, Statfs: statfs}
}

// Session starts an independent sampling session. The first Sample of a
// session reports CPUPercent 0 (no prior reading to delta against).
func (s *Sampler) Session() *Session {
	return &Session{conf: s}
}

// Session computes CPU deltas over its own lifetime. Not safe for
// concurrent use — one session per connection/goroutine.
type Session struct {
	conf *Sampler
	prev cpuTimes
}

// Sample reads one snapshot. Partial failures (one unreadable mount) are
// not fatal — the affected section is zero/omitted — but an unreadable
// procfs is, since the snapshot would be meaningless.
func (se *Session) Sample() (cobaltapi.SystemStats, error) {
	s := se.conf
	var out cobaltapi.SystemStats

	stat, err := os.ReadFile(filepath.Join(s.ProcRoot, "stat"))
	if err != nil {
		return out, fmt.Errorf("sysstats: %w", err)
	}
	cur, count, err := parseCPUTimes(stat)
	if err != nil {
		return out, err
	}
	out.CPUCount = count
	out.CPUPercent = cpuPercent(se.prev, cur)
	se.prev = cur

	mem, err := os.ReadFile(filepath.Join(s.ProcRoot, "meminfo"))
	if err != nil {
		return out, fmt.Errorf("sysstats: %w", err)
	}
	if err := parseMemInfo(mem, &out); err != nil {
		return out, err
	}

	load, err := os.ReadFile(filepath.Join(s.ProcRoot, "loadavg"))
	if err != nil {
		return out, fmt.Errorf("sysstats: %w", err)
	}
	if out.Load1, out.Load5, out.Load15, err = parseLoadAvg(load); err != nil {
		return out, err
	}

	up, err := os.ReadFile(filepath.Join(s.ProcRoot, "uptime"))
	if err != nil {
		return out, fmt.Errorf("sysstats: %w", err)
	}
	if out.UptimeSeconds, err = parseUptime(up); err != nil {
		return out, err
	}

	for _, m := range s.Mounts {
		used, total, err := s.Statfs(m)
		if err != nil || isDuplicateDisk(out.Disks, used, total) {
			continue
		}
		out.Disks = append(out.Disks, cobaltapi.DiskStats{Mount: m, UsedBytes: used, TotalBytes: total})
	}
	return out, nil
}

// isDuplicateDisk drops mounts that resolve to a filesystem already
// reported — the daemon's data dir usually lives on the root filesystem,
// and listing the same disk twice reads as double the storage.
func isDuplicateDisk(disks []cobaltapi.DiskStats, used, total uint64) bool {
	for _, d := range disks {
		if d.UsedBytes == used && d.TotalBytes == total {
			return true
		}
	}
	return false
}

// cpuTimes is the aggregate "cpu" line of /proc/stat, reduced to the two
// numbers utilisation needs. Units are jiffies; only deltas are meaningful.
type cpuTimes struct {
	busy, total uint64
}

// parseCPUTimes reads the aggregate cpu line and counts per-core "cpuN"
// lines. Fields: user nice system idle iowait irq softirq steal (guest
// columns are already included in user/nice and must not be re-added).
func parseCPUTimes(b []byte) (cpuTimes, int, error) {
	var t cpuTimes
	count := 0
	for line := range strings.SplitSeq(string(b), "\n") {
		switch {
		case strings.HasPrefix(line, "cpu "):
			f := strings.Fields(line)
			if len(f) < 9 {
				return t, 0, fmt.Errorf("sysstats: malformed cpu line %q", line)
			}
			var v [8]uint64
			for i := range v {
				n, err := strconv.ParseUint(f[i+1], 10, 64)
				if err != nil {
					return t, 0, fmt.Errorf("sysstats: cpu line %q: %w", line, err)
				}
				v[i] = n
			}
			idle := v[3] + v[4] // idle + iowait
			t.total = v[0] + v[1] + v[2] + v[5] + v[6] + v[7] + idle
			t.busy = t.total - idle
		case strings.HasPrefix(line, "cpu"):
			count++
		}
	}
	if t.total == 0 {
		return t, 0, fmt.Errorf("sysstats: no cpu line in /proc/stat")
	}
	return t, count, nil
}

// cpuPercent is the busy share of the jiffies elapsed between two samples,
// 0–100. Zero when there is no usable prior sample (first call, counter
// wrap, or a procfs that doesn't advance).
func cpuPercent(prev, cur cpuTimes) float64 {
	if prev.total == 0 || cur.total <= prev.total || cur.busy < prev.busy {
		return 0
	}
	return 100 * float64(cur.busy-prev.busy) / float64(cur.total-prev.total)
}

// parseMemInfo fills the memory fields from /proc/meminfo. "Used" follows
// free(1): MemTotal − MemAvailable, which accounts for reclaimable page
// cache (MemFree alone wildly overstates pressure).
func parseMemInfo(b []byte, out *cobaltapi.SystemStats) error {
	var memTotal, memAvail, swapTotal, swapFree uint64
	want := map[string]*uint64{
		"MemTotal":     &memTotal,
		"MemAvailable": &memAvail,
		"SwapTotal":    &swapTotal,
		"SwapFree":     &swapFree,
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		dst, ok := want[key]
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			return fmt.Errorf("sysstats: malformed meminfo line %q", line)
		}
		n, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return fmt.Errorf("sysstats: meminfo line %q: %w", line, err)
		}
		*dst = n * 1024 // values are kB
	}
	if memTotal == 0 {
		return fmt.Errorf("sysstats: no MemTotal in /proc/meminfo")
	}
	out.MemTotalBytes = memTotal
	out.MemUsedBytes = memTotal - memAvail
	out.SwapTotalBytes = swapTotal
	out.SwapUsedBytes = swapTotal - swapFree
	return nil
}

// parseLoadAvg reads the three load averages from /proc/loadavg.
func parseLoadAvg(b []byte) (l1, l5, l15 float64, err error) {
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return 0, 0, 0, fmt.Errorf("sysstats: malformed loadavg %q", string(b))
	}
	for i, dst := range []*float64{&l1, &l5, &l15} {
		if *dst, err = strconv.ParseFloat(f[i], 64); err != nil {
			return 0, 0, 0, fmt.Errorf("sysstats: loadavg %q: %w", string(b), err)
		}
	}
	return l1, l5, l15, nil
}

// parseUptime reads whole seconds of host uptime from /proc/uptime.
func parseUptime(b []byte) (int64, error) {
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0, fmt.Errorf("sysstats: malformed uptime %q", string(b))
	}
	sec, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, fmt.Errorf("sysstats: uptime %q: %w", string(b), err)
	}
	return int64(sec), nil
}

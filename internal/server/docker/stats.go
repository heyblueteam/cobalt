package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// swarmServiceLabel is set by swarm itself on every task container —
// the only ownership signal present on containers deployed before
// cobalt started adding --container-label (see CreateService).
const swarmServiceLabel = "com.docker.swarm.service.name"

// statsRow mirrors one emit of `docker stats --format '{{json .}}'`.
// All values arrive as human-formatted strings; parsing failures degrade
// the affected field to zero rather than dropping the row — a momentarily
// garbled column must not blank out a container mid-stream.
type statsRow struct {
	ID       string `json:"ID"`
	Name     string `json:"Name"`
	CPUPerc  string `json:"CPUPerc"`
	MemUsage string `json:"MemUsage"`
	NetIO    string `json:"NetIO"`
	BlockIO  string `json:"BlockIO"`
	PIDs     string `json:"PIDs"`
}

// ContainerUsage is one container's resource usage with raw docker
// identity; ownership attribution happens separately (ResolveOwner) so
// the sampler needs no knowledge of projects.
type ContainerUsage struct {
	ID, Name                        string
	CPUPercent                      float64
	MemUsedBytes, MemLimitBytes     uint64
	NetRxBytes, NetTxBytes          uint64
	BlockReadBytes, BlockWriteBytes uint64
	PIDs                            int
}

func (r statsRow) usage() ContainerUsage {
	u := ContainerUsage{ID: r.ID, Name: r.Name, CPUPercent: parsePercent(r.CPUPerc)}
	u.MemUsedBytes, u.MemLimitBytes = parseSizePair(r.MemUsage)
	u.NetRxBytes, u.NetTxBytes = parseSizePair(r.NetIO)
	u.BlockReadBytes, u.BlockWriteBytes = parseSizePair(r.BlockIO)
	u.PIDs, _ = strconv.Atoi(r.PIDs)
	return u
}

var statsArgs = []string{"stats", "--no-trunc", "--format", "{{json .}}"}

// StatsOnce samples every running container once. Blocks ~2s: docker
// needs a CPU sampling window. Callers computing host CPU deltas can use
// that window by sampling before and after this call.
func (c *Client) StatsOnce(ctx context.Context) ([]ContainerUsage, error) {
	args := append([]string{statsArgs[0], "--no-stream"}, statsArgs[1:]...)
	var buf bytes.Buffer
	if err := c.runner.Run(ctx, args, nil, &buf, nil); err != nil {
		return nil, err
	}
	var out []ContainerUsage
	forEachStatsRow(&buf, func(r statsRow) {
		out = append(out, r.usage())
	})
	return out, nil
}

// StatsStream runs `docker stats` in follow mode. The child process is
// bound to ctx — cancelling it (the SSE client disconnecting) kills the
// child; nothing lingers on the host. Snapshot returns the most recent
// emit per container, on the caller's cadence rather than docker's.
type StatsStream struct {
	mu     sync.Mutex
	latest map[string]streamEntry
	done   chan struct{}
	now    func() time.Time // injectable for eviction tests
}

type streamEntry struct {
	usage ContainerUsage
	seen  time.Time
}

// streamMaxAge evicts containers docker stopped emitting (they exited or
// were reaped by a deploy). Docker refreshes roughly every second, so a
// row this stale is gone, not slow.
const streamMaxAge = 15 * time.Second

func (c *Client) StatsStream(ctx context.Context) *StatsStream {
	s := &StatsStream{
		latest: map[string]streamEntry{},
		done:   make(chan struct{}),
		now:    time.Now,
	}
	pr, pw := io.Pipe()
	go func() {
		err := c.runner.Run(ctx, statsArgs, nil, pw, nil)
		pw.CloseWithError(err) // nil err → clean EOF for the consumer
	}()
	go func() {
		defer close(s.done)
		forEachStatsRow(pr, func(r statsRow) {
			s.mu.Lock()
			s.latest[r.ID] = streamEntry{usage: r.usage(), seen: s.now()}
			s.mu.Unlock()
		})
	}()
	return s
}

// Snapshot returns the latest usage per still-reporting container,
// evicting anything not seen within streamMaxAge.
func (s *StatsStream) Snapshot() []ContainerUsage {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := s.now().Add(-streamMaxAge)
	out := make([]ContainerUsage, 0, len(s.latest))
	for id, e := range s.latest {
		if e.seen.Before(cutoff) {
			delete(s.latest, id)
			continue
		}
		out = append(out, e.usage)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Done closes when the underlying docker process has exited and every
// buffered row has been consumed.
func (s *StatsStream) Done() <-chan struct{} { return s.done }

// forEachStatsRow scans line-delimited JSON emits, tolerating cursor-
// control bytes some docker versions prepend to refresh batches (anything
// before the first '{' is discarded) and skipping unparseable lines.
func forEachStatsRow(r io.Reader, fn func(statsRow)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if i := bytes.IndexByte(line, '{'); i >= 0 {
			line = line[i:]
		} else {
			continue
		}
		var row statsRow
		if err := json.Unmarshal(line, &row); err != nil || row.ID == "" {
			continue
		}
		fn(row)
	}
}

// ContainerOwner attributes a container to the cobalt project/service
// that deployed it. Zero Project means cobalt doesn't own it.
type ContainerOwner struct {
	Project, Service string
	Deployment, Slot int
}

// psOwnerFormat pulls exactly the labels ownership needs, tab-separated —
// {{.Label}} avoids parsing docker's comma-joined Labels blob, which is
// ambiguous the moment a value contains a comma.
const psOwnerFormat = `{{.ID}}\t{{.Names}}\t` +
	`{{.Label "` + LabelProjectName + `"}}\t{{.Label "` + LabelServiceName + `"}}\t` +
	`{{.Label "` + LabelDeploymentNumber + `"}}\t{{.Label "` + swarmServiceLabel + `"}}`

// ContainerOwners resolves ownership for every running container.
// knownProjects is the daemon's project list, needed to split swarm task
// names like "white-label-114-web" unambiguously (project names may
// themselves contain hyphens).
func (c *Client) ContainerOwners(ctx context.Context, knownProjects []string) (map[string]ContainerOwner, error) {
	var buf bytes.Buffer
	args := []string{"ps", "--no-trunc", "--format", psOwnerFormat}
	if err := c.runner.Run(ctx, args, nil, &buf, nil); err != nil {
		return nil, fmt.Errorf("docker ps for stats attribution: %w", err)
	}
	out := map[string]ContainerOwner{}
	for line := range strings.SplitSeq(buf.String(), "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 6 || f[0] == "" {
			continue
		}
		id, name, project, service, deployment, swarmSvc := f[0], f[1], f[2], f[3], f[4], f[5]
		out[id] = resolveOwner(name, project, service, deployment, swarmSvc, knownProjects)
	}
	return out, nil
}

// resolveOwner prefers cobalt's own container labels (one-off run
// containers always have them; service tasks deployed after the
// --container-label change do too), falling back to parsing the
// swarm-injected service-name label for older tasks.
func resolveOwner(name, project, service, deployment, swarmSvc string, knownProjects []string) ContainerOwner {
	o := ContainerOwner{Project: project, Service: service, Slot: taskSlot(name)}
	o.Deployment, _ = strconv.Atoi(deployment)
	if o.Project != "" {
		return o
	}
	if swarmSvc == "" {
		return ContainerOwner{}
	}
	// Longest known project name first: "white-label-114-web" must match
	// project "white-label", not a hypothetical project "white".
	sorted := append([]string(nil), knownProjects...)
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	for _, p := range sorted {
		rest, ok := strings.CutPrefix(swarmSvc, p+"-")
		if !ok {
			continue
		}
		numStr, svc, ok := strings.Cut(rest, "-")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		return ContainerOwner{Project: p, Service: svc, Deployment: n, Slot: o.Slot}
	}
	return ContainerOwner{}
}

// taskSlot extracts the 1-based replica index from a swarm task container
// name ("api-114-web.3.pz1x…" → 3). 0 for anything else.
func taskSlot(name string) int {
	parts := strings.Split(name, ".")
	if len(parts) < 3 {
		return 0
	}
	n, _ := strconv.Atoi(parts[1])
	return n
}

// parsePercent reads docker's "3.05%" strings; "--", "", and garbage
// degrade to 0.
func parsePercent(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	if err != nil {
		return 0
	}
	return v
}

// sizeUnits covers every suffix docker's go-units emits: decimal
// (HumanSize: net/block IO) and binary (BytesSize: memory).
var sizeUnits = map[string]float64{
	"B":  1,
	"kB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12, "PB": 1e15,
	"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40, "PiB": 1 << 50,
}

// parseSize reads one docker-formatted size ("532.5MiB", "1.2kB", "0B").
// Unknown units and garbage degrade to 0.
func parseSize(s string) uint64 {
	s = strings.TrimSpace(s)
	i := strings.IndexFunc(s, func(r rune) bool {
		return (r < '0' || r > '9') && r != '.'
	})
	if i <= 0 {
		return 0
	}
	num, err := strconv.ParseFloat(s[:i], 64)
	if err != nil {
		return 0
	}
	unit, ok := sizeUnits[strings.TrimSpace(s[i:])]
	if !ok {
		return 0
	}
	return uint64(num * unit)
}

// parseSizePair splits docker's "used / limit" columns.
func parseSizePair(s string) (uint64, uint64) {
	a, b, ok := strings.Cut(s, "/")
	if !ok {
		return parseSize(s), 0
	}
	return parseSize(a), parseSize(b)
}

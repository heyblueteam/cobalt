package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// serviceRow is one table line group: a cobalt service aggregated across
// its replicas, or a single foreign container under project "other".
type serviceRow struct {
	Project, Service  string
	CPU               float64
	MemUsed, MemLimit uint64
	NetRx, NetTx      uint64
	Containers        []cobaltapi.ContainerStats
}

func (r serviceRow) key() string { return r.Project + "/" + r.Service }

// otherProject groups containers cobalt doesn't manage. They still
// count: the view must explain 100% of host pressure.
const otherProject = "other"

// buildRows aggregates containers into per-service rows: CPU/mem/net sum
// across replicas; MemLimit stays the per-replica limit (replicas share
// one limit — summing it would suggest headroom that isn't there).
// Sorted by CPU (or memory) descending, keeping each project's rows
// contiguous, hottest project first, "other" always last.
func buildRows(snap cobaltapi.ServerStats, byMem bool) []serviceRow {
	byKey := map[string]*serviceRow{}
	var order []string
	for _, c := range snap.Containers {
		project, service := c.Project, c.Service
		if project == "" {
			project, service = otherProject, c.Name
		}
		key := project + "/" + service
		row := byKey[key]
		if row == nil {
			row = &serviceRow{Project: project, Service: service}
			byKey[key] = row
			order = append(order, key)
		}
		row.CPU += c.CPUPercent
		row.MemUsed += c.MemUsedBytes
		row.MemLimit = max(row.MemLimit, c.MemLimitBytes)
		row.NetRx += c.NetRxBytes
		row.NetTx += c.NetTxBytes
		row.Containers = append(row.Containers, c)
	}

	rows := make([]serviceRow, 0, len(byKey))
	for _, k := range order {
		r := *byKey[k]
		sort.Slice(r.Containers, func(i, j int) bool { return r.Containers[i].Slot < r.Containers[j].Slot })
		rows = append(rows, r)
	}

	metric := func(r serviceRow) float64 {
		if byMem {
			return float64(r.MemUsed)
		}
		return r.CPU
	}
	hottest := map[string]float64{}
	for _, r := range rows {
		hottest[r.Project] = max(hottest[r.Project], metric(r))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Project != b.Project {
			if (a.Project == otherProject) != (b.Project == otherProject) {
				return b.Project == otherProject
			}
			if hottest[a.Project] != hottest[b.Project] {
				return hottest[a.Project] > hottest[b.Project]
			}
			return a.Project < b.Project
		}
		return metric(a) > metric(b)
	})
	return rows
}

// filterRows keeps only the given project's rows ("" keeps everything).
func filterRows(rows []serviceRow, project string) []serviceRow {
	if project == "" {
		return rows
	}
	out := rows[:0:0]
	for _, r := range rows {
		if r.Project == project {
			out = append(out, r)
		}
	}
	return out
}

// projectsIn lists the distinct projects present, in row order — the `p`
// key cycles through these.
func projectsIn(rows []serviceRow) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range rows {
		if !seen[r.Project] {
			seen[r.Project] = true
			out = append(out, r.Project)
		}
	}
	return out
}

// humanBytes renders sizes the way a dashboard wants them: short.
// "1.5G", "532M", "23K", "0B".
func humanBytes(b uint64) string {
	format := func(v float64, unit string) string {
		s := strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0")
		return s + unit
	}
	switch {
	case b >= 1<<30:
		return format(float64(b)/(1<<30), "G")
	case b >= 1<<20:
		return format(float64(b)/(1<<20), "M")
	case b >= 1<<10:
		return format(float64(b)/(1<<10), "K")
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// fmtUptime renders host uptime at the two most significant units:
// "41d 3h", "3h 12m", "12m".
func fmtUptime(sec int64) string {
	d, h, m := sec/86400, sec%86400/3600, sec%3600/60
	switch {
	case d > 0:
		return fmt.Sprintf("%dd %dh", d, h)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	default:
		return fmt.Sprintf("%dm", m)
	}
}

// gauge renders a fixed-width usage bar. frac is clamped to [0,1].
func gauge(frac float64, width int) string {
	frac = min(max(frac, 0), 1)
	filled := int(frac*float64(width) + 0.5)
	return strings.Repeat("▓", filled) + strings.Repeat("░", width-filled)
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

// sparkline renders the last `width` values scaled against their own
// maximum — shape over absolute value, like every other sparkline.
func sparkline(vals []float64, width int) string {
	if len(vals) > width {
		vals = vals[len(vals)-width:]
	}
	peak := 0.0
	for _, v := range vals {
		peak = max(peak, v)
	}
	if peak == 0 {
		peak = 1
	}
	var b strings.Builder
	for _, v := range vals {
		i := int(v / peak * float64(len(sparkRunes)-1))
		b.WriteRune(sparkRunes[min(max(i, 0), len(sparkRunes)-1)])
	}
	return b.String()
}

var (
	stBold = lipgloss.NewStyle().Bold(true)
	stDim  = lipgloss.NewStyle().Faint(true)
	stLive = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	stWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	stHot  = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	stProj = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
)

// gaugeStyle colors a bar by how worried the reader should be.
func gaugeStyle(frac float64) lipgloss.Style {
	switch {
	case frac >= 0.9:
		return stHot
	case frac >= 0.7:
		return stWarn
	default:
		return stLive
	}
}

// renderHeader is the two host lines: identity + load + uptime + link
// state, then the CPU/MEM/DISK gauges.
func renderHeader(snap cobaltapi.ServerStats, status string) string {
	var b strings.Builder
	fmt.Fprintf(&b, " %s   load %.2f %.2f %.2f   up %s",
		stBold.Render(snap.Node),
		snap.System.Load1, snap.System.Load5, snap.System.Load15,
		fmtUptime(snap.System.UptimeSeconds))
	switch status {
	case "live":
		fmt.Fprintf(&b, "   %s", stLive.Render("● live"))
	case "":
	default:
		fmt.Fprintf(&b, "   %s", stWarn.Render("● "+status))
	}
	b.WriteString("\n ")

	cpuFrac := snap.System.CPUPercent / 100
	fmt.Fprintf(&b, "CPU %s %3.0f%%", gaugeStyle(cpuFrac).Render(gauge(cpuFrac, 10)), snap.System.CPUPercent)

	memFrac := 0.0
	if snap.System.MemTotalBytes > 0 {
		memFrac = float64(snap.System.MemUsedBytes) / float64(snap.System.MemTotalBytes)
	}
	fmt.Fprintf(&b, "   MEM %s %s/%s", gaugeStyle(memFrac).Render(gauge(memFrac, 10)),
		humanBytes(snap.System.MemUsedBytes), humanBytes(snap.System.MemTotalBytes))

	if snap.System.SwapUsedBytes > 0 {
		fmt.Fprintf(&b, "   SWAP %s", humanBytes(snap.System.SwapUsedBytes))
	}
	for _, d := range snap.System.Disks {
		frac := 0.0
		if d.TotalBytes > 0 {
			frac = float64(d.UsedBytes) / float64(d.TotalBytes)
		}
		fmt.Fprintf(&b, "   DISK %s %.0f%%", d.Mount, frac*100)
	}
	return b.String()
}

const histWidth = 12

// renderTable is the container table. history (keyed by serviceRow.key)
// feeds the sparkline column; nil means no history (one-shot mode).
//
// Cells are padded as plain text BEFORE styling — lipgloss escape codes
// have byte length but no display width, so styling first breaks %-Ns
// column alignment.
func renderTable(rows []serviceRow, history map[string][]float64, showReplicas bool) string {
	var b strings.Builder
	b.WriteString(" " + stDim.Render(fmt.Sprintf("%-12s %-16s %7s  %-13s %-13s %s",
		"PROJECT", "SERVICE", "CPU%", "MEM", "NET RX/TX", "HISTORY")) + "\n")

	lastProject := ""
	for _, r := range rows {
		project := ""
		if r.Project != lastProject {
			project = r.Project
			lastProject = r.Project
		}
		service := r.Service
		if len(r.Containers) > 1 {
			service = fmt.Sprintf("%s ×%d", r.Service, len(r.Containers))
		}
		mem := humanBytes(r.MemUsed)
		if r.MemLimit > 0 {
			mem += " / " + humanBytes(r.MemLimit)
		}
		fmt.Fprintf(&b, " %s %-16s %7.1f  %-13s %-13s %s\n",
			stProj.Render(pad(truncate(project, 12), 12)),
			truncate(service, 16), r.CPU, mem,
			humanBytes(r.NetRx)+"/"+humanBytes(r.NetTx),
			sparkline(history[r.key()], histWidth))

		if showReplicas && len(r.Containers) > 1 {
			for _, c := range r.Containers {
				slot := fmt.Sprintf(".%d", c.Slot)
				if c.Slot == 0 {
					slot = truncate(c.Name, 14)
				}
				fmt.Fprintf(&b, " %-12s %s %7.1f  %s\n", "",
					stDim.Render(pad("  "+slot, 16)), c.CPUPercent, humanBytes(c.MemUsedBytes))
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// pad right-pads plain (unstyled) text to n display columns.
func pad(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/heyblueteam/cobalt/internal/client"
	"github.com/heyblueteam/cobalt/internal/output"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/spf13/cobra"
)

func newStatsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Live server & container resource usage",
		Long: `Shows a live dashboard of host-level CPU / memory / load / disk and
per-container usage, grouped by project and service — including each
replica of a scaled service.

In a terminal this opens a full-screen view updating every ~2s:
  q quit · c/m sort by CPU/memory · r toggle replica rows · p cycle project

Piped, or with --once / --json, it prints a single snapshot instead.

Examples:
  cobalt stats
  cobalt stats --project api
  cobalt stats --once
  cobalt stats --json | jq '.containers[] | select(.project=="api")'`,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			once, _ := cmd.Flags().GetBool("once")
			project, _ := cmd.Flags().GetString("project")
			c, err := newClient(cmd)
			if err != nil {
				return err
			}
			jsonMode := cmd.Flag("json").Value.String() == "true"
			if once || jsonMode || !output.IsStdoutTTY() {
				return statsOnce(cmd.Context(), c, project, jsonMode)
			}
			return statsTUI(cmd.Context(), c, project)
		}),
	}
	cmd.Flags().Bool("once", false, "print one snapshot and exit")
	cmd.Flags().String("project", "", "only show containers for this project")
	return cmd
}

// statsOnce prints a single snapshot — the scriptable path.
func statsOnce(ctx context.Context, c *client.Client, project string, asJSON bool) error {
	snap, err := c.ServerStats(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		output.PrintJSON(snap)
		return nil
	}
	rows := filterRows(buildRows(snap, false), project)
	fmt.Fprintln(output.Stdout, renderHeader(snap, ""))
	fmt.Fprintln(output.Stdout)
	fmt.Fprintln(output.Stdout, renderTable(rows, nil, true))
	return nil
}

// --- TUI ---

// Messages flowing into the model. The SSE reader goroutine feeds a
// channel; waitMsg turns "next channel item" into a tea.Cmd, re-armed
// after every receive (the standard bubbletea streaming pattern).
type (
	connectedMsg struct{ ch chan tea.Msg }
	snapMsg      cobaltapi.ServerStats
	streamEndMsg struct {
		err  error
		code int // HTTP status of a failed connect; 0 when the stream died some other way
	}
	retryMsg     struct{}
	staleTickMsg struct{}
)

// reconnectDelay paces retry after the stream drops — matches the
// snapshot cadence, so a daemon redeploy reconnects within a beat.
const reconnectDelay = 2 * time.Second

// historyCap bounds per-service CPU history. At one sample per 2s this
// is four minutes of context — plenty for "did that spike just start".
const historyCap = 120

// staleAfter flips "live" to "stalled" when no snapshot has arrived for
// three intervals. A hung connection (daemon wedged, network blackhole)
// never delivers a streamEndMsg, so without a watchdog the view shows
// "live" over frozen numbers indefinitely.
const staleAfter = 3 * reconnectDelay

type statsModel struct {
	ctx     context.Context
	client  *client.Client
	filter  string
	byMem   bool
	hideRep bool

	snap       *cobaltapi.ServerStats
	history    map[string][]float64
	ch         chan tea.Msg
	status     string    // "live", "stalled", "connecting…", "reconnecting…"
	lastSnapAt time.Time // when the last snapshot arrived; drives the stalled check
	lastErr    error     // why the last stream attempt ended; shown while reconnecting
	err        error     // fatal: quits the program and is reported to the user
	width      int
	height     int
}

func statsTUI(ctx context.Context, c *client.Client, project string) error {
	m := statsModel{
		ctx:     ctx,
		client:  c,
		filter:  project,
		history: map[string][]float64{},
		status:  "connecting…",
	}
	final, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
		return nil
	}
	if err != nil {
		return err
	}
	if fm, ok := final.(statsModel); ok {
		return fm.err
	}
	return nil
}

func (m statsModel) Init() tea.Cmd { return tea.Batch(m.connect(), staleTick()) }

// staleTick drives the stalled-stream watchdog, re-armed on every tick.
func staleTick() tea.Cmd {
	return tea.Tick(reconnectDelay, func(time.Time) tea.Msg { return staleTickMsg{} })
}

// connect opens the SSE stream and hands its channel to the model. The
// reader goroutine lives until the stream ends or ctx is cancelled.
func (m statsModel) connect() tea.Cmd {
	ctx, c := m.ctx, m.client
	return func() tea.Msg {
		resp, err := c.ServerStatsSSE(ctx)
		if err != nil {
			return streamEndMsg{err: err}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return streamEndMsg{
				err:  fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body))),
				code: resp.StatusCode,
			}
		}
		ch := make(chan tea.Msg, 4)
		go func() {
			defer resp.Body.Close()
			_, err := output.ConsumeSSEEvents(ctx, resp.Body, func(_, data string) error {
				var s cobaltapi.ServerStats
				if jsonErr := json.Unmarshal([]byte(data), &s); jsonErr == nil {
					ch <- snapMsg(s)
				}
				return nil
			})
			ch <- streamEndMsg{err: err}
		}()
		return connectedMsg{ch}
	}
}

func waitMsg(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

func (m statsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "c":
			m.byMem = false
		case "m":
			m.byMem = true
		case "r":
			m.hideRep = !m.hideRep
		case "p":
			m.filter = m.nextFilter()
		}
		return m, nil

	case connectedMsg:
		m.ch = msg.ch
		m.status = "live"
		return m, waitMsg(m.ch)

	case snapMsg:
		s := cobaltapi.ServerStats(msg)
		m.snap = &s
		m.status = "live"
		m.lastSnapAt = time.Now()
		m.lastErr = nil
		// History tracks the full snapshot, not the filtered view, so
		// cycling filters doesn't restart the sparklines.
		for _, r := range buildRows(s, false) {
			key := r.key()
			h := m.history[key]
			h = append(h, r.CPU)
			if len(h) > historyCap {
				h = h[len(h)-historyCap:]
			}
			m.history[key] = h
		}
		return m, waitMsg(m.ch)

	case streamEndMsg:
		switch msg.code {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			// Retrying can't fix a bad API key or a daemon without the
			// endpoint (pre-stats version) — quit and report why.
			m.err = msg.err
			return m, tea.Quit
		}
		m.lastErr = msg.err
		m.status = "reconnecting…"
		return m, tea.Tick(reconnectDelay, func(time.Time) tea.Msg { return retryMsg{} })

	case retryMsg:
		return m, m.connect()

	case staleTickMsg:
		// Only demote a live view: connecting/reconnecting already say
		// the stream is down, and the next snapshot restores "live".
		if m.status == "live" && !m.lastSnapAt.IsZero() && time.Since(m.lastSnapAt) > staleAfter {
			m.status = "stalled"
		}
		return m, staleTick()
	}
	return m, nil
}

// nextFilter cycles all → project₁ → project₂ → … → all.
func (m statsModel) nextFilter() string {
	if m.snap == nil {
		return ""
	}
	projects := projectsIn(buildRows(*m.snap, m.byMem))
	if m.filter == "" {
		if len(projects) == 0 {
			return ""
		}
		return projects[0]
	}
	for i, p := range projects {
		if p == m.filter && i+1 < len(projects) {
			return projects[i+1]
		}
	}
	return ""
}

func (m statsModel) View() string {
	// The connection state, with the reason when the stream is down —
	// "reconnecting…" alone tells the user nothing actionable.
	status := m.status
	if m.lastErr != nil && status != "live" {
		status += " — " + m.lastErr.Error()
	}
	if m.snap == nil {
		return "\n  " + stDim.Render(status)
	}
	rows := filterRows(buildRows(*m.snap, m.byMem), m.filter)

	help := "q quit · c/m sort · r replicas · p filter"
	if m.filter != "" {
		help += " (" + m.filter + ")"
	}
	return renderHeader(*m.snap, status) + "\n\n" +
		renderTable(rows, m.history, !m.hideRep) + "\n\n " +
		stDim.Render(help) + "\n"
}

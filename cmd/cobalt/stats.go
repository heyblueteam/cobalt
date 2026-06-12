package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	streamEndMsg struct{ err error }
	retryMsg     struct{}
)

// reconnectDelay paces retry after the stream drops — matches the
// snapshot cadence, so a daemon redeploy reconnects within a beat.
const reconnectDelay = 2 * time.Second

// historyCap bounds per-service CPU history. At one sample per 2s this
// is four minutes of context — plenty for "did that spike just start".
const historyCap = 120

type statsModel struct {
	ctx     context.Context
	client  *client.Client
	filter  string
	byMem   bool
	hideRep bool

	snap    *cobaltapi.ServerStats
	history map[string][]float64
	ch      chan tea.Msg
	status  string // "live", "connecting…", "reconnecting…"
	width   int
	height  int
}

func statsTUI(ctx context.Context, c *client.Client, project string) error {
	m := statsModel{
		ctx:     ctx,
		client:  c,
		filter:  project,
		history: map[string][]float64{},
		status:  "connecting…",
	}
	_, err := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

func (m statsModel) Init() tea.Cmd { return m.connect() }

// connect opens the SSE stream and hands its channel to the model. The
// reader goroutine lives until the stream ends or ctx is cancelled.
func (m statsModel) connect() tea.Cmd {
	ctx, c := m.ctx, m.client
	return func() tea.Msg {
		resp, err := c.ServerStatsSSE(ctx)
		if err != nil {
			return streamEndMsg{err}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return streamEndMsg{fmt.Errorf("%s: %s", resp.Status, string(body))}
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
			ch <- streamEndMsg{err}
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
		// History tracks the full snapshot, not the filtered view, so
		// cycling filters doesn't restart the sparklines.
		for _, r := range buildRows(s, false) {
			h := append(m.history[r.key()], r.CPU)
			if len(h) > historyCap {
				h = h[len(h)-historyCap:]
			}
			m.history[r.key()] = h
		}
		return m, waitMsg(m.ch)

	case streamEndMsg:
		m.status = "reconnecting…"
		return m, tea.Tick(reconnectDelay, func(time.Time) tea.Msg { return retryMsg{} })

	case retryMsg:
		return m, m.connect()
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
	if m.snap == nil {
		return "\n  " + stDim.Render(m.status)
	}
	rows := filterRows(buildRows(*m.snap, m.byMem), m.filter)

	help := "q quit · c/m sort · r replicas · p filter"
	if m.filter != "" {
		help += " (" + m.filter + ")"
	}
	return renderHeader(*m.snap, m.status) + "\n\n" +
		renderTable(rows, m.history, !m.hideRep) + "\n\n " +
		stDim.Render(help) + "\n"
}

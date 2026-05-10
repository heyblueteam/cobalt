package output

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Categorical step icons reused across cobalt commands so the visual
// vocabulary stays consistent. Operators scan the left edge of the
// output to track progress; reusing the same emoji for the same kind
// of work across commands turns the column into a small UI.
const (
	IconSSH        = "🔌"
	IconDetect     = "🔍"
	IconPublicHost = "🌐"
	IconTLS        = "🔒"
	IconDocker     = "🐳"
	IconSwarm      = "🐝"
	IconNetwork    = "🕸 "
	IconSecret     = "🔑"
	IconWriting    = "📝"
	IconDeploy     = "🚀"
	IconHealth     = "💚"
	IconAPIKey     = "🎟 "
	IconSave       = "💾"
	IconGitHub     = "🦊"
	IconBrowser    = "🌍"
	IconDone       = "🎉"
)

// stepWidth is the column at which the per-step status mark (✓/✗)
// lands. Picked to fit both the longest cobalt init step label and a
// standard 80-col terminal with room to spare.
const stepWidth = 70

// Step is a single emoji-prefixed progress line. Construct with
// StartStep, then call OK / Fail / Skip exactly once to terminate it.
//
// Width math assumes the icon is two visible columns wide (true for
// every emoji in the categorical set above). The step's label is
// rune-counted, which slightly over-counts for the few labels with
// double-width chars but is fine in practice — the worst case is the
// status mark drifting one column inside the right margin.
type Step struct {
	silent bool
	cols   int
}

// StartStep prints "<icon> <label>..." to stderr and returns a Step
// handle whose OK / Fail / Skip method will print the trailing status
// mark right-aligned to stepWidth.
//
// Suppressed under JSON mode (returns a no-op handle) so machine
// consumers don't see decorative progress noise.
func StartStep(icon, label string) *Step {
	if jsonMode {
		return &Step{silent: true}
	}
	fmt.Fprintf(Stderr, "%s %s...", icon, label)
	cols := 2 + 1 + utf8.RuneCountInString(label) + 3
	return &Step{cols: cols}
}

// OK terminates the step with a green checkmark.
func (s *Step) OK() {
	if s == nil || s.silent {
		return
	}
	s.finish("✓")
}

// Fail terminates the step with a red X. If reason is non-empty, it
// is printed indented on the next line so the operator sees the cause
// without having to scroll.
func (s *Step) Fail(reason string) {
	if s == nil || s.silent {
		return
	}
	s.finish("✗")
	if reason != "" {
		fmt.Fprintf(Stderr, "   %s\n", reason)
	}
}

// Skip terminates the step inline with a short reason ("Docker
// already installed", etc.) instead of a status mark. Used when the
// step is a no-op rather than a success.
func (s *Step) Skip(reason string) {
	if s == nil || s.silent {
		return
	}
	if reason == "" {
		reason = "skipped"
	}
	fmt.Fprintf(Stderr, " %s\n", reason)
}

// Detail prints an indented note line under a step that has *not* yet
// terminated. Useful for the detection summary block where the 🔍
// step header is followed by 4 indented "key: value" rows.
func (s *Step) Detail(key, value string) {
	if s == nil || s.silent {
		return
	}
	fmt.Fprintf(Stderr, "\n    %s %s", key, value)
}

func (s *Step) finish(mark string) {
	pad := stepWidth - s.cols
	if pad < 1 {
		pad = 1
	}
	fmt.Fprintf(Stderr, "%s%s\n", strings.Repeat(" ", pad), mark)
}

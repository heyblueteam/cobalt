// Package notify renders and sends deploy notifications. Providers are
// best-effort: a notification failure is logged and dropped, never returned
// to the deployment path.
package notify

import "time"

// Event is the data a deploy notification can render. Every string field is
// HTML-escaped before it reaches a template; templates control layout only.
type Event struct {
	Status     string
	Project    string
	Number     int
	Repo       string
	Branch     string
	CommitSHA  string
	ShortSHA   string
	CommitURL  string
	PRNumber   int
	PRURL      string
	Duration   time.Duration
	StartedAt  time.Time
	FinishedAt time.Time
	Host       string
	Domains    []string
	Error      string
}

// DurationText is the wall clock rounded to seconds, for templates.
func (e Event) DurationText() string {
	return e.Duration.Round(time.Second).String()
}

// Config is the resolved per-deployment notification settings. TemplateFile
// is an absolute path already confined to the project root. Empty means the
// built-in default template.
type Config struct {
	Chat         string
	Events       []string
	TemplateFile string
}

// eventAllowed reports whether status is in events. An empty list allows
// nothing; cobaltfile defaults fill it before it reaches here.
func eventAllowed(events []string, status string) bool {
	for _, event := range events {
		if event == status {
			return true
		}
	}
	return false
}

package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/template"
	"time"
	"unicode/utf8"
)

// DefaultAPIBase is Telegram's public Bot API. Tests override it.
const DefaultAPIBase = "https://api.telegram.org"

// maxMessageBytes is slightly below Telegram's 4096-byte limit so an
// escaped entity at the boundary cannot push us over.
const maxMessageBytes = 4000

// DefaultTemplate is used when a project does not set templateFile. It is
// deliberately generic; Blue-specific wording belongs in a repo template.
const DefaultTemplate = `{{if eq .Status "success"}}✅{{else if eq .Status "failure"}}❌{{else}}ℹ️{{end}} Deploy {{.Status}} · {{.Project}} (#{{.Number}})
{{.Repo}} · {{.Branch}} @ {{.ShortSHA}}
{{if .PRNumber}}<a href="{{.PRURL}}">PR #{{.PRNumber}}</a> · {{end}}<a href="{{.CommitURL}}">{{.ShortSHA}}</a>
{{if .Error}}{{.Error}}
{{end}}{{.DurationText}}{{if .Host}} · {{.Host}}{{end}}`

// Telegram sends deploy messages through the Telegram Bot API.
type Telegram struct {
	Token       string
	DefaultChat string
	APIBase     string
	Client      *http.Client
}

// Notify renders and sends one message. It returns nil when the event is not
// subscribed or when the project has no chat. Errors are for logging only.
func (t *Telegram) Notify(ctx context.Context, cfg Config, ev Event) error {
	if !eventAllowed(cfg.Events, ev.Status) {
		return nil
	}
	chat := strings.TrimSpace(cfg.Chat)
	if chat == "" {
		chat = strings.TrimSpace(t.DefaultChat)
	}
	if chat == "" {
		return fmt.Errorf("notify: telegram chat is empty for project %q", ev.Project)
	}
	body, err := t.render(cfg, ev)
	if err != nil {
		return err
	}
	return t.send(ctx, chat, body)
}

func (t *Telegram) render(cfg Config, ev Event) (string, error) {
	src := DefaultTemplate
	if cfg.TemplateFile != "" {
		raw, err := os.ReadFile(cfg.TemplateFile)
		if err != nil {
			return "", fmt.Errorf("notify: read template: %w", err)
		}
		src = string(raw)
	}
	tmpl, err := template.New("telegram").Funcs(template.FuncMap{
		"tz": func(zone string, v time.Time) time.Time {
			loc, locErr := time.LoadLocation(zone)
			if locErr != nil {
				return v
			}
			return v.In(loc)
		},
		"join": strings.Join,
	}).Parse(src)
	if err != nil {
		return "", fmt.Errorf("notify: parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, escapeEvent(ev)); err != nil {
		return "", fmt.Errorf("notify: render template: %w", err)
	}
	return truncateUTF8(buf.String(), maxMessageBytes), nil
}

// escapeEvent returns a copy whose string values are safe to place inside
// Telegram HTML. Status is a fixed enum and stays untouched.
func escapeEvent(ev Event) Event {
	out := ev
	out.Project = html.EscapeString(ev.Project)
	out.Repo = html.EscapeString(ev.Repo)
	out.Branch = html.EscapeString(ev.Branch)
	out.CommitSHA = html.EscapeString(ev.CommitSHA)
	out.ShortSHA = html.EscapeString(ev.ShortSHA)
	out.CommitURL = html.EscapeString(ev.CommitURL)
	out.PRURL = html.EscapeString(ev.PRURL)
	out.Host = html.EscapeString(ev.Host)
	out.Error = html.EscapeString(ev.Error)
	out.Domains = make([]string, len(ev.Domains))
	for i, domain := range ev.Domains {
		out.Domains[i] = html.EscapeString(domain)
	}
	return out
}

func (t *Telegram) send(ctx context.Context, chat, text string) error {
	base := t.APIBase
	if base == "" {
		base = DefaultAPIBase
	}
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(base, "/"), t.Token)
	form := url.Values{
		"chat_id":                  {chat},
		"text":                     {text},
		"parse_mode":               {"HTML"},
		"disable_web_page_preview": {"true"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("notify: telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := t.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		// The token is in the URL. Strip the *url.Error wrapper so the
		// token never reaches a log line.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("notify: telegram send: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("notify: telegram status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// truncateUTF8 cuts s to at most max bytes without splitting a rune.
func truncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[:max]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

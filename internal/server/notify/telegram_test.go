package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type capturedMessage struct {
	chat string
	text string
}

// telegramTestServer records the form values Telegram would receive.
func telegramTestServer(t *testing.T) (*httptest.Server, *[]capturedMessage) {
	t.Helper()
	var messages []capturedMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		messages = append(messages, capturedMessage{
			chat: r.FormValue("chat_id"),
			text: r.FormValue("text"),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &messages
}

func newTestTelegram(srv *httptest.Server) *Telegram {
	return &Telegram{
		Token:   "test-token",
		APIBase: srv.URL,
		Client:  srv.Client(),
	}
}

func baseEvent() Event {
	return Event{
		Status:     "success",
		Project:    "api",
		Number:     4821,
		Repo:       "heyblueteam/blue",
		Branch:     "main",
		CommitSHA:  "4f2c9a1b2c3d4e5f60718293a4b5c6d7e8f90123",
		ShortSHA:   "4f2c9a1",
		CommitURL:  "https://github.com/heyblueteam/blue/commit/4f2c9a1b2c3d4e5f60718293a4b5c6d7e8f90123",
		PRNumber:   4310,
		PRURL:      "https://github.com/heyblueteam/blue/pull/4310",
		Duration:   6*time.Minute + 12*time.Second,
		StartedAt:  time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 10, 29, 12, 6, 12, 0, time.UTC),
		Host:       "server.blue.cc",
	}
}

func TestTelegram_RendersSuccessWithPullRequest(t *testing.T) {
	t.Parallel()
	srv, messages := telegramTestServer(t)
	tg := newTestTelegram(srv)

	err := tg.Notify(context.Background(), Config{
		Chat:   "-100123",
		Events: []string{"success", "failure"},
	}, baseEvent())
	if err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(*messages) != 1 {
		t.Fatalf("messages: got %d, want 1", len(*messages))
	}
	got := (*messages)[0]
	if got.chat != "-100123" {
		t.Errorf("chat: got %q", got.chat)
	}
	for _, want := range []string{
		"Deploy success",
		"api (#4821)",
		"heyblueteam/blue · main @ 4f2c9a1",
		`<a href="https://github.com/heyblueteam/blue/pull/4310">PR #4310</a>`,
		"6m12s",
		"server.blue.cc",
	} {
		if !strings.Contains(got.text, want) {
			t.Errorf("text missing %q:\n%s", want, got.text)
		}
	}
}

func TestTelegram_OmitsPullRequestWhenAbsent(t *testing.T) {
	t.Parallel()
	srv, messages := telegramTestServer(t)
	tg := newTestTelegram(srv)
	ev := baseEvent()
	ev.PRNumber = 0
	ev.PRURL = ""

	if err := tg.Notify(context.Background(), Config{Chat: "1", Events: []string{"success"}}, ev); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if strings.Contains((*messages)[0].text, "PR #") {
		t.Errorf("text should not mention a PR:\n%s", (*messages)[0].text)
	}
}

func TestTelegram_RendersFailure(t *testing.T) {
	t.Parallel()
	srv, messages := telegramTestServer(t)
	tg := newTestTelegram(srv)
	ev := baseEvent()
	ev.Status = "failure"
	ev.Error = "deploy: healthcheck timeout (web)"
	ev.PRNumber = 0
	ev.PRURL = ""

	if err := tg.Notify(context.Background(), Config{Chat: "1", Events: []string{"success", "failure"}}, ev); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	got := (*messages)[0].text
	if !strings.Contains(got, "Deploy failure") || !strings.Contains(got, "healthcheck timeout") {
		t.Errorf("failure text:\n%s", got)
	}
}

func TestTelegram_EscapesMessageData(t *testing.T) {
	t.Parallel()
	srv, messages := telegramTestServer(t)
	tg := newTestTelegram(srv)
	ev := baseEvent()
	ev.Project = `api<b>&"x"`
	ev.Error = `<script>alert(1)</script>`

	if err := tg.Notify(context.Background(), Config{Chat: "1", Events: []string{"success"}}, ev); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	got := (*messages)[0].text
	if strings.Contains(got, "<b>") || strings.Contains(got, "<script>") {
		t.Errorf("unescaped HTML in message:\n%s", got)
	}
	if !strings.Contains(got, "&lt;b&gt;") {
		t.Errorf("project not escaped:\n%s", got)
	}
}

func TestTelegram_UsesDefaultChat(t *testing.T) {
	t.Parallel()
	srv, messages := telegramTestServer(t)
	tg := newTestTelegram(srv)
	tg.DefaultChat = "-100default"

	if err := tg.Notify(context.Background(), Config{Chat: "", Events: []string{"success"}}, baseEvent()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if (*messages)[0].chat != "-100default" {
		t.Errorf("chat: got %q", (*messages)[0].chat)
	}
}

func TestTelegram_ErrorsWhenNoChat(t *testing.T) {
	t.Parallel()
	srv, _ := telegramTestServer(t)
	tg := newTestTelegram(srv)

	err := tg.Notify(context.Background(), Config{Chat: "", Events: []string{"success"}}, baseEvent())
	if err == nil {
		t.Fatal("expected an error when no chat resolves")
	}
}

func TestTelegram_FiltersUnsubscribedEvents(t *testing.T) {
	t.Parallel()
	srv, messages := telegramTestServer(t)
	tg := newTestTelegram(srv)

	if err := tg.Notify(context.Background(), Config{Chat: "1", Events: []string{"failure"}}, baseEvent()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if len(*messages) != 0 {
		t.Fatalf("messages: got %d, want 0", len(*messages))
	}
}

func TestTelegram_TemplateFileOverridesDefault(t *testing.T) {
	t.Parallel()
	srv, messages := telegramTestServer(t)
	tg := newTestTelegram(srv)
	dir := t.TempDir()
	path := filepath.Join(dir, "telegram.tmpl")
	if err := os.WriteFile(path, []byte(`custom {{.Project}} {{.Status}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := tg.Notify(context.Background(), Config{
		Chat: "1", Events: []string{"success"}, TemplateFile: path,
	}, baseEvent()); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := (*messages)[0].text; got != "custom api success" {
		t.Errorf("text: got %q", got)
	}
}

func TestTelegram_BadTemplateDoesNotSend(t *testing.T) {
	t.Parallel()
	srv, messages := telegramTestServer(t)
	tg := newTestTelegram(srv)
	dir := t.TempDir()
	path := filepath.Join(dir, "telegram.tmpl")
	if err := os.WriteFile(path, []byte(`{{.Project`), 0o644); err != nil {
		t.Fatal(err)
	}

	err := tg.Notify(context.Background(), Config{
		Chat: "1", Events: []string{"success"}, TemplateFile: path,
	}, baseEvent())
	if err == nil {
		t.Fatal("expected a template parse error")
	}
	if len(*messages) != 0 {
		t.Fatalf("messages: got %d, want 0", len(*messages))
	}
}

func TestTelegram_TruncatesLongMessages(t *testing.T) {
	t.Parallel()
	srv, messages := telegramTestServer(t)
	tg := newTestTelegram(srv)
	ev := baseEvent()
	ev.Error = strings.Repeat("é", 5000)

	if err := tg.Notify(context.Background(), Config{Chat: "1", Events: []string{"success"}}, ev); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	got := (*messages)[0].text
	if len(got) > maxMessageBytes {
		t.Errorf("len: got %d, want <= %d", len(got), maxMessageBytes)
	}
	if !utf8.ValidString(got) {
		t.Error("truncated message is not valid UTF-8")
	}
}

func TestTelegram_RetriesDoNotHappen(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, `{"ok":false}`, http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	tg := newTestTelegram(srv)

	if err := tg.Notify(context.Background(), Config{Chat: "1", Events: []string{"success"}}, baseEvent()); err == nil {
		t.Fatal("expected an error on 429")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls: got %d, want 1 (no retry)", calls.Load())
	}
}

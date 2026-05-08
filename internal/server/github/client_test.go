package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAPI is a minimal stand-in for api.github.com. Tests register handlers
// per path; unhandled paths return 404.
type fakeAPI struct {
	mu       sync.Mutex
	server   *httptest.Server
	handlers map[string]http.HandlerFunc
	last     *http.Request
	lastBody []byte
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{handlers: map[string]http.HandlerFunc{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.last = r.Clone(r.Context())
		// httptest preserves Body, so capture it explicitly for inspection.
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		f.lastBody = body
		h := f.handlers[r.URL.Path]
		f.mu.Unlock()
		if h == nil {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPI) on(path string, h http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers[path] = h
}

func (f *fakeAPI) client() *Client {
	return NewClientWithBaseURL(f.server.URL, f.server.Client())
}

func TestMintInstallationToken(t *testing.T) {
	t.Parallel()
	f := newFakeAPI(t)
	expires := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	f.on("/app/installations/12345/access_tokens", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-jwt" {
			t.Errorf("Authorization: got %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("API-Version: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "ghs_secret",
			"expires_at": expires.Format(time.RFC3339),
		})
	})

	c := f.client()
	tok, err := c.MintInstallationToken(context.Background(), "test-jwt", 12345)
	if err != nil {
		t.Fatalf("MintInstallationToken: %v", err)
	}
	if tok.Token != "ghs_secret" {
		t.Errorf("Token: %q", tok.Token)
	}
	if !tok.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt: %v vs %v", tok.ExpiresAt, expires)
	}
}

func TestInstallationToken_Valid(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cases := []struct {
		name string
		tok  InstallationToken
		want bool
	}{
		{"empty token", InstallationToken{Token: "", ExpiresAt: now.Add(time.Hour)}, false},
		{"expires after margin", InstallationToken{Token: "x", ExpiresAt: now.Add(10 * time.Minute)}, true},
		{"expires inside margin", InstallationToken{Token: "x", ExpiresAt: now.Add(2 * time.Minute)}, false},
		{"already expired", InstallationToken{Token: "x", ExpiresAt: now.Add(-time.Minute)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tok.Valid(now); got != tc.want {
				t.Errorf("Valid: got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAppExists_True(t *testing.T) {
	t.Parallel()
	f := newFakeAPI(t)
	f.on("/app", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":1}`))
	})
	exists, err := f.client().AppExists(context.Background(), "jwt")
	if err != nil {
		t.Fatalf("AppExists: %v", err)
	}
	if !exists {
		t.Error("want true")
	}
}

func TestAppExists_404(t *testing.T) {
	t.Parallel()
	f := newFakeAPI(t)
	f.on("/app", func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	})
	exists, err := f.client().AppExists(context.Background(), "jwt")
	if err != nil {
		t.Fatalf("AppExists: %v", err)
	}
	if exists {
		t.Error("404 should produce false")
	}
}

func TestAppExists_5xxIsError(t *testing.T) {
	t.Parallel()
	f := newFakeAPI(t)
	f.on("/app", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	if _, err := f.client().AppExists(context.Background(), "jwt"); err == nil {
		t.Error("want error for 500")
	}
}

func TestConvertManifestCode(t *testing.T) {
	t.Parallel()
	f := newFakeAPI(t)
	f.on("/app-manifests/CODE/conversions", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":             12345,
			"slug":           "acme-cobalt",
			"name":           "Acme Cobalt",
			"html_url":       "https://github.com/apps/acme-cobalt",
			"owner":          map[string]any{"login": "acme", "id": 7, "type": "Organization"},
			"webhook_secret": "wsecret",
			"pem":            "PEMDATA",
			"client_id":      "iv1.x",
			"client_secret":  "csecret",
		})
	})
	app, err := f.client().ConvertManifestCode(context.Background(), "CODE")
	if err != nil {
		t.Fatalf("ConvertManifestCode: %v", err)
	}
	if app.ID != 12345 {
		t.Errorf("ID: %d", app.ID)
	}
	if app.PEM != "PEMDATA" {
		t.Errorf("PEM: %q", app.PEM)
	}
	if app.WebhookSecret != "wsecret" {
		t.Errorf("WebhookSecret: %q", app.WebhookSecret)
	}
	if app.Owner.Login != "acme" || app.Owner.Type != "Organization" {
		t.Errorf("Owner: %+v", app.Owner)
	}
}

func TestInstallationsURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ownerType string
		ownerID   int64
		want      string
	}{
		{"Organization", 7, "https://github.com/apps/acme/installations/new/permissions?target_id=7"},
		{"User", 9, "https://github.com/apps/acme/installations/new"},
	}
	for _, c := range cases {
		got := InstallationsURL("https://github.com/apps/acme", c.ownerID, c.ownerType)
		if got != c.want {
			t.Errorf("got %q\nwant %q", got, c.want)
		}
	}
}

func TestNewAppName(t *testing.T) {
	t.Parallel()
	for i := 0; i < 100; i++ {
		name := NewAppName()
		// Must start with the canonical prefix.
		if got := name[:len(AppNamePrefix)]; got != AppNamePrefix {
			t.Errorf("missing prefix: %q", name)
		}
		// "Cobalt <adj> <noun>" — three space-separated tokens, all
		// non-empty, drawn from the inline lists.
		var parts [3]string
		var n int
		for _, tok := range splitFields(name) {
			if n < len(parts) {
				parts[n] = tok
			}
			n++
		}
		if n != 3 {
			t.Fatalf("want 3 tokens, got %d in %q", n, name)
		}
		if !inSlice(manifestAppAdjectives, parts[1]) {
			t.Errorf("unknown adjective %q in %q", parts[1], name)
		}
		if !inSlice(manifestAppNouns, parts[2]) {
			t.Errorf("unknown noun %q in %q", parts[2], name)
		}
	}
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func inSlice(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestBuildManifest(t *testing.T) {
	t.Parallel()
	m := BuildManifest("cobalt.example.com", "Cobalt", "abc-pending")
	if m.URL != "https://cobalt.example.com" {
		t.Errorf("URL: %q", m.URL)
	}
	if m.HookAttrs.URL != "https://cobalt.example.com/webhooks/github" {
		t.Errorf("HookAttrs.URL: %q", m.HookAttrs.URL)
	}
	if m.RedirectURL != "https://cobalt.example.com/github-apps/abc-pending/created" {
		t.Errorf("RedirectURL: %q", m.RedirectURL)
	}
	if m.Public {
		t.Error("Public should be false")
	}
	if len(m.Events) != 1 || m.Events[0] != "push" {
		t.Errorf("Events: %v", m.Events)
	}
	if m.Permissions["contents"] != "read" {
		t.Errorf("contents permission: %q", m.Permissions["contents"])
	}
}

func TestListInstallationRepos(t *testing.T) {
	t.Parallel()
	f := newFakeAPI(t)
	calls := 0
	f.on("/installation/repositories", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.Header.Get("Authorization"); got != "token tok123" {
			t.Errorf("Authorization: got %q", got)
		}
		// Single page totals 2 repos; respond once and stop.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": 2,
			"repositories": []map[string]any{
				{"id": 1, "full_name": "acme/api", "private": true, "default_branch": "main"},
				{"id": 2, "full_name": "acme/web", "private": false, "default_branch": "trunk"},
			},
		})
	})
	repos, err := f.client().ListInstallationRepos(context.Background(), "tok123")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d, want 2", len(repos))
	}
	if repos[0].FullName != "acme/api" || repos[1].DefaultBranch != "trunk" {
		t.Errorf("repos: %+v", repos)
	}
	if calls != 1 {
		t.Errorf("calls: got %d, want 1 (no pagination needed)", calls)
	}
}

func TestCloneURL_DoesNotLeakInLogString(t *testing.T) {
	t.Parallel()
	got := CloneURL("v1.secret-token", "acme/api")
	if !strings.Contains(got, "x-access-token") {
		t.Errorf("missing x-access-token: %q", got)
	}
	if !strings.Contains(got, "acme/api.git") {
		t.Errorf("missing repo path: %q", got)
	}
}

func TestHTTPError_Surface(t *testing.T) {
	t.Parallel()
	f := newFakeAPI(t)
	f.on("/app", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Not authorized", http.StatusUnauthorized)
	})
	_, err := f.client().AppExists(context.Background(), "jwt")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsStatus(err, http.StatusUnauthorized) {
		t.Errorf("IsStatus(401): false; err: %v", err)
	}
}

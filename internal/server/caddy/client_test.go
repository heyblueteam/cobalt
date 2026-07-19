package caddy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeCaddy is an httptest.Server that records every admin request, answers
// @id GETs from an in-memory map, and holds a full-config document for the
// GET /config/ + POST /load pair the read-modify-write route apply uses.
// Enough fidelity for our wire-format tests; not a real Caddy.
type fakeCaddy struct {
	t      *testing.T
	mu     sync.Mutex
	calls  []recordedCall
	byID   map[string]json.RawMessage
	config json.RawMessage
	server *httptest.Server
}

type recordedCall struct {
	Method string
	Path   string
	Body   json.RawMessage
}

// fakeBootstrapConfig is the full-config document a pristine cobalt Caddy
// holds: admin + logging blocks, and the cobalt server with only the daemon
// host route installed by the bootstrap config.
const fakeBootstrapConfig = `{"admin":{"listen":"unix//cobalt/caddy-socket/caddy.sock"},"apps":{"http":{"servers":{"cobalt":{"listen":[":80"],"routes":[{"@id":"cobalt-daemon-host","handle":[],"match":[{"host":["cobalt.example"]}],"terminal":true}]}}}},"logging":{}}`

func newFakeCaddy(t *testing.T) *fakeCaddy {
	t.Helper()
	f := &fakeCaddy{
		t:      t,
		byID:   map[string]json.RawMessage{},
		config: json.RawMessage(fakeBootstrapConfig),
	}
	f.indexIDs(f.config)
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

// indexIDs walks a config document and registers every object carrying an
// @id into byID, mirroring how real Caddy rebuilds its id index on load.
func (f *fakeCaddy) indexIDs(doc json.RawMessage) {
	var root any
	if err := json.Unmarshal(doc, &root); err != nil {
		return
	}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			if id, ok := t["@id"].(string); ok && id != "" {
				raw, _ := json.Marshal(t)
				f.byID[id] = raw
			}
			for _, child := range t {
				walk(child)
			}
		case []any:
			for _, child := range t {
				walk(child)
			}
		}
	}
	walk(root)
}

func (f *fakeCaddy) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, recordedCall{Method: r.Method, Path: r.URL.Path, Body: body})
	f.mu.Unlock()

	// Special handling for /id/<name> — used by exists/get checks.
	if rest, ok := strings.CutPrefix(r.URL.Path, "/id/"); ok {
		// rest may be `<id>` or `<id>/<subpath>` (e.g. .../upstreams/0/dial).
		id := rest
		subpath := ""
		if i := strings.Index(rest, "/"); i >= 0 {
			id = rest[:i]
			subpath = rest[i:]
		}
		switch r.Method {
		case http.MethodGet:
			v, ok := f.byID[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			// Special-case the upstreams[0].dial subpath that
			// CurrentUpstream/VerifyServeService queries: parse the stored
			// handler body and return just the dial as a JSON string.
			if subpath == "/upstreams/0/dial" {
				var doc struct {
					Upstreams []struct {
						Dial string `json:"dial"`
					} `json:"upstreams"`
				}
				if err := json.Unmarshal(v, &doc); err == nil && len(doc.Upstreams) > 0 {
					raw, _ := json.Marshal(doc.Upstreams[0].Dial)
					w.Write(raw)
					return
				}
				// If the stored value is already a bare JSON string (e.g.
				// pre-populated by a test), return it as-is.
				w.Write(v)
				return
			}
			w.Write(v)
			return
		case http.MethodDelete:
			if _, ok := f.byID[id]; !ok {
				http.NotFound(w, r)
				return
			}
			delete(f.byID, id)
			w.WriteHeader(http.StatusOK)
			return
		case http.MethodPatch:
			f.byID[id] = body
			w.WriteHeader(http.StatusOK)
			return
		}
	}

	// Full-config read + atomic swap — the read-modify-write route apply.
	if r.Method == http.MethodGet && r.URL.Path == "/config/" {
		w.Write(f.config)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/load" {
		f.config = body
		f.byID = map[string]json.RawMessage{}
		f.indexIDs(body)
		w.WriteHeader(http.StatusOK)
		return
	}

	// PUT to /config/.../routes/0 — register the route under its @id.
	if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/routes/0") {
		var doc struct {
			ID string `json:"@id"`
		}
		if err := json.Unmarshal(body, &doc); err != nil || doc.ID == "" {
			http.Error(w, "no @id in route body", http.StatusBadRequest)
			return
		}
		f.byID[doc.ID] = body
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (f *fakeCaddy) client() *Client {
	return NewHTTPClient(f.server.URL, f.server.Client())
}

func (f *fakeCaddy) lastCall(t *testing.T) recordedCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("no calls recorded")
	}
	return f.calls[len(f.calls)-1]
}

// --- tests ---

func TestProjectRouteLifecycle(t *testing.T) {
	t.Parallel()
	f := newFakeCaddy(t)
	c := f.client()
	ctx := context.Background()
	const projectID int64 = 7

	exists, err := c.ProjectRouteExists(ctx, projectID)
	if err != nil {
		t.Fatalf("exists pre-create: %v", err)
	}
	if exists {
		t.Fatal("route should not exist yet")
	}

	if err := c.AddProjectRoute(ctx, projectID, []string{"a.example", "b.example"}); err != nil {
		t.Fatalf("AddProjectRoute: %v", err)
	}
	exists, err = c.ProjectRouteExists(ctx, projectID)
	if err != nil || !exists {
		t.Fatalf("exists post-create: %v %v", exists, err)
	}

	if err := c.UpdateProjectDomains(ctx, projectID, []string{"only.example"}); err != nil {
		t.Fatalf("UpdateProjectDomains: %v", err)
	}

	if err := c.RemoveProjectRoute(ctx, projectID); err != nil {
		t.Fatalf("RemoveProjectRoute: %v", err)
	}
	exists, _ = c.ProjectRouteExists(ctx, projectID)
	if exists {
		t.Fatal("route still exists after remove")
	}
}

func TestAddProjectRoute_BodyShape(t *testing.T) {
	t.Parallel()
	f := newFakeCaddy(t)
	c := f.client()
	const projectID int64 = 42
	if err := c.AddProjectRoute(context.Background(), projectID, []string{"x.example"}); err != nil {
		t.Fatalf("AddProjectRoute: %v", err)
	}
	call := f.lastCall(t)
	if call.Method != http.MethodPost {
		t.Errorf("method: got %s, want POST", call.Method)
	}
	if call.Path != "/load" {
		t.Errorf("path: got %s", call.Path)
	}
	bodyStr := string(call.Body)
	for _, want := range []string{
		`"@id":"cobalt-project-42"`,
		`"@id":"cobalt-project-hosts-42"`,
		`"@id":"cobalt-project-handler-42"`,
		`"x.example"`,
		`"terminal":true`,
		`"reverse_proxy"`,
		c.PlaceholderUpstream,
		// The rest of the live config rides along untouched in the load.
		`"@id":"cobalt-daemon-host"`,
	} {
		if !strings.Contains(bodyStr, want) {
			t.Errorf("body missing %q\nfull: %s", want, bodyStr)
		}
	}
}

func TestServeService_PatchesHandler(t *testing.T) {
	t.Parallel()
	f := newFakeCaddy(t)
	c := f.client()
	const projectID int64 = 5
	if err := c.ServeService(context.Background(), projectID, "myapp-7-web", 3000, 7); err != nil {
		t.Fatalf("ServeService: %v", err)
	}
	call := f.lastCall(t)
	if call.Method != http.MethodPatch {
		t.Errorf("method: got %s, want PATCH", call.Method)
	}
	if call.Path != "/id/cobalt-project-handler-5" {
		t.Errorf("path: got %s", call.Path)
	}
	if !strings.Contains(string(call.Body), `"dial":"myapp-7-web:3000"`) {
		t.Errorf("body missing dial: %s", call.Body)
	}
	// The deployment-identity header must ride along in the same handler
	// PATCH so the data plane can report which deployment it serves.
	if !strings.Contains(string(call.Body), `"X-Cobalt-Deployment":["7"]`) {
		t.Errorf("body missing X-Cobalt-Deployment header: %s", call.Body)
	}
}

func TestServeStaticSite_PatchesHandler(t *testing.T) {
	t.Parallel()
	f := newFakeCaddy(t)
	c := f.client()
	if err := c.ServeStaticSite(context.Background(), 9, "myblog", 3); err != nil {
		t.Fatalf("ServeStaticSite: %v", err)
	}
	call := f.lastCall(t)
	if call.Method != http.MethodPatch {
		t.Errorf("method: got %s, want PATCH", call.Method)
	}
	body := string(call.Body)
	if !strings.Contains(body, `"handler":"file_server"`) {
		t.Errorf("expected file_server, got %s", body)
	}
	if !strings.Contains(body, `"root":"/cobalt/srv/myblog/deployments/3"`) {
		t.Errorf("root path wrong: %s", body)
	}
}

func TestSetDomainsForProject_Reconciles(t *testing.T) {
	t.Parallel()
	f := newFakeCaddy(t)
	c := f.client()
	ctx := context.Background()
	const projectID int64 = 1

	// 1. New project, domains given → AddProjectRoute.
	if err := c.SetDomainsForProject(ctx, projectID, []string{"a.example"}); err != nil {
		t.Fatalf("first set: %v", err)
	}

	// 2. Existing project, domains changed → UpdateProjectDomains (PATCH).
	if err := c.SetDomainsForProject(ctx, projectID, []string{"b.example"}); err != nil {
		t.Fatalf("second set: %v", err)
	}

	// 3. Existing project, no domains → RemoveProjectRoute (DELETE).
	if err := c.SetDomainsForProject(ctx, projectID, nil); err != nil {
		t.Fatalf("clear set: %v", err)
	}

	// 4. Already removed, no domains → no-op (no error, no extra call body).
	before := len(f.calls)
	if err := c.SetDomainsForProject(ctx, projectID, nil); err != nil {
		t.Fatalf("no-op set: %v", err)
	}
	// Just an exists check — we shouldn't have done a DELETE.
	gotMethods := []string{}
	for _, c := range f.calls[before:] {
		gotMethods = append(gotMethods, c.Method)
	}
	for _, m := range gotMethods {
		if m == http.MethodDelete {
			t.Errorf("no-op set issued DELETE")
		}
	}
}

func TestRedirects(t *testing.T) {
	t.Parallel()
	f := newFakeCaddy(t)
	c := f.client()
	ctx := context.Background()

	if err := c.AddApexWWWRedirect(ctx, 123, "www.example.com", "example.com"); err != nil {
		t.Fatalf("Add redirect: %v", err)
	}
	body := string(f.lastCall(t).Body)
	for _, want := range []string{
		`"@id":"cobalt-redirect-123"`,
		`"www.example.com"`,
		`"https://example.com{http.request.uri}"`,
		`"status_code":301`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("redirect body missing %q\n%s", want, body)
		}
	}

	if err := c.RemoveApexWWWRedirect(ctx, 123); err != nil {
		t.Fatalf("Remove redirect: %v", err)
	}
	if f.lastCall(t).Method != http.MethodDelete {
		t.Errorf("expected DELETE")
	}
}

func TestUpdateDaemonHost(t *testing.T) {
	t.Parallel()
	f := newFakeCaddy(t)
	c := f.client()
	if err := c.UpdateDaemonHost(context.Background(), "cobalt.blue.cc"); err != nil {
		t.Fatalf("UpdateDaemonHost: %v", err)
	}
	call := f.lastCall(t)
	if call.Method != http.MethodPatch {
		t.Errorf("method: got %s", call.Method)
	}
	if call.Path != "/id/cobalt-daemon-host/match/0/host/0" {
		t.Errorf("path: got %s", call.Path)
	}
	if !strings.Contains(string(call.Body), `"cobalt.blue.cc"`) {
		t.Errorf("body missing host: %s", call.Body)
	}
}

func TestHTTPError_5xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, srv.Client())
	err := c.AddProjectRoute(context.Background(), 1, []string{"x"})
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("expected *HTTPError, got %T: %v", err, err)
	}
	if he.Status != http.StatusInternalServerError {
		t.Errorf("status: got %d", he.Status)
	}
	if !strings.Contains(he.Body, "boom") {
		t.Errorf("body missing 'boom': %q", he.Body)
	}
	if IsNotFound(err) {
		t.Error("IsNotFound should be false for 500")
	}
}

func TestIsNotFound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, srv.Client())
	err := c.RemoveProjectRoute(context.Background(), 99)
	if !IsNotFound(err) {
		t.Errorf("IsNotFound: false for 404")
	}
}

func TestCurrentUpstream(t *testing.T) {
	t.Parallel()
	f := newFakeCaddy(t)
	// Manually populate a handler to read back.
	f.byID["cobalt-project-handler-7"] = json.RawMessage(`"myapp-3-web"`)
	c := f.client()

	got, err := c.CurrentUpstream(context.Background(), 7)
	if err != nil {
		t.Fatalf("CurrentUpstream: %v", err)
	}
	// fakeCaddy returns the raw stored value for any subpath GET; the value
	// here is a bare string, so the indexByte split returns the whole thing.
	if got == "" {
		t.Errorf("got empty, want some upstream")
	}
}

func TestCurrentUpstream_NotFoundReturnsEmpty(t *testing.T) {
	t.Parallel()
	f := newFakeCaddy(t)
	c := f.client()
	got, err := c.CurrentUpstream(context.Background(), 999)
	if err != nil {
		t.Fatalf("CurrentUpstream: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty for missing project, got %q", got)
	}
}

func TestStaticSiteDeploymentPath(t *testing.T) {
	t.Parallel()
	got := StaticSiteDeploymentPath("myblog", 5)
	want := "/cobalt/srv/myblog/deployments/5"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRetryClassification(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method, path string
		want         bool
	}{
		{http.MethodGet, "/config/", true},
		{http.MethodGet, "/id/cobalt-project-handler-2/upstreams/0/dial", true},
		{http.MethodPatch, "/id/cobalt-project-handler-2", true},
		{http.MethodDelete, "/id/cobalt-redirect-5", true},
		// An @id-keyed PUT converges on replay; safe to retry.
		{http.MethodPut, "/id/cobalt-project-2", true},
		// A positional route PUT is an insert: re-sending a call that timed
		// out after Caddy applied it would duplicate the route.
		{http.MethodPut, "/config/apps/http/servers/cobalt/routes/0", false},
		// The full-config load: POST stays outside the retried set.
		{http.MethodPost, "/load", false},
	}
	for _, tc := range cases {
		if got := isRetryable(tc.method, tc.path); got != tc.want {
			t.Errorf("isRetryable(%s %s): got %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestPositionalPutNotRetriedOnTransportError(t *testing.T) {
	t.Parallel()
	var attempts atomic.Int32
	// Hijack + close: the client sees a transport-layer error (the
	// transient shape the retry loop normally re-sends).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("response writer is not a hijacker")
			return
		}
		if conn, _, err := hj.Hijack(); err == nil {
			conn.Close()
		}
	}))
	defer srv.Close()

	c := NewHTTPClient(srv.URL, srv.Client())
	err := c.do(context.Background(), http.MethodPut,
		"/config/apps/http/servers/cobalt/routes/0", map[string]any{"@id": "x"}, nil)
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("positional PUT attempted %d times, want exactly 1", got)
	}
}

package caddy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Blocks of a live Caddy config the read-modify-write apply must never
// rebuild. Each carries deliberately NON-alphabetical interior key order:
// if the apply ever decoded one into a Go map and re-marshaled it, the keys
// would come back sorted and the byte-for-byte assertions below would fail.
const (
	rtAdminBlock    = `{"listen":"unix//cobalt/caddy-socket/caddy.sock","origins":["cobalt-caddy"],"enforce_origin":false}`
	rtLoggingBlock  = `{"logs":{"default":{"encoder":{"wrap":{"format":"json"},"format":"filter"}}}}`
	rtTLSApp        = `{"automation":{"policies":[{"subjects":["blue.app","www.example.com"]}]}}`
	rtRedirectRoute = `{"@id":"cobalt-redirect-5","match":[{"host":["www.example.com"]}],"handle":[{"handler":"subroute","routes":[{"handle":[{"handler":"static_response","status_code":301,"headers":{"Location":["https://example.com{http.request.uri}"]}}]}]}],"terminal":true}`
	rtProjectRoute  = `{"@id":"cobalt-project-2","match":[{"@id":"cobalt-project-hosts-2","host":["blue.app"]}],"handle":[{"handler":"subroute","routes":[{"handle":[{"@id":"cobalt-project-handler-2","handler":"reverse_proxy","upstreams":[{"dial":"next-114-web:80"}]}]}]}],"terminal":true}`
	rtDaemonRoute   = `{"@id":"cobalt-daemon-host","match":[{"host":["cobalt.blue.cc"]}],"handle":[{"handler":"subroute","routes":[{"handle":[{"@id":"cobalt-daemon-handler","handler":"reverse_proxy","upstreams":[{"dial":"cobalt:80"}]}]}]}],"terminal":true}`
)

// rtLiveConfig assembles the live document. The levels the apply traverses
// (top level, apps, http, servers, cobalt) are written in Go's map-marshal
// (alphabetical) key order, so an untouched round trip must reproduce the
// document byte-for-byte.
const rtLiveConfig = `{"admin":` + rtAdminBlock +
	`,"apps":{"http":{"servers":{"cobalt":{"listen":[":80"],"logs":{},"protocols":["h1","h2"],"routes":[` +
	rtRedirectRoute + `,` + rtProjectRoute + `,` + rtDaemonRoute +
	`]}}},"tls":` + rtTLSApp + `},"logging":` + rtLoggingBlock + `}`

// loadCapturingCaddy fakes only the two endpoints the read-modify-write
// apply touches: GET /config/ serves a fixed document, POST /load captures
// what the client sends back.
type loadCapturingCaddy struct {
	mu     sync.Mutex
	loads  [][]byte
	server *httptest.Server
}

func newLoadCapturingCaddy(t *testing.T, config string) *loadCapturingCaddy {
	t.Helper()
	f := &loadCapturingCaddy{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/config/":
			w.Write([]byte(config))
		case r.Method == http.MethodPost && r.URL.Path == "/load":
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.loads = append(f.loads, body)
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected admin call: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *loadCapturingCaddy) lastLoad(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.loads) == 0 {
		t.Fatal("no /load call captured")
	}
	return string(f.loads[len(f.loads)-1])
}

func (f *loadCapturingCaddy) loadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.loads)
}

func TestApplyCobaltRoutes_NoOpRoundTrip(t *testing.T) {
	t.Parallel()
	f := newLoadCapturingCaddy(t, rtLiveConfig)
	c := NewHTTPClient(f.server.URL, f.server.Client())

	err := c.applyCobaltRoutes(context.Background(), func(routes []json.RawMessage) ([]json.RawMessage, error) {
		return routes, nil
	})
	if err != nil {
		t.Fatalf("applyCobaltRoutes: %v", err)
	}
	posted := f.lastLoad(t)

	// Pinpoint first: every block cobalt doesn't own must survive the
	// modify step byte-for-byte.
	for name, block := range map[string]string{
		"admin":          rtAdminBlock,
		"logging":        rtLoggingBlock,
		"tls app":        rtTLSApp,
		"redirect route": rtRedirectRoute,
		"daemon route":   rtDaemonRoute,
		"project route":  rtProjectRoute,
	} {
		if !strings.Contains(posted, block) {
			t.Errorf("%s block not preserved byte-for-byte\nwant substring: %s\nposted: %s", name, block, posted)
		}
	}
	// And the untouched round trip reproduces the whole document exactly.
	if posted != rtLiveConfig {
		t.Errorf("no-op round trip altered the config\n got: %s\nwant: %s", posted, rtLiveConfig)
	}
}

func TestAddProjectRoute_TouchesOnlyOwnedRoutes(t *testing.T) {
	t.Parallel()
	f := newLoadCapturingCaddy(t, rtLiveConfig)
	c := NewHTTPClient(f.server.URL, f.server.Client())

	if err := c.AddProjectRoute(context.Background(), 7, []string{"seven.example"}); err != nil {
		t.Fatalf("AddProjectRoute: %v", err)
	}
	posted := f.lastLoad(t)

	// The pre-existing routes and non-route blocks pass through untouched,
	// keeping their relative order.
	idxRedirect := strings.Index(posted, rtRedirectRoute)
	idxProject := strings.Index(posted, rtProjectRoute)
	idxDaemon := strings.Index(posted, rtDaemonRoute)
	if idxRedirect < 0 || idxProject < 0 || idxDaemon < 0 {
		t.Fatalf("an existing route was altered (redirect=%d project=%d daemon=%d)\nposted: %s",
			idxRedirect, idxProject, idxDaemon, posted)
	}
	if idxRedirect >= idxProject || idxProject >= idxDaemon {
		t.Errorf("existing route order changed (redirect=%d project=%d daemon=%d)",
			idxRedirect, idxProject, idxDaemon)
	}
	for name, block := range map[string]string{
		"admin":   rtAdminBlock,
		"logging": rtLoggingBlock,
		"tls app": rtTLSApp,
	} {
		if !strings.Contains(posted, block) {
			t.Errorf("%s block not preserved byte-for-byte: %s", name, posted)
		}
	}
	// The new route is prepended ahead of every existing route.
	idxNew := strings.Index(posted, `"@id":"cobalt-project-7"`)
	if idxNew < 0 {
		t.Fatalf("new project route missing: %s", posted)
	}
	if idxNew > idxRedirect {
		t.Errorf("new route not prepended (new=%d redirect=%d)", idxNew, idxRedirect)
	}
	if !strings.Contains(posted, `"seven.example"`) {
		t.Errorf("new route missing its domain: %s", posted)
	}
}

func TestAddProjectRoute_ReplacesExistingRoute(t *testing.T) {
	t.Parallel()
	f := newLoadCapturingCaddy(t, rtLiveConfig)
	c := NewHTTPClient(f.server.URL, f.server.Client())

	// Project 2 already has a route in the live config; re-adding must
	// replace it, never leave two routes claiming the same @id.
	if err := c.AddProjectRoute(context.Background(), 2, []string{"replacement.example"}); err != nil {
		t.Fatalf("AddProjectRoute: %v", err)
	}
	posted := f.lastLoad(t)

	if n := strings.Count(posted, `"@id":"cobalt-project-2",`); n != 1 {
		t.Errorf("want exactly 1 route with the project @id, got %d\nposted: %s", n, posted)
	}
	if strings.Contains(posted, rtProjectRoute) {
		t.Errorf("stale project route survived the replace: %s", posted)
	}
	if !strings.Contains(posted, `"replacement.example"`) {
		t.Errorf("replacement route missing its domain: %s", posted)
	}
	for name, block := range map[string]string{
		"redirect route": rtRedirectRoute,
		"daemon route":   rtDaemonRoute,
	} {
		if !strings.Contains(posted, block) {
			t.Errorf("%s not preserved byte-for-byte: %s", name, posted)
		}
	}
}

func TestApplyCobaltRoutes_UnrecognizedConfigFailsLoud(t *testing.T) {
	t.Parallel()
	// A Caddy without the cobalt server block isn't running our bootstrap
	// config; the apply must refuse to invent structure (and must not POST).
	f := newLoadCapturingCaddy(t, `{"admin":{},"apps":{"http":{"servers":{}}}}`)
	c := NewHTTPClient(f.server.URL, f.server.Client())

	err := c.applyCobaltRoutes(context.Background(), func(routes []json.RawMessage) ([]json.RawMessage, error) {
		return routes, nil
	})
	if err == nil {
		t.Fatal("expected an error for a config without the cobalt server")
	}
	if !strings.Contains(err.Error(), `"cobalt"`) {
		t.Errorf("error should name the missing block: %v", err)
	}
	if n := f.loadCount(); n != 0 {
		t.Errorf("apply must not POST /load on an unrecognized config, got %d loads", n)
	}
}

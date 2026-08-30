package deploy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

func TestParseHTTPHead(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		raw        string
		wantParsed bool
		wantStatus int
		wantServed string
	}{
		{
			name:       "nc plaintext 200 with header",
			raw:        "HTTP/1.1 200 OK\r\nServer: Caddy\r\nX-Cobalt-Deployment: 114\r\nContent-Length: 0\r\n\r\n",
			wantParsed: true, wantStatus: 200, wantServed: "114",
		},
		{
			name:       "gateway 502 carries no header (dead upstream)",
			raw:        "HTTP/1.1 502 Bad Gateway\r\nServer: Caddy\r\n\r\n",
			wantParsed: true, wantStatus: 502, wantServed: "",
		},
		{
			name:       "indented response lines",
			raw:        "Connecting to localhost:443\n  HTTP/1.1 200 OK\n  Server: Caddy\n  X-Cobalt-Deployment: 207\n\n",
			wantParsed: true, wantStatus: 200, wantServed: "207",
		},
		{
			name:       "header name is case-insensitive",
			raw:        "HTTP/1.1 200 OK\r\nx-cobalt-deployment: 9\r\n\r\n",
			wantParsed: true, wantStatus: 200, wantServed: "9",
		},
		{
			name:       "standalone redirect: 3xx, no header",
			raw:        "HTTP/1.1 308 Permanent Redirect\r\nLocation: https://app.example.com/\r\n\r\n",
			wantParsed: true, wantStatus: 308, wantServed: "",
		},
		{
			name:       "header after blank line is ignored (body, not head)",
			raw:        "HTTP/1.1 200 OK\r\n\r\nX-Cobalt-Deployment: 5\r\n",
			wantParsed: true, wantStatus: 200, wantServed: "",
		},
		{
			name:       "no HTTP response (connection refused)",
			raw:        "nc: bad address 'localhost'\n",
			wantParsed: false,
		},
		{
			name:       "empty",
			raw:        "",
			wantParsed: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			res, parsed := parseHTTPHead(tc.raw)
			if parsed != tc.wantParsed {
				t.Fatalf("parsed: got %v, want %v", parsed, tc.wantParsed)
			}
			if !parsed {
				return
			}
			if res.Status != tc.wantStatus {
				t.Errorf("status: got %d, want %d", res.Status, tc.wantStatus)
			}
			if res.Served != tc.wantServed {
				t.Errorf("served: got %q, want %q", res.Served, tc.wantServed)
			}
		})
	}
}

func TestIsProbableHostname(t *testing.T) {
	t.Parallel()
	ok := []string{"blue.app", "wl.blue.app", "a-b.example.com", "localhost"}
	bad := []string{"", "has space.com", "semi;colon", "back`tick", "quote'd", "pipe|d", "$(x)"}
	for _, h := range ok {
		if !isProbableHostname(h) {
			t.Errorf("isProbableHostname(%q) = false, want true", h)
		}
	}
	for _, h := range bad {
		if isProbableHostname(h) {
			t.Errorf("isProbableHostname(%q) = true, want false", h)
		}
	}
}

// fakeDataPlaneProber stands in for *docker.Client in waitDataPlaneServing
// tests. It answers the :80 plaintext path with a canned HTTP response.
type fakeDataPlaneProber struct {
	plaintextResp     string // written to stdout for the nc path; "" + err => :80 closed
	plaintextByDomain map[string]string
	plaintextErr      error
	probedDomains     []string
}

func (f *fakeDataPlaneProber) FindContainerByLabel(context.Context, string) (string, error) {
	return "cobalt-caddy", nil
}

func (f *fakeDataPlaneProber) Exec(_ context.Context, _ string, cmd []string, stdout, _ io.Writer) error {
	if len(cmd) > 0 && cmd[0] == "sh" { // plaintext :80 path
		response := f.plaintextResp
		for domain, domainResponse := range f.plaintextByDomain {
			if strings.Contains(cmd[2], "'"+domain+"'") {
				f.probedDomains = append(f.probedDomains, domain)
				response = domainResponse
				break
			}
		}
		if response != "" {
			_, _ = io.WriteString(stdout, response)
		}
		return f.plaintextErr
	}
	return errors.New("unexpected exec command")
}

type fakeDomainLister struct {
	domains []string
	err     error
}

func (f fakeDomainLister) ListPrimaryDomainsForProject(context.Context, int64) ([]string, error) {
	return f.domains, f.err
}

func resp(status, served string) string {
	head := "HTTP/1.1 " + status + "\r\nServer: Caddy\r\n"
	if served != "" {
		head += "X-Cobalt-Deployment: " + served + "\r\n"
	}
	return head + "\r\n"
}

func TestWaitDataPlaneServing(t *testing.T) {
	// Shrink the wait; these globals are only ever read by this function and
	// only when an Orchestrator has a non-nil DataPlaneProber (never in other
	// tests), so mutating them here is safe.
	origTimeout, origPoll := dataPlaneVerifyTimeout, dataPlaneVerifyPoll
	dataPlaneVerifyTimeout, dataPlaneVerifyPoll = 40*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { dataPlaneVerifyTimeout, dataPlaneVerifyPoll = origTimeout, origPoll })

	container := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeContainer, Port: 3000},
	}}
	static := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeStatic},
	}}
	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{Number: 5}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name      string
		prober    *fakeDataPlaneProber
		cf        *cobaltfile.Cobaltfile
		domains   []string
		wantErr   bool
		errSubstr string
	}{
		{
			name:   "router serves new deployment → pass",
			prober: &fakeDataPlaneProber{plaintextResp: resp("200 OK", "5")},
			cf:     container, domains: []string{"api.example.com"},
		},
		{
			name:   "router 502s on dead upstream → hard fail (the incident)",
			prober: &fakeDataPlaneProber{plaintextResp: resp("502 Bad Gateway", "")},
			cf:     container, domains: []string{"api.example.com"},
			wantErr: true, errSubstr: "never converged",
		},
		{
			name:   "router still serves old deployment → hard fail",
			prober: &fakeDataPlaneProber{plaintextResp: resp("200 OK", "3")},
			cf:     container, domains: []string{"api.example.com"},
			wantErr: true, errSubstr: "never converged",
		},
		{
			name:   "probe inconclusive (no deployment header) → soft pass",
			prober: &fakeDataPlaneProber{plaintextResp: resp("200 OK", "")},
			cf:     container, domains: []string{"api.example.com"},
		},
		{
			name:   "static web type → skipped, no probe",
			prober: &fakeDataPlaneProber{plaintextResp: resp("502 Bad Gateway", "")},
			cf:     static, domains: []string{"api.example.com"},
		},
		{
			name:   "no domains → skipped",
			prober: &fakeDataPlaneProber{plaintextResp: resp("502 Bad Gateway", "")},
			cf:     container, domains: nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := fakeDomainLister{domains: tc.domains}
			err := waitDataPlaneServing(context.Background(), tc.prober, st, project, dep, tc.cf, log, io.Discard)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("error %q missing %q", err, tc.errSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("want nil, got %v", err)
			}
		})
	}
}

func TestWaitDataPlaneServing_InconclusiveDomain_TriesNextDomain(t *testing.T) {
	container := &cobaltfile.Cobaltfile{Services: map[string]cobaltfile.Service{
		"web": {Type: cobaltfile.TypeContainer, Port: 3000},
	}}
	prober := &fakeDataPlaneProber{plaintextByDomain: map[string]string{
		"legacy.example.com": resp("200 OK", ""),
		"api.example.com":    resp("200 OK", "5"),
	}}
	st := fakeDomainLister{domains: []string{"legacy.example.com", "api.example.com"}}
	project := store.Project{ID: 1, Name: "api"}
	dep := store.Deployment{Number: 5}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	err := waitDataPlaneServing(context.Background(), prober, st, project, dep, container, log, io.Discard)
	if err != nil {
		t.Fatalf("waitDataPlaneServing: %v", err)
	}
	if len(prober.probedDomains) != 2 || prober.probedDomains[0] != "legacy.example.com" ||
		prober.probedDomains[1] != "api.example.com" {
		t.Fatalf("probed domains = %v, want both domains in order", prober.probedDomains)
	}
}

func TestProbeDataPlaneHTTPSFallbackUsesProjectDomainForHostAndSNI(t *testing.T) {
	t.Parallel()
	type observation struct {
		host string
		sni  string
	}
	observed := make(chan observation, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- observation{host: r.Host, sni: r.TLS.ServerName}
		w.Header().Set("X-Cobalt-Deployment", "403")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	address := strings.TrimPrefix(server.URL, "https://")
	prober := &fakeDataPlaneProber{
		plaintextResp: resp("308 Permanent Redirect", ""),
	}
	result, err := probeDataPlaneAt(context.Background(), prober, "api.blue.app", address)
	if err != nil {
		t.Fatalf("probeDataPlaneAt: %v", err)
	}
	if result.Status != http.StatusNoContent || result.Served != "403" {
		t.Fatalf("result = %+v, want status 204 served 403", result)
	}
	got := <-observed
	if got.host != "api.blue.app" {
		t.Errorf("Host = %q, want api.blue.app", got.host)
	}
	if got.sni != "api.blue.app" {
		t.Errorf("SNI = %q, want api.blue.app", got.sni)
	}
}

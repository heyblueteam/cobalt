package e2e

import (
	"crypto/tls"
	"net/http"
	"os"
	"sync"
	"testing"

	"github.com/heyblueteam/cobalt/internal/cliconfig"
	"github.com/heyblueteam/cobalt/internal/client"
)

// Env captures the resolved configuration for one test run. Tests
// receive this from RequireEnv; missing required vars produce a
// t.Skip rather than a failure so `go test ./...` is cheap.
type Env struct {
	Host        string
	APIKey      string
	FixtureRepo string
	DomainBase  string
	Keep        bool
	// InsecureTLS, when true, makes the e2e HTTP probe accept any
	// server cert. Set via COBALT_E2E_INSECURE_TLS=1 to run against
	// a daemon initialized with `cobalt init --insecure-tls` (dev /
	// throwaway VM with Caddy's self-signed `tls internal` chain).
	InsecureTLS bool
}

const (
	envHost        = "COBALT_E2E_HOST"
	envAPIKey      = "COBALT_E2E_API_KEY"
	envFixture     = "COBALT_E2E_FIXTURE_REPO"
	envDomainBase  = "COBALT_E2E_DOMAIN_BASE"
	envKeep        = "COBALT_E2E_KEEP"
	envInsecureTLS = "COBALT_E2E_INSECURE_TLS"
	defaultFixture = "heyblueteam/cobalt-fixture-app"
)

// RequireEnv loads the e2e config from environment variables. If
// COBALT_E2E_HOST is unset, the test is skipped — this is the
// Terraform `TF_ACC` idiom: contributors who haven't configured a
// target see a SKIP, not a failure. COBALT_E2E_API_KEY is also
// required (cannot deploy without auth); its absence is a hard
// failure rather than a skip because a partially-configured run
// suggests the operator forgot to source their env file.
func RequireEnv(t *testing.T) Env {
	t.Helper()
	host := os.Getenv(envHost)
	if host == "" {
		t.Skipf("set %s to run e2e tests (see e2e/README.md)", envHost)
	}
	key := os.Getenv(envAPIKey)
	if key == "" {
		t.Fatalf("%s is set but %s is not — both are required", envHost, envAPIKey)
	}
	fixture := os.Getenv(envFixture)
	if fixture == "" {
		fixture = defaultFixture
	}
	e := Env{
		Host:        host,
		APIKey:      key,
		FixtureRepo: fixture,
		DomainBase:  os.Getenv(envDomainBase),
		Keep:        os.Getenv(envKeep) != "",
		InsecureTLS: os.Getenv(envInsecureTLS) != "",
	}
	applyInsecureTLS(e.InsecureTLS)
	return e
}

// applyInsecureTLS swaps the shared HTTP probe client's transport for
// one that skips cert verification. Done at most once per process —
// later calls are no-ops. Only used by the e2e harness against
// daemons running with `tls internal` (self-signed) certs.
var insecureTLSOnce sync.Once

func applyInsecureTLS(enabled bool) {
	if !enabled {
		return
	}
	insecureTLSOnce.Do(func() {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		httpClient.Transport = tr
	})
}

// requireDomainBase skips the test when COBALT_E2E_DOMAIN_BASE is
// unset. Used by scenarios that exercise public HTTPS — without a
// domain pointed at the daemon they can't assert serve-side
// behavior.
func (e Env) requireDomainBase(t *testing.T) string {
	t.Helper()
	if e.DomainBase == "" {
		t.Skipf("set %s to run scenarios that exercise public HTTPS", envDomainBase)
	}
	return e.DomainBase
}

// Client returns a cobalt API client wired to the e2e target.
func (e Env) Client() *client.Client {
	return client.New(cliconfig.Server{
		Host:   e.Host,
		APIKey: e.APIKey,
	})
}

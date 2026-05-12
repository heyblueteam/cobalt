package e2e

import (
	"crypto/tls"
	"crypto/x509"
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
	// CACertPEM is a PEM-encoded CA cert the harness's API client
	// should trust in addition to the system pool. Sourced from
	// COBALT_E2E_CA_CERT_FILE — typically the same root.crt the
	// CLI's `~/.cobalt/config.json` stores for a `--insecure-tls`
	// daemon. Distinct from InsecureTLS: pinning preserves real
	// chain verification, whereas InsecureTLS only relaxes the
	// probe client (curl-equivalent).
	CACertPEM string
}

const (
	envHost        = "COBALT_E2E_HOST"
	envAPIKey      = "COBALT_E2E_API_KEY"
	envFixture     = "COBALT_E2E_FIXTURE_REPO"
	envDomainBase  = "COBALT_E2E_DOMAIN_BASE"
	envKeep        = "COBALT_E2E_KEEP"
	envInsecureTLS = "COBALT_E2E_INSECURE_TLS"
	envCACertFile  = "COBALT_E2E_CA_CERT_FILE"
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
	caPEM := ""
	if p := os.Getenv(envCACertFile); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("%s=%q: %v", envCACertFile, p, err)
		}
		caPEM = string(b)
	}
	e := Env{
		Host:        host,
		APIKey:      key,
		FixtureRepo: fixture,
		DomainBase:  os.Getenv(envDomainBase),
		Keep:        os.Getenv(envKeep) != "",
		InsecureTLS: os.Getenv(envInsecureTLS) != "",
		CACertPEM:   caPEM,
	}
	configureProbeTLS(e)
	return e
}

// configureProbeTLS swaps the shared HTTP probe client's transport at
// most once per process so it trusts what the harness was told to
// trust. Two modes; pinning wins when both are set:
//
//   - CACertPEM → real chain verification against a pinned CA pool
//     (system roots + the cert in CACertPEM). Same model the CLI uses
//     for `cobalt init --insecure-tls` daemons. Preferred.
//   - InsecureTLS → InsecureSkipVerify. Skipped when CACertPEM is set
//     since pinning is strictly better.
var probeTLSOnce sync.Once

func configureProbeTLS(e Env) {
	if e.CACertPEM == "" && !e.InsecureTLS {
		return
	}
	probeTLSOnce.Do(func() {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if e.CACertPEM != "" {
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if pool.AppendCertsFromPEM([]byte(e.CACertPEM)) {
				tr.TLSClientConfig = &tls.Config{RootCAs: pool}
			}
		} else {
			tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
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
		Host:      e.Host,
		APIKey:    e.APIKey,
		CACertPEM: e.CACertPEM,
	})
}

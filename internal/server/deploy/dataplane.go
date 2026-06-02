package deploy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// dataPlaneResult is what a single probe through Caddy's own listener
// observed about the *running* (compiled) router — as opposed to the admin
// config tree, which can lag the router under reload pressure and so lies.
type dataPlaneResult struct {
	// Served is the X-Cobalt-Deployment header the running router stamped,
	// i.e. the deployment number it actually routes this host to. "" means
	// the response carried no such header — an old/pre-header handler or a
	// non-project response (e.g. Caddy's own redirect). Callers treat "" as
	// "unknown", never as drift.
	Served string
	// Status is the HTTP status code observed (0 if no HTTP response was
	// parsed). A gateway error (502/503/504) means the router is dialing a
	// dead upstream — itself a divergence signal.
	Status int
}

// dataPlaneProbeTimeout bounds a single scheme's probe (plaintext or TLS).
const dataPlaneProbeTimeout = 5 * time.Second

// probeDataPlane issues a real HTTP request through Caddy's own listener from
// inside the cobalt-caddy container, with Host set to the project's domain,
// and reports the X-Cobalt-Deployment header the running router emits plus the
// HTTP status. This is the only ground truth for "which deployment does the
// compiled router actually serve" — every admin-API read reflects the config
// tree, which can diverge from the running router.
//
// It auto-detects cobalt's two topologies so the daemon needn't know its own
// mode:
//   - Behind a TLS-terminating tunnel (Cloudflare Tunnel et al.), Caddy
//     listens on :80 plaintext.
//   - Standalone, Caddy listens on :443 and terminates TLS itself (and may
//     run an :80→:443 redirect vhost).
//
// It first issues a hand-built plaintext request on :80 (single, exact Host
// header so the project's host matcher matches — busybox tooling can't be
// trusted to set Host cleanly otherwise). If :80 is closed, or answers only
// with Caddy's own HTTPS redirect (the standalone case, recognised as a 3xx
// carrying no deployment header), it falls back to an HTTPS probe on :443.
//
// Returns an error only when neither scheme yields an HTTP response (Caddy
// unreachable). Any HTTP answer — even a 502 — is a successful probe.
func probeDataPlane(ctx context.Context, p ReadinessProber, domain string) (dataPlaneResult, error) {
	if !isProbableHostname(domain) {
		return dataPlaneResult{}, fmt.Errorf("data-plane probe: refusing to probe implausible host %q", domain)
	}
	container := resolveCaddyContainer(ctx, p)

	// 1) plaintext :80 — the tunnel topology, and the one we can build a
	//    clean single-Host request for.
	if res, ok := probePlaintext(ctx, p, container, domain); ok {
		return res, nil
	}
	// 2) https :443 — the standalone topology.
	if res, ok := probeHTTPS(ctx, p, container, domain); ok {
		return res, nil
	}
	return dataPlaneResult{}, fmt.Errorf(
		"data-plane probe: caddy did not answer on :80 or :443 for host %q", domain)
}

// probePlaintext sends a raw HTTP/1.1 request to Caddy's :80 listener via nc
// (the same busybox tool probeTCP relies on), giving us exact control over the
// Host header. Returns ok=false when :80 is closed/unanswered, or when the
// only answer is Caddy's own HTTPS redirect (no deployment header) — both of
// which mean "ask :443 instead".
func probePlaintext(ctx context.Context, p ReadinessProber, container, domain string) (dataPlaneResult, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, dataPlaneProbeTimeout)
	defer cancel()
	// printf with the domain as an *argument* (not in the format string), so a
	// hostname can't smuggle format directives; isProbableHostname already
	// bounds it to a safe charset for the surrounding single-quotes.
	script := fmt.Sprintf(
		`printf 'GET / HTTP/1.1\r\nHost: %%s\r\nConnection: close\r\n\r\n' '%s' | nc -w 3 localhost 80`,
		domain,
	)
	var stdout bytes.Buffer
	// nc exits non-zero on connection refused; we decide on parsed output, not
	// exit code (mirrors the busybox-exit-code caveat on the wget path).
	_ = p.Exec(probeCtx, container, []string{"sh", "-c", script}, &stdout, io.Discard)

	res, parsed := parseHTTPHead(stdout.String())
	if !parsed {
		return dataPlaneResult{}, false // :80 closed / nc failed → try TLS
	}
	if res.Served == "" && isRedirect(res.Status) {
		// Standalone's :80→:443 redirect vhost: a 3xx with no project header.
		// Project-level redirects come *through* the reverse_proxy and carry
		// the header, so they won't be misclassified here.
		return dataPlaneResult{}, false
	}
	return res, true
}

// probeHTTPS probes Caddy's :443 listener with busybox wget over TLS. wget -S
// writes the response head to stderr; we parse there. Note busybox wget exits
// non-zero on any 4xx/5xx even when a header is present, so we never gate on
// the exit code — only on whether a status line was parsed.
func probeHTTPS(ctx context.Context, p ReadinessProber, container, domain string) (dataPlaneResult, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, dataPlaneProbeTimeout)
	defer cancel()
	cmd := []string{
		"wget", "-S", "-O", "/dev/null", "-T", "4",
		"--no-check-certificate",
		"--header", "Host: " + domain,
		"https://localhost/",
	}
	var stderr bytes.Buffer
	_ = p.Exec(probeCtx, container, cmd, io.Discard, &stderr)
	return parseHTTPHead(stderr.String())
}

// parseHTTPHead scans raw response text (an nc body or busybox `wget -S`
// stderr) for the first HTTP status line and the X-Cobalt-Deployment header,
// stopping at the first blank line (end of headers). Each line is left-trimmed
// so it copes with wget's leading-space indentation. parsed is false when no
// HTTP status line is present.
func parseHTTPHead(raw string) (res dataPlaneResult, parsed bool) {
	for _, rawLine := range strings.Split(raw, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			if parsed {
				break // end of header block
			}
			continue
		}
		if !parsed && strings.HasPrefix(line, "HTTP/") {
			res.Status = parseStatusCode(line)
			parsed = true
			continue
		}
		if parsed {
			if name, val, ok := splitHeader(line); ok &&
				strings.EqualFold(name, caddy.DeploymentHeader) {
				res.Served = val
			}
		}
	}
	return res, parsed
}

// parseStatusCode pulls the numeric code out of a status line like
// "HTTP/1.1 502 Bad Gateway". Returns 0 if it can't.
func parseStatusCode(statusLine string) int {
	fields := strings.Fields(statusLine)
	if len(fields) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(fields[1])
	return n
}

// splitHeader splits "Name: value" into its parts.
func splitHeader(line string) (name, value string, ok bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

func isRedirect(status int) bool { return status >= 300 && status < 400 }

func isGatewayError(status int) bool {
	return status == 502 || status == 503 || status == 504
}

// primaryDomainLister is the store subset waitDataPlaneServing needs.
type primaryDomainLister interface {
	ListPrimaryDomainsForProject(ctx context.Context, projectID int64) ([]string, error)
}

// Tunable so tests can shrink the wait; production leaves the defaults.
var (
	dataPlaneVerifyTimeout = 30 * time.Second
	dataPlaneVerifyPoll    = 2 * time.Second
)

// waitDataPlaneServing blocks until Caddy's running router actually serves the
// just-cut-over deployment on the data plane — confirming the cutover landed
// in the *compiled* router, not merely the admin config tree (which can lag
// under reload pressure; that lag, against a since-deleted upstream, is the
// post-cutover 502 incident this guards).
//
// Returns nil (nothing to probe) for non-container web types, internally
// exposed services, and projects with no primary domains.
//
// Timeout policy is deliberately asymmetric:
//   - Saw the router serving a DIFFERENT deployment, or a gateway error
//     (502/503/504 — dialing a dead upstream)? That's real divergence:
//     return an error so the caller reverts. This is the incident signature.
//   - Only ever got probe-infra failures (Caddy unreachable / no header)?
//     Log loudly but return nil — a broken probe must not fail every deploy,
//     and the convergence reconciler still catches real post-deploy drift.
func waitDataPlaneServing(
	ctx context.Context,
	p ReadinessProber,
	st primaryDomainLister,
	project store.Project,
	dep store.Deployment,
	cf *cobaltfile.Cobaltfile,
	log *slog.Logger,
	out io.Writer,
) error {
	web, ok := cf.Services["web"]
	if !ok || web.ExposedInternally || web.Type != cobaltfile.TypeContainer {
		return nil
	}
	domains, err := st.ListPrimaryDomainsForProject(ctx, project.ID)
	if err != nil {
		log.Warn("data-plane verify: list domains failed; skipping",
			"project_id", project.ID, "error", err)
		return nil
	}
	if len(domains) == 0 {
		return nil
	}
	domain := domains[0]
	want := strconv.Itoa(dep.Number)

	fmt.Fprintf(out, "🔎 confirming the live router serves deployment #%d\n", dep.Number)

	deadline := time.Now().Add(dataPlaneVerifyTimeout)
	var sawDivergence bool
	var lastDetail string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := probeDataPlane(ctx, p, domain)
		switch {
		case err != nil:
			lastDetail = err.Error()
		case res.Served == want:
			fmt.Fprintf(out, "✅ live router serves deployment #%d\n", dep.Number)
			return nil
		case isGatewayError(res.Status):
			sawDivergence = true
			lastDetail = fmt.Sprintf("router returned %d (dialing a dead upstream)", res.Status)
		case res.Served != "" && res.Served != want:
			sawDivergence = true
			lastDetail = fmt.Sprintf("router still serves deployment %s, want %s", res.Served, want)
		default:
			lastDetail = fmt.Sprintf("no deployment header yet (status %d)", res.Status)
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(dataPlaneVerifyPoll):
		}
	}

	if sawDivergence {
		return fmt.Errorf(
			"live router never converged to deployment #%d within %s: %s",
			dep.Number, dataPlaneVerifyTimeout, lastDetail)
	}
	log.Warn("data-plane verify: no positive confirmation, proceeding (probe inconclusive)",
		"project_id", project.ID, "deployment", dep.Number, "detail", lastDetail)
	fmt.Fprintf(out, "⚠️  could not confirm live router (probe inconclusive); proceeding — reconciler will self-heal if needed\n")
	return nil
}

// isProbableHostname reports whether s is a plausible DNS hostname — letters,
// digits, dot, hyphen only. Used to keep the probe's shell command injection-
// free; anything outside this charset is refused rather than escaped.
func isProbableHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.' || r == '-':
		default:
			return false
		}
	}
	return true
}

// CaddyDataPlaneProber adapts a ReadinessProber into the deployment-identity
// probe the Caddy reconciler consumes (worker.DataPlaneProber). It lives here
// because the probe mechanics (exec-through-caddy) belong to deploy; the
// reconciler depends on it through a structural interface, so worker never
// imports deploy.
type CaddyDataPlaneProber struct {
	Prober ReadinessProber
}

// ServedDeployment reports the deployment number the running router serves for
// domain (via X-Cobalt-Deployment), plus the HTTP status. served == "" means
// no header (pre-header handler or non-project response). A non-nil error
// means the probe could not run; callers treat that as "unknown", not drift.
func (c CaddyDataPlaneProber) ServedDeployment(ctx context.Context, domain string) (served string, status int, err error) {
	res, err := probeDataPlane(ctx, c.Prober, domain)
	if err != nil {
		return "", 0, err
	}
	return res.Served, res.Status, nil
}

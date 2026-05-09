// Package e2e drives a real cobalt daemon over its HTTP API and
// asserts outcomes against real deployments. It is gated by the
// COBALT_E2E_HOST environment variable: when unset every test in
// this package calls t.Skip, so `go test ./...` stays cheap for
// contributors who haven't set up a target.
//
// Run with:
//
//	make e2e
//
// or directly:
//
//	COBALT_E2E_HOST=cobalt.example.com \
//	COBALT_E2E_API_KEY=... \
//	COBALT_E2E_DOMAIN_BASE=e2e.example.com \
//	go test -v -timeout 30m ./e2e/...
//
// See e2e/README.md for the full set of env vars and the
// "bring your own host" workflow.
package e2e

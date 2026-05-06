// Package cobaltapi contains request and response types shared between the
// cobalt CLI and the cobalt daemon. It is the only public package in this
// module — anything outside it lives under internal/ and cannot be imported
// by external callers.
package cobaltapi

// Health is the response from GET /healthz.
type Health struct {
	Status string `json:"status"`
}

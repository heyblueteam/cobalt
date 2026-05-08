package cobaltapi

// DomainTypePrimary marks a domain that serves the project's web
// service directly (Caddy reverse-proxies traffic to the container).
const DomainTypePrimary = "primary"

// DomainTypeRedirect marks a domain that 301s to another primary
// domain on the same project. Caddy serves the redirect; no upstream.
const DomainTypeRedirect = "redirect"

// Domain is the public shape of a domain bound to a project.
type Domain struct {
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt,omitempty"`
	// Type is "primary" or "redirect". Older clients/servers may
	// omit this field; absent or empty = treat as primary.
	Type string `json:"type,omitempty"`
	// RedirectTo is the apex/host this domain 301s to. Only set
	// when Type == "redirect".
	RedirectTo string `json:"redirectTo,omitempty"`
}

// DomainAddRequest is the body of POST /api/projects/{name}/domains.
//
// To register a primary domain, send {"name": "blue.cc"}.
// To register a redirect, send {"name": "www.blue.cc", "redirectTo":
// "blue.cc"} — the target must already exist as a primary on the
// same project.
type DomainAddRequest struct {
	Name       string `json:"name"`
	RedirectTo string `json:"redirectTo,omitempty"`
}

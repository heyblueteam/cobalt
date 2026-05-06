package cobaltapi

// Domain is the public shape of a domain bound to a project.
type Domain struct {
	Name      string `json:"name"`
	CreatedAt int64  `json:"createdAt,omitempty"`
}

// DomainAddRequest is the body of POST /api/projects/{name}/domains.
type DomainAddRequest struct {
	Name string `json:"name"`
}

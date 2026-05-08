package cobaltapi

// EnvVar is the public shape of one project env var.
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// UpdatedAt is the unix-second timestamp of the most recent
	// write to this var. Omitted from set responses where the
	// server doesn't compute it.
	UpdatedAt int64 `json:"updatedAt,omitempty"`
	// Stale is true when the var was updated after the project's
	// last successful deployment — the running containers were
	// built with a different value and haven't picked this up.
	// Computed server-side. Always false on a project that has no
	// successful deployment yet (nothing to be stale relative to).
	Stale bool `json:"stale,omitempty"`
}

// EnvSetRequest is the body of POST /api/projects/{name}/env.
type EnvSetRequest struct {
	// Vars is the set of key→value pairs to upsert. An empty map is
	// accepted (no-op).
	Vars map[string]string `json:"vars"`
	// Redeploy, when true, enqueues a fresh deployment after the env
	// change lands. Default false — env can be set without deploying.
	Redeploy bool `json:"redeploy,omitempty"`
}

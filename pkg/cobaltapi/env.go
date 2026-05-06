package cobaltapi

// EnvVar is the public shape of one project env var.
type EnvVar struct {
	Key   string `json:"key"`
	Value string `json:"value"`
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

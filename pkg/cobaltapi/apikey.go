package cobaltapi

// APIKey is the public shape of a key row. The raw key string is
// intentionally absent — it's only ever returned in the
// APIKeyCreateResponse the server sends back from POST /api/apikeys.
type APIKey struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	CreatedAt  int64  `json:"createdAt"`
	LastUsedAt int64  `json:"lastUsedAt,omitempty"`
}

// APIKeyCreateRequest is the body of POST /api/apikeys.
type APIKeyCreateRequest struct {
	Name string `json:"name"`
}

// APIKeyCreateResponse carries the raw key. Returned ONCE on
// creation; clients must persist it themselves (or lose it forever —
// the server only stores the hash).
type APIKeyCreateResponse struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Key       string `json:"key"`
	CreatedAt int64  `json:"createdAt"`
}

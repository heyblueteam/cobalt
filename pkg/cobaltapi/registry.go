package cobaltapi

// RegistryCredential is the public shape of a registry login. The
// password is write-only and never returned.
type RegistryCredential struct {
	Registry  string `json:"registry"`
	Username  string `json:"username"`
	UpdatedAt int64  `json:"updatedAt"`
}

// RegistryCredentialSetRequest is the body of POST /api/registries/{registry}.
type RegistryCredentialSetRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

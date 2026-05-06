package cobaltapi

// Volume is one named docker volume bound to a project. Volumes
// outlive deployments — they hold user data — so unlike most cobalt
// resources they're keyed by project id (not deployment number) on
// the docker side. The API uses the user-friendly name (the value
// declared in cobalt.json's volumes array).
type Volume struct {
	Name     string `json:"name"`     // user-friendly name, e.g. "data"
	FullName string `json:"fullName"` // docker name, e.g. "cobalt-volume-7-data"
}

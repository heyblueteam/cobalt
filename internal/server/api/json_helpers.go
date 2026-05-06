package api

import "encoding/json"

// jsonMarshal exists because manifest.go needs json.Marshal but the
// import is in api.go and we want to keep imports tight per file.
// Stdlib's json package is cheap to import; this indirection is worth
// keeping because it makes manifest.go's import block intent-clear.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

package docker

import "sort"

// sortStrings is a tiny shim so service.go doesn't import "sort" (build.go
// already does). Keeping it here keeps each file's imports tight.
func sortStrings(s []string) { sort.Strings(s) }

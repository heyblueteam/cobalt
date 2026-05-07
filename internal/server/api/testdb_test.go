package api

import (
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/internal/storetest"
)

// openTestDB delegates to storetest.OpenDB so the api tests share one
// rqlited harness with the rest of the codebase. Wrapped here so existing
// call sites stay tight.
func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	return storetest.OpenDB(t)
}

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/heyblueteam/cobalt/internal/server/middleware"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// bootstrapKeyFilename is the file the daemon writes its first-boot API key
// to (mode 0600) when the apikeys table is empty. Operators read it once via
// `cobalt init` (or `docker compose exec cobalt cat …`) and are expected to
// rotate it. The daemon never recreates it once a key exists in the table.
const bootstrapKeyFilename = "bootstrap-api-key"

// ensureBootstrapKey mints a first API key on initial daemon startup so the
// operator can authenticate without a chicken-and-egg HTTP bootstrap endpoint.
//
// Behavior:
//   - If the apikeys table already has any rows: no-op.
//   - Otherwise: read the bootstrap-api-key file if it exists (resilient to a
//     crash between write and DB insert on a prior boot); else generate a fresh
//     random hex string, write it to {dataDir}/bootstrap-api-key (0600) before
//     touching the DB, then insert its hash with name "init-bootstrap".
//
// The write-then-insert order means a crash between steps still leaves the
// raw key on disk so the next boot can hash and persist it. A crash before the
// write leaves DB empty and no file, so the next boot retries cleanly.
func ensureBootstrapKey(ctx context.Context, db *store.DB, dataDir string, log *slog.Logger) error {
	keys, err := db.ListAPIKeys(ctx)
	if err != nil {
		return fmt.Errorf("list apikeys: %w", err)
	}
	if len(keys) > 0 {
		return nil
	}

	path := filepath.Join(dataDir, bootstrapKeyFilename)

	raw, err := readExistingBootstrapKey(path)
	if err != nil {
		return err
	}
	if raw == "" {
		raw, err = generateAndWriteBootstrapKey(path, dataDir)
		if err != nil {
			return err
		}
	}

	hash := middleware.HashAPIKey(raw)
	if _, err := db.CreateAPIKey(ctx, hash, "init-bootstrap"); err != nil {
		return fmt.Errorf("insert bootstrap api key: %w", err)
	}
	log.Info("wrote bootstrap api key", "path", path, "name", "init-bootstrap")
	return nil
}

func readExistingBootstrapKey(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

func generateAndWriteBootstrapKey(path, dataDir string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	raw := hex.EncodeToString(b)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dataDir, err)
	}
	if err := os.WriteFile(path, []byte(raw+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return raw, nil
}

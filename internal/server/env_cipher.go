package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/heyblueteam/cobalt/internal/server/encryption"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// configureEnvCipher reads the encryption key from disk, builds a
// Cipher, attaches it to the store, and runs the boot migration that
// rewrites any plaintext env_vars rows as ciphertext.
//
// Refuse-to-start semantics:
//
//   - If the key path is missing AND env_vars has at least one row,
//     the daemon exits with a clear message — silently falling back
//     to plaintext on a previously-encrypted DB would let a stolen
//     backup mix freshly-decryptable rows with the stored ones.
//   - If the key path is missing AND env_vars is empty, we run in
//     plaintext mode and log a warning. Useful for local dev / tests
//     where running `cobalt init` (which generates the key) is
//     overkill.
func configureEnvCipher(ctx context.Context, db *store.DB, keyPath string, log *slog.Logger) error {
	if keyPath == "" {
		keyPath = encryption.DefaultKeyPath
	}
	keyBytes, err := encryption.ReadKeyFromSecret(keyPath)
	if errors.Is(err, encryption.ErrKeyMissing) {
		hasRows, herr := envVarsHasAnyRow(ctx, db)
		if herr != nil {
			return fmt.Errorf("env cipher: probe env_vars: %w", herr)
		}
		if hasRows {
			return fmt.Errorf(
				"%s missing but env_vars has rows; restore the key file or "+
					"run `cobalt admin reset-encryption` to drop encrypted rows",
				keyPath,
			)
		}
		log.Warn("env cipher: key file not found, running in PLAINTEXT mode",
			"path", keyPath,
			"hint", "set up the key with `cobalt init` for production deployments")
		return nil
	}
	if err != nil {
		return fmt.Errorf("env cipher: read key: %w", err)
	}

	cipher, err := encryption.New(keyBytes)
	if err != nil {
		return fmt.Errorf("env cipher: build cipher: %w", err)
	}
	db.SetCipher(cipher)
	log.Info("env cipher: ready", "path", keyPath)

	migrated, err := db.MigrateEnvVarsToEncrypted(ctx)
	if err != nil {
		return fmt.Errorf("env cipher: migrate: %w", err)
	}
	if migrated > 0 {
		log.Info("env cipher: migrated plaintext rows to ciphertext", "count", migrated)
	}
	return nil
}

// envVarsHasAnyRow returns true if the env_vars table has at least one
// row. Used to decide whether a missing key file is fatal.
func envVarsHasAnyRow(ctx context.Context, db *store.DB) (bool, error) {
	resp, err := db.QuerySingle(ctx, `SELECT 1 FROM env_vars LIMIT 1`)
	if err != nil {
		return false, err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return false, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return false, nil
	}
	return len(results[0].Values) > 0, nil
}

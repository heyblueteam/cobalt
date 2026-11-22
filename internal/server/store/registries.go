package store

import (
	"context"
	"errors"

	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
)

// RegistryCredential is one daemon-wide registry login used to pull
// prebuilt images. Password is encrypted at rest with the store cipher.
type RegistryCredential struct {
	Registry  string
	Username  string
	Password  string
	UpdatedAt int64
}

// SetRegistryCredential inserts or replaces the login for registry.
func (db *DB) SetRegistryCredential(ctx context.Context, registry, username, password string) error {
	if err := validator.ValidateRegistryHost(registry); err != nil {
		return err
	}
	if username == "" || password == "" {
		return errors.New("store: registry username and password are required")
	}
	enc, err := db.encryptValue(password)
	if err != nil {
		return err
	}
	_, err = db.ExecuteSingle(ctx, `
		INSERT INTO registry_credentials (registry, username, password, updated_at)
		VALUES (?, ?, ?, strftime('%s', 'now'))
		ON CONFLICT(registry) DO UPDATE SET
			username = excluded.username,
			password = excluded.password,
			updated_at = excluded.updated_at
	`, registry, username, enc)
	return err
}

// GetRegistryCredential returns the login for registry, or ErrNotFound.
func (db *DB) GetRegistryCredential(ctx context.Context, registry string) (*RegistryCredential, error) {
	resp, err := db.QuerySingle(ctx, `
		SELECT registry, username, password, updated_at FROM registry_credentials WHERE registry = ?
	`, registry)
	if err != nil {
		return nil, err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return nil, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		return nil, ErrNotFound
	}
	row := results[0].Values[0]
	password, err := db.decryptValue(blobToString(row[2]))
	if err != nil {
		return nil, err
	}
	return &RegistryCredential{
		Registry:  toString(row[0]),
		Username:  toString(row[1]),
		Password:  password,
		UpdatedAt: toInt64(row[3]),
	}, nil
}

// ListRegistryCredentials returns every login without its password.
func (db *DB) ListRegistryCredentials(ctx context.Context) ([]RegistryCredential, error) {
	resp, err := db.QuerySingle(ctx, `
		SELECT registry, username, updated_at FROM registry_credentials ORDER BY registry
	`)
	if err != nil {
		return nil, err
	}
	if hasError, _, errMsg := resp.HasError(); hasError {
		return nil, errors.New(errMsg)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return nil, nil
	}
	out := make([]RegistryCredential, 0, len(results[0].Values))
	for _, row := range results[0].Values {
		out = append(out, RegistryCredential{
			Registry:  toString(row[0]),
			Username:  toString(row[1]),
			UpdatedAt: toInt64(row[2]),
		})
	}
	return out, nil
}

// DeleteRegistryCredential removes the login for registry, or returns
// ErrNotFound.
func (db *DB) DeleteRegistryCredential(ctx context.Context, registry string) error {
	resp, err := db.ExecuteSingle(ctx, `DELETE FROM registry_credentials WHERE registry = ?`, registry)
	if err != nil {
		return err
	}
	if len(resp.Results) == 0 || resp.Results[0].RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

package store

import (
	"fmt"

	"github.com/heyblueteam/cobalt/internal/server/encryption"
)

// EnvVarCipher is the subset of internal/server/encryption.Cipher the
// store needs. Defined as an interface so the encryption package can
// satisfy it without importing the store and so tests can swap in a
// passthrough or rotate to a different key mid-test.
//
// Despite the historical name (kept stable to avoid a CLI/SDK churn),
// the cipher is now used for every encrypted column in cobalt's
// rqlite — env_vars.value, github_apps.webhook_secret,
// github_apps.private_key.
type EnvVarCipher interface {
	Encrypt(plaintext []byte) (string, error)
	Decrypt(s string) ([]byte, error)
}

// SetCipher wires an encryption cipher into the store. Must be called
// before any read or write on encrypted columns that should round-trip
// plaintext. Calling with nil reverts to plaintext storage (used in
// tests and unencrypted dev installs).
func (db *DB) SetCipher(c EnvVarCipher) {
	db.cipherMu.Lock()
	db.cipher = c
	db.cipherMu.Unlock()
}

// cipherSnapshot returns the currently-configured cipher under a read
// lock. Callers operate on the returned value without holding the
// lock; rotation happens via SetCipher after a re-encrypt sweep
// completes, so a concurrent reader either sees the old cipher
// (decrypts old rows fine) or the new one (decrypts the just-rewritten
// rows fine).
func (db *DB) cipherSnapshot() EnvVarCipher {
	db.cipherMu.RLock()
	defer db.cipherMu.RUnlock()
	return db.cipher
}

// decryptValue is the read-side of the cipher contract: when a cipher
// is configured AND the stored bytes look like a v1 frame, decrypt.
// Otherwise return the bytes as-is (plaintext rows pre-migration, or
// when no cipher is wired e.g. in tests). Fail-closed on a decrypt
// error so callers don't silently inject zero into deploys.
func (db *DB) decryptValue(stored string) (string, error) {
	c := db.cipherSnapshot()
	if c == nil || !encryption.IsCiphertext(stored) {
		return stored, nil
	}
	pt, err := c.Decrypt(stored)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}

// encryptValue is the write-side. With no cipher wired, store
// plaintext (tests, unencrypted dev installs).
func (db *DB) encryptValue(plaintext string) (string, error) {
	c := db.cipherSnapshot()
	if c == nil {
		return plaintext, nil
	}
	return c.Encrypt([]byte(plaintext))
}

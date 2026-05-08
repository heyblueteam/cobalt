// Package encryption provides at-rest encryption for sensitive values
// stored in cobalt's rqlite (today: env var values; later, github app
// webhook secrets and PEMs). The key lives outside rqlite — as a Docker
// Swarm secret mounted at /run/secrets/cobalt_encryption_key — so a
// backup of the data volume alone yields ciphertext only.
//
// Wire format for stored values:
//
//	base64-url-no-padding(version || nonce || ciphertext+tag)
//
// where version is one byte (0x01 = AES-256-GCM with 32-byte key),
// nonce is 12 random bytes per encryption, and ciphertext+tag is the
// AEAD output (Seal appends the 16-byte GCM tag automatically).
//
// See plans/cobalt/cobalt-env-encryption.md for the threat model.
package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeyLen is the required key size in bytes (256-bit AES key).
const KeyLen = 32

// versionV1 marks AES-256-GCM with a 32-byte key. Stored as the first
// byte of every ciphertext frame so a future algorithm change can
// coexist with existing rows without a wholesale re-encrypt sweep.
const versionV1 byte = 0x01

// Cipher is a stateless wrapper around an AES-GCM AEAD. Construct via
// New; reuse for the lifetime of the daemon (the underlying cipher is
// safe for concurrent use).
type Cipher struct {
	aead    cipher.AEAD
	version byte
}

// New constructs a Cipher from a raw 32-byte key. Returns an error if
// the key length is wrong; never logs the bytes.
func New(key []byte) (*Cipher, error) {
	if len(key) != KeyLen {
		return nil, fmt.Errorf("encryption: key must be %d bytes, got %d", KeyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("encryption: aes init: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("encryption: gcm init: %w", err)
	}
	return &Cipher{aead: aead, version: versionV1}, nil
}

// Encrypt produces an opaque ciphertext frame for plaintext. Output is
// safe to round-trip through any UTF-8 / SQL-text-column path.
func (c *Cipher) Encrypt(plaintext []byte) (string, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("encryption: nonce: %w", err)
	}
	ct := c.aead.Seal(nil, nonce, plaintext, nil)
	frame := make([]byte, 0, 1+len(nonce)+len(ct))
	frame = append(frame, c.version)
	frame = append(frame, nonce...)
	frame = append(frame, ct...)
	return base64.RawURLEncoding.EncodeToString(frame), nil
}

// Decrypt reverses Encrypt. Errors on:
//   - input that doesn't decode as base64-url-no-pad
//   - frames whose first byte isn't a known version
//   - frames shorter than version + nonce + minimum-tag
//   - any AEAD authentication failure (tampering, wrong key)
//
// Fail-closed by design — a corrupt or wrong-key boot must NOT silently
// hand zero / placeholder values to deploys.
func (c *Cipher) Decrypt(s string) ([]byte, error) {
	frame, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("encryption: base64: %w", err)
	}
	nonceSize := c.aead.NonceSize()
	if len(frame) < 1+nonceSize+c.aead.Overhead() {
		return nil, errors.New("encryption: frame too short")
	}
	if frame[0] != c.version {
		return nil, fmt.Errorf("encryption: unknown version 0x%02x", frame[0])
	}
	nonce := frame[1 : 1+nonceSize]
	ct := frame[1+nonceSize:]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("encryption: open: %w", err)
	}
	return pt, nil
}

// IsCiphertext reports whether s is a syntactically valid frame. Used
// by the migration path to distinguish plaintext rows from
// already-encrypted ones; does NOT verify the AEAD tag (that requires
// the key, and migration callers may not yet have it).
//
// A plaintext value that happens to base64-decode and start with a
// valid version byte is theoretically ambiguous, but the probability
// for human-typed env values is effectively zero — the first byte of
// any 32-byte-aligned base64 string would have to be exactly 0x01.
func IsCiphertext(s string) bool {
	if s == "" {
		return false
	}
	frame, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return false
	}
	if len(frame) == 0 {
		return false
	}
	return frame[0] == versionV1
}

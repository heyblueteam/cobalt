package encryption

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func newTestKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, KeyLen)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	return k
}

func TestCipher_RoundTrip(t *testing.T) {
	t.Parallel()
	c, err := New(newTestKey(t))
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"",
		"hello",
		"DATABASE_URL=postgres://u:p@host:5432/db?sslmode=require",
		strings.Repeat("a", 1<<16), // 64 KiB
		"\x00\x01\x02 binary \xff\xfe",
	}
	for _, plain := range cases {
		ct, err := c.Encrypt([]byte(plain))
		if err != nil {
			t.Fatalf("Encrypt(%q): %v", plain, err)
		}
		if ct == plain {
			t.Errorf("ciphertext equals plaintext: %q", plain)
		}
		got, err := c.Decrypt(ct)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if !bytes.Equal([]byte(plain), got) {
			t.Errorf("round trip: got %q, want %q", got, plain)
		}
	}
}

func TestCipher_DistinctNoncesAcrossEncrypts(t *testing.T) {
	t.Parallel()
	c, _ := New(newTestKey(t))
	a, _ := c.Encrypt([]byte("same plaintext"))
	b, _ := c.Encrypt([]byte("same plaintext"))
	if a == b {
		t.Error("two encryptions of the same plaintext produced identical ciphertext (nonce reused?)")
	}
}

func TestCipher_TamperedCiphertextRejected(t *testing.T) {
	t.Parallel()
	c, _ := New(newTestKey(t))
	ct, _ := c.Encrypt([]byte("important"))
	// Decode, flip the last byte (part of the GCM tag), re-encode.
	frame, _ := base64.RawURLEncoding.DecodeString(ct)
	frame[len(frame)-1] ^= 0x80
	tampered := base64.RawURLEncoding.EncodeToString(frame)
	if _, err := c.Decrypt(tampered); err == nil {
		t.Error("tampered ciphertext decrypted without error")
	}
}

func TestCipher_WrongKeyRejected(t *testing.T) {
	t.Parallel()
	c1, _ := New(newTestKey(t))
	c2, _ := New(newTestKey(t))
	ct, _ := c1.Encrypt([]byte("secret"))
	if _, err := c2.Decrypt(ct); err == nil {
		t.Error("decrypt with wrong key did not error")
	}
}

func TestCipher_UnknownVersionRejected(t *testing.T) {
	t.Parallel()
	c, _ := New(newTestKey(t))
	ct, _ := c.Encrypt([]byte("hi"))
	frame, _ := base64.RawURLEncoding.DecodeString(ct)
	frame[0] = 0xff // bogus version
	if _, err := c.Decrypt(base64.RawURLEncoding.EncodeToString(frame)); err == nil {
		t.Error("unknown-version frame decrypted without error")
	}
}

func TestCipher_BadInput(t *testing.T) {
	t.Parallel()
	c, _ := New(newTestKey(t))
	for _, bad := range []string{
		"!!!not base64!!!",
		"",
		base64.RawURLEncoding.EncodeToString([]byte{0x01}), // too short
	} {
		if _, err := c.Decrypt(bad); err == nil {
			t.Errorf("Decrypt(%q): expected error", bad)
		}
	}
}

func TestCipher_KeyLenValidation(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, 1, 16, 31, 33, 64} {
		_, err := New(make([]byte, n))
		if err == nil {
			t.Errorf("New(%d-byte key): expected error", n)
		}
	}
}

func TestIsCiphertext(t *testing.T) {
	t.Parallel()
	c, _ := New(newTestKey(t))
	ct, _ := c.Encrypt([]byte("x"))
	if !IsCiphertext(ct) {
		t.Errorf("IsCiphertext(real ciphertext) = false")
	}
	for _, plain := range []string{
		"",
		"DATABASE_URL=postgres://u:p@h/db",
		"some-random-token-abc123",
		"hello world",
	} {
		if IsCiphertext(plain) {
			t.Errorf("IsCiphertext(%q) = true (false positive)", plain)
		}
	}
}

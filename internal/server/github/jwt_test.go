package github

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"
)

// generateTestKey produces a fresh RSA-2048 key as a PEM string.
func generateTestKey(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	return string(pemBytes)
}

func TestSignAppJWT_RoundTrip(t *testing.T) {
	t.Parallel()
	keyPEM := generateTestKey(t)
	now := time.Unix(1_700_000_000, 0)
	const appID int64 = 12345

	tok, err := SignAppJWT(appID, keyPEM, now)
	if err != nil {
		t.Fatalf("SignAppJWT: %v", err)
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("got %d parts, want 3", len(parts))
	}

	// Header
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("header decode: %v", err)
	}
	var header map[string]string
	_ = json.Unmarshal(headerJSON, &header)
	if header["alg"] != "RS256" {
		t.Errorf("alg: got %q", header["alg"])
	}
	if header["typ"] != "JWT" {
		t.Errorf("typ: got %q", header["typ"])
	}

	// Claims
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("claims decode: %v", err)
	}
	var claims map[string]any
	_ = json.Unmarshal(claimsJSON, &claims)

	if iss, _ := claims["iss"].(float64); int64(iss) != appID {
		t.Errorf("iss: got %v, want %d", claims["iss"], appID)
	}
	if iat, _ := claims["iat"].(float64); int64(iat) != now.Add(-5*time.Second).Unix() {
		t.Errorf("iat: got %v", claims["iat"])
	}
	if exp, _ := claims["exp"].(float64); int64(exp) != now.Add(AppJWTTTL).Unix() {
		t.Errorf("exp: got %v", claims["exp"])
	}

	// Signature verifies against the public key.
	pubKey := pubKeyFromPEM(t, keyPEM)
	signingInput := []byte(parts[0] + "." + parts[1])
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("sig decode: %v", err)
	}
	digest := sha256.Sum256(signingInput)
	if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, digest[:], sig); err != nil {
		t.Errorf("VerifyPKCS1v15: %v", err)
	}
}

func TestSignAppJWT_PKCS8Key(t *testing.T) {
	t.Parallel()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))

	if _, err := SignAppJWT(1, keyPEM, time.Now()); err != nil {
		t.Errorf("PKCS8 should be accepted: %v", err)
	}
}

func TestSignAppJWT_BadPEM(t *testing.T) {
	t.Parallel()
	if _, err := SignAppJWT(1, "not a pem", time.Now()); err == nil {
		t.Error("want error for non-PEM input")
	}
}

func pubKeyFromPEM(t *testing.T, pemStr string) *rsa.PublicKey {
	t.Helper()
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		t.Fatal("no PEM block")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		// Try PKCS8
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return &k.(*rsa.PrivateKey).PublicKey
	}
	return &key.PublicKey
}

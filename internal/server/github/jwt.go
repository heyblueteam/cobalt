// Package github implements cobalt's GitHub App integration: JWT signing,
// installation access-token caching, webhook signature verification, and
// the manifest-flow primitives used to register new apps.
//
// The package targets the public GitHub REST API. Endpoints are documented
// at https://docs.github.com/en/rest/apps. We talk to api.github.com over
// HTTPS only; tests inject a custom baseURL pointing at httptest.Server.
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
	"errors"
	"fmt"
	"strings"
	"time"
)

// AppJWTTTL is the lifetime of an app JWT. GitHub allows up to 10 minutes;
// using a short window minimizes blast radius if a token is leaked.
const AppJWTTTL = 30 * time.Second

// SignAppJWT returns a JWT signed with the App's RSA private key (RS256).
//
// The token authenticates the App itself (not an installation) and is used
// to mint installation access tokens. Standard claims:
//
//	iat = now - 5s   (allow for small clock skew with GitHub)
//	exp = now + 30s
//	iss = appID
//
// Reference: https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app
func SignAppJWT(appID int64, privateKeyPEM string, now time.Time) (string, error) {
	key, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return "", fmt.Errorf("github: parse private key: %w", err)
	}

	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	claims := map[string]any{
		"iat": now.Add(-5 * time.Second).Unix(),
		"exp": now.Add(AppJWTTTL).Unix(),
		"iss": appID,
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("github: marshal header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("github: marshal claims: %w", err)
	}

	signingInput := base64URL(headerJSON) + "." + base64URL(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))

	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("github: sign jwt: %w", err)
	}

	return signingInput + "." + base64URL(sig), nil
}

// parseRSAPrivateKey parses a PEM-encoded RSA private key. GitHub gives out
// PKCS#1 PEMs ("BEGIN RSA PRIVATE KEY"); we also accept PKCS#8 for forward
// compatibility.
func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemStr)))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("PKCS8 key is not RSA")
		}
		return rsaKey, nil
	}
	return nil, errors.New("could not parse key as PKCS1 or PKCS8")
}

// base64URL is the URL-safe, no-padding base64 encoding JWTs use.
func base64URL(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

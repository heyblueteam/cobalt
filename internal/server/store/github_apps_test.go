package store

import (
	"context"
	"strings"
	"testing"

	"github.com/heyblueteam/cobalt/internal/server/encryption"
	rqlitehttp "github.com/rqlite/rqlite-go-http"
)

const samplePEM = `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA-fake-key-data-for-testing-only-fake-key-data
-----END RSA PRIVATE KEY-----`

const sampleWebhookSecret = "whsec_super_secret_value_abcdef123456"

func TestCreateGithubApp_RoundTripsThroughCipher(t *testing.T) {
	db := openTestDB(t)
	db.SetCipher(newTestCipher(t))

	id, err := db.CreateGithubApp(context.Background(), GithubApp{
		AppID:         12345,
		Owner:         "heyblueteam",
		PrivateKey:    samplePEM,
		WebhookSecret: sampleWebhookSecret,
	})
	if err != nil {
		t.Fatalf("CreateGithubApp: %v", err)
	}

	got, err := db.GetGithubApp(context.Background(), id)
	if err != nil {
		t.Fatalf("GetGithubApp: %v", err)
	}
	if got.PrivateKey != samplePEM {
		t.Errorf("PrivateKey round-trip: got %q want %q", got.PrivateKey, samplePEM)
	}
	if got.WebhookSecret != sampleWebhookSecret {
		t.Errorf("WebhookSecret round-trip: got %q want %q", got.WebhookSecret, sampleWebhookSecret)
	}
}

func TestCreateGithubApp_RawRowIsCiphertext(t *testing.T) {
	db := openTestDB(t)
	db.SetCipher(newTestCipher(t))

	_, err := db.CreateGithubApp(context.Background(), GithubApp{
		AppID:         54321,
		Owner:         "heyblueteam",
		PrivateKey:    samplePEM,
		WebhookSecret: sampleWebhookSecret,
	})
	if err != nil {
		t.Fatalf("CreateGithubApp: %v", err)
	}

	// Read straight from rqlite, bypassing scanGithubApp's decrypt.
	resp, err := db.QuerySingle(context.Background(),
		`SELECT private_key, webhook_secret FROM github_apps WHERE app_id = ?`, 54321)
	if err != nil {
		t.Fatalf("QuerySingle: %v", err)
	}
	results := resp.GetQueryResults()
	if len(results) == 0 || len(results[0].Values) == 0 {
		t.Fatal("row missing")
	}
	row := results[0].Values[0]
	pemStored := toString(row[0])
	whStored := toString(row[1])

	if pemStored == samplePEM {
		t.Error("private_key stored as plaintext (encryption not applied)")
	}
	if !encryption.IsCiphertext(pemStored) {
		t.Errorf("private_key not a v1 ciphertext frame: %q", pemStored)
	}
	if strings.Contains(pemStored, "BEGIN RSA") {
		t.Error("private_key contains the PEM marker plaintext")
	}

	if whStored == sampleWebhookSecret {
		t.Error("webhook_secret stored as plaintext")
	}
	if !encryption.IsCiphertext(whStored) {
		t.Errorf("webhook_secret not a v1 ciphertext frame: %q", whStored)
	}
}

func TestMigrateGithubAppsToEncrypted_EncryptsExistingPlaintext(t *testing.T) {
	db := openTestDB(t)

	// Seed a plaintext row directly (no cipher wired during insert),
	// simulating a daemon upgrade that pre-dated github_apps encryption.
	stmt, err := rqlitehttp.NewSQLStatement(
		`INSERT INTO github_apps (app_id, owner, private_key, webhook_secret, created_at)
		 VALUES (?, ?, ?, ?, strftime('%s','now'))`,
		99999, "old-org", samplePEM, sampleWebhookSecret,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Execute(context.Background(), rqlitehttp.SQLStatements{stmt}, nil); err != nil {
		t.Fatal(err)
	}

	db.SetCipher(newTestCipher(t))
	n, err := db.MigrateGithubAppsToEncrypted(context.Background())
	if err != nil {
		t.Fatalf("MigrateGithubAppsToEncrypted: %v", err)
	}
	if n != 1 {
		t.Errorf("migrated %d, want 1", n)
	}

	// Read back via the decrypting path; values should match the
	// pre-migration plaintext.
	got, err := db.GetGithubAppByAppID(context.Background(), 99999)
	if err != nil {
		t.Fatalf("GetGithubAppByAppID: %v", err)
	}
	if got.PrivateKey != samplePEM {
		t.Errorf("PrivateKey post-migration: got %q want %q", got.PrivateKey, samplePEM)
	}
	if got.WebhookSecret != sampleWebhookSecret {
		t.Errorf("WebhookSecret post-migration: got %q want %q", got.WebhookSecret, sampleWebhookSecret)
	}

	// Idempotent: a second migration is a no-op.
	n2, err := db.MigrateGithubAppsToEncrypted(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n2 != 0 {
		t.Errorf("second migration migrated %d, want 0 (idempotency)", n2)
	}
}

package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestVerifySignature_Valid(t *testing.T) {
	t.Parallel()
	body := []byte(`{"foo":"bar"}`)
	const secret = "supersecret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if err := VerifySignature(secret, body, header); err != nil {
		t.Errorf("valid sig rejected: %v", err)
	}
}

func TestVerifySignature_TamperedBody(t *testing.T) {
	t.Parallel()
	body := []byte(`{"foo":"bar"}`)
	const secret = "supersecret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if err := VerifySignature(secret, []byte(`{"foo":"BAZ"}`), header); err == nil {
		t.Error("tampered body accepted")
	}
}

func TestVerifySignature_MissingPrefix(t *testing.T) {
	t.Parallel()
	if err := VerifySignature("s", []byte("b"), "abc123"); err == nil {
		t.Error("missing sha256= prefix accepted")
	}
}

func TestVerifySignature_BadHex(t *testing.T) {
	t.Parallel()
	if err := VerifySignature("s", []byte("b"), "sha256=zzzz"); err == nil {
		t.Error("non-hex signature accepted")
	}
}

func TestVerifySignature_WrongSecret(t *testing.T) {
	t.Parallel()
	body := []byte(`{"a":1}`)
	mac := hmac.New(sha256.New, []byte("right"))
	mac.Write(body)
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if err := VerifySignature("wrong", body, header); err == nil {
		t.Error("wrong secret accepted")
	}
}

func TestParsePush(t *testing.T) {
	t.Parallel()
	body := []byte(`{
        "ref": "refs/heads/main",
        "before": "0000000000000000000000000000000000000000",
        "after": "abc123",
        "deleted": false,
        "repository": {"id": 42, "full_name": "acme/api"},
        "installation": {"id": 999}
    }`)
	ev, err := ParsePush(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ev.Branch() != "main" {
		t.Errorf("Branch: %q", ev.Branch())
	}
	if ev.Repository.FullName != "acme/api" {
		t.Errorf("FullName: %q", ev.Repository.FullName)
	}
	if ev.Installation == nil || ev.Installation.ID != 999 {
		t.Errorf("Installation: %+v", ev.Installation)
	}
	if ev.IsBranchDelete() {
		t.Error("not a branch delete")
	}
}

func TestPushEvent_Branch_NonBranchRef(t *testing.T) {
	t.Parallel()
	ev := PushEvent{Ref: "refs/tags/v1.0"}
	if ev.Branch() != "" {
		t.Errorf("tag ref should produce empty branch, got %q", ev.Branch())
	}
}

func TestPushEvent_IsBranchDelete(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ev   PushEvent
		want bool
	}{
		{"deleted flag", PushEvent{Deleted: true}, true},
		{"after all-zeros", PushEvent{After: strings.Repeat("0", 40)}, true},
		{"normal push", PushEvent{After: "abc123"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ev.IsBranchDelete(); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseInstallation(t *testing.T) {
	t.Parallel()
	body := []byte(`{
        "action": "created",
        "installation": {
            "id": 555,
            "app_id": 12345,
            "account": {"login": "acme", "id": 7}
        },
        "repositories": [
            {"id": 1, "full_name": "acme/api", "private": true},
            {"id": 2, "full_name": "acme/web", "private": false}
        ]
    }`)
	ev, err := ParseInstallation(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ev.Action != "created" {
		t.Errorf("Action: %q", ev.Action)
	}
	if ev.Installation.ID != 555 || ev.Installation.AppID != 12345 {
		t.Errorf("Installation: %+v", ev.Installation)
	}
	if ev.Installation.Account.Login != "acme" {
		t.Errorf("account login: %q", ev.Installation.Account.Login)
	}
	if len(ev.Repositories) != 2 {
		t.Errorf("repositories: got %d, want 2", len(ev.Repositories))
	}
}

func TestParseInstallationRepositories(t *testing.T) {
	t.Parallel()
	body := []byte(`{
        "action": "added",
        "installation": {"id": 555, "app_id": 12345, "account": {"login": "acme", "id": 7}},
        "repositories_added": [{"id": 3, "full_name": "acme/forms"}],
        "repositories_removed": []
    }`)
	ev, err := ParseInstallationRepositories(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ev.Action != "added" {
		t.Errorf("Action: %q", ev.Action)
	}
	if len(ev.RepositoriesAdded) != 1 || ev.RepositoriesAdded[0].FullName != "acme/forms" {
		t.Errorf("added: %+v", ev.RepositoriesAdded)
	}
}

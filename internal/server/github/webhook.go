package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

// HeaderEvent is the GitHub webhook event-type header.
const (
	HeaderEvent     = "X-GitHub-Event"
	HeaderSignature = "X-Hub-Signature-256"
	HeaderDelivery  = "X-GitHub-Delivery"
)

// Event names we care about. Anything not in this list is silently ignored.
const (
	EventPush                    = "push"
	EventInstallation            = "installation"
	EventInstallationRepositories = "installation_repositories"
)

// VerifySignature returns nil iff signatureHeader is a valid HMAC-SHA256
// signature of body using webhookSecret. Comparison is constant time.
//
// signatureHeader is the raw value of X-Hub-Signature-256, e.g.
// "sha256=ab12...". Empty header or malformed prefix produces an error.
func VerifySignature(webhookSecret string, body []byte, signatureHeader string) error {
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return errors.New("github: signature header missing sha256= prefix")
	}
	got, err := hex.DecodeString(signatureHeader[len(prefix):])
	if err != nil {
		return errors.New("github: signature header is not valid hex")
	}
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	want := mac.Sum(nil)
	if !hmac.Equal(got, want) {
		return errors.New("github: signature mismatch")
	}
	return nil
}

// PushEvent is the subset of GitHub's push event we use. Full schema:
// https://docs.github.com/en/webhooks/webhook-events-and-payloads#push
type PushEvent struct {
	Ref        string             `json:"ref"`        // refs/heads/main
	Before     string             `json:"before"`     // 40-hex previous tip
	After      string             `json:"after"`      // 40-hex new tip
	Deleted    bool               `json:"deleted"`    // branch-delete
	Repository PushRepository     `json:"repository"`
	Installation *PushInstallation `json:"installation,omitempty"`
}

// Branch returns the branch name from Ref ("refs/heads/<branch>"), or ""
// if Ref isn't a branch ref (could be a tag, etc.).
func (p PushEvent) Branch() string {
	const prefix = "refs/heads/"
	if !strings.HasPrefix(p.Ref, prefix) {
		return ""
	}
	return p.Ref[len(prefix):]
}

// IsBranchDelete reports whether this push deletes a branch. We never
// deploy on branch-deletes.
func (p PushEvent) IsBranchDelete() bool {
	return p.Deleted || p.After == strings.Repeat("0", 40)
}

// PushRepository is the subset of the push event's "repository" object.
type PushRepository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"` // "owner/repo"
}

// PushInstallation is the subset of the push event's "installation" object.
// It is non-nil only when the push came from a GitHub App-installed repo.
type PushInstallation struct {
	ID int64 `json:"id"`
}

// InstallationEvent fires when a user installs / uninstalls a GitHub App.
type InstallationEvent struct {
	Action       string                       `json:"action"` // "created", "deleted", "suspend", "unsuspend"
	Installation InstallationEventInstallation `json:"installation"`
	Repositories []InstallationEventRepo      `json:"repositories"`
}

// InstallationEventInstallation contains the installation row we'd insert.
type InstallationEventInstallation struct {
	ID      int64                       `json:"id"`
	Account InstallationEventAccount    `json:"account"`
	AppID   int64                       `json:"app_id"`
}

// InstallationEventAccount is the org or user the App is installed on.
type InstallationEventAccount struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// InstallationEventRepo is one repo in an installation event payload.
type InstallationEventRepo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	Private  bool   `json:"private"`
}

// InstallationRepositoriesEvent fires when the user adds or removes repos
// from an existing installation.
type InstallationRepositoriesEvent struct {
	Action               string                       `json:"action"` // "added", "removed"
	Installation         InstallationEventInstallation `json:"installation"`
	RepositoriesAdded    []InstallationEventRepo      `json:"repositories_added"`
	RepositoriesRemoved  []InstallationEventRepo      `json:"repositories_removed"`
}

// ParsePush, ParseInstallation, and ParseInstallationRepositories decode
// the typed event from the raw webhook body. They exist so the webhook
// receiver can dispatch to the right struct after looking at HeaderEvent.
func ParsePush(b []byte) (*PushEvent, error) {
	var ev PushEvent
	if err := json.Unmarshal(b, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

func ParseInstallation(b []byte) (*InstallationEvent, error) {
	var ev InstallationEvent
	if err := json.Unmarshal(b, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

func ParseInstallationRepositories(b []byte) (*InstallationRepositoriesEvent, error) {
	var ev InstallationRepositoriesEvent
	if err := json.Unmarshal(b, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

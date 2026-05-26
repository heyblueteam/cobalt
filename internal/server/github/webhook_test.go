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

// TestPushEvent_TouchesPath covers the path-filtered dispatch helper.
// The conservative cases (no commits, truncated push) MUST return true
// — skipping a deploy on incomplete information would be a regression
// vs the pre-monorepo behavior of always deploying.
func TestPushEvent_TouchesPath(t *testing.T) {
	t.Parallel()

	mk := func(commits ...PushCommit) PushEvent {
		return PushEvent{Commits: commits}
	}

	cases := []struct {
		name string
		ev   PushEvent
		path string
		want bool
	}{
		{
			name: "empty_path_is_repo_root_always_touched",
			ev:   mk(PushCommit{Modified: []string{"README.md"}}),
			path: "",
			want: true,
		},
		{
			name: "modified_under_subdir",
			ev:   mk(PushCommit{Modified: []string{"services/api/main.go"}}),
			path: "services/api",
			want: true,
		},
		{
			name: "added_under_subdir",
			ev:   mk(PushCommit{Added: []string{"api/new.go"}}),
			path: "api",
			want: true,
		},
		{
			name: "removed_under_subdir",
			ev:   mk(PushCommit{Removed: []string{"api/old.go"}}),
			path: "api",
			want: true,
		},
		{
			name: "sibling_subdir_not_touched",
			ev:   mk(PushCommit{Modified: []string{"web/index.ts"}}),
			path: "api",
			want: false,
		},
		{
			name: "prefix_match_only_at_segment_boundary",
			// "apiary/foo" must NOT match path "api". Without the
			// trailing-slash check this would be a false positive.
			ev:   mk(PushCommit{Modified: []string{"apiary/foo.go"}}),
			path: "api",
			want: false,
		},
		{
			name: "exact_path_match_as_a_file",
			// edge: a file literally named "api" at repo root, with
			// path == "api". Treat as touched — we don't have enough
			// signal to distinguish "the project lives in dir api/"
			// from "edited the file named api", and the conservative
			// answer is deploy.
			ev:   mk(PushCommit{Modified: []string{"api"}}),
			path: "api",
			want: true,
		},
		{
			name: "multiple_commits_one_touches",
			ev: mk(
				PushCommit{Modified: []string{"web/x.ts"}},
				PushCommit{Modified: []string{"api/y.go"}},
			),
			path: "api",
			want: true,
		},
		{
			name: "no_commits_falls_back_to_deploy",
			// Defensive: empty commits[] could be an event shape we
			// haven't seen. Deploy rather than silently skip.
			ev:   mk(),
			path: "api",
			want: true,
		},
		{
			name: "twenty_commits_treated_as_truncated",
			// GitHub caps commits[] at 20 in the push payload. We
			// can't see past that, so deploy.
			ev: PushEvent{Commits: func() []PushCommit {
				out := make([]PushCommit, 20)
				for i := range out {
					out[i] = PushCommit{Modified: []string{"web/x.ts"}}
				}
				return out
			}()},
			path: "api",
			want: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.ev.TouchesPath(c.path); got != c.want {
				t.Errorf("TouchesPath(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

// TestParsePush_DecodesCommits pins that the commits[] file lists are
// extracted from a representative GitHub push payload. If GitHub
// renames these fields (unlikely — they're stable) this would catch it.
func TestParsePush_DecodesCommits(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"ref": "refs/heads/main",
		"after": "abc",
		"repository": {"id": 1, "full_name": "acme/monorepo"},
		"commits": [
			{"added": ["api/new.go"], "removed": [], "modified": ["api/main.go"]},
			{"added": [], "removed": ["web/old.ts"], "modified": []}
		]
	}`)
	ev, err := ParsePush(body)
	if err != nil {
		t.Fatalf("ParsePush: %v", err)
	}
	if len(ev.Commits) != 2 {
		t.Fatalf("Commits: got %d, want 2", len(ev.Commits))
	}
	if !ev.TouchesPath("api") {
		t.Error("expected api/ to be touched")
	}
	if !ev.TouchesPath("web") {
		t.Error("expected web/ to be touched (removed file)")
	}
	if ev.TouchesPath("docs") {
		t.Error("expected docs/ untouched")
	}
}

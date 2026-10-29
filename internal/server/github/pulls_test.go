package github

import (
	"context"
	"net/http"
	"testing"
)

func TestListPullRequestsForCommit(t *testing.T) {
	t.Parallel()
	f := newFakeAPI(t)
	f.on("/repos/heyblueteam/blue/commits/abc123/pulls", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "token install-token" {
			t.Errorf("authorization: got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"number": 4310, "html_url": "https://github.com/heyblueteam/blue/pull/4310",
			 "merged": true, "merged_at": "2026-10-29T10:00:00Z"}
		]`))
	})

	prs, err := f.client().ListPullRequestsForCommit(context.Background(), "install-token", "heyblueteam/blue", "abc123")
	if err != nil {
		t.Fatalf("ListPullRequestsForCommit: %v", err)
	}
	if len(prs) != 1 || prs[0].Number != 4310 || prs[0].MergedAt == nil {
		t.Fatalf("prs: got %+v", prs)
	}
}

func TestListPullRequestsForCommit_EmptyWhenDirectPush(t *testing.T) {
	t.Parallel()
	f := newFakeAPI(t)
	f.on("/repos/heyblueteam/blue/commits/abc123/pulls", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})

	prs, err := f.client().ListPullRequestsForCommit(context.Background(), "install-token", "heyblueteam/blue", "abc123")
	if err != nil {
		t.Fatalf("ListPullRequestsForCommit: %v", err)
	}
	if len(prs) != 0 {
		t.Fatalf("prs: got %+v, want empty", prs)
	}
}

func TestListPullRequestsForCommit_RequiresToken(t *testing.T) {
	t.Parallel()
	f := newFakeAPI(t)
	if _, err := f.client().ListPullRequestsForCommit(context.Background(), "", "heyblueteam/blue", "abc123"); err == nil {
		t.Fatal("expected an error for an empty token")
	}
}

package api

import "testing"

func TestScaleLogicalNameSupportsStableWebAndNumberedSiblings(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ full, want string }{
		{"cobalt-web-7", "web"}, {"renamed-42-worker", "worker"}, {"renamed-42-embed", "embed"}, {"renamed-42-web", "web"},
	} {
		if got := scaleLogicalName(tc.full, "renamed", 7, 42); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.full, got, tc.want)
		}
	}
}

package validator

import (
	"errors"
	"fmt"
	"path"
	"strings"
)

// ErrProjectPathInvalid is returned when the sub-path inside a repo
// has a shape the deploy pipeline won't accept.
var ErrProjectPathInvalid = errors.New("cobaltapi: project path is invalid")

// MaxProjectPathLen caps the stored sub-path length. Generous: even
// deeply-nested monorepos rarely exceed ~80 chars; 256 leaves headroom
// without inviting pathological values into logs and Docker contexts.
const MaxProjectPathLen = 256

// ValidateProjectPath checks a repo-relative sub-path used by the
// deploy pipeline to locate a project's cobalt.json + build context.
// Empty is valid and means "repo root" — every existing project.
//
// Rejects:
//   - paths longer than MaxProjectPathLen
//   - absolute paths (`/foo`) — the path is repo-relative
//   - parent traversal (`..`, `foo/../bar`) — would escape the repo
//   - `.` (alone or as a component) — repo root is spelled `""`, not `"."`
//   - null bytes — defensive
//   - leading/trailing slashes — ambiguous; the canonical form is
//     `api` or `services/api`, not `/api` or `api/`
//   - backslashes — Windows path syntax; cobalt deploy paths are
//     POSIX-only since git + the docker daemon are POSIX-only
func ValidateProjectPath(p string) error {
	if p == "" {
		return nil
	}
	if len(p) > MaxProjectPathLen {
		return fmt.Errorf("%w: too long (%d > %d)", ErrProjectPathInvalid, len(p), MaxProjectPathLen)
	}
	// `.` survives path.Clean unchanged (clean(".") == "."), so the
	// canonical-form check below won't catch it. Reject explicitly so
	// repo root has exactly one spelling: "".
	if p == "." {
		return fmt.Errorf("%w: use empty string for repo root, not %q", ErrProjectPathInvalid, p)
	}
	if strings.ContainsRune(p, 0) {
		return fmt.Errorf("%w: contains null byte", ErrProjectPathInvalid)
	}
	if strings.ContainsRune(p, '\\') {
		return fmt.Errorf("%w: contains backslash; use forward slashes", ErrProjectPathInvalid)
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("%w: must be repo-relative (no leading slash)", ErrProjectPathInvalid)
	}
	if strings.HasSuffix(p, "/") {
		return fmt.Errorf("%w: must not end with a slash", ErrProjectPathInvalid)
	}
	// path.Clean collapses redundant separators and `.` components.
	// If the cleaned form differs from the input, the input was sloppy
	// (e.g. `api//web`, `./api`) — reject so the stored value is
	// always canonical.
	if cleaned := path.Clean(p); cleaned != p {
		return fmt.Errorf("%w: not in canonical form (got %q, want %q)", ErrProjectPathInvalid, p, cleaned)
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return fmt.Errorf("%w: parent traversal (`..`) not allowed", ErrProjectPathInvalid)
		}
		if part == "" {
			// path.Clean already collapses these, but defense in
			// depth in case of future logic changes.
			return fmt.Errorf("%w: empty segment", ErrProjectPathInvalid)
		}
	}
	return nil
}

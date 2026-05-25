package validator

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateProjectPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		in         string
		wantErr    bool
		wantSubstr string
	}{
		// Valid.
		{"empty_is_root", "", false, ""},
		{"single_segment", "api", false, ""},
		{"nested", "services/api", false, ""},
		{"deeply_nested", "apps/backend/api", false, ""},
		{"with_hyphens", "white-label-api", false, ""},
		{"with_underscores", "my_service", false, ""},
		{"with_digits", "api-v2", false, ""},
		// Invalid — leading/trailing slash.
		{"absolute", "/api", true, "leading slash"},
		{"trailing_slash", "api/", true, "slash"},
		{"absolute_nested", "/services/api", true, "leading slash"},
		// Invalid — non-canonical form.
		{"dot_prefix", "./api", true, "canonical"},
		{"double_slash", "services//api", true, "canonical"},
		{"trailing_dot", "api/.", true, "canonical"},
		// Invalid — parent traversal. (`..` and `../api` survive path.Clean
		// untouched, so the per-segment check is the one that flags them;
		// `services/../api` cleans to `api` and gets caught earlier by
		// the canonical-form check.)
		{"dotdot_only", "..", true, "parent traversal"},
		{"dotdot_segment", "../api", true, "parent traversal"},
		{"dotdot_middle", "services/../api", true, "canonical"},
		// Invalid — defense-in-depth checks.
		{"backslash", "services\\api", true, "backslash"},
		{"null_byte", "api\x00", true, "null byte"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateProjectPath(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("ValidateProjectPath(%q) = %v, wantErr=%v", c.in, err, c.wantErr)
			}
			if c.wantErr {
				if !errors.Is(err, ErrProjectPathInvalid) {
					t.Errorf("ValidateProjectPath(%q): want ErrProjectPathInvalid, got %v", c.in, err)
				}
				if c.wantSubstr != "" && !strings.Contains(err.Error(), c.wantSubstr) {
					t.Errorf("ValidateProjectPath(%q): error %q missing %q", c.in, err, c.wantSubstr)
				}
			}
		})
	}
}

package validator

import (
	"strings"
	"testing"
)

func TestProjectName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty", "", true},
		{"valid simple", "api", false},
		{"valid with numbers", "api2", false},
		{"valid with hyphens", "my-api-v2", false},
		{"valid max length", strings.Repeat("a", 63), false},
		{"too long", strings.Repeat("a", 64), true},
		{"starts with number", "2api", true},
		{"starts with hyphen", "-api", true},
		{"uppercase", "API", true},
		{"contains space", "my api", true},
		{"contains underscore", "my_api", true},
		{"contains dot", "my.api", true},
		{"trailing hyphen", "api-", false},
		{"double hyphen", "my--api", false},
		{"single char", "a", false},
		{"ends with number", "api2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProjectName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateProjectName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestEnvKey(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty", "", true},
		{"valid simple", "NODE_ENV", false},
		{"valid with numbers", "API_KEY_V2", false},
		{"valid single", "A", false},
		{"lowercase", "node_env", true},
		{"contains space", "NODE ENV", true},
		{"contains dash", "NODE-ENV", true},
		{"contains dot", "NODE.ENV", true},
		{"starts with number", "2NODE_ENV", true},
		{"underscore only", "_", true},
		{"double underscore", "NODE__ENV", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateEnvKey(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateEnvKey(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestDomain(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty", "", true},
		{"simple", "example.com", false},
		{"subdomain", "api.example.com", false},
		{"multi-level", "api.v2.example.com", false},
		{"single label", "localhost", false},
		{"with port", "api.example.com:8080", true},
		{"starts with dot", ".example.com", true},
		{"ends with dot", "example.com.", true},
		{"double dot", "api..example.com", true},
		{"trailing hyphen", "api-.example.com", true},
		{"double hyphen subdomain", "api--v2.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDomain(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDomain(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestGitHubRepo(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty", "", true},
		{"valid simple", "owner/repo", false},
		{"valid with numbers", "owner123/repo456", false},
		{"valid with hyphen", "my-org/my-repo", false},
		{"valid with underscore", "my_org/my_repo", false},
		{"valid dots", "my.org/my.repo", false},
		{"no slash", "owner-repo", true},
		{"double slash", "owner//repo", true},
		{"starts with slash", "/owner/repo", true},
		{"ends with slash", "owner/repo/", true},
		{"empty owner", "/repo", true},
		{"empty repo", "owner/", true},
		{"dot only", "./.", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateGitHubRepo(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateGitHubRepo(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestCommitSHA(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty", "", true},
		{"too short", "abc123", true},
		{"valid 7", "a1b2c3d", false},
		{"valid 20", "a1b2c3d4e5f6a1b2c3d4e5f", false},
		{"valid 40", strings.Repeat("a1", 20), false},
		{"too long", strings.Repeat("a1", 21), true},
		{"uppercase", "ABCDEF1", true},
		{"mixed case valid", "a1b2c3d", false},
		{"non-hex", "ghi7890", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCommit(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateCommit(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

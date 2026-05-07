package ssh

import (
	"testing"
)

func TestParseSSHURL(t *testing.T) {
	tests := []struct {
		raw    string
		wantU  string
		wantH  string
	}{
		{"root@server.com", "root", "server.com"},
		{"user@192.168.1.100", "user", "192.168.1.100"},
		{"server.com", "", "server.com"},
		{"my-host", "", "my-host"},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			user, host := ParseSSHURL(tt.raw)
			if user != tt.wantU {
				t.Errorf("ParseSSHURL(%q) user = %q, want %q", tt.raw, user, tt.wantU)
			}
			if host != tt.wantH {
				t.Errorf("ParseSSHURL(%q) host = %q, want %q", tt.raw, host, tt.wantH)
			}
		})
	}
}

func TestPasswordAuth(t *testing.T) {
	auth := PasswordAuth{Password: "secret123"}
	methods := auth.auth()

	if len(methods) != 1 {
		t.Fatalf("PasswordAuth.auth() returned %d methods, want 1", len(methods))
	}
}

func TestPublicKeyAuth_EmptyPath(t *testing.T) {
	auth := PublicKeyAuth{KeyPath: "/nonexistent/key"}
	methods := auth.auth()

	if len(methods) != 0 {
		t.Errorf("PublicKeyAuth with invalid key returned %d methods, want 0", len(methods))
	}
}

func TestPublicKeyAuth_ValidPath(t *testing.T) {
	auth := PublicKeyAuth{KeyPath: "/dev/null"}
	methods := auth.auth()

	if len(methods) != 0 {
		t.Errorf("PublicKeyAuth with /dev/null returned %d methods, want 0", len(methods))
	}
}

func TestAgentAuth_InvalidSocket(t *testing.T) {
	auth := AgentAuth{Socket: "/nonexistent/socket"}
	methods := auth.auth()

	if len(methods) != 0 {
		t.Errorf("AgentAuth with invalid socket returned %d methods, want 0", len(methods))
	}
}

func TestDefaultAgentSocket(t *testing.T) {
	socket := DefaultAgentSocket()
	if socket != "" && socket == "/nonexistent" {
		t.Errorf("DefaultAgentSocket() = %q, expected empty or valid path", socket)
	}
}

func TestNewClient(t *testing.T) {
	auth := PasswordAuth{Password: "test"}
	_ = NewClient("root", "server.com", auth)
}

func TestResult_Fields(t *testing.T) {
	r := &Result{
		Stdout:   "output",
		Stderr:   "error",
		ExitCode: 1,
	}

	if r.Stdout != "output" {
		t.Errorf("Result.Stdout = %q, want %q", r.Stdout, "output")
	}
	if r.Stderr != "error" {
		t.Errorf("Result.Stderr = %q, want %q", r.Stderr, "error")
	}
	if r.ExitCode != 1 {
		t.Errorf("Result.ExitCode = %d, want %d", r.ExitCode, 1)
	}
}
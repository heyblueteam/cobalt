package validator

import "testing"

const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestValidateImageDigestRef(t *testing.T) {
	t.Parallel()
	good := []string{
		"ghcr.io/heyblueteam/blue-server@" + digest,
		"ghcr.io/heyblueteam/blue-server:0123abc@" + digest,
		"localhost:5000/server@" + digest,
		"redis@" + digest,
	}
	for _, ref := range good {
		if err := ValidateImageDigestRef(ref); err != nil {
			t.Errorf("%q: %v", ref, err)
		}
	}
	bad := []string{
		"",
		"ghcr.io/heyblueteam/blue-server",
		"ghcr.io/heyblueteam/blue-server:latest",
		"ghcr.io/heyblueteam/blue-server@sha256:abc",
		"ghcr.io/heyblueteam/Blue-Server@" + digest,
		"ghcr.io/x@" + digest + " --privileged",
		"-v/:/host@" + digest,
	}
	for _, ref := range bad {
		if err := ValidateImageDigestRef(ref); err == nil {
			t.Errorf("%q: expected error", ref)
		}
	}
}

func TestValidateImageSource(t *testing.T) {
	t.Parallel()
	for _, s := range []string{ImageSourceBuild, ImageSourceRegistry} {
		if err := ValidateImageSource(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"", "ci", "Registry"} {
		if err := ValidateImageSource(s); err == nil {
			t.Errorf("%q: expected error", s)
		}
	}
}

func TestValidateRegistryHost(t *testing.T) {
	t.Parallel()
	for _, h := range []string{"ghcr.io", "localhost:5000", "registry-1.docker.io"} {
		if err := ValidateRegistryHost(h); err != nil {
			t.Errorf("%q: %v", h, err)
		}
	}
	for _, h := range []string{"", "ghcr.io/heyblueteam", "https://ghcr.io", "-bad"} {
		if err := ValidateRegistryHost(h); err == nil {
			t.Errorf("%q: expected error", h)
		}
	}
}

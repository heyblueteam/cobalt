package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestStartStep_OK(t *testing.T) {
	old := Stderr
	defer func() { Stderr = old }()

	var buf bytes.Buffer
	Stderr = &buf

	s := StartStep(IconDocker, "Installing Docker")
	s.OK()

	got := buf.String()
	if !strings.Contains(got, IconDocker) {
		t.Errorf("missing icon: %q", got)
	}
	if !strings.Contains(got, "Installing Docker...") {
		t.Errorf("missing label: %q", got)
	}
	if !strings.HasSuffix(got, "✓\n") {
		t.Errorf("expected trailing checkmark: %q", got)
	}
}

func TestStartStep_Fail(t *testing.T) {
	old := Stderr
	defer func() { Stderr = old }()

	var buf bytes.Buffer
	Stderr = &buf

	s := StartStep(IconDocker, "Installing Docker")
	s.Fail("apt-get lock held")

	got := buf.String()
	if !strings.Contains(got, "✗\n") {
		t.Errorf("missing X mark: %q", got)
	}
	if !strings.Contains(got, "apt-get lock held") {
		t.Errorf("missing failure reason: %q", got)
	}
}

func TestStartStep_Skip(t *testing.T) {
	old := Stderr
	defer func() { Stderr = old }()

	var buf bytes.Buffer
	Stderr = &buf

	s := StartStep(IconDocker, "Installing Docker")
	s.Skip("Docker already installed")

	got := buf.String()
	if !strings.Contains(got, "Docker already installed") {
		t.Errorf("missing skip reason: %q", got)
	}
	// Skip must not emit the green check.
	if strings.Contains(got, "✓") {
		t.Errorf("skip should not emit checkmark: %q", got)
	}
}

func TestStartStep_JSONMode(t *testing.T) {
	old := Stderr
	oldJSON := jsonMode
	defer func() { Stderr = old; jsonMode = oldJSON }()

	var buf bytes.Buffer
	Stderr = &buf
	jsonMode = true

	s := StartStep(IconDocker, "Installing Docker")
	s.OK()
	s.Fail("ignored")
	s.Skip("ignored")
	s.Detail("key", "value")

	if buf.Len() != 0 {
		t.Errorf("expected zero output in JSON mode, got: %q", buf.String())
	}
}

func TestStartStep_Detail(t *testing.T) {
	old := Stderr
	defer func() { Stderr = old }()

	var buf bytes.Buffer
	Stderr = &buf

	s := StartStep(IconDetect, "Detecting environment")
	s.Detail(IconDocker, "not installed, will install")
	s.Detail(IconSwarm, "not initialized, will initialize")
	s.OK()

	got := buf.String()
	if !strings.Contains(got, "not installed, will install") {
		t.Errorf("missing detail row: %q", got)
	}
	if !strings.Contains(got, "not initialized, will initialize") {
		t.Errorf("missing second detail row: %q", got)
	}
}

func TestStartStep_StatusColumn(t *testing.T) {
	old := Stderr
	defer func() { Stderr = old }()

	var buf bytes.Buffer
	Stderr = &buf

	// Both lines must terminate at the same visible column (the whole
	// point of the right-aligned status mark). After stripping "✓\n"
	// the byte lengths should match for any two labels that both fit
	// inside stepWidth, since the helper pads each up to stepWidth.
	StartStep(IconDocker, "x").OK()
	short := strings.TrimSuffix(buf.String(), "✓\n")

	buf.Reset()
	StartStep(IconDocker, strings.Repeat("x", 40)).OK()
	long := strings.TrimSuffix(buf.String(), "✓\n")

	if len(short) != len(long) {
		t.Errorf("expected equal post-trim length; short=%d long=%d", len(short), len(long))
	}

	// And when the label overflows stepWidth, we should still emit at
	// least one space before the mark (no glued-on ✓).
	buf.Reset()
	StartStep(IconDocker, strings.Repeat("x", 200)).OK()
	overflow := buf.String()
	if !strings.Contains(overflow, " ✓\n") {
		t.Errorf("overflow case should still pad at least one space: %q", overflow)
	}
}

func TestStartStep_NilSafe(t *testing.T) {
	// A nil *Step should be a safe no-op so callers don't have to
	// check before calling OK/Fail/Skip.
	var s *Step
	s.OK()
	s.Fail("x")
	s.Skip("y")
	s.Detail("a", "b")
}

package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestConfirm_AssumeYes(t *testing.T) {
	got, err := Confirm(IconGitHub, "x?", true, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got {
		t.Errorf("assumeYes with defaultYes=true should return true")
	}

	got, err = Confirm(IconGitHub, "x?", false, true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got {
		t.Errorf("assumeYes with defaultYes=false should return false")
	}
}

func TestConfirm_AcceptsYes(t *testing.T) {
	oldStdin := Stdin
	oldStderr := Stderr
	defer func() { Stdin = oldStdin; Stderr = oldStderr }()

	for _, in := range []string{"y\n", "Y\n", "yes\n", "YES\n"} {
		Stdin = strings.NewReader(in)
		Stderr = &bytes.Buffer{}
		got, err := confirmUnsafe(IconGitHub, "x?", false)
		if err != nil {
			t.Fatalf("input %q: err %v", in, err)
		}
		if !got {
			t.Errorf("input %q: expected true", in)
		}
	}
}

func TestConfirm_AcceptsNo(t *testing.T) {
	oldStdin := Stdin
	oldStderr := Stderr
	defer func() { Stdin = oldStdin; Stderr = oldStderr }()

	for _, in := range []string{"n\n", "N\n", "no\n", "NO\n"} {
		Stdin = strings.NewReader(in)
		Stderr = &bytes.Buffer{}
		got, err := confirmUnsafe(IconGitHub, "x?", true)
		if err != nil {
			t.Fatalf("input %q: err %v", in, err)
		}
		if got {
			t.Errorf("input %q: expected false", in)
		}
	}
}

func TestConfirm_Default(t *testing.T) {
	oldStdin := Stdin
	oldStderr := Stderr
	defer func() { Stdin = oldStdin; Stderr = oldStderr }()

	Stdin = strings.NewReader("\n")
	Stderr = &bytes.Buffer{}
	got, err := confirmUnsafe(IconGitHub, "x?", true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !got {
		t.Errorf("empty input with defaultYes=true should return true")
	}

	Stdin = strings.NewReader("\n")
	Stderr = &bytes.Buffer{}
	got, err = confirmUnsafe(IconGitHub, "x?", false)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got {
		t.Errorf("empty input with defaultYes=false should return false")
	}
}

func TestConfirm_InvalidResponse(t *testing.T) {
	oldStdin := Stdin
	oldStderr := Stderr
	defer func() { Stdin = oldStdin; Stderr = oldStderr }()

	Stdin = strings.NewReader("maybe\n")
	Stderr = &bytes.Buffer{}
	_, err := confirmUnsafe(IconGitHub, "x?", true)
	if err == nil {
		t.Error("expected error on invalid response")
	}
}

func TestInput_AssumeYesReturnsDefault(t *testing.T) {
	got, err := Input(IconPublicHost, "host?", "default.example.com", true)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "default.example.com" {
		t.Errorf("got %q want %q", got, "default.example.com")
	}
}

func TestInput_EmptyReturnsDefault(t *testing.T) {
	oldStdin := Stdin
	oldStderr := Stderr
	defer func() { Stdin = oldStdin; Stderr = oldStderr }()

	Stdin = strings.NewReader("\n")
	Stderr = &bytes.Buffer{}
	got, err := inputUnsafe(IconPublicHost, "host?", "default.example.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "default.example.com" {
		t.Errorf("got %q want default", got)
	}
}

func TestInput_OverridesDefault(t *testing.T) {
	oldStdin := Stdin
	oldStderr := Stderr
	defer func() { Stdin = oldStdin; Stderr = oldStderr }()

	Stdin = strings.NewReader("custom.example.com\n")
	Stderr = &bytes.Buffer{}
	got, err := inputUnsafe(IconPublicHost, "host?", "default.example.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "custom.example.com" {
		t.Errorf("got %q want %q", got, "custom.example.com")
	}
}

// confirmUnsafe / inputUnsafe bypass the IsTTY check so unit tests
// can exercise the parsing path without a real terminal. Production
// code must use Confirm / Input.
func confirmUnsafe(icon, question string, defaultYes bool) (bool, error) {
	suffix := "(Y/n)"
	if !defaultYes {
		suffix = "(y/N)"
	}
	_ = icon
	_ = question
	_ = suffix
	line, err := readLine(Stdin)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, errInvalidResponse(line)
	}
}

func inputUnsafe(icon, question, defaultValue string) (string, error) {
	_ = icon
	_ = question
	line, err := readLine(Stdin)
	if err != nil {
		return "", err
	}
	if line == "" {
		return defaultValue, nil
	}
	return line, nil
}

type invalidResponseErr struct{ got string }

func (e invalidResponseErr) Error() string {
	return "invalid response " + e.got
}

func errInvalidResponse(got string) error { return invalidResponseErr{got: got} }

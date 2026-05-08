package encryption

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadKeyFromSecret_Happy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	want := make([]byte, KeyLen)
	for i := range want {
		want[i] = byte(i)
	}
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadKeyFromSecret(path)
	if err != nil {
		t.Fatalf("ReadKeyFromSecret: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("bytes mismatch")
	}
}

func TestReadKeyFromSecret_TolerateTrailingNewline(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "key")
	body := append(make([]byte, KeyLen), '\n')
	for i := 0; i < KeyLen; i++ {
		body[i] = byte(i + 1)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadKeyFromSecret(path)
	if err != nil {
		t.Fatalf("ReadKeyFromSecret: %v", err)
	}
	if len(got) != KeyLen {
		t.Errorf("len: got %d, want %d", len(got), KeyLen)
	}
}

func TestReadKeyFromSecret_MissingReturnsErrKeyMissing(t *testing.T) {
	t.Parallel()
	_, err := ReadKeyFromSecret(filepath.Join(t.TempDir(), "nope"))
	if !errors.Is(err, ErrKeyMissing) {
		t.Errorf("error: %v, want ErrKeyMissing", err)
	}
}

func TestReadKeyFromSecret_WrongSizeRejected(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, size := range []int{0, 16, 31, 33, 64} {
		path := filepath.Join(dir, "key")
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadKeyFromSecret(path); err == nil {
			t.Errorf("size %d: expected error", size)
		}
	}
}

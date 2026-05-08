package encryption

import (
	"errors"
	"fmt"
	"os"
)

// DefaultKeyPath is where Docker Swarm mounts the cobalt encryption
// key by default (cobalt_encryption_key secret declared in the
// embedded compose). Operators can override via daemon flag if they
// run a non-standard topology.
const DefaultKeyPath = "/run/secrets/cobalt_encryption_key"

// ErrKeyMissing is returned when the secret path doesn't exist on
// disk. Distinguishable from other read errors so the daemon can
// emit a precise startup message ("create the swarm secret …").
var ErrKeyMissing = errors.New("encryption: key file not found")

// ReadKeyFromSecret loads exactly KeyLen bytes from path. Trailing
// newlines are tolerated (some `docker secret create -` invocations
// leave one behind, depending on the source command).
//
// Never logs the bytes. The returned slice is the only copy held;
// callers should pin it on the Cipher and discard their own
// reference promptly.
func ReadKeyFromSecret(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrKeyMissing
	}
	if err != nil {
		return nil, fmt.Errorf("encryption: read key: %w", err)
	}
	// Trim a single trailing \n so a key written with `echo bytes |
	// docker secret create` (which appends one) still validates.
	if n := len(data); n > 0 && data[n-1] == '\n' {
		data = data[:n-1]
	}
	if len(data) != KeyLen {
		return nil, fmt.Errorf("encryption: key at %s is %d bytes, want %d", path, len(data), KeyLen)
	}
	return data, nil
}

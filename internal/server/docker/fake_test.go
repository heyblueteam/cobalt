package docker

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

// fakeRunner implements Runner for tests. It records every invocation and
// can be programmed to return canned stdout / errors per-call.
type fakeRunner struct {
	mu    sync.Mutex
	calls []recorded

	// stdout is matched by argv prefix: the longest prefix wins.
	stdout map[string]string

	// errs is matched the same way; non-nil → return that error.
	errs map[string]error
}

type recorded struct {
	Args []string
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{stdout: map[string]string{}, errs: map[string]error{}}
}

// answerStdout configures the fake to return out (and nil error) for any
// invocation whose argv joined-by-space starts with prefix.
func (f *fakeRunner) answerStdout(prefix, out string) {
	f.stdout[prefix] = out
}

// answerErr makes invocations matching prefix return err.
func (f *fakeRunner) answerErr(prefix string, err error) {
	f.errs[prefix] = err
}

func (f *fakeRunner) Run(_ context.Context, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	f.mu.Lock()
	f.calls = append(f.calls, recorded{Args: append([]string(nil), args...)})
	f.mu.Unlock()

	joined := strings.Join(args, " ")

	// Errors win over stdout — same semantics as real shell-out.
	if err := f.matchErr(joined); err != nil {
		return err
	}
	if out, ok := f.matchStdout(joined); ok && stdout != nil {
		_, _ = io.WriteString(stdout, out)
	}
	if stdin != nil {
		// Drain so callers writing tar streams don't deadlock.
		_, _ = io.Copy(io.Discard, stdin)
	}
	return nil
}

func (f *fakeRunner) matchStdout(joined string) (string, bool) {
	var bestK, bestV string
	for k, v := range f.stdout {
		if strings.HasPrefix(joined, k) && len(k) > len(bestK) {
			bestK, bestV = k, v
		}
	}
	if bestK == "" {
		return "", false
	}
	return bestV, true
}

func (f *fakeRunner) matchErr(joined string) error {
	var bestK string
	var bestErr error
	for k, e := range f.errs {
		if strings.HasPrefix(joined, k) && len(k) > len(bestK) {
			bestK, bestErr = k, e
		}
	}
	return bestErr
}

func (f *fakeRunner) lastCall() recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return recorded{}
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// argFor returns true if the argv contains every elem in want, in any
// order. Useful when argv ordering varies (e.g. iterating a map).
func argHas(args []string, want ...string) bool {
	have := map[string]bool{}
	for _, a := range args {
		have[a] = true
	}
	for _, w := range want {
		if !have[w] {
			return false
		}
	}
	return true
}

// argSequence returns true if want appears as a contiguous subsequence of
// args. Use this when order matters (e.g. flag → value pairs).
func argSequence(args []string, want ...string) bool {
outer:
	for i := 0; i+len(want) <= len(args); i++ {
		for j, w := range want {
			if args[i+j] != w {
				continue outer
			}
		}
		return true
	}
	return false
}

// staticErr returns a stable error value tests can assert on.
type staticErr string

func (e staticErr) Error() string { return string(e) }

// dockerNotFoundErr mimics what shell-out errors look like for "no such
// service" so isNotFound returns true.
const dockerNotFoundErr = staticErr("Error: No such service: foo")

// errorf is a small helper for building test-only errors.
func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }

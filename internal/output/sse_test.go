package output

import (
	"errors"
	"testing"
)

func TestIsContextCanceled(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("oops"), false},
		{"context canceled", errors.New("Get \"...\": context canceled"), true},
		{
			"deadline exceeded (cancellation alias from net/http)",
			errors.New("read sse stream: context deadline exceeded (Client.Timeout or context cancellation while reading body)"), true,
		},
		{"closed conn", errors.New("read tcp 1.2.3.4: use of closed network connection"), true},
		{"capitalized canceled", errors.New("operation Canceled"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isContextCanceled(tt.err); got != tt.want {
				t.Errorf("isContextCanceled(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

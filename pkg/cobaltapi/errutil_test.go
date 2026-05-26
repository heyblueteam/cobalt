package cobaltapi

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestIsContextCanceled(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context canceled", context.Canceled, true},
		{"wrapped canceled", fmt.Errorf("wrap: %w", context.Canceled), true},
		{"context deadline exceeded", context.DeadlineExceeded, false},
		{"plain error", errors.New("some error"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsContextCanceled(tt.err); got != tt.want {
				t.Errorf("IsContextCanceled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsContextDeadlineExceeded(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context deadline exceeded", context.DeadlineExceeded, true},
		{"wrapped deadline", fmt.Errorf("wrap: %w", context.DeadlineExceeded), true},
		{"context canceled", context.Canceled, false},
		{"plain error", errors.New("some error"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsContextDeadlineExceeded(tt.err); got != tt.want {
				t.Errorf("IsContextDeadlineExceeded() = %v, want %v", got, tt.want)
			}
		})
	}
}

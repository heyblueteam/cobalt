package cobaltapi

import (
	"context"
	"errors"
)

func IsContextCanceled(err error) bool {
	return errors.Is(err, context.Canceled)
}

func IsContextDeadlineExceeded(err error) bool {
	return errors.Is(err, context.DeadlineExceeded)
}
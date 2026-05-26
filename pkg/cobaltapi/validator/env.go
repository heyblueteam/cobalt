package validator

import (
	"errors"
	"fmt"
)

var (
	ErrEnvKeyRequired = errors.New("cobaltapi: env key is required")
	ErrEnvKeyInvalid  = errors.New("cobaltapi: env key must match ^[A-Z][A-Z0-9_]+$")
)

func ValidateEnvKey(key string) error {
	if key == "" {
		return ErrEnvKeyRequired
	}
	if !EnvKeyRX.MatchString(key) {
		return fmt.Errorf("%w: must be uppercase letters, numbers, and underscores only", ErrEnvKeyInvalid)
	}
	return nil
}

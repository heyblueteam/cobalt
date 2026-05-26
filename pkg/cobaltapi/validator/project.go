package validator

import (
	"errors"
	"fmt"
)

var (
	ErrProjectNameRequired = errors.New("cobaltapi: project name is required")
	ErrProjectNameInvalid  = errors.New("cobaltapi: project name must match ^[a-z][a-z0-9-]{0,62}$")
)

func ValidateProjectName(name string) error {
	if name == "" {
		return ErrProjectNameRequired
	}
	if !ProjectNameRX.MatchString(name) {
		return fmt.Errorf("%w: must be 1-63 chars, lowercase letters/numbers/hyphens, starting with a letter", ErrProjectNameInvalid)
	}
	return nil
}

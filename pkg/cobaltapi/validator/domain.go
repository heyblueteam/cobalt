package validator

import (
	"errors"
	"fmt"
)

var (
	ErrDomainRequired = errors.New("cobaltapi: domain is required")
	ErrDomainInvalid  = errors.New("cobaltapi: domain format is invalid")
)

func ValidateDomain(domain string) error {
	if domain == "" {
		return ErrDomainRequired
	}
	if !DomainRX.MatchString(domain) {
		return fmt.Errorf("%w: must be a valid hostname (e.g. api.example.com)", ErrDomainInvalid)
	}
	return nil
}

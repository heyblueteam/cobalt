package validator

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	ErrImageRefInvalid     = errors.New("cobaltapi: image reference must be pinned by digest (name@sha256:<64 hex>)")
	ErrImageNameInvalid    = errors.New("cobaltapi: image name is invalid")
	ErrImageSourceInvalid  = errors.New(`cobaltapi: image source must be "build" or "registry"`)
	ErrRegistryHostInvalid = errors.New("cobaltapi: registry must be a host name, optionally with a port")

	// ImageDigestRefRX accepts host[:port]/path[:tag]@sha256:<hex>. A digest
	// names exactly one image, so a deploy cannot silently change under a
	// moved tag.
	ImageDigestRefRX = regexp.MustCompile(
		`^([a-zA-Z0-9.-]+(:[0-9]+)?/)?[a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*` +
			`(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?@sha256:[a-f0-9]{64}$`)

	// ImageNameRX matches cobalt.json `images` keys.
	ImageNameRX = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

	RegistryHostRX = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?(:[0-9]{1,5})?$`)
)

// Image sources a project can deploy from.
const (
	// ImageSourceBuild: Cobalt builds the image from the repo, on pushes and
	// on `cobalt deploy`. The default for every project.
	ImageSourceBuild = "build"
	// ImageSourceRegistry: another system builds and pushes the image.
	// Pushes do not deploy, and `cobalt deploy` requires --image.
	ImageSourceRegistry = "registry"
)

func ValidateImageDigestRef(ref string) error {
	if !ImageDigestRefRX.MatchString(ref) {
		return fmt.Errorf("%w: got %q", ErrImageRefInvalid, ref)
	}
	return nil
}

func ValidateImageName(name string) error {
	if !ImageNameRX.MatchString(name) {
		return fmt.Errorf("%w: got %q", ErrImageNameInvalid, name)
	}
	return nil
}

func ValidateImageSource(source string) error {
	if source != ImageSourceBuild && source != ImageSourceRegistry {
		return fmt.Errorf("%w: got %q", ErrImageSourceInvalid, source)
	}
	return nil
}

func ValidateRegistryHost(host string) error {
	if !RegistryHostRX.MatchString(host) {
		return fmt.Errorf("%w: got %q", ErrRegistryHostInvalid, host)
	}
	return nil
}

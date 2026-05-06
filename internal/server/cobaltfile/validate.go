package cobaltfile

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Validate runs every semantic check on cf. It assumes applyDefaults has
// already run (Parse calls it; manually-constructed Cobaltfiles can call
// applyDefaults via the unexported method or rely on Default()).
func (cf *Cobaltfile) Validate() error {
	if cf.Version != SupportedVersion {
		return fmt.Errorf(
			"cobaltfile: unsupported version %q (cobalt v1 only supports %q)",
			cf.Version, SupportedVersion,
		)
	}

	for name, s := range cf.Services {
		if err := validateService(name, s); err != nil {
			return err
		}
	}

	// Hook services have additional requirements that depend on the service
	// name, not just its shape.
	for _, hook := range []string{HookDeployStartBefore, HookDeployStartAfter} {
		if s, ok := cf.Services[hook]; ok {
			if s.Type != TypeCommand {
				return fmt.Errorf(
					"cobaltfile: service %q must be type=command (got %q)",
					hook, s.Type,
				)
			}
			if strings.TrimSpace(s.Command) == "" {
				return fmt.Errorf("cobaltfile: service %q requires a command", hook)
			}
		}
	}

	// Every service that references an image must reference one that exists.
	for name, s := range cf.Services {
		if !needsImage(s) {
			continue
		}
		if _, ok := cf.Images[s.Image]; !ok {
			return fmt.Errorf(
				"cobaltfile: service %q references unknown image %q",
				name, s.Image,
			)
		}
	}

	return nil
}

func validateService(name string, s Service) error {
	if name == "" {
		return errors.New("cobaltfile: service name must not be empty")
	}
	if !s.Type.IsValid() {
		return fmt.Errorf(
			"cobaltfile: service %q has invalid type %q (want one of: container, static, generator, command, cron)",
			name, s.Type,
		)
	}
	if s.Port < 1 || s.Port > 65535 {
		return fmt.Errorf("cobaltfile: service %q port %d out of range 1-65535", name, s.Port)
	}
	if s.Timeout < 0 {
		return fmt.Errorf("cobaltfile: service %q timeout %d must be non-negative", name, s.Timeout)
	}
	if s.Type == TypeCron {
		if err := validateCronSchedule(s.Schedule); err != nil {
			return fmt.Errorf("cobaltfile: service %q schedule: %w", name, err)
		}
		if strings.TrimSpace(s.Command) == "" {
			return fmt.Errorf("cobaltfile: cron service %q requires a command", name)
		}
	}
	for i, p := range s.PublishedPorts {
		if p.PublishedAs < 1 || p.PublishedAs > 65535 {
			return fmt.Errorf(
				"cobaltfile: service %q publishedPorts[%d].publishedAs %d out of range",
				name, i, p.PublishedAs,
			)
		}
		if p.FromContainerPort < 1 || p.FromContainerPort > 65535 {
			return fmt.Errorf(
				"cobaltfile: service %q publishedPorts[%d].fromContainerPort %d out of range",
				name, i, p.FromContainerPort,
			)
		}
		switch p.Protocol {
		case "tcp", "udp", "sctp":
		default:
			return fmt.Errorf(
				"cobaltfile: service %q publishedPorts[%d].protocol %q must be tcp, udp, or sctp",
				name, i, p.Protocol,
			)
		}
	}
	for i, v := range s.Volumes {
		if strings.TrimSpace(v.Name) == "" {
			return fmt.Errorf("cobaltfile: service %q volumes[%d].name is required", name, i)
		}
		if !strings.HasPrefix(v.DestinationPath, "/") {
			return fmt.Errorf(
				"cobaltfile: service %q volumes[%d].destinationPath %q must be absolute",
				name, i, v.DestinationPath,
			)
		}
	}
	if s.Health != nil && strings.TrimSpace(s.Health.Command) == "" {
		return fmt.Errorf("cobaltfile: service %q health.command is required when health is set", name)
	}
	return nil
}

// needsImage reports whether a service will actually try to build/run from
// an image (vs. e.g. a static-only service whose output Caddy serves
// directly, or a service that overrides with its own build command).
func needsImage(s Service) bool {
	if s.Build != "" {
		return false
	}
	if s.Type == TypeStatic && s.Command == "" {
		return false
	}
	return true
}

// cronField matches a single field of a 5-field cron expression.
//
// Accepts: *, */N, N, N-M, comma-separated lists of any of the above.
// This is intentionally permissive — the actual scheduler will do the
// authoritative parse; here we just reject obvious garbage.
var cronField = regexp.MustCompile(`^(\*|\*/\d+|\d+(-\d+)?(/\d+)?)(,(\*|\*/\d+|\d+(-\d+)?(/\d+)?))*$`)

func validateCronSchedule(s string) error {
	parts := strings.Fields(s)
	if len(parts) != 5 {
		return fmt.Errorf("must have 5 fields (got %d): %q", len(parts), s)
	}
	for i, p := range parts {
		if !cronField.MatchString(p) {
			return fmt.Errorf("field %d invalid: %q", i+1, p)
		}
	}
	return nil
}

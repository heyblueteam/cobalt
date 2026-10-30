package cobaltfile

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Validate runs every semantic check on cf. It assumes applyDefaults has
// already run (Parse calls it; manually-constructed Cobaltfiles can call
// applyDefaults via the unexported method or rely on Default()).
func (cf *Cobaltfile) Validate() error {
	if n := cf.WebGraceSeconds(); n < 60 || n > 86400 {
		return fmt.Errorf("cobaltfile: webRetirement.gracePeriodSeconds must be between 60 and 86400")
	}
	if n := cf.MaxWebGenerations(); n < 2 || n > 100 {
		return fmt.Errorf("cobaltfile: webRetirement.maxGenerations must be between 2 and 100")
	}

	if cf.Version != SupportedVersion {
		return fmt.Errorf(
			"cobaltfile: unsupported version %q (cobalt v1 only supports %q)",
			cf.Version, SupportedVersion,
		)
	}

	if cf.StablePublicWebNetwork != "" {
		if cf.StablePublicWebNetwork != "shared" {
			return fmt.Errorf("cobaltfile: stablePublicWebNetwork must be shared or omitted")
		}
		if !cf.StablePublicWeb {
			return fmt.Errorf("cobaltfile: stablePublicWebNetwork requires stablePublicWeb: true")
		}
		for name, service := range cf.Services {
			if name != "web" && service.Type == TypeContainer && service.StopFirst {
				return fmt.Errorf("cobaltfile: stablePublicWebNetwork shared requires start-first siblings; service %q has stopFirst", name)
			}
		}
		if reason := cf.StablePublicWebUnavailableReason(); reason != "" {
			return fmt.Errorf("cobaltfile: stablePublicWebNetwork: %s", reason)
		}
	}

	for name, s := range cf.Services {
		if err := validateService(name, s); err != nil {
			return err
		}
		if s.PrivateAddress && (name != "web" || !cf.UsesStablePublicWeb()) {
			return fmt.Errorf("cobaltfile: service %q privateAddress requires an eligible stablePublicWeb web service", name)
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

	if err := cf.validateNotify(); err != nil {
		return err
	}

	// Image-reference validation: intentionally none. We don't require
	// `s.Image` to be a key in `cf.Images` — that would reject pre-built
	// docker registry references (e.g. `redis/redis-stack:7.4.0-v8`,
	// `postgres:14-alpine`) which disco supports and many real
	// cobaltfiles rely on. Matches disco's resolution in
	// utils/docker.py:1218-1228: "in disco_file.images? build it. else?
	// docker pull it." The builder makes the same distinction. A bogus
	// pre-built ref surfaces as a `docker pull` failure at deploy time,
	// which is loud and well-localized — better than a parser error
	// blocking valid cobaltfiles.
	//
	// `s.Image` being empty is also fine here: applyDefaults() always
	// backfills it with `DefaultImageName` for services that need an
	// image, so we never see empty in practice.

	return nil
}

// validateNotify checks the notify block. Events are explicit; an unknown
// event name is a config mistake, not a silent no-op. templateFile must stay
// inside the project root because it is read from the deploy checkout.
func (cf *Cobaltfile) validateNotify() error {
	if cf.Notify == nil || cf.Notify.Telegram == nil {
		return nil
	}
	tg := cf.Notify.Telegram
	for _, event := range tg.On {
		switch event {
		case NotifyEventSuccess, NotifyEventFailure, NotifyEventCanceled, NotifyEventSkipped:
		default:
			return fmt.Errorf(
				"cobaltfile: notify.telegram.on contains invalid event %q (want success, failure, canceled, or skipped)",
				event,
			)
		}
	}
	if tg.TemplateFile != "" {
		if !filepath.IsLocal(tg.TemplateFile) {
			return fmt.Errorf(
				"cobaltfile: notify.telegram.templateFile %q must be a relative path inside the project",
				tg.TemplateFile,
			)
		}
		if filepath.Clean(tg.TemplateFile) == "." {
			return fmt.Errorf("cobaltfile: notify.telegram.templateFile must name a file")
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
	if s.MinReplicas < 0 {
		return fmt.Errorf("cobaltfile: service %q minReplicas %d must be non-negative", name, s.MinReplicas)
	}
	if s.StopGracePeriodSeconds < 0 || s.StopGracePeriodSeconds > 86400 {
		return fmt.Errorf("cobaltfile: service %q stopGracePeriodSeconds must be between 0 and 86400", name)
	}
	if s.StopGracePeriodSeconds > 0 && s.Type != TypeContainer {
		return fmt.Errorf("cobaltfile: service %q stopGracePeriodSeconds only valid for type=container (got %q)", name, s.Type)
	}
	if s.MinReplicas > 0 && s.Type != TypeContainer {
		return fmt.Errorf("cobaltfile: service %q minReplicas only valid for type=container (got %q)", name, s.Type)
	}
	if s.StopFirst {
		if s.Type != TypeContainer {
			return fmt.Errorf("cobaltfile: service %q stopFirst only valid for type=container (got %q)", name, s.Type)
		}
		// Public stop-first is an explicit single-writer, downtime mode.
		if name == "web" && !s.ExposedInternally && s.MinReplicas > 1 {
			return fmt.Errorf("cobaltfile: public stopFirst web requires at most one replica")
		}
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

// Package cobaltfile parses and validates the per-repo cobalt.json file
// that tells the daemon how to build and run a project.
//
// The file is JSON. Top level shape:
//
//	{
//	  "version": "1.0",
//	  "name":     "myapp",          // optional — used by CLI project resolver
//	  "services": { "<name>": Service, ... },
//	  "images":   { "<name>": Image,   ... }
//	}
//
// Defaults are applied during Parse, so callers always see a fully populated
// Cobaltfile. Validate enforces semantic rules (valid enums, hook services
// must be command, cron services must have a valid schedule, etc.).
package cobaltfile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// SupportedVersion is the only schema version cobaltfile understands today.
const SupportedVersion = "1.0"

// Default values applied when a field is absent.
const (
	DefaultServiceType  = TypeContainer
	DefaultImageName    = "default"
	DefaultPort         = 8000
	DefaultPublicPath   = "dist"
	DefaultSchedule     = "* * * * *"
	DefaultTimeout      = 300
	DefaultProtocol     = "tcp"
	DefaultDockerfile   = "Dockerfile"
	DefaultBuildContext = "."
)

// Hook service names recognized by the deploy flow.
const (
	HookDeployStartBefore = "hook:deploy:start:before"
	HookDeployStartAfter  = "hook:deploy:start:after"
)

// Cobaltfile is the parsed contents of cobalt.json.
type Cobaltfile struct {
	Version         string             `json:"version"`
	Name            string             `json:"name,omitempty"`
	Services        map[string]Service `json:"services,omitempty"`
	Images          map[string]Image   `json:"images,omitempty"`
	StablePublicWeb bool               `json:"stablePublicWeb,omitempty"`
	WebRetirement   *WebRetirement     `json:"webRetirement,omitempty"`
	Notify          *Notify            `json:"notify,omitempty"`
}

// Notify configures outbound deploy notifications. A nil Notify (or a nil
// channel inside it) means the project sends nothing.
type Notify struct {
	Telegram *TelegramNotify `json:"telegram,omitempty"`
}

// TelegramNotify configures Telegram messages for a project. The bot token
// is a daemon flag, never part of the repo.
//
// Enabled is a pointer so an omitted value can default to true while an
// explicit false stays false.
type TelegramNotify struct {
	Enabled      *bool    `json:"enabled,omitempty"`
	Chat         string   `json:"chat,omitempty"`
	On           []string `json:"on,omitempty"`
	TemplateFile string   `json:"templateFile,omitempty"`
}

// Notify events accepted in TelegramNotify.On.
const (
	NotifyEventSuccess  = "success"
	NotifyEventFailure  = "failure"
	NotifyEventCanceled = "canceled"
	NotifyEventSkipped  = "skipped"
)

// DefaultNotifyEvents is used when On is omitted. A project that opts into
// notifications hears from every deploy by default; silence then always
// means "no deploy", never "unknown".
func DefaultNotifyEvents() []string {
	return []string{NotifyEventSuccess, NotifyEventFailure}
}

// IsEnabled reports whether the channel is on. An omitted Enabled is true
// when the block exists.
func (t *TelegramNotify) IsEnabled() bool {
	return t != nil && (t.Enabled == nil || *t.Enabled)
}

// ServiceType is one of a fixed set of service kinds.
type ServiceType string

// Known service types. cgi is intentionally absent — Blue's cobalt drops it.
const (
	TypeContainer ServiceType = "container"
	TypeStatic    ServiceType = "static"
	TypeGenerator ServiceType = "generator"
	TypeCommand   ServiceType = "command"
	TypeCron      ServiceType = "cron"
)

// IsValid reports whether t is one of the known service types.
func (t ServiceType) IsValid() bool {
	switch t {
	case TypeContainer, TypeStatic, TypeGenerator, TypeCommand, TypeCron:
		return true
	}
	return false
}

// Service describes a single service inside a project.
type Service struct {
	Type              ServiceType     `json:"type,omitempty"`
	Image             string          `json:"image,omitempty"`
	Port              int             `json:"port,omitempty"`
	Command           string          `json:"command,omitempty"`
	Build             string          `json:"build,omitempty"`
	PublicPath        string          `json:"publicPath,omitempty"`
	PublishedPorts    []PublishedPort `json:"publishedPorts,omitempty"`
	Volumes           []Volume        `json:"volumes,omitempty"`
	Schedule          string          `json:"schedule,omitempty"`
	ExposedInternally bool            `json:"exposedInternally,omitempty"`
	Timeout           int             `json:"timeout,omitempty"`
	Health            *Health         `json:"health,omitempty"`
	ExtraSwarmParams  string          `json:"extraSwarmParams,omitempty"`
	ExtraRunParams    string          `json:"extraRunParams,omitempty"`
	// MinReplicas is the baseline replica count a new deployment of this
	// container service starts with. Zero means "use docker's default of
	// 1". `cobalt scale set` can still raise the count above the floor for
	// the current deployment, but the next deploy starts fresh from
	// MinReplicas — so this is how you encode "api needs 4 to survive prod
	// load" in the repo rather than only in operator memory.
	MinReplicas int `json:"minReplicas,omitempty"`
	// StopFirst stops the old instance before starting its replacement.
	// Public web services opt into downtime and a single writer (one replica).
	// Cobalt waits for exit and retains the old service for startup recovery.
	// Recovery restarts the old process; it does not undo data/schema changes.
	// Internal services retain their existing remove-before-start behavior.
	StopFirst bool `json:"stopFirst,omitempty"`
}

// Image describes how to build a docker image.
type Image struct {
	Dockerfile string `json:"dockerfile,omitempty"`
	Context    string `json:"context,omitempty"`
}

// Volume mounts a named docker volume into a container.
type Volume struct {
	Name            string `json:"name"`
	DestinationPath string `json:"destinationPath"`
	// SharedDuringUpdate permits old and new tasks to mount this volume at
	// the same time. Only opt in when the application supports concurrent access.
	SharedDuringUpdate bool `json:"sharedDuringUpdate,omitempty"`
}

// UsesStablePublicWeb reports whether the project can use Cobalt's durable
// public-web service. The stable service only joins cobalt-main, so projects
// needing a deployment-scoped network stay on the existing generation route.
// Keep this deliberately conservative until service dependencies are modeled
// explicitly in cobalt.json.
func (cf *Cobaltfile) UsesStablePublicWeb() bool {
	return cf.StablePublicWeb && cf.StablePublicWebUnavailableReason() == ""
}

// StablePublicWebUnavailableReason explains a requested stable-mode fallback.
// A volume needs explicit concurrent-access consent because start-first updates
// run both versions at once; databases and other single-writer stores are unsafe.
func (cf *Cobaltfile) StablePublicWebUnavailableReason() string {
	web, ok := cf.Services["web"]
	if !ok || web.Type != TypeContainer {
		return "web must be a container service"
	}
	if web.ExposedInternally || web.StopFirst || len(web.PublishedPorts) != 0 ||
		!stableSafeSwarmParams(web.ExtraSwarmParams) {
		return "web requires deployment-scoped networking, published ports, stop-first, or unsupported swarm parameters"
	}
	for _, volume := range web.Volumes {
		if !volume.SharedDuringUpdate {
			return fmt.Sprintf("volume %q must explicitly set sharedDuringUpdate: true for concurrent access", volume.Name)
		}
	}
	for name, service := range cf.Services {
		if name != "web" && service.Type == TypeContainer {
			return "additional container services require deployment-scoped networking"
		}
	}
	return ""
}

// stableSafeSwarmParams reports whether every extra swarm param is one the
// stable service can carry without fighting the properties cobalt manages
// itself. Only `--host` is allowed: it adds a /etc/hosts entry (the
// host-gateway alias pattern) and touches nothing cobalt sets. Anything else
// -- networks, ports, mounts, replicas, constraints -- either duplicates a
// field the stable path already owns or pins the service to a
// deployment-scoped resource, which is exactly what this service exists to
// avoid. Widen this list only with a matching reconcile in
// ReconcileStableService, or the param silently stops tracking cobalt.json.
func stableSafeSwarmParams(extra string) bool {
	params := strings.Fields(extra)
	for i := 0; i < len(params); i++ {
		switch {
		case params[i] == "--host":
			i++ // skip its value
			if i >= len(params) {
				return false
			}
		case strings.HasPrefix(params[i], "--host="):
		default:
			return false
		}
	}
	return true
}

// PublishedPort exposes a container port on the host network.
type PublishedPort struct {
	PublishedAs       int    `json:"publishedAs"`
	FromContainerPort int    `json:"fromContainerPort"`
	Protocol          string `json:"protocol,omitempty"`
}

// Health configures a docker healthcheck.
type Health struct {
	Command string `json:"command"`
}

// Parse parses a cobalt.json byte slice, applies defaults, and validates the
// result. If b is empty or nil, Parse returns the default Cobaltfile.
func Parse(b []byte) (*Cobaltfile, error) {
	if len(b) == 0 {
		cf := Default()
		return cf, nil
	}
	var cf Cobaltfile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cf); err != nil {
		return nil, fmt.Errorf("cobaltfile: parse: %w", err)
	}
	cf.applyDefaults()
	if err := cf.Validate(); err != nil {
		return nil, err
	}
	return &cf, nil
}

// ParseFile reads path and parses it. Missing files return the default
// Cobaltfile (matches upstream's "no disco.json → default" behavior).
func ParseFile(path string) (*Cobaltfile, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Default(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("cobaltfile: read %s: %w", path, err)
	}
	return Parse(b)
}

// Default returns the canonical "no cobalt.json" file: one web service of
// the default container type. The default image entry is added because the
// implicit web service will need to build.
func Default() *Cobaltfile {
	return &Cobaltfile{
		Version: SupportedVersion,
		Services: map[string]Service{
			"web": defaultService(),
		},
		Images: map[string]Image{
			DefaultImageName: defaultImage(),
		},
	}
}

func defaultService() Service {
	return Service{
		Type:       DefaultServiceType,
		Image:      DefaultImageName,
		Port:       DefaultPort,
		PublicPath: DefaultPublicPath,
		Schedule:   DefaultSchedule,
		Timeout:    DefaultTimeout,
	}
}

func defaultImage() Image {
	return Image{
		Dockerfile: DefaultDockerfile,
		Context:    DefaultBuildContext,
	}
}

// applyDefaults fills in zero values with their canonical defaults. It is
// idempotent and safe to call on an already-populated Cobaltfile.
func (cf *Cobaltfile) applyDefaults() {
	if cf.Services == nil {
		cf.Services = map[string]Service{}
	}
	if cf.Images == nil {
		cf.Images = map[string]Image{}
	}

	for name, s := range cf.Services {
		if s.Type == "" {
			s.Type = DefaultServiceType
		}
		if s.Image == "" {
			s.Image = DefaultImageName
		}
		if s.Port == 0 {
			s.Port = DefaultPort
		}
		if s.PublicPath == "" {
			s.PublicPath = DefaultPublicPath
		}
		if s.Schedule == "" {
			s.Schedule = DefaultSchedule
		}
		if s.Timeout == 0 {
			s.Timeout = DefaultTimeout
		}
		for i, p := range s.PublishedPorts {
			if p.Protocol == "" {
				s.PublishedPorts[i].Protocol = DefaultProtocol
			}
		}
		cf.Services[name] = s
	}

	for name, img := range cf.Images {
		if img.Dockerfile == "" {
			img.Dockerfile = DefaultDockerfile
		}
		if img.Context == "" {
			img.Context = DefaultBuildContext
		}
		cf.Images[name] = img
	}

	if cf.shouldAddDefaultImage() {
		cf.Images[DefaultImageName] = defaultImage()
	}

	if cf.Notify != nil && cf.Notify.Telegram != nil {
		tg := cf.Notify.Telegram
		if tg.Enabled == nil {
			enabled := true
			tg.Enabled = &enabled
		}
		if len(tg.On) == 0 {
			tg.On = DefaultNotifyEvents()
		}
	}
}

// shouldAddDefaultImage mirrors upstream's auto-injection rule: add the
// "default" image entry if any service uses image="default" AND that
// service will actually need an image (i.e. isn't a static-no-command and
// doesn't override with build:).
func (cf *Cobaltfile) shouldAddDefaultImage() bool {
	if _, ok := cf.Images[DefaultImageName]; ok {
		return false
	}
	for _, s := range cf.Services {
		if s.Image != DefaultImageName {
			continue
		}
		if s.Type == TypeStatic && s.Command == "" {
			continue
		}
		if s.Build != "" {
			continue
		}
		return true
	}
	return false
}

// WebRetirement controls running web services, not archived frontend assets.
// Pointer fields distinguish an omitted setting from an invalid explicit zero.
type WebRetirement struct {
	GracePeriodSeconds *int `json:"gracePeriodSeconds,omitempty"`
	MaxGenerations     *int `json:"maxGenerations,omitempty"`
}

const (
	DefaultWebGraceSeconds   = 300
	DefaultMaxWebGenerations = 2
)

func (cf *Cobaltfile) WebGraceSeconds() int {
	if cf.WebRetirement != nil && cf.WebRetirement.GracePeriodSeconds != nil {
		return *cf.WebRetirement.GracePeriodSeconds
	}
	return DefaultWebGraceSeconds
}

func (cf *Cobaltfile) MaxWebGenerations() int {
	if cf.WebRetirement != nil && cf.WebRetirement.MaxGenerations != nil {
		return *cf.WebRetirement.MaxGenerations
	}
	return DefaultMaxWebGenerations
}

package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PublishedPort exposes a container port on the host.
type PublishedPort struct {
	PublishedAs       int
	FromContainerPort int
	Protocol          string // "tcp", "udp", "sctp"
}

// ServiceVolume mounts a named docker volume into a service or container.
type ServiceVolume struct {
	VolumeName      string
	DestinationPath string
}

// NetworkAttachment binds a swarm service to an overlay network with an
// optional DNS alias. An empty Alias renders as `--network <Name>`; a
// non-empty Alias renders as `--network name=<Name>,alias=<Alias>`, which
// gives the service a stable hostname on that network independent of its
// swarm service name. Other services on the same network can resolve the
// alias via Docker's embedded DNS.
//
// Aliases matter for cross-project references (e.g. api connecting to
// `redis-redis:6379`) and within-project short names (e.g. api's web
// service reaching `worker` instead of `api-3-worker`). The alias survives
// across deployments — the swarm service name (which includes the
// deployment number) does not.
type NetworkAttachment struct {
	Name  string
	Alias string // optional
}

// ServiceCreateOpts describes a `docker service create` invocation.
type ServiceCreateOpts struct {
	ProjectID         int64
	ProjectName       string
	ServiceName       string
	DeploymentNumber  int
	Image             string
	Command           string
	EnvVars           map[string]string
	PublishedPorts    []PublishedPort
	Networks          []NetworkAttachment // first one is primary
	Replicas          int                 // 0 means use docker default (1)
	HealthCommand     string              // optional; sets --health-cmd
	HealthStartPeriod time.Duration       // defaults to 5 minutes
	HealthInterval    time.Duration       // defaults to 3 seconds
	Volumes           []ServiceVolume
	ExtraParams       []string // pre-split, e.g. via SplitParams(extraSwarmParams)
	// Name overrides the deployment-numbered default. Stable public web
	// services use an ID-based name so their Caddy upstream survives deploys.
	Name string
}

// CreateService creates a swarm service with the cobalt label set, env
// vars, networks, ports, optional healthcheck, and optional extra params.
func (c *Client) CreateService(ctx context.Context, opts ServiceCreateOpts) error {
	name := serviceNameForOpts(opts)
	args := []string{
		"service", "create",
		// --detach=true returns as soon as Swarm accepts the spec.
		// Docker 20.10+ defaults to synchronous "wait for service to
		// converge" mode, which hangs forever on a crash-looping
		// container — the daemon's WaitForServiceHealthy is the
		// proper place to wait, with fail-fast on shutdown count.
		// Without --detach, a deploy of a broken image wedges in
		// `🚀 starting service` and never reaches the fail-fast path.
		"--detach=true",
		"--name", name,
		"--with-registry-auth",
	}

	for _, l := range serviceLabels(opts.ProjectID, opts.ProjectName, opts.ServiceName, opts.DeploymentNumber) {
		args = append(args, "--label", l)
	}
	// --label lands on the swarm service object only; --container-label
	// puts the same set on every task container, so per-container views
	// (`docker stats` attribution) resolve ownership without parsing
	// swarm task names. See ResolveOwner.
	for _, l := range serviceLabels(opts.ProjectID, opts.ProjectName, opts.ServiceName, opts.DeploymentNumber) {
		args = append(args, "--container-label", l)
	}

	// Env vars: deterministic order so argv is reproducible for tests.
	envKeys := sortedKeys(opts.EnvVars)
	for _, k := range envKeys {
		args = append(args, "--env", k+"="+opts.EnvVars[k])
	}

	for _, n := range opts.Networks {
		args = append(args, "--network", networkFlagValue(n))
	}
	for _, p := range opts.PublishedPorts {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		args = append(
			args,
			"--publish",
			fmt.Sprintf("published=%d,target=%d,protocol=%s", p.PublishedAs, p.FromContainerPort, proto),
		)
	}
	for _, v := range opts.Volumes {
		args = append(
			args, "--mount",
			fmt.Sprintf("type=volume,source=%s,destination=%s", v.VolumeName, v.DestinationPath),
		)
	}
	if opts.Replicas > 0 {
		args = append(args, "--replicas", strconv.Itoa(opts.Replicas))
	}
	if opts.HealthCommand != "" {
		startPeriod := opts.HealthStartPeriod
		if startPeriod == 0 {
			startPeriod = 5 * time.Minute
		}
		interval := opts.HealthInterval
		if interval == 0 {
			interval = 3 * time.Second
		}
		args = append(
			args,
			"--health-cmd", opts.HealthCommand,
			"--health-start-period", durationFlag(startPeriod),
			"--health-start-interval", durationFlag(interval),
		)
	}
	args = append(args, opts.ExtraParams...)
	args = append(args, opts.Image)
	// Shell-split the command so multi-arg commands work as operators
	// expect. Passing opts.Command as a single argv string makes docker
	// treat it as a literal binary path, which exits immediately with
	// "executable file not found in $PATH". See ShellSplit in build.go
	// for the parsing rules and rationale.
	args = append(args, ShellSplit(opts.Command)...)

	return c.run(ctx, args...)
}

// ReconcileStableService creates a stable public-web service on its first
// deploy and updates it in place thereafter. Swarm's start-first update order
// keeps the service VIP continuously resolvable while tasks roll over.
func (c *Client) ReconcileStableService(ctx context.Context, opts ServiceCreateOpts) error {
	if opts.Name == "" {
		return errors.New("stable service requires an explicit name")
	}
	if err := c.run(ctx, "service", "inspect", opts.Name); err != nil {
		if !isNotFound(err) {
			return err
		}
		return c.CreateService(ctx, opts)
	}
	oldEnv, err := c.stableServiceEnvKeys(ctx, opts.Name)
	if err != nil {
		return err
	}

	args := []string{
		"service", "update", "--detach=true",
		"--update-order", "start-first",
		"--update-parallelism", "1",
		"--update-failure-action", "rollback",
		"--update-monitor", "90s",
		"--rollback-order", "start-first",
		"--image", opts.Image,
	}
	for _, l := range serviceLabels(opts.ProjectID, opts.ProjectName, opts.ServiceName, opts.DeploymentNumber) {
		args = append(args, "--label-add", l, "--container-label-add", l)
	}
	for _, k := range sortedKeys(opts.EnvVars) {
		args = append(args, "--env-add", k+"="+opts.EnvVars[k])
	}
	for _, k := range oldEnv {
		if _, wanted := opts.EnvVars[k]; !wanted {
			args = append(args, "--env-rm", k)
		}
	}
	// Stable public-web services always use their ID-based alias on cobalt-main.
	// The attachment is created once and must not be added again on every update.
	if opts.Replicas > 0 {
		args = append(args, "--replicas", strconv.Itoa(opts.Replicas))
	}
	if opts.HealthCommand != "" {
		args = append(args, "--health-cmd", opts.HealthCommand)
	}
	if opts.Command != "" {
		args = append(args, "--args", opts.Command)
	}
	return c.run(ctx, append(args, opts.Name)...)
}

func (c *Client) stableServiceEnvKeys(ctx context.Context, name string) ([]string, error) {
	out, err := c.output(ctx, "service", "inspect", "--format", "{{json .Spec.TaskTemplate.ContainerSpec.Env}}", name)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 || string(out) == "null" {
		return nil, nil
	}
	var env []string
	if err := json.Unmarshal(out, &env); err != nil {
		return nil, fmt.Errorf("inspect stable service env: %w", err)
	}
	keys := make([]string, 0, len(env))
	for _, entry := range env {
		if i := strings.IndexByte(entry, '='); i > 0 {
			keys = append(keys, entry[:i])
		}
	}
	sortStrings(keys)
	return keys, nil
}

func serviceNameForOpts(opts ServiceCreateOpts) string {
	if opts.Name != "" {
		return opts.Name
	}
	return ServiceName(opts.ProjectName, opts.DeploymentNumber, opts.ServiceName)
}

// RemoveService removes a swarm service by name. Returns nil if the service
// is already gone.
func (c *Client) RemoveService(ctx context.Context, name string) error {
	if err := c.run(ctx, "service", "rm", name); err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

// ScaleService changes replica count on an existing service.
func (c *Client) ScaleService(ctx context.Context, name string, replicas int) error {
	return c.run(ctx, "service", "scale", fmt.Sprintf("%s=%d", name, replicas))
}

// RestartService forces a rolling restart of an existing swarm service in
// place — same config, fresh tasks. The Caddy watchdog uses it to recover a
// wedged admin endpoint without a human SSHing in to `docker restart`.
//
// The restart is forced stop-first (old task removed before the new one
// starts). Its only caller is the Caddy ingress service, which publishes
// host-mode ports 80/443: two generations can't bind the same host port, so
// the Swarm default start-first ordering leaves the new task Pending forever
// ("no suitable node (host-mode port already in use)") while the wedged old
// task keeps running — the restart silently no-ops. Passing the order here
// also heals a service whose stored spec is start-first. The cost is a brief
// (~1-2s) window with no Caddy bound, which the watchdog already accounts for.
func (c *Client) RestartService(ctx context.Context, name string) error {
	return c.run(ctx, "service", "update", "--force", "--update-order", "stop-first", name)
}

// ServiceInfo summarizes a swarm service's identity.
type ServiceInfo struct {
	Name             string
	ProjectID        int64
	DeploymentNumber int
	ServiceName      string
	Replicas         int
}

// ListServicesForProject returns every cobalt-managed service tagged with
// the given project id, regardless of deployment number.
func (c *Client) ListServicesForProject(ctx context.Context, projectID int64) ([]ServiceInfo, error) {
	return c.listServices(ctx, FilterByProjectID(projectID))
}

// ListServicesForDeployment returns the services for a single deployment of
// a project.
func (c *Client) ListServicesForDeployment(ctx context.Context, projectID int64, deploymentNumber int) ([]ServiceInfo, error) {
	return c.listServices(ctx, FilterByDeployment(projectID, deploymentNumber))
}

func (c *Client) listServices(ctx context.Context, filters []string) ([]ServiceInfo, error) {
	args := []string{"service", "ls"}
	args = withFilterFlags(args, filters)
	args = append(args, "--format", "{{.Name}}\t{{.Replicas}}")
	out, err := c.output(ctx, args...)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	var services []ServiceInfo
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		s := ServiceInfo{Name: fields[0]}
		if len(fields) == 2 {
			s.Replicas = parseReplicas(fields[1])
		}
		services = append(services, s)
	}
	return services, nil
}

// parseReplicas pulls the desired-count denominator out of
// "<running>/<desired>" replica strings (e.g. "3/1" right after a
// scale-down).
//
// We want desired, not running. Callers ask "how many replicas is this
// service scaled to?" and need a stable answer that doesn't drift while
// swarm is draining or ramping. Reading the running numerator made
// `cobalt scale set web=1` print "web=3" immediately after a 3→1 scale
// because docker still reported "3/1" until the two extra tasks shut
// down.
//
// On failure, returns 0 — the caller usually treats unknown as "fall
// back to default".
func parseReplicas(s string) int {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
	}
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// WaitForServiceReady polls `docker service ps` until enough replicas reach
// the Running state, or the budget runs out, or it observes 3×replicas
// Shutdown states (matches upstream's "fail fast on bad image / placement"
// heuristic).
func (c *Client) WaitForServiceReady(ctx context.Context, name string, replicas int, timeout time.Duration) error {
	if replicas < 1 {
		replicas = 1
	}
	deadline := time.Now().Add(timeout)
	const pollInterval = 3 * time.Second

	for {
		states, err := c.taskStates(ctx, name)
		if err != nil {
			return fmt.Errorf("waitReady %s: %w", name, err)
		}
		var running, shutdown int
		for _, s := range states {
			switch s {
			case "Running":
				running++
			case "Shutdown", "Failed", "Rejected":
				shutdown++
			}
		}
		if running >= replicas {
			return nil
		}
		if shutdown >= 3*replicas {
			return fmt.Errorf("waitReady %s: %d shutdown/failed states (>= 3 * %d replicas)", name, shutdown, replicas)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("waitReady %s: timeout after %s", name, timeout)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (c *Client) taskStates(ctx context.Context, serviceName string) ([]string, error) {
	out, err := c.output(
		ctx,
		"service", "ps", serviceName,
		"--no-trunc",
		"--format", "{{.CurrentState}}",
	)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	var states []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// CurrentState looks like "Running 5 minutes ago" — take the first
		// word as the state.
		state := line
		if i := strings.IndexByte(line, ' '); i > 0 {
			state = line[:i]
		}
		states = append(states, state)
	}
	return states, nil
}

// networkFlagValue renders a NetworkAttachment as the string passed to
// `--network`. When the alias is empty we emit the bare network name; with
// an alias we use docker's name=,alias= form so the service registers a
// per-network DNS alias.
func networkFlagValue(n NetworkAttachment) string {
	if n.Alias == "" {
		return n.Name
	}
	return "name=" + n.Name + ",alias=" + n.Alias
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func durationFlag(d time.Duration) string {
	// docker accepts "3s", "5m", etc.
	return d.String()
}

// isNotFound reports whether err looks like a "no such ..." docker error.
// Used to treat repeat-removes as no-ops.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no such") ||
		strings.Contains(s, "not found") ||
		errors.Is(err, errNotFound)
}

var errNotFound = errors.New("not found")

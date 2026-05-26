package docker

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
)

// BuildOpts describes a single image build.
type BuildOpts struct {
	ProjectID        int64
	ProjectName      string
	ImageName        string // logical name from cobaltfile.images.<name>
	DeploymentNumber int
	Dockerfile       string // relative to Context
	Context          string // build context dir
	EnvSecrets       map[string]string
	NoCache          bool
	// CacheDir, when non-empty, is a per-project BuildKit local cache
	// directory. We pass it as both `--cache-from type=local,src=...` and
	// `--cache-to type=local,dest=...`. This isolates BuildKit's layer
	// cache between projects, so two projects sharing a repo can't poison
	// each other's builds (improvement E from the deploy-flow audit).
	CacheDir string
	// BuilderName selects the buildx instance to route the build through.
	// Empty falls back to BuildxBuilderName (the shared builder). The
	// deploy layer sets this to "cobalt-builder-<projectID>" for projects
	// that share source with at least one sibling — isolating BuildKit's
	// in-memory secret cache so two parallel builds of the same source
	// can't cross-contaminate each other's `--secret` values (cobalt#24).
	BuilderName string
	// Output, when non-nil, captures buildx's stdout AND stderr (interleaved
	// — buildkit writes its progress to stderr, image layer progress to
	// stdout). Callers tee this into the per-deployment log file so
	// `cobalt deployments output` and the SSE follow-stream have content
	// to render.
	Output io.Writer
}

// Build builds an image and tags it as InternalImageName(...).
//
// Each entry in EnvSecrets is exposed to the build two ways:
//
//  1. Per-key as `--secret id=KEY,env=KEY`. The Dockerfile opts in with
//     `RUN --mount=type=secret,id=KEY ...` and reads /run/secrets/KEY.
//  2. As a single aggregate `--secret id=.env,env=COBALT_DOT_ENV` whose
//     body is a dotenv-formatted file (KEY=VAL per line, values
//     conditionally double-quoted to survive newlines/quotes). This
//     matches the disco-era contract many Dockerfiles already expect:
//     `RUN --mount=type=secret,id=.env cp /run/secrets/.env .env && ...`.
//
// Secrets are NOT visible in image layers — this is the point of using
// --secret over --build-arg. Values are passed to the buildx subprocess
// via its environment so buildkit's `env=KEY` resolver finds them.
func (c *Client) Build(ctx context.Context, opts BuildOpts) (string, error) {
	if opts.ProjectName == "" || opts.ImageName == "" {
		return "", fmt.Errorf("docker.Build: ProjectName and ImageName required")
	}

	tag := InternalImageName(opts.ProjectName, opts.ImageName, opts.DeploymentNumber)
	// "buildx build --builder <name>" routes the build through a
	// docker-container driver instance. --load imports the resulting
	// image into the host's docker engine so swarm can run it.
	//
	// BuilderName empty → shared builder (default). The deploy layer
	// picks an isolated "cobalt-builder-<projectID>" builder name for
	// projects that share source with siblings (cobalt#24).
	builder := opts.BuilderName
	if builder == "" {
		builder = BuildxBuilderName
	}
	args := []string{
		"buildx", "build",
		"--builder", builder,
		"--load",
		"-t", tag,
	}
	if opts.Dockerfile != "" {
		args = append(args, "-f", opts.Dockerfile)
	}
	if opts.NoCache {
		args = append(args, "--no-cache")
	}
	for _, label := range serviceLabels(opts.ProjectID, opts.ProjectName, opts.ImageName, opts.DeploymentNumber) {
		args = append(args, "--label", label)
	}

	// Sort secret keys so the argv we generate is deterministic — matters
	// for tests, build cache layer reuse, and human readers.
	keys := make([]string, 0, len(opts.EnvSecrets))
	for k := range opts.EnvSecrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// `env=KEY` tells buildkit to read the value from the build
		// subprocess's env. Without it (`id=KEY` alone), buildkit
		// falls back to treating KEY as a filename — past versions
		// of cobalt hit "stat KEY: no such file or directory" exactly
		// for this reason.
		args = append(args, "--secret", "id="+k+",env="+k)
	}

	// Aggregate `.env` secret: a single dotenv-formatted file containing
	// every env var, mounted at /run/secrets/.env. Emitted unconditionally
	// (even when EnvSecrets is empty) so Dockerfiles that always
	// `RUN --mount=type=secret,id=.env ...` see a present-but-possibly-
	// empty file. The subprocess env var name is COBALT_DOT_ENV (cobalt-
	// prefixed to dodge collisions with user keys). buildkit then
	// resolves `id=.env,env=COBALT_DOT_ENV` to that string.
	args = append(args, "--secret", "id=.env,env=COBALT_DOT_ENV")

	if opts.CacheDir != "" {
		args = append(
			args,
			"--cache-from", "type=local,src="+opts.CacheDir,
			"--cache-to", "type=local,dest="+opts.CacheDir+",mode=max",
		)
	}

	contextDir := opts.Context
	if contextDir == "" {
		contextDir = "."
	}
	args = append(args, contextDir)

	// Thread the env values into the buildx subprocess so buildkit's
	// `env=KEY` resolver finds them. EnvSecrets is the project's
	// per-deploy env state; we don't leak it into the daemon's own
	// environment. We also synthesize COBALT_DOT_ENV (the aggregate
	// `.env` secret body) here rather than mutating the caller's map.
	runEnv := make(map[string]string, len(opts.EnvSecrets)+1)
	for k, v := range opts.EnvSecrets {
		runEnv[k] = v
	}
	runEnv["COBALT_DOT_ENV"] = formatDotEnv(opts.EnvSecrets)
	if err := c.runner.RunWithEnv(ctx, runEnv, args, nil, opts.Output, opts.Output); err != nil {
		return "", fmt.Errorf("docker.Build %s: %w", tag, err)
	}
	return tag, nil
}

// formatDotEnv serializes env into a deterministic dotenv-formatted body
// (sorted keys, KEY=VAL per line, trailing newline). Values containing
// newlines, quotes, backslashes, leading/trailing whitespace, or starting
// with '#' are double-quoted with backslash escapes so the file remains
// parseable by the dotenv libraries Vite/Next/etc. use. Plain values are
// emitted unquoted to keep the file legible.
//
// Returns "" for an empty/nil map (matches an empty .env file).
func formatDotEnv(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(quoteDotEnvValue(env[k]))
		b.WriteByte('\n')
	}
	return b.String()
}

// quoteDotEnvValue wraps v in double quotes (with backslash escapes for
// `\`, `"`, literal newlines, and carriage returns) when v contains any
// character that would corrupt a plain `KEY=VAL` line — newlines, quotes,
// backslashes, leading/trailing whitespace, or a leading '#' (which most
// dotenv parsers treat as a comment). Otherwise returns v unchanged.
func quoteDotEnvValue(v string) string {
	if v == "" {
		return ""
	}
	needsQuote := v[0] == '#'
	if !needsQuote {
		switch v[0] {
		case ' ', '\t':
			needsQuote = true
		}
	}
	if !needsQuote {
		switch v[len(v)-1] {
		case ' ', '\t':
			needsQuote = true
		}
	}
	if !needsQuote {
		for i := 0; i < len(v); i++ {
			switch v[i] {
			case '\n', '\r', '"', '\\':
				needsQuote = true
			}
			if needsQuote {
				break
			}
		}
	}
	if !needsQuote {
		return v
	}

	var b strings.Builder
	b.Grow(len(v) + 4)
	b.WriteByte('"')
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// SplitParams parses an extraSwarmParams / extraRunParams string into argv
// fragments. Whitespace-only or empty input returns nil. Internal whitespace
// runs collapse: "  --foo   bar  " → ["--foo", "bar"].
//
// Exposed so service-create and container-run paths share the same parsing.
func SplitParams(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Fields(s)
	if len(parts) == 0 {
		return nil
	}
	return parts
}

// ShellSplit parses a command string into argv the way a shell would —
// whitespace separates tokens, but single and double quotes hold their
// contents together (including embedded whitespace) and backslash escapes
// the next character.
//
// Why this exists: cobaltfile `command:` strings need shell-like parsing
// because operators write them in shell syntax. Examples:
//
//	"redis-server --maxmemory-policy noeviction"
//	    → ["redis-server", "--maxmemory-policy", "noeviction"]
//
//	"sh -c 'CI=true pnpm migrate:deploy && pnpm start'"
//	    → ["sh", "-c", "CI=true pnpm migrate:deploy && pnpm start"]
//
// `strings.Fields` (what SplitParams uses) splits on every whitespace,
// which mangles the second case into ten broken tokens. `docker service
// create <image> <opts.Command>` passing the whole string as one arg is
// also wrong — docker treats it as Cmd[0], i.e. a single binary literally
// named "redis-server --maxmemory-policy noeviction", which exits with
// "executable file not found".
//
// Matches disco's behavior (utils/docker.py uses Python's shlex.split).
// Handles: bare words, single quotes, double quotes, backslash escapes
// outside quotes, backslash escapes inside double quotes (per POSIX:
// inside single quotes, backslashes are literal). Does NOT handle env
// var expansion ($FOO) — that's a shell feature, not a parser feature;
// callers wanting variable expansion should use `sh -c '...'`.
func ShellSplit(s string) []string {
	var out []string
	var cur strings.Builder
	inToken := false

	const (
		none    = 0
		singleQ = 1
		doubleQ = 2
	)
	state := none

	flush := func() {
		if inToken {
			out = append(out, cur.String())
			cur.Reset()
			inToken = false
		}
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch state {
		case none:
			switch {
			case c == ' ' || c == '\t' || c == '\n' || c == '\r':
				flush()
			case c == '\'':
				inToken = true
				state = singleQ
			case c == '"':
				inToken = true
				state = doubleQ
			case c == '\\' && i+1 < len(s):
				inToken = true
				cur.WriteByte(s[i+1])
				i++
			default:
				inToken = true
				cur.WriteByte(c)
			}
		case singleQ:
			// Inside single quotes nothing is special — not even backslash.
			if c == '\'' {
				state = none
			} else {
				cur.WriteByte(c)
			}
		case doubleQ:
			switch {
			case c == '"':
				state = none
			case c == '\\' && i+1 < len(s):
				// Inside double quotes, backslash escapes only $ ` " \ and
				// newline (POSIX). Outside those, it's literal. We're
				// conservative: always treat as escape so callers don't get
				// surprised, since cobalt commands aren't shell-evaluated
				// anyway (no $var, no backtick).
				cur.WriteByte(s[i+1])
				i++
			default:
				cur.WriteByte(c)
			}
		}
	}
	flush()
	return out
}

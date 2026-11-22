package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
)

// BuiltService pairs a parsed cobaltfile service with the docker image
// tag the build phase produced for it. ImageTag is empty for static-only
// services and for services with a custom `build:` command (handled later).
type BuiltService struct {
	Name    string
	Service cobaltfile.Service
	// ImageTag is "" if the service does not need an image (e.g. type=static
	// without a generator command).
	ImageTag string
}

// Builder turns a workspace + parsed cobaltfile into a set of built
// services ready for cutover. Different services may share an image; we
// build each unique image once.
//
// out, when non-nil, receives buildx's progress output (stdout + stderr
// interleaved) so the orchestrator can tee it into the per-deployment
// log file.
type Builder interface {
	Build(ctx context.Context, project store.Project, dep store.Deployment, ws *Workspace, out io.Writer) ([]BuiltService, error)
}

// EnvLister is the subset of *store.DB the builder uses to gather build
// secrets for a project.
type EnvLister interface {
	EnvVarMap(ctx context.Context, projectID int64) (map[string]string, error)
}

// ProjectQuerier is the subset of *store.DB the builder uses to decide
// whether a project needs an isolated buildx instance. Kept distinct
// from EnvLister so a future expansion of either responsibility doesn't
// muddle the other's contract.
type ProjectQuerier interface {
	OtherProjectsWithSameSource(ctx context.Context, projectID int64) (int, error)
}

// DockerImageBuilder is the docker primitive the builder shells out to.
// Implemented by *docker.Client; defined here so tests can fake it.
type DockerImageBuilder interface {
	Build(ctx context.Context, opts docker.BuildOpts) (string, error)
	EnsureBuildxBuilder(ctx context.Context, name string) error
}

// ImagePuller is the docker subset a prebuilt-image deploy uses.
// Implemented by *docker.Client.
type ImagePuller interface {
	PullImage(ctx context.Context, ref string, auth *docker.RegistryAuth, out io.Writer) error
	TagImage(ctx context.Context, src, dst string) error
	ImageLabel(ctx context.Context, ref, key string) (string, error)
	RemoveImage(ctx context.Context, tag string) error
}

// RegistryCredentials looks up the daemon-wide pull login for a
// registry host. Implemented by *store.DB.
type RegistryCredentials interface {
	GetRegistryCredential(ctx context.Context, registry string) (*store.RegistryCredential, error)
}

// RevisionLabel is the OCI label CI stamps with the source commit. A
// prebuilt image whose label names another commit than the checked-out
// cobalt.json is refused.
const RevisionLabel = "org.opencontainers.image.revision"

// NewBuilder returns a Builder that uses the supplied docker primitive
// and per-project BuildKit cache directory rooted at dataDir/buildkit-cache.
//
// projects is consulted on every build to pick the right buildx instance:
// shared by default, isolated when the project shares (repo, branch,
// path) with another (cobalt#24).
func NewBuilder(d DockerImageBuilder, env EnvLister, projects ProjectQuerier, dataDir string) Builder {
	return &dockerBuilder{docker: d, env: env, projects: projects, dataDir: dataDir}
}

// NewBuilderWithPull is NewBuilder plus the pull path for deployments
// that carry prebuilt image references.
func NewBuilderWithPull(d DockerImageBuilder, env EnvLister, projects ProjectQuerier, dataDir string, puller ImagePuller, registries RegistryCredentials) Builder {
	return &dockerBuilder{docker: d, env: env, projects: projects, dataDir: dataDir, puller: puller, registries: registries}
}

type dockerBuilder struct {
	docker     DockerImageBuilder
	env        EnvLister
	projects   ProjectQuerier
	dataDir    string
	puller     ImagePuller
	registries RegistryCredentials
}

func (b *dockerBuilder) Build(ctx context.Context, project store.Project, dep store.Deployment, ws *Workspace, out io.Writer) ([]BuiltService, error) {
	if ws == nil || ws.Cobaltfile == nil {
		return nil, fmt.Errorf("deploy.Build: workspace required")
	}
	if len(dep.ImageRefs) > 0 {
		return b.pullPrebuilt(ctx, project, dep, ws, out)
	}
	if project.ImageSource == validator.ImageSourceRegistry {
		// The API refuses this; a row from an older client must not
		// fall back to building on the host.
		return nil, fmt.Errorf("deploy.Build: project %q deploys prebuilt images; this deployment has no image reference", project.Name)
	}

	envSecrets, err := b.env.EnvVarMap(ctx, project.ID)
	if err != nil {
		return nil, fmt.Errorf("deploy.Build: list env: %w", err)
	}

	cacheDir := ""
	if b.dataDir != "" {
		cacheDir = filepath.Join(b.dataDir, "buildkit-cache", strconv.FormatInt(project.ID, 10))
	}

	// Pick the buildx instance for this build. Shared by default; if any
	// other project points at the same (repo, branch, path), promote to
	// an isolated per-project builder so the two can't poison each
	// other's `--mount=type=secret` cache (cobalt#24).
	//
	// Store failure → soft-fall-back to the shared builder. We prefer
	// availability over correctness here because the cache race is rare
	// and a transient store error would otherwise block the deploy.
	// EnsureBuildxBuilder failure → hard-fail: we decided isolation is
	// needed; silently using the shared builder would re-introduce the
	// race we set out to avoid.
	builderName := ""
	if b.projects != nil {
		siblingCount, err := b.projects.OtherProjectsWithSameSource(ctx, project.ID)
		switch {
		case err != nil:
			if out != nil {
				fmt.Fprintf(out, "⚠️  could not check source siblings (%v); using shared builder\n", err)
			}
		case siblingCount > 0:
			builderName = docker.IsolatedBuilderName(project.ID)
			if err := b.docker.EnsureBuildxBuilder(ctx, builderName); err != nil {
				return nil, fmt.Errorf("deploy.Build: ensure isolated builder %q: %w", builderName, err)
			}
			if out != nil {
				fmt.Fprintf(out, "🔒 isolated builder %q (%d sibling project(s) share this source)\n", builderName, siblingCount)
			}
		}
	}

	// Build each unique image only once. Mirrors disco's resolution
	// (utils/docker.py:1218-1228): if `svc.Image` matches a key in
	// `cf.Images`, build from the Dockerfile spec under that key;
	// otherwise treat `svc.Image` as a pre-built registry reference
	// (e.g. `redis/redis-stack:7.4.0-v8`) and skip the build entirely.
	// `BuiltService.ImageTag` ends up either as the built tag or the
	// verbatim pre-built reference — same downstream consumer either way.
	tagByImage := map[string]string{}
	for _, svc := range ws.Cobaltfile.Services {
		if !needsBuild(svc) {
			continue
		}
		if _, done := tagByImage[svc.Image]; done {
			continue
		}
		img, ok := ws.Cobaltfile.Images[svc.Image]
		if !ok {
			// Pre-built registry reference — no Dockerfile to build.
			// The image string is passed through to the BuiltService
			// ImageTag below; `docker service create --image <ref>` will
			// `docker pull` it on first use. A bogus ref surfaces as a
			// pull error at deploy time.
			tagByImage[svc.Image] = svc.Image
			if out != nil {
				fmt.Fprintf(out, "📦 using pre-built image %q (no Dockerfile in repo)\n", svc.Image)
			}
			continue
		}

		if out != nil {
			fmt.Fprintf(out, "🔨 building image %q (Dockerfile=%s context=%s)\n", svc.Image, img.Dockerfile, img.Context)
		}
		buildStart := time.Now()
		opts := docker.BuildOpts{
			ProjectID:        project.ID,
			ProjectName:      project.Name,
			ImageName:        svc.Image,
			DeploymentNumber: dep.Number,
			Commit:           ws.Commit,
			Dockerfile:       filepath.Join(ws.Path, img.Dockerfile),
			Context:          filepath.Join(ws.Path, img.Context),
			EnvSecrets:       envSecrets,
			NoCache:          dep.NoCache,
			CacheDir:         cacheDir,
			BuilderName:      builderName,
			Output:           out,
		}
		tag, err := b.docker.Build(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("deploy.Build: image %q: %w", svc.Image, err)
		}
		if out != nil {
			fmt.Fprintf(out, "✅ built %s (%s)\n", tag, time.Since(buildStart).Round(time.Second))
		}
		tagByImage[svc.Image] = tag
	}

	built := make([]BuiltService, 0, len(ws.Cobaltfile.Services))
	for name, svc := range ws.Cobaltfile.Services {
		built = append(built, BuiltService{
			Name:     name,
			Service:  svc,
			ImageTag: tagByImage[svc.Image],
		})
	}
	return built, nil
}

// pullPrebuilt resolves every cobalt.json image from dep.ImageRefs: pull
// the digest-pinned reference, check its revision label, and tag it with
// the same internal name a build would produce. Rollback, image
// retention and hooks then work unchanged. Nothing is built.
func (b *dockerBuilder) pullPrebuilt(ctx context.Context, project store.Project, dep store.Deployment, ws *Workspace, out io.Writer) ([]BuiltService, error) {
	if out == nil {
		out = io.Discard
	}
	if b.puller == nil {
		return nil, fmt.Errorf("deploy.Build: prebuilt images are not supported by this builder")
	}
	needed := map[string]bool{}
	for _, svc := range ws.Cobaltfile.Services {
		if !needsBuild(svc) {
			continue
		}
		if _, declared := ws.Cobaltfile.Images[svc.Image]; declared {
			needed[svc.Image] = true
		}
	}
	for name := range dep.ImageRefs {
		if !needed[name] {
			return nil, fmt.Errorf("deploy.Build: image %q is not used by any service in cobalt.json", name)
		}
	}
	names := make([]string, 0, len(needed))
	for name := range needed {
		if _, ok := dep.ImageRefs[name]; !ok {
			return nil, fmt.Errorf("deploy.Build: no prebuilt reference for image %q; pass --image %s=<ref@sha256:...>", name, name)
		}
		names = append(names, name)
	}
	sort.Strings(names)

	tagByImage := map[string]string{}
	for _, name := range names {
		ref := dep.ImageRefs[name]
		auth, err := b.registryAuth(ctx, ref)
		if err != nil {
			return nil, err
		}
		login := "anonymous"
		if auth != nil {
			login = "as " + auth.Username
		}
		fmt.Fprintf(out, "📦 pulling image %q %s (%s)\n", name, ref, login)
		start := time.Now()
		if err := b.puller.PullImage(ctx, ref, auth, out); err != nil {
			return nil, fmt.Errorf("deploy.Build: image %q: %w", name, err)
		}
		revision, err := b.puller.ImageLabel(ctx, ref, RevisionLabel)
		if err != nil {
			return nil, fmt.Errorf("deploy.Build: image %q: %w", name, err)
		}
		if revision != "" && ws.Commit != "" && revision != ws.Commit {
			return nil, fmt.Errorf("deploy.Build: image %q was built from %s, but the deployment checked out %s", name, shortSHA(revision), shortSHA(ws.Commit))
		}
		tag := docker.InternalImageName(project.Name, name, dep.Number)
		if err := b.puller.TagImage(ctx, ref, tag); err != nil {
			return nil, fmt.Errorf("deploy.Build: image %q: %w", name, err)
		}
		// Keep the internal tag as the only name, so image retention owns
		// the image's lifetime. Pulls by the same digest stay cheap: Docker
		// keeps the layers while any tag uses them.
		if err := b.puller.RemoveImage(ctx, ref); err != nil {
			fmt.Fprintf(out, "⚠️  could not drop pulled name %s: %v\n", ref, err)
		}
		fmt.Fprintf(out, "✅ pulled %s as %s (%s)\n", ref, tag, time.Since(start).Round(time.Second))
		tagByImage[name] = tag
	}

	built := make([]BuiltService, 0, len(ws.Cobaltfile.Services))
	for name, svc := range ws.Cobaltfile.Services {
		tag := ""
		if needsBuild(svc) {
			tag = tagByImage[svc.Image]
			if tag == "" {
				// A registry reference written in cobalt.json itself.
				tag = svc.Image
			}
		}
		built = append(built, BuiltService{Name: name, Service: svc, ImageTag: tag})
	}
	return built, nil
}

func (b *dockerBuilder) registryAuth(ctx context.Context, ref string) (*docker.RegistryAuth, error) {
	if b.registries == nil {
		return nil, nil
	}
	host := docker.RegistryHost(ref)
	cred, err := b.registries.GetRegistryCredential(ctx, host)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("deploy.Build: registry login for %s: %w", host, err)
	}
	return &docker.RegistryAuth{Registry: host, Username: cred.Username, Password: cred.Password}, nil
}

// needsBuild mirrors cobaltfile's "this service needs an image" rule.
// Static-only services serve directly from disk; custom-build services
// have their own `build:` command and don't use the image table.
func needsBuild(s cobaltfile.Service) bool {
	if s.Build != "" {
		return false
	}
	if s.Type == cobaltfile.TypeStatic && s.Command == "" {
		return false
	}
	return true
}

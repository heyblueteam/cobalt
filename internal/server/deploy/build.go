package deploy

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/cobaltfile"
	"github.com/heyblueteam/cobalt/internal/server/docker"
	"github.com/heyblueteam/cobalt/internal/server/store"
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

// NewBuilder returns a Builder that uses the supplied docker primitive
// and per-project BuildKit cache directory rooted at dataDir/buildkit-cache.
//
// projects is consulted on every build to pick the right buildx instance:
// shared by default, isolated when the project shares (repo, branch,
// path) with another (cobalt#24).
func NewBuilder(d DockerImageBuilder, env EnvLister, projects ProjectQuerier, dataDir string) Builder {
	return &dockerBuilder{docker: d, env: env, projects: projects, dataDir: dataDir}
}

type dockerBuilder struct {
	docker   DockerImageBuilder
	env      EnvLister
	projects ProjectQuerier
	dataDir  string
}

func (b *dockerBuilder) Build(ctx context.Context, project store.Project, dep store.Deployment, ws *Workspace, out io.Writer) ([]BuiltService, error) {
	if ws == nil || ws.Cobaltfile == nil {
		return nil, fmt.Errorf("deploy.Build: workspace required")
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

package deploy

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

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
type Builder interface {
	Build(ctx context.Context, project store.Project, dep store.Deployment, ws *Workspace) ([]BuiltService, error)
}

// EnvLister is the subset of *store.DB the builder uses to gather build
// secrets for a project.
type EnvLister interface {
	EnvVarMap(ctx context.Context, projectID int64) (map[string]string, error)
}

// DockerImageBuilder is the docker primitive the builder shells out to.
// Implemented by *docker.Client; defined here so tests can fake it.
type DockerImageBuilder interface {
	Build(ctx context.Context, opts docker.BuildOpts) (string, error)
}

// NewBuilder returns a Builder that uses the supplied docker primitive
// and per-project BuildKit cache directory rooted at dataDir/buildkit-cache.
func NewBuilder(d DockerImageBuilder, env EnvLister, dataDir string) Builder {
	return &dockerBuilder{docker: d, env: env, dataDir: dataDir}
}

type dockerBuilder struct {
	docker  DockerImageBuilder
	env     EnvLister
	dataDir string
}

func (b *dockerBuilder) Build(ctx context.Context, project store.Project, dep store.Deployment, ws *Workspace) ([]BuiltService, error) {
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

	// Build each unique image only once.
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
			return nil, fmt.Errorf("deploy.Build: service references unknown image %q", svc.Image)
		}

		opts := docker.BuildOpts{
			ProjectID:        project.ID,
			ProjectName:      project.Name,
			ImageName:        svc.Image,
			DeploymentNumber: dep.Number,
			Dockerfile:       filepath.Join(ws.Path, img.Dockerfile),
			Context:          filepath.Join(ws.Path, img.Context),
			EnvSecrets:       envSecrets,
			NoCache:          dep.NoCache,
			CacheDir:         cacheDir,
		}
		tag, err := b.docker.Build(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("deploy.Build: image %q: %w", svc.Image, err)
		}
		tagByImage[svc.Image] = tag
	}

	out := make([]BuiltService, 0, len(ws.Cobaltfile.Services))
	for name, svc := range ws.Cobaltfile.Services {
		out = append(out, BuiltService{
			Name:     name,
			Service:  svc,
			ImageTag: tagByImage[svc.Image],
		})
	}
	return out, nil
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

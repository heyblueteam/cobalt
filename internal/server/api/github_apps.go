package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ListGithubApps implements GET /api/github-apps.
func (h *Handler) ListGithubApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.DB.ListGithubApps(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.GithubApp, 0, len(apps))
	for _, a := range apps {
		out = append(out, githubAppToAPI(a))
	}
	writeJSON(w, out)
}

// ListGithubAppRepos implements GET /api/github-app-repos. Returns
// every repo accessible across all installations on this daemon.
func (h *Handler) ListGithubAppRepos(w http.ResponseWriter, r *http.Request) {
	apps, err := h.DB.ListGithubApps(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var out []cobaltapi.GithubAppRepo
	for _, app := range apps {
		insts, err := h.DB.ListGithubAppInstallations(r.Context(), app.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for _, inst := range insts {
			repos, err := h.DB.ListGithubReposForInstallation(r.Context(), inst.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			for _, repo := range repos {
				api := cobaltapi.GithubAppRepo{
					ID:             repo.ID,
					InstallationID: repo.InstallationID,
					RepoID:         repo.RepoID,
					FullName:       repo.FullName,
					Private:        repo.Private,
				}
				if repo.DefaultBranch != "" {
					api.DefaultBranch = repo.DefaultBranch
				}
				out = append(out, api)
			}
		}
	}
	if out == nil {
		out = []cobaltapi.GithubAppRepo{}
	}
	writeJSON(w, out)
}

// PruneGithubApps implements POST /api/github-apps/prune. Synchronously
// reconciles local DB state with GitHub: removes apps GitHub no longer
// has, removes installations GitHub no longer has, adds/removes repos to
// match GitHub's view of each installation, and refreshes repos whose
// metadata drifted in place (rename, visibility, default branch) —
// following renames through to the projects that track them.
func (h *Handler) PruneGithubApps(w http.ResponseWriter, r *http.Request) {
	apps, err := h.DB.ListGithubApps(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := cobaltapi.PruneResponse{}
	for _, app := range apps {
		jwt, err := github.SignAppJWT(app.AppID, app.PrivateKey, time.Now())
		if err != nil {
			h.Log.Warn("prune: sign jwt", "app_id", app.ID, "error", err)
			continue
		}
		exists, err := h.GitHub.AppExists(r.Context(), jwt)
		if err != nil {
			h.Log.Warn("prune: probe app", "app_id", app.ID, "error", err)
			continue
		}
		if !exists {
			if err := h.DB.DeleteGithubApp(r.Context(), app.ID); err != nil {
				h.Log.Error("prune: delete app", "id", app.ID, "error", err)
				continue
			}
			resp.AppsRemoved++
			continue
		}

		insts, err := h.DB.ListGithubAppInstallations(r.Context(), app.ID)
		if err != nil {
			continue
		}
		for _, inst := range insts {
			h.pruneInstallation(r.Context(), jwt, inst, &resp)
		}
	}
	writeJSON(w, resp)
}

func (h *Handler) pruneInstallation(
	ctx context.Context,
	jwt string,
	inst store.GithubAppInstallation,
	resp *cobaltapi.PruneResponse,
) {
	tok, err := h.GitHub.MintInstallationToken(ctx, jwt, inst.InstallationID)
	if err != nil {
		// 404 on token mint means GitHub no longer recognizes the
		// installation (user uninstalled the app). Drop locally.
		if github.IsStatus(err, http.StatusNotFound) {
			if err := h.DB.DeleteGithubAppInstallation(ctx, inst.ID); err != nil {
				h.Log.Error("prune: delete installation", "id", inst.ID, "error", err)
				return
			}
			resp.InstallationsRemoved++
			return
		}
		h.Log.Warn("prune: mint token", "installation", inst.ID, "error", err)
		return
	}

	githubRepos, err := h.GitHub.ListInstallationRepos(ctx, tok.Token)
	if err != nil {
		h.Log.Warn("prune: list repos", "installation", inst.ID, "error", err)
		return
	}
	githubRepoIDs := map[int64]struct{}{}
	for _, r := range githubRepos {
		githubRepoIDs[r.ID] = struct{}{}
	}

	localRepos, err := h.DB.ListGithubReposForInstallation(ctx, inst.ID)
	if err != nil {
		return
	}
	localByRepoID := map[int64]store.GithubAppRepo{}
	for _, r := range localRepos {
		localByRepoID[r.RepoID] = r
	}

	// Add GitHub-side repos not yet local; refresh local rows whose
	// metadata drifted (renamed repo, visibility flip, default-branch
	// change). Repo IDs are stable across renames, so a rename shows up
	// here as an existing ID with a different full_name — and a stale
	// full_name breaks installation-token resolution, which resolves
	// repo → installation by name.
	for _, r := range githubRepos {
		local, exists := localByRepoID[r.ID]
		if exists &&
			local.FullName == r.FullName &&
			local.Private == r.Private &&
			local.DefaultBranch == r.DefaultBranch {
			continue
		}
		if _, err := h.DB.AddGithubAppRepo(ctx, store.GithubAppRepo{
			InstallationID: inst.ID,
			RepoID:         r.ID,
			FullName:       r.FullName,
			Private:        r.Private,
			DefaultBranch:  r.DefaultBranch,
		}); err != nil {
			h.Log.Error("prune: upsert repo", "full_name", r.FullName, "error", err)
			continue
		}
		if !exists {
			resp.ReposAdded++
			continue
		}
		resp.ReposUpdated++
		// A name change also strands projects tracking the old name:
		// pushes arrive under the new name and match nothing. Follow
		// the rename through to the projects table.
		if local.FullName != r.FullName {
			n, err := h.DB.RetargetProjectsGithubRepo(ctx, local.FullName, r.FullName)
			if err != nil {
				h.Log.Error("prune: retarget projects",
					"from", local.FullName, "to", r.FullName, "error", err)
				continue
			}
			resp.ProjectsRetargeted += int(n)
			h.Log.Info("prune: repo renamed",
				"from", local.FullName, "to", r.FullName, "projects_retargeted", n)
		}
	}
	// Remove local repos no longer on GitHub.
	for _, r := range localRepos {
		if _, ok := githubRepoIDs[r.RepoID]; ok {
			continue
		}
		if err := h.DB.RemoveGithubAppRepo(ctx, r.RepoID); err != nil && !errors.Is(err, store.ErrNotFound) {
			h.Log.Error("prune: remove repo", "full_name", r.FullName, "error", err)
			continue
		}
		resp.ReposRemoved++
	}
}

func githubAppToAPI(a store.GithubApp) cobaltapi.GithubApp {
	out := cobaltapi.GithubApp{
		ID:        a.ID,
		AppID:     a.AppID,
		Owner:     a.Owner,
		CreatedAt: a.CreatedAt,
	}
	if a.Slug.Valid {
		out.Slug = a.Slug.String
	}
	if a.Name.Valid {
		out.Name = a.Name.String
	}
	if a.HTMLURL.Valid {
		out.HTMLURL = a.HTMLURL.String
	}
	return out
}

package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/heyblueteam/cobalt/internal/server/deploy"
	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
)

// WebhookGithub implements POST /webhooks/github. The endpoint is
// public — auth is via the HMAC signature GitHub sends in
// X-Hub-Signature-256, verified against the per-app webhook_secret.
//
// Returns 202 Accepted for every event that passes auth (even ones we
// don't act on or that fail downstream processing). GitHub's retry
// behavior makes 5xx responses worse than just absorbing the error.
func (h *Handler) WebhookGithub(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20)) // 5MB cap
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}

	// Identify which App's webhook this belongs to. GitHub puts the
	// app_id in this header — without it we can't know which webhook
	// secret to verify against.
	appIDStr := r.Header.Get("X-GitHub-Hook-Installation-Target-ID")
	if appIDStr == "" {
		writeError(w, http.StatusBadRequest, "missing X-GitHub-Hook-Installation-Target-ID")
		return
	}
	appID, err := strconv.ParseInt(appIDStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid app id header")
		return
	}
	app, err := h.DB.GetGithubAppByAppID(r.Context(), appID)
	if errors.Is(err, store.ErrNotFound) {
		// Unknown app — could be an unrelated webhook hitting our
		// endpoint, or a stale one after the user deleted the app.
		// Don't leak which.
		writeError(w, http.StatusUnauthorized, "unknown app")
		return
	}
	if err != nil {
		h.Log.Error("webhook: lookup app", "appID", appID, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if err := github.VerifySignature(app.WebhookSecret, body, r.Header.Get(github.HeaderSignature)); err != nil {
		h.Log.Warn("webhook: signature verify failed", "appID", appID, "error", err)
		writeError(w, http.StatusUnauthorized, "signature mismatch")
		return
	}

	if h.Dedup != nil && h.Dedup.Seen(r.Header.Get(github.HeaderDelivery)) {
		// Already processed this delivery; ack and bail.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	event := r.Header.Get(github.HeaderEvent)
	switch event {
	case github.EventPush:
		h.handlePush(r.Context(), body, app.ID)
	case github.EventInstallation:
		h.handleInstallation(r.Context(), body, app.ID)
	case github.EventInstallationRepositories:
		h.handleInstallationRepositories(r.Context(), body)
	default:
		// Silently ignore — we only subscribed to push + installation
		// events but GitHub may deliver others. Log so we notice if
		// something interesting starts arriving.
		h.Log.Info("webhook: unhandled event", "event", event, "appID", appID)
	}
	w.WriteHeader(http.StatusAccepted)
}

// handlePush enqueues a deployment for every project tracking the
// pushed (repo, branch) combination. Branch deletes are ignored.
func (h *Handler) handlePush(ctx context.Context, body []byte, _ int64) {
	ev, err := github.ParsePush(body)
	if err != nil {
		h.Log.Warn("webhook: parse push", "error", err)
		return
	}
	if ev.IsBranchDelete() {
		return
	}
	branch := ev.Branch()
	if branch == "" {
		return // tag pushes etc.
	}
	projects, err := h.DB.FindProjectsForRepoBranch(ctx, ev.Repository.FullName, branch)
	if err != nil {
		h.Log.Error("webhook: find projects", "error", err)
		return
	}
	enqueued := 0
	for _, p := range projects {
		// Monorepo case: if the project is scoped to a sub-path and no
		// file under that sub-path was touched by this push, skip.
		// TouchesPath is conservative — it returns true when the push
		// is truncated (>=20 commits) or has no commits listed, so we
		// never skip on incomplete information.
		if !ev.TouchesPath(p.Path) {
			h.Log.Info("webhook: skip deploy (path not touched)",
				"project", p.Name, "path", p.Path, "commit", ev.After)
			continue
		}
		req := deploy.EnqueueRequest{ProjectID: p.ID, CommitSHA: ev.After}
		id, _, err := h.Queue.Enqueue(ctx, req)
		if err != nil {
			h.Log.Error("webhook: enqueue", "project", p.Name, "error", err)
			continue
		}
		enqueued++
		h.Log.Info("webhook: deploy enqueued",
			"project", p.Name, "commit", ev.After, "deployment_id", id)
	}
	if enqueued > 0 && h.Dispatcher != nil {
		h.Dispatcher.Notify()
	}
}

// handleInstallation processes installation:created / installation:deleted.
// Created: add the installation row + every initial repo. Deleted: remove
// the installation (cascades repos via FK).
func (h *Handler) handleInstallation(ctx context.Context, body []byte, localAppID int64) {
	ev, err := github.ParseInstallation(body)
	if err != nil {
		h.Log.Warn("webhook: parse installation", "error", err)
		return
	}
	switch ev.Action {
	case "created":
		// Idempotent: if the installation already exists, skip create.
		existing, err := h.DB.GetGithubAppInstallationByInstallationID(ctx, ev.Installation.ID)
		var instLocalID int64
		switch {
		case err == nil:
			instLocalID = existing.ID
		case errors.Is(err, store.ErrNotFound):
			id, err := h.DB.CreateGithubAppInstallation(ctx, store.GithubAppInstallation{
				AppID:          localAppID,
				InstallationID: ev.Installation.ID,
				AccountLogin:   ev.Installation.Account.Login,
			})
			if err != nil {
				h.Log.Error("webhook: create installation", "error", err)
				return
			}
			instLocalID = id
		default:
			h.Log.Error("webhook: lookup installation", "error", err)
			return
		}
		for _, repo := range ev.Repositories {
			_, err := h.DB.AddGithubAppRepo(ctx, store.GithubAppRepo{
				InstallationID: instLocalID,
				RepoID:         repo.ID,
				FullName:       repo.FullName,
				Private:        repo.Private,
			})
			if err != nil {
				h.Log.Error("webhook: add repo", "full_name", repo.FullName, "error", err)
			}
		}

	case "deleted":
		existing, err := h.DB.GetGithubAppInstallationByInstallationID(ctx, ev.Installation.ID)
		if errors.Is(err, store.ErrNotFound) {
			return
		}
		if err != nil {
			h.Log.Error("webhook: lookup installation for delete", "error", err)
			return
		}
		if err := h.DB.DeleteGithubAppInstallation(ctx, existing.ID); err != nil {
			h.Log.Error("webhook: delete installation", "error", err)
		}

	default:
		// suspend / unsuspend / new_permissions_accepted etc. — ignore.
	}
}

// handleInstallationRepositories processes added / removed events when
// the user changes which repos an existing installation has access to.
func (h *Handler) handleInstallationRepositories(ctx context.Context, body []byte) {
	ev, err := github.ParseInstallationRepositories(body)
	if err != nil {
		h.Log.Warn("webhook: parse installation_repositories", "error", err)
		return
	}
	inst, err := h.DB.GetGithubAppInstallationByInstallationID(ctx, ev.Installation.ID)
	if err != nil {
		h.Log.Warn("webhook: installation_repositories on unknown installation",
			"installation_id", ev.Installation.ID, "error", err)
		return
	}
	for _, repo := range ev.RepositoriesAdded {
		if _, err := h.DB.AddGithubAppRepo(ctx, store.GithubAppRepo{
			InstallationID: inst.ID,
			RepoID:         repo.ID,
			FullName:       repo.FullName,
			Private:        repo.Private,
		}); err != nil {
			h.Log.Error("webhook: add repo", "full_name", repo.FullName, "error", err)
		}
	}
	for _, repo := range ev.RepositoriesRemoved {
		if err := h.DB.RemoveGithubAppRepo(ctx, repo.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			h.Log.Error("webhook: remove repo", "full_name", repo.FullName, "error", err)
		}
	}
}

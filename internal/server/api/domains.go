package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ListDomains implements GET /api/projects/{name}/domains.
func (h *Handler) ListDomains(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	names, err := h.DB.ListDomainsForProject(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.Domain, 0, len(names))
	for _, n := range names {
		out = append(out, cobaltapi.Domain{Name: n})
	}
	writeJSON(w, out)
}

// AddDomain implements POST /api/projects/{name}/domains. Reconciles
// Caddy synchronously after the DB write so the new domain is live by
// the time we return.
func (h *Handler) AddDomain(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	var req cobaltapi.DomainAddRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "domain name required")
		return
	}
	if err := h.DB.AddDomain(r.Context(), p.ID, req.Name); err != nil {
		if errors.Is(err, store.ErrDomainTaken) {
			writeError(w, http.StatusConflict, "domain "+req.Name+" already configured")
			return
		}
		h.Log.Error("api: add domain", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.reconcileCaddyDomains(r.Context(), p.ID); err != nil {
		// DB write succeeded; Caddy reconcile failed. The §8d
		// convergence loop will retry. Surface a partial-success.
		h.Log.Warn("api: caddy reconcile after domain add failed",
			"project_id", p.ID, "error", err)
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, cobaltapi.Domain{Name: req.Name})
}

// RemoveDomain implements DELETE /api/projects/{name}/domains/{domain}.
func (h *Handler) RemoveDomain(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	domain := r.PathValue("domain")
	if domain == "" {
		writeError(w, http.StatusBadRequest, "missing domain")
		return
	}
	if err := h.DB.RemoveDomain(r.Context(), p.ID, domain); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "domain not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.reconcileCaddyDomains(r.Context(), p.ID); err != nil {
		h.Log.Warn("api: caddy reconcile after domain remove failed",
			"project_id", p.ID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// reconcileCaddyDomains updates Caddy's host matchers for a project to
// reflect the current domains list. Tolerates a nil Caddy client (used
// in tests that don't exercise Caddy).
func (h *Handler) reconcileCaddyDomains(ctx context.Context, projectID int64) error {
	if h.Caddy == nil {
		return nil
	}
	domains, err := h.DB.ListDomainsForProject(ctx, projectID)
	if err != nil {
		return err
	}
	return h.Caddy.SetDomainsForProject(ctx, projectID, domains)
}

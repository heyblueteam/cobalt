package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/heyblueteam/cobalt/internal/server/caddy"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ListDomains implements GET /api/projects/{name}/domains.
func (h *Handler) ListDomains(w http.ResponseWriter, r *http.Request) {
	p, ok := h.projectFromPath(w, r)
	if !ok {
		return
	}
	rows, err := h.DB.ListDomainsFullForProject(r.Context(), p.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.Domain, 0, len(rows))
	for _, d := range rows {
		entry := cobaltapi.Domain{
			Name:      d.Name,
			CreatedAt: d.CreatedAt,
			Type:      cobaltapi.DomainTypePrimary,
		}
		if d.IsRedirect() {
			entry.Type = cobaltapi.DomainTypeRedirect
			entry.RedirectTo = *d.RedirectTo
		}
		out = append(out, entry)
	}
	writeJSON(w, out)
}

// AddDomain implements POST /api/projects/{name}/domains. Reconciles
// Caddy synchronously after the DB write so the new domain is live by
// the time we return.
//
// If req.RedirectTo is set, this row is registered as a 301 redirect
// to that target. The target must already exist as a primary domain
// on the same project; we don't accept dangling-target redirects.
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
	if req.RedirectTo != "" {
		if req.RedirectTo == req.Name {
			writeError(w, http.StatusBadRequest, "redirectTo must not equal name (no self-redirects)")
			return
		}
		// The target must exist as a primary on this project — we don't
		// install dangling 301s pointing at hosts the project doesn't own.
		existing, err := h.DB.ListDomainsFullForProject(r.Context(), p.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		var targetIsPrimary bool
		for _, d := range existing {
			if d.Name == req.RedirectTo && !d.IsRedirect() {
				targetIsPrimary = true
				break
			}
		}
		if !targetIsPrimary {
			writeError(w, http.StatusBadRequest,
				"redirectTo "+req.RedirectTo+" must already exist as a primary domain on this project")
			return
		}
	}

	var err error
	if req.RedirectTo != "" {
		err = h.DB.AddDomainRedirect(r.Context(), p.ID, req.Name, req.RedirectTo)
	} else {
		err = h.DB.AddDomain(r.Context(), p.ID, req.Name)
	}
	if err != nil {
		if errors.Is(err, store.ErrDomainTaken) {
			writeError(w, http.StatusConflict, "domain "+req.Name+" already configured")
			return
		}
		h.Log.Error("api: add domain", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.reconcileCaddyForProject(r.Context(), p.ID); err != nil {
		// DB write succeeded; Caddy reconcile failed. The convergence
		// loop will retry. Surface a partial-success.
		h.Log.Warn("api: caddy reconcile after domain add failed",
			"project_id", p.ID, "error", err)
	}
	out := cobaltapi.Domain{Name: req.Name, Type: cobaltapi.DomainTypePrimary}
	if req.RedirectTo != "" {
		out.Type = cobaltapi.DomainTypeRedirect
		out.RedirectTo = req.RedirectTo
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, out)
}

// RemoveDomain implements DELETE /api/projects/{name}/domains/{domain}.
// Removing a primary that has redirects pointing at it cascades the
// redirects in the same transaction so we never leave dangling 301s.
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
	cascadedIDs, err := h.DB.RemoveDomainAndRedirects(r.Context(), p.ID, domain)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "domain not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := h.reconcileCaddyForProjectWithDropped(r.Context(), p.ID, cascadedIDs); err != nil {
		h.Log.Warn("api: caddy reconcile after domain remove failed",
			"project_id", p.ID, "error", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// reconcileCaddyForProject pushes the project's full domain state to
// Caddy in two phases: primary-host matchers (so the project's web
// service receives traffic for its real hosts), then redirect routes
// for the 301s. Tolerates a nil Caddy client (used in tests that
// don't exercise Caddy).
func (h *Handler) reconcileCaddyForProject(ctx context.Context, projectID int64) error {
	return h.reconcileCaddyForProjectWithDropped(ctx, projectID, nil)
}

// reconcileCaddyForProjectWithDropped is the cascade-aware variant.
// droppedRedirectIDs is the set of redirect-row ids that the DB has
// already forgotten about (because a primary delete cascaded them);
// they need to be passed through so SyncProjectRedirects knows to
// remove them from Caddy too. Without this, Caddy retains stale
// `cobalt-redirect-N` routes pointing at hosts the project doesn't
// own anymore.
func (h *Handler) reconcileCaddyForProjectWithDropped(ctx context.Context, projectID int64, droppedRedirectIDs []int64) error {
	if h.Caddy == nil {
		return nil
	}
	rows, err := h.DB.ListDomainsFullForProject(ctx, projectID)
	if err != nil {
		return err
	}
	primaries := make([]string, 0, len(rows))
	var redirects []caddy.RedirectSpec
	redirectIDs := append([]int64(nil), droppedRedirectIDs...)
	for _, d := range rows {
		if d.IsRedirect() {
			redirects = append(redirects, caddy.RedirectSpec{
				RowID:      d.ID,
				FromDomain: d.Name,
				ToDomain:   *d.RedirectTo,
			})
			redirectIDs = append(redirectIDs, d.ID)
		} else {
			primaries = append(primaries, d.Name)
		}
	}
	if err := h.Caddy.SetDomainsForProject(ctx, projectID, primaries); err != nil {
		return err
	}
	return h.Caddy.SyncProjectRedirects(ctx, redirectIDs, redirects)
}

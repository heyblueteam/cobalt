package api

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/heyblueteam/cobalt/internal/server/github"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// ManifestPendingTTL is how long a pending-app row is valid for. Long
// enough that a user can read the GitHub form, short enough that
// abandoned flows clean up quickly.
const ManifestPendingTTL = 30 * time.Minute

// CreatePendingApp implements POST /api/github-apps/create. Returns
// the URL the user must open in a browser to start the manifest flow.
func (h *Handler) CreatePendingApp(w http.ResponseWriter, r *http.Request) {
	var req cobaltapi.PendingAppCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	expiresAt := time.Now().Add(ManifestPendingTTL).Unix()
	id, _, err := h.DB.CreatePendingApp(r.Context(), req.Organization, expiresAt)
	if err != nil {
		h.Log.Error("manifest: create pending", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	host := h.publicHost(r)
	url := fmt.Sprintf("https://%s/github-apps/%d/create", host, id)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, cobaltapi.PendingAppCreateResponse{ID: id, URL: url, ExpiresAt: expiresAt})
}

// ManifestForm implements GET /github-apps/{id}/create. Public (no auth).
//
// Returns an HTML page that auto-submits a hidden form to GitHub's
// manifest-create endpoint with our manifest as a JSON-encoded field.
// GitHub will redirect the user back to /github-apps/{id}/created with
// the resulting code.
func (h *Handler) ManifestForm(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid pending id", http.StatusBadRequest)
		return
	}
	pending, err := h.DB.GetPendingApp(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "pending registration not found or expired", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if time.Now().Unix() > pending.ExpiresAt && pending.ExpiresAt > 0 {
		http.Error(w, "pending registration expired; restart with `cobalt github apps add`", http.StatusGone)
		return
	}

	host := h.publicHost(r)
	manifest := github.BuildManifest(host, github.NewAppName(), strconv.FormatInt(pending.ID, 10))
	manifestJSON, err := jsonMarshal(manifest)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// GitHub's endpoint accepts a POSTed form with a `manifest` field
	// (JSON) and a `state` token. For org-scoped Apps the URL is
	// /organizations/{org}/settings/apps/new; user-scoped is
	// /settings/apps/new.
	target := "https://github.com/settings/apps/new"
	if pending.Organization != "" {
		target = fmt.Sprintf("https://github.com/organizations/%s/settings/apps/new", pending.Organization)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = manifestFormTpl.Execute(w, struct {
		Target   string
		Manifest string
		State    string
	}{Target: target, Manifest: string(manifestJSON), State: pending.State})
}

// ManifestCreated implements GET /github-apps/{id}/created. Public (no
// auth). GitHub redirects the user here after they confirm app
// creation; we exchange the code for credentials and persist the app.
func (h *Handler) ManifestCreated(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid pending id", http.StatusBadRequest)
		return
	}
	pending, err := h.DB.GetPendingApp(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "pending registration not found or expired", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}
	if subtle.ConstantTimeCompare([]byte(state), []byte(pending.State)) != 1 {
		http.Error(w, "state mismatch", http.StatusUnauthorized)
		return
	}

	converted, err := h.GitHub.ConvertManifestCode(r.Context(), code)
	if err != nil {
		h.Log.Error("manifest: convert code", "error", err)
		http.Error(w, "GitHub rejected the manifest code", http.StatusBadGateway)
		return
	}

	_, err = h.DB.CreateGithubApp(r.Context(), store.GithubApp{
		AppID:         converted.ID,
		Slug:          sql.NullString{String: converted.Slug, Valid: converted.Slug != ""},
		Owner:         converted.Owner.Login,
		PrivateKey:    converted.PEM,
		WebhookSecret: converted.WebhookSecret,
		ClientID:      sql.NullString{String: converted.ClientID, Valid: converted.ClientID != ""},
		ClientSecret:  sql.NullString{String: converted.ClientSecret, Valid: converted.ClientSecret != ""},
		Name:          sql.NullString{String: converted.Name, Valid: converted.Name != ""},
		HTMLURL:       sql.NullString{String: converted.HTMLURL, Valid: converted.HTMLURL != ""},
	})
	if err != nil {
		h.Log.Error("manifest: persist app", "error", err)
		http.Error(w, "could not persist app credentials", http.StatusInternalServerError)
		return
	}
	if err := h.DB.DeletePendingApp(r.Context(), pending.ID); err != nil {
		h.Log.Warn("manifest: delete pending row", "error", err)
	}

	// Redirect the browser to GitHub's install page so the user can
	// grant the new App access to their repos.
	installURL := github.InstallationsURL(converted.HTMLURL, converted.Owner.ID, converted.Owner.Type)
	http.Redirect(w, r, installURL, http.StatusFound)
}

// publicHost returns the daemon's public hostname for use in manifest
// URLs. Prefers the configured Host (set via Config.PublicHost), falls
// back to the request's Host header.
func (h *Handler) publicHost(r *http.Request) string {
	if h.PublicHost != "" {
		return h.PublicHost
	}
	return r.Host
}

// manifestFormTpl renders the auto-submitting POST form GitHub expects.
var manifestFormTpl = template.Must(template.New("manifest").Parse(`<!DOCTYPE html>
<html><head><title>Creating cobalt GitHub App…</title></head>
<body>
<form id="f" method="post" action="{{.Target}}">
  <input type="hidden" name="manifest" value='{{.Manifest}}'>
  <input type="hidden" name="state" value="{{.State}}">
  <noscript><button type="submit">Continue to GitHub</button></noscript>
</form>
<script>document.getElementById('f').submit();</script>
</body></html>`))

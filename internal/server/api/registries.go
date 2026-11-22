package api

import (
	"errors"
	"net/http"

	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi/validator"
)

// ListRegistries implements GET /api/registries. Passwords are never
// returned.
func (h *Handler) ListRegistries(w http.ResponseWriter, r *http.Request) {
	creds, err := h.DB.ListRegistryCredentials(r.Context())
	if err != nil {
		h.Log.Error("api: list registries", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.RegistryCredential, 0, len(creds))
	for _, c := range creds {
		out = append(out, cobaltapi.RegistryCredential{Registry: c.Registry, Username: c.Username, UpdatedAt: c.UpdatedAt})
	}
	writeJSON(w, out)
}

// SetRegistry implements POST /api/registries/{registry}: the daemon-wide
// login used to pull prebuilt images from that registry host.
func (h *Handler) SetRegistry(w http.ResponseWriter, r *http.Request) {
	registry := r.PathValue("registry")
	if err := validator.ValidateRegistryHost(registry); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	var req cobaltapi.RegistryCredentialSetRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if err := h.DB.SetRegistryCredential(r.Context(), registry, req.Username, req.Password); err != nil {
		h.Log.Error("api: set registry", "registry", registry, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	cred, err := h.DB.GetRegistryCredential(r.Context(), registry)
	if err != nil {
		h.Log.Error("api: refetch registry", "registry", registry, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, cobaltapi.RegistryCredential{Registry: cred.Registry, Username: cred.Username, UpdatedAt: cred.UpdatedAt})
}

// DeleteRegistry implements DELETE /api/registries/{registry}.
func (h *Handler) DeleteRegistry(w http.ResponseWriter, r *http.Request) {
	registry := r.PathValue("registry")
	err := h.DB.DeleteRegistryCredential(r.Context(), registry)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "registry not found")
		return
	}
	if err != nil {
		h.Log.Error("api: delete registry", "registry", registry, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/heyblueteam/cobalt/internal/server/middleware"
	"github.com/heyblueteam/cobalt/internal/server/store"
	"github.com/heyblueteam/cobalt/pkg/cobaltapi"
)

// apiKeyByteLen is the random-byte size of a fresh API key. 32 bytes
// = 64 hex chars. Plenty of entropy; comfortably forgeable-resistant.
const apiKeyByteLen = 32

// ListAPIKeys implements GET /api/apikeys.
func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.DB.ListAPIKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]cobaltapi.APIKey, 0, len(keys))
	for _, k := range keys {
		out = append(out, apiKeyToAPI(k))
	}
	writeJSON(w, out)
}

// CreateAPIKey implements POST /api/apikeys. Returns the raw key
// once — clients must persist it themselves. The daemon only stores
// the SHA-256 hash.
func (h *Handler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var req cobaltapi.APIKeyCreateRequest
	if err := readJSON(w, r, &req); err != nil {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	raw, err := generateAPIKey()
	if err != nil {
		h.Log.Error("apikeys: generate", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	hash := middleware.HashAPIKey(raw)
	id, err := h.DB.CreateAPIKey(r.Context(), hash, req.Name)
	if err != nil {
		h.Log.Error("apikeys: create", "error", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	row, err := h.DB.GetAPIKeyByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, cobaltapi.APIKeyCreateResponse{
		ID:        row.ID,
		Name:      row.Name,
		Key:       raw,
		CreatedAt: row.CreatedAt,
	})
}

// DeleteAPIKey implements DELETE /api/apikeys/{id}.
func (h *Handler) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid api key id")
		return
	}
	if err := h.DB.DeleteAPIKey(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "api key not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// generateAPIKey returns a fresh random hex string from the system
// CSPRNG. Errors only on platform-level RNG failure.
func generateAPIKey() (string, error) {
	b := make([]byte, apiKeyByteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func apiKeyToAPI(k store.APIKey) cobaltapi.APIKey {
	out := cobaltapi.APIKey{
		ID:        k.ID,
		Name:      k.Name,
		CreatedAt: k.CreatedAt,
	}
	if k.LastUsedAt.Valid {
		out.LastUsedAt = k.LastUsedAt.Int64
	}
	return out
}

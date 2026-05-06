// Package middleware holds the daemon's HTTP middleware: request id,
// structured logging, panic recovery, and bearer-token auth.
package middleware

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyAPIKeyID
)

// RequestID assigns or echoes a request ID, available via RequestID(ctx).
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyRequestID, id)))
	})
}

// Logger logs each request once it completes, with method, path, status,
// duration, and request ID.
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.LogAttrs(r.Context(), slog.LevelInfo, "request",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Duration("duration", time.Since(start)),
				slog.String("request_id", RequestIDFrom(r.Context())),
			)
		})
	}
}

// Recover converts panics from downstream handlers into a logged 500.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rv := recover(); rv != nil {
					log.LogAttrs(r.Context(), slog.LevelError, "panic",
						slog.Any("recovered", rv),
						slog.String("stack", string(debug.Stack())),
						slog.String("request_id", RequestIDFrom(r.Context())),
					)
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// BearerAuth requires Authorization: Bearer <apiKey>. The raw key is hashed
// with SHA-256 and compared in constant time against apikeys.key_hash.
//
// On success, the matched apikeys.id is stored in the request context and
// last_used_at is updated best-effort (errors logged but not returned).
func BearerAuth(db *sql.DB, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, err := extractBearer(r)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			id, ok := lookupAPIKey(r.Context(), db, raw)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			go func(id int64) {
				_, err := db.Exec(`UPDATE apikeys SET last_used_at = unixepoch() WHERE id = ?`, id)
				if err != nil {
					log.Warn("update last_used_at", "id", id, "error", err)
				}
			}(id)
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKeyAPIKeyID, id)))
		})
	}
}

func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", errors.New("missing bearer")
	}
	tok := strings.TrimSpace(h[len(prefix):])
	if tok == "" {
		return "", errors.New("empty bearer")
	}
	return tok, nil
}

func lookupAPIKey(ctx context.Context, db *sql.DB, raw string) (int64, bool) {
	want := hashKey(raw)
	rows, err := db.QueryContext(ctx, `SELECT id, key_hash FROM apikeys`)
	if err != nil {
		return 0, false
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var got string
		if err := rows.Scan(&id, &got); err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			return id, true
		}
	}
	return 0, false
}

// HashAPIKey returns the canonical storage form of a raw API key.
// Exposed so apikey-creation code can store keys consistently.
func HashAPIKey(raw string) string { return hashKey(raw) }

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// RequestIDFrom returns the request ID stored in ctx by RequestID, or "".
func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

// APIKeyIDFrom returns the apikeys.id stored in ctx by BearerAuth, or 0.
func APIKeyIDFrom(ctx context.Context) int64 {
	v, _ := ctx.Value(ctxKeyAPIKeyID).(int64)
	return v
}

type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(b)
}

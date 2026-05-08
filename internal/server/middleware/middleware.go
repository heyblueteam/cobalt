package middleware

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"
	rqlitehttp "github.com/rqlite/rqlite-go-http"
)

type ctxKey int

const (
	ctxKeyRequestID ctxKey = iota
	ctxKeyAPIKeyID
)

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

func BearerAuth(db *rqlitehttp.Client, log *slog.Logger) func(http.Handler) http.Handler {
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
			// Update last_used_at fire-and-forget. Detach from the request
			// context (which is canceled the moment the response flushes) so
			// the UPDATE actually completes; cap with a short deadline so a
			// slow DB doesn't pile up goroutines.
			go func(id int64) {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
				defer cancel()
				_, err := db.ExecuteSingle(ctx, `UPDATE apikeys SET last_used_at = strftime('%s', 'now') WHERE id = ?`, id)
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

func lookupAPIKey(ctx context.Context, db *rqlitehttp.Client, raw string) (int64, bool) {
	want := hashKey(raw)
	resp, err := db.QuerySingle(ctx, `SELECT id, key_hash FROM apikeys`)
	if err != nil {
		return 0, false
	}
	results := resp.GetQueryResults()
	if len(results) == 0 {
		return 0, false
	}
	for _, row := range results[0].Values {
		id := toInt64(row[0])
		got := toString(row[1])
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1 {
			return id, true
		}
	}
	return 0, false
}

func HashAPIKey(raw string) string { return hashKey(raw) }

func hashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRequestID).(string)
	return v
}

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

// Flush forwards to the underlying http.ResponseWriter when it supports
// http.Flusher. Required because SSE handlers type-assert their writer for
// Flusher; without this, the Logger middleware's statusWriter masks it and
// every streaming endpoint returns 500 "streaming unsupported".
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func toInt64(v any) int64 {
	if v == nil {
		return 0
	}
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		i, _ := x.Int64()
		return i
	}
	return 0
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

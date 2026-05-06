package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// sseWriter wraps http.ResponseWriter with the SSE framing helpers and a
// Flusher accessor. Construct via newSSE; the writer's lifetime is the
// HTTP handler call.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// newSSE writes the canonical SSE response headers and returns a writer
// the caller can use to push events. Returns an error if the
// http.ResponseWriter doesn't support flushing — every reasonable
// production stack does, but tests using a vanilla recorder won't.
func newSSE(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("response writer does not support streaming")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Tell reverse proxies (nginx, Caddy with default buffer settings)
	// not to buffer the response. Caddy honors this header.
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()
	return &sseWriter{w: w, flusher: flusher}, nil
}

// data emits a `data:` event with the supplied payload. Newlines in
// data are split per the SSE spec — each line gets its own `data:`
// prefix. id is optional; when non-empty it sets the event's id field
// so clients can resume via Last-Event-ID.
func (s *sseWriter) data(id, payload string) error {
	if id != "" {
		if _, err := fmt.Fprintf(s.w, "id: %s\n", id); err != nil {
			return err
		}
	}
	for _, line := range strings.Split(payload, "\n") {
		if _, err := fmt.Fprintf(s.w, "data: %s\n", line); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(s.w, "\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// heartbeat emits an SSE comment line so reverse proxies don't time
// out the connection. Comments start with `:` and clients ignore them.
func (s *sseWriter) heartbeat() error {
	if _, err := io.WriteString(s.w, ": keepalive\n\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// heartbeatTicker fires a heartbeat every interval until ctx is done
// or the writer fails. Run in a goroutine alongside the data loop.
func (s *sseWriter) heartbeatLoop(stop <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if err := s.heartbeat(); err != nil {
				return
			}
		}
	}
}

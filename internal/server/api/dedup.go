package api

import (
	"sync"
	"time"
)

// webhookDedup tracks recently-seen X-GitHub-Delivery headers so re-
// delivered webhooks (network retries, manual retries from the GitHub
// admin UI) don't enqueue a second deployment.
//
// In-memory by design — lost on daemon restart. GitHub retries are
// almost always within seconds, so a 10-minute window covers the
// common case. If we ever cared about cross-restart dedup, we'd add a
// row table — but the cost of a duplicate webhook is just one extra
// deploy that the dispatcher's "newer supersedes older" logic
// downgrades to skipped anyway.
type webhookDedup struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
	now  func() time.Time
}

func newWebhookDedup(ttl time.Duration) *webhookDedup {
	return &webhookDedup{
		seen: map[string]time.Time{},
		ttl:  ttl,
		now:  time.Now,
	}
}

// Seen returns true if the deliveryID has already been recorded within
// ttl; otherwise records it and returns false. Empty deliveryID is
// always treated as "not seen" (caller already had a chance to bail).
//
// Cleans up expired entries inline rather than in a goroutine — at
// Blue's webhook volume (≤ a few per minute) the linear sweep is
// imperceptible.
func (d *webhookDedup) Seen(deliveryID string) bool {
	if deliveryID == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.now()
	cutoff := now.Add(-d.ttl)
	for k, t := range d.seen {
		if t.Before(cutoff) {
			delete(d.seen, k)
		}
	}
	if _, ok := d.seen[deliveryID]; ok {
		return true
	}
	d.seen[deliveryID] = now
	return false
}

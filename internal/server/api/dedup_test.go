package api

import (
	"testing"
	"time"
)

func TestWebhookDedup_FirstSeenIsFalse(t *testing.T) {
	t.Parallel()
	d := newWebhookDedup(time.Minute)
	if d.Seen("delivery-1") {
		t.Error("first call: want false")
	}
}

func TestWebhookDedup_SecondSeenIsTrue(t *testing.T) {
	t.Parallel()
	d := newWebhookDedup(time.Minute)
	_ = d.Seen("delivery-1")
	if !d.Seen("delivery-1") {
		t.Error("repeat call: want true")
	}
}

func TestWebhookDedup_DistinctIDsIndependent(t *testing.T) {
	t.Parallel()
	d := newWebhookDedup(time.Minute)
	if d.Seen("a") || d.Seen("b") {
		t.Error("first calls: want false")
	}
	if !d.Seen("a") || !d.Seen("b") {
		t.Error("repeat calls: want true")
	}
}

func TestWebhookDedup_EmptyDeliveryIsNeverSeen(t *testing.T) {
	t.Parallel()
	d := newWebhookDedup(time.Minute)
	if d.Seen("") {
		t.Error("empty id: want false")
	}
	if d.Seen("") {
		t.Error("empty id repeat: still want false")
	}
}

func TestWebhookDedup_TTLExpires(t *testing.T) {
	t.Parallel()
	d := newWebhookDedup(time.Hour)
	now := time.Unix(1_000_000_000, 0)
	d.now = func() time.Time { return now }
	_ = d.Seen("delivery-1")
	// Advance past TTL.
	now = now.Add(2 * time.Hour)
	if d.Seen("delivery-1") {
		t.Error("after TTL: want false (entry should have been swept)")
	}
}

package services

import (
	"time"
)

// Webhook event notifications. Events are queued per destination and
// delivered by small per-destination worker lanes with bounded retries (see
// webhook_dispatch.go); delivery is best-effort and never blocks the request
// path. Destinations are SSRF-guarded (webhook_guard.go). Payloads are signed
// with HMAC-SHA256 over the raw body (X-Bkt-Signature: sha256=<hex>) when the
// bucket has a webhook secret.

const (
	EventObjectCreated = "object:created"
	EventObjectRemoved = "object:removed"
)

// ObjectEvent is the JSON payload delivered to webhooks.
type ObjectEvent struct {
	Event     string    `json:"event"`
	Bucket    string    `json:"bucket"`
	Key       string    `json:"key"`
	Size      int64     `json:"size,omitempty"`
	ETag      string    `json:"etag,omitempty"`
	VersionID string    `json:"version_id,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// EnqueueWebhook queues an event for delivery. secret is the stored form
// (encrypted or legacy plaintext). Events over the per-bucket/per-destination
// limits are dropped with a (summarized) warning rather than blocking uploads.
func EnqueueWebhook(url, secret string, ev ObjectEvent) {
	if url == "" {
		return
	}
	ev.Timestamp = time.Now().UTC()
	webhookDispatcherInstance().enqueue(queuedEvent{url: url, secret: secret, event: ev})
}

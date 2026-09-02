package services

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"bkt/internal/logger"
)

// Webhook event notifications. Events are queued and delivered by a single
// background worker with bounded retries; delivery is best-effort and never
// blocks the request path. Payloads are signed with HMAC-SHA256 over the raw
// body (X-Bkt-Signature: sha256=<hex>) when the bucket has a webhook secret.

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

type queuedEvent struct {
	url    string
	secret string
	event  ObjectEvent
}

var (
	notifyQueue  = make(chan queuedEvent, 1024)
	notifyClient = &http.Client{Timeout: 10 * time.Second}
)

func init() {
	go notifyWorker()
}

// EnqueueWebhook queues an event for delivery. A full queue drops the event
// with a warning rather than blocking uploads.
func EnqueueWebhook(url, secret string, ev ObjectEvent) {
	if url == "" {
		return
	}
	ev.Timestamp = time.Now().UTC()
	select {
	case notifyQueue <- queuedEvent{url: url, secret: secret, event: ev}:
	default:
		logger.Warn("Webhook queue full — dropping event", map[string]interface{}{
			"bucket": ev.Bucket, "key": ev.Key, "event": ev.Event,
		})
	}
}

func notifyWorker() {
	for q := range notifyQueue {
		deliver(q)
	}
}

func deliver(q queuedEvent) {
	body, err := json.Marshal(q.event)
	if err != nil {
		return
	}
	for attempt := 1; attempt <= 3; attempt++ {
		req, err := http.NewRequest(http.MethodPost, q.url, bytes.NewReader(body))
		if err != nil {
			logger.Warn("Webhook: bad URL", map[string]interface{}{"url": q.url, "error": err.Error()})
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "bkt-webhook/1.0")
		if q.secret != "" {
			mac := hmac.New(sha256.New, []byte(q.secret))
			mac.Write(body)
			req.Header.Set("X-Bkt-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		resp, err := notifyClient.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
		}
		if attempt < 3 {
			time.Sleep(time.Duration(attempt*attempt) * time.Second)
		}
	}
	logger.Warn("Webhook delivery failed after retries", map[string]interface{}{
		"url": q.url, "bucket": q.event.Bucket, "key": q.event.Key, "event": q.event.Event,
	})
}

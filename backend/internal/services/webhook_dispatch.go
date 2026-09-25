package services

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"bkt/internal/logger"
)

// Webhook delivery scheduling.
//
// Isolation: events are queued per destination (scheme://host:port) and each
// destination has its own small worker lane (webhookWorkersPerHost), so a
// slow or tarpitting receiver only delays its own events — never another
// tenant's. Each delivery (all retries included) is bounded by
// webhookDeliveryDeadline.
//
// Fairness: every bucket has a pending-event cap and a token-bucket rate
// limit, so one bucket (e.g. a MoveFolder of 20k objects) cannot fill a
// shared queue. Events over a limit are dropped and counted; drops are
// logged once when they start and then summarized periodically.

const (
	webhookWorkersPerHost   = 2
	webhookHostQueue        = 1024
	webhookMaxHosts         = 256
	webhookMaxPending       = 8192
	webhookBucketMaxPending = 512
	webhookBucketRate       = 50.0   // events/second sustained, per bucket
	webhookBucketBurst      = 1000.0 // token-bucket capacity, per bucket
	webhookAttempts         = 3
	webhookAttemptTimeout   = 8 * time.Second
	webhookDeliveryDeadline = 20 * time.Second
	webhookLaneIdle         = 60 * time.Second
	webhookDropReportEvery  = time.Minute
	webhookMaxBodyDrain     = 64 << 10
)

type queuedEvent struct {
	url    string
	secret string // as stored (possibly encrypted); resolved at delivery
	event  ObjectEvent
}

type webhookLane struct {
	ch      chan queuedEvent
	workers int
}

type webhookBucketState struct {
	pending int
	tokens  float64
	last    time.Time
}

type webhookDispatcher struct {
	mu      sync.Mutex
	client  *http.Client
	lanes   map[string]*webhookLane
	buckets map[string]*webhookBucketState
	pending int
	dropped map[string]int // bucket -> events dropped since the last report

	now      func() time.Time
	deadline time.Duration
	backoff  func(attempt int) time.Duration
	idle     time.Duration
	deliverF func(q queuedEvent) // test hook; nil = d.deliver
	// preflight, when set (WEBHOOK_PROXY_URL mode), vets the target before
	// every attempt; an errWebhookBlocked result refuses the delivery.
	preflight func(ctx context.Context, rawURL string) error
}

func newWebhookDispatcher(client *http.Client) *webhookDispatcher {
	return &webhookDispatcher{
		client:   client,
		lanes:    map[string]*webhookLane{},
		buckets:  map[string]*webhookBucketState{},
		dropped:  map[string]int{},
		now:      time.Now,
		deadline: webhookDeliveryDeadline,
		backoff:  func(attempt int) time.Duration { return time.Duration(attempt*attempt) * time.Second },
		idle:     webhookLaneIdle,
	}
}

var (
	defaultWebhookDispatcher     *webhookDispatcher
	defaultWebhookDispatcherOnce sync.Once
)

func webhookDispatcherInstance() *webhookDispatcher {
	defaultWebhookDispatcherOnce.Do(func() {
		proxy, perr := webhookProxyFromEnv()
		d := newWebhookDispatcher(newWebhookClientVia(currentWebhookAllowlist, proxy))
		switch {
		case perr != nil:
			logger.Error("WEBHOOK_PROXY_URL is invalid — webhook deliveries are disabled until it is fixed or unset", map[string]interface{}{"error": perr.Error()})
			d.preflight = func(context.Context, string) error { return errWebhookProxyInvalid }
		case proxy != nil:
			logger.Info("Webhooks: delivering via WEBHOOK_PROXY_URL", map[string]interface{}{"proxy": redactWebhookURL(proxy.String())})
			d.preflight = webhookTargetPreflight(currentWebhookAllowlist, net.DefaultResolver.LookupIPAddr)
		}
		defaultWebhookDispatcher = d
		go defaultWebhookDispatcher.reportLoop()
	})
	return defaultWebhookDispatcher
}

// laneKey identifies a destination: scheme://host:port, lower-cased.
func laneKey(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		port = "80"
		if scheme == "https" {
			port = "443"
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port, true
}

// enqueue schedules q, or drops it (counted) when a limit is hit. It never
// blocks on delivery.
func (d *webhookDispatcher) enqueue(q queuedEvent) bool {
	key, ok := laneKey(q.url)
	if !ok {
		logger.Warn("Webhook: invalid URL — dropping event", map[string]interface{}{"bucket": q.event.Bucket})
		return false
	}
	bucket := q.event.Bucket

	d.mu.Lock()
	bs := d.buckets[bucket]
	now := d.now()
	if bs == nil {
		bs = &webhookBucketState{tokens: webhookBucketBurst, last: now}
		d.buckets[bucket] = bs
	}
	bs.refill(now)

	reason := ""
	switch {
	case bs.pending >= webhookBucketMaxPending:
		reason = "bucket backlog limit"
	case bs.tokens < 1:
		reason = "bucket rate limit"
	case d.pending >= webhookMaxPending:
		reason = "global queue full"
	}
	lane := d.lanes[key]
	if reason == "" && lane == nil {
		if len(d.lanes) >= webhookMaxHosts {
			reason = "too many webhook destinations"
		} else {
			lane = &webhookLane{ch: make(chan queuedEvent, webhookHostQueue)}
			d.lanes[key] = lane
		}
	}
	if reason == "" {
		select {
		case lane.ch <- q:
		default:
			reason = "destination queue full"
		}
	}
	if reason != "" {
		first := d.dropped[bucket] == 0
		d.dropped[bucket]++
		d.mu.Unlock()
		if first {
			logger.Warn("Webhook: dropping events (further drops for this bucket are summarized)", map[string]interface{}{
				"bucket": bucket, "reason": reason, "destination": redactWebhookURL(q.url),
			})
		}
		return false
	}
	bs.tokens--
	bs.pending++
	d.pending++
	if lane.workers < webhookWorkersPerHost {
		lane.workers++
		go d.laneWorker(key, lane)
	}
	d.mu.Unlock()
	return true
}

func (bs *webhookBucketState) refill(now time.Time) {
	if el := now.Sub(bs.last).Seconds(); el > 0 {
		bs.tokens += el * webhookBucketRate
		if bs.tokens > webhookBucketBurst {
			bs.tokens = webhookBucketBurst
		}
	}
	bs.last = now
}

// laneWorker delivers one destination's events until the lane is idle.
func (d *webhookDispatcher) laneWorker(key string, lane *webhookLane) {
	idle := time.NewTimer(d.idle)
	defer idle.Stop()
	for {
		select {
		case q := <-lane.ch:
			if d.deliverF != nil {
				d.deliverF(q)
			} else {
				d.deliver(q)
			}
			d.finished(q)
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(d.idle)
		case <-idle.C:
			d.mu.Lock()
			if len(lane.ch) == 0 {
				lane.workers--
				if lane.workers == 0 && d.lanes[key] == lane {
					delete(d.lanes, key)
				}
				d.mu.Unlock()
				return
			}
			d.mu.Unlock()
			idle.Reset(d.idle)
		}
	}
}

func (d *webhookDispatcher) finished(q queuedEvent) {
	d.mu.Lock()
	d.pending--
	if bs := d.buckets[q.event.Bucket]; bs != nil {
		bs.pending--
	}
	d.mu.Unlock()
}

// reportLoop periodically summarizes dropped events and prunes idle bucket
// state.
func (d *webhookDispatcher) reportLoop() {
	t := time.NewTicker(webhookDropReportEvery)
	defer t.Stop()
	for range t.C {
		d.reportAndPrune()
	}
}

func (d *webhookDispatcher) reportAndPrune() {
	d.mu.Lock()
	dropped := d.dropped
	d.dropped = map[string]int{}
	now := d.now()
	for name, bs := range d.buckets {
		bs.refill(now)
		if bs.pending == 0 && bs.tokens >= webhookBucketBurst {
			delete(d.buckets, name)
		}
	}
	d.mu.Unlock()
	for bucket, n := range dropped {
		logger.Warn("Webhook: events dropped in the last interval", map[string]interface{}{"bucket": bucket, "dropped": n})
	}
}

// deliver POSTs one event with bounded retries, all within d.deadline.
// Response bodies are drained (bounded) and closed so connections are
// reused. A blocked destination or a non-retryable 4xx is not retried.
func (d *webhookDispatcher) deliver(q queuedEvent) {
	dest := redactWebhookURL(q.url)
	secret, err := webhookSecretPlain(q.secret)
	if err != nil {
		logger.Warn("Webhook: cannot decrypt webhook secret — event not delivered", map[string]interface{}{"bucket": q.event.Bucket, "destination": dest})
		return
	}
	body, err := json.Marshal(q.event)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.deadline)
	defer cancel()

	lastErr := ""
	for attempt := 1; attempt <= webhookAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-time.After(d.backoff(attempt - 1)):
			case <-ctx.Done():
				attempt = webhookAttempts + 1
				continue
			}
		}
		status, err := d.attempt(ctx, q.url, secret, body)
		if err == nil && status >= 200 && status < 300 {
			return
		}
		if err != nil {
			if errors.Is(err, errWebhookBlocked) {
				logger.Warn("Webhook: destination refused (private/reserved address) — see WEBHOOK_ALLOWED_HOSTS", map[string]interface{}{
					"bucket": q.event.Bucket, "destination": dest,
				})
				return
			}
			if errors.Is(err, errWebhookProxyInvalid) {
				logger.Warn("Webhook: not delivered — WEBHOOK_PROXY_URL is invalid", map[string]interface{}{
					"bucket": q.event.Bucket, "destination": dest,
				})
				return
			}
			lastErr = "request failed"
			if errors.Is(err, context.DeadlineExceeded) {
				lastErr = "timeout"
			}
		} else {
			lastErr = http.StatusText(status)
			if status >= 400 && status < 500 && status != http.StatusRequestTimeout && status != http.StatusTooManyRequests {
				break // permanent client error
			}
			if status >= 300 && status < 400 {
				lastErr = "redirect not followed"
				break
			}
		}
	}
	logger.Warn("Webhook delivery failed", map[string]interface{}{
		"destination": dest, "bucket": q.event.Bucket, "event": q.event.Event, "error": lastErr,
	})
}

func (d *webhookDispatcher) attempt(ctx context.Context, rawURL, secret string, body []byte) (int, error) {
	if d.preflight != nil {
		if err := d.preflight(ctx, rawURL); err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bkt-webhook/1.0")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Bkt-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, err
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, webhookMaxBodyDrain))
	_ = resp.Body.Close()
	return resp.StatusCode, nil
}

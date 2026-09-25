package services

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsBlockedWebhookIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "127.8.9.10", "10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"169.254.169.254", "169.254.0.1", "100.64.0.1", "100.127.255.254", "0.0.0.0", "0.1.2.3",
		"224.0.0.1", "255.255.255.255", "240.0.0.1", "198.18.0.1",
		"::1", "::", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1", "fec0::1",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.0.0.1", "::127.0.0.1",
		"64:ff9b::a9fe:a9fe", "2002:a9fe:a9fe::1", "2001:db8::1",
	}
	for _, s := range blocked {
		if !isBlockedWebhookIP(net.ParseIP(s)) {
			t.Errorf("%s must be blocked", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "172.32.0.1", "100.128.0.1", "2606:4700:4700::1111", "::ffff:8.8.8.8"}
	for _, s := range allowed {
		if isBlockedWebhookIP(net.ParseIP(s)) {
			t.Errorf("%s must be allowed", s)
		}
	}
	if !isBlockedWebhookIP(nil) {
		t.Error("nil IP must be blocked")
	}
}

func TestValidateWebhookURL(t *testing.T) {
	setWebhookAllowlist("")
	defer setWebhookAllowlist("")
	bad := []string{
		"ftp://example.com/x", "file:///etc/passwd", "gopher://example.com", "example.com/hook",
		"http://", "http://user:pass@example.com/hook", "https://token@example.com/",
		"http://127.0.0.1/", "http://127.0.0.1:8080/x", "http://[::1]/", "http://169.254.169.254/latest/meta-data",
		"http://10.1.2.3/", "http://192.168.0.10/", "http://100.64.0.1/", "http://[::ffff:127.0.0.1]/",
		"http://[fe80::1%25eth0]/", "http://0.0.0.0/", "http://localhost/", "http://LOCALHOST./",
		"http://foo.localhost/", "http://127.1/", "http://2130706433/", "http://0x7f.1/",
		"mailto:a@b.c", "http://" + strings.Repeat("a", 2100) + ".com/",
	}
	for _, u := range bad {
		if ValidateWebhookURL(u) == nil {
			t.Errorf("%q must be rejected", u)
		}
	}
	good := []string{"https://hooks.example.com/abc?token=x", "http://example.com:8080/h", "https://8.8.8.8/x", "HTTPS://Example.COM/"}
	for _, u := range good {
		if err := ValidateWebhookURL(u); err != nil {
			t.Errorf("%q must be accepted: %v", u, err)
		}
	}

	setWebhookAllowlist("hooks.internal, 10.0.0.0/8, 127.0.0.1, localhost")
	for _, u := range []string{"http://hooks.internal/x", "http://10.2.3.4:9000/", "http://127.0.0.1/", "http://localhost:8080/"} {
		if err := ValidateWebhookURL(u); err != nil {
			t.Errorf("allowlisted %q must be accepted: %v", u, err)
		}
	}
	if ValidateWebhookURL("http://192.168.1.1/") == nil {
		t.Error("non-allowlisted private IP must still be rejected")
	}
	if ValidateWebhookURL("http://user@hooks.internal/") == nil {
		t.Error("credentials must be rejected even for allowlisted hosts")
	}
}

func TestParseWebhookAllowlist(t *testing.T) {
	al, invalid := parseWebhookAllowlist(" Hooks.Internal. ,10.0.0.0/8,::1, 192.168.1.5 ,bad/cidr,a b")
	if len(invalid) != 2 {
		t.Errorf("invalid entries: %v", invalid)
	}
	if !al.allowsHost("hooks.internal") || !al.allowsHost("HOOKS.INTERNAL.") || al.allowsHost("other") {
		t.Error("hostname matching")
	}
	for _, s := range []string{"10.9.9.9", "::1", "192.168.1.5", "::ffff:10.1.1.1"} {
		if !al.allowsIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowlisted", s)
		}
	}
	if al.allowsIP(net.ParseIP("192.168.1.6")) {
		t.Error("192.168.1.6 must not be allowlisted")
	}
}

// The dial guard refuses loopback (httptest listens on 127.0.0.1) unless
// allowlisted, including via a hostname that resolves to loopback.
func TestWebhookDialGuard(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	var al atomic.Pointer[webhookAllowlist]
	set := func(s string) { a, _ := parseWebhookAllowlist(s); al.Store(a) }
	client := newWebhookClient(func() *webhookAllowlist { return al.Load() })

	set("")
	for _, u := range []string{srv.URL, "http://localhost:" + port + "/"} {
		_, err := client.Post(u, "application/json", strings.NewReader("{}"))
		if err == nil || !errors.Is(err, errWebhookBlocked) {
			t.Errorf("%s: want errWebhookBlocked, got %v", u, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatal("blocked destination must not be reached")
	}

	set("127.0.0.1/32")
	resp, err := client.Post(srv.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("allowlisted IP: %v", err)
	}
	resp.Body.Close()

	set("localhost")
	resp, err = client.Post("http://localhost:"+port+"/", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("allowlisted hostname: %v", err)
	}
	resp.Body.Close()
	if hits.Load() != 2 {
		t.Errorf("hits=%d want 2", hits.Load())
	}
}

func TestWebhookClientDoesNotFollowRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetHits.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/latest/meta-data", http.StatusFound)
	}))
	defer redirector.Close()

	allow, _ := parseWebhookAllowlist("127.0.0.1")
	client := newWebhookClient(func() *webhookAllowlist { return allow })
	resp, err := client.Post(redirector.URL, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound || targetHits.Load() != 0 {
		t.Errorf("redirect must not be followed: status=%d targetHits=%d", resp.StatusCode, targetHits.Load())
	}
}

func TestRedactWebhookURL(t *testing.T) {
	if got := redactWebhookURL("https://hooks.slack.com/services/T0/B0/secret?x=1"); got != "https://hooks.slack.com" {
		t.Errorf("got %q", got)
	}
	if got := redactWebhookURL("http://user:pw@h:8080/p"); got != "http://h:8080" {
		t.Errorf("got %q", got)
	}
	if RedactWebhookURL("") != "" {
		t.Error("empty stays empty")
	}
}

func TestWebhookSecretSealing(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key-for-webhook-secrets-0123456789")
	sealed, err := SealWebhookSecret("s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, webhookSecretEncPrefix) || strings.Contains(sealed, "s3cr3t") {
		t.Fatalf("secret not sealed: %q", sealed)
	}
	if p, err := webhookSecretPlain(sealed); err != nil || p != "s3cr3t" {
		t.Errorf("round trip: %q %v", p, err)
	}
	if p, err := webhookSecretPlain("legacy-plain"); err != nil || p != "legacy-plain" {
		t.Errorf("legacy plaintext must be read as-is: %q %v", p, err)
	}
	if s, _ := SealWebhookSecret(""); s != "" {
		t.Error("empty secret stays empty")
	}
}

func testDispatcher(client *http.Client) *webhookDispatcher {
	d := newWebhookDispatcher(client)
	d.backoff = func(int) time.Duration { return 0 }
	d.idle = 50 * time.Millisecond
	return d
}

func TestWebhookDeliverSignsRetriesAndStops(t *testing.T) {
	var calls atomic.Int32
	var sig atomic.Value
	status := atomic.Int32{}
	status.Store(http.StatusInternalServerError)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		sig.Store(r.Header.Get("X-Bkt-Signature"))
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(strings.Repeat("x", 1024)))
	}))
	defer srv.Close()
	allow, _ := parseWebhookAllowlist("127.0.0.1")
	d := testDispatcher(newWebhookClient(func() *webhookAllowlist { return allow }))

	q := queuedEvent{url: srv.URL, secret: "k", event: ObjectEvent{Event: EventObjectCreated, Bucket: "b", Key: "x"}}
	d.deliver(q)
	if calls.Load() != webhookAttempts {
		t.Errorf("5xx: calls=%d want %d", calls.Load(), webhookAttempts)
	}
	if s, _ := sig.Load().(string); !strings.HasPrefix(s, "sha256=") {
		t.Errorf("missing signature: %q", s)
	}

	calls.Store(0)
	status.Store(http.StatusNotFound)
	d.deliver(q)
	if calls.Load() != 1 {
		t.Errorf("permanent 4xx must not be retried: calls=%d", calls.Load())
	}

	calls.Store(0)
	status.Store(http.StatusOK)
	d.deliver(q)
	if calls.Load() != 1 {
		t.Errorf("2xx: calls=%d", calls.Load())
	}

	// Blocked destination: not retried, not reached.
	blockAll := testDispatcher(newWebhookClient(func() *webhookAllowlist { return nil }))
	calls.Store(0)
	blockAll.deliver(q)
	if calls.Load() != 0 {
		t.Error("blocked destination must not be reached")
	}
}

func TestWebhookDeliveryDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	allow, _ := parseWebhookAllowlist("127.0.0.1")
	d := testDispatcher(newWebhookClient(func() *webhookAllowlist { return allow }))
	d.deadline = 200 * time.Millisecond
	start := time.Now()
	d.deliver(queuedEvent{url: srv.URL, event: ObjectEvent{Bucket: "b"}})
	if el := time.Since(start); el > 3*time.Second {
		t.Errorf("delivery must be bounded by its deadline, took %v", el)
	}
}

// A tarpit destination must not delay other destinations, and one bucket
// cannot exceed its backlog cap.
func TestWebhookDispatcherIsolation(t *testing.T) {
	d := testDispatcher(nil)
	block := make(chan struct{})
	var mu sync.Mutex
	delivered := map[string]int{}
	fast := make(chan struct{}, 1)
	d.deliverF = func(q queuedEvent) {
		if strings.Contains(q.url, "tarpit") {
			<-block
		}
		mu.Lock()
		delivered[q.event.Bucket]++
		mu.Unlock()
		if strings.Contains(q.url, "fast") {
			select {
			case fast <- struct{}{}:
			default:
			}
		}
	}
	defer close(block)

	accepted := 0
	for i := 0; i < 2000; i++ {
		if d.enqueue(queuedEvent{url: "https://tarpit.example/h", event: ObjectEvent{Bucket: "noisy", Key: "k"}}) {
			accepted++
		}
	}
	if accepted > webhookBucketMaxPending {
		t.Errorf("noisy bucket accepted %d events, cap %d", accepted, webhookBucketMaxPending)
	}
	if !d.enqueue(queuedEvent{url: "https://fast.example/h", event: ObjectEvent{Bucket: "quiet", Key: "k"}}) {
		t.Fatal("another bucket's event must still be accepted")
	}
	select {
	case <-fast:
	case <-time.After(2 * time.Second):
		t.Fatal("a tarpit destination delayed another destination's delivery")
	}
	d.mu.Lock()
	lane := d.lanes["https://tarpit.example:443"]
	workers := 0
	if lane != nil {
		workers = lane.workers
	}
	d.mu.Unlock()
	if workers > webhookWorkersPerHost {
		t.Errorf("tarpit lane has %d workers, cap %d", workers, webhookWorkersPerHost)
	}
}

func TestWebhookBucketRateLimit(t *testing.T) {
	d := testDispatcher(nil)
	d.deliverF = func(queuedEvent) {}
	now := time.Unix(1_000_000, 0)
	d.now = func() time.Time { return now }
	ev := func() queuedEvent { return queuedEvent{url: "https://r.example/", event: ObjectEvent{Bucket: "b"}} }

	if !d.enqueue(ev()) {
		t.Fatal("first event must be accepted")
	}
	d.mu.Lock()
	d.buckets["b"].tokens = 0
	d.mu.Unlock()
	if d.enqueue(ev()) {
		t.Fatal("event over the rate limit must be dropped")
	}
	d.mu.Lock()
	dropped := d.dropped["b"]
	d.mu.Unlock()
	if dropped != 1 {
		t.Errorf("dropped=%d want 1", dropped)
	}
	now = now.Add(time.Second) // refills webhookBucketRate tokens
	n := 0
	for i := 0; i < 1000 && d.enqueue(ev()); i++ {
		n++
	}
	if n < int(webhookBucketRate)-1 || n > int(webhookBucketRate)+1 {
		t.Errorf("accepted %d after 1s, want ~%v", n, webhookBucketRate)
	}
	d.reportAndPrune()
	d.mu.Lock()
	if len(d.dropped) != 0 {
		t.Error("report must reset drop counters")
	}
	d.mu.Unlock()
}

func TestLaneKey(t *testing.T) {
	cases := map[string]string{
		"https://Example.com/x":     "https://example.com:443",
		"http://example.com/x":      "http://example.com:80",
		"http://example.com:8080/x": "http://example.com:8080",
		"https://[2001:db8::1]:9/x": "https://2001:db8::1:9",
	}
	for in, want := range cases {
		if got, ok := laneKey(in); !ok || got != want {
			t.Errorf("laneKey(%q)=%q,%v want %q", in, got, ok, want)
		}
	}
	if _, ok := laneKey("::bad"); ok {
		t.Error("invalid URL must be rejected")
	}
}

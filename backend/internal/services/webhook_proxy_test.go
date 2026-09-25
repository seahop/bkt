package services

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// testForwardProxy is a minimal plain-HTTP forward proxy: it accepts
// absolute-form requests and relays every one of them to target (whatever
// host the request names), counting what it saw.
type testForwardProxy struct {
	srv   *httptest.Server
	hits  atomic.Int32
	hosts chan string
}

func newTestForwardProxy(t *testing.T, target string) *testForwardProxy {
	t.Helper()
	p := &testForwardProxy{hosts: make(chan string, 16)}
	tu, _ := url.Parse(target)
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		if !r.URL.IsAbs() {
			http.Error(w, "not a proxy request", http.StatusBadRequest)
			return
		}
		p.hosts <- r.URL.Host
		out := r.Clone(context.Background())
		out.RequestURI = ""
		out.URL.Scheme, out.URL.Host = tu.Scheme, tu.Host
		resp, err := http.DefaultTransport.RoundTrip(out)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func proxyDispatcher(t *testing.T, proxyURL string, allow string, lookup func(context.Context, string) ([]net.IPAddr, error)) *webhookDispatcher {
	t.Helper()
	setWebhookAllowlist(allow)
	t.Cleanup(func() { setWebhookAllowlist("") })
	pu, err := parseWebhookProxyURL(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	d := testDispatcher(newWebhookClientVia(currentWebhookAllowlist, pu))
	d.preflight = webhookTargetPreflight(currentWebhookAllowlist, lookup)
	return d
}

func noLookup(t *testing.T) func(context.Context, string) ([]net.IPAddr, error) {
	return func(context.Context, string) ([]net.IPAddr, error) {
		t.Error("unexpected DNS lookup")
		return nil, errors.New("no lookup")
	}
}

func staticLookup(ips ...string) func(context.Context, string) ([]net.IPAddr, error) {
	return func(context.Context, string) ([]net.IPAddr, error) {
		out := make([]net.IPAddr, 0, len(ips))
		for _, s := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return out, nil
	}
}

func TestWebhookProxyDeliversAllowedTarget(t *testing.T) {
	var targetHits atomic.Int32
	var sig atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		sig.Store(r.Header.Get("X-Bkt-Signature"))
	}))
	defer target.Close()
	proxy := newTestForwardProxy(t, target.URL)

	// Only the target NAME is allowlisted: the proxy (on loopback) must be
	// admitted by the dialer because it is the configured proxy, not
	// because of WEBHOOK_ALLOWED_HOSTS.
	d := proxyDispatcher(t, proxy.srv.URL, "target.test", noLookup(t))
	d.deliver(queuedEvent{url: "http://target.test/hook", secret: "k", event: ObjectEvent{Event: EventObjectCreated, Bucket: "b", Key: "x"}})

	if proxy.hits.Load() != 1 || targetHits.Load() != 1 {
		t.Fatalf("proxy hits=%d target hits=%d, want 1/1", proxy.hits.Load(), targetHits.Load())
	}
	if h := <-proxy.hosts; h != "target.test" {
		t.Errorf("proxy saw host %q", h)
	}
	if s, _ := sig.Load().(string); !strings.HasPrefix(s, "sha256=") {
		t.Errorf("signature not relayed: %q", s)
	}
}

func TestWebhookProxyRefusesBlockedTargetBeforeProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("blocked target reached")
	}))
	defer target.Close()
	proxy := newTestForwardProxy(t, target.URL)

	cases := []struct {
		name, url string
		lookup    func(context.Context, string) ([]net.IPAddr, error)
	}{
		{"loopback literal", target.URL + "/hook", noLookup(t)},
		{"metadata literal", "http://169.254.169.254/latest/meta-data/", noLookup(t)},
		{"localhost name", "http://localhost/hook", noLookup(t)},
		{"name resolving private", "http://evil.example/hook", staticLookup("10.1.2.3")},
		{"any resolved address blocked", "http://mixed.example/hook", staticLookup("8.8.8.8", "127.0.0.1")},
	}
	for _, tc := range cases {
		d := proxyDispatcher(t, proxy.srv.URL, "target.test", tc.lookup)
		err := d.preflight(context.Background(), tc.url)
		if !errors.Is(err, errWebhookBlocked) {
			t.Errorf("%s: preflight err=%v, want errWebhookBlocked", tc.name, err)
		}
		d.deliver(queuedEvent{url: tc.url, event: ObjectEvent{Event: EventObjectCreated, Bucket: "b", Key: "x"}})
		if n := proxy.hits.Load(); n != 0 {
			t.Fatalf("%s: proxy contacted %d times for a blocked target", tc.name, n)
		}
	}
}

// The target is re-resolved before every retry: a name that rebinds to a
// private address after the first attempt is refused on the retry.
func TestWebhookProxyPreflightRunsPerAttempt(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer target.Close()
	proxy := newTestForwardProxy(t, target.URL)

	var lookups atomic.Int32
	stable := func(context.Context, string) ([]net.IPAddr, error) {
		lookups.Add(1)
		return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
	}
	d := proxyDispatcher(t, proxy.srv.URL, "", stable)
	d.deliver(queuedEvent{url: "http://public.example/hook", event: ObjectEvent{Bucket: "b"}})
	if lookups.Load() != webhookAttempts || proxy.hits.Load() != webhookAttempts {
		t.Fatalf("lookups=%d proxy hits=%d, want %d each", lookups.Load(), proxy.hits.Load(), webhookAttempts)
	}

	proxy.hits.Store(0)
	lookups.Store(0)
	rebind := func(context.Context, string) ([]net.IPAddr, error) {
		if lookups.Add(1) == 1 {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("10.0.0.7")}}, nil
	}
	d = proxyDispatcher(t, proxy.srv.URL, "", rebind)
	d.deliver(queuedEvent{url: "http://public.example/hook", event: ObjectEvent{Bucket: "b"}})
	if proxy.hits.Load() != 1 || lookups.Load() != 2 {
		t.Fatalf("rebinding: proxy hits=%d lookups=%d, want 1/2", proxy.hits.Load(), lookups.Load())
	}
}

func TestWebhookInvalidProxyFailsClosed(t *testing.T) {
	var calls atomic.Int32
	d := testDispatcher(newWebhookClient(func() *webhookAllowlist { return nil }))
	d.preflight = func(context.Context, string) error { calls.Add(1); return errWebhookProxyInvalid }
	d.deliver(queuedEvent{url: "http://public.example/hook", event: ObjectEvent{Bucket: "b"}})
	if calls.Load() != 1 {
		t.Errorf("invalid proxy must not be retried: %d attempts", calls.Load())
	}
}

func TestParseWebhookProxyURL(t *testing.T) {
	for _, ok := range []string{"http://proxy.internal:3128", "https://user:pw@proxy:8443/", "HTTP://10.0.0.1:3128"} {
		if _, err := parseWebhookProxyURL(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"proxy:3128", "socks5://proxy:1080", "http://", "http://proxy/path", "http://proxy?x=1", "::"} {
		if _, err := parseWebhookProxyURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for in, want := range map[string]string{
		"http://Proxy.Internal":    "proxy.internal:80",
		"https://proxy":            "proxy:443",
		"http://[::1]:3128":        "[::1]:3128",
		"http://u:p@10.0.0.1:3128": "10.0.0.1:3128",
	} {
		u, err := parseWebhookProxyURL(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := webhookProxyAddr(u); got != want {
			t.Errorf("webhookProxyAddr(%q)=%q want %q", in, got, want)
		}
	}
}

// Unset WEBHOOK_PROXY_URL keeps the historical behaviour: no proxy at all,
// even if HTTP(S)_PROXY is set in the environment.
func TestWebhookProxyUnsetIgnoresEnvProxy(t *testing.T) {
	t.Setenv("WEBHOOK_PROXY_URL", "")
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	p, err := webhookProxyFromEnv()
	if p != nil || err != nil {
		t.Fatalf("got %v, %v", p, err)
	}
	tr := newWebhookClientVia(func() *webhookAllowlist { return nil }, nil).Transport.(*http.Transport)
	if tr.Proxy != nil {
		t.Error("default client must not use a proxy")
	}
}

package services

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"bkt/internal/logger"
)

// Webhook SSRF protection.
//
// A webhook URL is chosen by any user holding s3:PutBucketNotification, so
// the server must not become a proxy into its own network. Deliveries use a
// dedicated client whose dialer checks the IP address actually being
// connected to (after DNS resolution, so DNS rebinding cannot slip a private
// address past a check made earlier) and refuses loopback, private (RFC 1918,
// ULA), link-local (incl. cloud metadata 169.254.169.254), CGNAT, unspecified,
// multicast, reserved and IPv4-embedding IPv6 ranges. Redirects are never
// followed and environment proxies are ignored (a proxy would dial on our
// behalf, bypassing the check). URLs are also validated when configured.
//
// WEBHOOK_ALLOWED_HOSTS (comma-separated hostnames, IPs or CIDRs) exempts
// legitimate internal receivers from the private-address block: a listed
// hostname may resolve to any address; a listed IP/CIDR may be connected to
// whatever name resolved to it.

// errWebhookBlocked marks a destination refused by the SSRF guard.
var errWebhookBlocked = errors.New("webhook destination is not allowed (private, loopback, link-local or reserved address)")

// webhookAllowlist is the parsed WEBHOOK_ALLOWED_HOSTS.
type webhookAllowlist struct {
	hosts map[string]bool
	nets  []*net.IPNet
}

func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
}

// parseWebhookAllowlist parses a comma-separated list of hostnames, IPs and
// CIDRs. Invalid entries are returned separately (and ignored).
func parseWebhookAllowlist(s string) (*webhookAllowlist, []string) {
	al := &webhookAllowlist{hosts: map[string]bool{}}
	var invalid []string
	for _, raw := range strings.Split(s, ",") {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			if _, n, err := net.ParseCIDR(e); err == nil {
				al.nets = append(al.nets, n)
			} else {
				invalid = append(invalid, e)
			}
			continue
		}
		if ip := net.ParseIP(strings.Trim(e, "[]")); ip != nil {
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			al.nets = append(al.nets, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		h := normalizeHost(e)
		if h == "" || strings.ContainsAny(h, " /:@?#") {
			invalid = append(invalid, e)
			continue
		}
		al.hosts[h] = true
	}
	return al, invalid
}

func (al *webhookAllowlist) allowsHost(host string) bool {
	return al != nil && al.hosts[normalizeHost(host)]
}

func (al *webhookAllowlist) allowsIP(ip net.IP) bool {
	if al == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range al.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

var (
	webhookAllowlistPtr  atomic.Pointer[webhookAllowlist]
	webhookAllowlistOnce sync.Once
)

// currentWebhookAllowlist returns the allowlist, loading WEBHOOK_ALLOWED_HOSTS
// on first use.
func currentWebhookAllowlist() *webhookAllowlist {
	webhookAllowlistOnce.Do(func() {
		if webhookAllowlistPtr.Load() != nil {
			return // set explicitly (tests)
		}
		al, invalid := parseWebhookAllowlist(os.Getenv("WEBHOOK_ALLOWED_HOSTS"))
		if len(invalid) > 0 {
			logger.Warn("WEBHOOK_ALLOWED_HOSTS: ignoring invalid entries", map[string]interface{}{"entries": strings.Join(invalid, ",")})
		}
		webhookAllowlistPtr.Store(al)
	})
	return webhookAllowlistPtr.Load()
}

// setWebhookAllowlist replaces the allowlist (tests).
func setWebhookAllowlist(s string) {
	al, _ := parseWebhookAllowlist(s)
	webhookAllowlistOnce.Do(func() {})
	webhookAllowlistPtr.Store(al)
}

// blockedWebhookNets are ranges not covered by net.IP's classification
// helpers (IsLoopback/IsPrivate/IsLinkLocal*/IsMulticast/IsUnspecified).
var blockedWebhookNets = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8",       // "this network"
		"100.64.0.0/10",   // CGNAT (RFC 6598)
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserved + broadcast
		"::/96",           // IPv4-compatible IPv6 (deprecated)
		"64:ff9b::/96",    // NAT64 (embeds an IPv4 address)
		"64:ff9b:1::/48",  // local-use NAT64
		"2002::/16",       // 6to4 (embeds an IPv4 address)
		"2001::/32",       // Teredo (embeds an IPv4 address)
		"2001:db8::/32",   // documentation
		"fec0::/10",       // site-local (deprecated)
		"100::/64",        // discard-only
	}
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}()

// isBlockedWebhookIP reports whether ip must not be connected to by webhook
// deliveries. IPv4-mapped IPv6 addresses (::ffff:a.b.c.d) are classified as
// the IPv4 address they carry.
func isBlockedWebhookIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range blockedWebhookNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ValidateWebhookURL checks a webhook URL when it is configured: absolute
// http(s) URL, no embedded credentials, and — unless allowlisted — not an
// IP literal in a blocked range or a localhost name. Hostnames are NOT
// resolved here (the delivery dialer checks the resolved address on every
// connection, which also covers DNS rebinding).
func ValidateWebhookURL(raw string) error {
	if len(raw) > 2048 {
		return errors.New("webhook_url is too long")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("webhook_url is not a valid URL")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return errors.New("webhook_url must be an http(s) URL")
	}
	if u.Opaque != "" || u.Host == "" {
		return errors.New("webhook_url must be an absolute http(s) URL with a host")
	}
	if u.User != nil {
		return errors.New("webhook_url must not contain credentials (use webhook_secret for signing)")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("webhook_url must have a host")
	}
	al := currentWebhookAllowlist()
	if al.allowsHost(host) {
		return nil
	}
	if strings.Contains(host, "%") {
		return errors.New("webhook_url must not use a scoped (zone) IPv6 address")
	}
	if ip := net.ParseIP(host); ip != nil {
		if al.allowsIP(ip) || !isBlockedWebhookIP(ip) {
			return nil
		}
		return errors.New("webhook_url must not point to a private, loopback, link-local or reserved address (see WEBHOOK_ALLOWED_HOSTS)")
	}
	h := normalizeHost(host)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return errors.New("webhook_url must not point to localhost (see WEBHOOK_ALLOWED_HOSTS)")
	}
	// Numeric/hex shorthand hosts ("127.1", "2130706433", "0x7f.1") are
	// interpreted as IPs by some resolvers; no real TLD is numeric.
	labels := strings.Split(h, ".")
	last := labels[len(labels)-1]
	if strings.HasPrefix(last, "0x") || strings.Trim(last, "0123456789") == "" {
		return errors.New("webhook_url host is not a valid hostname or IP address")
	}
	return nil
}

// webhookDialContext returns a DialContext that enforces the SSRF policy on
// the address actually dialed (net.Dialer.Control runs per connection
// attempt with the resolved IP).
func webhookDialContext(allowlist func() *webhookAllowlist) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		al := allowlist()
		hostAllowed := al.allowsHost(host)
		d := &net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
			Control: func(network, address string, _ syscall.RawConn) error {
				h, _, err := net.SplitHostPort(address)
				if err != nil {
					return fmt.Errorf("%w: %s", errWebhookBlocked, address)
				}
				ip := net.ParseIP(h)
				if ip == nil {
					return fmt.Errorf("%w: %s", errWebhookBlocked, h)
				}
				if hostAllowed || al.allowsIP(ip) || !isBlockedWebhookIP(ip) {
					return nil
				}
				return fmt.Errorf("%w: %s", errWebhookBlocked, ip)
			},
		}
		return d.DialContext(ctx, network, addr)
	}
}

// newWebhookClient builds the delivery client: SSRF-guarded dialer, no
// proxies, no redirects (a 3xx is a failed delivery), bounded timeouts.
func newWebhookClient(allowlist func() *webhookAllowlist) *http.Client {
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           webhookDialContext(allowlist),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   webhookWorkersPerHost,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: webhookAttemptTimeout,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   webhookAttemptTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// redactWebhookURL reduces a webhook URL to scheme://host[:port] for logs
// (paths and queries often carry tokens).
func redactWebhookURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid url)"
	}
	return u.Scheme + "://" + u.Host
}

// RedactWebhookURL is redactWebhookURL for other packages (audit metadata).
func RedactWebhookURL(raw string) string {
	if raw == "" {
		return ""
	}
	return redactWebhookURL(raw)
}

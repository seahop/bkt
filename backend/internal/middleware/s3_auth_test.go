package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"bkt/internal/models"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// AWS documentation example credentials (SigV4 S3 examples).
const (
	docAccessKey = "AKIAIOSFODNN7EXAMPLE"
	docSecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

// testKeys exercise path encoding: spaces, parentheses, unicode, '+', '%'.
var testKeys = []string{
	"plain.txt",
	"My Docs/report (1).pdf",
	"ünï cödé.txt",
	"a+b%c.txt",
	"dir/sub dir/file~name_-.bin",
	"weird!$&'*,;=:@[]chars",
}

func TestAWSURIEncode(t *testing.T) {
	cases := []struct {
		in       string
		slash    bool
		expected string
	}{
		{"abcXYZ019-_.~", true, "abcXYZ019-_.~"},
		{"a b", true, "a%20b"},
		{"a+b", true, "a%2Bb"},
		{"a/b", true, "a%2Fb"},
		{"a/b", false, "a/b"},
		{"ü", true, "%C3%BC"},
		{"100%", true, "100%25"},
		{"(1)", true, "%281%29"},
	}
	for _, tc := range cases {
		if got := awsURIEncode(tc.in, tc.slash); got != tc.expected {
			t.Errorf("awsURIEncode(%q,%v) = %q, want %q", tc.in, tc.slash, got, tc.expected)
		}
	}
}

func TestCanonicalURIAndQuery(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/bucket/My%20Docs/a%2Fb/%C3%BC+x?prefix=a%20b&list-type=2&uploads", nil)
	if got, want := canonicalURI(r.URL), "/bucket/My%20Docs/a%2Fb/%C3%BC%2Bx"; got != want {
		t.Errorf("canonicalURI = %q, want %q", got, want)
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := canonicalQueryString(q, false), "list-type=2&prefix=a%20b&uploads="; got != want {
		t.Errorf("canonicalQueryString = %q, want %q", got, want)
	}
	// '+' in a query is a space for handlers (url.Query), so it must be
	// canonicalized as %20 — never as a literal plus.
	q2, _ := url.ParseQuery("prefix=a+b")
	if got := canonicalQueryString(q2, false); got != "prefix=a%20b" {
		t.Errorf("plus in query: %q", got)
	}
}

// AWS S3 SigV4 documentation example: "GET Object" with header auth.
func TestSigV4_AWSDocExample_GetObjectHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://examplebucket.s3.amazonaws.com/test.txt", nil)
	r.Header.Set("Range", "bytes=0-9")
	r.Header.Set("X-Amz-Content-Sha256", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	r.Header.Set("X-Amz-Date", "20130524T000000Z")
	authz := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request," +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date," +
		"Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	a, err := parseAuthorizationHeader(authz)
	if err != nil {
		t.Fatal(err)
	}
	if a.AccessKey != docAccessKey || a.Scope != "20130524/us-east-1/s3/aws4_request" {
		t.Fatalf("parsed %+v", a)
	}
	if _, err := computeHeaderSignature(r, a, docSecretKey); err != nil {
		t.Fatalf("AWS documented example did not verify: %v", err)
	}
	// Same request against the live clock is outside the replay window.
	if _, err := verifyHeaderSignature(r, a, docSecretKey, time.Now()); err == nil {
		t.Fatal("2013 timestamp must be rejected by the replay window")
	}
	// ...but inside the window at the documented time.
	at, _ := time.Parse(sigV4TimeFormat, "20130524T000500Z")
	if _, err := verifyHeaderSignature(r, a, docSecretKey, at); err != nil {
		t.Fatalf("within window: %v", err)
	}
}

// AWS S3 SigV4 documentation example: presigned GET /test.txt.
func TestSigV4_AWSDocExample_Presigned(t *testing.T) {
	u := "https://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	r := httptest.NewRequest(http.MethodGet, u, nil)
	q, _ := url.ParseQuery(r.URL.RawQuery)
	if err := verifyPresignedSignature(r, q, docSecretKey); err != nil {
		t.Fatalf("AWS documented presign example did not verify: %v", err)
	}
	q.Set("X-Amz-Expires", "86401")
	if err := verifyPresignedSignature(r, q, docSecretKey); err == nil {
		t.Fatal("tampered X-Amz-Expires must not verify")
	}
}

func TestCheckPresignWindow(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := checkPresignWindow(now.Add(-time.Minute), 900, now); err != nil {
		t.Errorf("fresh URL rejected: %v", err)
	}
	if err := checkPresignWindow(now.Add(-time.Hour), 900, now); !errors.Is(err, errPresignExpired) {
		t.Errorf("expired URL: %v", err)
	}
	if err := checkPresignWindow(now.Add(10*time.Minute), 900, now); err != nil {
		t.Errorf("small clock skew should be tolerated: %v", err)
	}
	// Far-future X-Amz-Date would otherwise yield a (near) forever-valid URL.
	if err := checkPresignWindow(now.AddDate(10, 0, 0), 604800, now); err == nil {
		t.Error("future-dated presigned URL must be rejected")
	}
}

func TestParseAuthorizationHeaderVariants(t *testing.T) {
	for _, h := range []string{
		"AWS4-HMAC-SHA256 Credential=AK/20240101/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-date, Signature=ABCDEF",
		"AWS4-HMAC-SHA256 Credential=AK/20240101/us-east-1/s3/aws4_request,SignedHeaders=host;x-amz-date,Signature=abcdef",
	} {
		a, err := parseAuthorizationHeader(h)
		if err != nil {
			t.Fatalf("%q: %v", h, err)
		}
		if a.AccessKey != "AK" || a.SignedHeaders != "host;x-amz-date" || a.Signature != "abcdef" {
			t.Errorf("%q parsed as %+v", h, a)
		}
	}
	for _, h := range []string{
		"AWS4-HMAC-SHA256 Credential=AK/20240101/us-east-1/s3, SignedHeaders=host, Signature=ab",
		"AWS4-HMAC-SHA256 SignedHeaders=host, Signature=ab",
		"AWS AK:sig",
	} {
		if _, err := parseAuthorizationHeader(h); err == nil {
			t.Errorf("%q should not parse", h)
		}
	}
}

// ── Round trips with the real AWS SDK ───────────────────────────────────────

// captureClient records the signed request instead of sending it.
type captureClient struct{ req *http.Request }

func (c *captureClient) Do(r *http.Request) (*http.Response, error) {
	c.req = r
	return &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/xml"}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    r,
	}, nil
}

func sdkClient(hc *captureClient) *s3.Client {
	return s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider(docAccessKey, docSecretKey, ""),
		BaseEndpoint: aws.String("http://s3.bkt.test:9000"),
		UsePathStyle: true,
		HTTPClient:   hc,
		Retryer:      aws.NopRetryer{},
	})
}

// toServerRequest rebuilds a client-side request the way net/http would
// present it to the server (RequestURI parsing, Host, headers).
func toServerRequest(t *testing.T, cr *http.Request, body []byte) *http.Request {
	t.Helper()
	sr := httptest.NewRequest(cr.Method, cr.URL.String(), bytes.NewReader(body))
	for k, v := range cr.Header {
		sr.Header[k] = append([]string(nil), v...)
	}
	sr.Host = cr.Host
	if sr.Host == "" {
		sr.Host = cr.URL.Host
	}
	return sr
}

func TestSigV4_SDKHeaderRoundTrip(t *testing.T) {
	for _, key := range testKeys {
		hc := &captureClient{}
		client := sdkClient(hc)
		_, _ = client.GetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String("bucket"), Key: aws.String(key)})
		if hc.req == nil {
			t.Fatalf("%q: no request captured", key)
		}
		sr := toServerRequest(t, hc.req, nil)
		a, err := parseAuthorizationHeader(sr.Header.Get("Authorization"))
		if err != nil {
			t.Fatalf("%q: %v", key, err)
		}
		if _, err := verifyHeaderSignature(sr, a, docSecretKey, time.Now()); err != nil {
			t.Errorf("key %q: SDK-signed GetObject failed to verify: %v (path %s)", key, err, sr.URL.EscapedPath())
		}
		if !strings.HasSuffix(sr.URL.Path, "/"+key) {
			t.Errorf("key %q: decoded path %q", key, sr.URL.Path)
		}
		// A request for a different key under the same signature must fail.
		tampered := sr.Clone(context.Background())
		tampered.URL.Path += "x"
		tampered.URL.RawPath = ""
		if _, err := verifyHeaderSignature(tampered, a, docSecretKey, time.Now()); err == nil {
			t.Errorf("key %q: tampered path verified", key)
		}
	}

	// Query-string encoding: prefix with spaces and reserved characters.
	hc := &captureClient{}
	_, _ = sdkClient(hc).ListObjectsV2(context.Background(), &s3.ListObjectsV2Input{
		Bucket: aws.String("bucket"), Prefix: aws.String("My Docs/a+b (1)&x=y"), Delimiter: aws.String("/"),
	})
	sr := toServerRequest(t, hc.req, nil)
	a, err := parseAuthorizationHeader(sr.Header.Get("Authorization"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyHeaderSignature(sr, a, docSecretKey, time.Now()); err != nil {
		t.Errorf("SDK-signed ListObjectsV2 with encoded prefix failed: %v (query %s)", err, sr.URL.RawQuery)
	}
	if got := sr.URL.Query().Get("prefix"); got != "My Docs/a+b (1)&x=y" {
		t.Errorf("handler-visible prefix = %q", got)
	}
}

func TestSigV4_SDKPutObjectWithPayloadHash(t *testing.T) {
	hc := &captureClient{}
	body := []byte("hello, integrity")
	_, _ = sdkClient(hc).PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"), Key: aws.String("My Docs/report (1).pdf"), Body: bytes.NewReader(body),
	})
	sr := toServerRequest(t, hc.req, body)
	a, err := parseAuthorizationHeader(sr.Header.Get("Authorization"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifyHeaderSignature(sr, a, docSecretKey, time.Now()); err != nil {
		t.Fatalf("SDK-signed PutObject failed to verify: %v", err)
	}
	ph := sr.Header.Get("X-Amz-Content-Sha256")
	if !validPayloadHashValue(ph) {
		t.Fatalf("SDK sent unsupported payload hash %q", ph)
	}
	if isHexSHA256(ph) {
		// The signed digest must match the real body...
		if !wrapBodyWithDigestCheck(sr, ph) {
			t.Fatal("wrap refused")
		}
		if got, err := io.ReadAll(sr.Body); err != nil || !bytes.Equal(got, body) {
			t.Fatalf("genuine body rejected: %v", err)
		}
		// ...and a swapped body (same signature) must be caught.
		sr2 := toServerRequest(t, hc.req, []byte("HELLO, INTEGRITY"))
		wrapBodyWithDigestCheck(sr2, ph)
		if _, err := io.ReadAll(sr2.Body); !errors.Is(err, ErrContentSHA256Mismatch) {
			t.Fatalf("tampered body not detected: %v", err)
		}
	}
}

func TestSigV4_SDKPresignRoundTrip(t *testing.T) {
	for _, key := range testKeys {
		ps := s3.NewPresignClient(sdkClient(&captureClient{}))
		out, err := ps.PresignGetObject(context.Background(), &s3.GetObjectInput{Bucket: aws.String("bucket"), Key: aws.String(key)},
			func(o *s3.PresignOptions) { o.Expires = 15 * time.Minute })
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodGet, out.URL, nil)
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			t.Fatal(err)
		}
		if err := verifyPresignedSignature(r, q, docSecretKey); err != nil {
			t.Errorf("key %q: SDK presigned URL failed to verify: %v (%s)", key, err, out.URL)
		}
		if err := checkScope(strings.SplitN(q.Get("X-Amz-Credential"), "/", 2)[1], q.Get("X-Amz-Date")); err != nil {
			t.Errorf("scope: %v", err)
		}
		// Wrong secret must fail.
		if err := verifyPresignedSignature(r, q, "not-the-secret"); err == nil {
			t.Errorf("key %q: verified with wrong secret", key)
		}
	}
}

// ── Payload integrity ───────────────────────────────────────────────────────

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func newBodyRequest(body []byte, contentLength int64) *http.Request {
	r := httptest.NewRequest(http.MethodPut, "/bucket/key", bytes.NewReader(body))
	r.ContentLength = contentLength
	return r
}

func TestDigestVerifyingReader(t *testing.T) {
	payload := []byte(strings.Repeat("0123456789", 1000))
	good := hexSHA(payload)
	bad := hexSHA([]byte("something else"))

	// Match: ReadAll succeeds and returns everything.
	r := newBodyRequest(payload, int64(len(payload)))
	if !wrapBodyWithDigestCheck(r, strings.ToUpper(good)) {
		t.Fatal("wrap refused")
	}
	got, err := io.ReadAll(r.Body)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("matching body: err=%v len=%d", err, len(got))
	}

	// Mismatch: ReadAll fails with the sentinel error.
	r = newBodyRequest(payload, int64(len(payload)))
	wrapBodyWithDigestCheck(r, bad)
	if _, err := io.ReadAll(r.Body); !errors.Is(err, ErrContentSHA256Mismatch) {
		t.Fatalf("mismatch via ReadAll: %v", err)
	}

	// Mismatch with a consumer that reads exactly Content-Length bytes
	// (never sees io.EOF) must still fail.
	r = newBodyRequest(payload, int64(len(payload)))
	wrapBodyWithDigestCheck(r, bad)
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(r.Body, buf); err == nil {
		t.Fatal("io.ReadFull of a tampered body must error")
	}
	r = newBodyRequest(payload, int64(len(payload)))
	wrapBodyWithDigestCheck(r, bad)
	if _, err := io.CopyN(io.Discard, r.Body, int64(len(payload))); err == nil {
		t.Fatal("io.CopyN of a tampered body must error")
	}

	// Unknown length (chunked transfer): checked at EOF.
	r = newBodyRequest(payload, -1)
	wrapBodyWithDigestCheck(r, bad)
	if _, err := io.Copy(io.Discard, r.Body); !errors.Is(err, ErrContentSHA256Mismatch) {
		t.Fatalf("chunked mismatch: %v", err)
	}
	r = newBodyRequest(payload, -1)
	wrapBodyWithDigestCheck(r, good)
	if n, err := io.Copy(io.Discard, r.Body); err != nil || n != int64(len(payload)) {
		t.Fatalf("chunked match: n=%d err=%v", n, err)
	}

	// Empty body: verified up front.
	r = httptest.NewRequest(http.MethodGet, "/bucket/key", nil)
	if !wrapBodyWithDigestCheck(r, hexSHA(nil)) {
		t.Error("empty body with the empty-string digest must pass")
	}
	r = httptest.NewRequest(http.MethodGet, "/bucket/key", nil)
	if wrapBodyWithDigestCheck(r, good) {
		t.Error("empty body with a non-empty digest must be rejected")
	}
}

func TestValidPayloadHashValue(t *testing.T) {
	ok := []string{"", "UNSIGNED-PAYLOAD", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD", "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER",
		"STREAMING-UNSIGNED-PAYLOAD-TRAILER", hexSHA([]byte("x"))}
	for _, v := range ok {
		if !validPayloadHashValue(v) {
			t.Errorf("%q should be accepted", v)
		}
	}
	for _, v := range []string{"garbage", "STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD", strings.Repeat("z", 64), "abc"} {
		if validPayloadHashValue(v) {
			t.Errorf("%q should be rejected", v)
		}
	}
}

func TestSTSCredentialRevokedOnTokenVersionBump(t *testing.T) {
	v := 3
	k := &models.AccessKey{Temporary: true, IssuerTokenVersion: &v}
	k.User.TokenVersion = 3
	if stsCredentialRevoked(k) {
		t.Error("same generation must be valid")
	}
	k.User.TokenVersion = 4
	if !stsCredentialRevoked(k) {
		t.Error("bumped TokenVersion must revoke the temporary credential")
	}
	long := &models.AccessKey{Temporary: false}
	long.User.TokenVersion = 9
	if stsCredentialRevoked(long) {
		t.Error("long-lived keys are not bound to TokenVersion")
	}
}

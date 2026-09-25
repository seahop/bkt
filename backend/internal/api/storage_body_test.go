package api

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bkt/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// ── SigV4 streaming helpers ─────────────────────────────────────────────────

func testHMAC(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func testSigningKey(secret, date, region string) []byte {
	k := testHMAC([]byte("AWS4"+secret), date)
	k = testHMAC(k, region)
	k = testHMAC(k, "s3")
	return testHMAC(k, "aws4_request")
}

// AWS documentation example for STREAMING-AWS4-HMAC-SHA256-PAYLOAD
// ("Signature Calculations for the Authorization Header: Transferring Payload
// in Multiple Chunks"): 66560 bytes of 'a' in a 64 KiB chunk and a 1 KiB chunk.
var awsExample = struct {
	secret, date, amzDate, region, seed string
	chunkSigs                           []string
}{
	secret:  "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	date:    "20130524",
	amzDate: "20130524T000000Z",
	region:  "us-east-1",
	seed:    "4f232c4386841ef735655705268965c44a0e4690baa4adea153f7db9fa80a0a9",
	chunkSigs: []string{
		"ad80c730a21e5b8d04586a2213dd63b9a0e99e0e2307b0ade35a65485a288648",
		"0055627c9e194cb4542bae2aa5492e3c1575bbb81b612b7d234b86a503ef5497",
		"b6c6ea8a5354eaf15b3cb7646744f4275b71ea724fed81ceb9323e279d449df9",
	},
}

func awsExampleContext() *chunkSigningContext {
	return &chunkSigningContext{
		key:     testSigningKey(awsExample.secret, awsExample.date, awsExample.region),
		seedSig: awsExample.seed,
		amzDate: awsExample.amzDate,
		scope:   awsExample.date + "/" + awsExample.region + "/s3/aws4_request",
	}
}

func awsExampleBody(sigs []string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "10000;chunk-signature=%s\r\n", sigs[0])
	b.Write(bytes.Repeat([]byte("a"), 65536))
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "400;chunk-signature=%s\r\n", sigs[1])
	b.Write(bytes.Repeat([]byte("a"), 1024))
	b.WriteString("\r\n")
	fmt.Fprintf(&b, "0;chunk-signature=%s\r\n\r\n", sigs[2])
	return b.Bytes()
}

// signChunks builds a signed aws-chunked body for data split into chunkSize
// pieces, optionally with signed trailers.
func signChunks(sc *chunkSigningContext, data []byte, chunkSize int, trailers [][2]string) []byte {
	var b bytes.Buffer
	prev := sc.seedSig
	sign := func(payload []byte) string {
		h := sha256.Sum256(payload)
		sts := "AWS4-HMAC-SHA256-PAYLOAD\n" + sc.amzDate + "\n" + sc.scope + "\n" + prev + "\n" + emptySHA256Hex + "\n" + hex.EncodeToString(h[:])
		prev = hex.EncodeToString(testHMAC(sc.key, sts))
		return prev
	}
	for off := 0; off < len(data); off += chunkSize {
		end := min(off+chunkSize, len(data))
		chunk := data[off:end]
		fmt.Fprintf(&b, "%x;chunk-signature=%s\r\n", len(chunk), sign(chunk))
		b.Write(chunk)
		b.WriteString("\r\n")
	}
	fmt.Fprintf(&b, "0;chunk-signature=%s\r\n", sign(nil))
	if trailers == nil {
		b.WriteString("\r\n")
		return b.Bytes()
	}
	var canon strings.Builder
	for _, t := range trailers {
		fmt.Fprintf(&b, "%s:%s\r\n", t[0], t[1])
		canon.WriteString(t[0] + ":" + t[1] + "\n")
	}
	h := sha256.Sum256([]byte(canon.String()))
	sts := "AWS4-HMAC-SHA256-TRAILER\n" + sc.amzDate + "\n" + sc.scope + "\n" + prev + "\n" + hex.EncodeToString(h[:])
	fmt.Fprintf(&b, "x-amz-trailer-signature:%s\r\n\r\n", hex.EncodeToString(testHMAC(sc.key, sts)))
	return b.Bytes()
}

func decodeAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }

// ── aws-chunked decoding / verification ─────────────────────────────────────

func TestAWSChunkedSignedMatchesAWSExample(t *testing.T) {
	r := newAWSChunkedReaderMode(bytes.NewReader(awsExampleBody(awsExample.chunkSigs)), chunkModeSigned, awsExampleContext())
	got, err := decodeAll(r)
	if err != nil {
		t.Fatalf("AWS example failed to verify: %v", err)
	}
	if len(got) != 66560 || !bytes.Equal(got, bytes.Repeat([]byte("a"), 66560)) {
		t.Fatalf("decoded %d bytes, want 66560 'a'", len(got))
	}
}

func TestAWSChunkedSignedRejectsTampering(t *testing.T) {
	sc := awsExampleContext()

	// Wrong chunk signature (first chunk).
	sigs := append([]string(nil), awsExample.chunkSigs...)
	sigs[0] = strings.Repeat("0", 64)
	if _, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(awsExampleBody(sigs)), chunkModeSigned, sc)); !errors.Is(err, errChunkSignature) {
		t.Errorf("bad first signature: err = %v, want errChunkSignature", err)
	}

	// Wrong final (zero-length) chunk signature — the terminator is signed too.
	sigs = append([]string(nil), awsExample.chunkSigs...)
	sigs[2] = strings.Repeat("1", 64)
	if _, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(awsExampleBody(sigs)), chunkModeSigned, sc)); !errors.Is(err, errChunkSignature) {
		t.Errorf("bad final signature: err = %v, want errChunkSignature", err)
	}

	// Modified payload byte with the original signatures.
	body := awsExampleBody(awsExample.chunkSigs)
	idx := bytes.IndexByte(body, 'a')
	body[idx+10] = 'b'
	if _, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(body), chunkModeSigned, sc)); !errors.Is(err, errChunkSignature) {
		t.Errorf("tampered data: err = %v, want errChunkSignature", err)
	}

	// Missing chunk-signature extension.
	noSig := []byte("5\r\nhello\r\n0\r\n\r\n")
	if _, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(noSig), chunkModeSigned, sc)); err == nil {
		t.Error("signed mode accepted chunks without signatures")
	}

	// A different seed (i.e. a different request) breaks the chain.
	other := *sc
	other.seedSig = strings.Repeat("a", 64)
	if _, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(awsExampleBody(awsExample.chunkSigs)), chunkModeSigned, &other)); !errors.Is(err, errChunkSignature) {
		t.Errorf("replayed chunks with another seed: err = %v, want errChunkSignature", err)
	}
}

func TestAWSChunkedSignedRoundTripAndTruncation(t *testing.T) {
	sc := awsExampleContext()
	data := bytes.Repeat([]byte("0123456789"), 5000)
	body := signChunks(sc, data, 8192, nil)
	got, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(body), chunkModeSigned, sc))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("round trip failed: err=%v len=%d", err, len(got))
	}

	// Truncated before the final chunk: must be an error, never a clean EOF.
	cut := body[:bytes.LastIndex(body, []byte("0;chunk-signature="))]
	if _, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(cut), chunkModeSigned, sc)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("truncated stream: err = %v, want io.ErrUnexpectedEOF", err)
	}
	// Truncated inside a chunk.
	if _, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(body[:100]), chunkModeSigned, sc)); err == nil {
		t.Error("stream truncated mid-chunk decoded without error")
	}
}

func TestAWSChunkedSignedTrailer(t *testing.T) {
	sc := awsExampleContext()
	data := []byte("hello trailer world")
	trailers := [][2]string{{"x-amz-checksum-crc32", "AAAAAA=="}}
	body := signChunks(sc, data, 8, trailers)
	got, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(body), chunkModeSignedTrailer, sc))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("signed trailer round trip: err=%v got=%q", err, got)
	}

	// Tampered trailer value → trailer signature mismatch.
	bad := bytes.Replace(body, []byte("AAAAAA=="), []byte("BBBBBB=="), 1)
	if _, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(bad), chunkModeSignedTrailer, sc)); !errors.Is(err, errChunkSignature) {
		t.Errorf("tampered trailer: err = %v, want errChunkSignature", err)
	}
	// Trailer signature removed.
	i := bytes.Index(body, []byte("x-amz-trailer-signature:"))
	stripped := append(append([]byte(nil), body[:i]...), []byte("\r\n")...)
	if _, err := decodeAll(newAWSChunkedReaderMode(bytes.NewReader(stripped), chunkModeSignedTrailer, sc)); !errors.Is(err, errChunkSignature) {
		t.Errorf("missing trailer signature: err = %v, want errChunkSignature", err)
	}
}

func TestAWSChunkedUnsignedTrailer(t *testing.T) {
	// aws-cli v2 default over TLS: STREAMING-UNSIGNED-PAYLOAD-TRAILER.
	body := "5\r\nhello\r\n6\r\n world\r\n0\r\nx-amz-checksum-crc32:DUoRhQ==\r\n\r\n"
	got, err := decodeAll(newAWSChunkedReaderMode(strings.NewReader(body), chunkModeUnsignedTrailer, nil))
	if err != nil || string(got) != "hello world" {
		t.Fatalf("unsigned trailer: err=%v got=%q", err, got)
	}
}

func TestAWSChunkedRejectsMalformed(t *testing.T) {
	cases := map[string]string{
		"non-hex size":        "zz\r\nhello\r\n0\r\n\r\n",
		"oversized line":      strings.Repeat("1", awsChunkMaxLine+10) + "\r\n",
		"missing data CRLF":   "5\r\nhelloXX0\r\n\r\n",
		"huge declared chunk": "7fffffffffffffff\r\n",
		"data after final":    "0\r\ngarbage\r\n\r\n",
	}
	for name, body := range cases {
		if _, err := decodeAll(newAWSChunkedReaderMode(strings.NewReader(body), chunkModeUnsigned, nil)); err == nil {
			t.Errorf("%s: decoded without error", name)
		}
	}
}

// ── guardedBody ─────────────────────────────────────────────────────────────

// stepReader returns its data one byte per Read and then a configurable
// final error (io.EOF or a verification failure), like the SigV4 payload-hash
// wrapper does at EOF.
type stepReader struct {
	data []byte
	end  error
}

func (s *stepReader) Read(p []byte) (int, error) {
	if len(s.data) == 0 {
		return 0, s.end
	}
	p[0] = s.data[0]
	s.data = s.data[1:]
	return 1, nil
}

func TestGuardedBodyWithholdsLastByteUntilVerified(t *testing.T) {
	hashErr := errors.New("payload hash mismatch")
	g := newGuardedBody(&stepReader{data: []byte("abcd"), end: hashErr}, 4, 0, nil)
	got := make([]byte, 0, 4)
	buf := make([]byte, 16)
	var err error
	for {
		var n int
		n, err = g.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			break
		}
	}
	if !errors.Is(err, hashErr) {
		t.Fatalf("err = %v, want the source's verification error", err)
	}
	if len(got) >= 4 {
		t.Fatalf("consumer received all %d bytes before verification failed", len(got))
	}
	if g.Complete() {
		t.Error("Complete() = true after a failed body")
	}

	// Happy path: all bytes, then EOF.
	g = newGuardedBody(&stepReader{data: []byte("abcd"), end: io.EOF}, 4, 0, nil)
	all, err := io.ReadAll(g)
	if err != nil || string(all) != "abcd" || !g.Complete() {
		t.Fatalf("happy path: err=%v got=%q complete=%v", err, all, g.Complete())
	}
}

func TestGuardedBodyLengthAndCap(t *testing.T) {
	// Shorter than declared → mismatch, and the consumer never gets all bytes.
	g := newGuardedBody(strings.NewReader("abc"), 5, 0, nil)
	if _, err := io.ReadAll(g); !errors.Is(err, errBodyLengthMismatch) {
		t.Errorf("short body: err = %v", err)
	}
	// Longer than declared.
	g = newGuardedBody(strings.NewReader("abcdef"), 5, 0, nil)
	if _, err := io.ReadAll(g); !errors.Is(err, errBodyLengthMismatch) {
		t.Errorf("long body: err = %v", err)
	}
	// Over the hard cap.
	g = newGuardedBody(strings.NewReader("abcdef"), -1, 4, nil)
	if _, err := io.ReadAll(g); !errors.Is(err, errBodyTooLarge) {
		t.Errorf("over cap: err = %v", err)
	}
	// Empty body with declared 0.
	g = newGuardedBody(strings.NewReader(""), 0, 10, nil)
	if b, err := io.ReadAll(g); err != nil || len(b) != 0 || !g.Complete() {
		t.Errorf("empty body: err=%v len=%d complete=%v", err, len(b), g.Complete())
	}
}

// ── quota reservations ──────────────────────────────────────────────────────

func withUsage(t *testing.T, used int64) {
	t.Helper()
	orig := bucketUsageFn
	bucketUsageFn = func(uuid.UUID) (int64, error) { return used, nil }
	t.Cleanup(func() { bucketUsageFn = orig })
}

func TestQuotaReservationsPreventConcurrentOvershoot(t *testing.T) {
	withUsage(t, 50)
	b := &models.Bucket{ID: uuid.New(), Name: "q", QuotaBytes: 100}

	r1, err := reserveBucketQuota(b, 30)
	if err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	// 50 used + 30 reserved + 30 > 100: the second concurrent writer is refused
	// even though usage alone (50) would have admitted it.
	if _, err := reserveBucketQuota(b, 30); err == nil {
		t.Fatal("second reservation admitted; concurrent uploads could overshoot the quota")
	}
	r1.release()
	r2, err := reserveBucketQuota(b, 30)
	if err != nil {
		t.Fatalf("reservation after release: %v", err)
	}
	r2.release()
	r2.release() // idempotent
}

func TestQuotaEnforcedOnActualBytes(t *testing.T) {
	withUsage(t, 0)
	b := &models.Bucket{ID: uuid.New(), Name: "q", QuotaBytes: 10}
	// A chunked upload declaring nothing (reservation 0) is cut off once the
	// bytes actually read exceed the quota.
	res, err := reserveBucketQuota(b, 0)
	if err != nil {
		t.Fatal(err)
	}
	g := newGuardedBody(strings.NewReader(strings.Repeat("x", 11)), -1, 0, res)
	_, err = io.ReadAll(g)
	var qe *quotaExceededError
	if !errors.As(err, &qe) {
		t.Fatalf("err = %v, want quota exceeded", err)
	}
	if code, _, status, ok := bodyFailure(g); !ok || code != "QuotaExceeded" || status != http.StatusForbidden {
		t.Errorf("bodyFailure = %s %d %v", code, status, ok)
	}

	// Final authoritative check: usage grew (another writer committed) while
	// this body streamed.
	res.release()
	res2, err := reserveBucketQuota(b, 5)
	if err != nil || res2 == nil {
		t.Fatalf("second reservation: res=%v err=%v", res2, err)
	}
	defer res2.release()
	bucketUsageFn = func(uuid.UUID) (int64, error) { return 8, nil }
	g = newGuardedBody(strings.NewReader("12345"), 5, 0, res2)
	if _, err := io.ReadAll(g); !errors.As(err, &qe) {
		t.Errorf("final check: err = %v, want quota exceeded", err)
	}
}

func TestQuotaNoopWithoutQuota(t *testing.T) {
	res, err := reserveBucketQuota(&models.Bucket{ID: uuid.New()}, 1<<40)
	if err != nil || res != nil {
		t.Fatalf("unlimited bucket: res=%v err=%v", res, err)
	}
	res.release() // nil-safe
	if err := res.ensure(1 << 50); err != nil {
		t.Error(err)
	}
}

// ── prepareUploadBody ───────────────────────────────────────────────────────

func newUploadCtx(body string, headers map[string]string, contentLength int64) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	req := httptest.NewRequest(http.MethodPut, "/b/k", strings.NewReader(body))
	req.ContentLength = contentLength
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	c.Request = req
	return c
}

func TestPrepareUploadBodyUsesDecodedLength(t *testing.T) {
	encoded := "5\r\nhello\r\n0\r\n\r\n"
	c := newUploadCtx(encoded, map[string]string{
		"Content-Encoding":             "aws-chunked",
		"X-Amz-Decoded-Content-Length": "5",
	}, int64(len(encoded)))
	ub, berr := prepareUploadBody(c, 1<<20)
	if berr != nil {
		t.Fatalf("unexpected error: %+v", berr)
	}
	if ub.declared != 5 {
		t.Fatalf("declared = %d, want decoded length 5 (not encoded %d)", ub.declared, len(encoded))
	}
	g := newGuardedBody(ub.reader, ub.declared, 1<<20, nil)
	if b, err := io.ReadAll(g); err != nil || string(b) != "hello" {
		t.Fatalf("decoded body: err=%v got=%q", err, b)
	}

	// A lying decoded length is caught against the actual bytes.
	c = newUploadCtx(encoded, map[string]string{
		"Content-Encoding":             "aws-chunked",
		"X-Amz-Decoded-Content-Length": "3",
	}, int64(len(encoded)))
	ub, _ = prepareUploadBody(c, 1<<20)
	g = newGuardedBody(ub.reader, ub.declared, 1<<20, nil)
	if _, err := io.ReadAll(g); !errors.Is(err, errBodyLengthMismatch) {
		t.Errorf("understated decoded length: err = %v", err)
	}
}

func TestPrepareUploadBodyRejections(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		cl      int64
		code    string
	}{
		{"aws-chunked without decoded length", map[string]string{"Content-Encoding": "aws-chunked"}, 10, "MissingContentLength"},
		{"bad decoded length", map[string]string{"Content-Encoding": "aws-chunked", "X-Amz-Decoded-Content-Length": "-1"}, 10, "MissingContentLength"},
		{"signed streaming without header auth", map[string]string{"X-Amz-Content-Sha256": payloadStreamingSigned, "X-Amz-Decoded-Content-Length": "5"}, 10, "AccessDenied"},
		{"signed trailer without header auth", map[string]string{"X-Amz-Content-Sha256": payloadStreamingSignedTrailer, "X-Amz-Decoded-Content-Length": "5"}, 10, "AccessDenied"},
		{"sigv4a streaming", map[string]string{"X-Amz-Content-Sha256": "STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD", "X-Amz-Decoded-Content-Length": "5"}, 10, "NotImplemented"},
		{"too large", map[string]string{"Content-Encoding": "aws-chunked", "X-Amz-Decoded-Content-Length": "999999"}, 10, "EntityTooLarge"},
		{"no length at all", map[string]string{}, -1, "MissingContentLength"},
	}
	for _, tc := range cases {
		c := newUploadCtx("", tc.headers, tc.cl)
		_, berr := prepareUploadBody(c, 1000)
		if berr == nil || berr.code != tc.code {
			t.Errorf("%s: got %+v, want %s", tc.name, berr, tc.code)
		}
	}
}

func TestPrepareUploadBodySignedStreamingWithContext(t *testing.T) {
	sc := awsExampleContext()
	body := awsExampleBody(awsExample.chunkSigs)
	c := newUploadCtx(string(body), map[string]string{
		"X-Amz-Content-Sha256":         payloadStreamingSigned,
		"Content-Encoding":             "aws-chunked",
		"X-Amz-Decoded-Content-Length": "66560",
	}, int64(len(body)))
	c.Set("sigv4_signing_key", sc.key)
	c.Set("sigv4_seed_signature", sc.seedSig)
	c.Set("sigv4_amz_date", sc.amzDate)
	c.Set("sigv4_scope", sc.scope)
	ub, berr := prepareUploadBody(c, 1<<20)
	if berr != nil {
		t.Fatalf("unexpected error: %+v", berr)
	}
	g := newGuardedBody(ub.reader, ub.declared, 1<<20, nil)
	got, err := io.ReadAll(g)
	if err != nil || len(got) != 66560 {
		t.Fatalf("err=%v len=%d", err, len(got))
	}
}

func TestReadBoundedBody(t *testing.T) {
	if b, err := readBoundedBody(strings.NewReader("abc"), 3); err != nil || string(b) != "abc" {
		t.Errorf("exact size: %q %v", b, err)
	}
	if _, err := readBoundedBody(strings.NewReader("abcd"), 3); !errors.Is(err, errRequestBodyTooLarge) {
		t.Errorf("oversize: err = %v", err)
	}
}

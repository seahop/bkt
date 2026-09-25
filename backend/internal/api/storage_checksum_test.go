package api

import (
	"bytes"
	"context"
	"crypto/md5"  //nolint:gosec // test vectors for Content-MD5
	"crypto/sha1" //nolint:gosec // test vectors for x-amz-checksum-sha1
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/gin-gonic/gin"
)

func b64sum(h hash.Hash, data string) string {
	h.Write([]byte(data))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// readUpload runs a prepared body through the guarded pipeline the handlers use.
func readUpload(t *testing.T, c *gin.Context) ([]byte, *guardedBody, *s3BodyError) {
	t.Helper()
	ub, berr := prepareUploadBody(c, 1<<30)
	if berr != nil {
		return nil, nil, berr
	}
	g := newGuardedBody(ub.reader, ub.declared, 1<<30, nil)
	data, _ := io.ReadAll(g)
	return data, g, nil
}

func TestCRC64NVMECheckValue(t *testing.T) {
	// CRC-64/NVME catalogue check value for "123456789".
	if got := crc64.Checksum([]byte("123456789"), crc64NVMETable); got != 0xae8b14860a799888 {
		t.Fatalf("crc64nvme(123456789) = %#x, want 0xae8b14860a799888", got)
	}
}

func TestHeaderChecksumsOnPlainPut(t *testing.T) {
	const body = "hello, checksum world"
	good := map[string]string{
		"Content-MD5":              b64sum(md5.New(), body), //nolint:gosec
		"x-amz-checksum-crc32":     b64sum(crc32.NewIEEE(), body),
		"x-amz-checksum-crc32c":    b64sum(crc32.New(crc32.MakeTable(crc32.Castagnoli)), body),
		"x-amz-checksum-crc64nvme": b64sum(crc64.New(crc64NVMETable), body),
		"x-amz-checksum-sha1":      b64sum(sha1.New(), body), //nolint:gosec
		"x-amz-checksum-sha256":    b64sum(sha256.New(), body),
	}
	for name, val := range good {
		c := newUploadCtx(body, map[string]string{name: val}, int64(len(body)))
		data, g, berr := readUpload(t, c)
		if berr != nil || !g.Complete() || string(data) != body {
			t.Errorf("%s: valid checksum rejected: berr=%+v err=%v", name, berr, g.Err())
		}

		// Same checksum, one byte changed in the body → BadDigest, and the
		// final byte is never released.
		bad := "j" + body[1:]
		c = newUploadCtx(bad, map[string]string{name: val}, int64(len(bad)))
		data, g, berr = readUpload(t, c)
		if berr != nil {
			t.Fatalf("%s: unexpected prepare error %+v", name, berr)
		}
		code, _, status, ok := bodyFailure(g)
		if !ok || code != "BadDigest" || status != http.StatusBadRequest || g.Complete() {
			t.Errorf("%s: mismatch → code=%q status=%d complete=%v", name, code, status, g.Complete())
		}
		if len(data) >= len(bad) {
			t.Errorf("%s: whole body released despite mismatch", name)
		}
	}
}

func TestMalformedChecksumHeaders(t *testing.T) {
	cases := map[string]string{
		"Content-MD5":           "not-base64!!",
		"x-amz-checksum-crc32":  base64.StdEncoding.EncodeToString([]byte{1, 2, 3}), // wrong size
		"x-amz-checksum-sha256": "@@@",
	}
	for name, val := range cases {
		c := newUploadCtx("abc", map[string]string{name: val}, 3)
		_, _, berr := readUpload(t, c)
		if berr == nil || berr.status != http.StatusBadRequest {
			t.Errorf("%s=%q: got %+v, want a 400", name, val, berr)
		}
	}
}

func chunkedWithTrailer(data string, trailers ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%x\r\n%s\r\n0\r\n", len(data), data)
	for _, t := range trailers {
		b.WriteString(t + "\r\n")
	}
	b.WriteString("\r\n")
	return b.String()
}

func TestUnsignedTrailerChecksums(t *testing.T) {
	const data = "trailing checksum payload"
	crc := b64sum(crc32.NewIEEE(), data)
	hdr := func(trailer string) map[string]string {
		m := map[string]string{
			"X-Amz-Content-Sha256":         payloadStreamingUnsignedTrail,
			"Content-Encoding":             "aws-chunked",
			"X-Amz-Decoded-Content-Length": fmt.Sprint(len(data)),
		}
		if trailer != "" {
			m["X-Amz-Trailer"] = trailer
		}
		return m
	}

	// Valid trailer.
	body := chunkedWithTrailer(data, "x-amz-checksum-crc32:"+crc)
	got, g, berr := readUpload(t, newUploadCtx(body, hdr("x-amz-checksum-crc32"), int64(len(body))))
	if berr != nil || !g.Complete() || string(got) != data {
		t.Fatalf("valid trailer: berr=%+v err=%v got=%q", berr, g.Err(), got)
	}

	// Wrong trailer value → BadDigest.
	body = chunkedWithTrailer(data, "x-amz-checksum-crc32:"+b64sum(crc32.NewIEEE(), "other"))
	_, g, _ = readUpload(t, newUploadCtx(body, hdr("x-amz-checksum-crc32"), int64(len(body))))
	if code, _, _, _ := bodyFailure(g); code != "BadDigest" || g.Complete() {
		t.Errorf("wrong trailer: code=%q complete=%v", code, g.Complete())
	}

	// Declared but missing trailer → rejected.
	body = chunkedWithTrailer(data)
	_, g, _ = readUpload(t, newUploadCtx(body, hdr("x-amz-checksum-crc32"), int64(len(body))))
	if code, _, _, _ := bodyFailure(g); code != "InvalidRequest" || g.Complete() {
		t.Errorf("missing trailer: code=%q complete=%v", code, g.Complete())
	}

	// Undeclared checksum trailer → rejected rather than silently ignored.
	body = chunkedWithTrailer(data, "x-amz-checksum-crc32:"+crc)
	_, g, _ = readUpload(t, newUploadCtx(body, hdr(""), int64(len(body))))
	if code, _, _, _ := bodyFailure(g); code != "InvalidRequest" || g.Complete() {
		t.Errorf("undeclared trailer: code=%q complete=%v", code, g.Complete())
	}

	// Unsupported trailer algorithm → 400 up front.
	_, _, berr = readUpload(t, newUploadCtx(body, hdr("x-amz-checksum-md4"), int64(len(body))))
	if berr == nil || berr.status != http.StatusBadRequest {
		t.Errorf("unsupported trailer algorithm: %+v", berr)
	}
}

// ── aws-sdk-go-v2 round trip ────────────────────────────────────────────────

// sdkUploadServer serves PUT /{bucket}/{key} through the same body pipeline
// as PutObject (prepareUploadBody → guardedBody), recording what it stored.
// Optionally corrupts the first occurrence of `corrupt` in the raw request
// body to simulate in-flight damage.
type sdkUploadServer struct {
	mu       sync.Mutex
	stored   []byte
	complete bool
	payload  string
	corrupt  byte
}

func (s *sdkUploadServer) handler() http.Handler {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.PUT("/:bucket/*key", func(c *gin.Context) {
		s.mu.Lock()
		s.payload = c.GetHeader("X-Amz-Content-Sha256")
		corrupt := s.corrupt
		s.mu.Unlock()
		if corrupt != 0 {
			c.Request.Body = io.NopCloser(&flipFirst{r: c.Request.Body, target: corrupt})
		}
		ub, berr := prepareUploadBody(c, 1<<30)
		if berr != nil {
			c.XML(berr.status, Error{Code: berr.code, Message: berr.msg})
			return
		}
		g := newGuardedBody(ub.reader, ub.declared, 1<<30, nil)
		data, _ := io.ReadAll(g)
		s.mu.Lock()
		s.stored, s.complete = data, g.Complete()
		s.mu.Unlock()
		if code, msg, status, ok := bodyFailure(g); ok {
			c.XML(status, Error{Code: code, Message: msg})
			return
		}
		c.Header("ETag", `"etag"`)
		c.Status(http.StatusOK)
	})
	return r
}

type flipFirst struct {
	r      io.Reader
	target byte
	done   bool
}

func (f *flipFirst) Read(p []byte) (int, error) {
	n, err := f.r.Read(p)
	if !f.done {
		if i := bytes.IndexByte(p[:n], f.target); i >= 0 {
			p[i]++
			f.done = true
		}
	}
	return n, err
}

// nonSeekable hides Seek so the SDK must stream (aws-chunked + trailer).
type nonSeekable struct{ io.Reader }

func newSDKClient(srv *httptest.Server) *s3.Client {
	return s3.New(s3.Options{
		Region:           "us-east-1",
		Credentials:      credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", ""),
		BaseEndpoint:     aws.String(srv.URL),
		UsePathStyle:     true,
		HTTPClient:       srv.Client(),
		RetryMaxAttempts: 1,
	})
}

func TestSDKTrailerChecksumRoundTrip(t *testing.T) {
	s := &sdkUploadServer{}
	srv := httptest.NewTLSServer(s.handler())
	defer srv.Close()
	client := newSDKClient(srv)

	data := strings.Repeat("Z", 200*1024+17) // spans several aws-chunked chunks
	algos := []types.ChecksumAlgorithm{
		types.ChecksumAlgorithmCrc32, types.ChecksumAlgorithmCrc32c,
		types.ChecksumAlgorithmSha1, types.ChecksumAlgorithmSha256,
		types.ChecksumAlgorithmCrc64nvme,
	}
	for _, algo := range algos {
		s.mu.Lock()
		s.corrupt, s.stored, s.complete = 0, nil, false
		s.mu.Unlock()
		_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket:            aws.String("b"),
			Key:               aws.String("k"),
			Body:              nonSeekable{strings.NewReader(data)},
			ContentLength:     aws.Int64(int64(len(data))),
			ChecksumAlgorithm: algo,
		})
		if err != nil {
			t.Fatalf("%s: PutObject: %v", algo, err)
		}
		s.mu.Lock()
		if !strings.HasSuffix(s.payload, "TRAILER") {
			t.Errorf("%s: SDK did not use a trailer payload (%q); test is not exercising trailers", algo, s.payload)
		}
		if !s.complete || string(s.stored) != data {
			t.Errorf("%s: server stored %d bytes complete=%v", algo, len(s.stored), s.complete)
		}
		s.mu.Unlock()

		// Corrupt one payload byte in flight: the trailer checksum no longer
		// matches, the server answers BadDigest and never completes the body.
		s.mu.Lock()
		s.corrupt, s.stored, s.complete = 'Z', nil, false
		s.mu.Unlock()
		_, err = client.PutObject(context.Background(), &s3.PutObjectInput{
			Bucket:            aws.String("b"),
			Key:               aws.String("k"),
			Body:              nonSeekable{strings.NewReader(data)},
			ContentLength:     aws.Int64(int64(len(data))),
			ChecksumAlgorithm: algo,
		})
		if err == nil || !strings.Contains(err.Error(), "BadDigest") {
			t.Errorf("%s: corrupted upload: err = %v, want BadDigest", algo, err)
		}
		s.mu.Lock()
		if s.complete {
			t.Errorf("%s: corrupted body reported complete", algo)
		}
		s.mu.Unlock()
	}
}

func TestSDKHeaderChecksumRoundTrip(t *testing.T) {
	// Seekable body over plain HTTP: the SDK sends the checksum as a request
	// header (no aws-chunked encoding).
	s := &sdkUploadServer{}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	client := newSDKClient(srv)

	data := "header checksum body"
	_, err := client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String("b"), Key: aws.String("k"),
		Body: strings.NewReader(data), ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
	})
	if err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	s.mu.Lock()
	if !s.complete || string(s.stored) != data || strings.HasPrefix(s.payload, "STREAMING-") {
		t.Fatalf("stored %q complete=%v payload=%q", s.stored, s.complete, s.payload)
	}
	s.corrupt = 'h'
	s.mu.Unlock()
	_, err = client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String("b"), Key: aws.String("k"),
		Body: strings.NewReader(data), ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
	})
	if err == nil || !strings.Contains(err.Error(), "BadDigest") {
		t.Fatalf("corrupted upload: err = %v, want BadDigest", err)
	}
}

// ── Upload idle timeout ─────────────────────────────────────────────────────

func TestUploadIdleTimeoutAbortsStalledClient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type result struct {
		n   int
		err error
	}
	results := make(chan result, 4)
	r := gin.New()
	r.PUT("/up", func(c *gin.Context) {
		applyUploadIdleTimeoutDuration(c, 300*time.Millisecond)
		n, err := io.Copy(io.Discard, c.Request.Body)
		results <- result{int(n), err}
		c.Status(http.StatusOK)
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	// A client that sends part of the body and then stalls.
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "PUT /up HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nabc")
	select {
	case res := <-results:
		if res.err == nil {
			t.Fatalf("stalled upload read %d bytes without error", res.n)
		}
		var ne net.Error
		if !errors.As(res.err, &ne) || !ne.Timeout() {
			t.Logf("stalled upload failed with %v (not a net timeout, still an abort)", res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stalled upload was not aborted by the idle timeout")
	}

	// A slow but live client (a byte every 100ms, 1.2s total) is not cut
	// off: each read extends the deadline.
	conn2, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	fmt.Fprintf(conn2, "PUT /up HTTP/1.1\r\nHost: x\r\nContent-Length: 12\r\n\r\n")
	for i := 0; i < 12; i++ {
		time.Sleep(100 * time.Millisecond)
		if _, err := conn2.Write([]byte{'x'}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case res := <-results:
		if res.err != nil || res.n != 12 {
			t.Fatalf("trickling upload: n=%d err=%v", res.n, res.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("trickling upload did not finish")
	}
}

package api

import (
	"bufio"
	"crypto/hmac"
	"crypto/md5"  //nolint:gosec // Content-MD5 integrity check mandated by the S3 API, not a security primitive
	"crypto/sha1" //nolint:gosec // x-amz-checksum-sha1 integrity check mandated by the S3 API
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"hash/crc64"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Request-body plumbing for object writes: aws-chunked decoding with SigV4
// chunk-signature verification, exact-length / max-size / quota enforcement,
// and an EOF gate that never hands the storage backend the final byte of a
// body until every check has passed. The gate matters for the S3 backend: an
// HTTP PUT whose declared Content-Length has been fully sent may be committed
// by the upstream server even if we fail afterwards, so failures must surface
// BEFORE the last byte leaves.

var (
	errBodyTooLarge       = errors.New("request body exceeds the maximum allowed size")
	errBodyLengthMismatch = errors.New("request body length does not match the declared length")
	errChunkSignature     = errors.New("aws-chunked: chunk signature does not match")
	errChunkMalformed     = errors.New("aws-chunked: malformed chunk encoding")
)

const (
	payloadStreamingSigned        = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	payloadStreamingSignedTrailer = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	payloadStreamingUnsignedTrail = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"

	awsChunkMaxLine       = 4096                   // chunk-size / trailer line bound
	awsChunkMaxSize       = 5 * 1024 * 1024 * 1024 // no single chunk may claim more than 5 GiB
	awsChunkMaxTrailers   = 32
	emptySHA256Hex        = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	streamingChunkAlgo    = "AWS4-HMAC-SHA256-PAYLOAD"
	streamingTrailingAlgo = "AWS4-HMAC-SHA256-TRAILER"
)

// chunkSigningContext carries the values the SigV4 middleware exposes after a
// successful header-authenticated request; chunk signatures chain from the
// request's seed signature.
type chunkSigningContext struct {
	key     []byte
	seedSig string
	amzDate string
	scope   string
}

func chunkSigningFromContext(c *gin.Context) (*chunkSigningContext, bool) {
	kv, ok1 := c.Get("sigv4_signing_key")
	sv, ok2 := c.Get("sigv4_seed_signature")
	dv, ok3 := c.Get("sigv4_amz_date")
	scv, ok4 := c.Get("sigv4_scope")
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, false
	}
	key, _ := kv.([]byte)
	seed, _ := sv.(string)
	date, _ := dv.(string)
	scope, _ := scv.(string)
	if len(key) == 0 || seed == "" || date == "" || scope == "" {
		return nil, false
	}
	return &chunkSigningContext{key: key, seedSig: seed, amzDate: date, scope: scope}, true
}

// chunkMode selects how an aws-chunked body is decoded/verified.
type chunkMode int

const (
	chunkModeUnsigned        chunkMode = iota // legacy/unsigned chunks, extensions ignored
	chunkModeSigned                           // STREAMING-AWS4-HMAC-SHA256-PAYLOAD
	chunkModeSignedTrailer                    // ... -PAYLOAD-TRAILER (signed chunks + signed trailer)
	chunkModeUnsignedTrailer                  // STREAMING-UNSIGNED-PAYLOAD-TRAILER
)

// awsChunkedReader decodes Content-Encoding: aws-chunked bodies.
//
//	chunk   = hex-size [";chunk-signature=" sig] CRLF data CRLF
//	final   = "0" [";chunk-signature=" sig] CRLF [trailers] CRLF
//
// In signed modes each chunk's signature is verified (chained from the seed
// signature); data is streamed out as it arrives and a mismatch is reported
// as an error at the chunk's end — callers must wrap the reader in a
// guardedBody so no write commits before the whole stream has verified.
type awsChunkedReader struct {
	r    *bufio.Reader
	mode chunkMode
	sig  *chunkSigningContext

	prevSig   string
	remaining int64
	expectSig string
	hasher    hash.Hash
	trailers  map[string]string // trailing headers (lower-case names), set at the end
	done      bool
	err       error
}

func newAWSChunkedReaderMode(r io.Reader, mode chunkMode, sig *chunkSigningContext) *awsChunkedReader {
	a := &awsChunkedReader{r: bufio.NewReaderSize(r, 64*1024), mode: mode, sig: sig}
	if sig != nil {
		a.prevSig = sig.seedSig
	}
	if a.signed() {
		a.hasher = sha256.New()
	}
	return a
}

func (a *awsChunkedReader) signed() bool {
	return a.mode == chunkModeSigned || a.mode == chunkModeSignedTrailer
}

func (a *awsChunkedReader) hasTrailer() bool {
	return a.mode == chunkModeSignedTrailer || a.mode == chunkModeUnsignedTrailer
}

// readLine reads one CRLF/LF-terminated line (terminator stripped), bounded
// by awsChunkMaxLine. EOF before any byte is returned as io.EOF; EOF inside a
// line as io.ErrUnexpectedEOF.
func (a *awsChunkedReader) readLine() (string, error) {
	var line []byte
	for {
		frag, err := a.r.ReadSlice('\n')
		line = append(line, frag...)
		if len(line) > awsChunkMaxLine {
			return "", errChunkMalformed
		}
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return "", io.EOF
			}
			return "", io.ErrUnexpectedEOF
		}
		return "", err
	}
	s := strings.TrimSuffix(string(line), "\n")
	return strings.TrimSuffix(s, "\r"), nil
}

func (a *awsChunkedReader) chunkStringToSign(dataHash string) string {
	return streamingChunkAlgo + "\n" + a.sig.amzDate + "\n" + a.sig.scope + "\n" +
		a.prevSig + "\n" + emptySHA256Hex + "\n" + dataHash
}

func (a *awsChunkedReader) sign(stringToSign string) string {
	m := hmac.New(sha256.New, a.sig.key)
	m.Write([]byte(stringToSign))
	return hex.EncodeToString(m.Sum(nil))
}

// verifyChunk checks the signature of the chunk just completed.
func (a *awsChunkedReader) verifyChunk() error {
	if !a.signed() {
		return nil
	}
	dataHash := hex.EncodeToString(a.hasher.Sum(nil))
	a.hasher.Reset()
	want := a.sign(a.chunkStringToSign(dataHash))
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(a.expectSig))) {
		return errChunkSignature
	}
	a.prevSig = want
	return nil
}

// readHeader parses the next chunk header into remaining/expectSig.
func (a *awsChunkedReader) readHeader() error {
	line, err := a.readLine()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF // stream ended without the final chunk
		}
		return err
	}
	sizePart, ext, _ := strings.Cut(line, ";")
	sizePart = strings.TrimSpace(sizePart)
	if sizePart == "" || len(sizePart) > 16 {
		return errChunkMalformed
	}
	size, perr := strconv.ParseInt(sizePart, 16, 64)
	if perr != nil || size < 0 || size > awsChunkMaxSize {
		return errChunkMalformed
	}
	a.expectSig = ""
	if a.signed() {
		for _, kv := range strings.Split(ext, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
			if k == "chunk-signature" {
				a.expectSig = v
			}
		}
		if len(a.expectSig) != 64 {
			return errChunkSignature
		}
	}
	a.remaining = size
	if size == 0 {
		if err := a.verifyChunk(); err != nil {
			return err
		}
		if err := a.readTrailers(); err != nil {
			return err
		}
		a.done = true
	}
	return nil
}

// readTrailers consumes everything after the final chunk: optional trailing
// headers (verified in the signed-trailer mode) and the closing blank line.
func (a *awsChunkedReader) readTrailers() error {
	var canonical strings.Builder
	trailerSig := ""
	for i := 0; ; i++ {
		if i > awsChunkMaxTrailers {
			return errChunkMalformed
		}
		line, err := a.readLine()
		if errors.Is(err, io.EOF) {
			break // tolerate a missing closing CRLF
		}
		if err != nil {
			return err
		}
		if line == "" {
			break
		}
		if !a.hasTrailer() {
			return errChunkMalformed // data after the final chunk
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return errChunkMalformed
		}
		name = strings.ToLower(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		if name == "x-amz-trailer-signature" {
			trailerSig = value
			continue
		}
		if a.trailers == nil {
			a.trailers = map[string]string{}
		}
		a.trailers[name] = value
		canonical.WriteString(name + ":" + value + "\n")
	}
	if a.mode == chunkModeSignedTrailer {
		if trailerSig == "" {
			return errChunkSignature
		}
		sum := sha256.Sum256([]byte(canonical.String()))
		sts := streamingTrailingAlgo + "\n" + a.sig.amzDate + "\n" + a.sig.scope + "\n" +
			a.prevSig + "\n" + hex.EncodeToString(sum[:])
		if !hmac.Equal([]byte(a.sign(sts)), []byte(strings.ToLower(trailerSig))) {
			return errChunkSignature
		}
	}
	return nil
}

func (a *awsChunkedReader) Read(p []byte) (int, error) {
	if a.err != nil {
		return 0, a.err
	}
	if a.done {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	for a.remaining == 0 {
		if err := a.readHeader(); err != nil {
			a.err = err
			return 0, err
		}
		if a.done {
			return 0, io.EOF
		}
	}
	toRead := int64(len(p))
	if toRead > a.remaining {
		toRead = a.remaining
	}
	n, err := a.r.Read(p[:toRead])
	if n > 0 && a.hasher != nil {
		a.hasher.Write(p[:n])
	}
	a.remaining -= int64(n)
	if a.remaining == 0 {
		// Chunk data must be followed by exactly CRLF.
		var crlf [2]byte
		if _, cerr := io.ReadFull(a.r, crlf[:]); cerr != nil || crlf != [2]byte{'\r', '\n'} {
			if cerr != nil && !errors.Is(cerr, io.EOF) && !errors.Is(cerr, io.ErrUnexpectedEOF) {
				a.err = cerr
			} else {
				a.err = errChunkMalformed
			}
			return n, a.err
		}
		if verr := a.verifyChunk(); verr != nil {
			a.err = verr
			return n, verr
		}
	}
	if err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // EOF inside a chunk: truncated body
		}
		a.err = err
		return n, err
	}
	return n, nil
}

// guardedBody enforces, in one pass over an upload body:
//   - a hard size cap (max),
//   - an exact decoded length (expected; -1 = unknown) — no silent truncation,
//   - the bucket quota (reservation grows with bytes read; authoritative
//     re-check at EOF),
//
// and holds back the final byte until the source reached EOF and all of the
// above passed. Any failure is sticky and returned from every later Read, so a
// consumer can never observe a complete body that failed verification.
type guardedBody struct {
	src      io.Reader
	expected int64
	max      int64
	quota    *quotaReservation

	n    int64
	buf  []byte
	pend []byte
	eof  bool
	err  error
}

func newGuardedBody(src io.Reader, expected, max int64, quota *quotaReservation) *guardedBody {
	return &guardedBody{src: src, expected: expected, max: max, quota: quota}
}

// Err returns the verification/read error that stopped the body, if any.
func (g *guardedBody) Err() error {
	if g == nil {
		return nil
	}
	return g.err
}

// Complete reports whether the whole body was consumed and verified.
func (g *guardedBody) Complete() bool {
	return g != nil && g.eof && g.err == nil && len(g.pend) == 0
}

// Size is the number of body bytes read so far.
func (g *guardedBody) Size() int64 { return g.n }

func (g *guardedBody) account(m int64) error {
	g.n += m
	if g.max > 0 && g.n > g.max {
		return errBodyTooLarge
	}
	if g.expected >= 0 && g.n > g.expected {
		return errBodyLengthMismatch
	}
	return g.quota.ensure(g.n)
}

func (g *guardedBody) finish() error {
	if g.expected >= 0 && g.n != g.expected {
		return errBodyLengthMismatch
	}
	return g.quota.finalize(g.n)
}

func (g *guardedBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if g.err != nil {
			return 0, g.err
		}
		if g.eof {
			if len(g.pend) == 0 {
				return 0, io.EOF
			}
			n := copy(p, g.pend)
			g.pend = g.pend[n:]
			return n, nil
		}
		if len(g.pend) > 1 {
			n := copy(p, g.pend[:len(g.pend)-1])
			g.pend = g.pend[n:]
			return n, nil
		}
		// At most one (held-back) byte is pending: refill from the source.
		if g.buf == nil {
			g.buf = make([]byte, 64*1024)
		}
		off := 0
		if len(g.pend) == 1 {
			g.buf[0] = g.pend[0]
			off = 1
		}
		m, err := g.src.Read(g.buf[off:])
		if m > 0 {
			if aerr := g.account(int64(m)); aerr != nil {
				g.err = aerr
				return 0, aerr
			}
		}
		g.pend = g.buf[:off+m]
		if err != nil {
			if errors.Is(err, io.EOF) {
				if ferr := g.finish(); ferr != nil {
					g.err = ferr
					return 0, ferr
				}
				g.eof = true
				continue
			}
			g.err = err
			return 0, err
		}
	}
}

// s3BodyError is an S3-style error selected while preparing a request body.
type s3BodyError struct {
	code   string
	msg    string
	status int
}

// uploadBody is a prepared, decoded object/part body.
type uploadBody struct {
	reader   io.Reader // decoded (not yet guarded) body
	declared int64     // decoded length the client declared (>= 0)
}

// hasAWSChunkedEncoding reports whether Content-Encoding lists aws-chunked
// (it may be combined with other codings, e.g. "aws-chunked,gzip").
func hasAWSChunkedEncoding(h http.Header) bool {
	for _, v := range h.Values("Content-Encoding") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "aws-chunked") {
				return true
			}
		}
	}
	return false
}

func parseDecodedLength(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 19 {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// prepareUploadBody selects the decoder for a PutObject/UploadPart body and
// determines its authoritative decoded length:
//   - aws-chunked (Content-Encoding or a STREAMING-* payload hash): the
//     length is X-Amz-Decoded-Content-Length (required, as on AWS) — never
//     the encoded Content-Length; signed streaming payloads have every chunk
//     signature verified (and are refused when the request was not
//     header-signed, e.g. presigned, since there is no seed signature);
//   - otherwise: Content-Length (required).
//
// The declared length is enforced against the actual bytes by guardedBody.
// Content-MD5 and x-amz-checksum-* values (request headers, or checksum
// trailers announced in X-Amz-Trailer for *-TRAILER payloads) are verified
// against the decoded bytes before EOF is reported (BadDigest on mismatch).
// The body also gets the upload inactivity bound (applyUploadIdleTimeout).
func prepareUploadBody(c *gin.Context, maxSize int64) (*uploadBody, *s3BodyError) {
	applyUploadIdleTimeout(c)
	payloadHash := c.GetHeader("X-Amz-Content-Sha256")
	streaming := strings.HasPrefix(payloadHash, "STREAMING-")
	if streaming || hasAWSChunkedEncoding(c.Request.Header) {
		declared, ok := parseDecodedLength(c.GetHeader("X-Amz-Decoded-Content-Length"))
		if !ok {
			return nil, &s3BodyError{"MissingContentLength", "aws-chunked uploads must include a valid X-Amz-Decoded-Content-Length header", http.StatusLengthRequired}
		}
		if maxSize > 0 && declared > maxSize {
			return nil, &s3BodyError{"EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size", http.StatusRequestEntityTooLarge}
		}
		mode := chunkModeUnsigned
		switch {
		case payloadHash == payloadStreamingSigned:
			mode = chunkModeSigned
		case payloadHash == payloadStreamingSignedTrailer:
			mode = chunkModeSignedTrailer
		case payloadHash == payloadStreamingUnsignedTrail:
			mode = chunkModeUnsignedTrailer
		case streaming:
			// e.g. STREAMING-AWS4-ECDSA-P256-SHA256-PAYLOAD (SigV4a)
			return nil, &s3BodyError{"NotImplemented", "Unsupported streaming payload signing mode", http.StatusNotImplemented}
		}
		var sig *chunkSigningContext
		if mode == chunkModeSigned || mode == chunkModeSignedTrailer {
			s, ok := chunkSigningFromContext(c)
			if !ok {
				return nil, &s3BodyError{"AccessDenied", "Signed streaming payloads require header-based SigV4 authentication", http.StatusForbidden}
			}
			sig = s
		}
		chunked := newAWSChunkedReaderMode(c.Request.Body, mode, sig)
		r, cerr := wrapChecksums(c.Request.Header, chunked, chunked)
		if cerr != nil {
			return nil, cerr
		}
		return &uploadBody{reader: r, declared: declared}, nil
	}

	declared := c.Request.ContentLength
	if declared < 0 {
		if v, ok := parseDecodedLength(c.GetHeader("X-Amz-Decoded-Content-Length")); ok {
			declared = v
		}
	}
	if declared < 0 {
		return nil, &s3BodyError{"MissingContentLength", "You must provide the Content-Length HTTP header", http.StatusLengthRequired}
	}
	if maxSize > 0 && declared > maxSize {
		return nil, &s3BodyError{"EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size", http.StatusRequestEntityTooLarge}
	}
	r, cerr := wrapChecksums(c.Request.Header, c.Request.Body, nil)
	if cerr != nil {
		return nil, cerr
	}
	return &uploadBody{reader: r, declared: declared}, nil
}

// bodyFailure maps a guardedBody failure to an S3 error (ok=false when the
// body itself did not fail, i.e. the backend failed for another reason).
func bodyFailure(g *guardedBody) (code, msg string, status int, ok bool) {
	if g == nil || g.Err() == nil {
		return "", "", 0, false
	}
	err := g.Err()
	var qe *quotaExceededError
	switch {
	case errors.As(err, &qe):
		return "QuotaExceeded", qe.Error(), http.StatusForbidden, true
	case errors.Is(err, errBodyTooLarge):
		return "EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size", http.StatusRequestEntityTooLarge, true
	case errors.Is(err, errBodyLengthMismatch), errors.Is(err, io.ErrUnexpectedEOF):
		return "IncompleteBody", "You did not provide the number of bytes specified by the Content-Length HTTP header", http.StatusBadRequest, true
	case errors.Is(err, errChunkSignature):
		return "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided", http.StatusForbidden, true
	case errors.Is(err, errChunkMalformed):
		return "InvalidRequest", "Malformed aws-chunked request body", http.StatusBadRequest, true
	case errors.Is(err, errChecksumMismatch):
		return "BadDigest", err.Error(), http.StatusBadRequest, true
	case errors.Is(err, errChecksumMalformed):
		return "InvalidRequest", err.Error(), http.StatusBadRequest, true
	default:
		// Includes the SigV4 middleware's payload-hash mismatch error.
		return "BadDigest", fmt.Sprintf("Request body could not be verified: %v", err), http.StatusBadRequest, true
	}
}

// errRequestBodyTooLarge is returned by readBoundedBody for oversized bodies.
var errRequestBodyTooLarge = errors.New("request body too large")

// readBoundedBody reads a small request body (XML documents) completely. It
// fails instead of silently truncating when the body exceeds max, which also
// guarantees the underlying reader reached EOF — where the SigV4 middleware's
// payload-hash check reports a mismatch. A truncated read would act on bytes
// whose integrity was never verified.
func readBoundedBody(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errRequestBodyTooLarge
	}
	return data, nil
}

// ── Upload inactivity bound ─────────────────────────────────────────────────

// uploadIdleTimeout bounds how long an upload body may go without delivering
// any bytes. Object writes hold the key's write lock (and a quota
// reservation) for the whole transfer; without this bound a stalled or
// trickling client would hold them — and stall lifecycle/replication sweeps
// and moves of that key — indefinitely. There is deliberately no total
// deadline: a large upload on a slow but live link keeps extending it.
const uploadIdleTimeout = 60 * time.Second

// idleDeadlineBody extends the connection read deadline before every Read of
// the request body, so a client that stops sending fails the Read with a
// timeout after uploadIdleTimeout. The deadline is cleared once the body is
// finished (EOF, error or Close) so it cannot affect the connection after
// the upload — net/http starts a background read at body EOF, and a stale
// deadline there would cancel the request context.
type idleDeadlineBody struct {
	io.ReadCloser
	rc      *http.ResponseController
	idle    time.Duration
	enabled bool
	cleared bool
}

func (b *idleDeadlineBody) Read(p []byte) (int, error) {
	if b.enabled {
		if err := b.rc.SetReadDeadline(time.Now().Add(b.idle)); err != nil {
			// e.g. http.ErrNotSupported behind a ResponseWriter wrapper
			// without Unwrap, or a test recorder: no bound, but no failure.
			b.enabled = false
		}
	}
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.clear()
	}
	return n, err
}

func (b *idleDeadlineBody) Close() error {
	b.clear()
	return b.ReadCloser.Close()
}

func (b *idleDeadlineBody) clear() {
	if b.enabled && !b.cleared {
		b.cleared = true
		_ = b.rc.SetReadDeadline(time.Time{})
	}
}

// applyUploadIdleTimeout wraps c.Request.Body so an upload that delivers no
// bytes for uploadIdleTimeout fails with a read timeout (releasing the object
// lock and quota reservation its handler holds). Call it before the body is
// read — the S3 PutObject/UploadPart path does so in prepareUploadBody; the
// console upload path should call it before parsing the multipart form.
// Idempotent. Response writers inserted by middleware must implement
// Unwrap() http.ResponseWriter for the deadline to reach the connection;
// otherwise the body is left unbounded (never an error).
func applyUploadIdleTimeout(c *gin.Context) {
	applyUploadIdleTimeoutDuration(c, uploadIdleTimeout)
}

func applyUploadIdleTimeoutDuration(c *gin.Context, idle time.Duration) {
	if c.Request == nil || c.Request.Body == nil || c.Request.Body == http.NoBody {
		return
	}
	if _, done := c.Request.Body.(*idleDeadlineBody); done {
		return
	}
	c.Request.Body = &idleDeadlineBody{
		ReadCloser: c.Request.Body,
		rc:         http.NewResponseController(c.Writer),
		idle:       idle,
		enabled:    true,
	}
}

// ── Payload checksums (Content-MD5, x-amz-checksum-*) ───────────────────────

var (
	errChecksumMismatch  = errors.New("payload checksum does not match")
	errChecksumMalformed = errors.New("malformed or undeclared payload checksum")
)

// checksumMismatchError names the checksum that failed (for the S3 message).
type checksumMismatchError struct{ name string }

func (e *checksumMismatchError) Error() string {
	return fmt.Sprintf("the %s you specified did not match the calculated checksum", e.name)
}
func (e *checksumMismatchError) Unwrap() error { return errChecksumMismatch }

// crc64NVMETable is CRC-64/NVME (poly 0xAD93D23594C93659, reflected form
// 0x9A6C9329AC4BC9B5, init/xorout all-ones — which is what hash/crc64
// applies). Verified against the catalogue check value in tests.
var crc64NVMETable = crc64.MakeTable(0x9a6c9329ac4bc9b5)

// checksumAlgorithms maps the lower-case x-amz-checksum-* header name to its
// hash constructor and display name.
var checksumAlgorithms = map[string]struct {
	display string
	newHash func() hash.Hash
}{
	"x-amz-checksum-crc32":     {"CRC32", func() hash.Hash { return crc32.NewIEEE() }},
	"x-amz-checksum-crc32c":    {"CRC32C", func() hash.Hash { return crc32.New(crc32.MakeTable(crc32.Castagnoli)) }},
	"x-amz-checksum-crc64nvme": {"CRC64NVME", func() hash.Hash { return crc64.New(crc64NVMETable) }},
	"x-amz-checksum-sha1":      {"SHA1", sha1.New},
	"x-amz-checksum-sha256":    {"SHA256", sha256.New},
}

// bodyChecksum is one checksum computed over the decoded body.
type bodyChecksum struct {
	header  string // lower-case header / trailer name ("content-md5" for MD5)
	display string
	h       hash.Hash
	want    []byte // nil until known (trailer checksums arrive at the end)
}

// checksumReader hashes the decoded body and, when the source reaches EOF,
// compares every checksum against the expected value (from request headers,
// or from the aws-chunked trailers). A mismatch is returned INSTEAD of EOF,
// so the guardedBody wrapping it never releases the final byte and the
// backend cannot commit the object.
type checksumReader struct {
	src      io.Reader
	sums     []*bodyChecksum
	trailers *awsChunkedReader // source of trailer values (nil = none)
	declared map[string]bool   // checksum trailers announced in X-Amz-Trailer
	err      error
}

func (r *checksumReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	n, err := r.src.Read(p)
	if n > 0 {
		for _, s := range r.sums {
			s.h.Write(p[:n])
		}
	}
	if errors.Is(err, io.EOF) {
		if verr := r.verify(); verr != nil {
			r.err = verr
			return n, verr
		}
	}
	return n, err
}

func (r *checksumReader) verify() error {
	if r.trailers != nil {
		for name := range r.trailers.trailers {
			if strings.HasPrefix(name, "x-amz-checksum-") && !r.declared[name] {
				return fmt.Errorf("%w: trailer %s was not declared in X-Amz-Trailer", errChecksumMalformed, name)
			}
		}
		for _, s := range r.sums {
			if s.want != nil {
				continue
			}
			v, ok := r.trailers.trailers[s.header]
			if !ok {
				return fmt.Errorf("%w: declared trailer %s is missing", errChecksumMalformed, s.header)
			}
			want, derr := decodeChecksumValue(v, s.h.Size())
			if derr != nil {
				return fmt.Errorf("%w: %s", errChecksumMalformed, s.header)
			}
			s.want = want
		}
	}
	for _, s := range r.sums {
		if s.want == nil || !hmac.Equal(s.h.Sum(nil), s.want) {
			return &checksumMismatchError{name: s.display}
		}
	}
	return nil
}

func decodeChecksumValue(v string, size int) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(v))
	if err != nil || len(b) != size {
		return nil, errChecksumMalformed
	}
	return b, nil
}

// wrapChecksums adds Content-MD5 / x-amz-checksum-* verification (header
// values, and in trailer modes the checksum trailers announced in
// X-Amz-Trailer) around the decoded body. chunked is the aws-chunked decoder
// when the body is aws-chunked (nil otherwise).
func wrapChecksums(h http.Header, decoded io.Reader, chunked *awsChunkedReader) (io.Reader, *s3BodyError) {
	var sums []*bodyChecksum
	if v := h.Get("Content-MD5"); v != "" {
		want, err := decodeChecksumValue(v, md5.Size)
		if err != nil {
			return nil, &s3BodyError{"InvalidDigest", "The Content-MD5 you specified was invalid.", http.StatusBadRequest}
		}
		sums = append(sums, &bodyChecksum{header: "content-md5", display: "Content-MD5", h: md5.New(), want: want}) //nolint:gosec // G401: Content-MD5 is the S3 wire format, integrity only
	}
	for name, alg := range checksumAlgorithms {
		v := h.Get(name)
		if v == "" {
			continue
		}
		hh := alg.newHash()
		want, err := decodeChecksumValue(v, hh.Size())
		if err != nil {
			return nil, &s3BodyError{"InvalidRequest", fmt.Sprintf("Value for %s header is invalid.", name), http.StatusBadRequest}
		}
		sums = append(sums, &bodyChecksum{header: name, display: alg.display, h: hh, want: want})
	}
	var declared map[string]bool
	if chunked != nil && chunked.hasTrailer() {
		declared = map[string]bool{}
		for _, v := range h.Values("X-Amz-Trailer") {
			for _, tok := range strings.Split(v, ",") {
				name := strings.ToLower(strings.TrimSpace(tok))
				if !strings.HasPrefix(name, "x-amz-checksum-") {
					continue
				}
				alg, ok := checksumAlgorithms[name]
				if !ok {
					return nil, &s3BodyError{"InvalidRequest", fmt.Sprintf("Unsupported checksum trailer %s", name), http.StatusBadRequest}
				}
				if declared[name] {
					continue
				}
				declared[name] = true
				sums = append(sums, &bodyChecksum{header: name, display: alg.display, h: alg.newHash()})
			}
		}
	} else {
		chunked = nil
	}
	if len(sums) == 0 && chunked == nil {
		return decoded, nil
	}
	// Trailer mode always goes through the reader so undeclared checksum
	// trailers are rejected rather than silently ignored.
	return &checksumReader{src: decoded, sums: sums, trailers: chunked, declared: declared}, nil
}

package middleware

import (
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// SigV4 constants.
const (
	sigV4Algorithm  = "AWS4-HMAC-SHA256"
	sigV4TimeFormat = "20060102T150405Z"
	sigV4MaxSkew    = 15 * time.Minute
	sigV4MaxPresign = 604800 // 7 days, per AWS
	unsignedPayload = "UNSIGNED-PAYLOAD"
)

// Gin context keys set after successful HEADER-based SigV4 verification, so
// the streaming (aws-chunked) body decoder can verify per-chunk signatures.
const (
	CtxSigV4SigningKey    = "sigv4_signing_key"    // []byte: derived signing key
	CtxSigV4SeedSignature = "sigv4_seed_signature" // string: request (seed) signature, hex
	CtxSigV4AmzDate       = "sigv4_amz_date"       // string: X-Amz-Date
	CtxSigV4Scope         = "sigv4_scope"          // string: "YYYYMMDD/region/s3/aws4_request"
)

// ErrContentSHA256Mismatch is returned by the request body reader (at the end
// of the payload) when the body does not hash to the value the client signed in
// X-Amz-Content-Sha256. Write paths must abort on any body read error; callers
// may use errors.Is to map it to the S3 "XAmzContentSHA256Mismatch" code.
var ErrContentSHA256Mismatch = errors.New("XAmzContentSHA256Mismatch: the provided 'x-amz-content-sha256' header does not match what was computed")

// streamingPayloadHashes are the X-Amz-Content-Sha256 sentinel values whose
// body integrity is enforced by the aws-chunked decoder (per-chunk signatures
// or trailers), not by a whole-body digest.
var streamingPayloadHashes = map[string]bool{
	"STREAMING-AWS4-HMAC-SHA256-PAYLOAD":         true,
	"STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER": true,
	"STREAMING-UNSIGNED-PAYLOAD-TRAILER":         true,
}

// S3AuthMiddleware validates AWS Signature Version 4 authentication.
// Supports both header-based auth (standard API) and query-string auth (presigned URLs).
func S3AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")

		// Detect presigned URL: no Authorization header but X-Amz-Algorithm query param present
		if authHeader == "" && c.Query("X-Amz-Algorithm") != "" {
			authenticatePresigned(c)
			return
		}

		if authHeader == "" {
			c.Header("WWW-Authenticate", sigV4Algorithm)
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"Code":    "AccessDenied",
				"Message": "Missing authorization header",
			})
			return
		}

		auth, err := parseAuthorizationHeader(authHeader)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"Code":    "InvalidArgument",
				"Message": err.Error(),
			})
			return
		}

		// The payload hash is part of the signed canonical request; reject
		// values we could never verify before doing any key lookup.
		payloadHash := c.GetHeader("X-Amz-Content-Sha256")
		if !validPayloadHashValue(payloadHash) {
			c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
				"Code":    "InvalidArgument",
				"Message": "x-amz-content-sha256 must be UNSIGNED-PAYLOAD, a supported STREAMING-* value, or a hex SHA-256 digest",
			})
			return
		}

		key, secretKey, ok := lookupAndDecryptKey(c, auth.AccessKey)
		if !ok {
			return
		}

		signingKey, err := verifyHeaderSignature(c.Request, auth, secretKey, time.Now())
		if err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"Code":    "SignatureDoesNotMatch",
				"Message": "The request signature we calculated does not match the signature you provided",
			})
			return
		}

		// Bind the body to the signed digest (contract: hex hash → verified
		// while read; streaming values are verified by the chunk decoder).
		if isHexSHA256(payloadHash) {
			if !wrapBodyWithDigestCheck(c.Request, payloadHash) {
				c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
					"Code":    "XAmzContentSHA256Mismatch",
					"Message": "The provided 'x-amz-content-sha256' header does not match what was computed.",
				})
				return
			}
		}

		c.Set(CtxSigV4SigningKey, signingKey)
		c.Set(CtxSigV4SeedSignature, auth.Signature)
		c.Set(CtxSigV4AmzDate, auth.Date)
		c.Set(CtxSigV4Scope, auth.Scope)

		updateLastUsed(key)
		c.Set("user_id", key.UserID)
		c.Set("user", &key.User)
		c.Set("is_admin", key.User.IsAdmin)
		c.Next()
	}
}

// authenticatePresigned handles query-string SigV4 authentication for presigned URLs
func authenticatePresigned(c *gin.Context) {
	q, err := url.ParseQuery(c.Request.URL.RawQuery)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"Code": "AuthorizationQueryParametersError", "Message": "Malformed query string"})
		return
	}
	if q.Get("X-Amz-Algorithm") != sigV4Algorithm {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"Code": "InvalidArgument", "Message": "Unsupported algorithm"})
		return
	}

	accessKey, scope, err := splitCredential(q.Get("X-Amz-Credential"))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"Code": "AuthorizationQueryParametersError", "Message": "Invalid X-Amz-Credential"})
		return
	}

	dateStr := q.Get("X-Amz-Date")
	expiresStr := q.Get("X-Amz-Expires")
	if dateStr == "" || expiresStr == "" {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"Code": "AuthorizationQueryParametersError", "Message": "Missing X-Amz-Date or X-Amz-Expires"})
		return
	}
	requestTime, err := time.Parse(sigV4TimeFormat, dateStr)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"Code": "AuthorizationQueryParametersError", "Message": "Invalid X-Amz-Date"})
		return
	}
	expiresSecs, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil || expiresSecs <= 0 || expiresSecs > sigV4MaxPresign {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"Code": "AuthorizationQueryParametersError", "Message": "Invalid X-Amz-Expires"})
		return
	}
	if err := checkPresignWindow(requestTime, expiresSecs, time.Now()); err != nil {
		code := "AccessDenied"
		if errors.Is(err, errPresignExpired) {
			code = "ExpiredToken"
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"Code": code, "Message": err.Error()})
		return
	}
	if err := checkScope(scope, dateStr); err != nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"Code": "AuthorizationQueryParametersError", "Message": err.Error()})
		return
	}

	key, secretKey, ok := lookupAndDecryptKey(c, accessKey)
	if !ok {
		return
	}

	if err := verifyPresignedSignature(c.Request, q, secretKey); err != nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"Code":    "SignatureDoesNotMatch",
			"Message": "The request signature we calculated does not match the signature you provided",
		})
		return
	}

	updateLastUsed(key)
	c.Set("user_id", key.UserID)
	c.Set("user", &key.User)
	c.Set("is_admin", key.User.IsAdmin)
	c.Next()
}

var errPresignExpired = errors.New("request has expired")

// checkPresignWindow rejects presigned URLs that have expired or whose
// X-Amz-Date lies in the future. Without the future check a client could sign
// with a far-future date and obtain a URL that is valid (almost) forever.
func checkPresignWindow(requestTime time.Time, expiresSecs int64, now time.Time) error {
	now = now.UTC()
	if requestTime.After(now.Add(sigV4MaxSkew)) {
		return fmt.Errorf("X-Amz-Date is too far in the future")
	}
	if now.After(requestTime.Add(time.Duration(expiresSecs) * time.Second)) {
		return errPresignExpired
	}
	return nil
}

// verifyPresignedSignature validates query-string SigV4 where the signature is in X-Amz-Signature query param
func verifyPresignedSignature(r *http.Request, q url.Values, secretKey string) error {
	providedSignature := q.Get("X-Amz-Signature")
	if providedSignature == "" {
		return fmt.Errorf("X-Amz-Signature missing")
	}
	_, scope, err := splitCredential(q.Get("X-Amz-Credential"))
	if err != nil {
		return err
	}
	signedHeaders := q.Get("X-Amz-SignedHeaders")
	if signedHeaders == "" {
		signedHeaders = "host"
	}
	if !signedHeadersIncludeHost(signedHeaders) {
		return fmt.Errorf("host must be a signed header")
	}
	dateStr := q.Get("X-Amz-Date")

	canonicalRequest := buildCanonicalRequest(r, q, true, signedHeaders, unsignedPayload)
	stringToSign := buildStringToSign(dateStr, scope, canonicalRequest)
	signingKey, err := deriveSigningKey(secretKey, scope)
	if err != nil {
		return err
	}
	calculated := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	if !hmac.Equal([]byte(calculated), []byte(strings.ToLower(providedSignature))) {
		return fmt.Errorf("signature mismatch")
	}
	return nil
}

// lookupAndDecryptKey fetches an access key from the DB and decrypts its secret
func lookupAndDecryptKey(c *gin.Context, accessKey string) (*models.AccessKey, string, bool) {
	var key models.AccessKey
	if err := database.DB.Where("access_key = ? AND is_active = ?", accessKey, true).
		Preload("User").First(&key).Error; err != nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"Code":    "InvalidAccessKeyId",
			"Message": "The access key ID you provided does not exist in our records",
		})
		return nil, "", false
	}

	if key.User.IsLocked {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"Code":    "InvalidAccessKeyId",
			"Message": "The AWS access key ID you provided does not exist in our records",
		})
		return nil, "", false
	}

	// Reject expired keys.
	if key.ExpiresAt != nil && time.Now().After(*key.ExpiresAt) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"Code":    "InvalidAccessKeyId",
			"Message": "The access key ID you provided has expired",
		})
		return nil, "", false
	}

	// Temporary (STS) credentials are bound to the session generation that
	// minted them: a password change, lock, refresh-token reuse detection or
	// "sign out everywhere" bumps the user's TokenVersion and thereby revokes
	// every temporary credential issued before it.
	if stsCredentialRevoked(&key) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"Code":    "InvalidAccessKeyId",
			"Message": "The access key ID you provided has been revoked",
		})
		return nil, "", false
	}

	// Read-only keys may only perform non-mutating operations.
	if key.ReadOnly {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			// allowed
		default:
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"Code":    "AccessDenied",
				"Message": "This access key is read-only",
			})
			return nil, "", false
		}
	}

	secretKey, err := security.DecryptSecretKey(key.SecretKeyEncrypted)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"Code":    "InternalError",
			"Message": "We encountered an internal error. Please try again.",
		})
		return nil, "", false
	}

	return &key, secretKey, true
}

// stsCredentialRevoked reports whether a temporary credential predates the
// user's current session generation (TokenVersion).
func stsCredentialRevoked(key *models.AccessKey) bool {
	return key.Temporary && key.IssuerTokenVersion != nil && *key.IssuerTokenVersion != key.User.TokenVersion
}

// updateLastUsed updates the access key's last-used timestamp (best-effort).
// It writes only the single column (not the whole row) and throttles to at most
// once per minute per key, to avoid write amplification / hot-row contention on
// every S3 request — especially for a shared key under load.
func updateLastUsed(key *models.AccessKey) {
	now := time.Now()
	if key.LastUsedAt != nil && now.Sub(*key.LastUsedAt) < time.Minute {
		return
	}
	key.LastUsedAt = &now
	database.DB.Model(&models.AccessKey{}).Where("id = ?", key.ID).Update("last_used_at", now)
}

// ── Authorization header ────────────────────────────────────────────────────

// sigV4Auth is a parsed "AWS4-HMAC-SHA256 Credential=..., SignedHeaders=...,
// Signature=..." header plus the request timestamp it is bound to.
type sigV4Auth struct {
	AccessKey     string
	Scope         string // YYYYMMDD/region/service/aws4_request
	SignedHeaders string
	Signature     string
	Date          string // X-Amz-Date (or Date) value used in the string to sign
}

// parseAuthorizationHeader parses the SigV4 Authorization header. Components
// may be separated by ", " or "," (both occur in the wild).
func parseAuthorizationHeader(h string) (*sigV4Auth, error) {
	if !strings.HasPrefix(h, sigV4Algorithm+" ") {
		return nil, fmt.Errorf("unsupported authorization method")
	}
	a := &sigV4Auth{}
	var credential string
	for _, part := range strings.Split(strings.TrimPrefix(h, sigV4Algorithm+" "), ",") {
		part = strings.TrimSpace(part)
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch k {
		case "Credential":
			credential = v
		case "SignedHeaders":
			a.SignedHeaders = v
		case "Signature":
			a.Signature = strings.ToLower(v)
		}
	}
	if credential == "" {
		return nil, fmt.Errorf("credential not found in authorization header")
	}
	if a.SignedHeaders == "" {
		return nil, fmt.Errorf("signed headers not found in authorization header")
	}
	if a.Signature == "" {
		return nil, fmt.Errorf("signature not found in authorization header")
	}
	var err error
	a.AccessKey, a.Scope, err = splitCredential(credential)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// splitCredential splits "ACCESS_KEY/date/region/service/aws4_request".
func splitCredential(credential string) (accessKey, scope string, err error) {
	parts := strings.Split(credential, "/")
	if len(parts) != 5 || parts[0] == "" || parts[4] != "aws4_request" {
		return "", "", fmt.Errorf("invalid credential format")
	}
	return parts[0], strings.Join(parts[1:], "/"), nil
}

// checkScope requires the credential scope's date to match the request
// timestamp and the service to be s3 (AWS rejects mismatches too).
func checkScope(scope, amzDate string) error {
	parts := strings.Split(scope, "/")
	if len(parts) != 4 {
		return fmt.Errorf("invalid credential scope")
	}
	if len(amzDate) < 8 || parts[0] != amzDate[:8] {
		return fmt.Errorf("credential scope date does not match X-Amz-Date")
	}
	if parts[2] != "s3" {
		return fmt.Errorf("credential scope service must be s3")
	}
	return nil
}

// verifyHeaderSignature validates a header-authenticated request and returns
// the derived signing key (needed to verify aws-chunked chunk signatures).
func verifyHeaderSignature(r *http.Request, a *sigV4Auth, secretKey string, now time.Time) ([]byte, error) {
	isoDate := true
	dateStr := r.Header.Get("X-Amz-Date")
	if dateStr == "" {
		dateStr = r.Header.Get("Date")
		isoDate = false
	}
	if dateStr == "" {
		return nil, fmt.Errorf("missing date header")
	}
	// Replay protection: 15-minute window per AWS spec.
	if err := validateTimestampAt(dateStr, now); err != nil {
		return nil, err
	}
	a.Date = dateStr
	if isoDate {
		if err := checkScope(a.Scope, dateStr); err != nil {
			return nil, err
		}
	}
	return computeHeaderSignature(r, a, secretKey)
}

// computeHeaderSignature recomputes the signature of a header-authenticated
// request (no timestamp checks) and compares it to the one provided.
func computeHeaderSignature(r *http.Request, a *sigV4Auth, secretKey string) ([]byte, error) {
	if !signedHeadersIncludeHost(a.SignedHeaders) {
		return nil, fmt.Errorf("host must be a signed header")
	}
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("malformed query string")
	}
	payloadHash := r.Header.Get("X-Amz-Content-Sha256")
	if payloadHash == "" {
		payloadHash = unsignedPayload
	}
	dateStr := a.Date
	if dateStr == "" {
		dateStr = r.Header.Get("X-Amz-Date")
	}
	canonicalRequest := buildCanonicalRequest(r, q, false, a.SignedHeaders, payloadHash)
	stringToSign := buildStringToSign(dateStr, a.Scope, canonicalRequest)
	signingKey, err := deriveSigningKey(secretKey, a.Scope)
	if err != nil {
		return nil, err
	}
	calculated := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	if !hmac.Equal([]byte(calculated), []byte(a.Signature)) {
		return nil, fmt.Errorf("signature mismatch")
	}
	return signingKey, nil
}

func signedHeadersIncludeHost(signedHeaders string) bool {
	for _, h := range strings.Split(signedHeaders, ";") {
		if h == "host" {
			return true
		}
	}
	return false
}

// ── Canonical request ───────────────────────────────────────────────────────

// buildCanonicalRequest builds the SigV4 canonical request. The URI and query
// are rebuilt from the request's *decoded* values re-encoded with the AWS
// rules, so a given canonical request corresponds to exactly one decoded path
// and query — the values handlers act on. For presigned requests
// X-Amz-Signature is excluded from the canonical query string.
func buildCanonicalRequest(r *http.Request, q url.Values, presigned bool, signedHeaders, payloadHash string) string {
	var sb strings.Builder
	sb.WriteString(r.Method)
	sb.WriteByte('\n')
	sb.WriteString(canonicalURI(r.URL))
	sb.WriteByte('\n')
	sb.WriteString(canonicalQueryString(q, presigned))
	sb.WriteByte('\n')
	sb.WriteString(canonicalHeaders(r, signedHeaders))
	sb.WriteByte('\n')
	sb.WriteString(signedHeaders)
	sb.WriteByte('\n')
	sb.WriteString(payloadHash)
	return sb.String()
}

// canonicalURI returns the S3 canonical URI: each path segment URI-encoded
// exactly once (S3 does not double-encode), '/' kept as the separator. It
// starts from the escaped (wire) path so an encoded "%2F" inside a segment
// stays part of that segment.
func canonicalURI(u *url.URL) string {
	raw := u.EscapedPath()
	if raw == "" {
		return "/"
	}
	segs := strings.Split(raw, "/")
	for i, s := range segs {
		if dec, err := url.PathUnescape(s); err == nil {
			s = dec
		}
		segs[i] = awsURIEncode(s, true)
	}
	out := strings.Join(segs, "/")
	if !strings.HasPrefix(out, "/") {
		out = "/" + out
	}
	return out
}

// canonicalQueryString encodes every key and value with the RFC 3986 encoder
// (space → %20) and sorts by key, then value.
func canonicalQueryString(q url.Values, presigned bool) string {
	type pair struct{ k, v string }
	pairs := make([]pair, 0, len(q))
	for k, vs := range q {
		if presigned && k == "X-Amz-Signature" {
			continue
		}
		ek := awsURIEncode(k, true)
		for _, v := range vs {
			pairs = append(pairs, pair{ek, awsURIEncode(v, true)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

// canonicalHeaders renders "name:value\n" for each signed header: values of
// repeated headers joined by ',', surrounding whitespace trimmed and inner
// runs of whitespace collapsed to one space.
func canonicalHeaders(r *http.Request, signedHeaders string) string {
	var sb strings.Builder
	for _, name := range strings.Split(signedHeaders, ";") {
		var values []string
		if name == "host" {
			values = []string{r.Host}
			if h := r.Header.Get("Host"); h != "" {
				values = []string{h}
			}
		} else {
			values = r.Header.Values(http.CanonicalHeaderKey(name))
			// net/http may keep Content-Length only in r.ContentLength.
			if name == "content-length" && len(values) == 0 && r.ContentLength >= 0 {
				values = []string{strconv.FormatInt(r.ContentLength, 10)}
			}
		}
		for i, v := range values {
			values[i] = strings.Join(strings.Fields(v), " ")
		}
		sb.WriteString(name)
		sb.WriteByte(':')
		sb.WriteString(strings.Join(values, ","))
		sb.WriteByte('\n')
	}
	return sb.String()
}

// awsURIEncode implements the SigV4 UriEncode: unreserved characters
// (A-Z a-z 0-9 - _ . ~) are kept, every other byte becomes %XX (uppercase hex).
// '/' is kept unless encodeSlash is set.
func awsURIEncode(s string, encodeSlash bool) string {
	const hexUpper = "0123456789ABCDEF"
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		b := s[i]
		switch {
		case (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9'),
			b == '-', b == '_', b == '.', b == '~':
			sb.WriteByte(b)
		case b == '/' && !encodeSlash:
			sb.WriteByte(b)
		default:
			sb.WriteByte('%')
			sb.WriteByte(hexUpper[b>>4])
			sb.WriteByte(hexUpper[b&0x0F])
		}
	}
	return sb.String()
}

// buildStringToSign builds the string to sign for signature validation
func buildStringToSign(dateStr, credentialScope, canonicalRequest string) string {
	return sigV4Algorithm + "\n" + dateStr + "\n" + credentialScope + "\n" + sha256Hash(canonicalRequest)
}

// deriveSigningKey derives the SigV4 signing key from the secret and scope
// ("date/region/service/aws4_request").
func deriveSigningKey(secretKey, credentialScope string) ([]byte, error) {
	scopeParts := strings.Split(credentialScope, "/")
	if len(scopeParts) < 3 {
		return nil, fmt.Errorf("invalid credential scope")
	}
	kDate := hmacSHA256([]byte("AWS4"+secretKey), scopeParts[0])
	kRegion := hmacSHA256(kDate, scopeParts[1])
	kService := hmacSHA256(kRegion, scopeParts[2])
	return hmacSHA256(kService, "aws4_request"), nil
}

// hmacSHA256 calculates HMAC-SHA256
func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// sha256Hash calculates SHA256 hash and returns hex string
func sha256Hash(data string) string {
	h := sha256.New()
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// validateTimestampAt validates that the request timestamp is within 15
// minutes of server time. This prevents replay attacks using captured requests.
func validateTimestampAt(dateStr string, now time.Time) error {
	// AWS Signature V4 uses ISO 8601 format: 20130524T000000Z
	requestTime, err := time.Parse(sigV4TimeFormat, dateStr)
	if err != nil {
		// Try RFC1123 format (used in Date header)
		requestTime, err = time.Parse(time.RFC1123, dateStr)
		if err != nil {
			// Try RFC1123Z format
			requestTime, err = time.Parse(time.RFC1123Z, dateStr)
			if err != nil {
				return fmt.Errorf("invalid date format")
			}
		}
	}

	// Symmetric window: rejects both stale and future-dated requests.
	diff := now.UTC().Sub(requestTime)
	if diff < 0 {
		diff = -diff
	}
	if diff > sigV4MaxSkew {
		return fmt.Errorf("request timestamp too old or too far in the future")
	}
	return nil
}

// ── Payload integrity ───────────────────────────────────────────────────────

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// validPayloadHashValue reports whether an X-Amz-Content-Sha256 value is one
// we can honour: absent, UNSIGNED-PAYLOAD, a streaming sentinel, or a digest.
func validPayloadHashValue(v string) bool {
	return v == "" || v == unsignedPayload || streamingPayloadHashes[v] || isHexSHA256(v)
}

// wrapBodyWithDigestCheck binds the request body to the signed SHA-256. A
// known-empty body is checked immediately (returns false on mismatch);
// otherwise the body is replaced with a reader that fails at the end of the
// payload if the digest differs.
func wrapBodyWithDigestCheck(r *http.Request, hexDigest string) bool {
	want, _ := hex.DecodeString(strings.ToLower(hexDigest))
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		sum := sha256.Sum256(nil)
		if !hmac.Equal(sum[:], want) {
			return false
		}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = &digestVerifyingReader{rc: r.Body, h: sha256.New(), want: want, known: true}
		}
		return true
	}
	r.Body = &digestVerifyingReader{rc: r.Body, h: sha256.New(), want: want, known: r.ContentLength > 0, remaining: r.ContentLength}
	return true
}

// digestVerifyingReader hashes the body as it is read and verifies the digest
// once the whole payload has been seen — at io.EOF, or as soon as
// Content-Length bytes have been read (so callers that stop at exactly
// Content-Length, e.g. io.ReadFull / io.CopyN, still get the check). On
// mismatch the final chunk is withheld and ErrContentSHA256Mismatch is
// returned, so a consumer can never observe the complete (tampered) payload
// without an error.
type digestVerifyingReader struct {
	rc        io.ReadCloser
	h         hash.Hash
	want      []byte
	known     bool  // Content-Length known
	remaining int64 // bytes left per Content-Length (when known)
	done      bool
	err       error
}

func (d *digestVerifyingReader) Read(p []byte) (int, error) {
	if d.done {
		if d.err != nil {
			return 0, d.err
		}
		return 0, io.EOF
	}
	n, err := d.rc.Read(p)
	if n > 0 {
		d.h.Write(p[:n])
		if d.known {
			d.remaining -= int64(n)
		}
	}
	if !errors.Is(err, io.EOF) && (!d.known || d.remaining > 0) {
		return n, err
	}
	d.done = true
	if !hmac.Equal(d.h.Sum(nil), d.want) {
		d.err = ErrContentSHA256Mismatch
		return 0, d.err
	}
	if err == nil && n == 0 {
		err = io.EOF
	}
	return n, err
}

func (d *digestVerifyingReader) Close() error { return d.rc.Close() }

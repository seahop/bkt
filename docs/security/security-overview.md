# Security Overview

This document provides a comprehensive overview of security features and best practices for bkt.

## Security Architecture

### Defense in Depth

The system implements multiple layers of security:

1. **Transport Layer**: TLS/SSL encryption for all communications
2. **Authentication Layer**: JWT tokens and access keys with bcrypt hashing
3. **Authorization Layer**: IAM-style policies with deny-by-default
4. **Data Layer**: PostgreSQL with SSL connections
5. **Application Layer**: Input validation, SQL injection prevention, rate limiting

### Security by Default

- ✅ **TLS Required**: All services use HTTPS/SSL by default
- ✅ **Deny-by-Default**: No access without explicit allow
- ✅ **Secure Randomness**: crypto/rand for all key generation
- ✅ **Password Hashing**: Bcrypt with cost factor 12
- ✅ **Constant-Time Comparison**: Prevents timing attacks
- ✅ **Input Validation**: All user input validated and sanitized
- ✅ **SQL Parameterization**: Prevents SQL injection

## Authentication

### JWT Tokens

**Implementation:**
- Algorithm: HS256 (HMAC with SHA-256)
- Access Token Lifetime: 15 minutes
- Refresh Token Lifetime: 7 days
- Claims: user_id, username, is_admin, exp, nbf, iat

**Security Features:**
- Tokens are signed, not encrypted
- Short-lived access tokens minimize risk
- Refresh tokens enable long sessions without exposing credentials
- **Refresh-token rotation with reuse detection**: every refresh issues a new
  refresh token and retires the old one; replaying a rotated (already-used)
  refresh token is treated as theft and revokes **all** of that user's sessions
- Logout revokes both the access and refresh tokens
- Access tokens are stateless; refresh tokens are tracked server-side to
  enable rotation, reuse detection, and revocation

**Best Practices:**
- Never store tokens in localStorage (XSS risk)
- Use httpOnly cookies or secure storage
- Implement automatic token refresh
- Clear tokens on logout
- Validate token expiration on every request

### Access Keys

**Format:**
- Access Key: `AK` + 20 random bytes (~27 characters)
- Secret Key: `SK` + 40 random bytes (~54 characters)

**Cryptographic Security:**
```go
// Secure random generation
func GenerateAccessKey() (string, error) {
    randomBytes := make([]byte, AccessKeyLength)
    if _, err := rand.Read(randomBytes); err != nil {
        return "", err
    }
    encoded := base64.RawURLEncoding.EncodeToString(randomBytes)
    return AccessKeyPrefix + encoded, nil
}

// Bcrypt hashing
func HashSecretKey(secretKey string) (string, error) {
    hashed, err := bcrypt.GenerateFromPassword([]byte(secretKey), 12)
    return string(hashed), err
}

// Constant-time comparison
func ValidateSecretKey(secretKey, hash string) bool {
    err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(secretKey))
    return err == nil
}
```

**Security Features:**
- Uses `crypto/rand` (not `math/rand`)
- Bcrypt cost factor 12 (secure against GPU attacks)
- Constant-time comparison prevents timing attacks
- Secrets never stored in plaintext
- Secrets shown only once during creation
- Maximum 5 active regular keys per user (prevents key sprawl)
- Soft delete for audit trail
- **Per-key scoping**: keys can be named, given an optional **expiry**, and
  flagged **read-only** (a read-only key is denied all mutating S3 operations)
- **Temporary credentials (bkt-STS)**: users can mint short-lived key pairs
  (default 1h, max 12h, optionally read-only) that auto-delete after expiry
  and don't count against the 5-key limit. Note: this is a console API, not
  the AWS STS API
- Presigned URLs are signed with one of the user's active keys and are capped
  by that key's expiry

### Password Security

**Requirements:**
- Minimum 8 characters
- No complexity requirements (length > complexity for security)

**Storage:**
- Bcrypt hashing with cost factor 12
- Salted automatically by bcrypt
- Password field never serialized in JSON responses

**Recommendations:**
- Use password managers
- Enable 2FA (future enhancement)
- Rotate passwords regularly
- Never reuse passwords across services

## Authorization

### Policy-Based Access Control

**Policy Model:**
- IAM-compatible policy documents
- JSON format with Version, Statement, Effect, Action, Resource
- Deny-by-default security model
- Explicit deny always wins over allow

**Evaluation Logic:**
```go
func EvaluatePolicy(policy *PolicyDocument, ctx *PolicyEvaluationContext) bool {
    // Admin bypass
    if ctx.IsAdmin {
        return true
    }

    hasExplicitAllow := false
    hasExplicitDeny := false

    for _, statement := range policy.Statement {
        if matchesAction && matchesResource {
            if statement.Effect == "Deny" {
                hasExplicitDeny = true
                break  // Deny wins immediately
            } else if statement.Effect == "Allow" {
                hasExplicitAllow = true
            }
        }
    }

    // DENY OVERRIDES ALLOW
    if hasExplicitDeny {
        return false
    }

    // DENY BY DEFAULT
    return hasExplicitAllow
}
```

**Security Features:**
- Deny-by-default prevents accidental access
- Explicit deny cannot be overridden
- Admin users bypass policy checks (use carefully!)
- Multiple policies evaluated as union
- Policy validation prevents injection

### Policy Validation

**Input Validation:**
```go
// Size limits
if len(documentJSON) > 10240 {  // 10KB max
    return nil, fmt.Errorf("policy document too large")
}

// Statement limits
if len(policy.Statement) > 20 {
    return nil, fmt.Errorf("policy cannot contain more than 20 statements")
}

// Path traversal prevention
if strings.Contains(resource, "..") {
    return fmt.Errorf("resource cannot contain '..'")
}

// Format validation
parts := strings.Split(action, ":")
if len(parts) != 2 {
    return fmt.Errorf("action must be in format 'service:action'")
}
```

**Security Validations:**
- ✅ Maximum policy size (10KB)
- ✅ Maximum statements (20)
- ✅ Path traversal prevention
- ✅ Action format validation (service:action)
- ✅ Resource ARN validation
- ✅ Alphanumeric validation for identifiers
- ✅ JSON re-serialization (prevents injection)

## Transport Security

### TLS/SSL Configuration

**Backend (Go):**

The server runs two TLS listeners that share the same hardened config — the
console (UI + REST API) on `9443` and the S3-compatible API on `9000`. TLS is
required in production (`GO_ENV=production` rejects `TLS_ENABLED=false`); plain
HTTP is only permitted in development or behind a TLS-terminating proxy.

```go
srv := &http.Server{
    Addr:    ":9443", // and a second server on :9000 for the S3 API
    Handler: router,
    TLSConfig: &tls.Config{
        MinVersion: tls.VersionTLS12,
        CipherSuites: []uint16{
            tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
            tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
        },
    },
}

srv.ListenAndServeTLS(certFile, keyFile)
```

**PostgreSQL:**
```sql
-- SSL enabled
ssl = on
ssl_cert_file = '/var/lib/postgresql/certs/server.crt'
ssl_key_file = '/var/lib/postgresql/certs/server.key'
ssl_ca_file = '/var/lib/postgresql/certs/ca.crt'

-- Verify SSL connection
SELECT datname, usename, ssl FROM pg_stat_ssl JOIN pg_stat_activity ON pg_stat_ssl.pid = pg_stat_activity.pid;
```

**Certificate Management:**
- Self-signed certificates for development
- CA-signed certificates for production
- Automatic certificate ownership (postgres UID 70)
- Easy certificate replacement
- Rotation without downtime

## Data Security

### Database Security

**Connection Security:**
- SSL/TLS for all database connections
- `sslmode=require` in connection string
- Certificate validation
- Encrypted data in transit

**Access Control:**
- Application uses dedicated database user
- Least privilege principle
- No direct database access for users
- Connection pooling

**Data Protection:**
- Password fields never serialized
- Secret key hashes never exposed
- Soft deletes for audit trail
- Foreign key constraints prevent orphans

### Object Storage Security

**File System Security:**
- Objects stored with sanitized paths
- Path traversal prevention
- Directory permissions (0755)
- File permissions (0644)

**Integrity Checking:**
```go
// MD5 and SHA256 hashing during upload
md5Hash := md5.New()
sha256Hash := sha256.New()
multiWriter := io.MultiWriter(dst, md5Hash, sha256Hash)

size, err := io.Copy(multiWriter, file)
md5Sum := hex.EncodeToString(md5Hash.Sum(nil))
sha256Sum := hex.EncodeToString(sha256Hash.Sum(nil))

// ETag for cache validation
c.Header("ETag", fmt.Sprintf("\"%s\"", md5Sum))
```

**Security Features:**
- MD5 ETags for cache validation
- SHA256 hashes for integrity verification
- Path sanitization prevents traversal
- Atomic file operations

**Encryption at Rest:**
- **External S3 backend**: set `S3_SSE=true` to request SSE-S3 (AES256)
  server-side encryption on every object written through the S3 backend —
  encryption is performed by the backing store
- **Local backend**: bkt does **not** encrypt bytes on disk — use disk-level
  encryption (LUKS/dm-crypt) on the storage volume
- Stored S3 credentials (S3 configurations) are encrypted in the database with
  `ENCRYPTION_KEY`

**Retention (WORM-lite):**
- A bucket's `retention_days` setting (requires versioning) protects data
  inside the window: version-addressed deletes are refused, lifecycle purges
  skip protected versions, versioning cannot be suspended, and the bucket
  cannot be deleted. Plain deletes still create delete markers (data kept)

**Webhook Integrity:**
- Bucket webhooks (`object:created` / `object:removed`) can be HMAC-signed:
  when a `webhook_secret` is set, each delivery carries an HMAC-SHA256 of the
  raw body in `X-Bkt-Signature: sha256=<hex>` so receivers can authenticate
  the sender

## Input Validation

### Request Validation

**JSON Binding:**
```go
type CreatePolicyRequest struct {
    Name        string `json:"name" binding:"required"`
    Description string `json:"description"`
    Document    string `json:"document" binding:"required"`
}

if err := c.ShouldBindJSON(&req); err != nil {
    c.JSON(http.StatusBadRequest, models.ErrorResponse{
        Error:   "Invalid request",
        Message: err.Error(),
    })
    return
}
```

**UUID Validation:**
```go
policyUUID, err := uuid.Parse(policyID)
if err != nil {
    c.JSON(http.StatusBadRequest, models.ErrorResponse{
        Error: "Invalid policy ID",
    })
    return
}
```

**SQL Injection Prevention:**
```go
// Always use parameterized queries
database.DB.Where("id = ?", userID).First(&user)

// NEVER use string concatenation
// database.DB.Where("id = " + userID).First(&user)  // UNSAFE!
```

### File Upload Validation

**Size Limits:**
```go
if fileHeader.Size > h.config.Storage.MaxFileSize {
    c.JSON(http.StatusRequestEntityTooLarge, models.ErrorResponse{
        Error:   "File too large",
        Message: fmt.Sprintf("Maximum file size is %d bytes", h.config.Storage.MaxFileSize),
    })
    return
}
```

**Path Sanitization:**
```go
func sanitizeObjectKey(key string) string {
    key = strings.Trim(key, "/")
    key = strings.ReplaceAll(key, "..", "")
    key = filepath.Clean(key)
    return key
}
```

## Attack Prevention

### SQL Injection

**Prevention:**
- ✅ Parameterized queries (GORM)
- ✅ Input validation
- ✅ Type checking
- ✅ ORM abstraction

**Example:**
```go
// SAFE - Parameterized
database.DB.Where("username = ?", username).First(&user)

// UNSAFE - String concatenation (NEVER DO THIS)
// database.DB.Where("username = '" + username + "'").First(&user)
```

### Path Traversal

**Prevention:**
- ✅ Path sanitization
- ✅ `..` detection and rejection
- ✅ filepath.Clean()
- ✅ Validation at multiple layers

**Example:**
```go
// Policy validation
if strings.Contains(resource, "..") {
    return fmt.Errorf("resource cannot contain '..'")
}

// File system sanitization
func sanitizeObjectKey(key string) string {
    key = strings.Trim(key, "/")
    key = strings.ReplaceAll(key, "..", "")
    key = filepath.Clean(key)
    return key
}
```

### Timing Attacks

**Prevention:**
- ✅ Constant-time comparison for secrets
- ✅ Bcrypt (inherently timing-safe)
- ✅ No early returns in authentication

**Example:**
```go
// SAFE - Constant-time comparison
func CompareStringsConstantTime(a, b string) bool {
    return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// UNSAFE - Variable-time comparison (NEVER DO THIS)
// return a == b
```

### Denial of Service (DoS)

**Prevention:**
- ✅ Request size limits (10KB policies)
- ✅ Statement count limits (20 per policy)
- ✅ Access key limits (5 per user)
- ✅ File size limits (5GB default)
- ✅ Rate limiting — auth endpoints (`AUTH_RATE_LIMIT`, per-IP/min) and
  optionally the S3 listener (`S3_RATE_LIMIT`); behind a proxy, set
  `TRUSTED_PROXIES` so real client IPs are used
- ✅ Connection pooling
- ✅ Timeouts

**Limits:**
```go
// Policy size
if len(documentJSON) > 10240 {
    return nil, fmt.Errorf("policy document too large")
}

// Access keys
if count >= 5 {
    c.JSON(http.StatusBadRequest, models.ErrorResponse{
        Error: "Maximum access keys reached",
    })
    return
}

// File size
if fileHeader.Size > h.config.Storage.MaxFileSize {
    // Reject upload
}
```

### Cross-Site Scripting (XSS)

**Prevention:**
- ✅ JSON API only (no HTML rendering)
- ✅ Content-Type headers
- ✅ CORS configuration
- ✅ Frontend sanitization (React escaping)

**CORS Configuration:**
```go
router.Use(cors.New(cors.Config{
    AllowOrigins:     []string{"http://localhost:5173"},
    AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "HEAD"},
    AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
    ExposeHeaders:    []string{"Content-Length", "ETag"},
    AllowCredentials: true,
}))
```

### Cross-Site Request Forgery (CSRF)

**Prevention:**
- ✅ JWT tokens (not cookies)
- ✅ CORS restrictions
- ✅ Authorization header required
- ✅ SameSite cookie policy (future)

### Information Disclosure — `/metrics`

The Prometheus endpoint exposes bucket/object/user counts. Set
`METRICS_TOKEN` so `GET /metrics` requires `Authorization: Bearer <token>`,
or keep the endpoint network-isolated.

## Security Best Practices

### For Users

1. **Passwords**
   - Use strong, unique passwords
   - Enable password manager
   - Rotate passwords every 90 days
   - Never share passwords

2. **Access Keys**
   - Generate separate keys per application
   - Store in environment variables or secret managers
   - Never commit to version control
   - Rotate every 90 days
   - Revoke unused keys

3. **Sessions**
   - Logout when done
   - Don't share tokens
   - Monitor last_used_at timestamps

### For Administrators

1. **User Management**
   - Follow least privilege principle
   - Regular access reviews
   - Disable inactive accounts
   - Monitor admin actions

2. **Policy Management**
   - Start with restrictive policies
   - Use explicit deny for critical restrictions
   - Document all policies
   - Test before deployment
   - Regular policy audits

3. **System Security**
   - Keep software updated
   - Monitor logs
   - Regular backups
   - Incident response plan
   - Security audits

### For Developers

1. **Code Security**
   - Input validation
   - Parameterized queries
   - Error handling
   - Security testing
   - Code reviews

2. **API Integration**
   - Use HTTPS always
   - Validate SSL certificates
   - Handle token expiration
   - Implement retry logic
   - Rate limit client requests

3. **Secret Management**
   - Environment variables
   - Secret rotation
   - Never log secrets
   - Secure storage
   - Access auditing

## Compliance and Auditing

### Audit Trail

bkt keeps a dedicated, database-backed audit log, queryable by admins via
`GET /api/audit` (filterable by user, action, resource type, status, and time
range; paginated).

**Logged Events:**
- User registration and user management (including deletions)
- Login attempts, success and failure — **local and SSO** (SSO logins include
  provider metadata)
- Access key creation/revocation and bkt-STS credential issuance
- Policy changes and group membership/policy operations
- Bucket operations, including settings, versioning, and lifecycle changes

**Retention:** rows older than `AUDIT_RETENTION_DAYS` (default 90) are pruned
automatically; `<= 0` disables pruning. Deleting a user keeps their audit
history.

**Future Enhancements:**
- Log export to SIEM
- Compliance reporting

### Data Privacy

**Personal Data:**
- Username
- Email address
- IP addresses (in logs)

**Data Handling:**
- Minimal collection
- Secure storage
- Right to deletion
- Data export

### Compliance Frameworks

The system can support:
- SOC 2
- ISO 27001
- GDPR
- HIPAA (with additional controls)

## Security Roadmap

### Shipped

- **Rate limiting** — per-IP limits on auth endpoints (`AUTH_RATE_LIMIT`) and
  the S3 listener (`S3_RATE_LIMIT`), proxy-aware via `TRUSTED_PROXIES`
- **Audit logging** — dedicated audit log table with configurable retention
  (`AUDIT_RETENTION_DAYS`), queryable via `GET /api/audit`

### Planned Enhancements

1. **Multi-Factor Authentication**
   - TOTP support
   - SMS backup codes
   - WebAuthn/FIDO2

2. **Advanced Policies**
   - IP address conditions
   - Time-based conditions
   - MFA requirements
   - Resource tagging

3. **Key Management**
   - Hardware Security Module (HSM) support
   - Key rotation automation
   - Key encryption keys (KEK)

4. **Monitoring**
   - Intrusion detection
   - Anomaly detection
   - Security dashboards
   - Alert integration

## Security Contacts

For security issues:
- Report via GitHub Issues (for non-critical)
- Email security contact (for critical vulnerabilities)
- Allow responsible disclosure period

## Related Documentation

- [Policies API](../api/policies.md)
- [Admin Guide](../guides/admin-guide.md)
- [Production Checklist](../deployment/production-checklist.md)

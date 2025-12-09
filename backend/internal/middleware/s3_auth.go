package middleware

import (
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// S3AuthMiddleware validates AWS Signature Version 4 authentication
// This is used for S3-compatible API requests (e.g., from s3fs-fuse)
func S3AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Extract authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.Header("WWW-Authenticate", "AWS4-HMAC-SHA256")
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"Code":    "AccessDenied",
				"Message": "Missing authorization header",
			})
			return
		}

		// Parse authorization header (AWS4-HMAC-SHA256 Credential=..., SignedHeaders=..., Signature=...)
		if !strings.HasPrefix(authHeader, "AWS4-HMAC-SHA256") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"Code":    "InvalidArgument",
				"Message": "Unsupported authorization method",
			})
			return
		}

		// Extract access key from Credential field
		accessKey, err := extractAccessKey(authHeader)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"Code":    "InvalidArgument",
				"Message": err.Error(),
			})
			return
		}

		// Look up access key in database
		var key models.AccessKey
		if err := database.DB.Where("access_key = ? AND is_active = ?", accessKey, true).
			Preload("User").First(&key).Error; err != nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"Code":    "InvalidAccessKeyId",
				"Message": "The access key ID you provided does not exist in our records",
			})
			return
		}

		// Check if user is locked
		if key.User.IsLocked {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"Code":    "AccessDenied",
				"Message": "Account is locked",
			})
			return
		}

		// Decrypt secret key
		secretKey, err := security.DecryptSecretKey(key.SecretKeyEncrypted)
		if err != nil {
			fmt.Printf("[S3Auth] Failed to decrypt secret for access key %s: %v\n", accessKey, err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
				"Code":    "InternalError",
				"Message": fmt.Sprintf("Failed to decrypt secret key: %v", err),
			})
			return
		}

		// Validate signature
		if err := validateSignature(c, authHeader, accessKey, secretKey); err != nil {
			// Log signature validation failure for debugging
			fmt.Printf("[S3Auth] Signature validation failed for %s: %v\n", accessKey, err)
			fmt.Printf("[S3Auth] Method: %s, Path: %s\n", c.Request.Method, c.Request.URL.Path)
			fmt.Printf("[S3Auth] Auth Header: %s\n", authHeader)
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"Code":    "SignatureDoesNotMatch",
				"Message": err.Error(),
			})
			return
		}

		// Update last used timestamp
		now := time.Now()
		key.LastUsedAt = &now
		database.DB.Save(&key)

		// Set user context for downstream handlers
		c.Set("user_id", key.UserID)
		c.Set("user", &key.User)
		c.Set("is_admin", key.User.IsAdmin)

		c.Next()
	}
}

// extractAccessKey extracts the access key from the Authorization header
func extractAccessKey(authHeader string) (string, error) {
	// Authorization format: AWS4-HMAC-SHA256 Credential=ACCESS_KEY/date/region/service/aws4_request, SignedHeaders=..., Signature=...
	parts := strings.Split(authHeader, " ")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid authorization header format")
	}

	for _, part := range parts[1:] {
		if strings.HasPrefix(part, "Credential=") {
			credentialStr := strings.TrimPrefix(part, "Credential=")
			credentialStr = strings.TrimSuffix(credentialStr, ",")

			// Credential format: ACCESS_KEY/date/region/service/aws4_request
			credentialParts := strings.Split(credentialStr, "/")
			if len(credentialParts) < 1 {
				return "", fmt.Errorf("invalid credential format")
			}

			return credentialParts[0], nil
		}
	}

	return "", fmt.Errorf("credential not found in authorization header")
}

// validateSignature validates the AWS Signature Version 4
func validateSignature(c *gin.Context, authHeader, accessKey, secretKey string) error {
	// Extract signature from authorization header
	providedSignature := extractSignature(authHeader)
	if providedSignature == "" {
		return fmt.Errorf("signature not found in authorization header")
	}

	// Extract signed headers
	signedHeaders := extractSignedHeaders(authHeader)
	if signedHeaders == "" {
		return fmt.Errorf("signed headers not found")
	}

	// Get request date (from X-Amz-Date header or Date header)
	dateStr := c.GetHeader("X-Amz-Date")
	if dateStr == "" {
		dateStr = c.GetHeader("Date")
	}
	if dateStr == "" {
		return fmt.Errorf("missing date header")
	}

	// Extract credential scope
	credentialScope, err := extractCredentialScope(authHeader)
	if err != nil {
		return err
	}

	// Build canonical request
	canonicalRequest := buildCanonicalRequest(c, signedHeaders)

	// Build string to sign
	stringToSign := buildStringToSign(dateStr, credentialScope, canonicalRequest)

	// Calculate signature
	calculatedSignature := calculateSignature(secretKey, dateStr, credentialScope, stringToSign)

	// Compare signatures (constant-time comparison to prevent timing attacks)
	if !hmac.Equal([]byte(calculatedSignature), []byte(providedSignature)) {
		return fmt.Errorf("signature mismatch")
	}

	return nil
}

// extractSignature extracts the signature from the Authorization header
func extractSignature(authHeader string) string {
	parts := strings.Split(authHeader, " ")
	for _, part := range parts {
		if strings.HasPrefix(part, "Signature=") {
			return strings.TrimPrefix(part, "Signature=")
		}
	}
	return ""
}

// extractSignedHeaders extracts the signed headers from the Authorization header
func extractSignedHeaders(authHeader string) string {
	parts := strings.Split(authHeader, " ")
	for _, part := range parts {
		if strings.HasPrefix(part, "SignedHeaders=") {
			return strings.TrimSuffix(strings.TrimPrefix(part, "SignedHeaders="), ",")
		}
	}
	return ""
}

// extractCredentialScope extracts the credential scope from the Authorization header
func extractCredentialScope(authHeader string) (string, error) {
	parts := strings.Split(authHeader, " ")
	for _, part := range parts {
		if strings.HasPrefix(part, "Credential=") {
			credentialStr := strings.TrimPrefix(part, "Credential=")
			credentialStr = strings.TrimSuffix(credentialStr, ",")

			// Credential format: ACCESS_KEY/date/region/service/aws4_request
			credentialParts := strings.Split(credentialStr, "/")
			if len(credentialParts) < 5 {
				return "", fmt.Errorf("invalid credential format")
			}

			// Scope is everything after ACCESS_KEY
			return strings.Join(credentialParts[1:], "/"), nil
		}
	}
	return "", fmt.Errorf("credential scope not found")
}

// buildCanonicalRequest builds the canonical request string for signature validation
func buildCanonicalRequest(c *gin.Context, signedHeaders string) string {
	// HTTP Method
	method := c.Request.Method

	// Canonical URI
	canonicalURI := c.Request.URL.Path
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	// Canonical query string - AWS SigV4 spec requires sorted, URL-encoded parameters
	query := c.Request.URL.Query()
	var queryKeys []string
	for key := range query {
		queryKeys = append(queryKeys, key)
	}
	sort.Strings(queryKeys)

	var queryParts []string
	for _, key := range queryKeys {
		encodedKey := url.QueryEscape(key)
		for _, value := range query[key] {
			encodedValue := url.QueryEscape(value)
			queryParts = append(queryParts, fmt.Sprintf("%s=%s", encodedKey, encodedValue))
		}
	}
	canonicalQuery := strings.Join(queryParts, "&")

	// Canonical headers
	headerNames := strings.Split(signedHeaders, ";")
	var canonicalHeaders []string
	for _, headerName := range headerNames {
		// Get header value - Gin stores headers with canonical names (Host, not host)
		// Convert to canonical form for lookup, but keep lowercase for signature
		canonicalName := http.CanonicalHeaderKey(headerName)
		headerValue := c.Request.Header.Get(canonicalName)

		// Special handling for Host header - it might be in c.Request.Host
		if headerName == "host" && headerValue == "" {
			headerValue = c.Request.Host
		}

		canonicalHeaders = append(canonicalHeaders, fmt.Sprintf("%s:%s\n", headerName, strings.TrimSpace(headerValue)))
	}
	canonicalHeadersStr := strings.Join(canonicalHeaders, "")

	// Hashed payload
	payloadHash := c.GetHeader("X-Amz-Content-Sha256")
	if payloadHash == "" {
		payloadHash = "UNSIGNED-PAYLOAD"
	}

	// Build canonical request
	return fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n%s",
		method,
		canonicalURI,
		canonicalQuery,
		canonicalHeadersStr,
		signedHeaders,
		payloadHash,
	)
}

// buildStringToSign builds the string to sign for signature validation
func buildStringToSign(dateStr, credentialScope, canonicalRequest string) string {
	hashedCanonicalRequest := sha256Hash(canonicalRequest)
	return fmt.Sprintf("AWS4-HMAC-SHA256\n%s\n%s\n%s",
		dateStr,
		credentialScope,
		hashedCanonicalRequest,
	)
}

// calculateSignature calculates the AWS Signature Version 4 signature
func calculateSignature(secretKey, dateStr, credentialScope, stringToSign string) string {
	// Extract date, region, and service from credential scope
	scopeParts := strings.Split(credentialScope, "/")
	if len(scopeParts) < 3 {
		return ""
	}

	date := scopeParts[0]
	region := scopeParts[1]
	service := scopeParts[2]

	// Derive signing key
	kDate := hmacSHA256([]byte("AWS4"+secretKey), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")

	// Calculate signature
	signature := hmacSHA256(kSigning, stringToSign)
	return hex.EncodeToString(signature)
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

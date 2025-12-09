package validation

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
)

// S3 bucket naming rules: https://docs.aws.amazon.com/AmazonS3/latest/userguide/bucketnamingrules.html
var (
	bucketNameRegex = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]*[a-z0-9]$`)
	ipAddressRegex  = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}\.[0-9]{1,3}$`)
	regionRegex     = regexp.MustCompile(`^[a-z]{2}-[a-z]+-[0-9]{1,2}$`)
)

// ValidateBucketName validates bucket name according to S3 naming rules
func ValidateBucketName(name string) error {
	// Length check (3-63 characters)
	if len(name) < 3 || len(name) > 63 {
		return fmt.Errorf("bucket name must be between 3 and 63 characters")
	}

	// Must start and end with lowercase letter or number
	// Can contain lowercase letters, numbers, and hyphens
	if !bucketNameRegex.MatchString(name) {
		return fmt.Errorf("bucket name must start and end with a lowercase letter or number, and can only contain lowercase letters, numbers, and hyphens")
	}

	// Must not be formatted as an IP address
	if ipAddressRegex.MatchString(name) {
		return fmt.Errorf("bucket name must not be formatted as an IP address")
	}

	// Must not contain consecutive hyphens
	if strings.Contains(name, "--") {
		return fmt.Errorf("bucket name must not contain consecutive hyphens")
	}

	// Must not start with "xn--" (reserved for internationalized domain names)
	if strings.HasPrefix(name, "xn--") {
		return fmt.Errorf("bucket name must not start with 'xn--' prefix")
	}

	// Must not end with "-s3alias" (reserved suffix)
	if strings.HasSuffix(name, "-s3alias") {
		return fmt.Errorf("bucket name must not end with '-s3alias' suffix")
	}

	return nil
}

// ValidateObjectKey validates object key to prevent path traversal and other attacks
func ValidateObjectKey(key string) error {
	// Check for empty key
	if key == "" {
		return fmt.Errorf("object key cannot be empty")
	}

	// Max length check (1024 bytes for S3)
	if len(key) > 1024 {
		return fmt.Errorf("object key cannot exceed 1024 characters")
	}

	// Check for path traversal patterns
	if strings.Contains(key, "..") {
		return fmt.Errorf("object key cannot contain '..' path traversal")
	}

	// Check for absolute paths
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("object key cannot start with '/'")
	}

	// Check for null bytes (security risk)
	if strings.Contains(key, "\x00") {
		return fmt.Errorf("object key cannot contain null bytes")
	}

	// Check for backslashes (Windows path separators - potential confusion)
	if strings.Contains(key, "\\") {
		return fmt.Errorf("object key cannot contain backslashes")
	}

	return nil
}

// ValidateIPAddress checks if a string is a valid IP address
func ValidateIPAddress(ip string) bool {
	return net.ParseIP(ip) != nil
}

// EscapeLikeWildcards escapes special characters in LIKE patterns to prevent SQL injection
func EscapeLikeWildcards(input string) string {
	// Escape backslash first (must be first to avoid double-escaping)
	escaped := strings.ReplaceAll(input, "\\", "\\\\")
	// Escape percent sign (matches any sequence of characters)
	escaped = strings.ReplaceAll(escaped, "%", "\\%")
	// Escape underscore (matches any single character)
	escaped = strings.ReplaceAll(escaped, "_", "\\_")
	return escaped
}

// DetectContentType detects the actual content type by reading the first 512 bytes of the file.
// It uses Go's http.DetectContentType which inspects magic numbers to determine the MIME type.
// Returns the detected content type and the bytes read (which should be used for upload).
func DetectContentType(reader io.Reader) (contentType string, firstBytes []byte, err error) {
	// Read first 512 bytes (maximum needed for http.DetectContentType)
	buffer := make([]byte, 512)
	n, readErr := io.ReadFull(reader, buffer)

	// io.ReadFull returns io.EOF or io.ErrUnexpectedEOF if file is smaller than 512 bytes
	// This is normal and should not be treated as an error
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return "", nil, fmt.Errorf("failed to read file content: %w", readErr)
	}

	// Use only the bytes actually read
	firstBytes = buffer[:n]

	// Detect content type using magic numbers
	contentType = http.DetectContentType(firstBytes)

	return contentType, firstBytes, nil
}

// IsSafeContentType checks if a content type is considered safe for upload.
// This function can be extended to block dangerous file types.
func IsSafeContentType(contentType string) bool {
	// Normalize content type (remove parameters like charset)
	normalized := strings.ToLower(strings.Split(contentType, ";")[0])
	normalized = strings.TrimSpace(normalized)

	// Block potentially dangerous executable types
	dangerousTypes := []string{
		"application/x-msdownload",           // .exe
		"application/x-msdos-program",        // .com, .exe
		"application/x-executable",           // executables
		"application/x-sharedlib",            // .so shared libraries
		"application/x-mach-binary",          // Mach-O binaries
		"application/vnd.microsoft.portable-executable", // PE executables
	}

	for _, dangerous := range dangerousTypes {
		if normalized == dangerous {
			return false
		}
	}

	return true
}

// ValidateRegion validates AWS/S3 region format
// Accepts standard AWS region format (e.g., "us-east-1", "eu-west-2")
// or allows empty string for default region
func ValidateRegion(region string) error {
	// Empty region is allowed (will use default)
	if region == "" {
		return nil
	}

	// Check if region matches standard AWS format: <continent>-<direction>-<number>
	// Examples: us-east-1, eu-west-2, ap-southeast-1
	if !regionRegex.MatchString(region) {
		return fmt.Errorf("region must match AWS format (e.g., us-east-1, eu-west-2)")
	}

	// Limit region length to prevent DoS
	if len(region) > 20 {
		return fmt.Errorf("region name too long (max 20 characters)")
	}

	return nil
}

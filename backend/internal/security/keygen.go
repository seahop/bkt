package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const (
	// AccessKeyLength is the length of the access key in bytes (20 bytes = ~27 chars base64)
	AccessKeyLength = 20
	// SecretKeyLength is the length of the secret key in bytes (40 bytes = ~54 chars base64)
	SecretKeyLength = 40
	// AccessKeyPrefix is prepended to access keys for easy identification
	AccessKeyPrefix = "AK"
	// SecretKeyPrefix is prepended to secret keys for easy identification
	SecretKeyPrefix = "SK"
)

// GenerateAccessKey generates a cryptographically secure random access key
// Format: AK + base64(20 random bytes) = ~29 characters
func GenerateAccessKey() (string, error) {
	randomBytes := make([]byte, AccessKeyLength)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// URL-safe base64 encoding without padding
	encoded := base64.RawURLEncoding.EncodeToString(randomBytes)
	return AccessKeyPrefix + encoded, nil
}

// GenerateSecretKey generates a cryptographically secure random secret key
// Format: SK + base64(40 random bytes) = ~56 characters
func GenerateSecretKey() (string, error) {
	randomBytes := make([]byte, SecretKeyLength)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// URL-safe base64 encoding without padding
	encoded := base64.RawURLEncoding.EncodeToString(randomBytes)
	return SecretKeyPrefix + encoded, nil
}

// HashSecretKey hashes a secret key using bcrypt
// This should be used before storing secret keys in the database
func HashSecretKey(secretKey string) (string, error) {
	// Using cost 12 for strong security (same as passwords)
	hashed, err := bcrypt.GenerateFromPassword([]byte(secretKey), 12)
	if err != nil {
		return "", fmt.Errorf("failed to hash secret key: %w", err)
	}
	return string(hashed), nil
}

// ValidateSecretKey validates a secret key against its hash using constant-time comparison
// This prevents timing attacks
func ValidateSecretKey(secretKey, hash string) bool {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(secretKey))
	return err == nil
}

// CompareStringsConstantTime performs constant-time string comparison
// This prevents timing attacks when comparing sensitive strings
func CompareStringsConstantTime(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ValidateAccessKeyFormat validates that an access key has the correct format
func ValidateAccessKeyFormat(accessKey string) bool {
	// Check prefix
	if len(accessKey) < len(AccessKeyPrefix) {
		return false
	}
	if accessKey[:len(AccessKeyPrefix)] != AccessKeyPrefix {
		return false
	}

	// Check length (prefix + base64 encoded 20 bytes)
	expectedLen := len(AccessKeyPrefix) + base64.RawURLEncoding.EncodedLen(AccessKeyLength)
	if len(accessKey) != expectedLen {
		return false
	}

	return true
}

// ValidateSecretKeyFormat validates that a secret key has the correct format
func ValidateSecretKeyFormat(secretKey string) bool {
	// Check prefix
	if len(secretKey) < len(SecretKeyPrefix) {
		return false
	}
	if secretKey[:len(SecretKeyPrefix)] != SecretKeyPrefix {
		return false
	}

	// Check length (prefix + base64 encoded 40 bytes)
	expectedLen := len(SecretKeyPrefix) + base64.RawURLEncoding.EncodedLen(SecretKeyLength)
	if len(secretKey) != expectedLen {
		return false
	}

	return true
}

package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
)

// getEncryptionKey derives a 32-byte encryption key from environment variable
// If ENCRYPTION_KEY is not set, it falls back to JWT_SECRET
func getEncryptionKey() ([]byte, error) {
	keyString := os.Getenv("ENCRYPTION_KEY")
	if keyString == "" {
		keyString = os.Getenv("JWT_SECRET")
	}
	if keyString == "" {
		return nil, fmt.Errorf("ENCRYPTION_KEY or JWT_SECRET must be set")
	}

	// Derive a 32-byte key using SHA256
	hash := sha256.Sum256([]byte(keyString))
	return hash[:], nil
}

// EncryptSecretKey encrypts a secret key using AES-256-GCM
// Returns base64-encoded ciphertext
func EncryptSecretKey(secretKey string) (string, error) {
	key, err := getEncryptionKey()
	if err != nil {
		return "", err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	// Generate random nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Encrypt and prepend nonce
	ciphertext := gcm.Seal(nonce, nonce, []byte(secretKey), nil)

	// Encode to base64 for database storage
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// DecryptSecretKey decrypts a secret key encrypted with EncryptSecretKey
func DecryptSecretKey(encryptedSecretKey string) (string, error) {
	key, err := getEncryptionKey()
	if err != nil {
		return "", err
	}

	// Decode from base64
	ciphertext, err := base64.StdEncoding.DecodeString(encryptedSecretKey)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("failed to create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("failed to create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}

	// Extract nonce and ciphertext
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]

	// Decrypt
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}

	return string(plaintext), nil
}

package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"sync"

	"golang.org/x/crypto/pbkdf2"
)

// Encryption format versions.
//
//	v1 (legacy): base64( nonce(12) || GCM(secret, nonce) )
//	    key = PBKDF2-SHA256(secret, static-salt, 100k). No AAD. Kept for
//	    DECRYPTION of data written before the hardening; never written anymore.
//	v2 (current): base64( 0x02 || salt(16) || nonce(12) || GCM(secret, nonce, AAD) )
//	    key = PBKDF2-SHA256(secret, per-ciphertext-salt, 600k). AAD binds the
//	    format version. Per-ciphertext salt removes the precomputation risk of a
//	    shared static salt.
const (
	encVersionV2  = 0x02
	v2SaltLen     = 16
	v2Iterations  = 600000
	legacyIters   = 100000
	derivedKeyLen = 32
)

var encAAD = []byte("bkt-secret-v2")

var (
	// secretMaterial is the raw passphrase (ENCRYPTION_KEY, or JWT_SECRET as a
	// last-resort fallback). Resolved once.
	secretMaterial     []byte
	secretMaterialOnce sync.Once
	secretMaterialErr  error

	// legacyKey is the v1 static-salt derived key, cached (single salt).
	legacyKey     []byte
	legacyKeyOnce sync.Once

	// derivedKeyCache memoizes v2 keys by salt so the expensive PBKDF2 is paid
	// once per distinct ciphertext salt — critical because DecryptSecretKey runs
	// on the S3 auth hot path (once per request, same salt per access key).
	derivedKeyCache sync.Map // hex(salt) -> []byte
)

func getSecretMaterial() ([]byte, error) {
	secretMaterialOnce.Do(func() {
		keyString := os.Getenv("ENCRYPTION_KEY")
		if keyString == "" {
			keyString = os.Getenv("JWT_SECRET")
			if keyString != "" {
				log.Println("WARNING: ENCRYPTION_KEY is not set; falling back to JWT_SECRET to encrypt stored credentials. " +
					"Set a dedicated ENCRYPTION_KEY so a JWT secret rotation/leak does not affect credential encryption.")
			}
		}
		if keyString == "" {
			secretMaterialErr = fmt.Errorf("ENCRYPTION_KEY (or JWT_SECRET) must be set")
			return
		}
		secretMaterial = []byte(keyString)
	})
	return secretMaterial, secretMaterialErr
}

func getLegacyKey() ([]byte, error) {
	secret, err := getSecretMaterial()
	if err != nil {
		return nil, err
	}
	legacyKeyOnce.Do(func() {
		salt := []byte("bkt-object-storage-v1")
		legacyKey = pbkdf2.Key(secret, salt, legacyIters, derivedKeyLen, sha256.New)
	})
	return legacyKey, nil
}

func deriveV2Key(salt []byte) ([]byte, error) {
	secret, err := getSecretMaterial()
	if err != nil {
		return nil, err
	}
	saltHex := hex.EncodeToString(salt)
	if cached, ok := derivedKeyCache.Load(saltHex); ok {
		return cached.([]byte), nil
	}
	key := pbkdf2.Key(secret, salt, v2Iterations, derivedKeyLen, sha256.New)
	derivedKeyCache.Store(saltHex, key)
	return key, nil
}

// EncryptSecretKey encrypts a secret using AES-256-GCM (format v2) and returns
// base64-encoded ciphertext.
func EncryptSecretKey(secretKey string) (string, error) {
	salt := make([]byte, v2SaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}
	key, err := deriveV2Key(salt)
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

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("failed to generate nonce: %w", err)
	}

	// Layout: version(1) || salt(16) || nonce || ciphertext
	out := make([]byte, 0, 1+v2SaltLen+len(nonce)+len(secretKey)+gcm.Overhead())
	out = append(out, encVersionV2)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, []byte(secretKey), encAAD)

	return base64.StdEncoding.EncodeToString(out), nil
}

// DecryptSecretKey decrypts a secret produced by EncryptSecretKey. It handles
// both the current v2 format and legacy v1 ciphertexts.
func DecryptSecretKey(encryptedSecretKey string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(encryptedSecretKey)
	if err != nil {
		return "", fmt.Errorf("failed to decode base64: %w", err)
	}

	if len(raw) > 0 && raw[0] == encVersionV2 {
		plaintext, err := decryptV2(raw)
		if err == nil {
			return plaintext, nil
		}
		// A legacy (v1) ciphertext is nonce||ct with a random nonce, so ~1/256 of
		// legacy blobs happen to begin with the v2 version byte. GCM authentication
		// makes a wrong-format decrypt fail, never succeed spuriously, so fall back
		// to the legacy path before reporting failure.
		if legacyPlain, lerr := decryptLegacy(raw); lerr == nil {
			return legacyPlain, nil
		}
		return "", err
	}
	return decryptLegacy(raw)
}

func decryptV2(raw []byte) (string, error) {
	if len(raw) < 1+v2SaltLen+12 {
		return "", fmt.Errorf("ciphertext too short")
	}
	salt := raw[1 : 1+v2SaltLen]
	rest := raw[1+v2SaltLen:]

	key, err := deriveV2Key(salt)
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
	nonceSize := gcm.NonceSize()
	if len(rest) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ct := rest[:nonceSize], rest[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, encAAD)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}
	return string(plaintext), nil
}

func decryptLegacy(ciphertext []byte) (string, error) {
	key, err := getLegacyKey()
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
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("failed to decrypt: %w", err)
	}
	return string(plaintext), nil
}

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
	"strings"
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

	// decryptOnlySources are DECRYPT-ONLY key sources, tried in order when
	// the primary key fails. New ciphertexts are always written with the
	// primary key; values read through one of these are reported as stale so
	// ReencryptStoredSecrets can move them to the primary key. In order:
	//   1. ENCRYPTION_KEY_PREVIOUS (comma-separated) — keys rotated away from;
	//   2. JWT_SECRET, when a dedicated ENCRYPTION_KEY is set — deployments
	//      that ran without ENCRYPTION_KEY stored credentials under it;
	//   3. ENCRYPTION_LEGACY_JWT_SECRET — a JWT_SECRET that has since been
	//      rotated (e.g. away from the public development default) under
	//      which credentials were encrypted.
	// None of them is subject to the secret-strength checks: they never
	// encrypt anything and exist only so old data stays readable.
	decryptOnlySources []*keySource
)

// keySource caches the derived keys for one piece of secret material.
type keySource struct {
	name       string // env var it came from (for logs; never the value)
	secret     []byte
	legacyOnce sync.Once
	legacy     []byte
	v2Cache    sync.Map // hex(salt) -> []byte
	usedOnce   sync.Once
}

func newKeySource(secret []byte) *keySource { return &keySource{secret: secret} }

func newNamedKeySource(name string, secret []byte) *keySource {
	return &keySource{name: name, secret: secret}
}

// splitKeyList parses a comma-separated key list, trimming blanks.
func splitKeyList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (k *keySource) legacyKey() ([]byte, error) {
	k.legacyOnce.Do(func() {
		k.legacy = pbkdf2.Key(k.secret, []byte("bkt-object-storage-v1"), legacyIters, derivedKeyLen, sha256.New)
	})
	return k.legacy, nil
}

func (k *keySource) v2Key(salt []byte) ([]byte, error) {
	saltHex := hex.EncodeToString(salt)
	if cached, ok := k.v2Cache.Load(saltHex); ok {
		return cached.([]byte), nil
	}
	key := pbkdf2.Key(k.secret, salt, v2Iterations, derivedKeyLen, sha256.New)
	k.v2Cache.Store(saltHex, key)
	return key, nil
}

func getSecretMaterial() ([]byte, error) {
	secretMaterialOnce.Do(func() {
		keyString := os.Getenv("ENCRYPTION_KEY")
		jwtSecret := os.Getenv("JWT_SECRET")
		if keyString == "" {
			keyString = jwtSecret
			if keyString != "" {
				log.Println("WARNING: ENCRYPTION_KEY is not set; falling back to JWT_SECRET to encrypt stored credentials. " +
					"Set a dedicated ENCRYPTION_KEY so a JWT secret rotation/leak does not affect credential encryption " +
					"(credentials already encrypted under JWT_SECRET remain readable after you add it).")
			}
		}
		if keyString == "" {
			secretMaterialErr = fmt.Errorf("ENCRYPTION_KEY (or JWT_SECRET) must be set")
			return
		}
		secretMaterial = []byte(keyString)
		decryptOnlySources = buildDecryptOnlySources(keyString, os.Getenv("ENCRYPTION_KEY") != "", jwtSecret,
			os.Getenv("ENCRYPTION_KEY_PREVIOUS"), os.Getenv("ENCRYPTION_LEGACY_JWT_SECRET"))
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
	return encryptV2With(secretKey, deriveV2Key)
}

func encryptV2With(secretKey string, derive func(salt []byte) ([]byte, error)) (string, error) {
	salt := make([]byte, v2SaltLen)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}
	key, err := derive(salt)
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

// buildDecryptOnlySources assembles the decrypt-only key chain (see
// decryptOnlySources), skipping blanks and duplicates of the primary or of
// each other.
func buildDecryptOnlySources(primary string, primaryIsEncryptionKey bool, jwtSecret, previous, legacyJWT string) []*keySource {
	seen := map[string]bool{primary: true}
	var out []*keySource
	add := func(name, v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, newNamedKeySource(name, []byte(v)))
	}
	for i, p := range splitKeyList(previous) {
		add(fmt.Sprintf("ENCRYPTION_KEY_PREVIOUS[%d]", i), p)
	}
	if primaryIsEncryptionKey {
		add("JWT_SECRET", jwtSecret)
	}
	add("ENCRYPTION_LEGACY_JWT_SECRET", strings.TrimSpace(legacyJWT))
	return out
}

// DecryptSecretKey decrypts a secret produced by EncryptSecretKey. It handles
// both the current v2 format and legacy v1 ciphertexts, and data written under
// any configured decrypt-only key (ENCRYPTION_KEY_PREVIOUS, the JWT_SECRET
// fallback, ENCRYPTION_LEGACY_JWT_SECRET).
func DecryptSecretKey(encryptedSecretKey string) (string, error) {
	plaintext, _, err := DecryptSecretKeyStatus(encryptedSecretKey)
	return plaintext, err
}

// DecryptSecretKeyStatus is DecryptSecretKey that also reports whether the
// ciphertext is stale — readable only through a decrypt-only key, or still in
// the legacy v1 format — and should be re-encrypted with EncryptSecretKey.
func DecryptSecretKeyStatus(encryptedSecretKey string) (plaintext string, stale bool, err error) {
	raw, err := base64.StdEncoding.DecodeString(encryptedSecretKey)
	if err != nil {
		return "", false, fmt.Errorf("failed to decode base64: %w", err)
	}

	plaintext, v1, err := decryptWith(raw, deriveV2Key, getLegacyKey)
	if err == nil {
		return plaintext, v1, nil
	}
	if _, merr := getSecretMaterial(); merr != nil {
		return "", false, err
	}
	for _, src := range decryptOnlySources {
		if p, _, srcErr := decryptWith(raw, src.v2Key, src.legacyKey); srcErr == nil {
			src.usedOnce.Do(func() {
				log.Printf("NOTICE: decrypted stored credentials with the decrypt-only key from %s. "+
					"They are re-encrypted under the current key at startup; keep %s configured until the startup log reports no stale values remain.",
					src.name, src.name)
			})
			return p, true, nil
		}
	}
	return "", false, err
}

// decryptWith dispatches on the format version using the given key sources.
// v1 reports whether the legacy (v1) format was used.
func decryptWith(raw []byte, derive func(salt []byte) ([]byte, error), legacy func() ([]byte, error)) (plaintext string, v1 bool, err error) {
	if len(raw) > 0 && raw[0] == encVersionV2 {
		plaintext, err := decryptV2With(raw, derive)
		if err == nil {
			return plaintext, false, nil
		}
		// A legacy (v1) ciphertext is nonce||ct with a random nonce, so ~1/256 of
		// legacy blobs happen to begin with the v2 version byte. GCM authentication
		// makes a wrong-format decrypt fail, never succeed spuriously, so fall back
		// to the legacy path before reporting failure.
		if legacyPlain, lerr := decryptLegacyWith(raw, legacy); lerr == nil {
			return legacyPlain, true, nil
		}
		return "", false, err
	}
	plaintext, err = decryptLegacyWith(raw, legacy)
	return plaintext, err == nil, err
}

func decryptV2With(raw []byte, derive func(salt []byte) ([]byte, error)) (string, error) {
	if len(raw) < 1+v2SaltLen+12 {
		return "", fmt.Errorf("ciphertext too short")
	}
	salt := raw[1 : 1+v2SaltLen]
	rest := raw[1+v2SaltLen:]

	key, err := derive(salt)
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

func decryptLegacyWith(ciphertext []byte, getKey func() ([]byte, error)) (string, error) {
	key, err := getKey()
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

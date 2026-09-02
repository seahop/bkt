package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"io"
	"testing"
)

// useTestKeyMaterial installs deterministic key material directly. Setting the
// env var is not enough: getSecretMaterial resolves via a sync.Once, so the
// first caller in the test binary wins and later Setenv calls are ignored.
func useTestKeyMaterial(t *testing.T) {
	t.Helper()
	secretMaterialOnce.Do(func() {}) // consume the Once so getSecretMaterial can't overwrite
	secretMaterial = []byte("unit-test-encryption-key-material")
	secretMaterialErr = nil
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	useTestKeyMaterial(t)

	secret := "AKIAEXAMPLESECRET/very+secret=value"
	enc, err := EncryptSecretKey(secret)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if enc == secret {
		t.Fatal("ciphertext equals plaintext")
	}

	dec, err := DecryptSecretKey(enc)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if dec != secret {
		t.Errorf("round trip mismatch: got %q want %q", dec, secret)
	}
}

func TestDecryptTamperedFails(t *testing.T) {
	useTestKeyMaterial(t)
	enc, err := EncryptSecretKey("payload")
	if err != nil {
		t.Fatal(err)
	}
	// Flip a character in the base64 to simulate tampering — GCM must reject it.
	b := []byte(enc)
	if b[len(b)-2] == 'A' {
		b[len(b)-2] = 'B'
	} else {
		b[len(b)-2] = 'A'
	}
	if _, err := DecryptSecretKey(string(b)); err == nil {
		t.Error("tampered ciphertext decrypted without error")
	}
}

func TestEncryptUsesDistinctSaltPerCall(t *testing.T) {
	useTestKeyMaterial(t)
	a, _ := EncryptSecretKey("same")
	b, _ := EncryptSecretKey("same")
	if a == b {
		t.Error("two encryptions of the same value produced identical ciphertext (salt/nonce not random)")
	}
}

// encryptLegacyForTest reproduces the v1 on-disk format (nonce||ct, static-salt
// key) so tests can cover the legacy decrypt path without stored fixtures.
func encryptLegacyForTest(t *testing.T, plaintext string) []byte {
	t.Helper()
	key, err := getLegacyKey()
	if err != nil {
		t.Fatalf("legacy key: %v", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil)
}

func TestDecryptLegacyRoundTrip(t *testing.T) {
	useTestKeyMaterial(t)
	secret := "legacy-format-secret"
	raw := encryptLegacyForTest(t, secret)
	dec, err := DecryptSecretKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("legacy decrypt failed: %v", err)
	}
	if dec != secret {
		t.Errorf("legacy round trip mismatch: got %q want %q", dec, secret)
	}
}

// A legacy ciphertext whose random nonce happens to start with the v2 version
// byte must still decrypt (the dispatcher falls back to the legacy path when
// v2 decryption fails).
func TestDecryptLegacyWithV2LeadingByte(t *testing.T) {
	useTestKeyMaterial(t)
	secret := "unlucky-first-byte"
	var raw []byte
	for i := 0; i < 10000; i++ {
		raw = encryptLegacyForTest(t, secret)
		if raw[0] == encVersionV2 {
			break
		}
		raw = nil
	}
	if raw == nil {
		t.Fatal("could not generate a legacy ciphertext starting with the v2 version byte")
	}
	dec, err := DecryptSecretKey(base64.StdEncoding.EncodeToString(raw))
	if err != nil {
		t.Fatalf("legacy ciphertext with v2 leading byte failed to decrypt: %v", err)
	}
	if dec != secret {
		t.Errorf("mismatch: got %q want %q", dec, secret)
	}
}

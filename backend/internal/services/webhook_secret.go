package services

import (
	"strings"

	"bkt/internal/security"
)

// Webhook secrets are stored encrypted (security.EncryptSecretKey, the same
// AES-GCM scheme as S3 credentials) with a version prefix so legacy plaintext
// values written before encryption stay readable (they are used as-is until
// the secret is next saved).
const webhookSecretEncPrefix = "enc:v2:"

// SealWebhookSecret returns the at-rest form of a webhook secret ("" stays "").
func SealWebhookSecret(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	ct, err := security.EncryptSecretKey(plain)
	if err != nil {
		return "", err
	}
	return webhookSecretEncPrefix + ct, nil
}

// webhookSecretPlain returns the plaintext of a stored webhook secret,
// accepting both the encrypted form and legacy plaintext.
func webhookSecretPlain(stored string) (string, error) {
	if !strings.HasPrefix(stored, webhookSecretEncPrefix) {
		return stored, nil
	}
	return security.DecryptSecretKey(strings.TrimPrefix(stored, webhookSecretEncPrefix))
}

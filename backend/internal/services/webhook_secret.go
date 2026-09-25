package services

import (
	"fmt"
	"log"
	"strings"

	"bkt/internal/security"

	"gorm.io/gorm"
)

// Webhook secrets are stored encrypted (security.EncryptSecretKey, the same
// AES-GCM scheme as S3 credentials) with a version prefix so legacy plaintext
// values written before encryption stay readable (they are used as-is until
// they are sealed at startup by SealLegacyWebhookSecrets or next saved).
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

const sealLegacyBatch = 200

// SealLegacyWebhookSecrets encrypts every legacy plaintext bucket webhook
// secret (non-empty, no "enc:v2:" prefix) in place. Idempotent and safe to
// run on every start and from several replicas: each value is verified to
// decrypt back to the same plaintext before it is written, and each row is
// updated with a compare-and-swap on the old value (a concurrent save wins).
// Secrets are never logged. Returns the number of rows sealed.
func SealLegacyWebhookSecrets(db *gorm.DB) (int, error) {
	if db == nil {
		return 0, fmt.Errorf("database not initialized")
	}
	sealed, skipped := 0, 0
	last := "00000000-0000-0000-0000-000000000000"
	for {
		var rows []struct {
			ID            string
			WebhookSecret string
		}
		if err := db.Table("buckets").Select("id::text AS id, webhook_secret").
			Where("id > ?::uuid AND webhook_secret <> '' AND webhook_secret NOT LIKE ?", last, webhookSecretEncPrefix+"%").
			Order("id").Limit(sealLegacyBatch).Find(&rows).Error; err != nil {
			return sealed, fmt.Errorf("scan buckets: %w", err)
		}
		for _, r := range rows {
			last = r.ID
			enc, err := SealWebhookSecret(r.WebhookSecret)
			if err != nil {
				return sealed, fmt.Errorf("seal webhook secret: %w", err)
			}
			if back, err := webhookSecretPlain(enc); err != nil || back != r.WebhookSecret {
				return sealed, fmt.Errorf("seal webhook secret: round-trip verification failed")
			}
			res := db.Exec(`UPDATE buckets SET webhook_secret = ? WHERE id = ?::uuid AND webhook_secret = ?`, enc, r.ID, r.WebhookSecret)
			if res.Error != nil {
				return sealed, fmt.Errorf("update bucket webhook secret: %w", res.Error)
			}
			if res.RowsAffected == 1 {
				sealed++
			} else {
				skipped++
			}
		}
		if len(rows) < sealLegacyBatch {
			break
		}
	}
	if sealed > 0 || skipped > 0 {
		log.Printf("Webhook secrets: sealed %d legacy plaintext secret(s) at rest (%d changed concurrently, left as saved)", sealed, skipped)
	}
	return sealed, nil
}

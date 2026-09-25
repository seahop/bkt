package security

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"

	"gorm.io/gorm"
)

// ReencryptStats summarizes one ReencryptStoredSecrets pass.
type ReencryptStats struct {
	Scanned     int // encrypted values examined
	Reencrypted int // stale values rewritten under the primary key
	Failed      int // values no configured key could decrypt (left untouched)
	Skipped     int // values that changed concurrently (left for the next pass)
}

// encryptedColumns are the stored credentials this pass manages. Values in a
// column with a prefix are only handled when they carry it (the prefix is
// kept): bucket webhook secrets are stored as "enc:v2:" + EncryptSecretKey
// (services.SealWebhookSecret); legacy plaintext webhook secrets (no prefix)
// are left alone and get sealed when next saved.
var encryptedColumns = []struct {
	table   string
	columns []string
	prefix  string
}{
	{"access_keys", []string{"secret_key_encrypted"}, ""},
	{"s3_configurations", []string{"access_key_id", "secret_access_key"}, ""},
	{"buckets", []string{"webhook_secret"}, "enc:v2:"},
}

const reencryptBatch = 200

// HasDecryptOnlyKeys reports whether any decrypt-only key (ENCRYPTION_KEY_PREVIOUS,
// the JWT_SECRET fallback, ENCRYPTION_LEGACY_JWT_SECRET) is configured.
func HasDecryptOnlyKeys() bool {
	if _, err := getSecretMaterial(); err != nil {
		return false
	}
	return len(decryptOnlySources) > 0
}

// ReencryptStoredSecrets rewrites, under the primary key, every stored
// credential that is readable only through a decrypt-only key or is still in
// the legacy v1 format, so the decrypt-only keys can be removed later.
//
// It is idempotent and safe to run on every start: a value is only written
// after it decrypted successfully and its new ciphertext was verified to
// decrypt back to the same plaintext with the primary key; each row is
// updated in its own transaction with a compare-and-swap on the old
// ciphertext (a concurrent change wins); values nothing can decrypt are never
// touched. Values are never logged.
func ReencryptStoredSecrets(db *gorm.DB) (ReencryptStats, error) {
	var st ReencryptStats
	if db == nil {
		return st, fmt.Errorf("database not initialized")
	}
	if _, err := getSecretMaterial(); err != nil {
		return st, err
	}
	// Without decrypt-only keys a v2 value is either readable with the
	// primary key (current) or unreadable (nothing to do), so only v1
	// candidates need the (expensive, PBKDF2) decryption attempt.
	fullScan := len(decryptOnlySources) > 0

	for _, tc := range encryptedColumns {
		for offset := 0; ; offset += reencryptBatch {
			var rows []map[string]interface{}
			sel := "id::text AS id"
			for _, c := range tc.columns {
				sel += ", " + c
			}
			if err := db.Table(tc.table).Select(sel).Order("id").Offset(offset).Limit(reencryptBatch).Find(&rows).Error; err != nil {
				return st, fmt.Errorf("scan %s: %w", tc.table, err)
			}
			for _, row := range rows {
				id, _ := row["id"].(string)
				updates := map[string]string{} // column -> new ciphertext
				olds := map[string]string{}
				for _, c := range tc.columns {
					old, _ := row[c].(string)
					if old == "" || !strings.HasPrefix(old, tc.prefix) {
						continue
					}
					ct := strings.TrimPrefix(old, tc.prefix)
					if !fullScan && !maybeLegacyFormat(ct) {
						continue
					}
					st.Scanned++
					plain, stale, err := DecryptSecretKeyStatus(ct)
					if err != nil {
						st.Failed++
						log.Printf("re-encrypt: %s.%s id=%s cannot be decrypted with any configured key (left unchanged)", tc.table, c, id)
						continue
					}
					if !stale {
						continue
					}
					fresh, err := EncryptSecretKey(plain)
					if err != nil {
						return st, fmt.Errorf("encrypt: %w", err)
					}
					// Verify the new ciphertext before it replaces the old one.
					if back, stillStale, err := DecryptSecretKeyStatus(fresh); err != nil || stillStale || back != plain {
						return st, fmt.Errorf("re-encrypt self-check failed for %s.%s id=%s", tc.table, c, id)
					}
					updates[c], olds[c] = tc.prefix+fresh, old
				}
				if len(updates) == 0 {
					continue
				}
				err := db.Transaction(func(tx *gorm.DB) error {
					for c, fresh := range updates {
						res := tx.Table(tc.table).Where("id::text = ? AND "+c+" = ?", id, olds[c]).Update(c, fresh)
						if res.Error != nil {
							return res.Error
						}
						if res.RowsAffected != 1 {
							return errConcurrentChange
						}
					}
					return nil
				})
				switch {
				case err == nil:
					st.Reencrypted += len(updates)
				case errors.Is(err, errConcurrentChange):
					st.Skipped += len(updates)
				default:
					return st, fmt.Errorf("update %s id=%s: %w", tc.table, id, err)
				}
			}
			if len(rows) < reencryptBatch {
				break
			}
		}
	}
	return st, nil
}

var errConcurrentChange = fmt.Errorf("row changed concurrently")

// maybeLegacyFormat reports whether a stored value could be a v1 ciphertext
// (anything not starting with the v2 version byte; ~1/256 of v1 blobs do
// start with it and are only found on a full scan).
func maybeLegacyFormat(enc string) bool {
	raw, err := base64.StdEncoding.DecodeString(enc)
	return err == nil && (len(raw) == 0 || raw[0] != encVersionV2)
}

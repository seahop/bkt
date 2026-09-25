package api

import (
	"time"

	"bkt/internal/database"
	"bkt/internal/models"

	"github.com/google/uuid"
)

// Access-key status as shown to users and admins. Both views derive it the
// same way so they can't disagree about which keys work.
const (
	keyStatusActive  = "active"
	keyStatusExpired = "expired"
	keyStatusRevoked = "revoked"
)

// accessKeyStatus mirrors what the SigV4 verifier accepts: a key works only
// if it is active, not expired, and (for STS credentials) was issued under the
// user's current session version.
func accessKeyStatus(k *models.AccessKey, userTokenVersion int, now time.Time) string {
	switch {
	case !k.IsActive:
		return keyStatusRevoked
	case k.ExpiresAt != nil && !now.Before(*k.ExpiresAt):
		return keyStatusExpired
	case k.Temporary && k.IssuerTokenVersion != nil && *k.IssuerTokenVersion != userTokenVersion:
		return keyStatusRevoked
	default:
		return keyStatusActive
	}
}

// annotateKeyStatuses fills Status on every key.
func annotateKeyStatuses(keys []models.AccessKey, userTokenVersion int) {
	now := time.Now()
	for i := range keys {
		keys[i].Status = accessKeyStatus(&keys[i], userTokenVersion, now)
	}
}

// userTokenVersion returns the user's current session version (0 if unknown).
func userTokenVersion(userID uuid.UUID) int {
	var u models.User
	if err := database.DB.Select("token_version").First(&u, "id = ?", userID).Error; err != nil {
		return 0
	}
	return u.TokenVersion
}

// ownKeyListing is the key list a user sees for themselves: long-lived keys
// that haven't been revoked (expired ones are kept, flagged, so the user knows
// to replace them) plus STS temporary credentials that still work. Dead
// temporary credentials are omitted — they can't be used and are cleaned up.
func ownKeyListing(keys []models.AccessKey) []models.AccessKey {
	out := make([]models.AccessKey, 0, len(keys))
	for _, k := range keys {
		if k.Temporary && k.Status != keyStatusActive {
			continue
		}
		if !k.Temporary && k.Status == keyStatusRevoked {
			continue
		}
		out = append(out, k)
	}
	return out
}

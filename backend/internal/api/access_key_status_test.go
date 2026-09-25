package api

import (
	"testing"
	"time"

	"bkt/internal/models"
)

func TestAccessKeyStatusAndOwnListing(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	v1, v2 := 1, 2
	keys := []models.AccessKey{
		{Name: "long", IsActive: true},
		{Name: "long-expired", IsActive: true, ExpiresAt: &past},
		{Name: "long-revoked", IsActive: false},
		{Name: "sts-ok", IsActive: true, Temporary: true, ExpiresAt: &future, IssuerTokenVersion: &v2},
		{Name: "sts-stale", IsActive: true, Temporary: true, ExpiresAt: &future, IssuerTokenVersion: &v1},
		{Name: "sts-expired", IsActive: true, Temporary: true, ExpiresAt: &past, IssuerTokenVersion: &v2},
	}
	annotateKeyStatuses(keys, 2)
	want := map[string]string{
		"long": keyStatusActive, "long-expired": keyStatusExpired, "long-revoked": keyStatusRevoked,
		"sts-ok": keyStatusActive, "sts-stale": keyStatusRevoked, "sts-expired": keyStatusExpired,
	}
	for _, k := range keys {
		if k.Status != want[k.Name] {
			t.Errorf("%s: status %q, want %q", k.Name, k.Status, want[k.Name])
		}
	}
	got := map[string]bool{}
	for _, k := range ownKeyListing(keys) {
		got[k.Name] = true
	}
	// The user's own list: working keys, plus expired long-lived keys (flagged
	// so they get replaced); never revoked keys or dead temporary credentials.
	for name, shown := range map[string]bool{
		"long": true, "long-expired": true, "long-revoked": false,
		"sts-ok": true, "sts-stale": false, "sts-expired": false,
	} {
		if got[name] != shown {
			t.Errorf("%s shown=%v, want %v", name, got[name], shown)
		}
	}
}

package api

import (
	"strings"
	"testing"
	"time"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/google/uuid"
)

func TestLifecyclePrincipalFor(t *testing.T) {
	configurer := uuid.New()
	ownerID := uuid.New()
	activeAdmin := &models.User{ID: ownerID, IsAdmin: true}
	lockedAdmin := &models.User{ID: ownerID, IsAdmin: true, IsLocked: true}
	plainOwner := &models.User{ID: ownerID}

	cases := []struct {
		name   string
		by     *uuid.UUID
		owner  *models.User
		want   uuid.UUID
		wantOK bool
	}{
		{"recorded configurer wins (even over an admin owner)", &configurer, activeAdmin, configurer, true},
		{"recorded configurer, owner irrelevant", &configurer, nil, configurer, true},
		{"legacy: active admin owner", nil, activeAdmin, ownerID, true},
		{"legacy: locked admin owner", nil, lockedAdmin, uuid.Nil, false},
		{"legacy: non-admin owner", nil, plainOwner, uuid.Nil, false},
		{"legacy: owner missing", nil, nil, uuid.Nil, false},
	}
	for _, tc := range cases {
		got, ok := lifecyclePrincipalFor(tc.by, tc.owner)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("%s: got (%v,%v) want (%v,%v)", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

// Expiry is authorized per key with the configurer's current permissions: a
// narrower Deny (or no Allow) keeps those keys out of the sweep.
func TestLifecycleExpiryPerKeyAuthorization(t *testing.T) {
	allowAll := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:*"],"Resource":["*"]}]}`
	denyKeep := `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["s3:DeleteObject"],"Resource":["arn:aws:s3:::logs/keep/*"]}]}`
	onlyTmp := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:DeleteObject"],"Resource":["arn:aws:s3:::logs/tmp/*"]}]}`

	eval := evalFor(t, "logs", allowAll, denyKeep)
	for key, want := range map[string]bool{"a.log": true, "keep/a.log": false, "deep/keep/a.log": true} {
		if got := lifecycleExpiryAllowed(eval, key); got != want {
			t.Errorf("allow-all+deny keep/: %q got %v want %v", key, got, want)
		}
	}

	narrow := evalFor(t, "logs", onlyTmp)
	for key, want := range map[string]bool{"tmp/x": true, "x": false, "keep/x": false} {
		if got := lifecycleExpiryAllowed(narrow, key); got != want {
			t.Errorf("tmp-only: %q got %v want %v", key, got, want)
		}
	}

	if lifecycleExpiryAllowed(evalFor(t, "logs"), "a.log") {
		t.Error("a configurer without any permission must expire nothing")
	}
	locked := models.User{Username: "bob", IsAdmin: true, IsLocked: true}
	if lifecycleExpiryAllowed(services.NewAccessEvaluatorFromData(&locked, "logs", true, nil), "a.log") {
		t.Error("a locked configurer must expire nothing")
	}
	admin := models.User{Username: "root", IsAdmin: true}
	if !lifecycleExpiryAllowed(services.NewAccessEvaluatorFromData(&admin, "logs", true, nil), "any/key") {
		t.Error("an active admin configurer may expire any key")
	}
}

// Postgres-gated: storing a rule records the configurer; clearing it clears
// the provenance; legacy configs resolve to an active-admin owner only.
func TestIntegrationLifecycleProvenance(t *testing.T) {
	integrationDB(t)
	owner := mkUser(t, "lco-"+uuid.NewString()[:8], "pw-unused-123", false, false)
	configurer := mkUser(t, "lcc-"+uuid.NewString()[:8], "pw-unused-123", false, false)
	b := models.Bucket{ID: uuid.New(), Name: "lc-" + uuid.NewString()[:8], OwnerID: owner.ID}
	if err := database.DB.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.DB.Exec(`DELETE FROM buckets WHERE id = ?`, b.ID) })
	reload := func() models.Bucket {
		var r models.Bucket
		if err := database.DB.First(&r, "id = ?", b.ID).Error; err != nil {
			t.Fatal(err)
		}
		return r
	}

	if err := storeLifecycleConfig(&b, &models.LifecycleConfig{ExpireDays: 7}, configurer.ID); err != nil {
		t.Fatal(err)
	}
	r := reload()
	if r.LifecycleConfiguredBy == nil || *r.LifecycleConfiguredBy != configurer.ID || r.LifecycleConfiguredAt == nil {
		t.Fatalf("configurer not recorded: %+v %+v", r.LifecycleConfiguredBy, r.LifecycleConfiguredAt)
	}
	if id, ok := lifecyclePrincipal(&r); !ok || id != configurer.ID {
		t.Errorf("principal = %v,%v want configurer", id, ok)
	}

	if err := storeLifecycleConfig(&b, nil, uuid.Nil); err != nil {
		t.Fatal(err)
	}
	r = reload()
	if r.Lifecycle != nil || r.LifecycleConfiguredBy != nil || r.LifecycleConfiguredAt != nil {
		t.Fatal("clearing lifecycle must clear its provenance")
	}

	// Legacy (no configurer): non-admin owner → skipped; admin owner → owner.
	if _, ok := lifecyclePrincipal(&r); ok {
		t.Error("legacy config with a non-admin owner must be skipped")
	}
	database.DB.Model(&models.User{}).Where("id = ?", owner.ID).Update("is_admin", true)
	if id, ok := lifecyclePrincipal(&r); !ok || id != owner.ID {
		t.Errorf("legacy config with an active admin owner: got %v,%v", id, ok)
	}

	// Candidate paging (so denied keys cannot starve the rest): keyset over
	// key for current objects, over (versioned_at, id) for versions.
	t.Cleanup(func() {
		database.DB.Exec(`DELETE FROM objects WHERE bucket_id = ?`, b.ID)
		database.DB.Exec(`DELETE FROM object_versions WHERE bucket_id = ?`, b.ID)
	})
	old := time.Now().Add(-48 * time.Hour)
	for _, k := range []string{"p/a", "p/b", "p/c", "q/d", "p/new"} {
		o := models.Object{ID: uuid.New(), BucketID: b.ID, Key: k, StoragePath: "x"}
		if err := database.DB.Create(&o).Error; err != nil {
			t.Fatal(err)
		}
		if k != "p/new" {
			database.DB.Exec(`UPDATE objects SET updated_at = ? WHERE id = ?`, old, o.ID)
		}
		for i := 0; i < 2; i++ {
			v := models.ObjectVersion{ID: uuid.New(), BucketID: b.ID, Key: k, VersionID: uuid.NewString(), VersionedAt: old.Add(time.Duration(i) * time.Minute)}
			if err := database.DB.Create(&v).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	cutoff := time.Now().Add(-time.Hour)
	var keys []string
	for after := ""; ; {
		page, err := lifecycleCurrentCandidates(b.ID, "p/", cutoff, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, o := range page {
			keys = append(keys, o.Key)
		}
		after = page[len(page)-1].Key
	}
	if strings.Join(keys, ",") != "p/a,p/b,p/c" {
		t.Errorf("current candidates = %v", keys)
	}
	var seen []time.Time
	count := 0
	var lastAt time.Time
	var lastID uuid.UUID
	for first := true; ; first = false {
		page, err := lifecycleVersionCandidates(b.ID, "", cutoff, !first, lastAt, lastID, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, v := range page {
			seen = append(seen, v.VersionedAt)
		}
		count += len(page)
		lastAt, lastID = page[len(page)-1].VersionedAt, page[len(page)-1].ID
	}
	if count != 10 {
		t.Errorf("version candidates paged = %d, want 10 (each exactly once)", count)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i].Before(seen[i-1]) {
			t.Fatal("version candidates must be oldest first across pages")
		}
	}
}

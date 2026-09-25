package database

import (
	"testing"

	"bkt/internal/config"
	"bkt/internal/database/pgtest"
	"bkt/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Postgres-gated (BKT_TEST_POSTGRES_HOST, see pgtest): the one-time S3
// routing migration against a real database, each test in its own database.

func routingFixture(t *testing.T, db *gorm.DB) (owner models.User) {
	t.Helper()
	owner = models.User{Username: "owner", Email: "owner@example.test"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	return owner
}

func mkS3Config(t *testing.T, db *gorm.DB, name string, isDefault bool) models.S3Configuration {
	t.Helper()
	c := models.S3Configuration{Name: name, Endpoint: "s3.invalid", Region: "us-east-1",
		AccessKeyID: "x", SecretAccessKey: "y", IsDefault: isDefault}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	return c
}

func mkRoutingBucket(t *testing.T, db *gorm.DB, owner uuid.UUID, name, backend string, cfgID *uuid.UUID) {
	t.Helper()
	b := models.Bucket{Name: name, OwnerID: owner, StorageBackend: backend, S3ConfigID: cfgID}
	if err := db.Create(&b).Error; err != nil {
		t.Fatal(err)
	}
}

func routingOf(t *testing.T, db *gorm.DB) map[string]*uuid.UUID {
	t.Helper()
	var bs []models.Bucket
	if err := db.Find(&bs).Error; err != nil {
		t.Fatal(err)
	}
	out := make(map[string]*uuid.UUID, len(bs))
	for _, b := range bs {
		out[b.Name] = b.S3ConfigID
	}
	return out
}

func sameID(a *uuid.UUID, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestIntegrationS3RoutingMigration(t *testing.T) {
	s, name := pgtest.NewDatabase(t, "database")

	// Build the pre-release state: schema only, no data migrations yet.
	db := pgtest.Open(t, s, name)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	owner := routingFixture(t, db)
	other := mkS3Config(t, db, "other", false)
	def := mkS3Config(t, db, "default", true)
	mkRoutingBucket(t, db, owner.ID, "photos", "s3", nil)
	mkRoutingBucket(t, db, owner.ID, "logs", "s3", nil)
	mkRoutingBucket(t, db, owner.ID, "env-a", "s3", nil) // provisioned from S3_BUCKETS
	mkRoutingBucket(t, db, owner.ID, "pinned", "s3", &other.ID)
	mkRoutingBucket(t, db, owner.ID, "disk", "local", nil)

	// Run through the real startup path (Initialize → runDataMigrations with
	// the configured S3_BUCKETS).
	cfg := &config.Config{Database: s.DatabaseConfig(name)}
	cfg.Storage.S3.Buckets = []string{"env-a", "not-a-bucket"}
	prevDB := DB
	t.Cleanup(func() {
		if DB != nil && DB != prevDB {
			if sqlDB, err := DB.DB(); err == nil {
				_ = sqlDB.Close()
			}
		}
		DB = prevDB
	})
	if err := Initialize(cfg); err != nil {
		t.Fatal(err)
	}

	want := map[string]*uuid.UUID{"photos": &def.ID, "logs": &def.ID, "env-a": nil, "pinned": &other.ID, "disk": nil}
	got := routingOf(t, db)
	for n, w := range want {
		if !sameID(got[n], w) {
			t.Errorf("after migration %s -> %v, want %v", n, got[n], w)
		}
	}
	var recorded int64
	db.Table("bkt_data_migrations").Where("name = ?", migrationPinS3BucketRouting).Count(&recorded)
	if recorded != 1 {
		t.Fatalf("migration recorded %d times", recorded)
	}

	// It runs once: later unpinned buckets and operator changes are left
	// alone by every subsequent startup.
	mkRoutingBucket(t, db, owner.ID, "later", "s3", nil)
	if err := db.Model(&models.Bucket{}).Where("name = ?", "photos").Update("s3_config_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := runDataMigrations(db, []string{"env-a"}); err != nil {
		t.Fatal(err)
	}
	if err := Initialize(cfg); err != nil {
		t.Fatal(err)
	}
	got = routingOf(t, db)
	if got["later"] != nil || got["photos"] != nil {
		t.Errorf("migration ran again: later=%v photos=%v", got["later"], got["photos"])
	}
}

func TestIntegrationS3RoutingMigrationWithoutDefault(t *testing.T) {
	s, name := pgtest.NewDatabase(t, "database")
	db := pgtest.Open(t, s, name)
	if err := runMigrations(db); err != nil {
		t.Fatal(err)
	}
	owner := routingFixture(t, db)
	mkS3Config(t, db, "not-default", false)
	mkRoutingBucket(t, db, owner.ID, "photos", "s3", nil)

	if err := runDataMigrations(db, nil); err != nil {
		t.Fatal(err)
	}
	if got := routingOf(t, db); got["photos"] != nil {
		t.Errorf("without a default config nothing may be pinned: photos -> %v", got["photos"])
	}
	// Recorded anyway: creating a default later must not pin retroactively.
	var recorded int64
	db.Table("bkt_data_migrations").Where("name = ?", migrationPinS3BucketRouting).Count(&recorded)
	if recorded != 1 {
		t.Fatalf("migration recorded %d times", recorded)
	}
	mkS3Config(t, db, "default", true)
	if err := runDataMigrations(db, nil); err != nil {
		t.Fatal(err)
	}
	if got := routingOf(t, db); got["photos"] != nil {
		t.Errorf("migration re-ran after a default appeared: photos -> %v", got["photos"])
	}
}

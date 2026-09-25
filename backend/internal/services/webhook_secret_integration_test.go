package services

import (
	"strings"
	"testing"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/database/pgtest"
	"bkt/internal/models"

	"github.com/google/uuid"
)

// Postgres-gated: BKT_TEST_POSTGRES_HOST=pg BKT_TEST_POSTGRES_PASSWORD=t go test ./internal/services -run Integration
func servicesIntegrationDB(t *testing.T) {
	t.Helper()
	s, name := pgtest.NewDatabase(t, "services")
	prev := database.DB
	cfg := &config.Config{}
	cfg.Database = s.DatabaseConfig(name)
	if err := database.Initialize(cfg); err != nil {
		t.Fatalf("db init: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := database.DB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		database.DB = prev
	})
}

func TestIntegrationSealLegacyWebhookSecrets(t *testing.T) {
	servicesIntegrationDB(t)
	t.Setenv("ENCRYPTION_KEY", "test-encryption-key-for-webhook-secrets-0123456789")
	db := database.DB

	n := "seal-" + uuid.NewString()[:8]
	owner := models.User{ID: uuid.New(), Username: n, Email: n + "@example.test", Password: "x"}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	already, err := SealWebhookSecret("already-sealed")
	if err != nil {
		t.Fatal(err)
	}
	secrets := map[string]string{"plain": "legacy-plain-secret", "sealed": already, "empty": ""}
	ids := map[string]uuid.UUID{}
	for kind, sec := range secrets {
		b := models.Bucket{ID: uuid.New(), Name: n + "-" + kind, OwnerID: owner.ID, WebhookSecret: sec}
		if err := db.Create(&b).Error; err != nil {
			t.Fatal(err)
		}
		ids[kind] = b.ID
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM buckets WHERE owner_id = ?`, owner.ID)
		db.Exec(`DELETE FROM users WHERE id = ?`, owner.ID)
	})
	stored := func(kind string) string {
		var s string
		db.Raw(`SELECT webhook_secret FROM buckets WHERE id = ?`, ids[kind]).Scan(&s)
		return s
	}

	count, err := SealLegacyWebhookSecrets(db)
	if err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatalf("sealed %d, want >= 1", count)
	}
	got := stored("plain")
	if !strings.HasPrefix(got, webhookSecretEncPrefix) || strings.Contains(got, "legacy-plain-secret") {
		t.Fatalf("plaintext secret not sealed: %q", got)
	}
	if p, err := webhookSecretPlain(got); err != nil || p != "legacy-plain-secret" {
		t.Fatalf("sealed value does not round-trip: %q %v", p, err)
	}
	if stored("sealed") != already {
		t.Error("already-sealed secret must not be rewritten")
	}
	if stored("empty") != "" {
		t.Error("empty secret must stay empty")
	}

	// Idempotent: a second pass finds nothing of ours to do.
	if _, err := SealLegacyWebhookSecrets(db); err != nil {
		t.Fatal(err)
	}
	if stored("plain") != got {
		t.Error("second pass must not re-seal an already sealed secret")
	}
}

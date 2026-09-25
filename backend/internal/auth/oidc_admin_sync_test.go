package auth

import (
	"testing"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/database/pgtest"
	"bkt/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestLastActiveAdmin(t *testing.T) {
	me, other := uuid.New(), uuid.New()
	cases := []struct {
		name   string
		admins []models.User
		want   bool
	}{
		{"only me", []models.User{{ID: me}}, true},
		{"other active admin", []models.User{{ID: me}, {ID: other}}, false},
		{"other admin locked", []models.User{{ID: me}, {ID: other, IsLocked: true}}, true},
		{"no admins at all", nil, true},
	}
	for _, tc := range cases {
		if got := lastActiveAdmin(tc.admins, me); got != tc.want {
			t.Errorf("%s: lastActiveAdmin=%v want %v", tc.name, got, tc.want)
		}
	}
	if stillAdmin([]models.User{{ID: other}}, me) {
		t.Error("stillAdmin must be false when the user is not among the admin rows")
	}
}

// Postgres-gated: BKT_TEST_POSTGRES_HOST=pg BKT_TEST_POSTGRES_PASSWORD=t go test ./internal/auth -run Integration
func authIntegrationDB(t *testing.T) {
	t.Helper()
	s, name := pgtest.NewDatabase(t, "auth")
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

// The whole scenario runs inside one outer transaction that is rolled back;
// pre-existing admins (none in the fresh test database, but be robust) are
// hidden inside it so the "last admin" arithmetic is exact.
func TestIntegrationOIDCDemotionKeepsLastAdmin(t *testing.T) {
	authIntegrationDB(t)
	tx := database.DB.Begin()
	defer tx.Rollback()
	if err := tx.Exec(`UPDATE users SET is_admin = false WHERE is_admin`).Error; err != nil {
		t.Fatal(err)
	}
	mk := func(admin, locked bool) *models.User {
		n := "adm-" + uuid.NewString()[:8]
		u := &models.User{ID: uuid.New(), Username: n, Email: n + "@example.test", Password: "x", IsAdmin: admin, IsLocked: locked}
		if err := tx.Create(u).Error; err != nil {
			t.Fatal(err)
		}
		return u
	}
	stored := func(u *models.User) bool {
		var row models.User
		if err := tx.First(&row, "id = ?", u.ID).Error; err != nil {
			t.Fatal(err)
		}
		return row.IsAdmin
	}
	demote := func(db *gorm.DB, u *models.User) bool {
		kept, err := syncAdminFlag(db, u, false)
		if err != nil {
			t.Fatal(err)
		}
		return kept
	}

	sole := mk(true, false)
	lockedAdmin := mk(true, true)
	if !demote(tx, sole) || !sole.IsAdmin || !stored(sole) {
		t.Fatal("sole active admin (other admin locked) must be kept admin")
	}

	second := mk(true, false)
	if demote(tx, sole) || sole.IsAdmin || stored(sole) {
		t.Fatal("demotion must apply when another active admin exists")
	}
	// Now `second` is the last active admin; the locked admin doesn't count.
	if !demote(tx, second) || !stored(second) {
		t.Fatal("last active admin must be kept")
	}
	_ = lockedAdmin

	// Promotion is never blocked.
	if kept, err := syncAdminFlag(tx, sole, true); err != nil || kept || !stored(sole) {
		t.Fatalf("promotion failed: kept=%v err=%v", kept, err)
	}
}

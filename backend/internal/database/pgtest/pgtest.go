// Package pgtest provisions throwaway PostgreSQL databases for the
// Postgres-gated integration tests. It is imported only by _test.go files.
//
// Tests are gated on BKT_TEST_POSTGRES_HOST (skipped when unset) and read:
//
//	BKT_TEST_POSTGRES_HOST      server host (required)
//	BKT_TEST_POSTGRES_PORT      default 5432
//	BKT_TEST_POSTGRES_USER      default postgres (must be allowed to CREATE DATABASE)
//	BKT_TEST_POSTGRES_PASSWORD  default empty
//	BKT_TEST_POSTGRES_DB        maintenance database to connect to, default postgres
//
// Every test binary (or test) works in its OWN freshly created database named
// bkt_it_<prefix>_<random>, dropped afterwards. That keeps packages that `go
// test ./...` runs in parallel against one server from seeing each other's
// rows, global UPDATEs, or DROP TABLEs, and leaves the maintenance database
// untouched.
package pgtest

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"bkt/internal/config"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Settings are the connection settings of the test server.
type Settings struct {
	Host, Port, User, Password, AdminDB string
}

// FromEnv reads Settings from the environment; ok=false when
// BKT_TEST_POSTGRES_HOST is unset (Postgres-gated tests should then skip).
func FromEnv() (Settings, bool) {
	s := Settings{
		Host:     os.Getenv("BKT_TEST_POSTGRES_HOST"),
		Port:     envOr("BKT_TEST_POSTGRES_PORT", "5432"),
		User:     envOr("BKT_TEST_POSTGRES_USER", "postgres"),
		Password: os.Getenv("BKT_TEST_POSTGRES_PASSWORD"),
		AdminDB:  envOr("BKT_TEST_POSTGRES_DB", "postgres"),
	}
	return s, s.Host != ""
}

// Require is FromEnv that skips tb when no test server is configured.
func Require(tb testing.TB) Settings {
	tb.Helper()
	s, ok := FromEnv()
	if !ok {
		tb.Skip("BKT_TEST_POSTGRES_HOST not set")
	}
	return s
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func quote(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

// DSN is a libpq keyword/value DSN for dbName on the test server.
func (s Settings) DSN(dbName string) string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		quote(s.Host), quote(s.Port), quote(s.User), quote(s.Password), quote(dbName))
}

// DatabaseConfig is the bkt config for dbName on the test server.
func (s Settings) DatabaseConfig(dbName string) config.DatabaseConfig {
	return config.DatabaseConfig{Host: s.Host, Port: s.Port, User: s.User,
		Password: s.Password, DBName: dbName, SSLMode: "disable"}
}

var validName = regexp.MustCompile(`^bkt_it_[a-z0-9_]+$`)

func (s Settings) admin(stmt string) error {
	db, err := gorm.Open(postgres.Open(s.DSN(s.AdminDB)), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return err
	}
	if sqlDB, err := db.DB(); err == nil {
		defer sqlDB.Close() //nolint:errcheck // test helper
	}
	return db.Exec(stmt).Error
}

// CreateDatabase creates an empty database bkt_it_<prefix>_<random> and
// returns its name. The caller drops it with DropDatabase.
func (s Settings) CreateDatabase(prefix string) (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	name := "bkt_it_" + strings.ToLower(prefix) + "_" + hex.EncodeToString(b[:])
	if !validName.MatchString(name) {
		return "", fmt.Errorf("invalid test database prefix %q", prefix)
	}
	if err := s.admin(`CREATE DATABASE "` + name + `"`); err != nil {
		return "", fmt.Errorf("create test database %s: %w", name, err)
	}
	return name, nil
}

// DropDatabase drops a database made by CreateDatabase, terminating any
// connections still open to it.
func (s Settings) DropDatabase(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("refusing to drop %q: not a test database", name)
	}
	return s.admin(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`)
}

// NewDatabase creates a fresh database for one test and drops it when the
// test ends.
func NewDatabase(tb testing.TB, prefix string) (Settings, string) {
	tb.Helper()
	s := Require(tb)
	name, err := s.CreateDatabase(prefix)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := s.DropDatabase(name); err != nil {
			tb.Logf("drop test database: %v", err)
		}
	})
	return s, name
}

// Open opens dbName with SQL logging discarded; the connection is closed
// when tb ends.
func Open(tb testing.TB, s Settings, dbName string) *gorm.DB {
	tb.Helper()
	db, err := gorm.Open(postgres.Open(s.DSN(dbName)), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		tb.Fatal(err)
	}
	if sqlDB, err := db.DB(); err == nil {
		tb.Cleanup(func() { _ = sqlDB.Close() })
	}
	return db
}

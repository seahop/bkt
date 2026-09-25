package database

import (
	"errors"
	"fmt"
	"sort"

	"bkt/internal/logger"
	"bkt/internal/models"

	"gorm.io/gorm"
)

// Data migrations are one-time transformations of existing rows (as opposed
// to AutoMigrate's additive schema changes). Each is recorded by name in
// bkt_data_migrations once applied, so it never runs twice — re-running a
// routing migration after operators have changed configs would itself
// re-route buckets.
const dataMigrationsTableDDL = `CREATE TABLE IF NOT EXISTS bkt_data_migrations (
	name       text PRIMARY KEY,
	applied_at timestamptz NOT NULL DEFAULT now()
)`

// migrationPinS3BucketRouting pins S3 buckets that relied on the dynamic
// "DB default config" resolution to that config's id. From this release on a
// nil s3_config_id always means the .env S3 backend.
const migrationPinS3BucketRouting = "2026-09-pin-s3-bucket-routing"

// runDataMigrations applies pending one-time data migrations. It runs under
// the migration advisory lock (see Initialize).
func runDataMigrations(db *gorm.DB, envS3Buckets []string) error {
	if err := db.Exec(dataMigrationsTableDDL).Error; err != nil {
		return fmt.Errorf("failed to create bkt_data_migrations: %w", err)
	}
	return applyDataMigration(db, migrationPinS3BucketRouting, func(tx *gorm.DB) error {
		return pinS3BucketRouting(tx, envS3Buckets)
	})
}

// applyDataMigration runs fn and records name in one transaction, unless name
// is already recorded.
func applyDataMigration(db *gorm.DB, name string, fn func(tx *gorm.DB) error) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Table("bkt_data_migrations").Where("name = ?", name).Count(&n).Error; err != nil {
			return fmt.Errorf("data migration %s: %w", name, err)
		}
		if n > 0 {
			return nil
		}
		if err := fn(tx); err != nil {
			return fmt.Errorf("data migration %s: %w", name, err)
		}
		if err := tx.Exec("INSERT INTO bkt_data_migrations (name) VALUES (?)", name).Error; err != nil {
			return fmt.Errorf("data migration %s: record: %w", name, err)
		}
		return nil
	})
}

// s3RoutingPlan is the outcome of planS3RoutingPins.
type s3RoutingPlan struct {
	Pin     []string // bucket names to pin to the default config
	EnvKept []string // S3_BUCKETS buckets left on the .env backend
}

// planS3RoutingPins decides, for the S3 buckets with no pinned config, which
// are pinned to the current default configuration: all of them except the
// buckets provisioned from S3_BUCKETS, which were created on (and belong to)
// the .env backend. With no default configuration nothing changes: those
// buckets were already served by the .env backend.
func planS3RoutingPins(unpinned []string, hasDefault bool, envS3Buckets []string) s3RoutingPlan {
	var plan s3RoutingPlan
	if !hasDefault {
		return plan
	}
	env := make(map[string]bool, len(envS3Buckets))
	for _, n := range envS3Buckets {
		env[n] = true
	}
	for _, name := range unpinned {
		if env[name] {
			plan.EnvKept = append(plan.EnvKept, name)
		} else {
			plan.Pin = append(plan.Pin, name)
		}
	}
	sort.Strings(plan.Pin)
	sort.Strings(plan.EnvKept)
	return plan
}

// pinS3BucketRouting: before this release an S3 bucket with a nil
// s3_config_id was served by whichever S3 configuration was the DB default
// at request time (falling back to .env), so creating or switching the
// default silently re-routed existing buckets. Preserve today's routing by
// pinning those buckets to the current default.
func pinS3BucketRouting(tx *gorm.DB, envS3Buckets []string) error {
	var def models.S3Configuration
	hasDefault := true
	if err := tx.Where("is_default = ?", true).Order("created_at ASC").First(&def).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		hasDefault = false
	}

	var unpinned []string
	if err := tx.Model(&models.Bucket{}).
		Where("storage_backend = ? AND s3_config_id IS NULL", "s3").
		Order("name ASC").Pluck("name", &unpinned).Error; err != nil {
		return err
	}

	plan := planS3RoutingPins(unpinned, hasDefault, envS3Buckets)
	if len(plan.Pin) > 0 {
		id := def.ID
		if err := tx.Model(&models.Bucket{}).
			Where("storage_backend = ? AND s3_config_id IS NULL AND name IN ?", "s3", plan.Pin).
			Update("s3_config_id", id).Error; err != nil {
			return err
		}
		logger.Info("Pinned S3 buckets to the default S3 configuration (one-time routing migration)", map[string]interface{}{
			"s3_config_id":   id.String(),
			"s3_config_name": def.Name,
			"buckets":        plan.Pin,
		})
	}
	if len(plan.EnvKept) > 0 {
		logger.Warn("S3_BUCKETS buckets stay on the .env S3 backend; until this release they were served through the default S3 configuration — verify their data location", map[string]interface{}{
			"s3_config_name": def.Name,
			"buckets":        plan.EnvKept,
		})
	}
	if !hasDefault || (len(plan.Pin) == 0 && len(plan.EnvKept) == 0) {
		logger.Info("S3 bucket routing migration: nothing to pin", nil)
	}
	return nil
}

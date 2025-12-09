package database

import (
	"fmt"
	"log"
	"time"
	"bkt/internal/config"
	"bkt/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

// Initialize connects to the database and runs migrations
func Initialize(cfg *config.Config) error {
	dsn := cfg.GetDSN()

	var err error
	DB, err = gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info),
	})

	if err != nil {
		return fmt.Errorf("failed to connect to database: %w", err)
	}

	// Configure connection pool to prevent resource exhaustion
	sqlDB, err := DB.DB()
	if err != nil {
		return fmt.Errorf("failed to get database instance: %w", err)
	}

	// Set maximum number of open connections (prevents exhausting database resources)
	sqlDB.SetMaxOpenConns(25)

	// Set maximum number of idle connections (reduces overhead)
	sqlDB.SetMaxIdleConns(10)

	// Set maximum lifetime of a connection (prevents stale connections)
	// 1 hour - forces connection refresh to pick up DNS/network changes
	sqlDB.SetConnMaxLifetime(time.Hour)

	log.Println("Database connection established")

	// Run auto migrations
	err = DB.AutoMigrate(
		&models.User{},
		&models.AccessKey{},
		&models.S3Configuration{},
		&models.Bucket{},
		&models.Object{},
		&models.Policy{},
		&models.BucketPolicy{},
	)

	if err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	log.Println("Database migrations completed")

	// Add custom indexes for performance (PostgreSQL-specific optimizations)
	// Create index for efficient LIKE prefix queries on object keys
	// Using text_pattern_ops operator class for better prefix matching performance
	err = DB.Exec(`
		CREATE INDEX IF NOT EXISTS idx_objects_key_pattern
		ON objects (bucket_id, key text_pattern_ops)
	`).Error
	if err != nil {
		// Log warning but don't fail - this is an optimization, not critical
		log.Printf("Warning: Failed to create pattern index: %v", err)
	} else {
		log.Println("Performance indexes created")
	}

	return nil
}

// GetDB returns the database instance
func GetDB() *gorm.DB {
	return DB
}

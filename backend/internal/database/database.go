package database

import (
	"fmt"
	"log"
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

	return nil
}

// GetDB returns the database instance
func GetDB() *gorm.DB {
	return DB
}

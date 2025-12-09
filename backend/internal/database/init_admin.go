package database

import (
	"errors"
	"log"
	"bkt/internal/config"
	"bkt/internal/models"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// InitializeDefaultAdmin creates the default admin user if it doesn't exist
func InitializeDefaultAdmin(cfg *config.Config) error {
	// Skip if no admin password is configured
	if cfg.Auth.AdminPassword == "" {
		log.Println("⚠️  No ADMIN_PASSWORD configured - skipping default admin creation")
		log.Println("   Run ./setup.sh to generate admin credentials")
		return nil
	}

	// Check if admin user already exists
	var existingUser models.User
	result := DB.Where("username = ?", cfg.Auth.AdminUsername).First(&existingUser)

	if result.Error == nil {
		// Admin user already exists
		log.Printf("✓ Default admin user '%s' already exists", cfg.Auth.AdminUsername)
		return nil
	}

	// If error is not "record not found", return it
	if !errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return result.Error
	}

	// Hash the password
	hashedPassword, err := bcrypt.GenerateFromPassword(
		[]byte(cfg.Auth.AdminPassword),
		cfg.Auth.BcryptCost,
	)
	if err != nil {
		return err
	}

	// Create the admin user
	adminUser := models.User{
		Username: cfg.Auth.AdminUsername,
		Email:    cfg.Auth.AdminEmail,
		Password: string(hashedPassword),
		IsAdmin:  true,
	}

	if err := DB.Create(&adminUser).Error; err != nil {
		return err
	}

	log.Println("========================================")
	log.Println("✅ DEFAULT ADMIN USER CREATED")
	log.Println("========================================")
	log.Printf("   Username: %s", cfg.Auth.AdminUsername)
	log.Printf("   Email:    %s", cfg.Auth.AdminEmail)
	log.Println("   Password: (from ADMIN_PASSWORD env var)")
	log.Println("========================================")
	log.Println("")

	return nil
}

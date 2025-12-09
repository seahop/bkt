package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Database   DatabaseConfig
	Server     ServerConfig
	Auth       AuthConfig
	Storage    StorageConfig
	TLS        TLSConfig
	CORS       CORSConfig
	GoogleSSO  GoogleSSOConfig
	VaultSSO   VaultSSOConfig
}

type DatabaseConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
	SSLMode  string
}

type ServerConfig struct {
	Port string
	Host string
}

type TLSConfig struct {
	Enabled  bool
	CertFile string
	KeyFile  string
	CAFile   string
}

type AuthConfig struct {
	JWTSecret            string
	AccessTokenExpiry    string
	RefreshTokenExpiry   string
	BcryptCost           int
	AdminUsername        string
	AdminPassword        string
	AdminEmail           string
	AllowRegistration    bool
}

type StorageConfig struct {
	Backend     string // "local" or "s3"
	RootPath    string // For local storage
	MaxFileSize int64
	S3          S3Config
}

type S3Config struct {
	Enabled         bool
	Endpoint        string // e.g., "s3.amazonaws.com" or MinIO endpoint
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	BucketPrefix    string // Prefix for all bucket names
	UseSSL          bool
	ForcePathStyle  bool   // Required for MinIO
}

type GoogleSSOConfig struct {
	Enabled      bool
	ClientID     string
	ClientSecret string
	RedirectURL  string
}

type VaultSSOConfig struct {
	Enabled  bool
	Address  string
	JWTPath  string
	Role     string
	Audience string
}

type CORSConfig struct {
	AllowedOrigins   []string
	AllowCredentials bool
}

func Load() *Config {
	return &Config{
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			User:     getEnv("DB_USER", "objectstore"),
			Password: getEnv("DB_PASSWORD", "objectstore_dev_password"),
			DBName:   getEnv("DB_NAME", "objectstore"),
			SSLMode:  getEnv("DB_SSL_MODE", "disable"),
		},
		Server: ServerConfig{
			Port: getEnv("SERVER_PORT", "9000"),
			Host: getEnv("SERVER_HOST", "0.0.0.0"),
		},
		Auth: AuthConfig{
			JWTSecret:          getEnv("JWT_SECRET", "dev_jwt_secret_change_in_production"),
			AccessTokenExpiry:  getEnv("ACCESS_TOKEN_EXPIRY", "15m"),
			RefreshTokenExpiry: getEnv("REFRESH_TOKEN_EXPIRY", "168h"), // 7 days
			BcryptCost:         12,
			AdminUsername:      getEnv("ADMIN_USERNAME", "admin"),
			AdminPassword:      getEnv("ADMIN_PASSWORD", ""),
			AdminEmail:         getEnv("ADMIN_EMAIL", "admin@localhost"),
			AllowRegistration:  getEnv("ALLOW_REGISTRATION", "false") == "true",
		},
		Storage: StorageConfig{
			Backend:     getEnv("STORAGE_BACKEND", "local"), // "local" or "s3"
			RootPath:    getEnv("STORAGE_ROOT", "/data/buckets"),
			MaxFileSize: 5 * 1024 * 1024 * 1024, // 5GB
			S3: S3Config{
				Enabled:         getEnv("S3_ENABLED", "false") == "true",
				Endpoint:        getEnv("S3_ENDPOINT", "s3.amazonaws.com"),
				Region:          getEnv("S3_REGION", "us-east-1"),
				AccessKeyID:     getEnv("S3_ACCESS_KEY_ID", ""),
				SecretAccessKey: getEnv("S3_SECRET_ACCESS_KEY", ""),
				BucketPrefix:    getEnv("S3_BUCKET_PREFIX", ""),
				UseSSL:          getEnv("S3_USE_SSL", "true") == "true",
				ForcePathStyle:  getEnv("S3_FORCE_PATH_STYLE", "false") == "true",
			},
		},
		TLS: TLSConfig{
			Enabled:  getEnv("TLS_ENABLED", "false") == "true",
			CertFile: getEnv("TLS_CERT_FILE", ""),
			KeyFile:  getEnv("TLS_KEY_FILE", ""),
			CAFile:   getEnv("TLS_CA_FILE", ""),
		},
		CORS: loadCORSConfig(),
		GoogleSSO: GoogleSSOConfig{
			Enabled:      getEnv("GOOGLE_SSO_ENABLED", "false") == "true",
			ClientID:     getEnv("GOOGLE_CLIENT_ID", ""),
			ClientSecret: getEnv("GOOGLE_CLIENT_SECRET", ""),
			RedirectURL:  getEnv("GOOGLE_REDIRECT_URL", "https://localhost:9443/api/auth/google/callback"),
		},
		VaultSSO: VaultSSOConfig{
			Enabled:  getEnv("VAULT_SSO_ENABLED", "false") == "true",
			Address:  getEnv("VAULT_ADDR", "https://vault.example.com:8200"),
			JWTPath:  getEnv("VAULT_JWT_PATH", "auth/jwt"),
			Role:     getEnv("VAULT_JWT_ROLE", "object-storage-users"),
			Audience: getEnv("VAULT_JWT_AUDIENCE", "object-storage"),
		},
	}
}

func (c *Config) GetDSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.Database.Host,
		c.Database.Port,
		c.Database.User,
		c.Database.Password,
		c.Database.DBName,
		c.Database.SSLMode,
	)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// loadCORSConfig loads CORS configuration from environment or uses secure defaults
func loadCORSConfig() CORSConfig {
	// Check if custom origins are set via environment variable (comma-separated)
	originsEnv := os.Getenv("CORS_ALLOWED_ORIGINS")
	var origins []string

	if originsEnv != "" {
		// Split by comma and trim spaces
		for _, origin := range splitAndTrim(originsEnv, ",") {
			if origin != "" {
				origins = append(origins, origin)
			}
		}
	} else {
		// Default to development origins for backward compatibility
		// In production, set CORS_ALLOWED_ORIGINS explicitly
		origins = []string{
			"https://localhost",
			"https://localhost:443",
			"https://localhost:5173",
			"http://localhost:5173",
			"http://localhost:3000",
		}
	}

	// AllowCredentials defaults to true if not explicitly disabled
	allowCredentials := getEnv("CORS_ALLOW_CREDENTIALS", "true") == "true"

	return CORSConfig{
		AllowedOrigins:   origins,
		AllowCredentials: allowCredentials,
	}
}

// splitAndTrim splits a string by delimiter and trims whitespace from each part
func splitAndTrim(s, delimiter string) []string {
	if s == "" {
		return []string{}
	}
	parts := []string{}
	for _, part := range strings.Split(s, delimiter) {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

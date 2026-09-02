package config

import (
	"fmt"
	"os"
	"strconv"
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
	Port           string
	Host           string
	ConsolePort    string   // Web UI + REST API listener (browser-facing)
	S3APIPort      string   // S3-compatible API listener (aws-cli/s3fs, root paths)
	FrontendURL    string   // URL where frontend is served (for SSO redirects)
	TrustedProxies []string // CIDRs/IPs whose X-Forwarded-For is trusted; empty = trust none
	MetricsToken   string   // when set, /metrics requires "Authorization: Bearer <token>"
	// S3PublicEndpoint is the base URL clients use to reach the S3 listener
	// (embedded in presigned URLs). Empty = derive from the request host +
	// S3APIPort, which is right whenever console and S3 share a hostname.
	S3PublicEndpoint string
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
	AuthRateLimit        int // requests per minute per IP on auth endpoints (default 5)
	S3RateLimit          int // requests per minute per IP on the S3 listener (0 = disabled)
}

type StorageConfig struct {
	Backend     string // "local" or "s3"
	RootPath    string // For local storage
	MaxFileSize int64
	// EnforceContentTypeDetection, when true, ignores the client's Content-Type,
	// detects it from magic bytes, and rejects "unsafe" types. Default false —
	// S3's contract is that Content-Type is client-declared metadata, and
	// rejecting binaries breaks CI artifacts, container layers, and backups.
	EnforceContentTypeDetection bool
	// S3SSE, when true, requests SSE-S3 (AES256) server-side encryption on
	// every object written through the S3 backend. Local-backend bytes are NOT
	// encrypted by bkt — use disk-level encryption (see docs).
	S3SSE bool
	S3    S3Config
}

type S3Config struct {
	Enabled         bool
	Endpoint        string // e.g., "s3.amazonaws.com" or MinIO endpoint
	Region          string
	AccessKeyID     string
	SecretAccessKey string
	BucketPrefix    string   // Prefix for all bucket names
	UseSSL          bool
	ForcePathStyle  bool     // Required for MinIO
	Buckets         []string // Buckets to auto-provision (link or create) on startup
}

type GoogleSSOConfig struct {
	OIDCEnabled  bool
	ClientID     string
	ClientSecret string
	RedirectURL  string
	// Google Workspace integration for group-based policy sync
	WorkspaceEnabled        bool
	ServiceAccountKeyFile   string // Path to service account JSON key
	WorkspaceAdminEmail     string // Admin email for domain-wide delegation
	PolicySyncMode          string // "direct" (group name = policy name) or "prefix" (group name with prefix)
	PolicyGroupPrefix       string // Prefix to filter groups (e.g., "bkt-" to only use groups starting with "bkt-")
}

type VaultSSOConfig struct {
	// Legacy JWT-based login
	Enabled  bool
	Address  string
	JWTPath  string
	Role     string
	Audience string
	// OIDC with PKCE (public client - no secret needed)
	OIDCEnabled bool
	ClientID    string
	ProviderURL string // e.g., https://vault.example.com/v1/identity/oidc/provider/default
	RedirectURL string
	Scopes      string // space-separated, e.g., "openid profile"
}

type CORSConfig struct {
	AllowedOrigins   []string
	AllowCredentials bool
}

func Load() *Config {
	cfg := &Config{
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			User:     getEnv("DB_USER", "objectstore"),
			Password: getEnv("DB_PASSWORD", "objectstore_dev_password"),
			DBName:   getEnv("DB_NAME", "objectstore"),
			SSLMode:  getEnv("DB_SSL_MODE", "disable"),
		},
		Server: ServerConfig{
			Port:        getEnv("SERVER_PORT", "9000"),
			Host:        getEnv("SERVER_HOST", "0.0.0.0"),
			ConsolePort:    getEnv("CONSOLE_PORT", "9443"),
			TrustedProxies: splitAndTrim(getEnv("TRUSTED_PROXIES", ""), ","),
			MetricsToken:     getEnv("METRICS_TOKEN", ""),
			S3PublicEndpoint: getEnv("S3_PUBLIC_ENDPOINT", ""),
			S3APIPort:   getEnv("S3_API_PORT", "9000"),
			FrontendURL: getEnv("FRONTEND_URL", "https://localhost"),
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
			AuthRateLimit:      getEnvInt("AUTH_RATE_LIMIT", 5),
			S3RateLimit:        getEnvInt("S3_RATE_LIMIT", 0),
		},
		Storage: StorageConfig{
			Backend:                     getEnv("STORAGE_BACKEND", "local"), // "local" or "s3"
			RootPath:                    getEnv("STORAGE_ROOT", "/data/buckets"),
			MaxFileSize:                 5 * 1024 * 1024 * 1024, // 5GB
			EnforceContentTypeDetection: getEnv("CONTENT_TYPE_ENFORCEMENT", "false") == "true",
			S3SSE:                       getEnv("S3_SSE", "false") == "true",
			S3: S3Config{
				Enabled:         getEnv("S3_ENABLED", "false") == "true",
				Endpoint:        getEnv("S3_ENDPOINT", "s3.amazonaws.com"),
				Region:          getEnv("S3_REGION", "us-east-1"),
				AccessKeyID:     getEnv("S3_ACCESS_KEY_ID", ""),
				SecretAccessKey: getEnv("S3_SECRET_ACCESS_KEY", ""),
				BucketPrefix:    getEnv("S3_BUCKET_PREFIX", ""),
				UseSSL:          getEnv("S3_USE_SSL", "true") == "true",
				ForcePathStyle:  getEnv("S3_FORCE_PATH_STYLE", "false") == "true",
				Buckets:         splitAndTrim(getEnv("S3_BUCKETS", ""), ","),
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
			OIDCEnabled:             getEnv("GOOGLE_OIDC_ENABLED", "false") == "true",
			ClientID:                getEnv("GOOGLE_CLIENT_ID", ""),
			ClientSecret:            getEnv("GOOGLE_CLIENT_SECRET", ""),
			RedirectURL:             getEnv("GOOGLE_REDIRECT_URL", "https://localhost:9443/api/auth/google/callback"),
			WorkspaceEnabled:        getEnv("GOOGLE_WORKSPACE_ENABLED", "false") == "true",
			ServiceAccountKeyFile:   getEnv("GOOGLE_SERVICE_ACCOUNT_KEY_FILE", ""),
			WorkspaceAdminEmail:     getEnv("GOOGLE_WORKSPACE_ADMIN_EMAIL", ""),
			PolicySyncMode:          getEnv("GOOGLE_POLICY_SYNC_MODE", "direct"), // "direct" or "prefix"
			PolicyGroupPrefix:       getEnv("GOOGLE_POLICY_GROUP_PREFIX", ""),    // e.g., "bkt-" to use groups like "bkt-engineering"
		},
		VaultSSO: VaultSSOConfig{
			Enabled:     getEnv("VAULT_SSO_ENABLED", "false") == "true",
			Address:     getEnv("VAULT_ADDR", "https://vault.example.com:8200"),
			JWTPath:     getEnv("VAULT_JWT_PATH", "auth/jwt"),
			Role:        getEnv("VAULT_JWT_ROLE", "object-storage-users"),
			Audience:    getEnv("VAULT_JWT_AUDIENCE", "object-storage"),
			OIDCEnabled: getEnv("VAULT_OIDC_ENABLED", "false") == "true",
			ClientID:    getEnv("VAULT_OIDC_CLIENT_ID", ""),
			ProviderURL: getEnv("VAULT_OIDC_PROVIDER_URL", ""),
			RedirectURL: getEnv("VAULT_OIDC_REDIRECT_URL", "https://localhost:9443/api/auth/vault/callback"),
			Scopes:      getEnv("VAULT_OIDC_SCOPES", "openid profile"),
		},
	}

	// Validate critical secrets in production
	if err := cfg.Validate(); err != nil {
		panic(fmt.Sprintf("Configuration validation failed: %v", err))
	}

	return cfg
}

// Validate checks that critical secrets are set in production environments
func (c *Config) Validate() error {
	// Check if running in production (via GO_ENV or APP_ENV environment variable)
	env := strings.ToLower(getEnv("GO_ENV", getEnv("APP_ENV", "development")))
	isProd := env == "production" || env == "prod"

	if !isProd {
		// Skip validation in development/test environments
		return nil
	}

	// In production, critical secrets must be explicitly set
	errors := []string{}

	// JWT Secret must not be default value
	if c.Auth.JWTSecret == "dev_jwt_secret_change_in_production" || c.Auth.JWTSecret == "" {
		errors = append(errors, "JWT_SECRET must be set in production (cannot use default value)")
	}

	// Database password should be set in production
	if c.Database.Password == "objectstore_dev_password" || c.Database.Password == "" {
		errors = append(errors, "DB_PASSWORD must be set in production (cannot use default value)")
	}

	// ENCRYPTION_KEY from environment (checked via security package initialization)
	encryptionKey := os.Getenv("ENCRYPTION_KEY")
	if encryptionKey == "" {
		errors = append(errors, "ENCRYPTION_KEY must be set in production (required for S3 credential encryption)")
	}

	// TLS should be enabled in production
	if !c.TLS.Enabled {
		errors = append(errors, "TLS_ENABLED must be true in production (TLS is required for secure communication)")
	}

	// If Google OIDC is enabled, credentials must be set
	if c.GoogleSSO.OIDCEnabled && (c.GoogleSSO.ClientID == "" || c.GoogleSSO.ClientSecret == "") {
		errors = append(errors, "Google OIDC enabled but GOOGLE_CLIENT_ID or GOOGLE_CLIENT_SECRET not set")
	}

	// If Google Workspace integration is enabled, service account must be configured
	if c.GoogleSSO.WorkspaceEnabled {
		if c.GoogleSSO.ServiceAccountKeyFile == "" {
			errors = append(errors, "Google Workspace enabled but GOOGLE_SERVICE_ACCOUNT_KEY_FILE not set")
		}
		if c.GoogleSSO.WorkspaceAdminEmail == "" {
			errors = append(errors, "Google Workspace enabled but GOOGLE_WORKSPACE_ADMIN_EMAIL not set")
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("production configuration errors:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
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

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if i, err := strconv.Atoi(value); err == nil {
			return i
		}
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
			"https://localhost:8443", // frontend is also published on 8443 (docker-compose)
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

package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Database  DatabaseConfig
	Server    ServerConfig
	Auth      AuthConfig
	Storage   StorageConfig
	TLS       TLSConfig
	CORS      CORSConfig
	GoogleSSO GoogleSSOConfig
	VaultSSO  VaultSSOConfig
	OIDC      OIDCConfig
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
	// Production is true when GO_ENV (or APP_ENV) is "production"/"prod".
	Production bool
	// SwaggerEnabled serves the Swagger UI at /api/docs (SWAGGER_ENABLED;
	// default on in development, off in production).
	SwaggerEnabled bool
	// HSTSMaxAge is the Strict-Transport-Security max-age (seconds) sent by
	// the console when it is reached over TLS (HSTS_MAX_AGE; 0 disables).
	HSTSMaxAge int
}

type TLSConfig struct {
	Enabled  bool
	CertFile string
	KeyFile  string
	CAFile   string
	// TerminatedUpstream declares that TLS is terminated by a reverse proxy /
	// ingress in front of bkt (TLS_TERMINATED_UPSTREAM=true). It satisfies the
	// production TLS requirement while the listeners themselves speak HTTP.
	TerminatedUpstream bool
}

type AuthConfig struct {
	JWTSecret          string
	AccessTokenExpiry  string
	RefreshTokenExpiry string
	BcryptCost         int
	AdminUsername      string
	AdminPassword      string
	AdminEmail         string
	AllowRegistration  bool
	AuthRateLimit      int // requests per minute per IP on auth endpoints (default 5)
	RefreshRateLimit   int // requests per minute per IP on /api/auth/refresh (default 30)
	S3RateLimit        int // requests per minute per IP on the S3 listener (0 = disabled)
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
	BucketPrefix    string // Prefix for all bucket names
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
	WorkspaceEnabled      bool
	ServiceAccountKeyFile string   // Path to service account JSON key
	WorkspaceAdminEmail   string   // Admin email for domain-wide delegation
	PolicySyncMode        string   // "direct" (group name = policy name) or "prefix" (group name with prefix)
	PolicyGroupPrefix     string   // Prefix to filter groups (e.g., "bkt-" to only use groups starting with "bkt-")
	AllowedDomains        []string // GOOGLE_ALLOWED_DOMAINS: Workspace domains allowed to sign in (hd claim + email domain)
}

type VaultSSOConfig struct {
	// Legacy JWT-based login
	Enabled  bool
	Address  string
	JWTPath  string
	Role     string
	Audience string
	Issuer   string // VAULT_JWT_ISSUER: when set, the JWT "iss" must match
	// OIDC with PKCE (public client - no secret needed)
	OIDCEnabled bool
	ClientID    string
	ProviderURL string // e.g., https://vault.example.com/v1/identity/oidc/provider/default
	RedirectURL string
	Scopes      string // space-separated, e.g., "openid profile"
}

// OIDCConfig is the generic OpenID Connect provider (Keycloak, Okta, Entra ID,
// Auth0, Authentik, Kanidm, Vault, ...). Endpoints come from the issuer's
// discovery document. The authorization-code flow always uses PKCE (S256);
// a client secret is optional and, when set, additionally authenticates the
// token request (confidential client).
type OIDCConfig struct {
	Enabled      bool
	IssuerURL    string // e.g. https://kc.example.com/realms/myrealm
	ClientID     string
	ClientSecret string // optional — public client (PKCE only) when empty
	RedirectURL  string // backend callback, must match the IdP's registered redirect URI
	Scopes       string // space-separated; "openid" is always required
	ProviderName string // login-button label, e.g. "Okta"

	// Claims mapping
	UsernameClaim string // claim used for the username at first login (default: preferred_username, then email local part)
	GroupsClaim   string // claim carrying group names (default "groups")
	AdminGroup    string // members of this group become admins (re-evaluated every login); empty = never via SSO
	UserGroup     string // when set, non-admin users must be in this group or login is denied
	PoliciesClaim string // claim carrying bkt policy names to sync (default "policies")
	LinkByEmail   bool   // link an unknown subject to an existing SSO account with the same verified email
	// PoliciesAuthoritative: the IdP owns policy membership even when the
	// policies claim is absent (absent = no policies). Defaults to true when
	// OIDC_POLICIES_CLAIM is set explicitly.
	PoliciesAuthoritative bool
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
			Port:             getEnv("SERVER_PORT", "9000"),
			Host:             getEnv("SERVER_HOST", "0.0.0.0"),
			ConsolePort:      getEnv("CONSOLE_PORT", "9443"),
			TrustedProxies:   splitAndTrim(getEnv("TRUSTED_PROXIES", ""), ","),
			MetricsToken:     getEnv("METRICS_TOKEN", ""),
			S3PublicEndpoint: getEnv("S3_PUBLIC_ENDPOINT", ""),
			S3APIPort:        getEnv("S3_API_PORT", "9000"),
			// Default: the console listener itself (the UI is embedded there).
			FrontendURL:    getEnv("FRONTEND_URL", defaultFrontendURL()),
			Production:     IsProduction(),
			SwaggerEnabled: getEnvBool("SWAGGER_ENABLED", !IsProduction()),
			HSTSMaxAge:     getEnvInt("HSTS_MAX_AGE", 31536000),
		},
		Auth: AuthConfig{
			// No default: an unset JWT_SECRET is rejected by Validate.
			JWTSecret:          getEnv("JWT_SECRET", ""),
			AccessTokenExpiry:  getEnv("ACCESS_TOKEN_EXPIRY", "15m"),
			RefreshTokenExpiry: getEnv("REFRESH_TOKEN_EXPIRY", "168h"), // 7 days
			BcryptCost:         12,
			AdminUsername:      getEnv("ADMIN_USERNAME", "admin"),
			AdminPassword:      getEnv("ADMIN_PASSWORD", ""),
			AdminEmail:         getEnv("ADMIN_EMAIL", "admin@localhost"),
			AllowRegistration:  getEnv("ALLOW_REGISTRATION", "false") == "true",
			AuthRateLimit:      getEnvInt("AUTH_RATE_LIMIT", 5),
			RefreshRateLimit:   getEnvInt("AUTH_REFRESH_RATE_LIMIT", 30),
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
			Enabled:            getEnv("TLS_ENABLED", "false") == "true",
			CertFile:           getEnv("TLS_CERT_FILE", ""),
			KeyFile:            getEnv("TLS_KEY_FILE", ""),
			CAFile:             getEnv("TLS_CA_FILE", ""),
			TerminatedUpstream: getEnvBool("TLS_TERMINATED_UPSTREAM", false),
		},
		CORS: loadCORSConfig(),
		GoogleSSO: GoogleSSOConfig{
			OIDCEnabled:           getEnv("GOOGLE_OIDC_ENABLED", "false") == "true",
			ClientID:              getEnv("GOOGLE_CLIENT_ID", ""),
			ClientSecret:          getEnv("GOOGLE_CLIENT_SECRET", ""),
			RedirectURL:           getEnv("GOOGLE_REDIRECT_URL", "https://localhost:9443/api/auth/google/callback"),
			WorkspaceEnabled:      getEnv("GOOGLE_WORKSPACE_ENABLED", "false") == "true",
			ServiceAccountKeyFile: getEnv("GOOGLE_SERVICE_ACCOUNT_KEY_FILE", ""),
			WorkspaceAdminEmail:   getEnv("GOOGLE_WORKSPACE_ADMIN_EMAIL", ""),
			PolicySyncMode:        getEnv("GOOGLE_POLICY_SYNC_MODE", "direct"), // "direct" or "prefix"
			PolicyGroupPrefix:     getEnv("GOOGLE_POLICY_GROUP_PREFIX", ""),    // e.g., "bkt-" to use groups like "bkt-engineering"
			AllowedDomains:        splitDomainList(getEnv("GOOGLE_ALLOWED_DOMAINS", "")),
		},
		VaultSSO: VaultSSOConfig{
			Enabled:     getEnv("VAULT_SSO_ENABLED", "false") == "true",
			Address:     getEnv("VAULT_ADDR", "https://vault.example.com:8200"),
			JWTPath:     getEnv("VAULT_JWT_PATH", "auth/jwt"),
			Role:        getEnv("VAULT_JWT_ROLE", "object-storage-users"),
			Audience:    getEnv("VAULT_JWT_AUDIENCE", "object-storage"),
			Issuer:      strings.TrimSpace(getEnv("VAULT_JWT_ISSUER", "")),
			OIDCEnabled: getEnv("VAULT_OIDC_ENABLED", "false") == "true",
			ClientID:    getEnv("VAULT_OIDC_CLIENT_ID", ""),
			ProviderURL: getEnv("VAULT_OIDC_PROVIDER_URL", ""),
			RedirectURL: getEnv("VAULT_OIDC_REDIRECT_URL", "https://localhost:9443/api/auth/vault/callback"),
			Scopes:      getEnv("VAULT_OIDC_SCOPES", "openid profile"),
		},
		OIDC: loadOIDCConfig(),
	}

	// Validate critical secrets in production
	if err := cfg.Validate(); err != nil {
		panic(fmt.Sprintf("Configuration validation failed: %v", err))
	}

	return cfg
}

// Well-known insecure values that must never be accepted as secrets.
const (
	// LegacyDevJWTSecret is the JWT secret older releases (and the old
	// docker-compose.yml) defaulted to. It is public, so tokens signed with it
	// are forgeable by anyone.
	LegacyDevJWTSecret = "dev_jwt_secret_change_in_production" //nolint:gosec // G101: public value kept only so it can be rejected
	// MinSecretLength is the minimum accepted length of JWT_SECRET and
	// ENCRYPTION_KEY (setup.py and the omnibus generate 64 hex chars).
	MinSecretLength = 32
)

// validateSecret returns a human-readable problem with a secret value, or ""
// when it is acceptable. Applied in EVERY environment: a forgeable JWT secret
// is an authentication bypass whether or not GO_ENV says "production".
func validateSecret(name, value string) string {
	switch {
	case value == "":
		return name + " must be set (generate one with: openssl rand -hex 32)"
	case value == LegacyDevJWTSecret:
		return name + " is the publicly known development default; generate a real one with: openssl rand -hex 32"
	case strings.ContainsAny(value, "<>"):
		return name + " still contains a placeholder (e.g. <generated_by_setup.py>); run setup.py or generate one with: openssl rand -hex 32"
	case len(value) < MinSecretLength:
		return fmt.Sprintf("%s must be at least %d characters (generate one with: openssl rand -hex 32)", name, MinSecretLength)
	}
	return ""
}

// IsProduction reports whether GO_ENV (or APP_ENV) selects production mode.
func IsProduction() bool {
	env := strings.ToLower(strings.TrimSpace(getEnv("GO_ENV", getEnv("APP_ENV", "development"))))
	return env == "production" || env == "prod"
}

// Validate checks the configuration. Secret-strength rules apply in every
// environment; the remaining checks only in production (GO_ENV=production).
func (c *Config) Validate() error {
	isProd := IsProduction()
	encryptionKey := os.Getenv("ENCRYPTION_KEY")

	errors := []string{}

	// ── Always: secrets must be real ─────────────────────────────────────
	if problem := validateSecret("JWT_SECRET", c.Auth.JWTSecret); problem != "" {
		errors = append(errors, problem)
	}
	if encryptionKey != "" {
		if problem := validateSecret("ENCRYPTION_KEY", encryptionKey); problem != "" {
			errors = append(errors, problem)
		}
	}

	if !isProd {
		if len(errors) > 0 {
			return fmt.Errorf("configuration errors:\n  - %s", strings.Join(errors, "\n  - "))
		}
		if encryptionKey == "" {
			// Kept for backward compatibility: existing development data was
			// encrypted with key material derived from JWT_SECRET.
			log.Println("WARNING: ENCRYPTION_KEY is not set — stored S3 credentials are encrypted with a key derived from JWT_SECRET. " +
				"Set a dedicated ENCRYPTION_KEY (openssl rand -hex 32); data encrypted under JWT_SECRET stays readable. " +
				"Production (GO_ENV=production) refuses to start without it.")
		}
		return nil
	}

	// ── Production only ─────────────────────────────────────────────────
	// Database password should be set in production
	if c.Database.Password == "objectstore_dev_password" || c.Database.Password == "" {
		errors = append(errors, "DB_PASSWORD must be set in production (cannot use default value)")
	}

	if encryptionKey == "" {
		errors = append(errors, "ENCRYPTION_KEY must be set in production (required for S3 credential encryption)")
	}

	// TLS must protect traffic in production — either on the listeners
	// themselves or at a TLS-terminating proxy/ingress in front of bkt.
	if !c.TLS.Enabled {
		if c.TLS.TerminatedUpstream {
			log.Println("TLS_ENABLED=false with TLS_TERMINATED_UPSTREAM=true: serving plain HTTP and trusting the proxy/ingress in front of bkt to terminate TLS. " +
				"Make sure the listeners are not reachable except through that proxy.")
		} else {
			errors = append(errors, "TLS_ENABLED must be true in production (or set TLS_TERMINATED_UPSTREAM=true when a reverse proxy/ingress terminates TLS in front of bkt)")
		}
	}

	// If Google OIDC is enabled, credentials must be set
	if c.GoogleSSO.OIDCEnabled && (c.GoogleSSO.ClientID == "" || c.GoogleSSO.ClientSecret == "") {
		errors = append(errors, "Google OIDC enabled but GOOGLE_CLIENT_ID or GOOGLE_CLIENT_SECRET not set")
	}

	// Generic OIDC: the pieces the flow cannot run without.
	if c.OIDC.Enabled {
		if c.OIDC.IssuerURL == "" || c.OIDC.ClientID == "" {
			errors = append(errors, "OIDC enabled but OIDC_ISSUER_URL or OIDC_CLIENT_ID not set")
		}
		if !strings.HasPrefix(c.OIDC.RedirectURL, "https://") {
			errors = append(errors, "OIDC_REDIRECT_URL must be an https:// URL in production")
		}
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
		dsnQuote(c.Database.Host),
		dsnQuote(c.Database.Port),
		dsnQuote(c.Database.User),
		dsnQuote(c.Database.Password),
		dsnQuote(c.Database.DBName),
		dsnQuote(c.Database.SSLMode),
	)
}

// dsnQuote renders a libpq keyword/value connection-string value: single-
// quoted, with backslashes and single quotes backslash-escaped, so passwords
// containing spaces, quotes or '=' can't break (or inject into) the DSN.
func dsnQuote(v string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'"
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvBool parses a boolean env var ("true"/"false"/"1"/"0"/...), returning
// defaultValue when it is unset or unparsable.
func getEnvBool(key string, defaultValue bool) bool {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return defaultValue
}

// defaultFrontendURL is where SSO flows land when FRONTEND_URL is unset: the
// console listener itself, which serves the embedded UI.
func defaultFrontendURL() string {
	scheme := "http"
	if getEnv("TLS_ENABLED", "false") == "true" {
		scheme = "https"
	}
	return scheme + "://localhost:" + getEnv("CONSOLE_PORT", "9443")
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

// splitDomainList parses a comma-separated domain allow-list
// (GOOGLE_ALLOWED_DOMAINS) into lower-cased, trimmed entries; a leading "@"
// is tolerated ("@example.com" == "example.com").
func splitDomainList(s string) []string {
	domains := []string{}
	for _, d := range splitAndTrim(s, ",") {
		d = strings.ToLower(strings.TrimPrefix(d, "@"))
		if d != "" {
			domains = append(domains, d)
		}
	}
	return domains
}

// loadOIDCConfig reads the generic OIDC provider settings. OIDC_ENABLED defaults
// to "on" whenever an issuer and client ID are both supplied, so the common case
// needs no explicit switch; set OIDC_ENABLED=false to keep a configured provider
// off (e.g. during rollout).
func loadOIDCConfig() OIDCConfig {
	issuer := strings.TrimSpace(getEnv("OIDC_ISSUER_URL", ""))
	clientID := strings.TrimSpace(getEnv("OIDC_CLIENT_ID", ""))
	enabledDefault := "false"
	if issuer != "" && clientID != "" {
		enabledDefault = "true"
	}
	return OIDCConfig{
		Enabled:               getEnv("OIDC_ENABLED", enabledDefault) == "true",
		IssuerURL:             issuer,
		ClientID:              clientID,
		ClientSecret:          getEnv("OIDC_CLIENT_SECRET", ""),
		RedirectURL:           getEnv("OIDC_REDIRECT_URL", "https://localhost:9443/api/auth/oidc/callback"),
		Scopes:                getEnv("OIDC_SCOPES", "openid profile email"),
		ProviderName:          getEnv("OIDC_PROVIDER_NAME", "SSO"),
		UsernameClaim:         getEnv("OIDC_USERNAME_CLAIM", ""),
		GroupsClaim:           getEnv("OIDC_GROUPS_CLAIM", "groups"),
		AdminGroup:            getEnv("OIDC_ADMIN_GROUP", ""),
		UserGroup:             getEnv("OIDC_USER_GROUP", ""),
		PoliciesClaim:         getEnv("OIDC_POLICIES_CLAIM", "policies"),
		LinkByEmail:           getEnv("OIDC_LINK_BY_EMAIL", "false") == "true",
		PoliciesAuthoritative: getEnvBool("OIDC_POLICIES_AUTHORITATIVE", strings.TrimSpace(os.Getenv("OIDC_POLICIES_CLAIM")) != ""),
	}
}

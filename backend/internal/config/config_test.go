package config

import (
	"strings"
	"testing"
)

const (
	goodSecret  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // openssl rand -hex 32
	goodSecret2 = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

// validProdConfig returns a config that passes production validation; tests
// mutate one field at a time.
func validProdConfig(t *testing.T) *Config {
	t.Helper()
	t.Setenv("GO_ENV", "production")
	t.Setenv("APP_ENV", "")
	t.Setenv("ENCRYPTION_KEY", goodSecret2)
	return &Config{
		Auth:     AuthConfig{JWTSecret: goodSecret},
		Database: DatabaseConfig{Password: "a-real-db-password"},
		TLS:      TLSConfig{Enabled: true},
	}
}

func devConfig(t *testing.T, jwt string) *Config {
	t.Helper()
	t.Setenv("GO_ENV", "development")
	t.Setenv("APP_ENV", "")
	t.Setenv("ENCRYPTION_KEY", "")
	return &Config{Auth: AuthConfig{JWTSecret: jwt}}
}

func TestValidateSecret(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"empty", "", false},
		{"legacy dev default", LegacyDevJWTSecret, false},
		{"setup.py placeholder", "<generated_by_setup.py>", false},
		{"placeholder padded to length", "<generated_by_setup.py>-xxxxxxxxxxxxxxxxxxxxx", false},
		{"too short", "short-secret", false},
		{"31 chars", strings.Repeat("a", 31), false},
		{"32 chars (setup.py alnum)", strings.Repeat("a", 32), true},
		{"64 hex (openssl rand -hex 32)", goodSecret, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problem := validateSecret("JWT_SECRET", tc.value)
			if tc.ok && problem != "" {
				t.Errorf("expected %q to be accepted, got %q", tc.value, problem)
			}
			if !tc.ok && problem == "" {
				t.Errorf("expected %q to be rejected", tc.value)
			}
		})
	}
}

// Secret-strength rules apply in development too — GO_ENV must not be a
// switch that turns a forgeable JWT secret into an accepted one.
func TestValidateRejectsWeakJWTSecretInDevelopment(t *testing.T) {
	for _, v := range []string{"", LegacyDevJWTSecret, "<generated_by_setup.py>", "tooshort"} {
		if err := devConfig(t, v).Validate(); err == nil {
			t.Errorf("development: expected JWT_SECRET %q to be rejected", v)
		}
	}
	if err := devConfig(t, goodSecret).Validate(); err != nil {
		t.Errorf("development: strong JWT_SECRET rejected: %v", err)
	}
}

func TestValidateDevelopmentAllowsMissingEncryptionKey(t *testing.T) {
	cfg := devConfig(t, goodSecret)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("development without ENCRYPTION_KEY should only warn, got %v", err)
	}
}

func TestValidateRejectsWeakEncryptionKeyWhenSet(t *testing.T) {
	for _, v := range []string{"<generated_by_setup.py>", "short", LegacyDevJWTSecret} {
		cfg := devConfig(t, goodSecret)
		t.Setenv("ENCRYPTION_KEY", v)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ENCRYPTION_KEY") {
			t.Errorf("expected ENCRYPTION_KEY %q to be rejected, got %v", v, err)
		}
	}
}

func TestValidateProductionHappyPath(t *testing.T) {
	if err := validProdConfig(t).Validate(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
}

func TestValidateProductionRequiresEncryptionKey(t *testing.T) {
	cfg := validProdConfig(t)
	t.Setenv("ENCRYPTION_KEY", "")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ENCRYPTION_KEY") {
		t.Fatalf("expected missing ENCRYPTION_KEY to be rejected in production, got %v", err)
	}
}

func TestValidateProductionRejectsDefaultDBPassword(t *testing.T) {
	cfg := validProdConfig(t)
	cfg.Database.Password = "objectstore_dev_password"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected default DB password to be rejected in production")
	}
}

func TestValidateProductionTLS(t *testing.T) {
	cfg := validProdConfig(t)
	cfg.TLS.Enabled = false
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("expected plain HTTP to be rejected in production, got %v", err)
	}

	// TLS terminated at an ingress / reverse proxy satisfies the requirement.
	cfg.TLS.TerminatedUpstream = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("TLS_TERMINATED_UPSTREAM=true should satisfy production TLS, got %v", err)
	}
}

func TestValidateProductionRejectsWeakJWTSecret(t *testing.T) {
	cfg := validProdConfig(t)
	cfg.Auth.JWTSecret = LegacyDevJWTSecret
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected legacy default JWT_SECRET to be rejected in production")
	}
}

func TestLoadHasNoJWTSecretDefault(t *testing.T) {
	t.Setenv("GO_ENV", "development")
	t.Setenv("JWT_SECRET", "")
	defer func() {
		if recover() == nil {
			t.Fatal("Load() with no JWT_SECRET should refuse to start")
		}
	}()
	Load()
}

func TestDefaultFrontendURLFollowsConsoleListener(t *testing.T) {
	t.Setenv("TLS_ENABLED", "true")
	t.Setenv("CONSOLE_PORT", "")
	if got := defaultFrontendURL(); got != "https://localhost:9443" {
		t.Errorf("got %q", got)
	}
	t.Setenv("TLS_ENABLED", "false")
	t.Setenv("CONSOLE_PORT", "8080")
	if got := defaultFrontendURL(); got != "http://localhost:8080" {
		t.Errorf("got %q", got)
	}
}

func TestSwaggerDefaultsOffInProduction(t *testing.T) {
	t.Setenv("JWT_SECRET", goodSecret)
	t.Setenv("ENCRYPTION_KEY", goodSecret2)
	t.Setenv("DB_PASSWORD", "a-real-db-password")
	t.Setenv("TLS_ENABLED", "true")
	t.Setenv("SWAGGER_ENABLED", "")

	t.Setenv("GO_ENV", "production")
	if Load().Server.SwaggerEnabled {
		t.Error("Swagger should default to off in production")
	}
	t.Setenv("SWAGGER_ENABLED", "true")
	if !Load().Server.SwaggerEnabled {
		t.Error("SWAGGER_ENABLED=true should enable Swagger in production")
	}
	t.Setenv("SWAGGER_ENABLED", "")
	t.Setenv("GO_ENV", "development")
	if !Load().Server.SwaggerEnabled {
		t.Error("Swagger should default to on in development")
	}
}

func TestGetDSNQuotesValues(t *testing.T) {
	cfg := &Config{Database: DatabaseConfig{
		Host: "db", Port: "5432", User: "objectstore",
		Password: `it's a $HOME \ #pw sslmode=disable`, DBName: "objectstore", SSLMode: "require",
	}}
	want := `host='db' port='5432' user='objectstore' password='it\'s a $HOME \\ #pw sslmode=disable' dbname='objectstore' sslmode='require'`
	if got := cfg.GetDSN(); got != want {
		t.Errorf("GetDSN()\n got %s\nwant %s", got, want)
	}
}

func TestSplitDomainList(t *testing.T) {
	got := splitDomainList(" Example.com, @corp.example.org ,, ")
	if len(got) != 2 || got[0] != "example.com" || got[1] != "corp.example.org" {
		t.Errorf("splitDomainList = %q", got)
	}
	if len(splitDomainList("")) != 0 {
		t.Error("empty list expected")
	}
}

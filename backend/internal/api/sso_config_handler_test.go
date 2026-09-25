package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"bkt/internal/config"

	"github.com/gin-gonic/gin"
)

// The public SSO config tells the login page whether self-service
// registration is enabled (ALLOW_REGISTRATION), and never leaks anything else.
func TestSSOConfigReportsAllowRegistration(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, allow := range []bool{false, true} {
		cfg := &config.Config{}
		cfg.Auth.AllowRegistration = allow
		cfg.Auth.JWTSecret = "must-not-leak"

		r := gin.New()
		r.GET("/api/auth/sso/config", NewSSOConfigHandler(cfg).GetSSOConfig)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/auth/sso/config", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("allow=%v: status %d", allow, w.Code)
		}

		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("allow=%v: bad JSON %q: %v", allow, w.Body.String(), err)
		}
		got, ok := body["allow_registration"].(bool)
		if !ok {
			t.Fatalf("allow=%v: allow_registration missing or not a bool: %s", allow, w.Body.String())
		}
		if got != allow {
			t.Errorf("allow_registration = %v, want %v", got, allow)
		}
		// Present even when false (no omitempty) so the client can rely on it.
		if body["google_enabled"] != false || body["vault_enabled"] != false || body["oidc_enabled"] != false {
			t.Errorf("allow=%v: unexpected SSO flags: %s", allow, w.Body.String())
		}
		for k := range body {
			switch k {
			case "google_enabled", "vault_enabled", "oidc_enabled", "allow_registration":
			default:
				t.Errorf("allow=%v: unexpected field %q in public config", allow, k)
			}
		}
	}
}

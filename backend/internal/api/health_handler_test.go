package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bkt/internal/database"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Health endpoints are unauthenticated: a DB failure must not echo driver
// errors (hostnames, ports, users) to the client.
func TestHealthEndpointsHideDBErrors(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 port=1 user=secretuser dbname=x sslmode=disable connect_timeout=1"),
		&gorm.Config{DisableAutomaticPing: true, Logger: logger.Discard})
	if err != nil {
		t.Skipf("cannot construct gorm handle: %v", err)
	}
	prev := database.DB
	database.DB = db
	defer func() { database.DB = prev }()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/health", HealthHandler)
	r.GET("/ready", ReadinessHandler)
	for _, p := range []string{"/health", "/ready"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: status %d", p, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, dbUnavailable) {
			t.Errorf("%s: body %s lacks generic message", p, body)
		}
		for _, leak := range []string{"127.0.0.1", "secretuser", "dial", "refused"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: body leaks %q: %s", p, leak, body)
			}
		}
	}
}

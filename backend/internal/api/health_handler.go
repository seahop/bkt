package api

import (
	"context"
	"log"
	"net/http"
	"time"

	"bkt/internal/database"

	"github.com/gin-gonic/gin"
)

// HealthResponse represents the health check response
type HealthResponse struct {
	Status    string            `json:"status"`
	Timestamp string            `json:"timestamp"`
	Checks    map[string]string `json:"checks"`
}

// dbUnavailable is the only database failure detail returned to (unauthenticated)
// health-check clients; the underlying error — which can reveal hostnames,
// ports, users or driver internals — is logged server-side instead.
const dbUnavailable = "database unavailable"

// healthDBTimeout bounds each database probe so a hung DB cannot pin health
// requests.
const healthDBTimeout = 3 * time.Second

func logHealthDBError(probe string, err error) {
	log.Printf("health: %s database check failed: %v", probe, err)
}

// HealthHandler handles health check requests
func HealthHandler(c *gin.Context) {
	checks := make(map[string]string)
	overallStatus := "healthy"

	// Check database connectivity
	if database.DB != nil {
		sqlDB, err := database.DB.DB()
		if err != nil {
			logHealthDBError("health", err)
			checks["database"] = dbUnavailable
			overallStatus = "unhealthy"
		} else {
			// Ping with timeout
			ctx, cancel := context.WithTimeout(c.Request.Context(), healthDBTimeout)
			err = sqlDB.PingContext(ctx)
			cancel()
			if err != nil {
				logHealthDBError("health", err)
				checks["database"] = dbUnavailable
				overallStatus = "unhealthy"
			} else {
				checks["database"] = "connected"
			}
		}
	} else {
		checks["database"] = "not initialized"
		overallStatus = "unhealthy"
	}

	response := HealthResponse{
		Status:    overallStatus,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Checks:    checks,
	}

	statusCode := http.StatusOK
	if overallStatus != "healthy" {
		statusCode = http.StatusServiceUnavailable
	}

	c.JSON(statusCode, response)
}

// ReadinessHandler checks if the service is ready to accept traffic
// More comprehensive than health - checks all dependencies
func ReadinessHandler(c *gin.Context) {
	checks := make(map[string]string)
	ready := true

	// Check database connectivity and query ability
	if database.DB != nil {
		sqlDB, err := database.DB.DB()
		if err != nil {
			logHealthDBError("readiness", err)
			checks["database"] = dbUnavailable
			ready = false
		} else {
			ctx, cancel := context.WithTimeout(c.Request.Context(), healthDBTimeout)
			defer cancel()
			err = sqlDB.PingContext(ctx)
			if err != nil {
				logHealthDBError("readiness", err)
				checks["database"] = dbUnavailable
				ready = false
			} else {
				// Check we can actually query
				var result int
				if err := database.DB.WithContext(ctx).Raw("SELECT 1").Scan(&result).Error; err != nil {
					logHealthDBError("readiness query", err)
					checks["database"] = dbUnavailable
					ready = false
				} else {
					checks["database"] = "ready"
				}
			}
		}
	} else {
		checks["database"] = "not initialized"
		ready = false
	}

	response := HealthResponse{
		Status:    "ready",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Checks:    checks,
	}

	statusCode := http.StatusOK
	if !ready {
		response.Status = "not ready"
		statusCode = http.StatusServiceUnavailable
	}

	c.JSON(statusCode, response)
}

// LivenessHandler is a simple check that the service is running
// Used by orchestrators to determine if the service should be restarted
func LivenessHandler(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "alive",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

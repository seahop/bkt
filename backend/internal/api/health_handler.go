package api

import (
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

// HealthHandler handles health check requests
func HealthHandler(c *gin.Context) {
	checks := make(map[string]string)
	overallStatus := "healthy"

	// Check database connectivity
	if database.DB != nil {
		sqlDB, err := database.DB.DB()
		if err != nil {
			checks["database"] = "error: " + err.Error()
			overallStatus = "unhealthy"
		} else {
			// Ping with timeout
			err = sqlDB.Ping()
			if err != nil {
				checks["database"] = "error: " + err.Error()
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
			checks["database"] = "error: " + err.Error()
			ready = false
		} else {
			err = sqlDB.Ping()
			if err != nil {
				checks["database"] = "error: " + err.Error()
				ready = false
			} else {
				// Check we can actually query
				var result int
				if err := database.DB.Raw("SELECT 1").Scan(&result).Error; err != nil {
					checks["database"] = "query error: " + err.Error()
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

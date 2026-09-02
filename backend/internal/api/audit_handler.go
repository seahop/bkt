package api

import (
	"net/http"
	"strconv"
	"time"

	"bkt/internal/config"
	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// AuditHandler exposes the audit log to administrators.
type AuditHandler struct {
	config       *config.Config
	auditService *services.AuditService
}

func NewAuditHandler(cfg *config.Config) *AuditHandler {
	return &AuditHandler{config: cfg, auditService: services.NewAuditService()}
}

// ListAuditLogs returns audit log entries with optional filters (admin only).
// @Summary List audit logs
// @Description Admin-only. Returns audit log entries, filterable by user, action, resource type, status, and time range.
// @Tags audit
// @Accept json
// @Produce json
// @Param user_id query string false "Filter by user ID"
// @Param action query string false "Filter by action"
// @Param resource_type query string false "Filter by resource type"
// @Param status query string false "Filter by status (success/failure/denied)"
// @Param start query string false "Start time (RFC3339)"
// @Param end query string false "End time (RFC3339)"
// @Param limit query int false "Max results (default 100, max 500)"
// @Param offset query int false "Offset for pagination"
// @Success 200 {object} map[string]interface{}
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/audit [get]
func (h *AuditHandler) ListAuditLogs(c *gin.Context) {
	var (
		userIDPtr       *uuid.UUID
		actionPtr       *string
		resourceTypePtr *string
		statusPtr       *string
		startPtr        *time.Time
		endPtr          *time.Time
	)

	if v := c.Query("user_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			userIDPtr = &id
		}
	}
	if v := c.Query("action"); v != "" {
		actionPtr = &v
	}
	if v := c.Query("resource_type"); v != "" {
		resourceTypePtr = &v
	}
	if v := c.Query("status"); v != "" {
		statusPtr = &v
	}
	if v := c.Query("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			startPtr = &t
		}
	}
	if v := c.Query("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			endPtr = &t
		}
	}

	limit := 100
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 500 {
		limit = 500
	}
	offset := 0
	if v := c.Query("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	logs, err := h.auditService.GetAuditLogs(userIDPtr, actionPtr, resourceTypePtr, statusPtr, startPtr, endPtr, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to fetch audit logs",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"logs":   logs,
		"limit":  limit,
		"offset": offset,
	})
}

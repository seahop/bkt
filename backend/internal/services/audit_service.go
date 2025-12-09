package services

import (
	"bkt/internal/database"
	"bkt/internal/models"
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// AuditService handles audit logging for administrative actions
type AuditService struct{}

// NewAuditService creates a new audit service
func NewAuditService() *AuditService {
	return &AuditService{}
}

// LogAction logs an administrative action to the audit log
func (as *AuditService) LogAction(
	c *gin.Context,
	userID uuid.UUID,
	username string,
	action string,
	resourceType string,
	resourceID string,
	resourceName string,
	status string,
	errorMessage string,
	metadata map[string]interface{},
) error {
	// Get request ID from context (set by RequestIDMiddleware)
	requestID := ""
	if reqID, exists := c.Get("request_id"); exists {
		requestID = reqID.(string)
	}

	// Get client IP
	ipAddress := c.ClientIP()

	// Get User-Agent
	userAgent := c.GetHeader("User-Agent")

	// Convert metadata to JSON string
	var metadataJSON string
	if metadata != nil {
		metadataBytes, err := json.Marshal(metadata)
		if err == nil {
			metadataJSON = string(metadataBytes)
		}
	}

	// Create audit log entry
	auditLog := models.AuditLog{
		UserID:       userID,
		Username:     username,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		ResourceName: resourceName,
		IPAddress:    ipAddress,
		UserAgent:    userAgent,
		RequestID:    requestID,
		Status:       status,
		ErrorMessage: errorMessage,
		Metadata:     metadataJSON,
		CreatedAt:    time.Now(),
	}

	// Save to database
	result := database.DB.Create(&auditLog)
	return result.Error
}

// LogSuccess logs a successful administrative action
func (as *AuditService) LogSuccess(
	c *gin.Context,
	userID uuid.UUID,
	username string,
	action string,
	resourceType string,
	resourceID string,
	resourceName string,
	metadata map[string]interface{},
) error {
	return as.LogAction(
		c,
		userID,
		username,
		action,
		resourceType,
		resourceID,
		resourceName,
		"success",
		"",
		metadata,
	)
}

// LogFailure logs a failed administrative action
func (as *AuditService) LogFailure(
	c *gin.Context,
	userID uuid.UUID,
	username string,
	action string,
	resourceType string,
	resourceID string,
	resourceName string,
	errorMessage string,
	metadata map[string]interface{},
) error {
	return as.LogAction(
		c,
		userID,
		username,
		action,
		resourceType,
		resourceID,
		resourceName,
		"failure",
		errorMessage,
		metadata,
	)
}

// LogDenied logs a denied administrative action (due to authorization)
func (as *AuditService) LogDenied(
	c *gin.Context,
	userID uuid.UUID,
	username string,
	action string,
	resourceType string,
	resourceID string,
	resourceName string,
	reason string,
	metadata map[string]interface{},
) error {
	return as.LogAction(
		c,
		userID,
		username,
		action,
		resourceType,
		resourceID,
		resourceName,
		"denied",
		reason,
		metadata,
	)
}

// GetAuditLogs retrieves audit logs with optional filters
func (as *AuditService) GetAuditLogs(
	userID *uuid.UUID,
	action *string,
	resourceType *string,
	status *string,
	startTime *time.Time,
	endTime *time.Time,
	limit int,
	offset int,
) ([]models.AuditLog, error) {
	query := database.DB.Model(&models.AuditLog{})

	// Apply filters
	if userID != nil {
		query = query.Where("user_id = ?", userID)
	}
	if action != nil {
		query = query.Where("action = ?", action)
	}
	if resourceType != nil {
		query = query.Where("resource_type = ?", resourceType)
	}
	if status != nil {
		query = query.Where("status = ?", status)
	}
	if startTime != nil {
		query = query.Where("created_at >= ?", startTime)
	}
	if endTime != nil {
		query = query.Where("created_at <= ?", endTime)
	}

	// Order by most recent first
	query = query.Order("created_at DESC")

	// Apply pagination
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	var logs []models.AuditLog
	result := query.Preload("User").Find(&logs)
	return logs, result.Error
}

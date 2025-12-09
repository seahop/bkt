package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type PolicyHandler struct {
	config *config.Config
}

func NewPolicyHandler(cfg *config.Config) *PolicyHandler {
	return &PolicyHandler{config: cfg}
}

// ListPolicies lists all policies (admin only) or user's attached policies
func (h *PolicyHandler) ListPolicies(c *gin.Context) {
	userID, _ := c.Get("user_id")
	isAdmin, _ := c.Get("is_admin")

	policies := make([]models.Policy, 0)

	if isAdmin.(bool) {
		// Admins can see all policies
		if err := database.DB.Find(&policies).Error; err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to list policies",
				Message: err.Error(),
			})
			return
		}
	} else {
		// Regular users can only see their attached policies
		var user models.User
		if err := database.DB.Preload("Policies").Where("id = ?", userID).First(&user).Error; err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to fetch user policies",
				Message: err.Error(),
			})
			return
		}
		policies = user.Policies
	}

	c.JSON(http.StatusOK, policies)
}

// CreatePolicy creates a new policy (admin only)
func (h *PolicyHandler) CreatePolicy(c *gin.Context) {
	isAdmin, _ := c.Get("is_admin")

	// Only admins can create policies
	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Only administrators can create policies",
		})
		return
	}

	var req models.CreatePolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Validate policy document for security
	policyDoc, err := security.ValidatePolicyDocument(req.Document)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid policy document",
			Message: err.Error(),
		})
		return
	}

	// Re-serialize validated policy (prevents injection attacks)
	validatedDoc, err := json.Marshal(policyDoc)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to serialize policy document",
			Message: err.Error(),
		})
		return
	}

	// Check if policy with same name already exists
	var existingPolicy models.Policy
	if err := database.DB.Where("name = ?", req.Name).First(&existingPolicy).Error; err == nil {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error: "Policy with this name already exists",
		})
		return
	}

	// Create policy
	policy := models.Policy{
		Name:        req.Name,
		Description: req.Description,
		Document:    string(validatedDoc),
	}

	if err := database.DB.Create(&policy).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create policy",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusCreated, policy)
}

// GetPolicy gets a specific policy
func (h *PolicyHandler) GetPolicy(c *gin.Context) {
	policyID := c.Param("id")
	isAdmin, _ := c.Get("is_admin")

	policyUUID, err := uuid.Parse(policyID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid policy ID",
		})
		return
	}

	var policy models.Policy
	if err := database.DB.Where("id = ?", policyUUID).First(&policy).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Policy not found",
		})
		return
	}

	// Only admins can view any policy
	// Regular users can only view policies attached to them (checked in ListPolicies)
	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Access denied",
		})
		return
	}

	c.JSON(http.StatusOK, policy)
}

// UpdatePolicy updates a policy (admin only)
func (h *PolicyHandler) UpdatePolicy(c *gin.Context) {
	isAdmin, _ := c.Get("is_admin")

	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Only administrators can update policies",
		})
		return
	}

	policyID := c.Param("id")
	policyUUID, err := uuid.Parse(policyID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid policy ID",
		})
		return
	}

	var req models.UpdatePolicyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	var policy models.Policy
	if err := database.DB.Where("id = ?", policyUUID).First(&policy).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Policy not found",
		})
		return
	}

	// Validate new policy document if provided
	if req.Document != "" {
		policyDoc, err := security.ValidatePolicyDocument(req.Document)
		if err != nil {
			c.JSON(http.StatusBadRequest, models.ErrorResponse{
				Error:   "Invalid policy document",
				Message: err.Error(),
			})
			return
		}

		// Re-serialize validated policy
		validatedDoc, err := json.Marshal(policyDoc)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to serialize policy document",
				Message: err.Error(),
			})
			return
		}
		policy.Document = string(validatedDoc)
	}

	// Update other fields
	if req.Name != "" {
		policy.Name = req.Name
	}
	if req.Description != "" {
		policy.Description = req.Description
	}

	if err := database.DB.Save(&policy).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to update policy",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, policy)
}

// DeletePolicy deletes a policy (admin only)
func (h *PolicyHandler) DeletePolicy(c *gin.Context) {
	isAdmin, _ := c.Get("is_admin")

	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Only administrators can delete policies",
		})
		return
	}

	policyID := c.Param("id")
	policyUUID, err := uuid.Parse(policyID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid policy ID",
		})
		return
	}

	var policy models.Policy
	if err := database.DB.Where("id = ?", policyUUID).First(&policy).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Policy not found",
		})
		return
	}

	// Check if policy is attached to any users
	var userCount int64
	database.DB.Table("user_policies").Where("policy_id = ?", policyUUID).Count(&userCount)
	if userCount > 0 {
		c.JSON(http.StatusConflict, models.ErrorResponse{
			Error:   "Cannot delete policy",
			Message: "Policy is attached to users. Detach it first.",
		})
		return
	}

	if err := database.DB.Delete(&policy).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete policy",
			Message: err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Policy deleted successfully",
	})
}

// AttachPolicyToUser attaches a policy to a user (admin only)
func (h *PolicyHandler) AttachPolicyToUser(c *gin.Context) {
	isAdmin, _ := c.Get("is_admin")

	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Only administrators can attach policies",
		})
		return
	}

	userIDParam := c.Param("user_id")
	userUUID, err := uuid.Parse(userIDParam)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid user ID",
		})
		return
	}

	var req struct {
		PolicyID string `json:"policy_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	policyUUID, err := uuid.Parse(req.PolicyID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid policy ID",
		})
		return
	}

	// Use transaction to ensure atomicity (prevents TOCTOU race)
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// Verify user exists (within transaction)
		var user models.User
		if err := tx.Where("id = ?", userUUID).First(&user).Error; err != nil {
			return fmt.Errorf("user not found")
		}

		// Verify policy exists (within transaction)
		var policy models.Policy
		if err := tx.Where("id = ?", policyUUID).First(&policy).Error; err != nil {
			return fmt.Errorf("policy not found")
		}

		// Attach policy (GORM handles many-to-many, prevents duplicates)
		if err := tx.Model(&user).Association("Policies").Append(&policy); err != nil {
			return fmt.Errorf("failed to attach policy: %w", err)
		}

		return nil
	})

	if err != nil {
		// Determine appropriate status code based on error
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, models.ErrorResponse{
				Error: err.Error(),
			})
		} else {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to attach policy",
				Message: err.Error(),
			})
		}
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Policy attached successfully",
	})
}

// DetachPolicyFromUser detaches a policy from a user (admin only)
func (h *PolicyHandler) DetachPolicyFromUser(c *gin.Context) {
	isAdmin, _ := c.Get("is_admin")

	if !isAdmin.(bool) {
		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error: "Only administrators can detach policies",
		})
		return
	}

	userIDParam := c.Param("user_id")
	policyIDParam := c.Param("policy_id")

	userUUID, err := uuid.Parse(userIDParam)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid user ID",
		})
		return
	}

	policyUUID, err := uuid.Parse(policyIDParam)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid policy ID",
		})
		return
	}

	// Use transaction to ensure atomicity (prevents TOCTOU race)
	err = database.DB.Transaction(func(tx *gorm.DB) error {
		// Verify user exists (within transaction)
		var user models.User
		if err := tx.Where("id = ?", userUUID).First(&user).Error; err != nil {
			return fmt.Errorf("user not found")
		}

		// Verify policy exists (within transaction)
		var policy models.Policy
		if err := tx.Where("id = ?", policyUUID).First(&policy).Error; err != nil {
			return fmt.Errorf("policy not found")
		}

		// Detach policy (GORM handles many-to-many)
		if err := tx.Model(&user).Association("Policies").Delete(&policy); err != nil {
			return fmt.Errorf("failed to detach policy: %w", err)
		}

		return nil
	})

	if err != nil {
		// Determine appropriate status code based on error
		if strings.Contains(err.Error(), "not found") {
			c.JSON(http.StatusNotFound, models.ErrorResponse{
				Error: err.Error(),
			})
		} else {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error:   "Failed to detach policy",
				Message: err.Error(),
			})
		}
		return
	}

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Policy detached successfully",
	})
}

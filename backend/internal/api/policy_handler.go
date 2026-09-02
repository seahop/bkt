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
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type PolicyHandler struct {
	config       *config.Config
	auditService *services.AuditService
}

func NewPolicyHandler(cfg *config.Config) *PolicyHandler {
	return &PolicyHandler{config: cfg, auditService: services.NewAuditService()}
}

// actor extracts the authenticated admin's id/username from the request context
// for audit logging.
func actor(c *gin.Context) (uuid.UUID, string) {
	id, _ := c.Get("user_id")
	name, _ := c.Get("username")
	uid, _ := id.(uuid.UUID)
	uname, _ := name.(string)
	return uid, uname
}

// ListPolicies lists all policies (admin only) or user's attached policies
// @Summary List policies
// @Description Returns all policies for admins, or only the policies attached to the authenticated user for regular users.
// @Tags policies
// @Accept json
// @Produce json
// @Success 200 {array} models.Policy
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/policies [get]
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
// @Summary Create a policy
// @Description Admin-only. Creates a new IAM-style policy with a validated policy document.
// @Tags policies
// @Accept json
// @Produce json
// @Param request body models.CreatePolicyRequest true "Policy definition"
// @Success 201 {object} models.Policy
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/policies [post]
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

	uid, uname := actor(c)
	h.auditService.LogSuccess(c, uid, uname, "policy.create", "policy", policy.ID.String(), policy.Name, nil)

	c.JSON(http.StatusCreated, policy)
}

// GetPolicy gets a specific policy
// @Summary Get a policy
// @Description Admin-only. Returns the details of a specific policy by ID.
// @Tags policies
// @Accept json
// @Produce json
// @Param id path string true "Policy ID"
// @Success 200 {object} models.Policy
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/policies/{id} [get]
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
// @Summary Update a policy
// @Description Admin-only. Updates an existing policy's name, description, or document. The policy document is validated before saving.
// @Tags policies
// @Accept json
// @Produce json
// @Param id path string true "Policy ID"
// @Param request body models.UpdatePolicyRequest true "Updated policy fields"
// @Success 200 {object} models.Policy
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/policies/{id} [put]
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

	uid, uname := actor(c)
	h.auditService.LogSuccess(c, uid, uname, "policy.update", "policy", policy.ID.String(), policy.Name, nil)

	c.JSON(http.StatusOK, policy)
}

// DeletePolicy deletes a policy (admin only)
// @Summary Delete a policy
// @Description Admin-only. Deletes a policy. Fails if the policy is still attached to any users.
// @Tags policies
// @Accept json
// @Produce json
// @Param id path string true "Policy ID"
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/policies/{id} [delete]
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

	uid, uname := actor(c)
	h.auditService.LogSuccess(c, uid, uname, "policy.delete", "policy", policy.ID.String(), policy.Name, nil)

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Policy deleted successfully",
	})
}

// AttachPolicyToUser attaches a policy to a user (admin only)
// @Summary Attach a policy to a user
// @Description Admin-only. Attaches an existing policy to a user, granting them the policy's permissions.
// @Tags policies
// @Accept json
// @Produce json
// @Param user_id path string true "User ID"
// @Param request body object true "Policy to attach" SchemaExample({"policy_id":"uuid-here"})
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/policies/users/{user_id}/attach [post]
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

	uid, uname := actor(c)
	h.auditService.LogSuccess(c, uid, uname, "policy.attach", "user", userUUID.String(), "", map[string]interface{}{"policy_id": req.PolicyID})

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Policy attached successfully",
	})
}

// DetachPolicyFromUser detaches a policy from a user (admin only)
// @Summary Detach a policy from a user
// @Description Admin-only. Removes the association between a policy and a user, revoking those permissions.
// @Tags policies
// @Accept json
// @Produce json
// @Param user_id path string true "User ID"
// @Param policy_id path string true "Policy ID"
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/policies/users/{user_id}/detach/{policy_id} [delete]
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

	uid, uname := actor(c)
	h.auditService.LogSuccess(c, uid, uname, "policy.detach", "user", userUUID.String(), "", map[string]interface{}{"policy_id": policyUUID.String()})

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Policy detached successfully",
	})
}

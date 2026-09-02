package api

import (
	"net/http"
	"strings"
	"bkt/internal/auth"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type UserHandler struct {
	config       *config.Config
	auditService *services.AuditService
}

func NewUserHandler(cfg *config.Config) *UserHandler {
	return &UserHandler{
		config:       cfg,
		auditService: services.NewAuditService(),
	}
}

// GetCurrentUser returns the authenticated user's profile
// @Summary Get current user profile
// @Description Returns the profile of the currently authenticated user.
// @Tags users
// @Accept json
// @Produce json
// @Success 200 {object} models.User
// @Failure 404 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/users/me [get]
func (h *UserHandler) GetCurrentUser(c *gin.Context) {
	userID, _ := c.Get("user_id")

	var user models.User
	// Don't preload Buckets and AccessKeys to avoid memory issues with large datasets
	// Clients should use dedicated endpoints to list buckets/keys if needed
	if err := database.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "User not found",
		})
		return
	}

	c.JSON(http.StatusOK, user)
}

// UpdateCurrentUser updates the authenticated user's profile
// @Summary Update current user profile
// @Description Updates the email and/or password of the currently authenticated user.
// @Tags users
// @Accept json
// @Produce json
// @Param request body object true "Fields to update" SchemaExample({"email":"user@example.com","password":"newpassword"})
// @Success 200 {object} models.User
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/users/me [put]
func (h *UserHandler) UpdateCurrentUser(c *gin.Context) {
	userID, _ := c.Get("user_id")

	var req struct {
		Email           string `json:"email" binding:"omitempty,email"`
		CurrentPassword string `json:"current_password,omitempty"`
		Password        string `json:"password,omitempty"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	var user models.User
	if err := database.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "User not found",
		})
		return
	}

	// Update email if provided (already validated by binding tag)
	if req.Email != "" {
		user.Email = req.Email
	}

	passwordChanged := false
	if req.Password != "" {
		// SSO-provisioned accounts are governed by the identity provider and
		// must not be able to set a local password (which would bypass SSO
		// policy sync and let them log in outside SSO).
		if user.SSOProvider != "" {
			c.JSON(http.StatusForbidden, models.ErrorResponse{
				Error:   "Password change not allowed",
				Message: "This account signs in via SSO and cannot set a local password.",
			})
			return
		}

		// Require the current password (re-authentication) so a stolen/borrowed
		// session token cannot silently take over the account.
		if user.Password == "" || !auth.CheckPassword(req.CurrentPassword, user.Password) {
			c.JSON(http.StatusUnauthorized, models.ErrorResponse{
				Error:   "Current password is incorrect",
				Message: "Please provide your current password to change it.",
			})
			return
		}

		if len(req.Password) < 8 {
			c.JSON(http.StatusBadRequest, models.ErrorResponse{
				Error:   "Password too short",
				Message: "Password must be at least 8 characters.",
			})
			return
		}

		hashedPassword, err := auth.HashPassword(req.Password, h.config.Auth.BcryptCost)
		if err != nil {
			c.JSON(http.StatusInternalServerError, models.ErrorResponse{
				Error: "Failed to hash password",
			})
			return
		}
		user.Password = hashedPassword
		// Invalidate all outstanding sessions on password change.
		user.TokenVersion++
		passwordChanged = true
	}

	if err := database.DB.Save(&user).Error; err != nil {
		// Surface a unique-constraint clash (e.g. email already in use) as 409
		// rather than a generic 500.
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "duplicate") {
			c.JSON(http.StatusConflict, models.ErrorResponse{
				Error:   "Email already in use",
				Message: "That email address is already associated with another account.",
			})
			return
		}
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to update user",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	if h.auditService != nil {
		if passwordChanged {
			_ = h.auditService.LogSuccess(c, user.ID, user.Username, "user.password_change", "user", user.ID.String(), user.Username, nil)
		} else if req.Email != "" {
			_ = h.auditService.LogSuccess(c, user.ID, user.Username, "user.email_change", "user", user.ID.String(), user.Username, nil)
		}
	}

	c.JSON(http.StatusOK, user)
}

// CreateUser creates a new user account (admin only)
// @Summary Create a new user
// @Description Admin-only. Creates a new user account with the specified credentials and role.
// @Tags users
// @Accept json
// @Produce json
// @Param request body object true "New user details" SchemaExample({"username":"alice","email":"alice@example.com","password":"secret123","is_admin":false})
// @Success 201 {object} models.User
// @Failure 400 {object} models.ErrorResponse
// @Failure 409 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/users [post]
func (h *UserHandler) CreateUser(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Email    string `json:"email" binding:"required,email"`
		Password string `json:"password" binding:"required,min=8"`
		IsAdmin  bool   `json:"is_admin"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error:   "Invalid request",
			Message: err.Error(),
		})
		return
	}

	// Hash password
	hashedPassword, err := auth.HashPassword(req.Password, h.config.Auth.BcryptCost)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to hash password",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	// Create new user
	user := models.User{
		Username: req.Username,
		Email:    req.Email,
		Password: hashedPassword,
		IsAdmin:  req.IsAdmin,
	}

	if err := database.DB.Create(&user).Error; err != nil {
		// Get admin user info for audit log
		adminUserID, _ := c.Get("user_id")
		adminUsername, _ := c.Get("username")

		// Log failure
		_ = h.auditService.LogFailure(
			c,
			adminUserID.(uuid.UUID),
			adminUsername.(string),
			"CreateUser",
			"User",
			"",
			req.Username,
			err.Error(),
			map[string]interface{}{
				"target_username": req.Username,
				"target_email":    req.Email,
				"is_admin":        req.IsAdmin,
			},
		)

		// Check for unique constraint violations
		errMsg := err.Error()
		if strings.Contains(errMsg, "duplicate key") || strings.Contains(errMsg, "unique constraint") {
			// Determine which field caused the violation
			if strings.Contains(errMsg, "username") || strings.Contains(errMsg, "idx_users_username") {
				c.JSON(http.StatusConflict, models.ErrorResponse{
					Error:   "Username already exists",
					Message: "A user with this username already exists",
				})
				return
			}
			if strings.Contains(errMsg, "email") || strings.Contains(errMsg, "idx_users_email") {
				c.JSON(http.StatusConflict, models.ErrorResponse{
					Error:   "Email already exists",
					Message: "A user with this email already exists",
				})
				return
			}
		}
		// Generic database error
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to create user",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	// Get admin user info for audit log
	adminUserID, _ := c.Get("user_id")
	adminUsername, _ := c.Get("username")

	// Log success
	_ = h.auditService.LogSuccess(
		c,
		adminUserID.(uuid.UUID),
		adminUsername.(string),
		"CreateUser",
		"User",
		user.ID.String(),
		user.Username,
		map[string]interface{}{
			"target_username": user.Username,
			"target_email":    user.Email,
			"is_admin":        user.IsAdmin,
		},
	)

	// Don't return password hash
	user.Password = ""
	c.JSON(http.StatusCreated, user)
}

// ListUsers lists all users (admin only)
// @Summary List all users
// @Description Admin-only. Returns a list of all user accounts in the system.
// @Tags users
// @Accept json
// @Produce json
// @Success 200 {array} models.User
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/users [get]
func (h *UserHandler) ListUsers(c *gin.Context) {
	users := make([]models.User, 0)
	// Don't preload Policies to avoid memory issues when there are many users
	// Use dedicated policy endpoints if policy details are needed
	if err := database.DB.Find(&users).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to fetch users",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	c.JSON(http.StatusOK, users)
}

// DeleteUser deletes a user account (admin only)
// @Summary Delete a user
// @Description Admin-only. Permanently deletes the specified user account.
// @Tags users
// @Accept json
// @Produce json
// @Param id path string true "User ID"
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/users/{id} [delete]
func (h *UserHandler) DeleteUser(c *gin.Context) {
	userIDStr := c.Param("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid user ID",
		})
		return
	}

	// Get user info before deletion for audit log
	var targetUser models.User
	if err := database.DB.First(&targetUser, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "User not found",
		})
		return
	}

	// Hard-delete the user's access keys: the FK (fk_users_access_keys)
	// otherwise blocks deleting any user who ever created a key, and a key
	// row pointing at a deleted user is useless (S3 auth loads the user row).
	// Key issuance/revocation history lives in the audit log.
	if err := database.DB.Where("user_id = ?", userID).Delete(&models.AccessKey{}).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete user",
			Message: "Could not remove the user's access keys. Please try again.",
		})
		return
	}

	// Detach policies and group memberships: pure join rows that must die
	// with the user (their FKs otherwise block deletion).
	if err := database.DB.Exec(`DELETE FROM user_policies WHERE user_id = ?`, userID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete user",
			Message: "Could not detach the user's policies. Please try again.",
		})
		return
	}
	if err := database.DB.Exec(`DELETE FROM user_groups WHERE user_id = ?`, userID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete user",
			Message: "Could not remove the user's group memberships. Please try again.",
		})
		return
	}

	if err := database.DB.Delete(&models.User{}, "id = ?", userID).Error; err != nil {
		// Get admin user info for audit log
		adminUserID, _ := c.Get("user_id")
		adminUsername, _ := c.Get("username")

		// Log failure
		_ = h.auditService.LogFailure(
			c,
			adminUserID.(uuid.UUID),
			adminUsername.(string),
			"DeleteUser",
			"User",
			userID.String(),
			targetUser.Username,
			err.Error(),
			map[string]interface{}{
				"target_username": targetUser.Username,
				"target_email":    targetUser.Email,
			},
		)

		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete user",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	// Get admin user info for audit log
	adminUserID, _ := c.Get("user_id")
	adminUsername, _ := c.Get("username")

	// Log success
	_ = h.auditService.LogSuccess(
		c,
		adminUserID.(uuid.UUID),
		adminUsername.(string),
		"DeleteUser",
		"User",
		userID.String(),
		targetUser.Username,
		map[string]interface{}{
			"target_username": targetUser.Username,
			"target_email":    targetUser.Email,
		},
	)

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "User deleted successfully",
	})
}

// LockUser locks a user account to prevent login
// @Summary Lock a user account
// @Description Admin-only. Locks a user account to prevent the user from logging in. Admin accounts cannot be locked.
// @Tags users
// @Accept json
// @Produce json
// @Param id path string true "User ID"
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 403 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/users/{id}/lock [post]
func (h *UserHandler) LockUser(c *gin.Context) {
	userIDStr := c.Param("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid user ID",
		})
		return
	}

	var user models.User
	if err := database.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "User not found",
		})
		return
	}

	// Prevent locking admin users
	if user.IsAdmin {
		// Get admin user info for audit log
		adminUserID, _ := c.Get("user_id")
		adminUsername, _ := c.Get("username")

		// Log denied action
		_ = h.auditService.LogDenied(
			c,
			adminUserID.(uuid.UUID),
			adminUsername.(string),
			"LockUser",
			"User",
			userID.String(),
			user.Username,
			"Cannot lock admin user",
			map[string]interface{}{
				"target_username": user.Username,
				"is_admin":        true,
			},
		)

		c.JSON(http.StatusForbidden, models.ErrorResponse{
			Error:   "Cannot lock admin user",
			Message: "Admin users cannot be locked",
		})
		return
	}

	user.IsLocked = true
	// Invalidate outstanding sessions immediately so the lock takes effect on
	// the console path without waiting for token expiry.
	user.TokenVersion++
	if err := database.DB.Save(&user).Error; err != nil {
		// Get admin user info for audit log
		adminUserID, _ := c.Get("user_id")
		adminUsername, _ := c.Get("username")

		// Log failure
		_ = h.auditService.LogFailure(
			c,
			adminUserID.(uuid.UUID),
			adminUsername.(string),
			"LockUser",
			"User",
			userID.String(),
			user.Username,
			err.Error(),
			map[string]interface{}{
				"target_username": user.Username,
			},
		)

		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to lock user",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	// NOTE: the user's access keys are deliberately NOT deactivated here. The
	// S3 auth path re-checks the live User.IsLocked flag on every request, so
	// the lock already blocks S3 access — and leaving is_active untouched means
	// unlocking restores the user's keys instead of silently bricking them.

	// Get admin user info for audit log
	adminUserID, _ := c.Get("user_id")
	adminUsername, _ := c.Get("username")

	// Log success
	_ = h.auditService.LogSuccess(
		c,
		adminUserID.(uuid.UUID),
		adminUsername.(string),
		"LockUser",
		"User",
		userID.String(),
		user.Username,
		map[string]interface{}{
			"target_username": user.Username,
		},
	)

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "User locked successfully",
	})
}

// UnlockUser unlocks a user account to allow login
// @Summary Unlock a user account
// @Description Admin-only. Unlocks a previously locked user account to allow login again.
// @Tags users
// @Accept json
// @Produce json
// @Param id path string true "User ID"
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/users/{id}/unlock [post]
func (h *UserHandler) UnlockUser(c *gin.Context) {
	userIDStr := c.Param("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid user ID",
		})
		return
	}

	var user models.User
	if err := database.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "User not found",
		})
		return
	}

	user.IsLocked = false
	if err := database.DB.Save(&user).Error; err != nil {
		// Get admin user info for audit log
		adminUserID, _ := c.Get("user_id")
		adminUsername, _ := c.Get("username")

		// Log failure
		_ = h.auditService.LogFailure(
			c,
			adminUserID.(uuid.UUID),
			adminUsername.(string),
			"UnlockUser",
			"User",
			userID.String(),
			user.Username,
			err.Error(),
			map[string]interface{}{
				"target_username": user.Username,
			},
		)

		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to unlock user",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	// Get admin user info for audit log
	adminUserID, _ := c.Get("user_id")
	adminUsername, _ := c.Get("username")

	// Log success
	_ = h.auditService.LogSuccess(
		c,
		adminUserID.(uuid.UUID),
		adminUsername.(string),
		"UnlockUser",
		"User",
		userID.String(),
		user.Username,
		map[string]interface{}{
			"target_username": user.Username,
		},
	)

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "User unlocked successfully",
	})
}

// ListUserAccessKeys lists all access keys for a specific user (admin only)
// @Summary List access keys for a user
// @Description Admin-only. Returns all access keys associated with the specified user.
// @Tags users
// @Accept json
// @Produce json
// @Param id path string true "User ID"
// @Success 200 {array} models.AccessKey
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/users/{id}/access-keys [get]
func (h *UserHandler) ListUserAccessKeys(c *gin.Context) {
	userIDStr := c.Param("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid user ID",
		})
		return
	}

	// Check user exists
	var user models.User
	if err := database.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "User not found",
		})
		return
	}

	// Get all access keys for the user
	var accessKeys []models.AccessKey
	if err := database.DB.Where("user_id = ?", userID).Order("created_at DESC").Find(&accessKeys).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to list access keys",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	c.JSON(http.StatusOK, accessKeys)
}

// DeleteUserAccessKey deletes a specific access key for a user (admin only)
// @Summary Delete a user's access key
// @Description Admin-only. Permanently deletes (hard delete) the specified access key belonging to the given user.
// @Tags users
// @Accept json
// @Produce json
// @Param id path string true "User ID"
// @Param key_id path string true "Access Key ID"
// @Success 200 {object} models.SuccessResponse
// @Failure 400 {object} models.ErrorResponse
// @Failure 404 {object} models.ErrorResponse
// @Failure 500 {object} models.ErrorResponse
// @Security BearerAuth
// @Router /api/users/{id}/access-keys/{key_id} [delete]
func (h *UserHandler) DeleteUserAccessKey(c *gin.Context) {
	userIDStr := c.Param("id")
	keyIDStr := c.Param("key_id")

	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid user ID",
		})
		return
	}

	keyID, err := uuid.Parse(keyIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{
			Error: "Invalid key ID",
		})
		return
	}

	// Check user exists
	var user models.User
	if err := database.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "User not found",
		})
		return
	}

	// Find and delete the access key
	var accessKey models.AccessKey
	if err := database.DB.Where("id = ? AND user_id = ?", keyID, userID).First(&accessKey).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{
			Error: "Access key not found",
		})
		return
	}

	// Hard delete the access key for admin revocation
	if err := database.DB.Unscoped().Delete(&accessKey).Error; err != nil {
		// Get admin user info for audit log
		adminUserID, _ := c.Get("user_id")
		adminUsername, _ := c.Get("username")

		// Log failure
		_ = h.auditService.LogFailure(
			c,
			adminUserID.(uuid.UUID),
			adminUsername.(string),
			"DeleteAccessKey",
			"AccessKey",
			keyID.String(),
			accessKey.AccessKey,
			err.Error(),
			map[string]interface{}{
				"target_user_id":  userID.String(),
				"target_username": user.Username,
				"access_key":      accessKey.AccessKey,
			},
		)

		c.JSON(http.StatusInternalServerError, models.ErrorResponse{
			Error:   "Failed to delete access key",
			Message: "An internal error occurred. Please try again.",
		})
		return
	}

	// Get admin user info for audit log
	adminUserID, _ := c.Get("user_id")
	adminUsername, _ := c.Get("username")

	// Log success
	_ = h.auditService.LogSuccess(
		c,
		adminUserID.(uuid.UUID),
		adminUsername.(string),
		"DeleteAccessKey",
		"AccessKey",
		keyID.String(),
		accessKey.AccessKey,
		map[string]interface{}{
			"target_user_id":  userID.String(),
			"target_username": user.Username,
			"access_key":      accessKey.AccessKey,
		},
	)

	c.JSON(http.StatusOK, models.SuccessResponse{
		Message: "Access key deleted successfully",
	})
}

package api

import (
	"net/http"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GroupHandler manages groups: named sets of users that policies attach to.
// A user's effective policies = direct policies ∪ policies of their groups.
type GroupHandler struct {
	auditService *services.AuditService
}

func NewGroupHandler() *GroupHandler {
	return &GroupHandler{auditService: services.NewAuditService()}
}

func (h *GroupHandler) audit(c *gin.Context, action, resourceID, resourceName string, meta map[string]interface{}) {
	uid, uname := actor(c)
	h.auditService.LogSuccess(c, uid, uname, action, "group", resourceID, resourceName, meta)
}

// ListGroups handles GET /api/groups (admin).
// @Summary List groups
// @Tags groups
// @Produce json
// @Success 200 {array} models.Group
// @Security BearerAuth
// @Router /api/groups [get]
func (h *GroupHandler) ListGroups(c *gin.Context) {
	groups := []models.Group{}
	if err := database.DB.Preload("Users").Preload("Policies").Order("name ASC").Find(&groups).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to list groups"})
		return
	}
	c.JSON(http.StatusOK, groups)
}

// CreateGroup handles POST /api/groups {name, description} (admin).
// @Summary Create a group
// @Tags groups
// @Accept json
// @Produce json
// @Success 201 {object} models.Group
// @Security BearerAuth
// @Router /api/groups [post]
func (h *GroupHandler) CreateGroup(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required,min=2,max=64"`
		Description string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid request", Message: err.Error()})
		return
	}
	group := models.Group{Name: req.Name, Description: req.Description}
	if err := database.DB.Create(&group).Error; err != nil {
		c.JSON(http.StatusConflict, models.ErrorResponse{Error: "Group already exists or could not be created"})
		return
	}
	h.audit(c, "group.create", group.ID.String(), group.Name, nil)
	c.JSON(http.StatusCreated, group)
}

// DeleteGroup handles DELETE /api/groups/:id (admin). Memberships and policy
// attachments are removed; users and policies themselves are untouched.
// @Summary Delete a group
// @Tags groups
// @Produce json
// @Success 200 {object} models.SuccessResponse
// @Security BearerAuth
// @Router /api/groups/{id} [delete]
func (h *GroupHandler) DeleteGroup(c *gin.Context) {
	group, ok := h.loadGroup(c)
	if !ok {
		return
	}
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`DELETE FROM user_groups WHERE group_id = ?`, group.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM group_policies WHERE group_id = ?`, group.ID).Error; err != nil {
			return err
		}
		return tx.Delete(&models.Group{}, "id = ?", group.ID).Error
	}); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to delete group"})
		return
	}
	h.audit(c, "group.delete", group.ID.String(), group.Name, nil)
	c.JSON(http.StatusOK, models.SuccessResponse{Message: "Group deleted"})
}

func (h *GroupHandler) loadGroup(c *gin.Context) (*models.Group, bool) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid group ID"})
		return nil, false
	}
	var group models.Group
	if err := database.DB.First(&group, "id = ?", id).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Error: "Group not found"})
		return nil, false
	}
	return &group, true
}

// AddGroupMember handles POST /api/groups/:id/members {user_id} (admin).
// @Summary Add a user to a group
// @Tags groups
// @Accept json
// @Produce json
// @Success 200 {object} models.SuccessResponse
// @Security BearerAuth
// @Router /api/groups/{id}/members [post]
func (h *GroupHandler) AddGroupMember(c *gin.Context) {
	group, ok := h.loadGroup(c)
	if !ok {
		return
	}
	var req struct {
		UserID string `json:"user_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid request", Message: err.Error()})
		return
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid user ID"})
		return
	}
	var user models.User
	if err := database.DB.First(&user, "id = ?", userID).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Error: "User not found"})
		return
	}
	if err := database.DB.Exec(
		`INSERT INTO user_groups (user_id, group_id) VALUES (?, ?) ON CONFLICT DO NOTHING`,
		userID, group.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to add member"})
		return
	}
	h.audit(c, "group.member_add", group.ID.String(), group.Name, map[string]interface{}{"user": user.Username})
	c.JSON(http.StatusOK, models.SuccessResponse{Message: "Member added"})
}

// RemoveGroupMember handles DELETE /api/groups/:id/members/:user_id (admin).
// @Summary Remove a user from a group
// @Tags groups
// @Produce json
// @Success 200 {object} models.SuccessResponse
// @Security BearerAuth
// @Router /api/groups/{id}/members/{user_id} [delete]
func (h *GroupHandler) RemoveGroupMember(c *gin.Context) {
	group, ok := h.loadGroup(c)
	if !ok {
		return
	}
	userID, err := uuid.Parse(c.Param("user_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid user ID"})
		return
	}
	if err := database.DB.Exec(`DELETE FROM user_groups WHERE user_id = ? AND group_id = ?`, userID, group.ID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to remove member"})
		return
	}
	h.audit(c, "group.member_remove", group.ID.String(), group.Name, map[string]interface{}{"user_id": userID.String()})
	c.JSON(http.StatusOK, models.SuccessResponse{Message: "Member removed"})
}

// AttachGroupPolicy handles POST /api/groups/:id/policies {policy_id} (admin).
// @Summary Attach a policy to a group
// @Tags groups
// @Accept json
// @Produce json
// @Success 200 {object} models.SuccessResponse
// @Security BearerAuth
// @Router /api/groups/{id}/policies [post]
func (h *GroupHandler) AttachGroupPolicy(c *gin.Context) {
	group, ok := h.loadGroup(c)
	if !ok {
		return
	}
	var req struct {
		PolicyID string `json:"policy_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid request", Message: err.Error()})
		return
	}
	policyID, err := uuid.Parse(req.PolicyID)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid policy ID"})
		return
	}
	var policy models.Policy
	if err := database.DB.First(&policy, "id = ?", policyID).Error; err != nil {
		c.JSON(http.StatusNotFound, models.ErrorResponse{Error: "Policy not found"})
		return
	}
	if err := database.DB.Exec(
		`INSERT INTO group_policies (group_id, policy_id) VALUES (?, ?) ON CONFLICT DO NOTHING`,
		group.ID, policyID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to attach policy"})
		return
	}
	h.audit(c, "group.policy_attach", group.ID.String(), group.Name, map[string]interface{}{"policy": policy.Name})
	c.JSON(http.StatusOK, models.SuccessResponse{Message: "Policy attached"})
}

// DetachGroupPolicy handles DELETE /api/groups/:id/policies/:policy_id (admin).
// @Summary Detach a policy from a group
// @Tags groups
// @Produce json
// @Success 200 {object} models.SuccessResponse
// @Security BearerAuth
// @Router /api/groups/{id}/policies/{policy_id} [delete]
func (h *GroupHandler) DetachGroupPolicy(c *gin.Context) {
	group, ok := h.loadGroup(c)
	if !ok {
		return
	}
	policyID, err := uuid.Parse(c.Param("policy_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid policy ID"})
		return
	}
	if err := database.DB.Exec(`DELETE FROM group_policies WHERE group_id = ? AND policy_id = ?`, group.ID, policyID).Error; err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to detach policy"})
		return
	}
	h.audit(c, "group.policy_detach", group.ID.String(), group.Name, map[string]interface{}{"policy_id": policyID.String()})
	c.JSON(http.StatusOK, models.SuccessResponse{Message: "Policy detached"})
}

package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

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
	_ = h.auditService.LogSuccess(c, uid, uname, action, "group", resourceID, resourceName, meta)
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
	links, err := ssoGroupsByGroup(database.DB)
	if err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to list groups"})
		return
	}
	for i := range groups {
		groups[i].SSOGroups = nonNil(links[groups[i].ID])
	}
	c.JSON(http.StatusOK, groups)
}

// CreateGroup handles POST /api/groups {name, description, sso_groups?} (admin).
// @Summary Create a group
// @Tags groups
// @Accept json
// @Produce json
// @Success 201 {object} models.Group
// @Security BearerAuth
// @Router /api/groups [post]
func (h *GroupHandler) CreateGroup(c *gin.Context) {
	var req struct {
		Name        string   `json:"name" binding:"required,min=2,max=64"`
		Description string   `json:"description"`
		SSOGroups   []string `json:"sso_groups"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid request", Message: err.Error()})
		return
	}
	ssoGroups, err := normalizeSSOGroupNames(req.SSOGroups)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid sso_groups", Message: err.Error()})
		return
	}
	group := models.Group{Name: req.Name, Description: req.Description}
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&group).Error; err != nil {
			return err
		}
		return replaceSSOGroupLinks(tx, group.ID, ssoGroups)
	}); err != nil {
		c.JSON(http.StatusConflict, models.ErrorResponse{Error: "Group already exists or could not be created"})
		return
	}
	group.SSOGroups = ssoGroups
	var meta map[string]interface{}
	if len(ssoGroups) > 0 {
		meta = map[string]interface{}{"sso_groups": ssoGroups}
	}
	h.audit(c, "group.create", group.ID.String(), group.Name, meta)
	c.JSON(http.StatusCreated, group)
}

// DeleteGroup handles DELETE /api/groups/:id (admin). Memberships, policy
// attachments and SSO group links are removed; users and policies themselves
// are untouched.
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
		// Also removed by ON DELETE CASCADE; explicit for clarity.
		if err := tx.Exec(`DELETE FROM group_sso_links WHERE group_id = ?`, group.ID).Error; err != nil {
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

// SetGroupSSOGroups handles PUT /api/groups/:id/sso-groups {sso_groups} (admin):
// it replaces the identity-provider group names linked to the group (an
// empty list unlinks it, making the group purely manual again). Memberships
// change at each SSO user's next sign-in, not immediately.
// @Summary Replace a group's linked SSO (IdP) groups
// @Tags groups
// @Accept json
// @Produce json
// @Success 200 {object} models.Group
// @Security BearerAuth
// @Router /api/groups/{id}/sso-groups [put]
func (h *GroupHandler) SetGroupSSOGroups(c *gin.Context) {
	group, ok := h.loadGroup(c)
	if !ok {
		return
	}
	var req struct {
		SSOGroups *[]string `json:"sso_groups"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid request", Message: err.Error()})
		return
	}
	if req.SSOGroups == nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid request", Message: "sso_groups is required (use [] to remove all links)"})
		return
	}
	names, err := normalizeSSOGroupNames(*req.SSOGroups)
	if err != nil {
		c.JSON(http.StatusBadRequest, models.ErrorResponse{Error: "Invalid sso_groups", Message: err.Error()})
		return
	}
	var before []string
	if err := database.DB.Transaction(func(tx *gorm.DB) error {
		// Lock the group row so concurrent replacements serialize.
		if err := tx.Exec(`SELECT id FROM "groups" WHERE id = ? FOR UPDATE`, group.ID).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.GroupSSOLink{}).Where("group_id = ?", group.ID).
			Order("LOWER(sso_group)").Pluck("sso_group", &before).Error; err != nil {
			return err
		}
		return replaceSSOGroupLinks(tx, group.ID, names)
	}); err != nil {
		c.JSON(http.StatusInternalServerError, models.ErrorResponse{Error: "Failed to update SSO groups"})
		return
	}
	added, removed := diffSSOGroupNames(before, names)
	if len(added) > 0 || len(removed) > 0 {
		h.audit(c, "group.sso_groups_update", group.ID.String(), group.Name, map[string]interface{}{
			"sso_groups": names, "added": added, "removed": removed,
		})
	}
	group.SSOGroups = names
	c.JSON(http.StatusOK, group)
}

// maxSSOGroupsPerGroup / maxSSOGroupNameLen bound the linked IdP group names.
const (
	maxSSOGroupsPerGroup = 100
	maxSSOGroupNameLen   = 256
)

// normalizeSSOGroupNames validates linked IdP group names: each is trimmed
// and must be non-empty and at most 256 characters; duplicates are dropped
// case-insensitively (the first spelling wins); at most 100 remain. The
// result is sorted case-insensitively and never nil.
func normalizeSSOGroupNames(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range in {
		name := strings.TrimSpace(raw)
		if name == "" {
			return nil, fmt.Errorf("SSO group names must not be empty")
		}
		if utf8.RuneCountInString(name) > maxSSOGroupNameLen {
			return nil, fmt.Errorf("SSO group name %.40q… exceeds %d characters", name, maxSSOGroupNameLen)
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	if len(out) > maxSSOGroupsPerGroup {
		return nil, fmt.Errorf("at most %d SSO groups can be linked to a group", maxSSOGroupsPerGroup)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i]) < strings.ToLower(out[j]) })
	return out, nil
}

// diffSSOGroupNames compares two link lists case-insensitively.
func diffSSOGroupNames(before, after []string) (added, removed []string) {
	in := func(list []string, name string) bool {
		for _, x := range list {
			if strings.EqualFold(x, name) {
				return true
			}
		}
		return false
	}
	for _, n := range after {
		if !in(before, n) {
			added = append(added, n)
		}
	}
	for _, n := range before {
		if !in(after, n) {
			removed = append(removed, n)
		}
	}
	return added, removed
}

// replaceSSOGroupLinks makes names (already normalized) the group's links.
func replaceSSOGroupLinks(tx *gorm.DB, groupID uuid.UUID, names []string) error {
	if err := tx.Where("group_id = ?", groupID).Delete(&models.GroupSSOLink{}).Error; err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	links := make([]models.GroupSSOLink, 0, len(names))
	for _, n := range names {
		links = append(links, models.GroupSSOLink{GroupID: groupID, SSOGroup: n})
	}
	return tx.Create(&links).Error
}

// ssoGroupsByGroup returns every group's linked IdP group names in one query.
func ssoGroupsByGroup(db *gorm.DB) (map[uuid.UUID][]string, error) {
	var rows []models.GroupSSOLink
	if err := db.Order("LOWER(sso_group)").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]string{}
	for _, r := range rows {
		out[r.GroupID] = append(out[r.GroupID], r.SSOGroup)
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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

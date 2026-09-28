package auth

import (
	"fmt"
	"sort"
	"strings"

	"bkt/internal/logger"
	"bkt/internal/models"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SSO group → bkt group mapping.
//
// Administrators link identity-provider group names to bkt groups
// (group_sso_links). A bkt group with at least one link is "SSO-managed": at
// every SSO sign-in the signing-in user's membership in each SSO-managed
// group is made to match their IdP groups — added when one of their IdP
// groups matches a link (case-insensitive exact match), removed otherwise.
// Groups without links are manual and never touched, and local (non-SSO)
// accounts are never touched at all.

// ssoGroupLink is one (bkt group, IdP group name) link.
type ssoGroupLink struct {
	GroupID   uuid.UUID
	GroupName string
	SSOGroup  string
}

// normalizeSSOGroup is the matching key for IdP group names: trimmed and
// case-folded.
func normalizeSSOGroup(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// planSSOGroupSync decides which SSO-managed groups the user must be added to
// and removed from. current is the set of groups the user is a member of
// (any group; non-managed ones are ignored). With groupsKnown=false the
// provider supplied no group information at all, which fails closed: the
// user belongs to no SSO-managed group. Results are sorted by group ID for
// determinism.
func planSSOGroupSync(links []ssoGroupLink, current map[uuid.UUID]bool, idpGroups []string, groupsKnown bool) (add, remove []uuid.UUID) {
	have := make(map[string]bool, len(idpGroups))
	if groupsKnown {
		for _, g := range idpGroups {
			if n := normalizeSSOGroup(g); n != "" {
				have[n] = true
			}
		}
	}
	managed := map[uuid.UUID]bool{}
	desired := map[uuid.UUID]bool{}
	for _, l := range links {
		managed[l.GroupID] = true
		if have[normalizeSSOGroup(l.SSOGroup)] {
			desired[l.GroupID] = true
		}
	}
	for id := range desired {
		if !current[id] {
			add = append(add, id)
		}
	}
	for id := range managed {
		if current[id] && !desired[id] {
			remove = append(remove, id)
		}
	}
	byString := func(ids []uuid.UUID) {
		sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	}
	byString(add)
	byString(remove)
	return add, remove
}

// SyncSSOGroupMemberships applies the SSO group → bkt group mapping for user
// at sign-in, in one transaction, and returns the names of the bkt groups the
// user was added to and removed from. idpGroups are the user's group names
// as the provider reported them; groupsKnown=false means the provider
// supplied no group information at all (fail closed: the user is removed
// from every SSO-managed group). source describes where group names were
// expected to come from (e.g. `the "groups" claim (OIDC_GROUPS_CLAIM)`) and
// is only used in the warning logged when they are missing.
//
// Local accounts (no SSO provider) are never touched, nor are memberships in
// groups without SSO links.
func SyncSSOGroupMemberships(db *gorm.DB, user *models.User, idpGroups []string, groupsKnown bool, source string) (added, removed []string, err error) {
	if user == nil || user.SSOProvider == "" {
		return nil, nil, nil
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		var links []ssoGroupLink
		if err := tx.Raw(`SELECT l.group_id, g.name AS group_name, l.sso_group
			FROM group_sso_links l JOIN "groups" g ON g.id = l.group_id`).Scan(&links).Error; err != nil {
			return fmt.Errorf("failed to load SSO group links: %w", err)
		}
		if len(links) == 0 {
			return nil // no SSO-managed groups: nothing to do
		}
		if !groupsKnown {
			logger.Warn("SSO sign-in carried no group information; removing the user from all SSO-linked groups (fail closed). Check that the identity provider sends group membership.", map[string]interface{}{
				"user": user.Username, "provider": user.SSOProvider, "expected": source,
			})
		}
		var memberOf []uuid.UUID
		if err := tx.Table("user_groups").Where("user_id = ?", user.ID).Pluck("group_id", &memberOf).Error; err != nil {
			return fmt.Errorf("failed to load group memberships: %w", err)
		}
		current := make(map[uuid.UUID]bool, len(memberOf))
		for _, id := range memberOf {
			current[id] = true
		}
		add, remove := planSSOGroupSync(links, current, idpGroups, groupsKnown)
		names := make(map[uuid.UUID]string, len(links))
		for _, l := range links {
			names[l.GroupID] = l.GroupName
		}
		for _, id := range add {
			if err := tx.Exec(`INSERT INTO user_groups (user_id, group_id) VALUES (?, ?) ON CONFLICT DO NOTHING`, user.ID, id).Error; err != nil {
				return fmt.Errorf("failed to add group membership: %w", err)
			}
			added = append(added, names[id])
		}
		if len(remove) > 0 {
			if err := tx.Exec(`DELETE FROM user_groups WHERE user_id = ? AND group_id IN ?`, user.ID, remove).Error; err != nil {
				return fmt.Errorf("failed to remove group memberships: %w", err)
			}
			for _, id := range remove {
				removed = append(removed, names[id])
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed, nil
}

// addSSOGroupAudit records the sync outcome in a login audit entry's metadata.
func addSSOGroupAudit(meta map[string]interface{}, added, removed []string) map[string]interface{} {
	if meta == nil {
		meta = map[string]interface{}{}
	}
	if len(added) > 0 {
		meta["sso_groups_added"] = added
	}
	if len(removed) > 0 {
		meta["sso_groups_removed"] = removed
	}
	return meta
}

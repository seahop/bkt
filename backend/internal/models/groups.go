package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Group is a named set of users that policies can be attached to. A user's
// effective policies are the union of their directly-attached policies and
// the policies of every group they belong to.
type Group struct {
	ID          uuid.UUID `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	Name        string    `gorm:"uniqueIndex;not null" json:"name"`
	Description string    `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	Users    []User   `gorm:"many2many:user_groups;" json:"users,omitempty"`
	Policies []Policy `gorm:"many2many:group_policies;" json:"policies,omitempty"`

	// SSOGroups are the identity-provider group names linked to this group
	// (from GroupSSOLink; not a column). SSO users whose IdP groups match any
	// of them (case-insensitively) are made members at sign-in, and removed
	// when they no longer match. A group with at least one link is
	// "SSO-managed"; groups without links are purely manual.
	SSOGroups []string `gorm:"-" json:"sso_groups"`
}

func (g *Group) BeforeCreate(tx *gorm.DB) error {
	if g.ID == uuid.Nil {
		g.ID = uuid.New()
	}
	return nil
}

// GroupSSOLink links a bkt group to an identity-provider group name. Names
// are stored trimmed but otherwise as entered; matching against a user's IdP
// groups is case-insensitive, and (group_id, LOWER(sso_group)) is unique
// (expression index created in database.runMigrations). Links are removed
// with their group (ON DELETE CASCADE).
type GroupSSOLink struct {
	ID        uuid.UUID `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	GroupID   uuid.UUID `gorm:"type:uuid;not null;index" json:"group_id"`
	SSOGroup  string    `gorm:"column:sso_group;size:256;not null" json:"sso_group"`
	CreatedAt time.Time `json:"created_at"`

	Group *Group `gorm:"foreignKey:GroupID;constraint:OnDelete:CASCADE" json:"-"`
}

func (l *GroupSSOLink) BeforeCreate(tx *gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}

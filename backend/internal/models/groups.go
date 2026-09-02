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
}

func (g *Group) BeforeCreate(tx *gorm.DB) error {
	if g.ID == uuid.Nil {
		g.ID = uuid.New()
	}
	return nil
}

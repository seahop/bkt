package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UploadStatus represents the status of an ongoing upload
type UploadStatus string

const (
	UploadStatusPending    UploadStatus = "pending"
	UploadStatusProcessing UploadStatus = "processing"
	UploadStatusCompleted  UploadStatus = "completed"
	UploadStatusFailed     UploadStatus = "failed"
)

// Upload represents an asynchronous file upload
type Upload struct {
	ID           uuid.UUID    `gorm:"type:uuid;primary_key;default:gen_random_uuid()" json:"id"`
	UserID       uuid.UUID    `gorm:"type:uuid;not null;index" json:"user_id"`
	BucketName   string       `gorm:"not null" json:"bucket_name"`
	ObjectKey    string       `gorm:"not null" json:"object_key"`
	Filename     string       `gorm:"not null" json:"filename"`
	ContentType  string       `json:"content_type"`
	TotalSize    int64        `gorm:"not null" json:"total_size"`
	UploadedSize int64        `gorm:"default:0" json:"uploaded_size"`
	Status       UploadStatus `gorm:"type:text;not null;index" json:"status"`
	ErrorMessage string       `json:"error_message,omitempty"`
	ObjectID     *uuid.UUID   `gorm:"type:uuid" json:"object_id,omitempty"` // Set when upload completes
	CreatedAt    time.Time    `gorm:"index" json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	CompletedAt  *time.Time   `json:"completed_at,omitempty"`

	// Relationships
	User User `gorm:"foreignKey:UserID" json:"user,omitempty"`
}

func (u *Upload) BeforeCreate(tx *gorm.DB) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	if u.Status == "" {
		u.Status = UploadStatusPending
	}
	return nil
}

// UploadStatusResponse is the response for upload status queries
type UploadStatusResponse struct {
	ID            uuid.UUID    `json:"id"`
	Status        UploadStatus `json:"status"`
	Filename      string       `json:"filename"`
	ObjectKey     string       `json:"object_key"`
	TotalSize     int64        `json:"total_size"`
	UploadedSize  int64        `json:"uploaded_size"`
	ProgressPct   float64      `json:"progress_percent"`
	ErrorMessage  string       `json:"error_message,omitempty"`
	ObjectID      *uuid.UUID   `json:"object_id,omitempty"`
	CreatedAt     time.Time    `json:"created_at"`
	CompletedAt   *time.Time   `json:"completed_at,omitempty"`
	EstimatedTime *string      `json:"estimated_time_remaining,omitempty"` // e.g., "2m 30s"
}

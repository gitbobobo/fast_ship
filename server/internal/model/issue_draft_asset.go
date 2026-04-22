package model

import "time"

type IssueDraftAsset struct {
	ID              string    `gorm:"type:text;primaryKey" json:"id"`
	ProjectID       string    `gorm:"type:text;not null;index" json:"project_id"`
	FileName        string    `gorm:"type:text;not null" json:"file_name"`
	FilePath        string    `gorm:"type:text;not null" json:"-"`
	MimeType        string    `gorm:"type:text;not null" json:"mime_type"`
	FileSize        int64     `gorm:"not null" json:"file_size"`
	CreatedByUserID string    `gorm:"type:text;not null;index" json:"created_by_user_id"`
	CreatedAt       time.Time `gorm:"not null;index" json:"created_at"`

	Project Project `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueDraftAsset) TableName() string {
	return "issue_draft_assets"
}

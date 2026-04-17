package model

import "time"

type IssueAssetStatus string

const (
	IssueAssetStatusPending  IssueAssetStatus = "pending"
	IssueAssetStatusAttached IssueAssetStatus = "attached"
)

type IssueAsset struct {
	ID              string           `gorm:"type:text;primaryKey" json:"id"`
	IssueID         string           `gorm:"type:text;not null;index" json:"issue_id"`
	FileName        string           `gorm:"type:text;not null" json:"file_name"`
	FilePath        string           `gorm:"type:text;not null" json:"-"`
	MimeType        string           `gorm:"type:text;not null" json:"mime_type"`
	FileSize        int64            `gorm:"not null" json:"file_size"`
	Status          IssueAssetStatus `gorm:"type:text;not null;default:pending;index" json:"status"`
	CreatedByUserID string           `gorm:"type:text;not null;index" json:"created_by_user_id"`
	CreatedAt       time.Time        `gorm:"not null;index" json:"created_at"`

	Issue Issue `gorm:"foreignKey:IssueID;constraint:OnDelete:CASCADE" json:"-"`
}

func (IssueAsset) TableName() string {
	return "issue_assets"
}

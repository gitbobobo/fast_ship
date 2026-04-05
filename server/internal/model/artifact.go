package model

import "time"

type Artifact struct {
	ID         string    `gorm:"type:text;primaryKey" json:"id"`
	VersionID  string    `gorm:"type:text;not null;index" json:"version_id"`
	FileName   string    `gorm:"type:text;not null" json:"file_name"`
	FileSize   int64     `gorm:"not null" json:"file_size"`
	FilePath   string    `gorm:"type:text;not null" json:"-"`
	Platform   string    `gorm:"type:text" json:"platform"`
	UploadedAt time.Time `gorm:"not null" json:"uploaded_at"`

	Version Version `gorm:"foreignKey:VersionID;constraint:OnDelete:CASCADE" json:"-"`
}

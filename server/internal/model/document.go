package model

import "time"

type Document struct {
	ID        string    `gorm:"type:text;primaryKey" json:"id"`
	ProjectID string    `gorm:"type:text;not null" json:"project_id"`
	ParentID  *string   `gorm:"type:text" json:"parent_id"`
	Title     string    `gorm:"type:text;not null" json:"title"`
	Body      string    `gorm:"type:text;not null;default:''" json:"body"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	Project Project   `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
	Parent  *Document `gorm:"foreignKey:ParentID;constraint:OnDelete:CASCADE" json:"-"`
}

func (Document) TableName() string {
	return "documents"
}

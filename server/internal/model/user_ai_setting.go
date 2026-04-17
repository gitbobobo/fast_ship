package model

import "time"

type UserAISetting struct {
	UserID          string    `gorm:"type:text;primaryKey" json:"user_id"`
	APIHost         string    `gorm:"type:text;not null" json:"api_host"`
	Model           string    `gorm:"type:text;not null" json:"model"`
	APIKeyEncrypted []byte    `gorm:"type:blob;not null" json:"-"`
	CreatedAt       time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (UserAISetting) TableName() string {
	return "user_ai_settings"
}

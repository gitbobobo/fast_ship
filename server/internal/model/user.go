package model

import "time"

type User struct {
	ID           string    `gorm:"type:text;primaryKey" json:"id"`
	Username     string    `gorm:"type:text;not null;uniqueIndex" json:"username"`
	Email        string    `gorm:"type:text;not null;uniqueIndex" json:"email"`
	PasswordHash string    `gorm:"type:text;not null" json:"-"`
	AvatarURL    string    `gorm:"type:text" json:"avatar_url"`
	CreatedAt    time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time `gorm:"not null" json:"updated_at"`
}

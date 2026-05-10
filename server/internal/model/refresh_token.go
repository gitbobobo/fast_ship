package model

import "time"

type RefreshToken struct {
	ID        string     `gorm:"type:text;primaryKey" json:"id"`
	UserID    string     `gorm:"type:text;not null;index" json:"user_id"`
	TokenHash string     `gorm:"type:text;not null;index" json:"token_hash"`
	ExpiresAt time.Time  `gorm:"not null" json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
	CreatedAt time.Time  `gorm:"not null" json:"created_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

package model

import "time"

type ApiKey struct {
	ID         string     `gorm:"type:text;primaryKey" json:"id"`
	UserID     string     `gorm:"type:text;not null;index" json:"user_id"`
	Name       string     `gorm:"type:text;not null" json:"name"`
	KeyPrefix  string     `gorm:"type:text;not null" json:"key_prefix"`
	KeyHash    string     `gorm:"type:text;not null;index" json:"key_hash"`
	LastUsedAt *time.Time `json:"last_used_at"`
	CreatedAt  time.Time  `gorm:"not null" json:"created_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

package model

import "time"

type JWTBlacklist struct {
	JTI       string    `gorm:"type:text;primaryKey"`
	ExpiredAt time.Time `gorm:"not null"`
}

func (JWTBlacklist) TableName() string {
	return "jwt_blacklist"
}

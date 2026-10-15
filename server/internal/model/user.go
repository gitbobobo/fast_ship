package model

import "time"

type User struct {
	ID           string `gorm:"type:text;primaryKey" json:"id"`
	Username     string `gorm:"type:text;not null;uniqueIndex" json:"username"`
	Email        string `gorm:"type:text;not null;uniqueIndex" json:"email"`
	PasswordHash string `gorm:"type:text;not null" json:"-"`
	AvatarURL    string `gorm:"type:text" json:"avatar_url"`
	// TokenVersion 是用户级令牌版本，签发进 JWT 的 ver claim；改密递增后，
	// 此前签发的 access token 在中间件校验时立即失效。
	TokenVersion int       `gorm:"not null;default:0" json:"-"`
	CreatedAt    time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time `gorm:"not null" json:"updated_at"`
}

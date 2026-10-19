package model

import "time"

type User struct {
	ID           string `gorm:"type:text;primaryKey" json:"id"`
	Username     string `gorm:"type:text;not null;uniqueIndex" json:"username"`
	Email        string `gorm:"type:text;not null;uniqueIndex" json:"email"`
	PasswordHash string `gorm:"type:text;not null" json:"-"`
	AvatarURL    string `gorm:"type:text" json:"avatar_url"`
	// Role 用户角色（INT-129）：admin 可管理成员、邀请与部署设置；member 只能
	// 使用自己的资源。每套部署首个注册用户自动成为 admin，之后注册一律凭邀请、
	// 固定 member。角色只存库，由中间件每请求查库判定，不写进 JWT。
	Role string `gorm:"type:text;not null;default:'member'" json:"role"`
	// TokenVersion 是用户级令牌版本，签发进 JWT 的 ver claim；改密递增后，
	// 此前签发的 access token 在中间件校验时立即失效。
	TokenVersion int       `gorm:"not null;default:0" json:"-"`
	CreatedAt    time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt    time.Time `gorm:"not null" json:"updated_at"`
}

// 用户角色取值，与 api.UserRole 一一对应。
const (
	UserRoleAdmin  = "admin"
	UserRoleMember = "member"
)

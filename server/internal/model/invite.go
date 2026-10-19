package model

import "time"

// Invite 是一张一次性注册邀请（INT-129）：公开注册关闭后，新用户只能凭
// 管理员签发的邀请链接注册。库里只存 token 的 SHA-256，明文只在创建响应里
// 出现一次；一次注册即消费（used_by/used_at），撤销与过期后不可再用。
type Invite struct {
	ID string `gorm:"type:text;primaryKey" json:"id"`
	// TokenHash sha256(明文 token)，uniqueIndex 支持按链接反查。
	TokenHash string     `gorm:"type:text;not null;uniqueIndex" json:"-"`
	CreatedBy string     `gorm:"type:text;not null" json:"created_by"`
	ExpiresAt time.Time  `gorm:"not null" json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at"`
	// UsedBy 使用该邀请注册的用户 ID；空串表示尚未使用（API 投影为 null）。
	// 不声明外键：邀请是历史凭证记录，使用者被删也不影响审计展示。
	UsedBy    string     `gorm:"type:text" json:"used_by"`
	UsedAt    *time.Time `json:"used_at"`
	CreatedAt time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time  `gorm:"not null" json:"updated_at"`

	// Creator belongs-to 侧声明外键（users，级联删除），见 docs/internal/database.md。
	Creator User `gorm:"foreignKey:CreatedBy;constraint:OnDelete:CASCADE" json:"-"`
}

func (Invite) TableName() string {
	return "invites"
}

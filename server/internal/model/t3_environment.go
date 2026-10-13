package model

import "time"

// T3Environment 记录一个已配对的 T3 环境连接。
//
// Address 是从配对链接解析出的 T3 服务端基址（规范化 origin，如 https://host）；
// BearerTokenEncrypted 是配对 token 换来的 orchestration:read 访问令牌，
// 加密方式与 AI Key 一致（AES-256-GCM，见 pkg/crypto）。
// 配对 token 是一次性凭证，换到 Bearer 后即被消费，不落库；
// Bearer 过期或被吊销后环境进入 repair 状态，需要用户重新配对。
type T3Environment struct {
	ID                   string    `gorm:"type:text;primaryKey" json:"id"`
	UserID               string    `gorm:"type:text;not null;index" json:"user_id"`
	Name                 string    `gorm:"type:text;not null" json:"name"`
	Address              string    `gorm:"type:text;not null" json:"address"`
	BearerTokenEncrypted []byte    `gorm:"type:blob;not null" json:"-"`
	BearerExpiresAt      time.Time `gorm:"not null" json:"-"`
	BearerScope          string    `gorm:"type:text;not null" json:"-"`
	CreatedAt            time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt            time.Time `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (T3Environment) TableName() string {
	return "t3_environments"
}

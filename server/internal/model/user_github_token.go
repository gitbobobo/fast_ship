package model

import "time"

// UserGitHubToken 是用户级 GitHub Token 库的一行：密文 + 明文指纹 + 检测结果。
// 项目不再各自存密文，只保存对这里记录的引用（public/code 两个用途）。
// token_fingerprint 是明文的 SHA-256 hex——AES-GCM 密文每次都不同，绝不能按密文去重；
// 同一用户内指纹唯一，重复添加完全相同的 Token 明文时复用已有记录。
type UserGitHubToken struct {
	ID               string     `gorm:"type:text;primaryKey" json:"id"`
	UserID           string     `gorm:"type:text;not null;index;uniqueIndex:idx_user_github_tokens_user_fp,priority:1" json:"user_id"`
	Title            string     `gorm:"type:text;not null" json:"title"`
	TokenEncrypted   []byte     `gorm:"type:blob" json:"-"`
	TokenFingerprint string     `gorm:"type:text;not null;uniqueIndex:idx_user_github_tokens_user_fp,priority:2" json:"-"`
	TokenPrefix      string     `gorm:"type:text;not null" json:"token_prefix"`
	DetectionStatus  string     `gorm:"type:text;not null" json:"detection_status"`
	DetectionType    string     `gorm:"type:text;not null" json:"detection_token_type"`
	DetectionScopes  string     `gorm:"type:text;not null" json:"detection_scopes"`
	DetectionLogin   string     `gorm:"type:text;not null" json:"detection_login"`
	DetectionDetail  string     `gorm:"type:text;not null" json:"detection_detail"`
	DetectedAt       *time.Time `json:"detected_at"`
	CreatedAt        time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (UserGitHubToken) TableName() string {
	return "user_github_tokens"
}

// 检测状态与 Token 类型取值。检测状态区分「未检测 / 检测失败 / 凭证无效 / 认证成功」：
// 检测失败（网络、GitHub 5xx）不是凭证无效，网络故障不能把好 Token 判死。
const (
	TokenDetectionUnchecked = "unchecked"
	TokenDetectionOK        = "ok"
	TokenDetectionInvalid   = "invalid"
	TokenDetectionFailed    = "failed"
)

const (
	TokenTypeClassic     = "classic"
	TokenTypeFineGrained = "fine_grained"
	TokenTypeUnknown     = "unknown"
)

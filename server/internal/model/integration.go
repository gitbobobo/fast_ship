package model

import "time"

// Integration 是一个外部服务连接的用户级配置（INT-119）：一个用户一个 provider
// 一行，(user_id, provider) 唯一。webhook 入站按 App ID 头定位到用户，PR 评审
// 与 agent 端点按运行所属用户取身份，用户之间互不可见、互不覆盖。
// ConfigEncrypted 是整组连接参数（含 App 私钥、webhook 密钥）的 AES-256-GCM
// 密文；明文永不落库、不进日志。用户自己的行存在时永远优先于 config.yaml 兜底。
type Integration struct {
	ID string `gorm:"type:text;primaryKey" json:"id"`
	// UserID 配置归属用户；deleted 用户级联清空集成配置。
	UserID string `gorm:"type:text;not null;index;uniqueIndex:idx_integrations_user_provider,priority:1" json:"-"`
	// Provider 外部服务标识，当前只有 github_app；与 user_id 组成复合唯一，
	// 与 app_id 组成全库唯一（webhook 验签候选按行验签，重复 App ID 行会让
	// 创建顺序靠后者永远收不到投递）。
	Provider string `gorm:"type:text;not null;uniqueIndex:idx_integrations_user_provider,priority:2;uniqueIndex:idx_integrations_provider_app_id,priority:1" json:"provider"`
	// AppID 明文列：App ID 不是密钥，webhook 验签按它定位用户行，页面也用它
	// 展示。同 provider 下全库唯一（保存路径另有应用层 409）；与 config 同名
	// 不受限——config 不是数据库行，用户收编 config 身份是正常迁移路径。
	AppID           string     `gorm:"type:text;not null;index;uniqueIndex:idx_integrations_provider_app_id,priority:2" json:"app_id"`
	ConfigEncrypted []byte     `gorm:"type:blob;not null" json:"-"`
	DetectionStatus string     `gorm:"type:text;not null" json:"detection_status"`
	DetectionDetail string     `gorm:"type:text;not null" json:"detection_detail"`
	DetectedAt      *time.Time `json:"detected_at"`
	CreatedAt       time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"not null" json:"updated_at"`

	// User belongs-to 侧声明外键（users，级联删除），见 docs/internal/database.md。
	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (Integration) TableName() string {
	return "integrations"
}

// IntegrationProviderGitHubApp 当前唯一接入的外部服务。
const IntegrationProviderGitHubApp = "github_app"

// 检测状态取值，与 api.GitHubAppIntegrationDetectionStatus 一一对应；
// unchecked 表示从未检测，或刚保存还没跑完这一次检测。
const (
	IntegrationDetectionUnchecked = "unchecked"
	IntegrationDetectionOK        = "ok"
	IntegrationDetectionInvalid   = "invalid"
	IntegrationDetectionFailed    = "failed"
)

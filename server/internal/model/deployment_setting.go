package model

import "time"

// DeploymentSettingsID 单行表（每套部署一份设置）的固定主键。
const DeploymentSettingsID = "default"

// DeploymentSettings 是部署级设置（INT-129）：每套部署一行、主键固定为
// DeploymentSettingsID。A 轮只读写 public_url；App 身份四列（app_id/app_slug/
// config_encrypted/detection_*）随表一起建好，供 B 轮部署级 GitHub App 直接
// 使用，避免二次迁移。config_encrypted 与 integrations 表同形：整组密文的
// AES-256-GCM，明文永不落库。
type DeploymentSettings struct {
	ID string `gorm:"type:text;primaryKey" json:"id"`
	// PublicURL 对外访问基址；空串表示未在网页配置，读取侧回落 config.yaml。
	PublicURL string `gorm:"type:text;not null;default:''" json:"public_url"`

	AppID           string     `gorm:"type:text;not null;default:''" json:"-"`
	AppSlug         string     `gorm:"type:text;not null;default:''" json:"-"`
	ConfigEncrypted []byte     `gorm:"type:blob" json:"-"`
	DetectionStatus string     `gorm:"type:text;not null;default:'unchecked'" json:"-"`
	DetectionDetail string     `gorm:"type:text;not null;default:''" json:"-"`
	DetectedAt      *time.Time `json:"-"`
	CreatedAt       time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt       time.Time  `gorm:"not null" json:"updated_at"`
}

func (DeploymentSettings) TableName() string {
	return "deployment_settings"
}

// 检测状态取值（B 轮 GitHub App 连接检测使用），与 integrations 表同名语义。
const (
	DeploymentDetectionUnchecked = "unchecked"
	DeploymentDetectionOK        = "ok"
	DeploymentDetectionInvalid   = "invalid"
	DeploymentDetectionFailed    = "failed"
)

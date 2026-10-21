package model

import "time"

// AIProvider 是按用户隔离的 AI 提供商配置（INT-133）：一份地址 + 密钥可被
// 多个机器人引用，轮换密钥后所有引用它的机器人在下一个 run 生效——密钥只在
// provider 行上存一份，不复制进 bots。CatalogID 是 models.dev 的提供商 id，
// "custom" 表示自定义（base_url 由用户提供）。密钥全链路脱敏：任何响应与
// 日志只允许出现 APIKeyLast4。
type AIProvider struct {
	ID        string `gorm:"type:text;primaryKey" json:"id"`
	UserID    string `gorm:"type:text;not null" json:"user_id"`
	CatalogID string `gorm:"type:text;not null" json:"catalog_id"`
	Name      string `gorm:"type:text;not null" json:"name"`
	// BaseURL 规范化存储：TrimSpace + 去尾部 /。目录提供商由目录决定，
	// custom 由用户提供。
	BaseURL string `gorm:"type:text;not null" json:"base_url"`
	// APIKeyEncrypted 密文（AES-GCM）；禁止出现在任何 API 投影（json:"-"）。
	APIKeyEncrypted []byte `gorm:"type:blob;not null" json:"-"`
	APIKeyLast4     string `gorm:"type:text;not null" json:"api_key_last4"`

	// CircuitOpenUntil / CircuitReason 是定时任务熔断标记（INT-137）：
	// schedule run 因认证失效或额度用完失败时打开，CircuitOpenUntil 晚于当前
	// 即熔断中——该提供商的定时任务到点直接记 skipped，不再发起模型请求。
	// 窗口过后自动恢复；用户更新密钥/地址或测试连接成功也会清除。内部调度
	// 状态，不进任何 API 投影（json:"-"）。
	CircuitOpenUntil *time.Time `json:"-"`
	CircuitReason    string     `gorm:"type:text;not null;default:''" json:"-"`
	// ConfigVersionAt 是「当前凭证/地址配置」的生效时刻（INT-137）：创建、
	// 换 key/地址、以及测试连接确认现有密钥可用时写入。迟到的失败只在
	// 运行开始时配置已生效（ConfigVersionAt <= run.started_at）才允许熔断，
	// 防止用户修好后被旧凭证的迟到失败重新熔断。内部调度状态，不进投影。
	ConfigVersionAt *time.Time `json:"-"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (AIProvider) TableName() string {
	return "ai_providers"
}

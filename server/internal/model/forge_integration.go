package model

import "time"

// 内置 github-default 集成的固定 ID：只读映射到部署级 GitHub App 配置
// （deployment_settings / config.yaml 链路），不在本表存储。
const ForgeIntegrationBuiltinGitHubID = "github-default"

// 集成种类：github=内置 GitHub App（不在表内），forgejo=机器人账号 + 仓库
// 范围 PAT 的 Forgejo/Gitea 实例。
const (
	ForgeIntegrationKindGitHub  = "github"
	ForgeIntegrationKindForgejo = "forgejo"
)

// ForgeIntegration 是部署级代码仓集成（INT-166）：token 与 webhook_secret 各自
// 整体 AES-256-GCM 加密（密钥 cfg.Encryption.Key，与 deployment_settings 同一
// 模式），明文永不落库、不进日志、不进响应。allow_private_network 只影响本
// 集成 client 的 SSRF 判定（详见 pkg/forges 与 docs/internal/forge-integrations.md）。
type ForgeIntegration struct {
	ID      string `gorm:"type:text;primaryKey" json:"id"`
	Kind    string `gorm:"type:text;not null" json:"kind"`
	Name    string `gorm:"type:text;not null" json:"name"`
	BaseURL string `gorm:"type:text;not null" json:"base_url"`
	// APIBaseURL 存「意图」而非展开值：空串 = 按 {base_url}/api/v1 推导
	// （读取侧由 forgeAPIBaseURL 展开，改 base_url 自动跟随），非空 = 管理
	// 员显式钉死的端点。列保留可空语义，历史行为 NULL 时与空串同义。
	APIBaseURL string `gorm:"column:api_base_url;type:text" json:"api_base_url"`
	// HostKey 是 base_url 的归一键（forges.HostKey：小写 host + scheme 缺省
	// 端口），同归一主机的部分唯一索引（idx_forge_integrations_host_key）靠它
	// 兜应用层查重的并发窗口。保存/回填时按 base_url 现算落库；解析不出主机键
	// 的脏行为空串（部分唯一索引排除空串，不挡启动）。内置 github 行不落本表。
	HostKey string `gorm:"column:host_key;type:text" json:"-"`
	// TokenEncrypted / WebhookSecretEncrypted 各自独立加密：PAT 与验签密钥
	// 生命周期不同，分开存让「只换 token」不触碰 webhook_secret 密文。
	TokenEncrypted         []byte `gorm:"type:blob" json:"-"`
	WebhookSecretEncrypted []byte `gorm:"type:blob" json:"-"`
	// CACertPEM 自定义 CA（PEM），可空；空串/NULL = 仅系统根证书池。
	CACertPEM string `gorm:"type:text" json:"-"`
	// AllowPrivateNetwork 放行本集成配置主机解析到私网地址（http 同样只在
	// 开启时可用）；关闭时 client 按公网校验全部解析结果。
	AllowPrivateNetwork bool      `gorm:"not null;default:false" json:"allow_private_network"`
	CreatedAt           time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt           time.Time `gorm:"not null" json:"updated_at"`
}

func (ForgeIntegration) TableName() string {
	return "forge_integrations"
}

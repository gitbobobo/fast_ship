package model

import "time"

// BotToolCall 是同步、异步工具共用的执行账本，确认状态也放在这里。
// ID 同时是平台调用身份与执行幂等键；provider_tool_call_id 只负责模型协议配对。
// 调用记录先落库再执行：CAS 认领 queued→running，终态也用条件 UPDATE，
// 成功完成、取消、恢复三者只有一个能落定。
type BotToolCall struct {
	ID        string `gorm:"type:text;primaryKey" json:"id"`
	BotID     string `gorm:"type:text;not null" json:"bot_id"`
	RequestID string `gorm:"type:text;not null" json:"request_id"`

	ProviderToolCallID string `gorm:"type:text;not null" json:"provider_tool_call_id"`
	Ordinal            int64  `gorm:"not null" json:"ordinal"`

	ToolName string `gorm:"type:text;not null" json:"tool_name"`
	Action   string `gorm:"type:text;not null" json:"action"`
	// ArgumentsJSON 是规范化后的参数；ArgumentsHash 用于授权规则与审计比对。
	ArgumentsJSON string `gorm:"type:text;not null" json:"arguments"`
	ArgumentsHash string `gorm:"type:text;not null" json:"arguments_hash"`

	ExecutionMode        string `gorm:"type:text;not null" json:"execution_mode"` // sync / async
	RequiresConfirmation bool   `gorm:"not null" json:"requires_confirmation"`

	Status BotToolCallStatus `gorm:"type:text;not null" json:"status"`

	// Decision 是用户的确认决定；可空，与 DecidedAt 一起落。
	Decision  *string    `gorm:"type:text" json:"decision,omitempty"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`

	AuthorizationID *string `gorm:"type:text" json:"authorization_id,omitempty"`

	// ExecutionContextJSON 是服务端生成的执行身份（T3 command/thread/message ID
	// 等），不让模型指定幂等身份。
	ExecutionContextJSON string `gorm:"type:text;not null" json:"execution_context"`

	// PolicyJSON 是写操作分级放行（INT-160）的服务端审计：tier/rule_id/auto 与
	// 受理时算出的前置条件事实。可空——历史行为空即「策略上线前」，不做回填。
	PolicyJSON string `gorm:"type:text;not null;default:''" json:"-"`

	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`

	// ResultJSON 是瘦身后的完整结果；ResultPreviewJSON 是进入模型上下文的受限
	// 结果。Truncated 时模型可用 fs_result.read 按 Unicode 字符续读。
	ResultJSON        *string `gorm:"type:text" json:"-"`
	ResultPreviewJSON *string `gorm:"type:text" json:"result_preview,omitempty"`
	ResultChars       int64   `gorm:"not null" json:"result_chars"`
	// ResultItems 是结果实际返回条数：列表记条数、普通对象记 1、空结果记 0
	//（不是分页总命中数）；折叠引用与压缩统计使用。INT-102 前的存量数据在
	// 首次折叠时按 ResultJSON 补算。
	ResultItems int64 `gorm:"not null;default:0" json:"result_items"`
	Truncated   bool  `gorm:"not null" json:"truncated"`

	ErrorCode    *string `json:"error_code,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`
	// EffectMayHaveOccurred 表示副作用可能已发生：取消、崩溃或取消竞速下
	// 不声称回滚，模型应先读取核实再决定新操作。
	EffectMayHaveOccurred bool `gorm:"not null" json:"effect_may_have_occurred"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`

	Bot           Bot               `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
	Request       BotModelRequest   `gorm:"foreignKey:RequestID;constraint:OnDelete:CASCADE" json:"-"`
	Authorization *BotAuthorization `gorm:"foreignKey:AuthorizationID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotToolCall) TableName() string {
	return "bot_tool_calls"
}

type BotToolCallStatus string

const (
	BotCallStatusPendingConfirmation BotToolCallStatus = "pending_confirmation"
	BotCallStatusQueued              BotToolCallStatus = "queued"
	BotCallStatusRunning             BotToolCallStatus = "running"
	BotCallStatusSucceeded           BotToolCallStatus = "succeeded"
	BotCallStatusFailed              BotToolCallStatus = "failed"
	BotCallStatusRejected            BotToolCallStatus = "rejected"
	BotCallStatusCancelled           BotToolCallStatus = "cancelled"
	BotCallStatusNotExecuted         BotToolCallStatus = "not_executed"
)

// IsTerminal 报告调用是否已到终态；终态只能落定一次。
func (s BotToolCallStatus) IsTerminal() bool {
	switch s {
	case BotCallStatusSucceeded, BotCallStatusFailed, BotCallStatusRejected,
		BotCallStatusCancelled, BotCallStatusNotExecuted:
		return true
	}
	return false
}

type BotToolExecutionMode string

const (
	BotExecutionSync  BotToolExecutionMode = "sync"
	BotExecutionAsync BotToolExecutionMode = "async"
)

type BotCallDecision string

const (
	BotDecisionApprove     BotCallDecision = "approve"
	BotDecisionReject      BotCallDecision = "reject"
	BotDecisionAlwaysAllow BotCallDecision = "always_allow"
)

// BotAuthorization 是结构化的「始终允许」规则；撤销只写 RevokedAt，保留审计
// 关联。活跃授权按 (bot_id, rule_hash) 部分唯一。
type BotAuthorization struct {
	ID       string `gorm:"type:text;primaryKey" json:"id"`
	BotID    string `gorm:"type:text;not null" json:"bot_id"`
	ToolName string `gorm:"type:text;not null" json:"tool_name"`
	Action   string `gorm:"type:text;not null" json:"action"`

	// ScopeKind/ScopeID/EnvironmentID 界定授权范围：项目更新指定项目、Issue 写
	// 指定 Fast Ship 项目、线程操作指定环境（与线程）、项目创建限定当前用户集合。
	ScopeKind     string  `gorm:"type:text;not null" json:"scope_kind"`
	ScopeID       string  `gorm:"type:text;not null" json:"scope_id"`
	EnvironmentID *string `gorm:"type:text" json:"environment_id,omitempty"`

	// ConstraintsJSON 是规范化约束（如允许写入的字段集合）；规则整体哈希为
	// RuleHash，用于活跃唯一与快速匹配。ExpiresAt 自 INT-160 起为新建授权的
	// 必填值（默认 30 天）：永久、范围宽的「始终允许」是实测中最不安全的放宽
	// 形态；过期行由查询条件挡掉，不再命中。
	ConstraintsJSON string     `gorm:"type:text;not null" json:"constraints"`
	RuleHash        string     `gorm:"type:text;not null" json:"rule_hash"`
	CreatedAt       time.Time  `gorm:"not null" json:"created_at"`
	ExpiresAt       *time.Time `json:"expires_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`

	Bot Bot `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotAuthorization) TableName() string {
	return "bot_authorizations"
}

// BotWritePolicyRule 是分级放行（INT-160）每条规则的独立开关与误判计数：
// 行不存在即启用（缺省启用，用户不必先建行才能放行）。同一 (bot, rule)
// 24 小时内累计 2 次误判即自动关闭（auto_disabled_at 落时戳），并给主线
// 追加一条生命周期消息说明「规则已自动降级回确认」。Enabled 不带 default
// 标签——GORM 的 Create 会把带 default 的零值字段替换成库默认值，显式写
// false 会被静默翻成 true。
type BotWritePolicyRule struct {
	BotID  string `gorm:"type:text;primaryKey" json:"bot_id"`
	RuleID string `gorm:"type:text;primaryKey" json:"rule_id"`

	Enabled bool `gorm:"not null" json:"enabled"`

	// FalsePositiveCount24h 是 24 小时窗口内的误判次数；LastFalsePositiveAt
	// 是窗口锚点——距离上次误判超过 24 小时的下一次误判把计数重置为 1。
	FalsePositiveCount24h int        `gorm:"not null" json:"false_positive_count_24h"`
	LastFalsePositiveAt   *time.Time `json:"last_false_positive_at,omitempty"`
	AutoDisabledAt        *time.Time `json:"auto_disabled_at,omitempty"`
	UpdatedAt             time.Time  `gorm:"not null" json:"updated_at"`
}

func (BotWritePolicyRule) TableName() string {
	return "bot_write_policy_rules"
}

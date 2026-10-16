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

	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty"`
	StartedAt         *time.Time `json:"started_at,omitempty"`
	FinishedAt        *time.Time `json:"finished_at,omitempty"`

	// ResultJSON 是瘦身后的完整结果；ResultPreviewJSON 是进入模型上下文的受限
	// 结果。Truncated 时模型可用 fs_result.read 按 Unicode 字符续读。
	ResultJSON        *string `gorm:"type:text" json:"-"`
	ResultPreviewJSON *string `gorm:"type:text" json:"result_preview,omitempty"`
	ResultChars       int64   `gorm:"not null" json:"result_chars"`
	Truncated         bool    `gorm:"not null" json:"truncated"`

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
	// RuleHash，用于活跃唯一与快速匹配。
	ConstraintsJSON string     `gorm:"type:text;not null" json:"constraints"`
	RuleHash        string     `gorm:"type:text;not null" json:"rule_hash"`
	CreatedAt       time.Time  `gorm:"not null" json:"created_at"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`

	Bot Bot `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotAuthorization) TableName() string {
	return "bot_authorizations"
}

package model

import "time"

// 机器人（INT-101）：用户自带 OpenAI 兼容 key 的智能体。一个 bot 就是一条
// 主对话线的身份；配置、对话账本、运行账本、调用账本与授权都挂在 bot 下。
//
// 通用约定：ID 为 UUID，时间一律 UTC。目标项目 / Issue / T3 线程等业务引用
// 保存在工具参数中，不建立会随目标删除历史账本的外键。

type Bot struct {
	ID            string `gorm:"type:text;primaryKey" json:"id"`
	UserID        string `gorm:"type:text;not null" json:"user_id"`
	Name          string `gorm:"type:text;not null" json:"name"`
	PersonaPrompt string `gorm:"type:text;not null" json:"persona_prompt"`
	// SystemPromptText 是已拼好的人设与平台规则全文，避免每次请求重新生成；
	// 人设变更时整体重建并递增 PromptRevision。
	SystemPromptText      string `gorm:"type:text;not null" json:"-"`
	PromptRevision        int64  `gorm:"not null" json:"prompt_revision"`
	PlatformPromptVersion int64  `gorm:"not null" json:"platform_prompt_version"`
	ToolsetVersion        int64  `gorm:"not null" json:"toolset_version"`

	BaseURL string `gorm:"type:text;not null" json:"base_url"`
	// Model 列存模型名；列名 model 不是 SQLite 保留字，GORM 会加引号。
	Model string `gorm:"column:model;type:text;not null" json:"model"`
	// APIKeyEncrypted 密文（AES-GCM）；任何响应与日志只允许出现 APIKeyLast4。
	APIKeyEncrypted []byte `gorm:"type:blob;not null" json:"-"`
	APIKeyLast4     string `gorm:"type:text;not null" json:"api_key_last4"`

	// NextMessageSeq / NextEventSeq 是两条账本的水位（下一条的 seq）；
	// 取号只允许在短事务内完成。
	NextMessageSeq int64 `gorm:"not null" json:"next_message_seq"`
	NextEventSeq   int64 `gorm:"not null" json:"next_event_seq"`
	// OfferedInputSeq 是已经交给某次模型请求的输入水位：run 失败后同一批输入
	// 不会无限自动重试。
	OfferedInputSeq int64 `gorm:"not null" json:"offered_input_seq"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (Bot) TableName() string {
	return "bots"
}

type BotRunStatus string

const (
	BotRunStatusQueued    BotRunStatus = "queued"
	BotRunStatusRunning   BotRunStatus = "running"
	BotRunStatusCompleted BotRunStatus = "completed"
	BotRunStatusFailed    BotRunStatus = "failed"
	BotRunStatusStopped   BotRunStatus = "stopped"
)

type BotRunTrigger string

const (
	BotTriggerUserMessage BotRunTrigger = "user_message"
	BotTriggerToolResult  BotRunTrigger = "tool_result"
	BotTriggerRecovery    BotRunTrigger = "recovery"
)

// BotRun 是一次主线执行。同一 bot 至多一个未结束 run（部分唯一索引兜底），
// 唯一 (bot_id, trigger_key) 让重复投递的触发不重复建 run。
type BotRun struct {
	ID            string        `gorm:"type:text;primaryKey" json:"id"`
	BotID         string        `gorm:"type:text;not null" json:"bot_id"`
	TriggerSource BotRunTrigger `gorm:"type:text;not null" json:"trigger_source"`
	TriggerKey    string        `gorm:"type:text;not null" json:"trigger_key"`
	// TriggerContextJSON 记录触发来源的细节（消息 seq、call_id 等）。
	TriggerContextJSON string       `gorm:"type:text;not null" json:"trigger_context_json"`
	Status             BotRunStatus `gorm:"type:text;not null" json:"status"`
	// ConfigSnapshotJSON 是本次 run 的模型 / 地址 / 提示词版本快照，不含明文 key。
	ConfigSnapshotJSON string `gorm:"type:text;not null" json:"config_snapshot"`
	// InputThroughSeq 是本 run 已消费的输入水位；新输入指 seq 大于它的
	// user / background_result 消息。
	InputThroughSeq  int64      `gorm:"not null" json:"input_through_seq"`
	StopRequestedAt  *time.Time `json:"stop_requested_at"`
	PromptTokens     *int64     `json:"prompt_tokens"`
	CompletionTokens *int64     `json:"completion_tokens"`
	TotalTokens      *int64     `json:"total_tokens"`
	CachedTokens     *int64     `json:"cached_tokens"`
	// UsageComplete 区分「服务没报 usage」与「usage 为零」。
	UsageComplete bool       `json:"usage_complete"`
	ErrorCode     *string    `json:"error_code"`
	ErrorMessage  *string    `json:"error_message"`
	CreatedAt     time.Time  `gorm:"not null" json:"created_at"`
	StartedAt     *time.Time `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at"`

	Bot Bot `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotRun) TableName() string {
	return "bot_runs"
}

type BotModelRequestStatus string

const (
	BotModelRequestRunning   BotModelRequestStatus = "running"
	BotModelRequestSucceeded BotModelRequestStatus = "succeeded"
	BotModelRequestFailed    BotModelRequestStatus = "failed"
)

// BotModelRequest 记录每次真正发往模型的请求：序号、输入水位、前缀指纹与
// token 计量。完整请求体不复制进库，测试用 transport 捕获实际字节。
type BotModelRequest struct {
	ID      string `gorm:"type:text;primaryKey" json:"id"`
	RunID   string `gorm:"type:text;not null" json:"run_id"`
	Ordinal int64  `gorm:"not null" json:"ordinal"`

	Status          BotModelRequestStatus `gorm:"type:text;not null" json:"status"`
	InputThroughSeq int64                 `gorm:"not null" json:"input_through_seq"`
	PromptRevision  int64                 `gorm:"not null" json:"prompt_revision"`
	ToolsetVersion  int64                 `gorm:"not null" json:"toolset_version"`

	// PrefixHash / ToolsHash / RequestHash 是不可变前缀（系统提示到最近消息）、
	// 工具定义数组与两者合并的 SHA-256，用于缓存命中与回归对比。
	PrefixHash  string `gorm:"type:text;not null" json:"prefix_hash"`
	ToolsHash   string `gorm:"type:text;not null" json:"tools_hash"`
	RequestHash string `gorm:"type:text;not null" json:"request_hash"`

	ToolsSchemaBytes    int64  `gorm:"not null" json:"tools_schema_bytes"`
	ToolsTokensEstimate int64  `gorm:"not null" json:"tools_tokens_estimate"`
	TokenEstimator      string `gorm:"type:text;not null" json:"token_estimator"`

	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
	CachedTokens     *int64 `json:"cached_tokens"`

	ProviderRequestID string     `gorm:"type:text;not null" json:"provider_request_id"`
	ErrorCode         *string    `json:"error_code"`
	ErrorMessage      *string    `json:"error_message"`
	StartedAt         time.Time  `gorm:"not null" json:"started_at"`
	FinishedAt        *time.Time `json:"finished_at"`

	Run BotRun `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotModelRequest) TableName() string {
	return "bot_model_requests"
}

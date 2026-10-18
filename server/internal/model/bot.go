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
	// ToolsetVersion 是该对话线最近激活的工具版本（压缩/重启边界才切换）；
	// LastToldVersion 是最后一次通过 lifecycle 事件告知过的版本水位，用于
	// 重启或长期不活跃后补发应告知的变更（INT-102）。
	ToolsetVersion  int64 `gorm:"not null" json:"toolset_version"`
	LastToldVersion int64 `gorm:"not null;default:1" json:"last_told_version"`

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
	// BotRunStatusSkipped 是定时任务专用终态：计划时间已错过超过宽限，按
	// 「跳过并记录」处理，不执行。
	BotRunStatusSkipped BotRunStatus = "skipped"
)

type BotRunTrigger string

const (
	BotTriggerUserMessage BotRunTrigger = "user_message"
	BotTriggerToolResult  BotRunTrigger = "tool_result"
	BotTriggerRecovery    BotRunTrigger = "recovery"
	BotTriggerSchedule    BotRunTrigger = "schedule"
)

// 会话判别：主线（main）与定时任务（schedule）共用 bot_runs/bot_messages
// 账本与 seq 水位，但读取面互相隔离。列默认 main，存量行无需回填。
const (
	BotSessionKindMain     = "main"
	BotSessionKindSchedule = "schedule"
)

// BotRun 是一次主线执行。同一 bot 至多一个未结束主线 run（部分唯一索引按
// session_kind='main' 兜底），唯一 (bot_id, trigger_key) 让重复投递的触发
// 不重复建 run；schedule 会话不受部分唯一约束，靠 trigger_key 幂等。
type BotRun struct {
	ID            string        `gorm:"type:text;primaryKey" json:"id"`
	BotID         string        `gorm:"type:text;not null" json:"bot_id"`
	TriggerSource BotRunTrigger `gorm:"type:text;not null" json:"trigger_source"`
	TriggerKey    string        `gorm:"type:text;not null" json:"trigger_key"`
	// TriggerContextJSON 记录触发来源的细节（消息 seq、call_id 等）。
	TriggerContextJSON string       `gorm:"type:text;not null" json:"trigger_context_json"`
	Status             BotRunStatus `gorm:"type:text;not null" json:"status"`
	// SessionKind 区分主线与定时任务会话；ScheduleTaskID / ScheduledFor 只
	// 在 schedule 会话有值（不建外键：任务删除后运行记录仍保留供排查）。
	SessionKind    string     `gorm:"type:text;not null;default:main" json:"session_kind"`
	ScheduleTaskID *string    `gorm:"type:text" json:"schedule_task_id,omitempty"`
	ScheduledFor   *time.Time `json:"scheduled_for,omitempty"`
	// Summary 是运行收尾时的结果摘要（截断到安全长度）；Silent 标记静默
	// 结果（[[SILENT]] 哨兵）。skipped run 的 Summary 记跳过原因。
	Summary string `gorm:"type:text;not null;default:''" json:"summary"`
	Silent  bool   `gorm:"not null;default:false" json:"silent"`
	// APIKeyEncryptedSnapshot 是派发时冻结的密钥密文（仅 schedule run 有值）。
	// 它与配置快照里的 base_url 成对使用：派发后用户同时换了地址与密钥时，
	// 不能把新密钥发往旧地址。禁止出现在任何 API 投影（json:"-"）。
	APIKeyEncryptedSnapshot []byte `gorm:"type:blob" json:"-"`
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
	// ToolsetVersion 记录本次请求实际使用的冻结工具版本（取自构建产物，
	// 不读可能过时的 bots.toolset_version）。
	ToolsetVersion int64 `gorm:"not null" json:"toolset_version"`
	// ContextGeneration 是请求构建时的投影状态代数：压缩发布 +1 后基线失效。
	ContextGeneration int64 `gorm:"not null;default:0" json:"context_generation"`

	// InputTokensEstimate 是发送前的上下文 token 估算值；InputEstimator 标注
	// 估算方式（usage_delta/3.5 或 utf8_bytes/3.5）。估算不写进实际 usage 列。
	InputTokensEstimate int64  `gorm:"not null;default:0" json:"input_tokens_estimate"`
	InputEstimator      string `gorm:"type:text;not null;default:''" json:"input_estimator"`

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

	ProviderRequestID string    `gorm:"type:text;not null" json:"provider_request_id"`
	ErrorCode         *string   `json:"error_code"`
	ErrorMessage      *string   `json:"error_message"`
	StartedAt         time.Time `gorm:"not null" json:"started_at"`
	// FirstDeltaAt 是首个非空正文流式片段的到达时间（INT-115 指标口径的
	// 起点）；上游未流出正文（纯工具调用/失败）时为 NULL。
	FirstDeltaAt *time.Time `json:"first_delta_at"`
	FinishedAt   *time.Time `json:"finished_at"`

	Run BotRun `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotModelRequest) TableName() string {
	return "bot_model_requests"
}

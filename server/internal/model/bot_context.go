package model

import "time"

// 上下文压缩（INT-102）：原始消息账本永不删改；压缩只改「持久投影状态」
// （bot_context_states），构造请求时用摘要替换更早历史，用折叠引用替换旧的大
// 工具结果。bot_compactions 是每个压缩周期的最终记录（成功、仅折叠、失败、
// 无进展都留记录）。

// BotContextState 是某机器人当前的持久投影状态；generation 每次发布 +1，
// 摘要水位不得后退（守护触发器拦截）。
type BotContextState struct {
	// BotID 是主键：每机器人一行。
	BotID string `gorm:"type:text;primaryKey" json:"bot_id"`
	// Generation 单调递增；压缩发布用 CAS（generation 旧值）提交。
	Generation int64 `gorm:"not null" json:"generation"`
	// SummaryThroughSeq 是摘要已覆盖的最大消息 seq；0 表示尚无摘要。
	SummaryThroughSeq int64  `gorm:"not null" json:"summary_through_seq"`
	SummaryText       string `gorm:"type:text;not null" json:"summary_text"`
	// IdentifierManifestJSON 是随摘要注入的确定性标识符附录原文。
	IdentifierManifestJSON string `gorm:"type:text;not null" json:"identifier_manifest_json"`
	// FoldedResultsJSON 是消息 ID → 折叠引用正文 的持久映射；构建投影按它
	// 确定性替换，不每次动态重折叠。
	FoldedResultsJSON string `gorm:"type:text;not null" json:"folded_results_json"`
	// LifecycleRenderVersion 记录「压缩发布后旧 lifecycle 消息改按 user 角色
	// 投影」的水位（INT-137 R6）：0 表示未发布过压缩——旧 toolset_updated 保持
	// system 角色，既有会话 prefix_hash 不变；任何一次发布（含 fold-only）后
	// 写 1，仍留在投影里的旧 toolset_updated 也按 user 角色发送（system 已不
	// 在开头，部分兼容后端会拒绝）。
	LifecycleRenderVersion int64 `gorm:"not null;default:0" json:"lifecycle_render_version"`

	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	Bot Bot `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotContextState) TableName() string {
	return "bot_context_states"
}

type BotCompactionStatus string

const (
	BotCompactionSucceeded BotCompactionStatus = "succeeded"
	BotCompactionFoldOnly  BotCompactionStatus = "fold_only"
	BotCompactionFailed    BotCompactionStatus = "failed"
	BotCompactionNoGain    BotCompactionStatus = "no_progress"
)

type BotCompactionReason string

const (
	BotCompactionThreshold BotCompactionReason = "threshold"
	BotCompactionForced    BotCompactionReason = "context_limit"
	// BotCompactionStyle 是「说话方式升级后的一次性压缩」（INT-133）：存量
	// 会话激活跨过 botStylePromptVersion 后，在下一个 run 边界强制压缩一次，
	// 把旧风格的长回复收进摘要，避免模型继续模仿自己的历史回复。
	BotCompactionStyle BotCompactionReason = "style_upgrade"
)

// BotCompaction 是一个压缩周期的最终记录；禁止 UPDATE（append-only）。
type BotCompaction struct {
	ID    string `gorm:"type:text;primaryKey" json:"id"`
	BotID string `gorm:"type:text;not null" json:"bot_id"`
	// MaintenanceRunID 唯一：一个维护运行至多一条最终压缩记录。
	MaintenanceRunID string  `gorm:"type:text;not null" json:"maintenance_run_id"`
	ParentRunID      *string `gorm:"type:text" json:"parent_run_id,omitempty"`

	Reason BotCompactionReason `gorm:"type:text;not null" json:"reason"`

	// FromSeq / ThroughSeq 是本次摘要覆盖的半开区间 (from_seq, through_seq]。
	FromSeq    int64 `gorm:"not null" json:"from_seq"`
	ThroughSeq int64 `gorm:"not null" json:"through_seq"`

	BeforeGeneration int64 `gorm:"not null" json:"before_generation"`
	AfterGeneration  int64 `gorm:"not null" json:"after_generation"`

	Status                 BotCompactionStatus `gorm:"type:text;not null" json:"status"`
	SummaryText            string              `gorm:"type:text;not null" json:"summary_text"`
	IdentifierManifestJSON string              `gorm:"type:text;not null" json:"identifier_manifest_json"`
	FoldedResultsJSON      string              `gorm:"type:text;not null" json:"folded_results_json"`

	BeforeTokensEstimate int64  `gorm:"not null" json:"before_tokens_estimate"`
	AfterTokensEstimate  int64  `gorm:"not null" json:"after_tokens_estimate"`
	Estimator            string `gorm:"type:text;not null" json:"estimator"`

	ErrorCode    *string   `json:"error_code,omitempty"`
	ErrorMessage *string   `json:"error_message,omitempty"`
	CreatedAt    time.Time `gorm:"not null" json:"created_at"`

	Bot            Bot               `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
	MaintenanceRun BotMaintenanceRun `gorm:"foreignKey:MaintenanceRunID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotCompaction) TableName() string {
	return "bot_compactions"
}

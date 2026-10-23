package model

import "time"

// 机器人维护运行（INT-102）：压缩周期与夜间整理的执行账本。独立于主线
// bot_runs（部分唯一索引强制每机器人仅一个未结束主线 run，维护不占用）；
// 每次实际模型请求单独记 bot_maintenance_requests，不混入主线请求记录。

type BotMaintenanceKind string

const (
	BotMaintenanceCompaction BotMaintenanceKind = "compaction"
	BotMaintenanceNightly    BotMaintenanceKind = "nightly"
)

type BotMaintenanceStatus string

const (
	BotMaintenanceRunning   BotMaintenanceStatus = "running"
	BotMaintenanceSucceeded BotMaintenanceStatus = "succeeded"
	BotMaintenanceFailed    BotMaintenanceStatus = "failed"
)

type BotMaintenancePurpose string

const (
	BotPurposeSummary BotMaintenancePurpose = "summary"
	BotPurposeArchive BotMaintenancePurpose = "archive"
	BotPurposeNightly BotMaintenancePurpose = "nightly"
)

// BotMaintenanceRun 是一次维护执行的账本行。(bot_id, kind, trigger_key) 唯一：
// 压缩周期由机器人+旧 generation+固定 through_seq 派生键幂等；夜间整理
// trigger_key=nightly:<当地日期> 保证每天一次。
type BotMaintenanceRun struct {
	ID          string               `gorm:"type:text;primaryKey" json:"id"`
	BotID       string               `gorm:"type:text;not null" json:"bot_id"`
	Kind        BotMaintenanceKind   `gorm:"type:text;not null" json:"kind"`
	TriggerKey  string               `gorm:"type:text;not null" json:"trigger_key"`
	ParentRunID *string              `gorm:"type:text" json:"parent_run_id,omitempty"`
	Status      BotMaintenanceStatus `gorm:"type:text;not null" json:"status"`
	// ConfigSnapshotJSON 是冻结的维护配置（模型、限额、时区），运行期间不受
	// 配置编辑影响。
	ConfigSnapshotJSON string `gorm:"type:text;not null" json:"config_snapshot_json"`
	// InputSnapshotJSON 是执行输入的摘要快照（来源文件与版本等），供排查。
	InputSnapshotJSON string `gorm:"type:text;not null" json:"input_snapshot_json"`
	// ArchiveAttemptedAt 在发出归档请求前持久写入：同周期失败/重启也不再
	// 二次发起归档。
	ArchiveAttemptedAt *time.Time `json:"archive_attempted_at,omitempty"`
	ResultJSON         string     `gorm:"type:text;not null" json:"result_json"`

	ErrorCode    *string    `json:"error_code,omitempty"`
	ErrorMessage *string    `json:"error_message,omitempty"`
	CreatedAt    time.Time  `gorm:"not null" json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`

	Bot Bot `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotMaintenanceRun) TableName() string {
	return "bot_maintenance_runs"
}

// BotMaintenanceRequest 记录维护路径的每次实际模型请求（摘要 / 静默存档 /
// 夜间整理），与主线 bot_model_requests 分开记账。
type BotMaintenanceRequest struct {
	ID               string `gorm:"type:text;primaryKey" json:"id"`
	MaintenanceRunID string `gorm:"type:text;not null" json:"maintenance_run_id"`
	Ordinal          int64  `gorm:"not null" json:"ordinal"`

	Purpose BotMaintenancePurpose `gorm:"type:text;not null" json:"purpose"`
	// Model 是实际使用的模型（辅助模型回退主模型时记录实际值）。
	Model  string                `gorm:"column:model;type:text;not null" json:"model"`
	Status BotModelRequestStatus `gorm:"type:text;not null" json:"status"`

	// PrefixHash / ToolsHash 沿用主线口径（维护请求无业务工具时 tools_hash
	// 为空串）。
	PrefixHash string `gorm:"type:text;not null" json:"prefix_hash"`
	ToolsHash  string `gorm:"type:text;not null" json:"tools_hash"`
	// ToolsetVersion 记录请求时使用的冻结工具版本。
	ToolsetVersion int64 `gorm:"not null" json:"toolset_version"`

	ProviderRequestID string `gorm:"type:text;not null" json:"provider_request_id"`

	UsageComplete    bool   `json:"usage_complete"`
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
	CachedTokens     *int64 `json:"cached_tokens"`
	// CostEstimate / CostSource 同 bot_model_requests 口径（INT-139）。
	CostEstimate *float64 `json:"cost_estimate"`
	CostSource   *string  `json:"cost_source"`

	ErrorCode    *string    `json:"error_code,omitempty"`
	ErrorMessage *string    `json:"error_message,omitempty"`
	StartedAt    time.Time  `gorm:"not null" json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`

	MaintenanceRun BotMaintenanceRun `gorm:"foreignKey:MaintenanceRunID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotMaintenanceRequest) TableName() string {
	return "bot_maintenance_requests"
}

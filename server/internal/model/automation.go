package model

import "time"

// 自动化（INT-90）：用户可配置的触发调度与运行账本。Fast Ship 只负责触发、
// 上下文、运行记录、状态回报与后置操作；不做内置规则，不绑定具体 issue。
//
// 通用约定：ID 为 UUID，时间一律 UTC。T3 项目/线程/命令 ID 与 Issue ID 是
// 外部系统身份，只存值不建外键（远端生命周期不由本库管理）。

type AutomationTriggerType string

const (
	AutomationTriggerCron                AutomationTriggerType = "cron"
	AutomationTriggerIssueCreated        AutomationTriggerType = "issue_created"
	AutomationTriggerIssueStatusChanged  AutomationTriggerType = "issue_status_changed"
	AutomationTriggerIssueLabelAdded     AutomationTriggerType = "issue_label_added"
	AutomationTriggerGitHubPROpened      AutomationTriggerType = "github_pr_opened"
	AutomationTriggerGitHubPRSynchronize AutomationTriggerType = "github_pr_synchronize"
)

// IsPRTrigger GitHub PR 事件触发：走防抖/多模型/排队通道，与 cron/issue 的
// 单活动运行约束互不影响。
func (t AutomationTriggerType) IsPRTrigger() bool {
	return t == AutomationTriggerGitHubPROpened || t == AutomationTriggerGitHubPRSynchronize
}

func (t AutomationTriggerType) IsIssueEvent() bool {
	return t == AutomationTriggerIssueCreated ||
		t == AutomationTriggerIssueStatusChanged ||
		t == AutomationTriggerIssueLabelAdded
}

func IsValidAutomationTriggerType(t AutomationTriggerType) bool {
	switch t {
	case AutomationTriggerCron,
		AutomationTriggerIssueCreated,
		AutomationTriggerIssueStatusChanged,
		AutomationTriggerIssueLabelAdded,
		AutomationTriggerGitHubPROpened,
		AutomationTriggerGitHubPRSynchronize:
		return true
	}
	return false
}

type AutomationPostAction string

const (
	AutomationPostNone    AutomationPostAction = "none"
	AutomationPostSettle  AutomationPostAction = "settle"
	AutomationPostArchive AutomationPostAction = "archive"
)

func IsValidAutomationPostAction(a AutomationPostAction) bool {
	switch a {
	case AutomationPostNone, AutomationPostSettle, AutomationPostArchive:
		return true
	}
	return false
}

type AutomationRunStatus string

const (
	// RunStatusPendingRun 表示运行行已创建但尚未被 worker 认领（status 存空串）。
	RunStatusPendingRun     AutomationRunStatus = ""
	AutomationRunQueued     AutomationRunStatus = "queued"
	AutomationRunDispatched AutomationRunStatus = "dispatched"
	AutomationRunRunning    AutomationRunStatus = "running"
	AutomationRunCompleted  AutomationRunStatus = "completed"
	AutomationRunFailed     AutomationRunStatus = "failed"
	AutomationRunTimedOut   AutomationRunStatus = "timed_out"
	AutomationRunCancelled  AutomationRunStatus = "cancelled"
)

func (s AutomationRunStatus) Terminal() bool {
	return s == AutomationRunCompleted || s == AutomationRunFailed ||
		s == AutomationRunTimedOut || s == AutomationRunCancelled
}

type AutomationPostState string

const (
	AutomationPostStateNone      AutomationPostState = "none"
	AutomationPostStatePending   AutomationPostState = "pending"
	AutomationPostStateSucceeded AutomationPostState = "succeeded"
	AutomationPostStateFailed    AutomationPostState = "failed"
)

// Automation 一条用户自动化配置。更新只影响后续触发；进行中的运行按
// 创建时的 config_snapshot 执行。
type Automation struct {
	ID        string    `gorm:"type:text;primaryKey" json:"id"`
	UserID    string    `gorm:"type:text;not null;index:idx_automations_user_created,priority:1" json:"user_id"`
	Name      string    `gorm:"type:text;not null" json:"name"`
	Enabled   bool      `gorm:"not null;default:false" json:"enabled"`
	CreatedAt time.Time `gorm:"not null;index:idx_automations_user_created,priority:2" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	EnvironmentID string `gorm:"type:text;not null" json:"environment_id"`
	T3ProjectID   string `gorm:"column:t3_project_id;type:text;not null" json:"t3_project_id"`

	TriggerType    AutomationTriggerType `gorm:"type:text;not null" json:"trigger_type"`
	CronExpression string                `gorm:"column:cron_expression;type:text" json:"cron_expression"`
	Timezone       string                `gorm:"type:text" json:"timezone"`
	NextRunAt      *time.Time            `gorm:"index" json:"next_run_at"`

	// IssueProjectID 可选：只对单个 Fast Ship 项目的事件触发；nil 表示该项目用户的全部项目。
	IssueProjectID *string `gorm:"column:issue_project_id;type:text" json:"issue_project_id,omitempty"`
	// EventFilterJSON 事件过滤（状态变化 field/from/to、加标签匹配名称），无过滤为空串。
	EventFilterJSON string `gorm:"column:event_filter_json;type:text" json:"event_filter_json,omitempty"`

	PromptTemplate     string `gorm:"column:prompt_template;type:text;not null" json:"prompt_template"`
	ModelSelectionJSON string `gorm:"column:model_selection_json;type:text" json:"model_selection_json,omitempty"`
	// ModelSelectionsJSON GitHub PR 触发的多模型数组（元素同 model_selection 的单个对象）；
	// cron/issue 触发为空串。
	ModelSelectionsJSON string `gorm:"column:model_selections_json;type:text" json:"model_selections_json,omitempty"`
	// DebounceSeconds 仅 GitHub PR 触发：防抖窗口秒数（0=立即）；service 落库时
	// 显式给值（PR 触发缺省 60，cron/issue 恒 0），不用列默认值以便存 0。
	DebounceSeconds int                  `gorm:"not null;default:0" json:"debounce_seconds"`
	RuntimeMode     string               `gorm:"type:text;not null;default:full-access" json:"runtime_mode"`
	WorkspaceJSON   string               `gorm:"column:workspace_json;type:text" json:"workspace_json,omitempty"`
	TimeoutSeconds  int                  `gorm:"not null;default:3600" json:"timeout_seconds"`
	PostAction      AutomationPostAction `gorm:"column:post_action;type:text;not null;default:none" json:"post_action"`
	PostOnTimeout   bool                 `gorm:"column:post_on_timeout;not null;default:false" json:"post_on_timeout"`

	// 外键 CASCADE：删用户/环境/项目带走自动化；进行中的运行经 runs 的
	// automation_id CASCADE 一并清理（活动运行会先被 service 40919 拦下）。
	User         User          `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
	Environment  T3Environment `gorm:"foreignKey:EnvironmentID;constraint:OnDelete:CASCADE" json:"-"`
	IssueProject *Project      `gorm:"foreignKey:IssueProjectID;constraint:OnDelete:CASCADE" json:"-"`
}

func (Automation) TableName() string { return "automations" }

// AutomationRun 一次触发的完整账本：触发上下文、配置快照、预分配的 T3 身份、
// 状态回报与后置操作进度。status 为空串表示「已落库、待 worker 认领」；
// dispatched 表示已向 T3 发出启动（结果可能未确认），running 表示 agent 已回报开始。
type AutomationRun struct {
	ID           string `gorm:"type:text;primaryKey" json:"id"`
	UserID       string `gorm:"type:text;not null;index" json:"user_id"`
	AutomationID string `gorm:"column:automation_id;type:text;not null" json:"automation_id"`

	TriggerType AutomationTriggerType `gorm:"type:text;not null" json:"trigger_type"`
	// TriggerKey 同一自动化内唯一：cron 为 `cron:<原计划UTC>`，Issue 事件为 `event:<事件UUID>`。
	TriggerKey         string    `gorm:"column:trigger_key;type:text;not null" json:"trigger_key"`
	TriggerContextJSON string    `gorm:"column:trigger_context_json;type:text" json:"trigger_context_json"`
	TriggeredAt        time.Time `gorm:"not null" json:"triggered_at"`

	// ConfigSnapshotJSON 创建运行时的自动化配置快照（模板、模型、工作区、后置等）。
	// 不存渲染后提示词、令牌明文、线程内容。
	ConfigSnapshotJSON string `gorm:"column:config_snapshot_json;type:text;not null" json:"config_snapshot_json"`

	// 预分配的 T3 身份：launchThread 以这些 ID 幂等。
	EnvironmentID    string `gorm:"type:text;not null" json:"environment_id"`
	T3ProjectID      string `gorm:"column:t3_project_id;type:text;not null" json:"t3_project_id"`
	ThreadID         string `gorm:"column:thread_id;type:text;not null" json:"thread_id"`
	LaunchCommandID  string `gorm:"column:launch_command_id;type:text;not null" json:"launch_command_id"`
	InitialMessageID string `gorm:"column:initial_message_id;type:text;not null" json:"initial_message_id"`

	// Status 空串=待认领；queued=PR 审查排队（不占在途名额）；dispatched/running=
	// 活动；completed/failed/timed_out/cancelled=终态。
	Status            AutomationRunStatus `gorm:"type:text;not null;default:''" json:"status"`
	DispatchStartedAt *time.Time          `json:"dispatch_started_at,omitempty"`
	// DispatchedAt 仅在启动 RPC 结果得到确认（launchThread 成功或 agent 回报开始）时写入；
	// 为空表示线程是否建成未确认，此类运行不执行后置操作。
	DispatchedAt *time.Time `json:"dispatched_at,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `gorm:"index" json:"finished_at,omitempty"`

	// DeadlineAt 回报截止（triggered_at + timeout）。
	DeadlineAt time.Time `gorm:"not null" json:"deadline_at"`

	ErrorCode    string `gorm:"column:error_code;type:text" json:"error_code,omitempty"`
	ErrorMessage string `gorm:"column:error_message;type:text" json:"error_message,omitempty"`
	Summary      string `gorm:"type:text" json:"summary,omitempty"`

	// PostState 后置操作进度；pending 的运行继续占用「活动」判定（重入/删除约束）。
	PostState         AutomationPostState `gorm:"column:post_state;type:text;not null;default:none" json:"post_state"`
	PostAttempt       int                 `gorm:"column:post_attempt;not null;default:0" json:"post_attempt"`
	PostNextAttemptAt *time.Time          `gorm:"column:post_next_attempt_at" json:"post_next_attempt_at,omitempty"`
	PostDeadlineAt    *time.Time          `gorm:"column:post_deadline_at" json:"post_deadline_at,omitempty"`
	PostError         string              `gorm:"column:post_error;type:text" json:"post_error,omitempty"`

	// GitHub PR 审查（INT-93）专用列；cron/issue 触发保持零值。
	RepoFullName   string `gorm:"column:repo_full_name;type:text" json:"repo_full_name,omitempty"`
	PRNumber       int    `gorm:"column:pr_number" json:"pr_number,omitempty"`
	HeadSHA        string `gorm:"column:head_sha;type:text" json:"head_sha,omitempty"`
	RoundNumber    int    `gorm:"column:round_number" json:"round_number,omitempty"`
	InstallationID int64  `gorm:"column:installation_id" json:"installation_id,omitempty"`
	// CheckRunID 本运行在 GitHub 上的 check run；NULL=尚未创建（或退回合并 check）。
	CheckRunID *int64 `gorm:"column:check_run_id" json:"check_run_id,omitempty"`
	// ModelInstanceID/ModelName 本运行绑定的模型（一轮多模型并行，每个模型一行）。
	ModelInstanceID string `gorm:"column:model_instance_id;type:text" json:"model_instance_id,omitempty"`
	ModelName       string `gorm:"column:model_name;type:text" json:"model_name,omitempty"`
	// StopAttemptedAt 取消后的远端线程中断尝试（StopThread 只发一次的标记）；
	// 排队取消与未认领的空串运行为 NULL 且不发。
	StopAttemptedAt *time.Time `gorm:"column:stop_attempted_at" json:"stop_attempted_at,omitempty"`

	// 外键 CASCADE：删自动化带走全部运行记录；删用户级联（经 automations 或直接）。
	Automation Automation `gorm:"foreignKey:AutomationID;constraint:OnDelete:CASCADE" json:"-"`
	User       User       `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (AutomationRun) TableName() string { return "automation_runs" }

// AutomationRunToken 一次运行一个回报令牌；只存完整令牌（含 fsr_ 前缀）的
// SHA-256。结束回报消费令牌；expires_at 之后连重放也不再受理。
type AutomationRunToken struct {
	RunID      string     `gorm:"column:run_id;type:text;primaryKey" json:"run_id"`
	TokenHash  string     `gorm:"column:token_hash;type:text;not null;uniqueIndex" json:"token_hash"`
	CreatedAt  time.Time  `gorm:"not null" json:"created_at"`
	ExpiresAt  time.Time  `gorm:"column:expires_at;not null;index" json:"expires_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`

	Run AutomationRun `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE" json:"-"`
}

func (AutomationRunToken) TableName() string { return "automation_run_tokens" }

type NotificationKind string

const (
	NotificationAgent                    NotificationKind = "agent"
	NotificationAutomationDispatchFailed NotificationKind = "automation_dispatch_failed"
	NotificationAutomationRunTimeout     NotificationKind = "automation_run_timeout"
	NotificationAutomationPostFailed     NotificationKind = "automation_post_failed"
	NotificationAutomationBusy           NotificationKind = "automation_busy"
)

// Notification 站内通知。environment_id/thread_id/run_id 是弱引用（无外键，
// 目标删除后通知保留）；dedup_key 在 (user_id, dedup_key) 上唯一。
type Notification struct {
	ID       string           `gorm:"type:text;primaryKey" json:"id"`
	UserID   string           `gorm:"type:text;not null" json:"user_id"`
	Kind     NotificationKind `gorm:"type:text;not null" json:"kind"`
	DedupKey string           `gorm:"column:dedup_key;type:text;not null" json:"dedup_key"`
	// StateFingerprint 同键下业务状态的指纹：相同只续 last_seen_at，变化则更新并清已读。
	StateFingerprint string `gorm:"column:state_fingerprint;type:text;not null" json:"state_fingerprint"`
	Title            string `gorm:"type:text;not null" json:"title"`
	Body             string `gorm:"type:text" json:"body"`

	EnvironmentID string `gorm:"column:environment_id;type:text" json:"environment_id,omitempty"`
	ThreadID      string `gorm:"column:thread_id;type:text" json:"thread_id,omitempty"`
	RunID         string `gorm:"column:run_id;type:text" json:"run_id,omitempty"`

	CreatedAt  time.Time  `gorm:"not null" json:"created_at"`
	UpdatedAt  time.Time  `gorm:"not null" json:"updated_at"`
	LastSeenAt time.Time  `gorm:"column:last_seen_at;not null" json:"last_seen_at"`
	ReadAt     *time.Time `json:"read_at,omitempty"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (Notification) TableName() string { return "notifications" }

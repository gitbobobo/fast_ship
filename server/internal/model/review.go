package model

import "time"

// PR 审查（INT-135）：按项目开启，代码审查 + 视觉审查两段。系统负责流程
// （触发、防抖、排队、工作区、历史、评论、状态、衔接视觉审查），提示词只
// 保留审查判断本身。
//
// 通用约定：ID 为 UUID，时间一律 UTC；T3 项目/线程/命令与 GitHub 身份是外部
// 系统值，只存值不建外键（远端生命周期不由本库管理）。

// ReviewKind 审查类型。
type ReviewKind string

const (
	ReviewKindCode   ReviewKind = "code"
	ReviewKindVisual ReviewKind = "visual"
)

// ReviewRunStatus 一次审查运行的状态。queued=等名额；dispatched=已向 T3 发出
// 启动（结果可能未确认）；running=Agent 已读取审查上下文；其余为终态。
type ReviewRunStatus string

const (
	ReviewRunQueued     ReviewRunStatus = "queued"
	ReviewRunDispatched ReviewRunStatus = "dispatched"
	ReviewRunRunning    ReviewRunStatus = "running"
	ReviewRunCompleted  ReviewRunStatus = "completed"
	ReviewRunFailed     ReviewRunStatus = "failed"
	ReviewRunTimedOut   ReviewRunStatus = "timed_out"
	ReviewRunCancelled  ReviewRunStatus = "cancelled"
)

// Terminal 是否终态。
func (s ReviewRunStatus) Terminal() bool {
	return s == ReviewRunCompleted || s == ReviewRunFailed ||
		s == ReviewRunTimedOut || s == ReviewRunCancelled
}

// Active 是否在途（占用户并发名额）。
func (s ReviewRunStatus) Active() bool {
	return s == ReviewRunDispatched || s == ReviewRunRunning
}

// ReviewWorktreeState 代码审查工作区的回收进度。视觉审查不开工作区，恒为
// none。branch_pending（INT-170）表示工作树已删、本轮专属分支仍在：远程 T3
// 没有删分支的 RPC，分支消失只能靠审查代理收尾或人工清理，复查到分支不在
// 登记（ref 整条消失）才转 removed。
type ReviewWorktreeState string

const (
	ReviewWorktreeNone          ReviewWorktreeState = "none"
	ReviewWorktreeRemoved       ReviewWorktreeState = "removed"
	ReviewWorktreeFailed        ReviewWorktreeState = "failed"
	ReviewWorktreeBranchPending ReviewWorktreeState = "branch_pending"
)

// ProjectReviewSettings 项目级审查开关与组合配置。仓库（github_owner/repo）
// 取自项目本身；同一集成内同一仓库只允许一个项目开启（INT-167 起唯一键带
// integration_id，同名仓库在两个集成下可各自被启用）。
type ProjectReviewSettings struct {
	ProjectID string `gorm:"column:project_id;type:text;primaryKey" json:"project_id"`
	UserID    string `gorm:"column:user_id;type:text;not null;index" json:"user_id"`
	// RepoFullName 小写的 owner/repo 快照：同仓库唯一占用由部分唯一索引
	// (integration_id, repo_full_name) WHERE enabled 兜底，同一集成下开两个
	// 项目抢同一仓库时后写者失败。
	RepoFullName string `gorm:"column:repo_full_name;type:text" json:"repo_full_name"`
	// IntegrationID 审查侧的集成身份快照（INT-167）：保存设置时取项目绑定的
	// 归一值——内置 GitHub 记 ForgeIntegrationBuiltinGitHubID，Forgejo 记
	// forge_integrations.id。占用判定、webhook 反查、同步过滤与账本分区都带
	// 这一维：owner/repo 字面相同、换了实例就是换了仓库。
	IntegrationID string `gorm:"column:integration_id;type:text;not null" json:"integration_id"`

	// TriggerOpened/TriggerSynchronize 不带 default 标签：GORM Create 会把零值
	// 替换成列默认，首次显式保存 false 会被写成 true。新项目默认勾选由
	// GetSettings 对「无行」返回 true 提供。
	Enabled               bool `gorm:"not null;default:false" json:"enabled"`
	TriggerOpened         bool `gorm:"column:trigger_opened;not null" json:"trigger_opened"`
	TriggerSynchronize    bool `gorm:"column:trigger_synchronize;not null" json:"trigger_synchronize"`
	TriggerReadyForReview bool `gorm:"column:trigger_ready_for_review;not null;default:false" json:"trigger_ready_for_review"`
	TriggerReopened       bool `gorm:"column:trigger_reopened;not null;default:false" json:"trigger_reopened"`

	// EnabledAt 当前启用的打点（INT-167 轮询基线）：false→true 翻转时写入，
	// 停用清空、再启用重新打点。Forgejo 轮询用它区分「启用后丢的 webhook
	// 事件」与「启用前就存在的存量 open PR」——后者不该被首 tick 全部开轮
	// （GitHub 侧只审开启后的新事件）。存量升级行为 NULL：下一次 enabled
	// 保存时回填 created_at（不晚于实际启用时点，宁可多收不漏）。
	EnabledAt *time.Time `gorm:"column:enabled_at" json:"-"`

	// CodeCombinationsJSON / VisualCombinationsJSON 是 ReviewCombination 数组。
	// 代码审查 1–5 个（启用时必填），视觉审查 0–3 个（0 个即从不启动视觉审查）。
	CodeCombinationsJSON   string `gorm:"column:code_combinations_json;type:text" json:"code_combinations_json"`
	VisualCombinationsJSON string `gorm:"column:visual_combinations_json;type:text" json:"visual_combinations_json"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	Project Project `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
	User    User    `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (ProjectReviewSettings) TableName() string { return "project_review_settings" }

// ReviewRun 一次审查运行（一轮 × 一个组合）。提示词与结果不做二次加工存储：
// ResultJSON 保存 Agent 提交并通过校验的结构化结论。
type ReviewRun struct {
	ID        string     `gorm:"type:text;primaryKey" json:"id"`
	UserID    string     `gorm:"column:user_id;type:text;not null;index" json:"user_id"`
	ProjectID string     `gorm:"column:project_id;type:text;not null;index" json:"project_id"`
	Kind      ReviewKind `gorm:"type:text;not null" json:"kind"`

	// 审查对象快照：开轮时的 PR 状态（后续推送不改写历史轮次）。
	RepoFullName   string `gorm:"column:repo_full_name;type:text;not null" json:"repo_full_name"`
	PRNumber       int    `gorm:"column:pr_number;not null" json:"pr_number"`
	HeadSHA        string `gorm:"column:head_sha;type:text;not null" json:"head_sha"`
	RoundNumber    int    `gorm:"column:round_number;not null" json:"round_number"`
	InstallationID int64  `gorm:"column:installation_id" json:"installation_id"`
	// IntegrationID 本运行所属集成（INT-167，归一值）：按集成选 client、按
	// 集成隔离同名仓库的历史；入 idx_review_runs_combination 组合幂等键
	// （同名仓库换集成后是不同组合，不能被 OnConflict 吞行）。
	IntegrationID string `gorm:"column:integration_id;type:text;not null" json:"integration_id"`
	TriggerAction string `gorm:"column:trigger_action;type:text" json:"trigger_action"`
	PRTitle       string `gorm:"column:pr_title;type:text" json:"pr_title"`
	PRURL         string `gorm:"column:pr_url;type:text" json:"pr_url"`
	BaseRef       string `gorm:"column:base_ref;type:text" json:"base_ref"`
	HeadRef       string `gorm:"column:head_ref;type:text" json:"head_ref"`

	// 组合：环境 + harness（instance_id）+ 模型 + 选项（思考强度等）。
	EnvironmentID    string `gorm:"column:environment_id;type:text;not null" json:"environment_id"`
	T3ProjectID      string `gorm:"column:t3_project_id;type:text" json:"t3_project_id"`
	InstanceID       string `gorm:"column:instance_id;type:text;not null" json:"instance_id"`
	Model            string `gorm:"column:model;type:text;not null" json:"model"`
	ModelOptionsJSON string `gorm:"column:model_options_json;type:text" json:"model_options_json"`

	// 预分配的 T3 身份：重试与结果关联都以这些 ID 为准。
	ThreadID         string `gorm:"column:thread_id;type:text;not null" json:"thread_id"`
	LaunchCommandID  string `gorm:"column:launch_command_id;type:text;not null" json:"launch_command_id"`
	InitialMessageID string `gorm:"column:initial_message_id;type:text;not null" json:"initial_message_id"`

	Status            ReviewRunStatus `gorm:"type:text;not null;default:queued" json:"status"`
	DispatchStartedAt *time.Time      `json:"dispatch_started_at,omitempty"`
	// DispatchedAt 仅在启动 RPC 结果确认时写入；为空表示线程是否建成未确认。
	DispatchedAt *time.Time `json:"dispatched_at,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `gorm:"index" json:"finished_at,omitempty"`
	DeadlineAt   time.Time  `gorm:"column:deadline_at;not null" json:"deadline_at"`

	// PrepRetryCount T3 工作区准备失败后的自动重试次数（INT-147）：重试前先
	// 条件加一再发 RPC；达到上限后运行落 failed 终态，不再发第 4 次。
	PrepRetryCount int `gorm:"column:prep_retry_count;not null;default:0" json:"prep_retry_count"`

	ErrorCode    string `gorm:"column:error_code;type:text" json:"error_code,omitempty"`
	ErrorMessage string `gorm:"column:error_message;type:text" json:"error_message,omitempty"`
	Summary      string `gorm:"type:text" json:"summary,omitempty"`
	// ResultJSON Agent 提交的结构化结论（字段随 Kind 不同，提交时已校验）。
	ResultJSON string `gorm:"column:result_json;type:text" json:"result_json,omitempty"`

	// ReviewID 本运行提交的 GitHub review id（事件 COMMENT；只有 P0–P2 行内
	// 评论时才创建），0 表示没有。
	ReviewID int64 `gorm:"column:review_id" json:"review_id,omitempty"`
	// PostedAt 本运行结论是否已推送到 GitHub（评论/状态由 PR 级同步统一收敛）。
	PostedAt *time.Time `gorm:"column:posted_at" json:"posted_at,omitempty"`

	// WorktreePath/State 代码审查工作区回收；视觉审查恒为 none。
	WorktreePath string `gorm:"column:worktree_path;type:text" json:"worktree_path,omitempty"`
	// WorkspaceRoot launch 时项目绑定的主检出根目录快照：回收工作区要用它当
	// cwd，绑定之后被删/改也不能用当前绑定去猜。
	WorkspaceRoot string `gorm:"column:workspace_root;type:text" json:"workspace_root,omitempty"`
	// WorktreeBranch 线程投影读到的本轮专属分支名（INT-170）：与 worktree_path
	// 同款「空才补」回写。线程 404 后工作树与分支的两步回收只能靠行内快照，
	// 服务端又没有删分支的 RPC，删除结果（worktree 还在/分支仍在）都以这份
	// 快照对照远端登记判定。
	WorktreeBranch string              `gorm:"column:worktree_branch;type:text" json:"worktree_branch,omitempty"`
	WorktreeState  ReviewWorktreeState `gorm:"column:worktree_state;type:text;not null;default:none" json:"worktree_state"`
	WorktreeError  string              `gorm:"column:worktree_error;type:text" json:"worktree_error,omitempty"`
	// WorktreeReason 回收问题的分类码（INT-170，见 review_cleanup.go 的
	// worktreeReason* 常量）：removed 时清空；failed/branch_pending 与持续
	// 待确认（none + registry_incomplete / remove_uncertain /
	// remove_budget_pending）时写入。聚合通知按
	// (environment_id, repo_full_name, worktree_reason) 分键查询受影响
	// 运行清单。
	WorktreeReason string `gorm:"column:worktree_reason;type:text" json:"worktree_reason,omitempty"`

	// StopAttemptedAt 取消后的线程中断尝试（只发一次的标记）。
	StopAttemptedAt *time.Time `gorm:"column:stop_attempted_at" json:"stop_attempted_at,omitempty"`

	// ArchivedAt 终态后线程归档写回的时刻；空 = 尚未归档。归档有限重试超限
	// 后也写入当前时间（不再重试），失败原因见 worktree_error 与通知。
	ArchivedAt *time.Time `gorm:"column:archived_at" json:"archived_at,omitempty"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	User    User    `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
	Project Project `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE" json:"-"`
}

func (ReviewRun) TableName() string { return "review_runs" }

// ReviewRunToken 一次运行的运行令牌：只存完整令牌的 SHA-256。
type ReviewRunToken struct {
	RunID      string     `gorm:"column:run_id;type:text;primaryKey" json:"run_id"`
	TokenHash  string     `gorm:"column:token_hash;type:text;not null;uniqueIndex" json:"token_hash"`
	CreatedAt  time.Time  `gorm:"not null" json:"created_at"`
	ExpiresAt  time.Time  `gorm:"column:expires_at;not null" json:"expires_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`

	Run ReviewRun `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE" json:"-"`
}

func (ReviewRunToken) TableName() string { return "review_run_tokens" }

// ReviewPullRequest 一个 PR 的审查总账（评论落点、状态汇总、视觉审核进度）。
// 主键 (project_id, repo_full_name, pr_number, integration_id)：执行态按项目
// 分区——同一仓库换项目接管后各自一本账，互不吞事件、不混结果；GitHub 产出
// （评论、状态、行内 review）按 (integration_id, repo_full_name) 归一（同一
// 集成的同一仓库只允许一个项目 enabled）。
type ReviewPullRequest struct {
	ProjectID     string `gorm:"column:project_id;type:text;primaryKey" json:"project_id"`
	RepoFullName  string `gorm:"column:repo_full_name;type:text;primaryKey" json:"repo_full_name"`
	PRNumber      int    `gorm:"column:pr_number;primaryKey" json:"pr_number"`
	IntegrationID string `gorm:"column:integration_id;type:text;primaryKey" json:"integration_id"`

	UserID         string `gorm:"column:user_id;type:text;not null;index" json:"user_id"`
	InstallationID int64  `gorm:"column:installation_id" json:"installation_id"`

	RoundNumber int    `gorm:"column:round_number;not null;default:0" json:"round_number"`
	HeadSHA     string `gorm:"column:head_sha;type:text" json:"head_sha,omitempty"`

	// CodeCommentID/VisualCommentID 机器人顶层评论 id；0=尚未创建（按隐藏标记找回）。
	CodeCommentID   int64 `gorm:"column:code_comment_id" json:"code_comment_id,omitempty"`
	VisualCommentID int64 `gorm:"column:visual_comment_id" json:"visual_comment_id,omitempty"`

	// CodeStatus/VisualStatus 最近一次推给 GitHub 的 commit status 结论。
	CodeStatus   string `gorm:"column:code_status;type:text" json:"code_status,omitempty"`
	VisualStatus string `gorm:"column:visual_status;type:text" json:"visual_status,omitempty"`
	// VisualScreensFingerprint 上一轮视觉审查用的截图集合指纹；用于判断截图是否变化。
	VisualScreensFingerprint string `gorm:"column:visual_screens_fingerprint;type:text" json:"visual_screens_fingerprint,omitempty"`

	// SyncPending/SyncEpoch/SyncError 事务外 GitHub 同步标记：置 pending 时 epoch+1，
	// 同步方清标记按开始时的 epoch 条件写，期间的新 pending 不会被误清。
	SyncPending bool   `gorm:"column:sync_pending;not null;default:false" json:"sync_pending"`
	SyncEpoch   int    `gorm:"column:sync_epoch;not null;default:0" json:"sync_epoch"`
	SyncError   string `gorm:"column:sync_error;type:text" json:"sync_error,omitempty"`
	// SyncFailedEpoch 最近一次同步失败时的 epoch：等于当前 epoch 表示「同一份
	// 快照已失败过」，调度按退避间隔再重试（新事件会推进 epoch，立即重新入队）；
	// 成功后清零。
	SyncFailedEpoch int `gorm:"column:sync_failed_epoch;not null;default:0" json:"sync_failed_epoch"`

	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

func (ReviewPullRequest) TableName() string { return "review_pull_requests" }

// ReviewAsset 视觉审查用的截图（从 PR 描述里的图片链接下载后本地保存，
// 私有仓库的 user-attachments 走 App 令牌取）。
type ReviewAsset struct {
	ID string `gorm:"type:text;primaryKey" json:"id"`
	// 同一 (集成, repo, pr, head, 源地址) 只允许一行：并发视觉运行时两个 Agent
	// 都会先读到空列表，随机 id 不会撞，必须靠唯一索引兜住重复。integration_id
	// 键首：资产保持仓库级共享（无 project_id），但同名仓库在两个集成下是不同
	// 实例的图源，互不吞行。
	IntegrationID string `gorm:"column:integration_id;type:text;not null;uniqueIndex:idx_review_assets_source,priority:1" json:"integration_id"`
	RepoFullName  string `gorm:"column:repo_full_name;type:text;not null;uniqueIndex:idx_review_assets_source,priority:2" json:"repo_full_name"`
	PRNumber      int    `gorm:"column:pr_number;not null;uniqueIndex:idx_review_assets_source,priority:3" json:"pr_number"`
	HeadSHA       string `gorm:"column:head_sha;type:text;not null;uniqueIndex:idx_review_assets_source,priority:4" json:"head_sha"`

	Position    int    `gorm:"not null" json:"position"`
	Label       string `gorm:"type:text" json:"label"`
	SourceURL   string `gorm:"column:source_url;type:text;not null;uniqueIndex:idx_review_assets_source,priority:5" json:"source_url"`
	StoragePath string `gorm:"column:storage_path;type:text;not null" json:"storage_path"`
	MimeType    string `gorm:"column:mime_type;type:text" json:"mime_type"`
	SizeBytes   int64  `gorm:"column:size_bytes" json:"size_bytes"`
	// Error 下载失败的记录（StoragePath 为空时图片端点回 404）。
	Error     string    `gorm:"column:error;type:text" json:"error,omitempty"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
}

func (ReviewAsset) TableName() string { return "review_assets" }

// ReviewDebounce 同一项目+PR 的防抖窗口：窗口内的新推送只更新 head SHA 并推后
// fire_at，到点按最新 SHA 开轮（防抖固定 60s，不提供配置）。主键带
// integration_id：同名仓库在两个集成下的推送各算各的窗口。
type ReviewDebounce struct {
	ProjectID     string `gorm:"column:project_id;type:text;primaryKey" json:"project_id"`
	RepoFullName  string `gorm:"column:repo_full_name;type:text;primaryKey" json:"repo_full_name"`
	PRNumber      int    `gorm:"column:pr_number;primaryKey" json:"pr_number"`
	IntegrationID string `gorm:"column:integration_id;type:text;primaryKey" json:"integration_id"`

	HeadSHA        string    `gorm:"column:head_sha;type:text;not null" json:"head_sha"`
	Action         string    `gorm:"column:action;type:text;not null" json:"action"`
	InstallationID int64     `gorm:"column:installation_id" json:"installation_id"`
	PRTitle        string    `gorm:"column:pr_title;type:text" json:"pr_title"`
	PRURL          string    `gorm:"column:pr_url;type:text" json:"pr_url"`
	BaseRef        string    `gorm:"column:base_ref;type:text" json:"base_ref"`
	HeadRef        string    `gorm:"column:head_ref;type:text" json:"head_ref"`
	FireAt         time.Time `gorm:"column:fire_at;not null;index" json:"fire_at"`
	UpdatedAt      time.Time `gorm:"not null" json:"updated_at"`
}

func (ReviewDebounce) TableName() string { return "review_debounces" }

// ReviewSeenSHA 某项目已受理过的 head SHA（force-push 回旧 SHA 不再开轮）。
// 主键 (project_id, repo_full_name, pr_number, head_sha, integration_id)：
// 接管后新项目对同仓库同一 SHA 重新受理（各项目一本已受理账，旧项目的已见
// 集合不沿用）；同名仓库在两个集成下也各记各的受理账。
type ReviewSeenSHA struct {
	ProjectID     string     `gorm:"column:project_id;type:text;primaryKey" json:"project_id"`
	RepoFullName  string     `gorm:"column:repo_full_name;type:text;primaryKey" json:"repo_full_name"`
	PRNumber      int        `gorm:"column:pr_number;primaryKey" json:"pr_number"`
	HeadSHA       string     `gorm:"column:head_sha;type:text;primaryKey" json:"head_sha"`
	IntegrationID string     `gorm:"column:integration_id;type:text;primaryKey" json:"integration_id"`
	PRUpdatedAt   *time.Time `gorm:"column:pr_updated_at" json:"pr_updated_at,omitempty"`
	AcceptedAt    time.Time  `gorm:"column:accepted_at;not null" json:"accepted_at"`
}

func (ReviewSeenSHA) TableName() string { return "review_seen_shas" }

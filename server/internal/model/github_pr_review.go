package model

import "time"

// GitHub PR 自动评审（INT-93）的支撑表。均不含指向 GitHub 的外键——
// 远端对象（delivery、PR、check）的生命周期由 GitHub 管理，这里只存账。

// GitHubWebhookDelivery webhook 幂等账：X-GitHub-Delivery 为 PK，重复投递插入
// 冲突直接 202，不再开轮。行保留 7 天，由调度循环顺带清理。
type GitHubWebhookDelivery struct {
	DeliveryID string    `gorm:"column:delivery_id;type:text;primaryKey" json:"delivery_id"`
	Event      string    `gorm:"column:event;type:text;not null" json:"event"`
	ReceivedAt time.Time `gorm:"column:received_at;not null" json:"received_at"`
}

func (GitHubWebhookDelivery) TableName() string { return "github_webhook_deliveries" }

// GitHubPRDebounce 防抖窗口账：每个 (规则, 仓库, PR) 一行，只留最新 head SHA，
// 窗口内新推送重置 fire_at。到点由调度循环（或 debounce=0 的请求事务）消费并删行。
type GitHubPRDebounce struct {
	AutomationID   string    `gorm:"column:automation_id;type:text;primaryKey" json:"automation_id"`
	RepoFullName   string    `gorm:"column:repo_full_name;type:text;primaryKey" json:"repo_full_name"`
	PRNumber       int       `gorm:"column:pr_number;primaryKey" json:"pr_number"`
	UserID         string    `gorm:"column:user_id;type:text;not null" json:"user_id"`
	ProjectID      string    `gorm:"column:project_id;type:text;not null" json:"project_id"`
	InstallationID int64     `gorm:"column:installation_id;not null" json:"installation_id"`
	Action         string    `gorm:"column:action;type:text;not null" json:"action"` // opened | synchronize
	HeadSHA        string    `gorm:"column:head_sha;type:text;not null" json:"head_sha"`
	PRTitle        string    `gorm:"column:pr_title;type:text" json:"pr_title"`
	PRURL          string    `gorm:"column:pr_url;type:text" json:"pr_url"`
	BaseRef        string    `gorm:"column:base_ref;type:text" json:"base_ref"`
	HeadRef        string    `gorm:"column:head_ref;type:text" json:"head_ref"`
	FireAt         time.Time `gorm:"column:fire_at;not null;index" json:"fire_at"`
	UpdatedAt      time.Time `gorm:"column:updated_at;not null" json:"updated_at"`
}

func (GitHubPRDebounce) TableName() string { return "github_pr_debounces" }

// GitHubPRReview 每 PR 一行的评论/check 同步账：comment_id 是机器人顶层评论
// 的 GitHub ID；sync_pending 置位表示本轮状态变化待同步到 GitHub（事务外执行，
// 失败记 sync_error 下轮重试，不影响运行终态）。
type GitHubPRReview struct {
	RepoFullName   string    `gorm:"column:repo_full_name;type:text;primaryKey" json:"repo_full_name"`
	PRNumber       int       `gorm:"column:pr_number;primaryKey" json:"pr_number"`
	InstallationID int64     `gorm:"column:installation_id;not null" json:"installation_id"`
	CommentID      int64     `gorm:"column:comment_id" json:"comment_id"`
	RoundNumber    int       `gorm:"column:round_number;not null" json:"round_number"`
	HeadSHA        string    `gorm:"column:head_sha;type:text;not null" json:"head_sha"`
	SyncPending    bool      `gorm:"column:sync_pending;not null;default:false;index" json:"sync_pending"`
	SyncEpoch      int64     `gorm:"column:sync_epoch;not null;default:0" json:"sync_epoch"`
	SyncError      string    `gorm:"column:sync_error;type:text" json:"sync_error,omitempty"`
	UpdatedAt      time.Time `gorm:"column:updated_at;not null" json:"updated_at"`
}

func (GitHubPRReview) TableName() string { return "github_pr_reviews" }

// GitHubPRSeenSHA 已受理过的 PR head SHA。防抖窗口内还没有 github_pr_reviews
// 行、也没有运行时，靠这张表拒绝迟到的旧 SHA，避免 DeleteStaleSHATx 把当前
// debounce 删掉。force-push 回已见过的 SHA 不再开轮。
type GitHubPRSeenSHA struct {
	RepoFullName string     `gorm:"column:repo_full_name;type:text;primaryKey" json:"repo_full_name"`
	PRNumber     int        `gorm:"column:pr_number;primaryKey" json:"pr_number"`
	HeadSHA      string     `gorm:"column:head_sha;type:text;primaryKey" json:"head_sha"`
	SeenAt       time.Time  `gorm:"column:seen_at;not null" json:"seen_at"`
	PRUpdatedAt  *time.Time `gorm:"column:pr_updated_at" json:"pr_updated_at"`
}

func (GitHubPRSeenSHA) TableName() string { return "github_pr_seen_shas" }

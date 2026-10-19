package model

import "time"

// 站内通知（原属 automation.go；INT-135 起通用自动化移除，kind 收窄为
// 审查系统故障三类）。environment_id/thread_id/run_id 是
// 弱引用（无外键，目标删除后通知保留）；dedup_key 在 (user_id, dedup_key) 上唯一。

type NotificationKind string

const (
	// NotificationReviewDispatchFailed 审查运行派发失败（建线程/发消息未确认）。
	NotificationReviewDispatchFailed NotificationKind = "review_dispatch_failed"
	// NotificationReviewRunTimeout 审查运行超时未回报。
	NotificationReviewRunTimeout NotificationKind = "review_run_timeout"
	// NotificationReviewCleanupFailed 审查工作区清理失败（不影响审查结论）。
	NotificationReviewCleanupFailed NotificationKind = "review_cleanup_failed"
)

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

package model

import "time"

// GitHubWebhookRedelivery webhook 重投次数账（INT-146）：guid 为主键，跨 24h
// 对账窗口累计本服务发出的重投次数。次数必须持久化——投递列表只保留约
// 3 天，原始行滑出回看窗口后从列表推导的次数会变少（可能重复重投或漏发
// 超限通知）。行由调度阶段按 reviewDeliveryRetention 清理。
type GitHubWebhookRedelivery struct {
	GUID      string    `gorm:"column:guid;type:text;primaryKey" json:"guid"`
	Attempts  int       `gorm:"column:attempts;not null;default:0" json:"attempts"`
	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`
}

func (GitHubWebhookRedelivery) TableName() string { return "github_webhook_redeliveries" }

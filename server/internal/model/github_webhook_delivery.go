package model

import "time"

// GitHubWebhookDelivery webhook 幂等账：X-GitHub-Delivery 为 PK，重复投递插入
// 冲突直接 202，不再处理。行保留 7 天，由审查调度循环顺带清理。
type GitHubWebhookDelivery struct {
	DeliveryID string    `gorm:"column:delivery_id;type:text;primaryKey" json:"delivery_id"`
	Event      string    `gorm:"column:event;type:text;not null" json:"event"`
	ReceivedAt time.Time `gorm:"column:received_at;not null" json:"received_at"`
}

func (GitHubWebhookDelivery) TableName() string { return "github_webhook_deliveries" }

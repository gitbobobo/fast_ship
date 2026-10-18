package model

import "time"

// BotModelWindow 是按「服务地址 + 模型名」缓存的模型上下文窗口元数据
// （INT-115 需求一）。地址与模型都做规范化（地址 TrimSpace + 去尾部 /，模型
// 原样）；同名模型在不同服务之间不串用。计数列 0 表示未知——缺失字段绝不
// 当成 0 上限参与预算，解析方只认 >0 的值。这是探测缓存而非配置：无外键、
// 不随机器人删除级联，地址或模型变化后旧行自然失效（键不同）。
type BotModelWindow struct {
	ID              string `gorm:"type:text;primaryKey" json:"id"`
	BaseURL         string `gorm:"type:text;not null" json:"base_url"`
	Model           string `gorm:"column:model;type:text;not null" json:"model"`
	ContextWindow   int64  `gorm:"not null;default:0" json:"context_window"`
	MaxOutputTokens int64  `gorm:"not null;default:0" json:"max_output_tokens"`
	// Source 目前恒为 "detected"（来自 /models 探测）；保留列以区分未来
	// 其他来源。
	Source string `gorm:"type:text;not null" json:"source"`
	// Detail 记录探测方式（如 "GET /models"），供展示层标注口径。
	Detail    string    `gorm:"type:text;not null;default:''" json:"detail"`
	FetchedAt time.Time `gorm:"not null" json:"fetched_at"`
	// FailedAt 记录最近一次探测失败的时间，只用于失败退避（成功行 10 分钟内
	// 不重复同步探测）；不影响成功数据的新鲜度——24h TTL 仍看 FetchedAt。
	// 成功探测写入时置 nil 清除退避；成功行保留窗口/输出/原始 fetched_at。
	FailedAt  *time.Time `gorm:"type:timestamp" json:"failed_at,omitempty"`
	UpdatedAt time.Time  `gorm:"not null" json:"updated_at"`
}

func (BotModelWindow) TableName() string {
	return "bot_model_windows"
}

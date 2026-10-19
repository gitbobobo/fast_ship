package model

import "time"

// BotScheduleTask 是机器人绑定的定时任务（INT-103）：只由机器人经 fs_schedule
// 工具创建/修改/删除，用户只能查看与开启/关闭。调度事实来源是 next_run_at
// （UTC），cron 五字段 + 独立 IANA 时区（空串 = UTC）在触发时换算。
type BotScheduleTask struct {
	ID    string `gorm:"type:text;primaryKey" json:"id"`
	BotID string `gorm:"type:text;not null" json:"bot_id"`
	Name  string `gorm:"type:text;not null" json:"name"`
	// CronExpression 是标准五字段（分 时 日 月 周）；不内嵌时区前缀。
	CronExpression string `gorm:"type:text;not null" json:"cron_expression"`
	// Timezone 是 IANA 名称（如 Asia/Shanghai）；空串 = UTC。
	Timezone string `gorm:"type:text;not null;default:''" json:"timezone"`
	// Instruction 是每次运行交给机器人的任务指令。
	Instruction string `gorm:"type:text;not null" json:"instruction"`
	// Model 可选：空串用机器人当前模型。
	Model string `gorm:"type:text;not null;default:''" json:"model"`
	// NeedsReport 为 true 时，成功且非静默的运行也追加事件进主线。
	NeedsReport bool `gorm:"not null;default:false" json:"needs_report"`
	// Enabled 关闭后不再触发；重新开启时 next_run_at 重算为 now 之后的下一次。
	Enabled bool `gorm:"not null;default:true" json:"enabled"`
	// NextRunAt 是下一次计划触发时间（UTC）；nil 表示停用或配置损坏暂停。
	NextRunAt *time.Time `json:"next_run_at"`

	// StateJSON 是任务跨运行保留的小状态（INT-123）：JSON 文本，空串 = 无状态。
	// 与长期记忆无关——不写 bot_memory_*、不产生记忆版本；随任务删除而删除
	// （同表列，天然级联），改任务指令/开关/模型不触碰它。
	StateJSON string `gorm:"type:text;not null;default:''" json:"state_json"`
	// StateUpdatedAt 是状态最近一次写入时间；无状态时为 NULL。
	StateUpdatedAt *time.Time `json:"state_updated_at"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	Bot Bot `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotScheduleTask) TableName() string {
	return "bot_schedule_tasks"
}

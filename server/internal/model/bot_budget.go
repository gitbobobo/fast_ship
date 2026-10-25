package model

import "time"

// 机器人花费预算（INT-164 第三期）：预算挂在机器人或某个定时任务上，
// 周期为每日或每月，单位为美元，并带一组告警阈值（如 0.8 = 80%）。
// 预算只统计已计价金额，超过阈值只告警不停止运行；未设置预算时系统行为与
// 引入预算前完全一致。
//
// 目标列 bot_id / schedule_task_id 恰好其一非空（表级 check）；目标删除时
// 预算随 CASCADE 消失（给已删除对象保留预算没有意义）。同一目标同一周期
// 至多一条预算，由 repository.BotBudgetDDL 的两条部分唯一索引保证
// （用户还能经 REST upsert 覆盖，不会出现第二条）。

type BotBudgetPeriod string

const (
	// BotBudgetPeriodDaily 每日预算：按用户有效时区的当天 [当地零点, 现在)。
	BotBudgetPeriodDaily BotBudgetPeriod = "daily"
	// BotBudgetPeriodMonthly 每月预算：按用户有效时区的当月 1 日零点起。
	BotBudgetPeriodMonthly BotBudgetPeriod = "monthly"
)

// BotBudget 是一条预算配置。Thresholds 是规范化后的阈值 JSON 数组文本
// （去重升序，1 = 100%；服务层校验值域并换算成整数基点做告警键）。
type BotBudget struct {
	ID string `gorm:"type:text;primaryKey" json:"id"`
	// UserID 是预算归属（= 目标机器人的归属用户）；随用户级联删除。
	UserID string `gorm:"column:user_id;type:text;not null" json:"user_id"`
	// BotID 与 ScheduleTaskID 恰好其一非空（表级 check 约束）。
	BotID          *string `gorm:"column:bot_id;type:text" json:"bot_id,omitempty"`
	ScheduleTaskID *string `gorm:"column:schedule_task_id;type:text" json:"schedule_task_id,omitempty"`

	Period    BotBudgetPeriod `gorm:"type:text;not null;check:bot_budgets_target_exactly_one,(bot_id IS NOT NULL) != (schedule_task_id IS NOT NULL)" json:"period"`
	AmountUSD float64         `gorm:"column:amount_usd;type:real;not null" json:"amount_usd"`
	// Thresholds 是阈值 JSON 数组文本（如 "[0.8,1]"）：值 1 表示预算的
	// 100%，大于 1 表示按比例超出的档位。规范化与校验在服务层。
	Thresholds string `gorm:"type:text;not null" json:"thresholds"`

	CreatedAt time.Time `gorm:"not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"not null" json:"updated_at"`

	User         User             `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
	Bot          *Bot             `gorm:"foreignKey:BotID;constraint:OnDelete:CASCADE" json:"-"`
	ScheduleTask *BotScheduleTask `gorm:"foreignKey:ScheduleTaskID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotBudget) TableName() string { return "bot_budgets" }

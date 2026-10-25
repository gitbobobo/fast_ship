package model

import "time"

// 花费页按天汇总（INT-164）：bot_usage_daily 是把实时账本
// （bot_runs / bot_model_requests / bot_maintenance_*）按「用户有效时区的
// 当地日 × 归属桶」预聚合的持久层。历史日期读这张表，缺失/脏日期与今天回退
// 实时明细扫描——汇总只是缓存，账本仍是事实来源。
//
// 口径与 service/bot_usage.go 的实时累加完全同源：
//   - date 是写入时该用户有效时区下的当地日（YYYY-MM-DD）；有效时区变化后
//     覆盖标记上的 timezone 与当前不同会被判脏整表重算（明细还在）。已删除
//     机器人明细已级联清理，date 永远停留在写入时的时区口径（已知限制）。
//   - 记日：run 自身成本按 run.created_at；维护成本按维护自身 created_at
//     记日、归属桶跟父 run 走；父 run 行不存在或属于其他 bot → maintenance。
//   - kind 是解析后的归属桶：普通主线 run（含解析不出的 schedule_event）→
//     main；schedule run → schedule（带 task_id）；已解析的 schedule_event
//     主线 run → schedule_event（带反查到的 task_id）；有父 run 的维护 →
//     父 run 的桶；无父维护 → maintenance。
//   - 金额/token 沿用缺失 ≠ 0：display_cost 是请求级实收优先
//     （COALESCE(provider_cost, cost_estimate)）的桶合计，与总览/明细/预算
//     共用同一口径，没有任何金额成分时保持 NULL；reasoning_tokens 全 NULL
//     保持 NULL。
//   - display_costs 是 JSON 数组：该日该桶下各 run 的显示成本样本（run 自身
//   - 全部关联维护，与实时 avg/max/p90 同一集合）。只有 run 类桶行携带；
//     维护行的 display_costs 恒为 []（维护把成本/请求/token 折进它发生日的
//     父桶行，不产生运行样本）。真零保留、NULL 排除，整日替换不追加。
//
// bot_id / schedule_task_id 不建外键：删除机器人/任务后历史汇总照常展示
// （bot_name/task_name 是写入时快照）；整张表只随用户删除级联清理。
const (
	BotUsageDayMain          = "main"
	BotUsageDaySchedule      = "schedule"
	BotUsageDayScheduleEvent = "schedule_event"
	BotUsageDayMaintenance   = "maintenance"
)

// BotUsageDaily 一行 = (user_id, bot_id, date, kind, schedule_task_id)。
// 唯一性由两条部分唯一索引兜底（SQLite 复合 UNIQUE 对 NULL 不去重）：
// schedule_task_id IS NULL 时 (bot_id,date,kind) 唯一；非空时
// (bot_id,date,kind,schedule_task_id) 唯一。
type BotUsageDaily struct {
	ID     string `gorm:"type:text;primaryKey" json:"id"`
	UserID string `gorm:"type:text;not null" json:"user_id"`
	BotID  string `gorm:"type:text;not null" json:"bot_id"`
	// BotName 是写入时的机器人名快照：bot 删除后花费页按快照展示原名。
	BotName        string  `gorm:"type:text;not null" json:"bot_name"`
	ScheduleTaskID *string `gorm:"type:text" json:"schedule_task_id"`
	// TaskName 是任务名快照：写入时取活名，重滚拿不到活名时按序沿用
	// 上一版快照与 bot_schedule_task_names 墓碑（删除时刻写下的名字，
	// 覆盖旧快照）；任务从未有过名字或无关联时为空串。
	TaskName string `gorm:"type:text;not null;default:''" json:"task_name"`
	Date     string `gorm:"type:text;not null" json:"date"`
	Kind     string `gorm:"type:text;not null" json:"kind"`
	// Requests 含折进本桶的维护请求；UnpricedRequests 是展示口径的无金额数
	// （估算与实收都为空），保证 Requests - UnpricedRequests = 进合计的
	// 请求数，与实时口径一致。
	Requests         int64 `gorm:"not null;default:0" json:"requests"`
	UnpricedRequests int64 `gorm:"not null;default:0" json:"unpriced_requests"`
	// Runs / Failed / Skipped 只统计 run 行（schedule_event 桶的 run 数对应
	// 连带主线 run，不进任务的 runs/failed/skipped 投影——与实时口径一致）。
	Runs             int64 `gorm:"not null;default:0" json:"runs"`
	Failed           int64 `gorm:"not null;default:0" json:"failed"`
	Skipped          int64 `gorm:"not null;default:0" json:"skipped"`
	PromptTokens     int64 `gorm:"not null;default:0" json:"prompt_tokens"`
	CachedTokens     int64 `gorm:"not null;default:0" json:"cached_tokens"`
	CompletionTokens int64 `gorm:"not null;default:0" json:"completion_tokens"`
	// ReasoningTokens / DisplayCost 可空：空集合保持 NULL。
	ReasoningTokens *int64   `json:"reasoning_tokens"`
	DisplayCost     *float64 `json:"display_cost"`
	// DisplayCosts 是该桶当日各 run 显示成本的 JSON 数组（见文件头口径）。
	DisplayCosts string    `gorm:"type:text;not null;default:'[]'" json:"display_costs"`
	RolledAt     time.Time `gorm:"not null" json:"rolled_at"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotUsageDaily) TableName() string {
	return "bot_usage_daily"
}

// BotUsageRollupDay 是 (user_id, bot_id, date) 的覆盖标记：该日已按 timezone
// 口径汇总完成。明细指纹（四类行数 + 最近写入时刻）用于迟到写入/收尾更新的
// 脏日判定——指纹与当前明细不符即重算该日；timezone 与当前有效时区不同则
// 该 bot 的汇总整表重算。
type BotUsageRollupDay struct {
	UserID string `gorm:"type:text;primaryKey" json:"user_id"`
	BotID  string `gorm:"type:text;primaryKey" json:"bot_id"`
	Date   string `gorm:"type:text;primaryKey" json:"date"`
	// Timezone 是写入时的有效时区名（IANA）。
	Timezone string    `gorm:"type:text;not null" json:"timezone"`
	RolledAt time.Time `gorm:"not null" json:"rolled_at"`
	// 明细指纹：当日 run 数、触及当日的维护运行数（当日创建 ∪ 父 run 属当日）、
	// 当日 run 名下请求数、触及当日维护名下请求数、相关行最近一次
	// created/finished 写入（unix nano）。请求 INSERT 改变计数、收尾 UPDATE
	// 推进 LastWriteNs——两类迟写都能把该日判脏。
	RunCount          int64 `gorm:"not null;default:0" json:"run_count"`
	MaintRunCount     int64 `gorm:"not null;default:0" json:"maint_run_count"`
	RequestCount      int64 `gorm:"not null;default:0" json:"request_count"`
	MaintRequestCount int64 `gorm:"not null;default:0" json:"maint_request_count"`
	LastWriteNs       int64 `gorm:"not null;default:0" json:"last_write_ns"`

	User User `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE" json:"-"`
}

func (BotUsageRollupDay) TableName() string {
	return "bot_usage_rollup_days"
}

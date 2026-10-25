package repository

import (
	"errors"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// BotBudgetModels 返回预算表模型。**接线说明**：主代理把它并入
// bot_schema.go 的 BotModels()，把 BotBudgetDDL 并入 botSchemaDDL，本文件
// 不直接修改 bot_schema.go（与并行任务的文件分工）。
func BotBudgetModels() []interface{} {
	return []interface{}{&model.BotBudget{}}
}

// BotBudgetDDL 是 AutoMigrate 建不了的部分唯一索引与查询索引，全部幂等。
// 同一目标同一周期至多一条预算：bot 预算与 task 预算各自一条部分唯一索引
// （另一列恒为 NULL，普通复合唯一索引挡不住）。
var BotBudgetDDL = []string{
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_budgets_bot_period ON bot_budgets(bot_id, period) WHERE bot_id IS NOT NULL`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_bot_budgets_schedule_task_period ON bot_budgets(schedule_task_id, period) WHERE schedule_task_id IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_bot_budgets_user ON bot_budgets(user_id)`,
}

// BotBudgetRepository 是预算配置的读写与周期花费的轻量聚合（INT-164）。
// 配置读写很轻（每用户至多几十行）；花费聚合不改账本、不用花费页的
// 30 天总览，按窗口一次扫描 bot_runs + bot_maintenance_* 得到已计价金额。
type BotBudgetRepository struct {
	db *gorm.DB
}

func NewBotBudgetRepository(db *gorm.DB) *BotBudgetRepository {
	return &BotBudgetRepository{db: db}
}

// ---------- 配置读写 ----------

// ListAll 返回全部预算（评估循环按用户分组用）。
func (r *BotBudgetRepository) ListAll() ([]model.BotBudget, error) {
	var rows []model.BotBudget
	if err := r.db.Order("user_id ASC, created_at ASC, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListByBot 返回某机器人的预算（至多每日/每月各一条）。
func (r *BotBudgetRepository) ListByBot(botID string) ([]model.BotBudget, error) {
	var rows []model.BotBudget
	if err := r.db.Where("bot_id = ?", botID).Order("period ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListByScheduleTask 返回某定时任务的预算（至多每日/每月各一条）。
func (r *BotBudgetRepository) ListByScheduleTask(taskID string) ([]model.BotBudget, error) {
	var rows []model.BotBudget
	if err := r.db.Where("schedule_task_id = ?", taskID).Order("period ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Upsert 按「目标 + 周期」覆盖预算：存在则更新金额与阈值（保留 id 与创建
// 时间），否则插入。不依赖 SQLite 的 ON CONFLICT 目标谓词（部分索引的目标
// 写法各版本不一致），读改写收在一个事务里；生产单连接池天然串行。
func (r *BotBudgetRepository) Upsert(b *model.BotBudget) error {
	// 边界校验先于事务：双 nil 会在下文的解引用处 panic，check 约束
	// 根本轮不到；双非 nil 同样违法（目标必须恰好一个）。
	if (b.BotID == nil) == (b.ScheduleTaskID == nil) {
		return errors.New("bot_budget: 目标必须恰好一个（bot_id 或 schedule_task_id）")
	}
	return r.db.Transaction(func(tx *gorm.DB) error {
		q := tx.Where("period = ?", b.Period)
		if b.BotID != nil {
			q = q.Where("bot_id = ?", *b.BotID)
		} else {
			q = q.Where("schedule_task_id = ?", *b.ScheduleTaskID)
		}
		var existing model.BotBudget
		err := q.First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(b).Error
		}
		if err != nil {
			return err
		}
		b.ID = existing.ID
		b.CreatedAt = existing.CreatedAt
		return tx.Model(&model.BotBudget{}).Where("id = ?", existing.ID).Updates(map[string]any{
			"amount_usd": b.AmountUSD,
			"thresholds": b.Thresholds,
			"updated_at": b.UpdatedAt,
		}).Error
	})
}

// DeleteByBotPeriod 删除某机器人某周期的预算；返回是否真的删除了行。
func (r *BotBudgetRepository) DeleteByBotPeriod(botID string, period model.BotBudgetPeriod) (bool, error) {
	res := r.db.Where("bot_id = ? AND period = ?", botID, period).Delete(&model.BotBudget{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// DeleteByScheduleTaskPeriod 删除某定时任务某周期的预算。
func (r *BotBudgetRepository) DeleteByScheduleTaskPeriod(taskID string, period model.BotBudgetPeriod) (bool, error) {
	res := r.db.Where("schedule_task_id = ? AND period = ?", taskID, period).Delete(&model.BotBudget{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ---------- 周期花费聚合 ----------

// BotBudgetRunRow 是窗口内 run 的金额扫描行：只取归属与已计价成本，不回读
// 快照等大列。
type BotBudgetRunRow struct {
	ID             string
	BotID          string
	SessionKind    string
	TriggerSource  string
	TriggerKey     string
	ScheduleTaskID *string
	CostEstimate   *float64
}

// BotBudgetSpend 是一次窗口扫描的金额结果（USD，只含已计价部分）：
// BotUSD 按机器人汇总全部 run 与维护花费；TaskUSD 按定时任务汇总该任务的
// schedule run、解析到该任务的 schedule_event 主线 run 及它们的关联维护。
type BotBudgetSpend struct {
	BotUSD  map[string]float64
	TaskUSD map[string]float64
}

// Bot 返回机器人已计价花费（无记录为 0）。
func (s BotBudgetSpend) Bot(botID string) float64 { return s.BotUSD[botID] }

// Task 返回定时任务已计价花费（无记录为 0）。
func (s BotBudgetSpend) Task(taskID string) float64 { return s.TaskUSD[taskID] }

// SpendInWindow 聚合机器人集合在 [start, end) 内的已计价金额。口径：
//   - 请求行按「实际金额优先」计价：provider_cost（只落置信 USD 的行）
//     覆盖 cost_estimate，与花费页展示口径一致（INT-164）；
//   - run（主线与 schedule 都算）按其请求行合计记入所属机器人；无请求
//     行时回退 run.cost_estimate（历史行口径不变）；
//   - 维护请求按维护运行自己的 created_at 落在窗口内计入机器人（有父无父都
//     算，父 run 可能在窗口外）；任务归属按父 run 反查——父在窗口内经
//     parentUSD 消费，父在窗口外的单独补一轮（与花费页
//     ScheduleTaskCostsLast7d 的 lateParent 同口径：跨午夜收尾的维护不丢）；
//   - 任务的 schedule run 按 schedule_task_id 归任务；schedule_event 唤醒的
//     主线 run 经 trigger_key（schedule_event:<runID>:<status>）反查被引用
//     schedule run 的任务归属；
//   - cost_estimate IS NULL 且无 provider_cost 即未计价，不计入（缺失 ≠ 0）。
func (r *BotBudgetRepository) SpendInWindow(botIDs []string, start, end time.Time) (BotBudgetSpend, error) {
	out := BotBudgetSpend{BotUSD: map[string]float64{}, TaskUSD: map[string]float64{}}
	if len(botIDs) == 0 {
		return out, nil
	}
	runs, err := r.listRunsInWindow(botIDs, start, end)
	if err != nil {
		return out, err
	}
	runIDs := make([]string, 0, len(runs))
	botSet := make(map[string]bool, len(botIDs))
	for _, id := range botIDs {
		botSet[id] = true
	}
	inWindow := make(map[string]bool, len(runs))
	for i := range runs {
		runIDs = append(runIDs, runs[i].ID)
		inWindow[runs[i].ID] = true
	}
	reqSums, err := r.modelRequestSums(runIDs)
	if err != nil {
		return out, err
	}
	maintCosts, err := r.maintenanceCostsInWindow(botIDs, start, end)
	if err != nil {
		return out, err
	}
	// 维护成本按父 run 汇总，供任务归属使用。
	parentUSD := make(map[string]float64, len(maintCosts))
	for i := range maintCosts {
		m := &maintCosts[i]
		if m.CostUSD == nil {
			continue
		}
		out.BotUSD[m.BotID] += *m.CostUSD
		if m.ParentRunID != nil {
			parentUSD[*m.ParentRunID] += *m.CostUSD
		}
	}
	// schedule_event 主线 run 的任务归属：被引用的 schedule run 可能落在
	// 窗口外，按 ID 直查、不带时间窗。
	refIDs := make([]string, 0, len(runs))
	for i := range runs {
		if runs[i].SessionKind == model.BotSessionKindMain &&
			runs[i].TriggerSource == string(model.BotTriggerScheduleEvent) {
			if ref := botBudgetScheduleEventRunID(runs[i].TriggerKey); ref != "" {
				refIDs = append(refIDs, ref)
			}
		}
	}
	taskByRefRun, err := r.scheduleTaskIDsByRunIDs(refIDs)
	if err != nil {
		return out, err
	}
	for i := range runs {
		row := &runs[i]
		// 优先按请求行合计（含实际金额覆盖）；无请求行回退 run 快照值。
		var runUSD *float64
		if sum, ok := reqSums[row.ID]; ok {
			runUSD = sum
		} else {
			runUSD = row.CostEstimate
		}
		if runUSD != nil {
			out.BotUSD[row.BotID] += *runUSD
		}
		display := float64(0)
		if runUSD != nil {
			display += *runUSD
		}
		display += parentUSD[row.ID]
		if display == 0 {
			continue
		}
		switch {
		case row.SessionKind == model.BotSessionKindSchedule && row.ScheduleTaskID != nil:
			out.TaskUSD[*row.ScheduleTaskID] += display
		case row.SessionKind == model.BotSessionKindMain &&
			row.TriggerSource == string(model.BotTriggerScheduleEvent):
			if ref := botBudgetScheduleEventRunID(row.TriggerKey); ref != "" {
				if taskID, ok := taskByRefRun[ref]; ok {
					out.TaskUSD[taskID] += display
				}
			}
		}
	}
	// 父 run 在窗口外的维护：父集合直查不带窗口，归属按父的任务/连带主线
	// 口径补进 TaskUSD（父在窗口内的已由上面 parentUSD 消费，不重复）。
	var lateParentIDs []string
	for i := range maintCosts {
		m := &maintCosts[i]
		if m.ParentRunID == nil || inWindow[*m.ParentRunID] || parentUSD[*m.ParentRunID] == 0 {
			continue
		}
		lateParentIDs = append(lateParentIDs, *m.ParentRunID)
	}
	if len(lateParentIDs) > 0 {
		lateParents, err := r.runsByIDs(lateParentIDs)
		if err != nil {
			return out, err
		}
		refOf := make(map[string]string, len(lateParents))
		var lateRefIDs []string
		for i := range lateParents {
			p := &lateParents[i]
			if !botSet[p.BotID] || p.SessionKind != model.BotSessionKindMain ||
				p.TriggerSource != string(model.BotTriggerScheduleEvent) {
				continue
			}
			if ref := botBudgetScheduleEventRunID(p.TriggerKey); ref != "" {
				refOf[p.ID] = ref
				lateRefIDs = append(lateRefIDs, ref)
			}
		}
		lateTaskByRef, err := r.scheduleTaskIDsByRunIDs(lateRefIDs)
		if err != nil {
			return out, err
		}
		for i := range lateParents {
			p := &lateParents[i]
			display := parentUSD[p.ID]
			if display == 0 || !botSet[p.BotID] {
				continue
			}
			switch {
			case p.SessionKind == model.BotSessionKindSchedule:
				if p.ScheduleTaskID != nil {
					out.TaskUSD[*p.ScheduleTaskID] += display
				}
			case p.SessionKind == model.BotSessionKindMain &&
				p.TriggerSource == string(model.BotTriggerScheduleEvent):
				if taskID, ok := lateTaskByRef[refOf[p.ID]]; ok {
					out.TaskUSD[taskID] += display
				}
			}
		}
	}
	return out, nil
}

// runsByIDs 按 ID 取 run 金额行（不带窗口——给窗口内维护找窗口外父 run）。
func (r *BotBudgetRepository) runsByIDs(runIDs []string) ([]BotBudgetRunRow, error) {
	var rows []BotBudgetRunRow
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var part []BotBudgetRunRow
		err := r.db.Model(&model.BotRun{}).
			Select("id, bot_id, session_kind, trigger_source, trigger_key, schedule_task_id, cost_estimate").
			Where("id IN ?", batch).
			Scan(&part).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

// botBudgetScheduleEventRunID 解析 schedule_event 触发键里的被引用 schedule
// run ID（键形如 schedule_event:<runID>:<status>，runID 不含冒号）。口径与
// 花费页一致，这里自带一份以免依赖花费页的内部函数。
func botBudgetScheduleEventRunID(triggerKey string) string {
	rest, ok := strings.CutPrefix(triggerKey, "schedule_event:")
	if !ok {
		return ""
	}
	runID, _, _ := strings.Cut(rest, ":")
	return runID
}

// listRunsInWindow 返回机器人集合在窗口内的 run 金额行（升序稳定）。
func (r *BotBudgetRepository) listRunsInWindow(botIDs []string, start, end time.Time) ([]BotBudgetRunRow, error) {
	var rows []BotBudgetRunRow
	for _, batch := range ChunkIDs(botIDs, UsageQueryBatch) {
		var part []BotBudgetRunRow
		err := r.db.Model(&model.BotRun{}).
			Select("id, bot_id, session_kind, trigger_source, trigger_key, schedule_task_id, cost_estimate").
			Where("bot_id IN ?", batch).
			Where("created_at >= ? AND created_at < ?", start, end).
			Order("created_at ASC, id ASC").
			Scan(&part).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

// BotBudgetMaintenanceRow 是窗口内维护运行的金额行（成本已按维护请求合计，
// 全部未计价时为 nil）。
type BotBudgetMaintenanceRow struct {
	ID          string
	BotID       string
	ParentRunID *string
	CostUSD     *float64
}

// maintenanceCostsInWindow 汇总窗口内维护运行（按它们自己的 created_at 记
// 窗口）的已计价请求成本。
func (r *BotBudgetRepository) maintenanceCostsInWindow(botIDs []string, start, end time.Time) ([]BotBudgetMaintenanceRow, error) {
	var out []BotBudgetMaintenanceRow
	for _, batch := range ChunkIDs(botIDs, UsageQueryBatch) {
		var rows []BotBudgetMaintenanceRow
		err := r.db.Model(&model.BotMaintenanceRun{}).
			Select("id, bot_id, parent_run_id").
			Where("bot_id IN ?", batch).
			Where("created_at >= ? AND created_at < ?", start, end).
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 {
			continue
		}
		ids := make([]string, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].ID)
		}
		sums, err := r.maintenanceRequestSums(ids)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			rows[i].CostUSD = sums[rows[i].ID]
		}
		out = append(out, rows...)
	}
	return out, nil
}

// modelRequestSums 返回主线 run 的请求级成本合计（实际金额优先：
// COALESCE(provider_cost, cost_estimate)）；run 没有任何请求行时不出现。
func (r *BotBudgetRepository) modelRequestSums(runIDs []string) (map[string]*float64, error) {
	out := map[string]*float64{}
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var rows []struct {
			RunID   string   `gorm:"column:run_id"`
			CostSum *float64 `gorm:"column:cost_sum"`
		}
		err := r.db.Model(&model.BotModelRequest{}).
			Select("run_id, SUM(COALESCE(provider_cost, cost_estimate)) AS cost_sum").
			Where("run_id IN ?", batch).
			Group("run_id").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.RunID] = row.CostSum
		}
	}
	return out, nil
}

// maintenanceRequestSums 返回维护运行的已计价请求成本合计（实际金额优先
// 同口径）；全未计价不出现。
func (r *BotBudgetRepository) maintenanceRequestSums(maintRunIDs []string) (map[string]*float64, error) {
	out := map[string]*float64{}
	for _, batch := range ChunkIDs(maintRunIDs, UsageQueryBatch) {
		var rows []struct {
			MaintenanceRunID string   `gorm:"column:maintenance_run_id"`
			CostSum          *float64 `gorm:"column:cost_sum"`
		}
		err := r.db.Model(&model.BotMaintenanceRequest{}).
			Select("maintenance_run_id, SUM(COALESCE(provider_cost, cost_estimate)) AS cost_sum").
			Where("maintenance_run_id IN ?", batch).
			Group("maintenance_run_id").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.MaintenanceRunID] = row.CostSum
		}
	}
	return out, nil
}

// scheduleTaskIDsByRunIDs 批量反查 run 的定时任务归属（仅 schedule run）。
func (r *BotBudgetRepository) scheduleTaskIDsByRunIDs(runIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var rows []struct {
			ID             string  `gorm:"column:id"`
			ScheduleTaskID *string `gorm:"column:schedule_task_id"`
		}
		err := r.db.Model(&model.BotRun{}).
			Select("id, schedule_task_id").
			Where("id IN ? AND schedule_task_id IS NOT NULL", batch).
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.ScheduleTaskID != nil {
				out[row.ID] = *row.ScheduleTaskID
			}
		}
	}
	return out, nil
}

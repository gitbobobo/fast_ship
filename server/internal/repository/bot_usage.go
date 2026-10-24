package repository

import (
	"fmt"
	"sort"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// UsageQueryBatch 是花费页 IN 查询的分批大小（包级变量便于测试缩小）：SQLite
// 的 host 参数上限 32766，30 天窗口内的 run/请求 id 可能超过，所有 IN 查询
// 统一按批执行再合并，不改任何查询语义。
var UsageQueryBatch = 500

// ChunkIDs 把 ids 按 size 切成不超过 size 的批次（size < 1 视为 1）。
func ChunkIDs(ids []string, size int) [][]string {
	if size < 1 {
		size = 1
	}
	batches := make([][]string, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		batches = append(batches, ids[start:end])
	}
	return batches
}

// sortByCreatedThenID 按 (createdAt, id) 升序原地排序（分批查询各批内有序，
// 合并后在这里恢复整体排序，保持与单批执行相同的返回顺序）。
func sortByCreatedThenID[T any](rows []T, key func(*T) (time.Time, string)) {
	sort.Slice(rows, func(i, j int) bool {
		ct, cid := key(&rows[i])
		dt, did := key(&rows[j])
		if !ct.Equal(dt) {
			return ct.Before(dt)
		}
		return cid < did
	})
}

// BotUsageRepository 花费页（INT-162）的只读聚合查询：只用现有账本
// （bot_runs / bot_model_requests / bot_maintenance_* / bot_tool_calls）实时
// 聚合，不写任何列。全部查询按机器人集合与时间窗过滤（窗口最长 30 天），
// 扫描范围走 bot_schema.go 的 idx_bot_runs_bot_kind_created 与
// idx_bot_maintenance_runs_bot_created。
type BotUsageRepository struct {
	db *gorm.DB
}

func NewBotUsageRepository(db *gorm.DB) *BotUsageRepository {
	return &BotUsageRepository{db: db}
}

// UsageRunRow 是窗口内 run 的聚合扫描行：只取花费页用到的列，不搬运配置
// 快照等大字段。
type UsageRunRow struct {
	ID             string
	BotID          string
	SessionKind    string
	TriggerSource  string
	TriggerKey     string
	Status         string
	ScheduleTaskID *string
	CostEstimate   *float64
	CreatedAt      time.Time
}

// ListRunsInWindow 返回机器人集合在 [start, end) 内创建的 run（main 与
// schedule 都含），时间升序。
func (r *BotUsageRepository) ListRunsInWindow(botIDs []string, start, end time.Time) ([]UsageRunRow, error) {
	if len(botIDs) == 0 {
		return nil, nil
	}
	var rows []UsageRunRow
	for _, batch := range ChunkIDs(botIDs, UsageQueryBatch) {
		var part []UsageRunRow
		err := r.db.Model(&model.BotRun{}).
			Select("id, bot_id, session_kind, trigger_source, trigger_key, status, schedule_task_id, cost_estimate, created_at").
			Where("bot_id IN ?", batch).
			Where("created_at >= ? AND created_at < ?", start, end).
			Order("created_at ASC, id ASC").
			Scan(&part).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	sortByCreatedThenID(rows, func(row *UsageRunRow) (time.Time, string) { return row.CreatedAt, row.ID })
	return rows, nil
}

// ListRunsByIDs 按 ID 直查 run（花费页父 run 反查用）：关联维护的父 run 可能
// 在查询窗口之外，这里不带时间窗，供服务层判定归属桶。
func (r *BotUsageRepository) ListRunsByIDs(runIDs []string) (map[string]UsageRunRow, error) {
	out := map[string]UsageRunRow{}
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var rows []UsageRunRow
		err := r.db.Model(&model.BotRun{}).
			Select("id, bot_id, session_kind, trigger_source, trigger_key, status, schedule_task_id, cost_estimate, created_at").
			Where("id IN ?", batch).
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for i := range rows {
			out[rows[i].ID] = rows[i]
		}
	}
	return out, nil
}

// ScheduleTasksByIDs 现存任务信息（总览投影用；已删除任务取不到行）。
func (r *BotUsageRepository) ScheduleTasksByIDs(taskIDs []string) ([]model.BotScheduleTask, error) {
	var rows []model.BotScheduleTask
	for _, batch := range ChunkIDs(taskIDs, UsageQueryBatch) {
		var part []model.BotScheduleTask
		if err := r.db.Where("id IN ?", batch).Find(&part).Error; err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

// maintenanceUsageRow 是维护运行的聚合扫描行。
type MaintenanceUsageRow struct {
	ID          string
	BotID       string
	ParentRunID *string
	CreatedAt   time.Time
}

// ListMaintenanceRunsInWindow 返回机器人集合在 [start, end) 内创建的维护运行。
func (r *BotUsageRepository) ListMaintenanceRunsInWindow(botIDs []string, start, end time.Time) ([]MaintenanceUsageRow, error) {
	if len(botIDs) == 0 {
		return nil, nil
	}
	var rows []MaintenanceUsageRow
	for _, batch := range ChunkIDs(botIDs, UsageQueryBatch) {
		var part []MaintenanceUsageRow
		err := r.db.Model(&model.BotMaintenanceRun{}).
			Select("id, bot_id, parent_run_id, created_at").
			Where("bot_id IN ?", batch).
			Where("created_at >= ? AND created_at < ?", start, end).
			Order("created_at ASC, id ASC").
			Scan(&part).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	sortByCreatedThenID(rows, func(row *MaintenanceUsageRow) (time.Time, string) { return row.CreatedAt, row.ID })
	return rows, nil
}

// requestUsageAgg 是一组模型请求的计数与用量合计（按 run 或维护运行分组）。
type RequestUsageAgg struct {
	GroupID   string   `gorm:"column:group_id"`
	Total     int64    `gorm:"column:total"`
	Priced    int64    `gorm:"column:priced"`
	Unpriced  int64    `gorm:"column:unpriced"`
	CostSum   *float64 `gorm:"column:cost_sum"`
	CachedSum float64  `gorm:"column:cached_sum"`
	PromptSum float64  `gorm:"column:prompt_sum"`
}

// requestAggSelect 是请求聚合的公共投影：usage 完整的请求才进缓存命中统计，
// cost_estimate 非空才算已计价（缺失 ≠ 0）。usage 完整的判定分两张表：
// bot_maintenance_requests 有 usage_complete 列；bot_model_requests 没有，
// 沿用聊天页指标口径「落过 usage 计数列 = prompt_tokens IS NOT NULL」。
const requestAggSelect = "COALESCE(%[1]s, '') AS group_id, " +
	"COUNT(*) AS total, " +
	"SUM(CASE WHEN %[2]s.cost_estimate IS NOT NULL THEN 1 ELSE 0 END) AS priced, " +
	"SUM(CASE WHEN %[2]s.cost_estimate IS NULL THEN 1 ELSE 0 END) AS unpriced, " +
	"SUM(%[2]s.cost_estimate) AS cost_sum, " +
	"COALESCE(SUM(CASE WHEN %[3]s THEN COALESCE(%[2]s.cached_tokens, 0) ELSE 0 END), 0) AS cached_sum, " +
	"COALESCE(SUM(CASE WHEN %[3]s THEN COALESCE(%[2]s.prompt_tokens, 0) ELSE 0 END), 0) AS prompt_sum"

// RequestAggregatesByRun 按主线/定时 run 聚合模型请求计数与用量；runIDs 为空
// 返回空表。
func (r *BotUsageRepository) RequestAggregatesByRun(runIDs []string) (map[string]RequestUsageAgg, error) {
	out := map[string]RequestUsageAgg{}
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var rows []RequestUsageAgg
		err := r.db.Model(&model.BotModelRequest{}).
			Select(fmt.Sprintf(requestAggSelect, "run_id", "bot_model_requests", "bot_model_requests.prompt_tokens IS NOT NULL")).
			Where("run_id IN ?", batch).
			Group("run_id").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.GroupID] = row
		}
	}
	return out, nil
}

// MaintenanceRequestAggregatesByRun 按维护运行聚合其模型请求；形状与
// RequestAggregatesByRun 一致。
func (r *BotUsageRepository) MaintenanceRequestAggregatesByRun(maintRunIDs []string) (map[string]RequestUsageAgg, error) {
	out := map[string]RequestUsageAgg{}
	for _, batch := range ChunkIDs(maintRunIDs, UsageQueryBatch) {
		var rows []RequestUsageAgg
		err := r.db.Model(&model.BotMaintenanceRequest{}).
			Select(fmt.Sprintf(requestAggSelect, "maintenance_run_id", "bot_maintenance_requests", "bot_maintenance_requests.usage_complete")).
			Where("maintenance_run_id IN ?", batch).
			Group("maintenance_run_id").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.GroupID] = row
		}
	}
	return out, nil
}

// MaintenanceCostsByParentRun 关联维护已计价成本合计（parent_run_id →
// SUM(cost_estimate)）。值仅在有已计价请求行时非 nil——全部未计价与无维护
// 同样表示「无已计价」，供显示成本的空值合并（缺失 ≠ 0）。列表投影批量取
// 全也走这一条（当前页一次查询，不做逐 run N+1）。
func (r *BotUsageRepository) MaintenanceCostsByParentRun(runIDs []string) (map[string]*float64, error) {
	out := map[string]*float64{}
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var rows []struct {
			ParentRunID string   `gorm:"column:parent_run_id"`
			CostSum     *float64 `gorm:"column:cost_sum"`
		}
		err := r.db.Table("bot_maintenance_requests").
			Select("bot_maintenance_runs.parent_run_id AS parent_run_id, SUM(bot_maintenance_requests.cost_estimate) AS cost_sum").
			Joins("JOIN bot_maintenance_runs ON bot_maintenance_runs.id = bot_maintenance_requests.maintenance_run_id").
			Where("bot_maintenance_runs.parent_run_id IN ?", batch).
			Group("bot_maintenance_runs.parent_run_id").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.ParentRunID] = row.CostSum
		}
	}
	return out, nil
}

// ListScheduleRunsForTasksSince 返回某机器人指定任务自 since 起的 schedule run
// （任务近 7 天成本合计用）。
func (r *BotUsageRepository) ListScheduleRunsForTasksSince(botID string, taskIDs []string, since time.Time) ([]UsageRunRow, error) {
	if len(taskIDs) == 0 {
		return nil, nil
	}
	var rows []UsageRunRow
	for _, batch := range ChunkIDs(taskIDs, UsageQueryBatch) {
		var part []UsageRunRow
		err := r.db.Model(&model.BotRun{}).
			Select("id, schedule_task_id, cost_estimate, created_at").
			Where("bot_id = ? AND session_kind = ? AND schedule_task_id IN ? AND created_at >= ?",
				botID, model.BotSessionKindSchedule, batch, since).
			Order("created_at ASC, id ASC").
			Scan(&part).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	sortByCreatedThenID(rows, func(row *UsageRunRow) (time.Time, string) { return row.CreatedAt, row.ID })
	return rows, nil
}

// ListScheduleEventMainRunsSince 返回某机器人自 since 起由 schedule_event 唤醒
// 的主线 run（连带成本归集用；任务归属在服务层经 trigger_key 反查）。
func (r *BotUsageRepository) ListScheduleEventMainRunsSince(botID string, since time.Time) ([]UsageRunRow, error) {
	var rows []UsageRunRow
	err := r.db.Model(&model.BotRun{}).
		Select("id, trigger_key, cost_estimate, created_at").
		Where("bot_id = ? AND session_kind = ? AND trigger_source = ? AND created_at >= ?",
			botID, model.BotSessionKindMain, model.BotTriggerScheduleEvent, since).
		Order("created_at ASC, id ASC").
		Scan(&rows).Error
	return rows, err
}

// ScheduleTaskIDsByRunIDs 批量反查 run 的任务归属（schedule_event 主线 run 的
// trigger_key 里是被引用 schedule run 的 ID，它可能在本窗口之外，因此按 ID
// 直查、不带时间窗）。
func (r *BotUsageRepository) ScheduleTaskIDsByRunIDs(runIDs []string) (map[string]string, error) {
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

// ---------- 单次运行花费明细（GET /api/bots/usage/runs/:runId） ----------

// ListRequestsByRun 返回 run 的模型请求（含失败重试），按 ordinal 正序，
// 至多 limit 条——多取一行判截断由调用方负责（传 limit+1）。
func (r *BotUsageRepository) ListRequestsByRun(runID string, limit int) ([]model.BotModelRequest, error) {
	var rows []model.BotModelRequest
	err := r.db.Where("run_id = ?", runID).
		Order("ordinal ASC").Limit(limit).Find(&rows).Error
	return rows, err
}

// ListToolCallsByRequestIDs 返回一批模型请求的工具调用（请求侧按 created_at
// 稳定排序，分组在服务层做）。
func (r *BotUsageRepository) ListToolCallsByRequestIDs(requestIDs []string) ([]model.BotToolCall, error) {
	if len(requestIDs) == 0 {
		return nil, nil
	}
	var rows []model.BotToolCall
	for _, batch := range ChunkIDs(requestIDs, UsageQueryBatch) {
		var part []model.BotToolCall
		// 明细投影只需展示列；arguments_json/result_json 可为瘦身前大文本，
		// 非终态明细每 5s 轮询，整行拉取会让响应体积无界。
		err := r.db.Select("id", "request_id", "tool_name", "action", "status",
			"error_code", "created_at", "finished_at").
			Where("request_id IN ?", batch).
			Order("created_at ASC, id ASC").Find(&part).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	sortByCreatedThenID(rows, func(row *model.BotToolCall) (time.Time, string) { return row.CreatedAt, row.ID })
	return rows, nil
}

// ListMaintenanceRunsByParent 返回 parent_run_id 指向本 run 的维护运行。
func (r *BotUsageRepository) ListMaintenanceRunsByParent(parentRunID string) ([]model.BotMaintenanceRun, error) {
	var rows []model.BotMaintenanceRun
	err := r.db.Where("parent_run_id = ?", parentRunID).
		Order("created_at ASC, id ASC").Find(&rows).Error
	return rows, err
}

// ListMaintenanceRequests 返回维护运行的模型请求，按 ordinal 正序。
func (r *BotUsageRepository) ListMaintenanceRequests(maintRunIDs []string) ([]model.BotMaintenanceRequest, error) {
	if len(maintRunIDs) == 0 {
		return nil, nil
	}
	var rows []model.BotMaintenanceRequest
	for _, batch := range ChunkIDs(maintRunIDs, UsageQueryBatch) {
		var part []model.BotMaintenanceRequest
		err := r.db.Where("maintenance_run_id IN ?", batch).
			Order("maintenance_run_id ASC, ordinal ASC").Find(&part).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	// 各批批内有序，合并后恢复整体 (maintenance_run_id, ordinal) 排序。
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].MaintenanceRunID != rows[j].MaintenanceRunID {
			return rows[i].MaintenanceRunID < rows[j].MaintenanceRunID
		}
		return rows[i].Ordinal < rows[j].Ordinal
	})
	return rows, nil
}

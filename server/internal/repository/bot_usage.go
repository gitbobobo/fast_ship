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

// UsageRunRow 是窗口内 run 的聚合扫描行：只取花费页用到的列。INT-163 起带
// silent（空转条件一）。config_snapshot_json 单行可达数 KB 且总览不消费，
// 不随这两条扫描回读（30 天窗口会把整列快照读回内存）；换模型差价的快照
// 归属由 insights 经 RunConfigSnapshotsByIDs 按 runIDs 单独补查。
type UsageRunRow struct {
	ID             string
	BotID          string
	SessionKind    string
	TriggerSource  string
	TriggerKey     string
	Status         string
	ScheduleTaskID *string
	CostEstimate   *float64
	Silent         bool
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
			Select("id, bot_id, session_kind, trigger_source, trigger_key, status, schedule_task_id, cost_estimate, silent, created_at").
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
			Select("id, bot_id, session_kind, trigger_source, trigger_key, status, schedule_task_id, cost_estimate, silent, created_at").
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

// RequestUsageAgg 是一组模型请求的计数与用量合计（按 run 或维护运行分组）：
// INT-162 的计数/成本口径与 INT-163 的 token 口径合成同一次聚合，总览与优化
// 参考共用一份结果。usage 完整的请求才进缓存命中与 token 合计，cost_estimate
// 非空才算已计价（缺失 ≠ 0）；cached 逐行按 MIN(cached_tokens, prompt_tokens)
// 钳制（与 botCostEstimate 的异常钳制同口径，cached>prompt 的异常行不会把
// 命中率推过 1）；priced_* 只统计已计价行的 token 合计，供换模型差价用同一
// 分母（已计价必以 usage 完整为前提）。
type RequestUsageAgg struct {
	GroupID             string   `gorm:"column:group_id"`
	Total               int64    `gorm:"column:total"`
	Priced              int64    `gorm:"column:priced"`
	Unpriced            int64    `gorm:"column:unpriced"`
	CostSum             *float64 `gorm:"column:cost_sum"`
	PromptSum           int64    `gorm:"column:prompt_sum"`
	CachedSum           int64    `gorm:"column:cached_sum"`
	CompletionSum       int64    `gorm:"column:completion_sum"`
	ToolsSum            int64    `gorm:"column:tools_sum"`
	PricedPromptSum     int64    `gorm:"column:priced_prompt_sum"`
	PricedCachedSum     int64    `gorm:"column:priced_cached_sum"`
	PricedCompletionSum int64    `gorm:"column:priced_completion_sum"`
}

// requestAggSelect 是请求聚合的公共投影：%1 = 分组列、%2 = 表名、
// %3 = usage 完整判定表达式、%4 = tools 合计表达式（bot_maintenance_requests
// 无工具定义列，传 0）。usage 完整的判定分两张表：bot_maintenance_requests
// 有 usage_complete 列；bot_model_requests 没有，沿用聊天页指标口径「落过
// usage 计数列 = prompt_tokens IS NOT NULL」。cached 的两处（usage 完整合计
// 与已计价合计）都逐行 MIN(cached_tokens, prompt_tokens) 钳制。
const requestAggSelect = "COALESCE(%[1]s, '') AS group_id, " +
	"COUNT(*) AS total, " +
	"SUM(CASE WHEN %[2]s.cost_estimate IS NOT NULL THEN 1 ELSE 0 END) AS priced, " +
	"SUM(CASE WHEN %[2]s.cost_estimate IS NULL THEN 1 ELSE 0 END) AS unpriced, " +
	"SUM(%[2]s.cost_estimate) AS cost_sum, " +
	"COALESCE(SUM(CASE WHEN %[3]s THEN COALESCE(%[2]s.prompt_tokens, 0) ELSE 0 END), 0) AS prompt_sum, " +
	"COALESCE(SUM(CASE WHEN %[3]s THEN MIN(COALESCE(%[2]s.cached_tokens, 0), COALESCE(%[2]s.prompt_tokens, 0)) ELSE 0 END), 0) AS cached_sum, " +
	"COALESCE(SUM(CASE WHEN %[3]s THEN COALESCE(%[2]s.completion_tokens, 0) ELSE 0 END), 0) AS completion_sum, " +
	"%[4]s AS tools_sum, " +
	"COALESCE(SUM(CASE WHEN %[2]s.cost_estimate IS NOT NULL THEN COALESCE(%[2]s.prompt_tokens, 0) ELSE 0 END), 0) AS priced_prompt_sum, " +
	"COALESCE(SUM(CASE WHEN %[2]s.cost_estimate IS NOT NULL THEN MIN(COALESCE(%[2]s.cached_tokens, 0), COALESCE(%[2]s.prompt_tokens, 0)) ELSE 0 END), 0) AS priced_cached_sum, " +
	"COALESCE(SUM(CASE WHEN %[2]s.cost_estimate IS NOT NULL THEN COALESCE(%[2]s.completion_tokens, 0) ELSE 0 END), 0) AS priced_completion_sum"

// RequestAggregatesByRun 按主线/定时 run 聚合模型请求计数、成本与用量；
// runIDs 为空返回空表。总览与优化参考（INT-163）共用这一份聚合。
func (r *BotUsageRepository) RequestAggregatesByRun(runIDs []string) (map[string]RequestUsageAgg, error) {
	out := map[string]RequestUsageAgg{}
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var rows []RequestUsageAgg
		err := r.db.Model(&model.BotModelRequest{}).
			Select(fmt.Sprintf(requestAggSelect, "run_id", "bot_model_requests",
				"bot_model_requests.prompt_tokens IS NOT NULL",
				"COALESCE(SUM(CASE WHEN bot_model_requests.prompt_tokens IS NOT NULL THEN bot_model_requests.tools_tokens_estimate ELSE 0 END), 0)")).
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

// MaintenanceRequestAggregatesByRun 按维护运行聚合其模型请求；投影与
// RequestAggregatesByRun 一致（维护请求无工具定义列，tools_sum 恒 0）。
func (r *BotUsageRepository) MaintenanceRequestAggregatesByRun(maintRunIDs []string) (map[string]RequestUsageAgg, error) {
	out := map[string]RequestUsageAgg{}
	for _, batch := range ChunkIDs(maintRunIDs, UsageQueryBatch) {
		var rows []RequestUsageAgg
		err := r.db.Model(&model.BotMaintenanceRequest{}).
			Select(fmt.Sprintf(requestAggSelect, "maintenance_run_id", "bot_maintenance_requests",
				"bot_maintenance_requests.usage_complete", "0")).
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
// 至多 limit 条。截断时保留**最新**的请求（先 ordinal DESC 取再反转）——
// 明细页轮询关注的正是最新活动，保留最早段会让新请求永远不可见；多取
// 一行判截断由调用方负责（传 limit+1）。
func (r *BotUsageRepository) ListRequestsByRun(runID string, limit int) ([]model.BotModelRequest, error) {
	var rows []model.BotModelRequest
	err := r.db.Where("run_id = ?", runID).
		Order("ordinal DESC").Limit(limit).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	return rows, nil
}

// RequestHashesBeforeOrdinal 对给定非空 request_hash 集合做存在性查询，返回
// 其中在同 run 内更小 ordinal 的更早请求里出现过的子集：明细截断只保留最新
// 一段，被截掉的更早行不在内存里，is_retry 的「同 run 更早同 hash」口径由
// 这次查询补齐。保留行至多 usageRunDetailMaxRequests 条，hash 集合不会逼近
// IN 参数上限，单次查询即可。
func (r *BotUsageRepository) RequestHashesBeforeOrdinal(runID string, hashes []string, before int64) (map[string]bool, error) {
	out := make(map[string]bool, len(hashes))
	if len(hashes) == 0 {
		return out, nil
	}
	var rows []struct {
		RequestHash string `gorm:"column:request_hash"`
	}
	err := r.db.Model(&model.BotModelRequest{}).
		Select("DISTINCT request_hash").
		Where("run_id = ? AND ordinal < ? AND request_hash IN ?", runID, before, hashes).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		out[row.RequestHash] = true
	}
	return out, nil
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

// ---------- 成本优化参考（INT-163：GET /api/bots/usage/insights） ----------

// RunConfigSnapshotsByIDs 按 runIDs 批量补查 run 冻结快照（换模型差价分组的
// 目录/模型归属）：快照列单行可达数 KB，总览不消费它，不随共享账本扫描回读，
// 只有 insights 按需取这一列。
func (r *BotUsageRepository) RunConfigSnapshotsByIDs(runIDs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var rows []struct {
			ID                 string `gorm:"column:id"`
			ConfigSnapshotJSON string `gorm:"column:config_snapshot_json"`
		}
		err := r.db.Model(&model.BotRun{}).
			Select("id, config_snapshot_json").
			Where("id IN ?", batch).
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.ID] = row.ConfigSnapshotJSON
		}
	}
	return out, nil
}

// FirstRequestInputRow 是 run 首次请求的输入 token 扫描行（context_growth 用）：
// usage 完整取 prompt_tokens，缺 usage 回退发送前的 input_tokens_estimate 并标
// estimated；估算列 NOT NULL default 0 分不出「未写估算」与真 0，prompt 与
// 估算皆无的首请求不产出点（该 run 在 context_growth 里缺席）。
type FirstRequestInputRow struct {
	RunID       string `gorm:"column:run_id"`
	InputTokens int64  `gorm:"column:input_tokens"`
	Estimated   bool   `gorm:"column:estimated"`
}

// FirstRequestInputByRun 返回每个 run ordinal 最小的那次请求的输入 token
// （窗口内每个有可用输入的 run 一行）。子查询先求各 run 的 MIN(ordinal)，
// 不把 run 的全部请求行拉回来；回退仅在估算 > 0 时发生——WHERE 过滤掉
// prompt 与估算皆无的首请求，不出 input_tokens=0、estimated=true 的点。
func (r *BotUsageRepository) FirstRequestInputByRun(runIDs []string) (map[string]FirstRequestInputRow, error) {
	out := map[string]FirstRequestInputRow{}
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var rows []FirstRequestInputRow
		err := r.db.Model(&model.BotModelRequest{}).
			Select("bot_model_requests.run_id AS run_id, "+
				"COALESCE(bot_model_requests.prompt_tokens, bot_model_requests.input_tokens_estimate) AS input_tokens, "+
				"(bot_model_requests.prompt_tokens IS NULL) AS estimated").
			Joins("JOIN (SELECT run_id, MIN(ordinal) AS ordinal FROM bot_model_requests WHERE run_id IN ? GROUP BY run_id) first "+
				"ON first.run_id = bot_model_requests.run_id AND first.ordinal = bot_model_requests.ordinal", batch).
			Where("bot_model_requests.prompt_tokens IS NOT NULL OR bot_model_requests.input_tokens_estimate > 0").
			// ordinal 无唯一约束：同一 run 并列最小 ordinal 时按 id 升序，
			// 后写覆盖先写，out 里确定性留下最大 id 那行，消除取值抖动。
			Order("bot_model_requests.id").
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.RunID] = row
		}
	}
	return out, nil
}

// RetryCountRow 是 run 的重试行数（retried_requests 用）：同 run 内与更早请求
// request_hash 相同的行数，每次重复尝试计 1（含最终成功那次）；空 hash 不参与。
type RetryCountRow struct {
	RunID   string `gorm:"column:run_id"`
	Retries int64  `gorm:"column:retries"`
}

// RetryCountsByRun 按 run 统计重复 request_hash 的重试行数。
func (r *BotUsageRepository) RetryCountsByRun(runIDs []string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var rows []RetryCountRow
		err := r.db.Raw("SELECT run_id, SUM(cnt - 1) AS retries FROM ("+
			"SELECT run_id, request_hash, COUNT(*) AS cnt FROM bot_model_requests "+
			"WHERE run_id IN ? AND request_hash != '' GROUP BY run_id, request_hash"+
			") GROUP BY run_id", batch).Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			out[row.RunID] = row.Retries
		}
	}
	return out, nil
}

// FailedRequestAggRow 是失败请求按 (run, error_code) 的聚合行（failures 用）：
// error_code 为 NULL 的失败归到 null 一组（扫描为 nil 指针）；cost_sum 只含
// 已计价行，全未计价为 nil（缺失 ≠ 0）。
type FailedRequestAggRow struct {
	RunID     string   `gorm:"column:run_id"`
	ErrorCode *string  `gorm:"column:error_code"`
	Requests  int64    `gorm:"column:requests"`
	CostSum   *float64 `gorm:"column:cost_sum"`
}

// FailedRequestAggsByRun 聚合窗口内 run 的 status=failed 请求行。
func (r *BotUsageRepository) FailedRequestAggsByRun(runIDs []string) ([]FailedRequestAggRow, error) {
	var rows []FailedRequestAggRow
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var part []FailedRequestAggRow
		err := r.db.Model(&model.BotModelRequest{}).
			Select("run_id, error_code, COUNT(*) AS requests, SUM(cost_estimate) AS cost_sum").
			Where("run_id IN ? AND status = ?", batch, model.BotModelRequestFailed).
			Group("run_id, error_code").
			Scan(&part).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

// MaintenanceRequestDetailRow 是维护请求的明细扫描行（INT-163）：用途拆分、
// 失败统计与按维护运行记日的缓存命中都在服务层归并，这里只按维护运行分批
// 拉回需要的列。
type MaintenanceRequestDetailRow struct {
	MaintenanceRunID string   `gorm:"column:maintenance_run_id"`
	Purpose          string   `gorm:"column:purpose"`
	Status           string   `gorm:"column:status"`
	UsageComplete    bool     `gorm:"column:usage_complete"`
	PromptTokens     *int64   `gorm:"column:prompt_tokens"`
	CachedTokens     *int64   `gorm:"column:cached_tokens"`
	CostEstimate     *float64 `gorm:"column:cost_estimate"`
	ErrorCode        *string  `gorm:"column:error_code"`
}

// ListMaintenanceRequestRows 扫描维护运行的请求明细（无排序要求，消费方按
// 维护运行归并）。
func (r *BotUsageRepository) ListMaintenanceRequestRows(maintRunIDs []string) ([]MaintenanceRequestDetailRow, error) {
	var rows []MaintenanceRequestDetailRow
	for _, batch := range ChunkIDs(maintRunIDs, UsageQueryBatch) {
		var part []MaintenanceRequestDetailRow
		err := r.db.Model(&model.BotMaintenanceRequest{}).
			Select("maintenance_run_id, purpose, status, usage_complete, prompt_tokens, cached_tokens, cost_estimate, error_code").
			Where("maintenance_run_id IN ?", batch).
			Scan(&part).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

// RunToolCallTriple 是 run 名下出现过的 (tool_name, action) 去重组合（空转判定
// 用）：读/写判定的事实源 botReadOnlyActions 是服务层的 Go 映射，无法下推成
// SQL 条件，取去重组合回内存过滤，行数远小于调用行总数。
type RunToolCallTriple struct {
	RunID    string `gorm:"column:run_id"`
	ToolName string `gorm:"column:tool_name"`
	Action   string `gorm:"column:action"`
}

// RunToolCallTriples 返回一批 run 经 bot_model_requests 关联的工具调用去重
// 组合（调用行各状态都计，不做状态过滤）。
func (r *BotUsageRepository) RunToolCallTriples(runIDs []string) ([]RunToolCallTriple, error) {
	var rows []RunToolCallTriple
	for _, batch := range ChunkIDs(runIDs, UsageQueryBatch) {
		var part []RunToolCallTriple
		err := r.db.Table("bot_tool_calls").
			Select("DISTINCT bot_model_requests.run_id AS run_id, bot_tool_calls.tool_name AS tool_name, bot_tool_calls.action AS action").
			Joins("JOIN bot_model_requests ON bot_model_requests.id = bot_tool_calls.request_id").
			Where("bot_model_requests.run_id IN ?", batch).
			Scan(&part).Error
		if err != nil {
			return nil, err
		}
		rows = append(rows, part...)
	}
	return rows, nil
}

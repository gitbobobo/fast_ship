package service

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"gorm.io/gorm"
)

// 花费页（INT-162）：只用现有账本（bot_runs / bot_model_requests /
// bot_maintenance_*）实时聚合每个机器人每天、每个定时任务的花费，不新增表、
// 不回填。口径与 api/openapi.yaml 的 BotUsage* schema 一致：
//
//   - 切日时区 = 查询用户的有效时区（botUserTimezoneOf 的回退链）；
//   - 金额全部来自目录单价估算：cost_estimate IS NULL 即「未计价」，缺失 ≠ 0，
//     所有已计价金额字段在没有任何已计价成分时输出 null；
//   - 归属：schedule run 按 schedule_task_id 归任务；schedule_event 唤醒的
//     主线 run 经 trigger_key 反查任务计入 linked_main_cost（近似），解析不出
//     计入主线；维护请求经 parent_run_id 计入所属 run 的显示成本，桶跟父 run
//     走（主线/定时/连带主线），金额按维护运行自己的 created_at 记日（跨日不
//     随父 run 或查询窗口漂移）；无父 run 或父 run 行不存在（如夜间整理）才
//     计入机器人维护项；
//   - 显示成本 = run 自身已计价成本 + 关联维护已计价成本，不改写账本列。
//
// 全部查询按机器人集合与时间窗过滤（窗口最长 30 个当地日），扫描走
// idx_bot_runs_bot_kind_created / idx_bot_maintenance_runs_bot_created。

const (
	botUsageRangeToday = "today"
	botUsageRange7d    = "7d"
	botUsageRange30d   = "30d"
)

// botUsageRangeDays 把 range 参数映射成窗口天数（含今天的当地日数）。
func botUsageRangeDays(r string) (int, bool) {
	switch r {
	case botUsageRangeToday:
		return 1, true
	case botUsageRange7d:
		return 7, true
	case botUsageRange30d:
		return 30, true
	}
	return 0, false
}

// botUsageDayStart 某时刻在 loc 时区的当地零点（UTC 表示）。
func botUsageDayStart(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc).UTC()
}

// botUsageWindow 计算 [start, now) 窗口与窗口内按时间升序的当地日列表。
func botUsageWindow(days int, loc *time.Location, now time.Time) (time.Time, []string) {
	startLocal := botUsageDayStart(now, loc).In(loc).AddDate(0, 0, -(days - 1))
	start := startLocal.UTC()
	dates := make([]string, 0, days)
	for i := 0; i < days; i++ {
		dates = append(dates, startLocal.AddDate(0, 0, i).Format("2006-01-02"))
	}
	return start, dates
}

// botSumOptional 空值合并加法：任一非空即非空，全空保持 nil（缺失 ≠ 0）。
func botSumOptional(a, b *float64) *float64 {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	}
	sum := *a + *b
	return &sum
}

// botScheduleEventRunIDOf 解析 schedule_event 触发键里的被引用 schedule run
// ID。键形如 schedule_event:<runID>:<status>（wakeScheduleEvent 生成），runID
// 为 UUID 不含冒号。
func botScheduleEventRunIDOf(triggerKey string) string {
	rest, ok := strings.CutPrefix(triggerKey, "schedule_event:")
	if !ok {
		return ""
	}
	runID, _, _ := strings.Cut(rest, ":")
	return runID
}

// botUsageMonthlyTriggerCount 统计 cron 在未来 30 天的触发次数。解析走与任务
// 创建/调度同一入口 parseScheduleCron（TZ= 前缀拒绝、独立时区字段），失败返回
// false（估算输出 null）。
func botUsageMonthlyTriggerCount(expression, timezone string, now time.Time) (int64, bool) {
	loc, first, err := parseScheduleCron(expression, timezone, now)
	if err != nil {
		return 0, false
	}
	// parseScheduleCron 只回首次触发，迭代仍需 schedule 对象；用同一解析器
	// 重放一次（此时必已通过，失败兜底返回 false）。解析器不允许表达式内
	// TZ=，schedule.Location 恒为 time.Local，Next 按 t 的时区求值。
	schedule, err := cronScheduleParser.Parse(strings.TrimSpace(expression))
	if err != nil {
		return 0, false
	}
	// 「未来 30 天」是任务时区的 30 个当地日：必须在 loc 里 AddDate，否则
	// UTC 时刻 +30 天等价于固定 720h，跨夏令时切换会漏掉边界日内的触发。
	horizon := now.In(loc).AddDate(0, 0, 30)
	t := first.In(loc)
	var n int64
	for !t.IsZero() && !t.After(horizon) {
		n++
		t = schedule.Next(t)
	}
	return n, true
}

// usageRunDetailMaxRequests 单次运行花费明细返回的请求上限：超出截断并置
// requests_truncated，防止极端长跑 run 的明细响应体积无界。
const usageRunDetailMaxRequests = 500

// usageEstimateStat 是任务月推算的固定窗口统计：最近 30 个当地日内
// schedule run 的单次成本（自身 + 关联维护）与其连带主线摊销。
type usageEstimateStat struct {
	runs       int64
	priced     int64
	displaySum *float64
	linked     *float64
}

// botUsageEstimateStats 按固定窗口统计启用任务的单次触发成本，独立于所选
// range——切到「今天」无已计价 run 时不该让月推算消失或在 today/7d/30d
// 间漂移。窗口起止与总览 30d 同规则（当地日）；父在窗口内的维护必落在
// 窗内（维护 created_at 不早于父 run），故按父归集取全量即可。
func (s *BotService) botUsageEstimateStats(
	tasks map[string]model.BotScheduleTask, loc *time.Location, now time.Time,
) (map[string]*usageEstimateStat, error) {
	out := map[string]*usageEstimateStat{}
	byBot := map[string][]string{}
	for id, t := range tasks {
		if t.Enabled {
			byBot[t.BotID] = append(byBot[t.BotID], id)
		}
	}
	if len(byBot) == 0 {
		return out, nil
	}
	start, _ := botUsageWindow(30, loc, now)
	repo := repository.NewBotUsageRepository(s.db)
	for botID, taskIDs := range byBot {
		inSet := make(map[string]bool, len(taskIDs))
		for _, id := range taskIDs {
			inSet[id] = true
			out[id] = &usageEstimateStat{}
		}
		schedRuns, err := repo.ListScheduleRunsForTasksSince(botID, taskIDs, start)
		if err != nil {
			return nil, err
		}
		eventMains, err := repo.ListScheduleEventMainRunsSince(botID, start)
		if err != nil {
			return nil, err
		}
		parentIDs := make([]string, 0, len(schedRuns)+len(eventMains))
		refIDs := make([]string, 0, len(eventMains))
		for i := range schedRuns {
			parentIDs = append(parentIDs, schedRuns[i].ID)
		}
		for i := range eventMains {
			parentIDs = append(parentIDs, eventMains[i].ID)
			if ref := botScheduleEventRunIDOf(eventMains[i].TriggerKey); ref != "" {
				refIDs = append(refIDs, ref)
			}
		}
		maintByParent, err := repo.MaintenanceCostsByParentRun(parentIDs)
		if err != nil {
			return nil, err
		}
		taskByRefRun, err := repo.ScheduleTaskIDsByRunIDs(refIDs)
		if err != nil {
			return nil, err
		}
		for i := range schedRuns {
			row := &schedRuns[i]
			if row.ScheduleTaskID == nil || !inSet[*row.ScheduleTaskID] {
				continue
			}
			acc := out[*row.ScheduleTaskID]
			acc.runs++
			if disp := botSumOptional(row.CostEstimate, maintByParent[row.ID]); disp != nil {
				acc.priced++
				acc.displaySum = botSumOptional(acc.displaySum, disp)
			}
		}
		for i := range eventMains {
			row := &eventMains[i]
			ref := botScheduleEventRunIDOf(row.TriggerKey)
			if ref == "" {
				continue
			}
			taskID, ok := taskByRefRun[ref]
			if !ok || !inSet[taskID] {
				continue
			}
			acc := out[taskID]
			acc.linked = botSumOptional(acc.linked,
				botSumOptional(row.CostEstimate, maintByParent[row.ID]))
		}
	}
	return out, nil
}

// botUsageCostLess 按已计价金额降序的比较谓词：nil（无已计价）永远排最后。
func botUsageCostLess(a, b *float64) bool {
	switch {
	case a == nil && b == nil:
		return false
	case a == nil:
		return false
	case b == nil:
		return true
	}
	return *a > *b
}

// ---------- 总览：GET /api/bots/usage ----------

func (s *BotService) GetBotUsageOverview(userID, rangeParam, botID string) (*api.BotUsageOverview, error) {
	days, ok := botUsageRangeDays(rangeParam)
	if !ok {
		return nil, errs.New(errs.ErrInvalidParams.Code, "range 必须是 today、7d 或 30d")
	}
	// 一切聚合先按用户取机器人集合；bot_id 参数须属于当前用户。
	bots, err := s.bots.ListByUser(userID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	botByID := make(map[string]model.Bot, len(bots))
	for _, b := range bots {
		botByID[b.ID] = b
	}
	scoped := bots
	if botID != "" {
		b, ok := botByID[botID]
		if !ok {
			return nil, errs.ErrBotNotFound
		}
		scoped = []model.Bot{b}
	}
	botIDs := make([]string, 0, len(scoped))
	botNames := make(map[string]string, len(scoped))
	for _, b := range scoped {
		botIDs = append(botIDs, b.ID)
		botNames[b.ID] = b.Name
	}

	now := time.Now().UTC()
	tzName, loc := s.botUserTimezoneOf(userID)
	start, dates := botUsageWindow(days, loc, now)

	repo := repository.NewBotUsageRepository(s.db)
	runRows, err := repo.ListRunsInWindow(botIDs, start, now)
	if err != nil {
		return nil, errs.ErrInternal
	}
	maintRows, err := repo.ListMaintenanceRunsInWindow(botIDs, start, now)
	if err != nil {
		return nil, errs.ErrInternal
	}
	runIDs := make([]string, 0, len(runRows))
	runByID := make(map[string]*repository.UsageRunRow, len(runRows))
	for i := range runRows {
		runIDs = append(runIDs, runRows[i].ID)
		runByID[runRows[i].ID] = &runRows[i]
	}
	maintRunIDs := make([]string, 0, len(maintRows))
	for _, m := range maintRows {
		maintRunIDs = append(maintRunIDs, m.ID)
	}

	// 关联维护成本：窗口内 run 的 parent_run_id 汇总（一次查询，非逐 run）。
	maintByParent, err := repo.MaintenanceCostsByParentRun(runIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	runAgg, err := repo.RequestAggregatesByRun(runIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	maintAgg, err := repo.MaintenanceRequestAggregatesByRun(maintRunIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}

	// 父 run 反查（不受窗口限制）：有父 run 的维护金额按维护自己的 created_at
	// 记日、桶跟父 run 走；父 run 不在窗口内时同样要解析它的归属。
	parentIDs := make([]string, 0, len(maintRows))
	for _, m := range maintRows {
		if m.ParentRunID != nil {
			parentIDs = append(parentIDs, *m.ParentRunID)
		}
	}
	parentByID, err := repo.ListRunsByIDs(parentIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}

	// schedule_event 主线 run 的任务反查：被引用的 schedule run 可能在窗口外，
	// 按 ID 直查。窗口内 event run 与窗口外的父 event run 一起解析。
	eventTaskOf := map[string]string{}
	for i := range runRows {
		row := &runRows[i]
		if row.SessionKind == model.BotSessionKindMain && row.TriggerSource == string(model.BotTriggerScheduleEvent) {
			if ref := botScheduleEventRunIDOf(row.TriggerKey); ref != "" {
				eventTaskOf[row.ID] = ref
			}
		}
	}
	for _, row := range parentByID {
		if row.SessionKind == model.BotSessionKindMain && row.TriggerSource == string(model.BotTriggerScheduleEvent) {
			if ref := botScheduleEventRunIDOf(row.TriggerKey); ref != "" {
				eventTaskOf[row.ID] = ref
			}
		}
	}
	refIDs := make([]string, 0, len(eventTaskOf))
	for _, ref := range eventTaskOf {
		refIDs = append(refIDs, ref)
	}
	taskByRefRun, err := repo.ScheduleTaskIDsByRunIDs(refIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}

	// ---------- 累加器 ----------
	type botAcc struct {
		name            string
		costMain        *float64
		costSchedule    *float64
		costLinked      *float64
		costMaintenance *float64
		runs            int64
		displayCosts    []float64
		totalReq        int64
		pricedReq       int64
		cachedSum       float64
		promptSum       float64
	}
	type dayAcc struct {
		cost    float64
		hasReq  map[string]bool
		botCost map[string]float64
	}
	type taskAcc struct {
		botID        string
		runs         int64
		failed       int64
		skipped      int64
		cost         *float64
		displayCosts []float64
		linked       *float64
	}

	botAccs := make(map[string]*botAcc, len(botIDs))
	for id, name := range botNames {
		botAccs[id] = &botAcc{name: name}
	}
	dayAccs := make([]*dayAcc, len(dates))
	dayIndex := make(map[string]int, len(dates))
	for i, d := range dates {
		dayAccs[i] = &dayAcc{hasReq: map[string]bool{}, botCost: map[string]float64{}}
		dayIndex[d] = i
	}
	taskAccs := map[string]*taskAcc{}

	// dayOf 把时刻映射到窗口内的日累计器；窗口外（理论不可达）返回 nil。
	dayOf := func(t time.Time) *dayAcc {
		if idx, ok := dayIndex[t.In(loc).Format("2006-01-02")]; ok {
			return dayAccs[idx]
		}
		return nil
	}

	// applyRunBucket 把一笔已计价金额按 run 的归属桶累计：schedule run → 定时
	// 桶 + 对应任务；schedule_event 连带主线 → 解析出任务时进定时桶 + 连带近似
	// + 任务连带，否则留主线桶；其余主线 → 主线桶。窗口外父 run 名下的维护
	// 金额复用同一归属（桶跟父走）；run 计数与单次成本统计不在这里。
	applyRunBucket := func(row *repository.UsageRunRow, amount float64) {
		acc := botAccs[row.BotID]
		if acc == nil {
			return
		}
		switch {
		case row.SessionKind == model.BotSessionKindSchedule:
			acc.costSchedule = botSumOptional(acc.costSchedule, &amount)
			if row.ScheduleTaskID != nil {
				task := taskAccs[*row.ScheduleTaskID]
				if task == nil {
					task = &taskAcc{botID: row.BotID}
					taskAccs[*row.ScheduleTaskID] = task
				}
				task.cost = botSumOptional(task.cost, &amount)
			}
		case row.TriggerSource == string(model.BotTriggerScheduleEvent):
			// 连带主线成本（近似）：解析得出任务归属计入该任务（也进机器人
			// 定时桶），否则整条留在主线桶。
			bucket := &acc.costMain
			if taskID, resolved := taskByRefRun[eventTaskOf[row.ID]]; resolved {
				task := taskAccs[taskID]
				if task == nil {
					task = &taskAcc{botID: row.BotID}
					taskAccs[taskID] = task
				}
				task.linked = botSumOptional(task.linked, &amount)
				bucket = &acc.costSchedule
				acc.costLinked = botSumOptional(acc.costLinked, &amount)
			}
			*bucket = botSumOptional(*bucket, &amount)
		default:
			acc.costMain = botSumOptional(acc.costMain, &amount)
		}
	}

	// 窗口内 run 的桶金额只吸收窗口内维护：维护按自身发生日记「按天」，
	// 窗口外维护若随父显示成本进桶，三桶之和就会大于 Σ按天、用户对不上账。
	// run 的显示成本与单次统计仍用全量关联维护（maintByParent 无窗口下限）
	// ——那是 run 的展示口径，不是窗口记账。
	maintInWindowByParent := make(map[string]float64, len(maintRows))
	for _, m := range maintRows {
		if m.ParentRunID == nil || runByID[*m.ParentRunID] == nil {
			continue
		}
		if agg := maintAgg[m.ID]; agg.CostSum != nil {
			maintInWindowByParent[*m.ParentRunID] += *agg.CostSum
		}
	}

	// ---------- 主线 / 定时 run ----------
	for i := range runRows {
		row := &runRows[i]
		acc := botAccs[row.BotID]
		agg := runAgg[row.ID]
		acc.runs++
		acc.totalReq += agg.Total
		acc.pricedReq += agg.Priced
		acc.cachedSum += agg.CachedSum
		acc.promptSum += agg.PromptSum

		// 显示成本 = run 自身 + 关联维护（都为空则 null），桶归属随显示成本。
		// 日归属只记 run 自身成本——关联维护部分由维护循环按维护自己的
		// created_at 记日，跨日不随父 run 漂移；有请求（含未计价）的机器人
		// 进当日 bots 列表。
		display := botSumOptional(row.CostEstimate, maintByParent[row.ID])
		if display != nil {
			acc.displayCosts = append(acc.displayCosts, *display)
		}
		var inWindowMaint *float64
		if v, ok := maintInWindowByParent[row.ID]; ok {
			inWindowMaint = &v
		}
		if bucketAmount := botSumOptional(row.CostEstimate, inWindowMaint); bucketAmount != nil {
			applyRunBucket(row, *bucketAmount)
		}
		day := dayOf(row.CreatedAt)
		if day != nil {
			// hasReq 门槛与 botCost 累加同口径：run 级有价但请求行缺失的存量
			// 数据也要进当日 bots，否则堆叠柱总高含它而分层里没这一层。
			if agg.Total > 0 || row.CostEstimate != nil {
				day.hasReq[row.BotID] = true
			}
			if row.CostEstimate != nil {
				day.cost += *row.CostEstimate
				day.botCost[row.BotID] += *row.CostEstimate
			}
		}

		if row.SessionKind == model.BotSessionKindSchedule {
			if row.ScheduleTaskID == nil {
				continue
			}
			task := taskAccs[*row.ScheduleTaskID]
			if task == nil {
				task = &taskAcc{botID: row.BotID}
				taskAccs[*row.ScheduleTaskID] = task
			}
			task.runs++
			switch model.BotRunStatus(row.Status) {
			case model.BotRunStatusFailed:
				task.failed++
			case model.BotRunStatusSkipped:
				task.skipped++
			}
			if display != nil {
				task.displayCosts = append(task.displayCosts, *display)
			}
		}
	}

	// ---------- 维护运行 ----------
	// 金额按维护运行自己的 created_at 记日；桶跟父 run 走：父 run 在窗口内时
	// 桶归属已随其显示成本完成，父 run 在窗口外时在这里按父的归属补桶，无父
	// run（或父 run 行不存在）才进维护桶。
	for _, m := range maintRows {
		acc := botAccs[m.BotID]
		agg := maintAgg[m.ID]
		acc.totalReq += agg.Total
		acc.pricedReq += agg.Priced
		acc.cachedSum += agg.CachedSum
		acc.promptSum += agg.PromptSum

		day := dayOf(m.CreatedAt)
		if day != nil && agg.Total > 0 {
			day.hasReq[m.BotID] = true
		}
		if agg.CostSum == nil {
			continue // 全部未计价：不进任何金额（缺失 ≠ 0）
		}
		var parent *repository.UsageRunRow
		if m.ParentRunID != nil {
			if row, ok := parentByID[*m.ParentRunID]; ok {
				parent = &row
			}
		}
		switch {
		case parent == nil || botAccs[parent.BotID] == nil:
			// 无父 run、父 run 行不存在（或父的机器人不在查询集合）：
			// 计入机器人维护项。
			acc.costMaintenance = botSumOptional(acc.costMaintenance, agg.CostSum)
		case runByID[parent.ID] != nil:
			// 父 run 在窗口内：桶归属已随父显示成本完成，这里只记日。
		default:
			// 父 run 在窗口外：桶跟父走。
			applyRunBucket(parent, *agg.CostSum)
		}
		if day != nil {
			day.cost += *agg.CostSum
			day.botCost[m.BotID] += *agg.CostSum
		}
	}

	// ---------- 投影 ----------
	overview := &api.BotUsageOverview{
		Range:    api.BotUsageOverviewRange(rangeParam),
		Timezone: tzName,
		Start:    start.UTC().Format(time.RFC3339Nano),
		End:      now.UTC().Format(time.RFC3339Nano),
		Totals: api.BotUsageTotals{
			TotalRequests:    0,
			PricedRequests:   0,
			UnpricedRequests: 0,
		},
		Days:          make([]api.BotUsageDay, 0, len(dayAccs)),
		Bots:          make([]api.BotUsageBot, 0, len(botAccs)),
		ScheduleTasks: make([]api.BotUsageScheduleTask, 0, len(taskAccs)),
	}

	for i := range dayAccs {
		acc := dayAccs[i]
		day := api.BotUsageDay{Date: dates[i], Cost: acc.cost, Bots: []api.BotUsageDay_Bots{}}
		// 当日有模型请求（含未计价）的机器人都进列表；只有未计价请求时
		// cost 为 0（这是必填数值，与「缺失 ≠ 0」的可空字段不同）。
		for botID := range acc.hasReq {
			day.Bots = append(day.Bots, api.BotUsageDay_Bots{BotId: botID, Cost: acc.botCost[botID]})
		}
		sort.Slice(day.Bots, func(i, j int) bool { return day.Bots[i].BotId < day.Bots[j].BotId })
		overview.Days = append(overview.Days, day)
	}

	botItems := make([]api.BotUsageBot, 0, len(botAccs))
	var totalCost *float64
	for botID, acc := range botAccs {
		overview.Totals.TotalRequests += acc.totalReq
		overview.Totals.PricedRequests += acc.pricedReq
		overview.Totals.UnpricedRequests += acc.totalReq - acc.pricedReq
		cost := botSumOptional(botSumOptional(acc.costMain, acc.costSchedule), acc.costMaintenance)
		totalCost = botSumOptional(totalCost, cost)
		var avg, maxCost *float64
		if len(acc.displayCosts) > 0 {
			var sum float64
			maxCost = &acc.displayCosts[0]
			for _, c := range acc.displayCosts {
				sum += c
				if c > *maxCost {
					*maxCost = c
				}
			}
			avg = ptrFloat64(sum / float64(len(acc.displayCosts)))
		}
		var cacheHit *float64
		if acc.promptSum > 0 {
			cacheHit = ptrFloat64(acc.cachedSum / acc.promptSum)
		}
		botItems = append(botItems, api.BotUsageBot{
			BotId:                    botID,
			Name:                     acc.name,
			Cost:                     cost,
			CostMain:                 acc.costMain,
			CostSchedule:             acc.costSchedule,
			CostScheduleLinkedApprox: acc.costLinked,
			CostMaintenance:          acc.costMaintenance,
			Runs:                     acc.runs,
			AvgRunCost:               avg,
			MaxRunCost:               maxCost,
			CacheHitRate:             cacheHit,
			UnpricedRequests:         acc.totalReq - acc.pricedReq,
		})
	}
	sort.Slice(botItems, func(i, j int) bool {
		if botUsageCostLess(botItems[i].Cost, botItems[j].Cost) {
			return true
		}
		if botUsageCostLess(botItems[j].Cost, botItems[i].Cost) {
			return false
		}
		return botItems[i].BotId < botItems[j].BotId
	})
	overview.Bots = botItems

	// 现存任务信息（已删除的取不到行，按契约以 deleted=true 呈现）。
	taskIDs := make([]string, 0, len(taskAccs))
	for taskID := range taskAccs {
		taskIDs = append(taskIDs, taskID)
	}
	taskRows := map[string]model.BotScheduleTask{}
	if len(taskIDs) > 0 {
		rows, err := repo.ScheduleTasksByIDs(taskIDs)
		if err != nil {
			return nil, errs.ErrInternal
		}
		for _, row := range rows {
			taskRows[row.ID] = row
		}
	}
	// 月推算统计走固定窗口（最近 30 个当地日），独立于所选 range：
	// 否则 range=today 无已计价 run 时估算直接消失，且同一任务在不同
	// range 下推算值跳变。
	estStats, err := s.botUsageEstimateStats(taskRows, loc, now)
	if err != nil {
		return nil, errs.ErrInternal
	}
	taskItems := make([]api.BotUsageScheduleTask, 0, len(taskAccs))
	for taskID, acc := range taskAccs {
		item := api.BotUsageScheduleTask{
			TaskId:         taskID,
			BotId:          acc.botID,
			BotName:        botNames[acc.botID],
			Name:           "",
			Deleted:        true,
			Enabled:        false,
			Timezone:       "",
			Runs:           acc.runs,
			Failed:         acc.failed,
			Skipped:        acc.skipped,
			Cost:           acc.cost,
			LinkedMainCost: acc.linked,
		}
		if len(acc.displayCosts) > 0 {
			var sum float64
			maxCost := acc.displayCosts[0]
			for _, c := range acc.displayCosts {
				sum += c
				if c > maxCost {
					maxCost = c
				}
			}
			item.AvgRunCost = ptrFloat64(sum / float64(len(acc.displayCosts)))
			item.MaxRunCost = &maxCost
		}
		if task, ok := taskRows[taskID]; ok {
			item.Deleted = false
			item.Enabled = task.Enabled
			item.Name = task.Name
			item.CronExpression = task.CronExpression
			item.Timezone = task.Timezone
			// 月推算 = 单次运行口径平均 × 未来 30 天触发次数；单次口径与行内
			// 「总花费」一致：定时 run 平均 + 每次触发摊到的连带主线成本
			// （schedule_event 主线写回是这类任务的主路径，不含它会系统性
			// 低估）。统计来自固定 30 天窗而非所选 range。停用、已删除、
			// 无已计价平均或 cron 解析失败都不估算。
			if task.Enabled {
				if st := estStats[taskID]; st != nil && st.priced > 0 && st.runs > 0 {
					if n, ok := botUsageMonthlyTriggerCount(task.CronExpression, task.Timezone, now); ok {
						perRun := *st.displaySum / float64(st.priced)
						if st.linked != nil {
							perRun += *st.linked / float64(st.runs)
						}
						item.EstimatedMonthlyCost = ptrFloat64(perRun * float64(n))
					}
				}
			}
		}
		taskItems = append(taskItems, item)
	}
	sort.Slice(taskItems, func(i, j int) bool {
		ci := botSumOptional(taskItems[i].Cost, taskItems[i].LinkedMainCost)
		cj := botSumOptional(taskItems[j].Cost, taskItems[j].LinkedMainCost)
		if botUsageCostLess(ci, cj) {
			return true
		}
		if botUsageCostLess(cj, ci) {
			return false
		}
		return taskItems[i].TaskId < taskItems[j].TaskId
	})
	overview.ScheduleTasks = taskItems

	// totals.cost 与分桶同口径：任一已计价成分（含 run 级价目，不必依赖请求
	// 行有价）存在即输出；全空才是「未计价」的 null，不能出现按机器人表有
	// 金额而总卡片「—」的自相矛盾。
	if totalCost != nil {
		overview.Totals.Cost = totalCost
		overview.Totals.DailyAverage = ptrFloat64(*totalCost / float64(len(dayAccs)))
	}
	return overview, nil
}

func ptrFloat64(v float64) *float64 { return &v }

// ---------- 单次运行明细：GET /api/bots/usage/runs/:runId ----------

func (s *BotService) GetBotUsageRunDetail(userID, runID string) (*api.BotUsageRunDetail, error) {
	run, err := s.runs.FindByID(runID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotRunNotFound
		}
		return nil, errs.ErrInternal
	}
	// 归属按 run 的 bot 判定：机器人不属于当前用户与 run 不存在同样 404，
	// 不区分「没有这个 run」与「不是你的 run」。
	bot, err := s.bots.FindByIDAndUser(run.BotID, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errs.ErrBotRunNotFound
		}
		return nil, errs.ErrInternal
	}

	repo := repository.NewBotUsageRepository(s.db)
	// 明细请求数有上限：长跑 run 数百请求 × 非终态 5s 轮询会让响应体积
	// 无界；超出部分截断并置 requests_truncated。
	requestRows, err := repo.ListRequestsByRun(run.ID, usageRunDetailMaxRequests+1)
	if err != nil {
		return nil, errs.ErrInternal
	}
	requestsTruncated := len(requestRows) > usageRunDetailMaxRequests
	if requestsTruncated {
		requestRows = requestRows[:usageRunDetailMaxRequests]
	}
	requestIDs := make([]string, 0, len(requestRows))
	for _, r := range requestRows {
		requestIDs = append(requestIDs, r.ID)
	}
	callRows, err := repo.ListToolCallsByRequestIDs(requestIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	callsByRequest := make(map[string][]api.BotUsageToolCall, len(requestRows))
	for _, c := range callRows {
		callsByRequest[c.RequestID] = append(callsByRequest[c.RequestID], api.BotUsageToolCall{
			Id:         c.ID,
			ToolName:   c.ToolName,
			Action:     c.Action,
			Status:     string(c.Status),
			ErrorCode:  c.ErrorCode,
			CreatedAt:  c.CreatedAt.UTC().Format(time.RFC3339Nano),
			FinishedAt: rfc3339(c.FinishedAt),
		})
	}

	requests := make([]api.BotUsageRequest, 0, len(requestRows))
	seenHash := map[string]bool{}
	for i := range requestRows {
		row := &requestRows[i]
		// is_retry：与同 run 更早请求 request_hash 相同即视为重试（重试每次
		// 尝试单独一行，同一逻辑尝试的指纹相同）。
		isRetry := row.RequestHash != "" && seenHash[row.RequestHash]
		if row.RequestHash != "" {
			seenHash[row.RequestHash] = true
		}
		calls := callsByRequest[row.ID]
		if calls == nil {
			// tool_calls 是必填数组：无调用时给空数组，不序列化成 null。
			calls = []api.BotUsageToolCall{}
		}
		requests = append(requests, api.BotUsageRequest{
			Id:                  row.ID,
			Ordinal:             row.Ordinal,
			Status:              string(row.Status),
			IsRetry:             isRetry,
			InputThroughSeq:     row.InputThroughSeq,
			InputTokensEstimate: row.InputTokensEstimate,
			InputEstimator:      row.InputEstimator,
			PromptTokens:        row.PromptTokens,
			CachedTokens:        row.CachedTokens,
			CompletionTokens:    row.CompletionTokens,
			TotalTokens:         row.TotalTokens,
			ToolsTokensEstimate: row.ToolsTokensEstimate,
			// bot_model_requests 没有 usage_complete 列，沿用聊天页口径：
			// 落过 usage 计数列（prompt_tokens 非空）才算完整。
			UsageComplete: row.PromptTokens != nil,
			CostEstimate:  row.CostEstimate,
			CostSource:    row.CostSource,
			StartedAt:     row.StartedAt.UTC().Format(time.RFC3339Nano),
			FirstDeltaAt:  rfc3339(row.FirstDeltaAt),
			FinishedAt:    rfc3339(row.FinishedAt),
			ErrorCode:     row.ErrorCode,
			ErrorMessage:  row.ErrorMessage,
			ToolCalls:     calls,
		})
	}

	maintRunRows, err := repo.ListMaintenanceRunsByParent(run.ID)
	if err != nil {
		return nil, errs.ErrInternal
	}
	maintIDs := make([]string, 0, len(maintRunRows))
	for _, m := range maintRunRows {
		maintIDs = append(maintIDs, m.ID)
	}
	maintReqRows, err := repo.ListMaintenanceRequests(maintIDs)
	if err != nil {
		return nil, errs.ErrInternal
	}
	maintReqsByRun := make(map[string][]api.BotUsageMaintenanceRequest, len(maintRunRows))
	var maintCost *float64
	for i := range maintReqRows {
		row := &maintReqRows[i]
		maintReqsByRun[row.MaintenanceRunID] = append(maintReqsByRun[row.MaintenanceRunID], api.BotUsageMaintenanceRequest{
			Id:               row.ID,
			Ordinal:          row.Ordinal,
			Purpose:          string(row.Purpose),
			Model:            row.Model,
			Status:           string(row.Status),
			UsageComplete:    row.UsageComplete,
			PromptTokens:     row.PromptTokens,
			CachedTokens:     row.CachedTokens,
			CompletionTokens: row.CompletionTokens,
			TotalTokens:      row.TotalTokens,
			CostEstimate:     row.CostEstimate,
			CostSource:       row.CostSource,
			ErrorCode:        row.ErrorCode,
			ErrorMessage:     row.ErrorMessage,
			StartedAt:        row.StartedAt.UTC().Format(time.RFC3339Nano),
			FinishedAt:       rfc3339(row.FinishedAt),
		})
		maintCost = botSumOptional(maintCost, row.CostEstimate)
	}
	maintRuns := make([]api.BotUsageMaintenanceRun, 0, len(maintRunRows))
	for i := range maintRunRows {
		row := &maintRunRows[i]
		reqs := maintReqsByRun[row.ID]
		if reqs == nil {
			reqs = []api.BotUsageMaintenanceRequest{}
		}
		maintRuns = append(maintRuns, api.BotUsageMaintenanceRun{
			Id:           row.ID,
			Kind:         string(row.Kind),
			Status:       string(row.Status),
			CreatedAt:    row.CreatedAt.UTC().Format(time.RFC3339Nano),
			StartedAt:    rfc3339(row.StartedAt),
			FinishedAt:   rfc3339(row.FinishedAt),
			CostEstimate: maintCostOf(row.ID, maintReqsByRun),
			Requests:     reqs,
		})
	}

	// 任务名：现存任务才有；任务已删除或无关联时为 null。
	var taskID, taskName *string
	if run.ScheduleTaskID != nil {
		taskID = run.ScheduleTaskID
		if task, err := s.schedTasks.FindByID(*run.ScheduleTaskID); err == nil && task.BotID == run.BotID {
			taskName = &task.Name
		}
	}

	detail := &api.BotUsageRunDetail{
		Requests:          requests,
		RequestsTruncated: requestsTruncated,
		MaintenanceRuns:   maintRuns,
		Run: api.BotUsageRun{
			Id:               run.ID,
			BotId:            run.BotID,
			BotName:          bot.Name,
			SessionKind:      run.SessionKind,
			ScheduleTaskId:   taskID,
			ScheduleTaskName: taskName,
			TriggerSource:    string(run.TriggerSource),
			TriggerKey:       run.TriggerKey,
			Status:           string(run.Status),
			Summary:          run.Summary,
			Silent:           run.Silent,
			ScheduledFor:     rfc3339(run.ScheduledFor),
			PromptTokens:     run.PromptTokens,
			CompletionTokens: run.CompletionTokens,
			TotalTokens:      run.TotalTokens,
			CachedTokens:     run.CachedTokens,
			UsageComplete:    run.UsageComplete,
			CostEstimate:     run.CostEstimate,
			CostSource:       run.CostSource,
			MaintenanceCost:  maintCost,
			DisplayCost:      botSumOptional(run.CostEstimate, maintCost),
			ErrorCode:        run.ErrorCode,
			ErrorMessage:     run.ErrorMessage,
			CreatedAt:        run.CreatedAt.UTC().Format(time.RFC3339Nano),
			StartedAt:        rfc3339(run.StartedAt),
			FinishedAt:       rfc3339(run.FinishedAt),
		},
	}
	return detail, nil
}

// maintCostOf 单个维护运行的已计价成本合计（服务层在装配明细时顺带算好，
// 不再回库）。
func maintCostOf(maintRunID string, byRun map[string][]api.BotUsageMaintenanceRequest) *float64 {
	var cost *float64
	for _, req := range byRun[maintRunID] {
		cost = botSumOptional(cost, req.CostEstimate)
	}
	return cost
}

// ---------- 投影辅助（display_cost_estimate / cost_last_7d） ----------

// maintenanceCostsOf 批量取 run 的关联维护已计价成本合计（run ID → 合计，
// 无已计价维护的 run 不在结果里）：列表投影一次取全当前页，显示口径成本
// 在投影函数里与 run 自身成本合成，不做逐 run N+1。
func (s *BotService) maintenanceCostsOf(runIDs []string) (map[string]*float64, error) {
	if len(runIDs) == 0 {
		return map[string]*float64{}, nil
	}
	return repository.NewBotUsageRepository(s.db).MaintenanceCostsByParentRun(runIDs)
}

// ScheduleTaskCostsLast7d 批量计算任务近 7 天已计价成本（schedule run 自身 +
// 关联维护 + 连带主线成本；连带主线按总览 linked_main_cost 同口径：主线自身
// 与关联维护合成，两者皆未计价则整条不计）。窗口与总览一致按用户时区的当地
// 日切（botUsageWindow），不用滚动 7×24h；维护归属同总览：窗口内发生、父
// run 在窗口外的维护经父反查归入任务。整页任务常数条查询取全，不做逐任务
// N+1；无已计价记录的任务不出现在结果里（投影侧保持 null）。
func (s *BotService) ScheduleTaskCostsLast7d(userID, botID string, taskIDs []string) (map[string]*float64, error) {
	out := make(map[string]*float64, len(taskIDs))
	if len(taskIDs) == 0 {
		return out, nil
	}
	now := time.Now().UTC()
	_, loc := s.botUserTimezoneOf(userID)
	windowStart, _ := botUsageWindow(7, loc, now)
	repo := repository.NewBotUsageRepository(s.db)

	runs, err := repo.ListScheduleRunsForTasksSince(botID, taskIDs, windowStart)
	if err != nil {
		return nil, err
	}
	inSet := make(map[string]bool, len(taskIDs))
	for _, id := range taskIDs {
		inSet[id] = true
	}
	runTaskOf := make(map[string]string, len(runs))
	runIDs := make([]string, 0, len(runs))
	for _, r := range runs {
		runIDs = append(runIDs, r.ID)
		if r.ScheduleTaskID != nil && inSet[*r.ScheduleTaskID] {
			runTaskOf[r.ID] = *r.ScheduleTaskID
			out[*r.ScheduleTaskID] = botSumOptional(out[*r.ScheduleTaskID], r.CostEstimate)
		}
	}
	// 连带主线 run：schedule_event 唤醒的主线经 trigger_key 反查任务。
	mains, err := repo.ListScheduleEventMainRunsSince(botID, windowStart)
	if err != nil {
		return nil, err
	}
	mainIDs := make([]string, 0, len(mains))
	refIDs := make([]string, 0, len(mains))
	for _, m := range mains {
		mainIDs = append(mainIDs, m.ID)
		if ref := botScheduleEventRunIDOf(m.TriggerKey); ref != "" {
			refIDs = append(refIDs, ref)
		}
	}
	taskByRefRun, err := repo.ScheduleTaskIDsByRunIDs(refIDs)
	if err != nil {
		return nil, err
	}
	// 关联维护与总览同口径只算窗口内发生的部分：父在窗口内的维护按维护
	// 自身 created_at 落在 7 天窗口内计，窗口外维护不摊进近 7 天（总览桶
	// 的 maintInWindowByParent 同规则）；父在窗口外的窗口内维护由下方反查
	// 按父归属补记。
	maintRows, err := repo.ListMaintenanceRunsInWindow([]string{botID}, windowStart, now)
	if err != nil {
		return nil, err
	}
	coveredParent := make(map[string]bool, len(runIDs)+len(mainIDs))
	for _, id := range runIDs {
		coveredParent[id] = true
	}
	for _, id := range mainIDs {
		coveredParent[id] = true
	}
	maintIDs := make([]string, 0, len(maintRows))
	for _, m := range maintRows {
		maintIDs = append(maintIDs, m.ID)
	}
	maintAgg, err := repo.MaintenanceRequestAggregatesByRun(maintIDs)
	if err != nil {
		return nil, err
	}
	maint := make(map[string]*float64)
	var lateParentIDs []string
	for _, m := range maintRows {
		if m.ParentRunID == nil {
			continue
		}
		if coveredParent[*m.ParentRunID] {
			agg := maintAgg[m.ID]
			if agg.CostSum != nil {
				maint[*m.ParentRunID] = botSumOptional(maint[*m.ParentRunID], agg.CostSum)
			}
		} else {
			lateParentIDs = append(lateParentIDs, *m.ParentRunID)
		}
	}
	for runID, cost := range maint {
		taskID, ok := runTaskOf[runID]
		if !ok || cost == nil {
			continue
		}
		out[taskID] = botSumOptional(out[taskID], cost)
	}
	for _, m := range mains {
		ref := botScheduleEventRunIDOf(m.TriggerKey)
		taskID, ok := taskByRefRun[ref]
		if !ok || !inSet[taskID] {
			continue
		}
		display := botSumOptional(m.CostEstimate, maint[m.ID])
		if display == nil {
			continue // 主线自身与关联维护都未计价：整条不计
		}
		out[taskID] = botSumOptional(out[taskID], display)
	}

	// 窗口内维护、父 run 在窗口外（与总览同口径：记日跟维护、桶跟父走）：
	// 父是任务的 schedule run 或指向任务的连带主线时，维护也计入近 7 天。
	// 父在窗口内的维护已由上面的 maint 映射覆盖，不重复累加。
	if len(lateParentIDs) > 0 {
		parentByID, err := repo.ListRunsByIDs(lateParentIDs)
		if err != nil {
			return nil, err
		}
		refOf := make(map[string]string, len(parentByID))
		var lateRefIDs []string
		for _, p := range parentByID {
			if p.BotID != botID ||
				p.SessionKind != model.BotSessionKindMain ||
				p.TriggerSource != string(model.BotTriggerScheduleEvent) {
				continue
			}
			if ref := botScheduleEventRunIDOf(p.TriggerKey); ref != "" {
				refOf[p.ID] = ref
				lateRefIDs = append(lateRefIDs, ref)
			}
		}
		lateTaskByRef, err := repo.ScheduleTaskIDsByRunIDs(lateRefIDs)
		if err != nil {
			return nil, err
		}
		for _, m := range maintRows {
			if m.ParentRunID == nil || coveredParent[*m.ParentRunID] {
				continue
			}
			agg := maintAgg[m.ID]
			if agg.CostSum == nil {
				continue
			}
			parent, ok := parentByID[*m.ParentRunID]
			if !ok || parent.BotID != botID {
				continue
			}
			switch {
			case parent.SessionKind == model.BotSessionKindSchedule:
				if parent.ScheduleTaskID != nil && inSet[*parent.ScheduleTaskID] {
					out[*parent.ScheduleTaskID] = botSumOptional(out[*parent.ScheduleTaskID], agg.CostSum)
				}
			default:
				if taskID, ok := lateTaskByRef[refOf[parent.ID]]; ok && inSet[taskID] {
					out[taskID] = botSumOptional(out[taskID], agg.CostSum)
				}
			}
		}
	}
	return out, nil
}

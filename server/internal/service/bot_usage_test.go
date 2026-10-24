package service

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// 花费页（INT-162）服务层测试：时区切日、三桶之和、schedule_event 连带成本、
// 删除任务语义、失败/跳过计数、月推算、未计价空值与运行明细投影。
// 成本一律直接 seed cost_estimate 列——聚合只读账本，不关心计价过程。

func seedUsageRun(t *testing.T, env *botTestEnv, botID string, mutate func(*model.BotRun)) *model.BotRun {
	t.Helper()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "usage-" + uuid.NewString(),
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}",
		CreatedAt:          time.Now().UTC(), StartedAt: ptrTime(time.Now().UTC().Add(-time.Minute)),
		FinishedAt: ptrTime(time.Now().UTC()),
	}
	if mutate != nil {
		mutate(run)
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed usage run: %v", err)
	}
	return run
}

func seedUsageRequest(t *testing.T, env *botTestEnv, runID string, cost *float64, requestHash string) *model.BotModelRequest {
	t.Helper()
	var maxOrdinal int64
	if err := env.db.Model(&model.BotModelRequest{}).Where("run_id = ?", runID).
		Select("COALESCE(MAX(ordinal), 0)").Scan(&maxOrdinal).Error; err != nil {
		t.Fatalf("next ordinal: %v", err)
	}
	now := time.Now().UTC()
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: runID, Ordinal: maxOrdinal + 1,
		Status: model.BotModelRequestSucceeded, StartedAt: now,
		PrefixHash: "p", ToolsHash: "t", RequestHash: requestHash, TokenEstimator: "chars/4",
	}
	if cost != nil {
		req.CostEstimate = cost
		prompt := int64(1000)
		completion := int64(100)
		total := prompt + completion
		req.PromptTokens = &prompt
		req.CompletionTokens = &completion
		req.TotalTokens = &total
	}
	if err := env.db.Create(req).Error; err != nil {
		t.Fatalf("seed usage request: %v", err)
	}
	return req
}

func seedUsageMaintenance(t *testing.T, env *botTestEnv, botID string, parentRunID *string, cost *float64) *model.BotMaintenanceRun {
	t.Helper()
	now := time.Now().UTC()
	run := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: botID,
		Kind: model.BotMaintenanceCompaction, TriggerKey: "usage-maint-" + uuid.NewString(),
		ParentRunID: parentRunID, Status: model.BotMaintenanceSucceeded,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.db.Create(run).Error; err != nil {
		t.Fatalf("seed maintenance run: %v", err)
	}
	req := &model.BotMaintenanceRequest{
		ID: uuid.NewString(), MaintenanceRunID: run.ID, Ordinal: 1,
		Purpose: model.BotPurposeSummary, Model: "fake-model",
		Status:     model.BotModelRequestSucceeded,
		PrefixHash: "p", ToolsHash: "", ToolsetVersion: 1,
		UsageComplete: cost != nil, StartedAt: now, FinishedAt: &now,
	}
	if cost != nil {
		req.CostEstimate = cost
		prompt := int64(500)
		req.PromptTokens = &prompt
		req.TotalTokens = &prompt
	}
	if err := env.db.Create(req).Error; err != nil {
		t.Fatalf("seed maintenance request: %v", err)
	}
	return run
}

func ptrTime(t time.Time) *time.Time { return &t }
func ptrCost(v float64) *float64     { return &v }

// costEq 浮点金额的容差相等（聚合是浮点累加，不做十进制舍入）。
func costEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Asia/Shanghai 切日：UTC 16:30 属当地次日；7d 窗口输出含无数据日在内的
// 7 个当地日，且当天行只含当天数据。
func TestBotUsageTimezoneDayBucketing(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagetz")
	bot := env.createBot(t, userID)
	setUserTimezone(t, env, userID, "Asia/Shanghai")

	sh, _ := time.LoadLocation("Asia/Shanghai")
	// 两天前（上海）零点回退 48h 再加 30 分钟 = 前天 UTC 16:30 = 上海次日 00:30。
	createdAt := botUsageDayStart(time.Now().UTC(), sh).Add(-48 * time.Hour).Add(30 * time.Minute)
	if got, want := createdAt.In(sh).Format("2006-01-02"), createdAt.UTC().Format("2006-01-02"); got == want {
		t.Fatalf("测试时刻构造失效：上海日期不应等于 UTC 日期（%s）", got)
	}
	wantDate := createdAt.In(sh).Format("2006-01-02")
	tzRun := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = createdAt
		r.CostEstimate = ptrCost(0.25)
	})
	seedUsageRequest(t, env, tzRun.ID, ptrCost(0.25), "hash-tz")

	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.Timezone != "Asia/Shanghai" {
		t.Fatalf("生效时区应为 Asia/Shanghai，实得 %s", overview.Timezone)
	}
	if len(overview.Days) != 7 {
		t.Fatalf("7d 应输出 7 个当地日（含无数据日），实得 %d", len(overview.Days))
	}
	var hit *struct {
		cost float64
		bots int
	}
	for i, d := range overview.Days {
		if i == len(overview.Days)-1 && d.Date != time.Now().In(sh).Format("2006-01-02") {
			t.Fatalf("末行应是今天（当地），实得 %s", d.Date)
		}
		if d.Date == wantDate {
			cost := d.Cost
			hit = &struct {
				cost float64
				bots int
			}{cost, len(d.Bots)}
		}
	}
	if hit == nil {
		t.Fatalf("应存在日期 %s 的行: %+v", wantDate, overview.Days)
	}
	if !costEq(hit.cost, 0.25) || hit.bots != 1 {
		t.Fatalf("%s 应计入该笔成本且含该机器人，实得 cost=%v bots=%d", wantDate, hit.cost, hit.bots)
	}
	// 该时刻的 UTC 日期行不应存在独立归属（同一笔只计一次）。
	sum := 0.0
	for _, d := range overview.Days {
		sum += d.Cost
	}
	if !costEq(sum, 0.25) {
		t.Fatalf("days 成本合计应为 0.25（同一笔只进一个当地日），实得 %v", sum)
	}
}

// 三桶之和 = 机器人总花费：主线（含关联维护）+ 定时 + 无父维护；totals 与
// days 同源。
func TestBotUsageThreeBucketsSum(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagesum")
	bot := env.createBot(t, userID)

	// 主线 run：自身 1.0 + 关联维护 0.5。
	main := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CostEstimate = ptrCost(1.0)
	})
	seedUsageRequest(t, env, main.ID, ptrCost(1.0), "hash-main")
	seedUsageMaintenance(t, env, bot.ID, &main.ID, ptrCost(0.5))

	// 定时 run：自身 2.0 + 关联维护 0.25。
	task := seedScheduleTask(t, env, bot.ID, nil)
	sched := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.Status = model.BotRunStatusCompleted
		r.SessionKind = model.BotSessionKindSchedule
		taskID := task.ID
		r.ScheduleTaskID = &taskID
		r.CostEstimate = ptrCost(2.0)
	})
	seedUsageRequest(t, env, sched.ID, ptrCost(2.0), "hash-sched")
	seedUsageMaintenance(t, env, bot.ID, &sched.ID, ptrCost(0.25))

	// 无父维护（夜间整理口径）：3.0。
	seedUsageMaintenance(t, env, bot.ID, nil, ptrCost(3.0))

	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(overview.Bots) != 1 {
		t.Fatalf("应有 1 个机器人，实得 %d", len(overview.Bots))
	}
	b := overview.Bots[0]
	if b.CostMain == nil || !costEq(*b.CostMain, 1.5) {
		t.Fatalf("主线桶应含关联维护 = 1.5，实得 %v", b.CostMain)
	}
	if b.CostSchedule == nil || !costEq(*b.CostSchedule, 2.25) {
		t.Fatalf("定时桶应含关联维护 = 2.25，实得 %v", b.CostSchedule)
	}
	if b.CostMaintenance == nil || !costEq(*b.CostMaintenance, 3.0) {
		t.Fatalf("维护项应为 3.0，实得 %v", b.CostMaintenance)
	}
	if b.Cost == nil || !costEq(*b.Cost, 6.75) {
		t.Fatalf("机器人合计应为 6.75，实得 %v", b.Cost)
	}
	if overview.Totals.Cost == nil || !costEq(*overview.Totals.Cost, 6.75) {
		t.Fatalf("totals.cost 应为 6.75，实得 %v", overview.Totals.Cost)
	}
	if overview.Totals.PricedRequests != 5 || overview.Totals.UnpricedRequests != 0 ||
		overview.Totals.TotalRequests != 5 {
		t.Fatalf("请求计数不符: %+v", overview.Totals)
	}
	daySum := 0.0
	for _, d := range overview.Days {
		daySum += d.Cost
	}
	if !costEq(daySum, 6.75) {
		t.Fatalf("days 合计应与 totals 同源 = 6.75，实得 %v", daySum)
	}
}

// schedule_event 连带成本计入任务且不重复计入主线；解析不出归属时留在主线。
func TestBotUsageScheduleEventLinkedCost(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagelink")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	sched := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := task.ID
		r.ScheduleTaskID = &taskID
		r.CostEstimate = ptrCost(0.2)
	})
	seedUsageRequest(t, env, sched.ID, ptrCost(0.2), "hash-l1")
	// 连带主线 run：trigger_key 反查回该任务。
	linked := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerScheduleEvent
		r.TriggerKey = "schedule_event:" + sched.ID + ":" + string(model.BotRunStatusCompleted)
		r.CostEstimate = ptrCost(0.7)
	})
	seedUsageRequest(t, env, linked.ID, ptrCost(0.7), "hash-l2")
	// 归属解析不出的连带主线（引用的 schedule run 不存在）留在主线桶。
	orphan := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerScheduleEvent
		r.TriggerKey = "schedule_event:" + uuid.NewString() + ":" + string(model.BotRunStatusCompleted)
		r.CostEstimate = ptrCost(0.1)
	})
	seedUsageRequest(t, env, orphan.ID, ptrCost(0.1), "hash-l3")

	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	b := overview.Bots[0]
	if b.CostMain == nil || !costEq(*b.CostMain, 0.1) {
		t.Fatalf("主线桶应只剩解析不出归属的 0.1，实得 %v", b.CostMain)
	}
	if b.CostSchedule == nil || !costEq(*b.CostSchedule, 0.9) {
		t.Fatalf("定时桶应为 schedule 0.2 + 连带 0.7 = 0.9，实得 %v", b.CostSchedule)
	}
	if b.CostScheduleLinkedApprox == nil || !costEq(*b.CostScheduleLinkedApprox, 0.7) {
		t.Fatalf("连带近似部分应为 0.7，实得 %v", b.CostScheduleLinkedApprox)
	}
	if b.Cost == nil || !costEq(*b.Cost, 1.0) {
		t.Fatalf("合计应为 1.0，实得 %v", b.Cost)
	}
	if len(overview.ScheduleTasks) != 1 {
		t.Fatalf("应有 1 个任务，实得 %d", len(overview.ScheduleTasks))
	}
	taskItem := overview.ScheduleTasks[0]
	if taskItem.LinkedMainCost == nil || !costEq(*taskItem.LinkedMainCost, 0.7) {
		t.Fatalf("任务连带成本应为 0.7，实得 %v", taskItem.LinkedMainCost)
	}
	if taskItem.Cost == nil || !costEq(*taskItem.Cost, 0.2) {
		t.Fatalf("任务自身成本应为 0.2（不含连带），实得 %v", taskItem.Cost)
	}
	if overview.Totals.Cost == nil || !costEq(*overview.Totals.Cost, 1.0) {
		t.Fatalf("totals 不应因连带重复计数，应为 1.0，实得 %v", overview.Totals.Cost)
	}
}

// 任务删除后运行记录保留：deleted=true、名称/表达式为空串、enabled=false，
// 花费照常聚合。
func TestBotUsageDeletedTaskKept(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagedel")
	bot := env.createBot(t, userID)
	deletedTaskID := uuid.NewString()
	seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		r.ScheduleTaskID = &deletedTaskID
		r.CostEstimate = ptrCost(0.4)
	})

	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(overview.ScheduleTasks) != 1 {
		t.Fatalf("已删除任务仍应出现在聚合里，实得 %d", len(overview.ScheduleTasks))
	}
	item := overview.ScheduleTasks[0]
	if !item.Deleted || item.Enabled || item.Name != "" || item.CronExpression != "" {
		t.Fatalf("已删除任务投影不符: %+v", item)
	}
	if item.Cost == nil || !costEq(*item.Cost, 0.4) {
		t.Fatalf("已删除任务花费应保留，实得 %v", item.Cost)
	}
	if item.EstimatedMonthlyCost != nil {
		t.Fatalf("已删除任务不做月推算，实得 %v", item.EstimatedMonthlyCost)
	}
	if item.BotName != bot.Name {
		t.Fatalf("bot_name 应来自运行记录的机器人，实得 %s", item.BotName)
	}
}

// 失败/跳过计数 + 停用任务不做月推算；0 9 * * *（上海，无夏令时）月推算
// 恰为 30 次触发 × 平均。
func TestBotUsageTaskCountsAndMonthlyEstimate(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usageest")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	mk := func(status model.BotRunStatus, cost *float64) {
		seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
			r.TriggerSource = model.BotTriggerSchedule
			r.SessionKind = model.BotSessionKindSchedule
			taskID := task.ID
			r.ScheduleTaskID = &taskID
			r.Status = status
			r.CostEstimate = cost
		})
	}
	mk(model.BotRunStatusCompleted, ptrCost(0.1))
	mk(model.BotRunStatusFailed, ptrCost(0.05))
	skipped := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := task.ID
		r.ScheduleTaskID = &taskID
		r.Status = model.BotRunStatusSkipped
	})
	// schedule_event 唤醒的连带主线：经 trigger_key 反查归任务，进
	// linked_main_cost 与月推算口径（4 次定时 run 摊 0.3 → 每次 0.075）。
	seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerScheduleEvent
		r.TriggerKey = "schedule_event:" + skipped.ID + ":completed"
		r.SessionKind = model.BotSessionKindMain
		r.CostEstimate = ptrCost(0.3)
	})
	// 10 天前的已计价 run：在 7d 所选窗口之外、固定 30 天估算窗口之内。
	// 月推算用它但不影响行内 runs/avg（行内仍按所选窗口统计）。
	seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := task.ID
		r.ScheduleTaskID = &taskID
		r.CostEstimate = ptrCost(0.2)
		r.CreatedAt = time.Now().UTC().AddDate(0, 0, -10)
	})

	disabled := seedScheduleTask(t, env, bot.ID, nil)
	// enabled 列带 default:true，Create 会省略零值——先建后改才是真实停用路径。
	if err := env.db.Model(&model.BotScheduleTask{}).Where("id = ?", disabled.ID).
		Update("enabled", false).Error; err != nil {
		t.Fatalf("disable task: %v", err)
	}
	seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := disabled.ID
		r.ScheduleTaskID = &taskID
		r.CostEstimate = ptrCost(0.2)
	})

	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	byTask := map[string]*struct {
		runs, failed, skipped int64
		estimated             *float64
		avg                   *float64
	}{}
	for i := range overview.ScheduleTasks {
		item := overview.ScheduleTasks[i]
		byTask[item.TaskId] = &struct {
			runs, failed, skipped int64
			estimated             *float64
			avg                   *float64
		}{item.Runs, item.Failed, item.Skipped, item.EstimatedMonthlyCost, item.AvgRunCost}
	}
	enabled := byTask[task.ID]
	if enabled.runs != 3 || enabled.failed != 1 || enabled.skipped != 1 {
		t.Fatalf("任务计数不符: %+v", enabled)
	}
	// 平均只统计有已计价成本的 run（0.1 与 0.05）。
	if enabled.avg == nil || !costEq(*enabled.avg, 0.075) {
		t.Fatalf("平均应只含有已计价 run = 0.075，实得 %v", enabled.avg)
	}
	// 上海无夏令时，(now, now+30d] 恰含 30 次每日触发；单次口径来自固定
	// 30 天窗（4 个已计价 run：0.1/0.05/0.2 均值 0.1167 + 连带 0.3/4）：
	// 30 × (0.35/3 + 0.075) ≈ 5.75。
	if enabled.estimated == nil || *enabled.estimated < 5.7 || *enabled.estimated > 5.8 {
		t.Fatalf("月推算应为 30 × (0.1167 + 0.075) ≈ 5.75，实得 %v", enabled.estimated)
	}
	// 固定窗口让 today/7d/30d 的推算一致（所选 range 内无已计价 run 也
	// 不消失）。
	todayOverview, err := env.bots.GetBotUsageOverview(userID, "today", "")
	if err != nil {
		t.Fatalf("today overview: %v", err)
	}
	for i := range todayOverview.ScheduleTasks {
		item := todayOverview.ScheduleTasks[i]
		if item.TaskId == task.ID {
			if item.EstimatedMonthlyCost == nil ||
				*item.EstimatedMonthlyCost < 5.7 || *item.EstimatedMonthlyCost > 5.8 {
				t.Fatalf("today 月推算应与 7d 一致 ≈5.75，实得 %v", item.EstimatedMonthlyCost)
			}
		}
	}
	disabledItem := byTask[disabled.ID]
	if disabledItem.estimated != nil {
		t.Fatalf("停用任务不做月推算，实得 %v", disabledItem.estimated)
	}
}

// 未计价（cost_estimate IS NULL）计 unpriced_requests，金额字段缺省 null
// 不为 0；display_cost 在自身与维护都未计价时为 null。
func TestBotUsageUnpricedNull(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagenull")
	bot := env.createBot(t, userID)

	run := seedUsageRun(t, env, bot.ID, nil)
	seedUsageRequest(t, env, run.ID, nil, "hash-u1")
	seedUsageRequest(t, env, run.ID, nil, "hash-u2")
	seedUsageMaintenance(t, env, bot.ID, nil, nil)

	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	b := overview.Bots[0]
	if b.Cost != nil || b.CostMain != nil || b.CostMaintenance != nil {
		t.Fatalf("全未计价时金额字段应为 null: %+v", b)
	}
	if b.UnpricedRequests != 3 {
		t.Fatalf("未计价请求数应为 3（2 主线 + 1 维护），实得 %d", b.UnpricedRequests)
	}
	if overview.Totals.Cost != nil || overview.Totals.PricedRequests != 0 {
		t.Fatalf("无已计价请求时 totals.cost 应为 null: %+v", overview.Totals)
	}
	if overview.Totals.TotalRequests != 3 {
		t.Fatalf("total_requests 应含维护 = 3，实得 %d", overview.Totals.TotalRequests)
	}
	if overview.Totals.DailyAverage != nil {
		t.Fatalf("cost 为 null 时 daily_average 同 null，实得 %v", overview.Totals.DailyAverage)
	}

	detail, err := env.bots.GetBotUsageRunDetail(userID, run.ID)
	if err != nil {
		t.Fatalf("run detail: %v", err)
	}
	if detail.Run.CostEstimate != nil || detail.Run.MaintenanceCost != nil || detail.Run.DisplayCost != nil {
		t.Fatalf("未计价 run 的明细金额应全为 null: %+v", detail.Run)
	}
}

// 明细投影：请求按 ordinal 正序、is_retry 按同 run 更早同 request_hash 判定、
// 工具调用按 request 关联、关联维护运行与显示成本按契约合成。
func TestBotUsageRunDetailProjection(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagedetail")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	run := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := task.ID
		r.ScheduleTaskID = &taskID
		r.CostEstimate = ptrCost(0.3)
	})
	first := seedUsageRequest(t, env, run.ID, ptrCost(0.3), "same-hash")
	seedUsageRequest(t, env, run.ID, nil, "same-hash") // 重试：同指纹、未计价
	maint := seedUsageMaintenance(t, env, bot.ID, &run.ID, ptrCost(0.2))
	// usage 未收齐（usage_complete=false）但已写 prompt_tokens 的维护请求：
	// 投影必须用真实列，不能按 tokens 非空推断成「已收齐」。
	now := time.Now().UTC()
	partial := int64(200)
	if err := env.db.Create(&model.BotMaintenanceRequest{
		ID: uuid.NewString(), MaintenanceRunID: maint.ID, Ordinal: 2,
		Purpose: model.BotPurposeSummary, Model: "fake-model",
		Status:     model.BotModelRequestSucceeded,
		PrefixHash: "p", ToolsHash: "", ToolsetVersion: 1,
		UsageComplete: false, PromptTokens: &partial, TotalTokens: &partial,
		StartedAt: now, FinishedAt: &now,
	}).Error; err != nil {
		t.Fatalf("seed partial maintenance request: %v", err)
	}

	detail, err := env.bots.GetBotUsageRunDetail(userID, run.ID)
	if err != nil {
		t.Fatalf("run detail: %v", err)
	}
	r := detail.Run
	if r.Id != run.ID || r.BotName != bot.Name || r.SessionKind != "schedule" {
		t.Fatalf("明细 run 头部不符: %+v", r)
	}
	if r.ScheduleTaskId == nil || *r.ScheduleTaskId != task.ID ||
		r.ScheduleTaskName == nil || *r.ScheduleTaskName != task.Name {
		t.Fatalf("任务关联应解析出现存任务名: %+v", r)
	}
	if r.CostEstimate == nil || !costEq(*r.CostEstimate, 0.3) {
		t.Fatalf("自身成本应为 0.3，实得 %v", r.CostEstimate)
	}
	if r.MaintenanceCost == nil || !costEq(*r.MaintenanceCost, 0.2) {
		t.Fatalf("维护成本应为 0.2，实得 %v", r.MaintenanceCost)
	}
	if r.DisplayCost == nil || !costEq(*r.DisplayCost, 0.5) {
		t.Fatalf("显示成本应为 0.5，实得 %v", r.DisplayCost)
	}

	if len(detail.Requests) != 2 {
		t.Fatalf("应有 2 条请求，实得 %d", len(detail.Requests))
	}
	if detail.Requests[0].Id != first.ID || detail.Requests[0].Ordinal != 1 {
		t.Fatalf("请求应按 ordinal 正序: %+v", detail.Requests[0])
	}
	if detail.Requests[0].IsRetry {
		t.Fatalf("首条不应计为重试")
	}
	if !detail.Requests[1].IsRetry {
		t.Fatalf("同 request_hash 的后一条应计为重试: %+v", detail.Requests[1])
	}
	if detail.Requests[1].CostEstimate != nil {
		t.Fatalf("未计价重试行成本应为 null，实得 %v", detail.Requests[1].CostEstimate)
	}
	if len(detail.Requests[0].ToolCalls) != 0 {
		t.Fatalf("无工具调用应为空数组，实得 %+v", detail.Requests[0].ToolCalls)
	}

	if len(detail.MaintenanceRuns) != 1 {
		t.Fatalf("应有 1 条关联维护运行，实得 %d", len(detail.MaintenanceRuns))
	}
	mr := detail.MaintenanceRuns[0]
	if mr.Id != maint.ID || mr.CostEstimate == nil || !costEq(*mr.CostEstimate, 0.2) {
		t.Fatalf("维护运行投影不符: %+v", mr)
	}
	if len(mr.Requests) != 2 || mr.Requests[0].Purpose != "summary" {
		t.Fatalf("维护请求应随行返回: %+v", mr.Requests)
	}
	if !mr.Requests[0].UsageComplete {
		t.Fatalf("已收齐请求的 usage_complete 应为 true: %+v", mr.Requests[0])
	}
	if mr.Requests[1].UsageComplete {
		t.Fatalf("usage_complete=false 的维护请求应按实投影，不能按 tokens 推断: %+v", mr.Requests[1])
	}
}

// 花费页参数与归属：非法 range 40001；他人 bot_id 404；他人 runId 404；
// today 只有一个日行。
func TestBotUsageParamsAndOwnership(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usageown")
	bot := env.createBot(t, userID)
	run := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) { r.CostEstimate = ptrCost(0.1) })
	otherID := createBotTestUser(t, env.db, "usageother")

	if _, err := env.bots.GetBotUsageOverview(userID, "4d", ""); err == nil {
		t.Fatalf("非法 range 应报错")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("非法 range 应为参数错误，实得 %v", err)
	}

	today, err := env.bots.GetBotUsageOverview(userID, "today", "")
	if err != nil {
		t.Fatalf("today: %v", err)
	}
	if len(today.Days) != 1 {
		t.Fatalf("today 应只有 1 个日行，实得 %d", len(today.Days))
	}

	if _, err := env.bots.GetBotUsageOverview(userID, "7d", "not-a-bot"); err == nil {
		t.Fatalf("未知 bot_id 应报错")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrBotNotFound.Code {
		t.Fatalf("他人 bot_id 应为 40424，实得 %v", err)
	}
	if _, err := env.bots.GetBotUsageOverview(otherID, "7d", bot.ID); err == nil {
		t.Fatalf("他人 bot_id 应报错")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrBotNotFound.Code {
		t.Fatalf("他人 bot_id 应为 40424，实得 %v", err)
	}

	if _, err := env.bots.GetBotUsageRunDetail(otherID, run.ID); err == nil {
		t.Fatalf("他人 runId 应报错")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrBotRunNotFound.Code {
		t.Fatalf("他人 runId 应为 40425，实得 %v", err)
	}
	if _, err := env.bots.GetBotUsageRunDetail(userID, uuid.NewString()); err == nil {
		t.Fatalf("未知 runId 应报错")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != errs.ErrBotRunNotFound.Code {
		t.Fatalf("未知 runId 应为 40425，实得 %v", err)
	}
}

// 运行列表投影带显示成本（自身 + 关联维护），且列表只做一次批量维护查询。
func TestBotUsageListRunsDisplayCost(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagelist")
	bot := env.createBot(t, userID)

	priced := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) { r.CostEstimate = ptrCost(0.6) })
	seedUsageMaintenance(t, env, bot.ID, &priced.ID, ptrCost(0.15))
	seedUsageRun(t, env, bot.ID, nil) // 未计价、无维护 → display null

	runs, err := env.bots.ListRuns(userID, bot.ID, 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("应有 2 条 run，实得 %d", len(runs))
	}
	byID := map[string]BotRunResponse{}
	for _, r := range runs {
		byID[r.ID] = r
	}
	if got := byID[priced.ID].DisplayCostEstimate; got == nil || !costEq(*got, 0.75) {
		t.Fatalf("显示成本应为 0.6+0.15，实得 %v", got)
	}
}

// 任务投影的近 7 天成本（F1）：schedule 自身 + 关联维护 + 连带主线；连带
// 主线按总览 linked_main_cost 同口径（自身与关联维护合成，皆空整条不计，
// 主线自身未计价但维护已计价时维护照算）；窗口按用户当地日切，窗口起点前
// 1 分钟的 run 不计（滚动 7×24h 会把它算进来）。列表对最新 run 与维护成本
// 各做一次批量查询：有运行的任务带 last_run，无运行为 null。
func TestBotUsageScheduleTaskCostLast7d(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagetask7d")
	bot := env.createBot(t, userID)
	taskA := seedScheduleTask(t, env, bot.ID, nil)
	taskB := seedScheduleTask(t, env, bot.ID, nil)

	schedA := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := taskA.ID
		r.ScheduleTaskID = &taskID
		r.CostEstimate = ptrCost(0.5)
	})
	seedUsageMaintenance(t, env, bot.ID, &schedA.ID, ptrCost(0.1))
	seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerScheduleEvent
		r.TriggerKey = "schedule_event:" + schedA.ID + ":" + string(model.BotRunStatusCompleted)
		r.CostEstimate = ptrCost(0.4)
	})
	// 任务 B 只有未计价 run：无已计价记录，投影应为缺省 null。
	seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := taskB.ID
		r.ScheduleTaskID = &taskID
	})

	tasks, err := env.bots.ListScheduleTasks(userID, bot.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	byID := map[string]*float64{}
	lastRunOf := map[string]*BotScheduleRunResponse{}
	for i := range tasks {
		byID[tasks[i].ID] = tasks[i].CostLast7d
		lastRunOf[tasks[i].ID] = tasks[i].LastRun
	}
	if got := byID[taskA.ID]; got == nil || !costEq(*got, 1.0) {
		t.Fatalf("任务 A 近 7 天成本应 = 0.5+0.1+0.4 = 1.0，实得 %v", got)
	}
	if got := byID[taskB.ID]; got != nil {
		t.Fatalf("任务 B 无已计价记录应为 null，实得 %v", got)
	}
	if fmt.Sprintf("%T", tasks[0].CostLast7d) != "*float64" {
		t.Fatalf("投影字段类型应为 *float64")
	}
	// 最新 run 整页一次取全：有运行的任务带 last_run，无运行为 null。
	if lr := lastRunOf[taskA.ID]; lr == nil || lr.ID != schedA.ID {
		t.Fatalf("任务 A 应带 last_run（批量反查），实得 %+v", lr)
	}
	if lr := lastRunOf[taskB.ID]; lr == nil {
		t.Fatalf("任务 B 有未计价 run，last_run 不应为 null")
	}
	noRun := seedScheduleTask(t, env, bot.ID, nil)
	tasks, err = env.bots.ListScheduleTasks(userID, bot.ID)
	if err != nil {
		t.Fatalf("list tasks again: %v", err)
	}
	for i := range tasks {
		if tasks[i].ID == noRun.ID && tasks[i].LastRun != nil {
			t.Fatalf("无运行任务的 last_run 应为 null: %+v", tasks[i].LastRun)
		}
	}
}

// 任务近 7 天成本的连带主线口径（F1）：主线自身与关联维护合成——自身未计价
// 但维护已计价时维护照算；窗口按用户当地日切，起点前 1 分钟不计。
func TestBotUsageTaskCostsLinkedMainMaintenance(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagelinkmaint")
	bot := env.createBot(t, userID)
	setUserTimezone(t, env, userID, "Asia/Shanghai")
	task := seedScheduleTask(t, env, bot.ID, nil)

	mkSched := func(cost *float64) *model.BotRun {
		return seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
			r.TriggerSource = model.BotTriggerSchedule
			r.SessionKind = model.BotSessionKindSchedule
			taskID := task.ID
			r.ScheduleTaskID = &taskID
			r.CostEstimate = cost
		})
	}
	schedA := mkSched(ptrCost(0.5))
	schedB := mkSched(nil) // 自身未计价
	// 连带主线带自身成本与维护：0.7 + 0.25 全算。
	linkedA := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerScheduleEvent
		r.TriggerKey = "schedule_event:" + schedA.ID + ":" + string(model.BotRunStatusCompleted)
		r.CostEstimate = ptrCost(0.7)
	})
	seedUsageMaintenance(t, env, bot.ID, &linkedA.ID, ptrCost(0.25))
	// 连带主线自身未计价但维护已计价：维护 0.25 照算（整条跳过是修复前的行为）。
	linkedB := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerScheduleEvent
		r.TriggerKey = "schedule_event:" + schedB.ID + ":" + string(model.BotRunStatusCompleted)
	})
	seedUsageMaintenance(t, env, bot.ID, &linkedB.ID, ptrCost(0.25))

	// 窗口边界：当地 7 天窗口起点前 1 分钟的 schedule run 不计。
	sh, _ := time.LoadLocation("Asia/Shanghai")
	windowStart, _ := botUsageWindow(7, sh, time.Now().UTC())
	mkSchedAt := func(cost *float64, at time.Time) {
		seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
			r.TriggerSource = model.BotTriggerSchedule
			r.SessionKind = model.BotSessionKindSchedule
			taskID := task.ID
			r.ScheduleTaskID = &taskID
			r.CostEstimate = cost
			r.CreatedAt = at
		})
	}
	mkSchedAt(ptrCost(9.9), windowStart.Add(-time.Minute))
	mkSchedAt(ptrCost(0.1), windowStart.Add(time.Minute))

	tasks, err := env.bots.ListScheduleTasks(userID, bot.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("应有 1 个任务，实得 %d", len(tasks))
	}
	got := tasks[0].CostLast7d
	// 0.5 + 0.1（schedule）+ 0.7+0.25 + 0.25（连带主线自身+维护）= 1.8；
	// 窗口外的 9.9 与连带主线的重复计维护都排除。
	if got == nil || !costEq(*got, 1.8) {
		t.Fatalf("近 7 天成本应 = 1.8（含自身未计价的连带主线的维护），实得 %v", got)
	}
}

// 维护成本记日跟维护运行自己的 created_at（F2）：父 run 与维护跨日时，金额
// 落在维护当天、不随父 run 的日期漂移；桶仍跟父走（主线桶），不进维护项。
func TestBotUsageMaintenanceDateFollowsMaintenanceDay(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagemaintday")
	bot := env.createBot(t, userID)
	setUserTimezone(t, env, userID, "Asia/Shanghai")
	sh, _ := time.LoadLocation("Asia/Shanghai")

	// 父主线 run：昨天当地（今天零点前 12 小时），自身未计价。
	parent := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = botUsageDayStart(time.Now().UTC(), sh).Add(-12 * time.Hour)
	})
	// 维护：今天，已计价 0.3。
	seedUsageMaintenance(t, env, bot.ID, &parent.ID, ptrCost(0.3))

	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	today := time.Now().In(sh).Format("2006-01-02")
	yesterday := botUsageDayStart(time.Now().UTC(), sh).In(sh).AddDate(0, 0, -1).Format("2006-01-02")
	dayCost := map[string]float64{}
	for _, d := range overview.Days {
		dayCost[d.Date] = d.Cost
	}
	if !costEq(dayCost[today], 0.3) {
		t.Fatalf("维护金额应记在维护当天 %s，实得 %v", today, dayCost[today])
	}
	if dayCost[yesterday] != 0 {
		t.Fatalf("父 run 所在日 %s 不应再计入维护金额，实得 %v", yesterday, dayCost[yesterday])
	}
	if len(overview.Bots) != 1 {
		t.Fatalf("应有 1 个机器人，实得 %d", len(overview.Bots))
	}
	b := overview.Bots[0]
	if b.CostMain == nil || !costEq(*b.CostMain, 0.3) {
		t.Fatalf("桶跟父 run 走（主线桶）应为 0.3，实得 %v", b.CostMain)
	}
	if b.CostMaintenance != nil {
		t.Fatalf("有父 run 的维护不进维护项，实得 %v", b.CostMaintenance)
	}
	if overview.Totals.Cost == nil || !costEq(*overview.Totals.Cost, 0.3) {
		t.Fatalf("总花费应为 0.3，实得 %v", overview.Totals.Cost)
	}
}

// 父 run 在窗口外（F2）：维护金额按维护日记日、按父桶归桶（含 schedule 父的
// 任务桶）；「今天」与「近 7 天」的同一天金额一致，三桶之和仍等于总花费。
func TestBotUsageMaintenanceFollowsParentOutOfWindow(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagemaintout")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	// 窗口外父主线（10 个当地日前，自身 9.9 在窗口外不计）。
	oldMain := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = time.Now().UTC().AddDate(0, 0, -10)
		r.CostEstimate = ptrCost(9.9)
	})
	// 窗口外父 schedule run（自身 2.0 同样在窗口外）。
	oldSched := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := task.ID
		r.ScheduleTaskID = &taskID
		r.CreatedAt = time.Now().UTC().AddDate(0, 0, -10)
		r.CostEstimate = ptrCost(2.0)
	})
	// 窗口外连带主线（schedule_event 唤醒、trigger_key 指向 oldSched）。
	oldLinked := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerScheduleEvent
		r.TriggerKey = "schedule_event:" + oldSched.ID + ":" + string(model.BotRunStatusCompleted)
		r.CreatedAt = time.Now().UTC().AddDate(0, 0, -10)
		r.CostEstimate = ptrCost(8.8) // 窗口外，自身不计
	})
	// 三笔维护都在今天：按维护日记日，按父桶归桶。
	seedUsageMaintenance(t, env, bot.ID, &oldMain.ID, ptrCost(0.3))
	seedUsageMaintenance(t, env, bot.ID, &oldSched.ID, ptrCost(0.4))
	seedUsageMaintenance(t, env, bot.ID, &oldLinked.ID, ptrCost(0.5))

	seven, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("7d: %v", err)
	}
	if len(seven.Bots) != 1 {
		t.Fatalf("应有 1 个机器人，实得 %d", len(seven.Bots))
	}
	b := seven.Bots[0]
	if b.CostMain == nil || !costEq(*b.CostMain, 0.3) {
		t.Fatalf("窗口外父主线的维护应进主线桶 0.3，实得 %v", b.CostMain)
	}
	if b.CostSchedule == nil || !costEq(*b.CostSchedule, 0.9) {
		t.Fatalf("窗口外父 schedule/连带主线的维护应进定时桶 0.9，实得 %v", b.CostSchedule)
	}
	if b.CostMaintenance != nil {
		t.Fatalf("有父 run 的维护不进维护项，实得 %v", b.CostMaintenance)
	}
	if b.Runs != 0 {
		t.Fatalf("窗口内没有 run 本体，runs 应为 0，实得 %d", b.Runs)
	}
	if len(seven.ScheduleTasks) != 1 {
		t.Fatalf("父任务桶应有 1 个任务，实得 %d", len(seven.ScheduleTasks))
	}
	item := seven.ScheduleTasks[0]
	if item.TaskId != task.ID || item.Cost == nil || !costEq(*item.Cost, 0.4) {
		t.Fatalf("任务桶应记维护成本 0.4: %+v", item)
	}
	if item.LinkedMainCost == nil || !costEq(*item.LinkedMainCost, 0.5) {
		t.Fatalf("任务连带成本应记窗口外连带主线的维护 0.5: %+v", item)
	}
	// 三笔维护都发生在今天：只有今天（末行）有金额，与 days 合计一致。
	var daySum float64
	for _, d := range seven.Days {
		daySum += d.Cost
	}
	if !costEq(seven.Days[len(seven.Days)-1].Cost, 1.2) || !costEq(daySum, 1.2) {
		t.Fatalf("7d 的今天日行应为 1.2 且唯一有金额，实得末行 %v 合计 %v",
			seven.Days[len(seven.Days)-1].Cost, daySum)
	}

	now, err := env.bots.GetBotUsageOverview(userID, "today", "")
	if err != nil {
		t.Fatalf("today: %v", err)
	}
	if len(now.Days) != 1 || !costEq(now.Days[0].Cost, 1.2) {
		t.Fatalf("today 日行应为 1.2（与 7d 同一天一致），实得 %+v", now.Days)
	}
	nb := now.Bots[0]
	if nb.CostMain == nil || !costEq(*nb.CostMain, 0.3) ||
		nb.CostSchedule == nil || !costEq(*nb.CostSchedule, 0.9) {
		t.Fatalf("today 的桶归属应与 7d 一致: %+v", nb)
	}
	if now.Totals.Cost == nil || !costEq(*now.Totals.Cost, 1.2) {
		t.Fatalf("today 总花费应为 1.2，实得 %v", now.Totals.Cost)
	}

	// cost_last_7d 与总览同口径：窗口内发生、父在窗口外的维护同样计入
	// （schedule 父 0.4 + 连带主线父 0.5）。修复前这里返回 null。
	tasks, err := env.bots.ListScheduleTasks(userID, bot.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].CostLast7d == nil || !costEq(*tasks[0].CostLast7d, 0.9) {
		t.Fatalf("cost_last_7d 应为 0.9（含窗口外父的窗口内维护），实得 %+v", tasks)
	}
}

// 月推算的「未来 30 天」是任务时区的 30 个当地日（F10 复审）：now 落在
// 夏令时切换前、截止日落在切换后时，UTC+30 天（固定 720h）会漏掉边界日
// 内的触发；按任务当地日推进才正确。
func TestBotUsageMonthlyTriggerCountLocalDays(t *testing.T) {
	// 2026-10-24T14:00Z = 纽约当地 10:00（EDT）；11-01 切回 EST。每日
	// 09:30 触发：10-25..11-23 共 30 次；按 UTC horizon（11-23 09:00 当地）
	// 会把 11-23 09:30 漏掉计成 29。
	now := time.Date(2026, 10, 24, 14, 0, 0, 0, time.UTC)
	n, ok := botUsageMonthlyTriggerCount("30 9 * * *", "America/New_York", now)
	if !ok || n != 30 {
		t.Fatalf("跨夏令时的 30 当地日应计 30 次触发，实得 %d, ok=%v", n, ok)
	}

	// 钉死迭代时区：正午纽约任务在当地 12:00 触发。now=10-24 10:00 EDT，
	// horizon=11-23 10:00 EST；正确口径 10-24..11-22 共 30 次（11-23 正午
	// =17:00Z 超出 horizon）。若 Next 误按服务器 UTC 迭代，11-23 12:00Z 会
	// 落在 horizon(15:00Z) 内，得出 31。
	n, ok = botUsageMonthlyTriggerCount("0 12 * * *", "America/New_York", now)
	if !ok || n != 30 {
		t.Fatalf("正午纽约任务应按任务时区迭代计 30 次，实得 %d, ok=%v", n, ok)
	}
}

// 反向窗口边界（INT-162 复审 P2）：窗口内父 run + 窗口外维护——维护发生在
// 窗口外，不进桶也不进任何天柱；run 显示口径（单次平均等统计）仍含全量。
// 顺带钉死 totals.cost 与分桶同口径：run 级有价、请求行全未计价时不为 null。
func TestBotUsageMaintenanceBeforeWindowNotCounted(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "usagemaintpre")
	bot := env.createBot(t, userID)

	// 窗口内 run：自身有价 1.0，唯一请求行未计价（成本只在 run 列上）。
	main := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CostEstimate = ptrCost(1.0)
	})
	seedUsageRequest(t, env, main.ID, nil, "hash-unpriced")
	// 10 天前的维护挂窗口内父 run：0.5 不计入窗口记账。
	oldMaint := seedUsageMaintenance(t, env, bot.ID, &main.ID, ptrCost(0.5))
	if err := env.db.Model(&model.BotMaintenanceRun{}).Where("id = ?", oldMaint.ID).
		Update("created_at", time.Now().UTC().AddDate(0, 0, -10)).Error; err != nil {
		t.Fatalf("backdate maintenance: %v", err)
	}

	seven, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("7d: %v", err)
	}
	if len(seven.Bots) != 1 {
		t.Fatalf("应有 1 个机器人，实得 %d", len(seven.Bots))
	}
	b := seven.Bots[0]
	if b.CostMain == nil || !costEq(*b.CostMain, 1.0) {
		t.Fatalf("主线桶只含窗口内自身成本 1.0，实得 %v", b.CostMain)
	}
	if b.CostMaintenance != nil {
		t.Fatalf("有父 run 的维护不进维护桶，实得 %v", b.CostMaintenance)
	}
	// 显示口径：单次平均含全量关联维护（1.0 + 0.5）。
	if b.AvgRunCost == nil || !costEq(*b.AvgRunCost, 1.5) {
		t.Fatalf("avg_run_cost 应为全量显示成本 1.5，实得 %v", b.AvgRunCost)
	}
	if b.Cost == nil || !costEq(*b.Cost, 1.0) {
		t.Fatalf("机器人总成本应为 1.0，实得 %v", b.Cost)
	}
	// totals.cost 不再以 priced 请求行为门槛：run 级有价即输出。
	if seven.Totals.Cost == nil || !costEq(*seven.Totals.Cost, 1.0) {
		t.Fatalf("totals.cost 应为 1.0（run 级价目），实得 %v", seven.Totals.Cost)
	}
	if seven.Totals.PricedRequests != 0 || seven.Totals.UnpricedRequests != 1 {
		t.Fatalf("请求计数应为 0 priced / 1 unpriced，实得 %+v", seven.Totals)
	}
	// Σ按天 = 总花费：窗口外维护不进任何天柱。
	var daySum float64
	for _, d := range seven.Days {
		daySum += d.Cost
	}
	if !costEq(daySum, *seven.Totals.Cost) {
		t.Fatalf("Σ按天 %.4f 应等于总花费 %.4f", daySum, *seven.Totals.Cost)
	}

	// cost_last_7d 同口径：窗口内 schedule run（自身 2.0）挂 10 天前维护
	// 0.5——近 7 天只计 2.0，窗口外维护不摊进来。
	task := seedScheduleTask(t, env, bot.ID, nil)
	sched := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := task.ID
		r.ScheduleTaskID = &taskID
		r.CostEstimate = ptrCost(2.0)
	})
	oldMaint2 := seedUsageMaintenance(t, env, bot.ID, &sched.ID, ptrCost(0.5))
	if err := env.db.Model(&model.BotMaintenanceRun{}).Where("id = ?", oldMaint2.ID).
		Update("created_at", time.Now().UTC().AddDate(0, 0, -10)).Error; err != nil {
		t.Fatalf("backdate maintenance: %v", err)
	}
	tasks, err := env.bots.ListScheduleTasks(userID, bot.ID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].CostLast7d == nil || !costEq(*tasks[0].CostLast7d, 2.0) {
		t.Fatalf("cost_last_7d 应为 2.0（窗口外维护不计），实得 %+v", tasks)
	}
}

package service

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

// INT-164 预算与超限告警：每日/每月预算的花费口径、阈值告警「同一周期同一
// 阈值只告警一次」、上一周期回看兜底、任务预算归属与未设预算零行为。
//
// 预算表不在 repository.BotModels 里（由主代理接线），测试自行建表并补齐
// 部分唯一索引。

func setupBotBudgetEnv(t *testing.T) *botTestEnv {
	t.Helper()
	env := setupBotTestEnv(t)
	if err := env.db.AutoMigrate(repository.BotBudgetModels()...); err != nil {
		t.Fatalf("migrate bot budget: %v", err)
	}
	for _, ddl := range repository.BotBudgetDDL {
		if err := env.db.Exec(ddl).Error; err != nil {
			t.Fatalf("exec bot budget ddl: %v", err)
		}
	}
	return env
}

// seedBudgetRun 直接落一条已终态 run（带已计价成本），绕过运行路径。
func seedBudgetRun(t *testing.T, env *botTestEnv, botID string, cost float64, at time.Time) *model.BotRun {
	t.Helper()
	started := at
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "budget-test-" + uuid.NewString(),
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CostEstimate: &cost,
		CreatedAt: at, StartedAt: &started, FinishedAt: &started,
	}
	if err := env.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return run
}

// seedBudgetScheduleRun 落一条带成本的 schedule run（归某个定时任务）。
func seedBudgetScheduleRun(t *testing.T, env *botTestEnv, botID, taskID string, cost float64, at time.Time) *model.BotRun {
	t.Helper()
	started := at
	taskIDCopy := taskID
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerSchedule, TriggerKey: "budget-sched-" + uuid.NewString(),
		Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindSchedule,
		ScheduleTaskID: &taskIDCopy, ConfigSnapshotJSON: "{}", CostEstimate: &cost,
		CreatedAt: at, StartedAt: &started, FinishedAt: &started,
	}
	if err := env.db.Create(run).Error; err != nil {
		t.Fatalf("seed schedule run: %v", err)
	}
	return run
}

// seedBudgetLinkedMainRun 落一条由 schedule_event 唤醒的主线 run（连带归属到
// 被引用 schedule run 的任务）。
func seedBudgetLinkedMainRun(t *testing.T, env *botTestEnv, botID, refRunID string, cost float64, at time.Time) *model.BotRun {
	t.Helper()
	run := seedBudgetRun(t, env, botID, cost, at)
	if err := env.db.Model(&model.BotRun{}).Where("id = ?", run.ID).Updates(map[string]any{
		"trigger_source": string(model.BotTriggerScheduleEvent),
		"trigger_key":    "schedule_event:" + refRunID + ":completed",
	}).Error; err != nil {
		t.Fatalf("mark linked main: %v", err)
	}
	return run
}

func countBudgetNotifications(t *testing.T, env *botTestEnv, userID string) int64 {
	t.Helper()
	var n int64
	if err := env.db.Model(&model.Notification{}).
		Where("user_id = ? AND kind = ?", userID, model.NotificationBotBudgetThreshold).
		Count(&n).Error; err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	return n
}

func countBudgetMessages(t *testing.T, env *botTestEnv, botID string) int64 {
	t.Helper()
	var n int64
	if err := env.db.Model(&model.BotMessage{}).
		Where("bot_id = ? AND dedup_key LIKE ?", botID, "budget:%").
		Count(&n).Error; err != nil {
		t.Fatalf("count budget messages: %v", err)
	}
	return n
}

func budgetMessageExists(t *testing.T, env *botTestEnv, botID, dedup string) bool {
	t.Helper()
	_, err := repository.NewBotMessageRepository(env.db).FindByDedupKey(botID, dedup)
	return err == nil
}

// TestBotBudgetDailyThresholdAlertsOnce 每日 $1 预算、阈值 80%/100%：花费到
// $0.8 各告警一次（站内通知 + 主线 lifecycle），同日再评估不重复；涨到
// $1.0 再触发 100% 档一次，80% 档仍只有一条。
func TestBotBudgetDailyThresholdAlertsOnce(t *testing.T) {
	env := setupBotBudgetEnv(t)
	userID := createBotTestUser(t, env.db, "budget164daily")
	bot := env.createBot(t, userID)

	// 固定时钟：2026-03-10 12:00 UTC（用户未声明时区 → UTC 当地日）。
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	env.bots.nowFn = func() time.Time { return now }
	seedBudgetRun(t, env, bot.ID, 0.8, now.Add(-time.Hour))

	saved, err := env.bots.UpsertBotBudget(userID, bot.ID, "daily", &BotBudgetUpsertRequest{
		AmountUSD: 1, Thresholds: []float64{1, 0.8, 0.8},
	})
	if err != nil {
		t.Fatalf("upsert budget: %v", err)
	}
	if len(saved.Thresholds) != 2 || saved.Thresholds[0] != 0.8 || saved.Thresholds[1] != 1 {
		t.Fatalf("阈值应规范化去重升序为 [0.8 1]，实得 %v", saved.Thresholds)
	}
	if saved.SpendUSD != 0.8 {
		t.Fatalf("当前周期花费应为 0.8，实得 %v", saved.SpendUSD)
	}
	if saved.PeriodKey != "2026-03-10" {
		t.Fatalf("周期键应为当地日期，实得 %s", saved.PeriodKey)
	}

	env.bots.evalBudgetsOnce(true)
	if n := countBudgetNotifications(t, env, userID); n != 1 {
		t.Fatalf("80%% 阈值应告警 1 次，实得 %d", n)
	}
	if n := countBudgetMessages(t, env, bot.ID); n != 1 {
		t.Fatalf("主线应各 1 条告警消息，实得 %d", n)
	}
	if !budgetMessageExists(t, env, bot.ID, "budget:"+saved.ID+":2026-03-10:8000") {
		t.Fatalf("80%% 档的消息去重键应是整数基点形式")
	}

	// 同日重复评估：不再告警（金额与调用次数都不改变键）。
	env.bots.evalBudgetsOnce(true)
	env.bots.evalBudgetsOnce(true)
	if n := countBudgetNotifications(t, env, userID); n != 1 {
		t.Fatalf("同日重复评估不应重复告警，实得 %d", n)
	}
	if n := countBudgetMessages(t, env, bot.ID); n != 1 {
		t.Fatalf("同日重复评估不应重复追加消息，实得 %d", n)
	}

	// 再花 0.2（累计 1.0）：只补 100% 档一条，80% 档不重复。
	seedBudgetRun(t, env, bot.ID, 0.2, now.Add(-30*time.Minute))
	env.bots.evalBudgetsOnce(true)
	if n := countBudgetNotifications(t, env, userID); n != 2 {
		t.Fatalf("100%% 阈值应再告警一条，实得 %d", n)
	}
	if n := countBudgetMessages(t, env, bot.ID); n != 2 {
		t.Fatalf("主线应有 2 条告警消息，实得 %d", n)
	}
	if !budgetMessageExists(t, env, bot.ID, "budget:"+saved.ID+":2026-03-10:10000") {
		t.Fatalf("100%% 档的消息去重键应存在")
	}
	if n := countBudgetMessages(t, env, bot.ID); n != 2 {
		t.Fatalf("80%% 档不应重复，消息数实得 %d", n)
	}

	// 通知正文必须说明口径与「不停止运行」，并带首次触发快照金额。
	var notice model.Notification
	if err := env.db.Where("user_id = ? AND kind = ?", userID, model.NotificationBotBudgetThreshold).
		Order("created_at ASC").First(&notice).Error; err != nil {
		t.Fatalf("load notification: %v", err)
	}
	if !strings.Contains(notice.Body, "未计价") || !strings.Contains(notice.Body, "不会停止运行") {
		t.Fatalf("通知正文应说明未计价口径与不停止运行: %s", notice.Body)
	}
	if !strings.Contains(notice.Body, "$0.8") || !strings.Contains(notice.Body, "$1") {
		t.Fatalf("通知正文应含首次触发快照金额: %s", notice.Body)
	}
	if !strings.Contains(notice.Title, "每日预算") || !strings.Contains(notice.Title, "80%") {
		t.Fatalf("通知标题应含周期与阈值: %s", notice.Title)
	}
}

// TestBotBudgetMonthlyPreviousPeriodBackfill 每月预算：上一周期已越过阈值、
// 恢复评估时补告警一次（周期键仍是上一周期），当前月不误报。
func TestBotBudgetMonthlyPreviousPeriodBackfill(t *testing.T) {
	env := setupBotBudgetEnv(t)
	userID := createBotTestUser(t, env.db, "budget164monthly")
	bot := env.createBot(t, userID)

	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	env.bots.nowFn = func() time.Time { return now }
	// 二月的一笔花费：当前月窗口为空，只有上一周期回看能看见。
	seedBudgetRun(t, env, bot.ID, 0.9, time.Date(2026, 2, 20, 8, 0, 0, 0, time.UTC))

	saved, err := env.bots.UpsertBotBudget(userID, bot.ID, "monthly", &BotBudgetUpsertRequest{
		AmountUSD: 1, Thresholds: []float64{0.8},
	})
	if err != nil {
		t.Fatalf("upsert budget: %v", err)
	}
	if saved.SpendUSD != 0 {
		t.Fatalf("当前月花费应为 0，实得 %v", saved.SpendUSD)
	}
	if saved.PeriodKey != "2026-03" {
		t.Fatalf("周期键应为 2026-03，实得 %s", saved.PeriodKey)
	}

	env.bots.evalBudgetsOnce(true)
	if n := countBudgetNotifications(t, env, userID); n != 1 {
		t.Fatalf("上一周期越线应补一条告警，实得 %d", n)
	}
	if !budgetMessageExists(t, env, bot.ID, "budget:"+saved.ID+":2026-02:8000") {
		t.Fatalf("上一周期告警的周期键应是 2026-02")
	}
	// 再评估一次不重复。
	env.bots.evalBudgetsOnce(true)
	if n := countBudgetNotifications(t, env, userID); n != 1 {
		t.Fatalf("上一周期告警不应重复，实得 %d", n)
	}
}

// TestBotBudgetPreviousWindowSkippedOnTick 评审回归：纯 tick 轮次
// （includePrev=false）不扫上一周期窗口——该窗口花费只可能因跨界
// straggler 落账而增长，而每笔 usage 落账都会 kick 一轮含回看的评估，
// 纯 tick 扫它是固定开销却没有新数据可看。首轮评估（停摆兜底）仍照常
// 回看。
func TestBotBudgetPreviousWindowSkippedOnTick(t *testing.T) {
	env := setupBotBudgetEnv(t)
	userID := createBotTestUser(t, env.db, "budgetprevskip")
	bot := env.createBot(t, userID)

	// 3 月 10 日（宽限早已过），上一周期二月花费已越阈。
	now := time.Date(2026, 3, 10, 10, 0, 0, 0, time.UTC)
	env.bots.nowFn = func() time.Time { return now }
	seedBudgetRun(t, env, bot.ID, 0.9, time.Date(2026, 2, 20, 8, 0, 0, 0, time.UTC))
	if _, err := env.bots.UpsertBotBudget(userID, bot.ID, "monthly", &BotBudgetUpsertRequest{
		AmountUSD: 1, Thresholds: []float64{0.8},
	}); err != nil {
		t.Fatalf("upsert budget: %v", err)
	}
	// 预算写入应 kick 一轮含回看的评估——运行中途新建的预算也能补一次
	// 上一周期 catch-up，不必等停摆重启。
	select {
	case <-env.bots.budgetKick:
	default:
		t.Fatal("Upsert 后应 kick 一轮含回看的预算评估")
	}

	env.bots.evalBudgetsOnce(false)
	if n := countBudgetNotifications(t, env, userID); n != 0 {
		t.Fatalf("无新落账的 tick 轮次不应扫上一周期，实得 %d 条告警", n)
	}
	env.bots.evalBudgetsOnce(true)
	if n := countBudgetNotifications(t, env, userID); n != 1 {
		t.Fatalf("首轮评估应照常回看上一周期，实得 %d 条告警", n)
	}
}

// TestBotBudgetTaskScopeAndLinkedMain 任务预算只算该任务口径：schedule run +
// schedule_event 连带主线 + 关联维护；告警写进所属机器人主线并带任务名。
func TestBotBudgetTaskScopeAndLinkedMain(t *testing.T) {
	env := setupBotBudgetEnv(t)
	userID := createBotTestUser(t, env.db, "budget164task")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	env.bots.nowFn = func() time.Time { return now }
	schedRun := seedBudgetScheduleRun(t, env, bot.ID, task.ID, 0.5, now.Add(-3*time.Hour))
	seedBudgetLinkedMainRun(t, env, bot.ID, schedRun.ID, 0.3, now.Add(-2*time.Hour))
	// 无关主线花费只进机器人口径，不进任务口径。
	seedBudgetRun(t, env, bot.ID, 5, now.Add(-time.Hour))

	budgets, err := env.bots.ListScheduleTaskBudgets(userID, bot.ID, task.ID)
	if err != nil {
		t.Fatalf("list task budgets: %v", err)
	}
	if len(budgets) != 0 {
		t.Fatalf("未设置时任务预算列表应为空: %v", budgets)
	}
	saved, err := env.bots.UpsertScheduleTaskBudget(userID, bot.ID, task.ID, "daily", &BotBudgetUpsertRequest{
		AmountUSD: 1, Thresholds: []float64{0.8},
	})
	if err != nil {
		t.Fatalf("upsert task budget: %v", err)
	}
	if math.Abs(saved.SpendUSD-0.8) > 1e-9 {
		t.Fatalf("任务花费应只含本任务口径的 0.8，实得 %v", saved.SpendUSD)
	}
	botBudgets, err := env.bots.ListBotBudgets(userID, bot.ID)
	if err != nil {
		t.Fatalf("list bot budgets: %v", err)
	}
	if len(botBudgets) != 0 {
		t.Fatalf("任务预算不应出现在机器人预算里: %v", botBudgets)
	}

	env.bots.evalBudgetsOnce(true)
	if n := countBudgetNotifications(t, env, userID); n != 1 {
		t.Fatalf("任务预算越线应告警一次，实得 %d", n)
	}
	msg, err := repository.NewBotMessageRepository(env.db).FindByDedupKey(bot.ID, "budget:"+saved.ID+":2026-03-10:8000")
	if err != nil {
		t.Fatalf("任务预算告警应写进所属机器人主线: %v", err)
	}
	if !strings.Contains(msg.ContentJSON, "schedule_task") ||
		!strings.Contains(msg.ContentJSON, task.Name) {
		t.Fatalf("任务预算告警应标明任务归属与任务名: %s", msg.ContentJSON)
	}
}

// TestBotBudgetLateParentMaintenance 评审回归：窗口内维护、父 run 在窗口外
// ——维护花费按它自己的 created_at 进 BotUSD，任务归属也要按父 run 补上
// （与花费页 lateParent 同口径）；修复前这部分在 TaskUSD 里被静默丢掉，
// 跨午夜收尾的维护会让日预算永远少算。
func TestBotBudgetLateParentMaintenance(t *testing.T) {
	env := setupBotBudgetEnv(t)
	userID := createBotTestUser(t, env.db, "budgetlate")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	day := time.Now().UTC().Truncate(24 * time.Hour)
	start, end := day, day.Add(24*time.Hour)
	// 父 schedule run 在窗口外（昨天），自身 2.0 不进本窗口任何口径。
	parent := seedBudgetScheduleRun(t, env, bot.ID, task.ID, 2.0, start.Add(-time.Hour))
	maint := seedUsageMaintenance(t, env, bot.ID, &parent.ID, ptrCost(0.6))
	if err := env.db.Model(&model.BotMaintenanceRun{}).Where("id = ?", maint.ID).
		Update("created_at", start.Add(30*time.Minute)).Error; err != nil {
		t.Fatalf("place maintenance in window: %v", err)
	}

	spend, err := repository.NewBotBudgetRepository(env.db).
		SpendInWindow([]string{bot.ID}, start, end)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if !costEq(spend.BotUSD[bot.ID], 0.6) {
		t.Fatalf("窗口内维护应进 BotUSD 0.6，实得 %v", spend.BotUSD)
	}
	if !costEq(spend.TaskUSD[task.ID], 0.6) {
		t.Fatalf("维护应按窗口外父 run 归到任务 0.6，实得 %v", spend.TaskUSD)
	}
}

// TestBotBudgetUnpricedRequestsFallback 评审回归：run 有请求行但全未计价
// （provider_cost 与 cost_estimate 均空）时，花费聚合必须回退 run 冻结
// 快照——与全站 runDisplayOwn 同口径；修复前请求行存在即吞掉快照，存量
// 「成本只在 run 列上」的 run 会让预算花费永远少算。
func TestBotBudgetUnpricedRequestsFallback(t *testing.T) {
	env := setupBotBudgetEnv(t)
	userID := createBotTestUser(t, env.db, "budgetunpriced")
	bot := env.createBot(t, userID)

	day := time.Now().UTC().Truncate(24 * time.Hour)
	start, end := day, day.Add(24*time.Hour)
	run := seedBudgetRun(t, env, bot.ID, 1.5, start.Add(time.Hour))
	now := time.Now().UTC()
	// 请求行存在但两个金额字段都空：SUM 出 nil，不是「无请求行」。
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, PromptRevision: 1,
		ToolsetVersion: botToolsetVersion,
		StartedAt:      now, FinishedAt: &now,
	}
	if err := env.db.Create(req).Error; err != nil {
		t.Fatalf("seed unpriced request: %v", err)
	}

	spend, err := repository.NewBotBudgetRepository(env.db).
		SpendInWindow([]string{bot.ID}, start, end)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if !costEq(spend.BotUSD[bot.ID], 1.5) {
		t.Fatalf("请求行全未计价应回退 run 快照 1.5，实得 %v", spend.BotUSD)
	}
}

// TestBotBudgetSchemaConstraints 表约束：目标恰好其一非空、同目标同周期至多
// 一条（部分唯一索引）、目标任务删除时预算级联消失。
func TestBotBudgetSchemaConstraints(t *testing.T) {
	env := setupBotBudgetEnv(t)
	userID := createBotTestUser(t, env.db, "budget164schema")
	bot := env.createBot(t, userID)
	now := time.Now().UTC()

	newBudget := func() *model.BotBudget {
		botID := bot.ID
		return &model.BotBudget{
			ID: uuid.NewString(), UserID: userID, BotID: &botID,
			Period: model.BotBudgetPeriodDaily, AmountUSD: 1, Thresholds: "[0.8]",
			CreatedAt: now, UpdatedAt: now,
		}
	}
	first := newBudget()
	if err := env.db.Create(first).Error; err != nil {
		t.Fatalf("首次插入应成功: %v", err)
	}
	if err := env.db.Create(newBudget()).Error; err == nil {
		t.Fatalf("同目标同周期的第二条预算应被部分唯一索引拒绝")
	}

	both := newBudget()
	both.Period = model.BotBudgetPeriodMonthly
	taskID := uuid.NewString()
	both.ScheduleTaskID = &taskID
	if err := env.db.Create(both).Error; err == nil {
		t.Fatalf("bot_id 与 schedule_task_id 同时非空应被 check 拒绝")
	}

	none := newBudget()
	none.Period = model.BotBudgetPeriodMonthly
	none.BotID = nil
	if err := env.db.Create(none).Error; err == nil {
		t.Fatalf("目标两列都空应被 check 拒绝")
	}

	// 删机器人：预算随外键级联删除。
	if err := env.db.Delete(&model.Bot{}, "id = ?", bot.ID).Error; err != nil {
		t.Fatalf("delete bot: %v", err)
	}
	var left int64
	if err := env.db.Model(&model.BotBudget{}).Where("bot_id = ?", bot.ID).Count(&left).Error; err != nil {
		t.Fatalf("count budgets: %v", err)
	}
	if left != 0 {
		t.Fatalf("机器人删除后预算应级联删除，实得 %d 条", left)
	}
}

// TestBotBudgetUnsetKeepsBehavior 未设置预算时评估循环不产生任何通知或消息。
func TestBotBudgetUnsetKeepsBehavior(t *testing.T) {
	env := setupBotBudgetEnv(t)
	userID := createBotTestUser(t, env.db, "budget164none")
	bot := env.createBot(t, userID)
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	env.bots.nowFn = func() time.Time { return now }
	seedBudgetRun(t, env, bot.ID, 42, now.Add(-time.Minute))

	env.bots.evalBudgetsOnce(true)
	if n := countBudgetNotifications(t, env, userID); n != 0 {
		t.Fatalf("未设预算不应产生通知，实得 %d", n)
	}
	if n := countBudgetMessages(t, env, bot.ID); n != 0 {
		t.Fatalf("未设预算不应产生主线消息，实得 %d", n)
	}
	// 机器人预算列表为空，且不影响既有读取路径。
	budgets, err := env.bots.ListBotBudgets(userID, bot.ID)
	if err != nil {
		t.Fatalf("list bot budgets: %v", err)
	}
	if len(budgets) != 0 {
		t.Fatalf("未设预算列表应为空: %v", budgets)
	}
}

// TestBotBudgetThresholdNormalization 阈值规范化与告警键的整数基点形式：
// 去重升序、值域校验、精度下限，且同一档位的告警键与金额无关。
func TestBotBudgetThresholdNormalization(t *testing.T) {
	normalized, raw, err := botBudgetNormalizeThresholds([]float64{1, 0.8, 0.8, 0.80000001, 2})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []float64{0.8, 1, 2}
	if len(normalized) != len(want) {
		t.Fatalf("规范化结果 %v 应为 %v", normalized, want)
	}
	for i := range want {
		if normalized[i] != want[i] {
			t.Fatalf("规范化结果 %v 应为 %v", normalized, want)
		}
	}
	if raw != "[0.8,1,2]" {
		t.Fatalf("库内阈值文本应为 [0.8,1,2]，实得 %s", raw)
	}

	invalid := [][]float64{
		nil,
		{},
		{0},
		{-0.5},
		{100.5},
		{math.NaN()},
		{math.Inf(1)},
	}
	for _, in := range invalid {
		if _, _, err := botBudgetNormalizeThresholds(in); err == nil {
			t.Fatalf("非法阈值 %v 应被拒绝", in)
		}
	}

	// 告警键与金额无关：同一预算同一档位不同花费得到同一个键。
	if a, b := botBudgetThresholdBasisPoints(0.8), botBudgetThresholdBasisPoints(0.80000001); a != b {
		t.Fatalf("浮点邻近值应归一到同一基点: %d vs %d", a, b)
	}
	if bp := botBudgetThresholdBasisPoints(0.805); bp != 8050 {
		t.Fatalf("80.5%% 应为 8050 基点，实得 %d", bp)
	}
	if !botBudgetThresholdReached(1, 0.8, 0.8) {
		t.Fatalf("恰好等于阈值应触发")
	}
	if botBudgetThresholdReached(1, 0.8, 0.7999) {
		t.Fatalf("低于阈值不应触发")
	}
}

// TestBotBudgetRejectsInvalidInput 服务层参数校验：金额与周期非法都返回
// 40001，任务预算要求 task 归属本机器人。
func TestBotBudgetRejectsInvalidInput(t *testing.T) {
	env := setupBotBudgetEnv(t)
	userID := createBotTestUser(t, env.db, "budget164invalid")
	bot := env.createBot(t, userID)
	otherBot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	if _, err := env.bots.UpsertBotBudget(userID, bot.ID, "weekly", &BotBudgetUpsertRequest{
		AmountUSD: 1, Thresholds: []float64{0.8},
	}); err == nil {
		t.Fatalf("非法周期应被拒绝")
	}
	if _, err := env.bots.UpsertBotBudget(userID, bot.ID, "daily", &BotBudgetUpsertRequest{
		AmountUSD: 0, Thresholds: []float64{0.8},
	}); err == nil {
		t.Fatalf("金额 0 应被拒绝")
	}
	if _, err := env.bots.UpsertBotBudget(userID, bot.ID, "daily", &BotBudgetUpsertRequest{
		AmountUSD: 1,
	}); err == nil {
		t.Fatalf("空阈值应被拒绝")
	}
	if _, err := env.bots.UpsertScheduleTaskBudget(userID, otherBot.ID, task.ID, "daily", &BotBudgetUpsertRequest{
		AmountUSD: 1, Thresholds: []float64{0.8},
	}); err == nil {
		t.Fatalf("任务不属于该机器人时应拒绝")
	}
	// 他人机器人一律 40424。
	otherUserID := createBotTestUser(t, env.db, "budget164other")
	if _, err := env.bots.ListBotBudgets(otherUserID, bot.ID); err == nil {
		t.Fatalf("他人机器人应 404")
	}
}

func TestBotBudgetActualText(t *testing.T) {
	for pct, want := range map[float64]string{
		0:    "0%",
		328:  "328%",
		999:  "999%",
		1000: "约 10 倍",
		4200: "约 42 倍",
	} {
		if got := botBudgetActualText(pct); got != want {
			t.Fatalf("pct=%v 期望 %q 实得 %q", pct, want, got)
		}
	}
}

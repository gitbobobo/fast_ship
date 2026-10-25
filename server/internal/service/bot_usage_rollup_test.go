package service

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// 花费页按天汇总（INT-164）服务层测试：注入时钟下汇总=实时、幂等、脏日重算、
// NULL 语义、删除保留历史（含当天）、活动维护阻塞删除、时区变更重算。

// usageOverviewJSON 用 JSON 序列化整个总览做全维度等值比较：bots/days/
// schedule_tasks/totals 各维度的任何漂移都会变成文本差异。
func usageOverviewJSON(t *testing.T, env *botTestEnv, userID, rangeParam, botID string) string {
	t.Helper()
	overview, err := env.bots.GetBotUsageOverview(userID, rangeParam, botID)
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	out, err := json.Marshal(overview)
	if err != nil {
		t.Fatalf("marshal overview: %v", err)
	}
	return string(out)
}

// usageDayAt 返回「fixed 之前 daysAgo 个 UTC 日」当天下午 15:00 的时刻——
// 测试数据统一按 UTC 记日（测试里用户时区显式设为 UTC）。
func usageDayAt(fixed time.Time, daysAgo int) time.Time {
	return botUsageDayStart(fixed, time.UTC).Add(-time.Duration(daysAgo) * 24 * time.Hour).Add(15 * time.Hour)
}

// usageMoveMaint 把维护运行的记日移到指定时刻（创建路径没有 created_at
// 形参，跨日维护场景先 seed 再回写）。
func usageMoveMaint(t *testing.T, env *botTestEnv, maintID string, createdAt time.Time) {
	t.Helper()
	if err := env.db.Model(&model.BotMaintenanceRun{}).Where("id = ?", maintID).
		Update("created_at", createdAt).Error; err != nil {
		t.Fatalf("move maintenance: %v", err)
	}
}

// dailyContent 返回该 bot 汇总行的可比较稳定内容（id/rolled_at 每次重算
// 都会变，剥离后比对口径列）。
func dailyContent(t *testing.T, env *botTestEnv, botID string) string {
	t.Helper()
	var rows []model.BotUsageDaily
	if err := env.db.Where("bot_id = ?", botID).
		Order("date, kind, schedule_task_id, requests, runs").
		Find(&rows).Error; err != nil {
		t.Fatalf("list daily rows: %v", err)
	}
	for i := range rows {
		rows[i].ID = ""
		rows[i].RolledAt = time.Time{}
	}
	out, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal daily: %v", err)
	}
	return string(out)
}

// usageCoveredDates 返回某 bot 在指定日期集合上当前判「覆盖」的映射
// （标记存在 + timezone 一致 + 明细指纹吻合）。
func usageCoveredDates(t *testing.T, env *botTestEnv, botID string, dates []string, loc *time.Location, tz string) map[string]bool {
	t.Helper()
	covered, err := env.bots.usageRollupCovered([]string{botID}, dates, loc, tz)
	if err != nil {
		t.Fatalf("usageRollupCovered: %v", err)
	}
	return covered[botID]
}

// 混合窗口里全维度等值：先以「无汇总」状态取实时基线（daily 表为空时混合
// 读取退化回明细扫描），跑一轮汇总后再取——覆盖日走 bot_usage_daily、今天
// 走实时，输出必须与基线逐字节一致。
func TestBotUsageRollupMatchesRealtime(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "rollupmix")
	setUserTimezone(t, env, userID, "UTC")
	bot := env.createBot(t, userID)
	bot2 := env.createBot(t, userID)

	fixed := time.Now().UTC().Truncate(time.Hour)
	env.bots.nowFn = func() time.Time { return fixed }

	// 前天：主线 run（已计价 + 未计价请求 + reasoning/provider USD）+ 同日
	// 关联维护。
	main1 := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 2)
		r.CostEstimate = ptrCost(1.0)
	})
	req := seedUsageRequest(t, env, main1.ID, ptrCost(0.6), "mix-1")
	reasoning := int64(42)
	pc := 0.5
	// provider_cost 列只落置信 USD 的值（写入侧口径），直接赋值即可。
	if err := env.db.Model(&model.BotModelRequest{}).Where("id = ?", req.ID).
		Updates(map[string]interface{}{
			"reasoning_tokens": reasoning,
			"provider_cost":    pc,
		}).Error; err != nil {
		t.Fatalf("seed reasoning/provider: %v", err)
	}
	seedUsageRequest(t, env, main1.ID, nil, "mix-2")
	usageMoveMaint(t, env, seedUsageMaintenance(t, env, bot.ID, &main1.ID, ptrCost(0.4)).ID,
		usageDayAt(fixed, 2).Add(2*time.Hour))

	// 昨天：定时 run（成功/失败/跳过）+ 已解析 schedule_event 连带 +
	// 解析不出的 event 主线 + 无父维护。
	task := seedScheduleTask(t, env, bot.ID, nil)
	mkSched := func(status model.BotRunStatus, cost *float64) *model.BotRun {
		return seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
			r.CreatedAt = usageDayAt(fixed, 1)
			r.TriggerSource = model.BotTriggerSchedule
			r.SessionKind = model.BotSessionKindSchedule
			taskID := task.ID
			r.ScheduleTaskID = &taskID
			r.Status = status
			r.CostEstimate = cost
		})
	}
	sched := mkSched(model.BotRunStatusCompleted, ptrCost(2.0))
	mkSched(model.BotRunStatusFailed, ptrCost(0.05))
	mkSched(model.BotRunStatusSkipped, nil)
	seedUsageRequest(t, env, sched.ID, ptrCost(2.0), "mix-3")
	linked := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 1)
		r.TriggerSource = model.BotTriggerScheduleEvent
		r.TriggerKey = "schedule_event:" + sched.ID + ":" + string(model.BotRunStatusCompleted)
		r.CostEstimate = ptrCost(0.7)
	})
	seedUsageRequest(t, env, linked.ID, ptrCost(0.7), "mix-4")
	orphan := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 1)
		r.TriggerSource = model.BotTriggerScheduleEvent
		r.TriggerKey = "schedule_event:" + uuid.NewString() + ":completed"
		r.CostEstimate = ptrCost(0.1)
	})
	seedUsageRequest(t, env, orphan.ID, ptrCost(0.1), "mix-5")
	usageMoveMaint(t, env, seedUsageMaintenance(t, env, bot.ID, nil, ptrCost(3.0)).ID,
		usageDayAt(fixed, 1).Add(time.Hour))
	// 跨日维护：今天创建、父 run 在昨天——成本记今天、桶跟父（实时路径里
	// 今天不读汇总表，这行给「汇总+实时混合」的对照组提供维护维度）。
	usageMoveMaint(t, env, seedUsageMaintenance(t, env, bot.ID, &sched.ID, ptrCost(0.25)).ID,
		usageDayAt(fixed, 0))
	// 今天：主线 run。
	todayRun := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 0)
		r.CostEstimate = ptrCost(0.9)
	})
	seedUsageRequest(t, env, todayRun.ID, ptrCost(0.9), "mix-6")
	// 第二个 bot：另一条任务上的定时 run（多 bot/多任务覆盖）。
	task2 := seedScheduleTask(t, env, bot2.ID, nil)
	sched2 := seedUsageRun(t, env, bot2.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 1)
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := task2.ID
		r.ScheduleTaskID = &taskID
		r.CostEstimate = ptrCost(1.2)
	})
	seedUsageRequest(t, env, sched2.ID, ptrCost(1.2), "mix-7")

	for _, rangeParam := range []string{"today", "7d", "30d"} {
		base := usageOverviewJSON(t, env, userID, rangeParam, "")
		env.bots.scanUsageRollupOnce()
		got := usageOverviewJSON(t, env, userID, rangeParam, "")
		if got != base {
			t.Fatalf("range=%s 汇总后总览与纯实时不一致:\nbase=%s\ngot =%s", rangeParam, base, got)
		}
	}
	// bot_id 收窄视图也走混合：汇总行只折覆盖日，今天仍实时。
	base := usageOverviewJSON(t, env, userID, "7d", bot.ID)
	env.bots.scanUsageRollupOnce()
	if got := usageOverviewJSON(t, env, userID, "7d", bot.ID); got != base {
		t.Fatalf("bot_id 收窄视图不一致:\nbase=%s\ngot =%s", base, got)
	}

	// 覆盖断言：昨天的标记必须存在且指纹新鲜——上面的相等不能只是
	// 「全部回退实时」的同义反复。
	var marks int64
	if err := env.db.Model(&model.BotUsageRollupDay{}).Where("bot_id = ?", bot.ID).Count(&marks).Error; err != nil {
		t.Fatalf("count marks: %v", err)
	}
	if marks == 0 {
		t.Fatalf("汇总应已写覆盖标记")
	}
	yesterday := botUsageDayStart(fixed, time.UTC).Add(-24 * time.Hour).Format("2006-01-02")
	if !usageCoveredDates(t, env, bot.ID, []string{yesterday}, time.UTC, "UTC")[yesterday] {
		t.Fatalf("昨天应判为已覆盖")
	}
	// 昨天的 schedule 桶行：3 个 run（成/败/跳过）、1 条已计价请求、任务与
	// 机器人名快照。
	var row model.BotUsageDaily
	if err := env.db.Where("bot_id = ? AND date = ? AND kind = ?", bot.ID, yesterday, model.BotUsageDaySchedule).
		First(&row).Error; err != nil {
		t.Fatalf("daily schedule row: %v", err)
	}
	if row.TaskName != task.Name || row.BotName != bot.Name {
		t.Fatalf("快照名不符: %+v", row)
	}
	if row.Requests != 1 || row.Runs != 3 || row.Failed != 1 || row.Skipped != 1 {
		t.Fatalf("计数不符: %+v", row)
	}
	if row.ReasoningTokens != nil {
		t.Fatalf("schedule 桶无 reasoning 明细时应保持 NULL: %+v", row.ReasoningTokens)
	}
}

// 汇总幂等：重复跑同一段时间产出同样的行内容（id/rolled_at 除外）；迟到
// 写入（旧 run 新增请求行）触发脏日判定与重算。
func TestBotUsageRollupIdempotentAndDirtyDay(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "rollupidem")
	setUserTimezone(t, env, userID, "UTC")
	bot := env.createBot(t, userID)
	fixed := time.Now().UTC().Truncate(time.Hour)
	env.bots.nowFn = func() time.Time { return fixed }

	run := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 2)
		r.CostEstimate = ptrCost(0.5)
	})
	seedUsageRequest(t, env, run.ID, ptrCost(0.5), "idem-1")

	env.bots.scanUsageRollupOnce()
	first := dailyContent(t, env, bot.ID)
	env.bots.scanUsageRollupOnce()
	if got := dailyContent(t, env, bot.ID); got != first {
		t.Fatalf("重复汇总不幂等:\nfirst=%s\nagain=%s", first, got)
	}

	// 迟到写入：给前天的 run 补一条请求。该 run 的请求计数变了——指纹
	// 立即不符，该日判脏、回退实时；重扫后重新覆盖且行内容更新。
	day := usageDayAt(fixed, 2).Format("2006-01-02")
	seedUsageRequest(t, env, run.ID, ptrCost(0.9), "idem-late")
	if usageCoveredDates(t, env, bot.ID, []string{day}, time.UTC, "UTC")[day] {
		t.Fatalf("迟到写入后该日应判脏")
	}
	dirty := usageOverviewJSON(t, env, userID, "7d", "")
	env.bots.scanUsageRollupOnce()
	if !usageCoveredDates(t, env, bot.ID, []string{day}, time.UTC, "UTC")[day] {
		t.Fatalf("重算后该日应重新判覆盖")
	}
	if got := usageOverviewJSON(t, env, userID, "7d", ""); got != dirty {
		t.Fatalf("脏日重算后总览不应变化:\ndirty=%s\ngot  =%s", dirty, got)
	}
	if second := dailyContent(t, env, bot.ID); second == first {
		t.Fatalf("迟到写入后汇总行应变化")
	}
}

// NULL 语义（缺失 ≠ 0）：全未计价的日，daily 行的金额/token 可空列保持
// NULL，总览同样不给 0。
func TestBotUsageRollupNullSemantics(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "rollupnull")
	setUserTimezone(t, env, userID, "UTC")
	bot := env.createBot(t, userID)
	fixed := time.Now().UTC().Truncate(time.Hour)
	env.bots.nowFn = func() time.Time { return fixed }

	run := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 1)
	})
	seedUsageRequest(t, env, run.ID, nil, "null-1")
	usageMoveMaint(t, env, seedUsageMaintenance(t, env, bot.ID, nil, nil).ID,
		usageDayAt(fixed, 1).Add(time.Hour))

	env.bots.scanUsageRollupOnce()

	yesterday := botUsageDayStart(fixed, time.UTC).Add(-24 * time.Hour).Format("2006-01-02")
	var rows []model.BotUsageDaily
	if err := env.db.Where("bot_id = ? AND date = ?", bot.ID, yesterday).Find(&rows).Error; err != nil {
		t.Fatalf("list daily: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("昨天应有汇总行")
	}
	var totalReq, totalUnpriced int64
	for _, row := range rows {
		if row.DisplayCost != nil || row.ReasoningTokens != nil {
			t.Fatalf("全未计价时金额/reasoning 应保持 NULL: %+v", row)
		}
		totalReq += row.Requests
		totalUnpriced += row.UnpricedRequests
	}
	if totalReq != 2 || totalUnpriced != 2 {
		t.Fatalf("请求计数应为 2/2（1 主线 + 1 维护），实得 %d/%d", totalReq, totalUnpriced)
	}
	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	b := overview.Bots[0]
	if b.Cost != nil || b.CostMain != nil || b.CostMaintenance != nil {
		t.Fatalf("全未计价时金额字段应为 null: %+v", b)
	}
	if overview.Totals.Cost != nil || overview.Totals.DailyAverage != nil {
		t.Fatalf("totals 金额应全为 null: %+v", overview.Totals)
	}
}

// 删除机器人：事务外补齐 + 事务内补今天，历史（含当天部分）全部留在汇总
// 表；总览继续展示原名与 deleted=true。
func TestBotUsageDeletePreservesUsageHistory(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "rollupdel")
	setUserTimezone(t, env, userID, "UTC")
	bot := env.createBot(t, userID)
	fixed := time.Now().UTC().Truncate(time.Hour)
	env.bots.nowFn = func() time.Time { return fixed }

	task := seedScheduleTask(t, env, bot.ID, nil)
	sched := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 1)
		r.TriggerSource = model.BotTriggerSchedule
		r.SessionKind = model.BotSessionKindSchedule
		taskID := task.ID
		r.ScheduleTaskID = &taskID
		r.CostEstimate = ptrCost(2.0)
	})
	seedUsageRequest(t, env, sched.ID, ptrCost(2.0), "del-1")
	todayRun := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 0)
		r.CostEstimate = ptrCost(0.9)
	})
	seedUsageRequest(t, env, todayRun.ID, ptrCost(0.9), "del-2")

	if err := env.bots.Delete(userID, bot.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// 明细账本已级联清理——之后的一切展示只能来自汇总表。
	var ledger int64
	env.db.Model(&model.BotRun{}).Where("bot_id = ?", bot.ID).Count(&ledger)
	if ledger != 0 {
		t.Fatalf("明细应已级联删除")
	}
	today := botUsageDayStart(fixed, time.UTC).Format("2006-01-02")
	var todayRows int64
	env.db.Model(&model.BotUsageDaily{}).Where("bot_id = ? AND date = ?", bot.ID, today).Count(&todayRows)
	if todayRows == 0 {
		t.Fatalf("删除路径应补齐当天的部分快照")
	}

	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if len(overview.Bots) != 1 {
		t.Fatalf("已删除机器人应仍在列表里，实得 %d", len(overview.Bots))
	}
	b := overview.Bots[0]
	if b.Name != bot.Name {
		t.Fatalf("名称快照应为 %q，实得 %q", bot.Name, b.Name)
	}
	if b.Deleted == nil || !*b.Deleted {
		t.Fatalf("deleted 应为 true: %+v", b)
	}
	if b.Cost == nil || !costEq(*b.Cost, 2.9) {
		t.Fatalf("已删除机器人的历史成本应为 2.9，实得 %v", b.Cost)
	}
	if overview.Totals.Cost == nil || !costEq(*overview.Totals.Cost, 2.9) {
		t.Fatalf("totals 应保留已删除机器人的历史: %v", overview.Totals)
	}
	// 按天分列：今天行包含该 bot 的 0.9（删除事务内补的当天快照）。
	var todayDay *api.BotUsageDay
	for i := range overview.Days {
		if overview.Days[i].Date == today {
			todayDay = &overview.Days[i]
		}
	}
	if todayDay == nil || !costEq(todayDay.Cost, 0.9) {
		t.Fatalf("今天行应含删除前的 0.9: %+v", overview.Days)
	}
	// 任务行（属于已删除 bot）也随汇总继续展示。
	if len(overview.ScheduleTasks) != 1 || overview.ScheduleTasks[0].BotName != bot.Name {
		t.Fatalf("已删除机器人的任务行应保留原名: %+v", overview.ScheduleTasks)
	}
	if !overview.ScheduleTasks[0].Deleted {
		t.Fatalf("任务随 bot 删除应标 deleted")
	}
}

// 活动维护运行（finished_at IS NULL）与活动 run 一样阻塞删除。
func TestBotUsageDeleteConflictActiveMaintenance(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "rollupmaint")
	bot := env.createBot(t, userID)
	maint := seedUsageMaintenance(t, env, bot.ID, nil, nil)
	if err := env.db.Model(&model.BotMaintenanceRun{}).Where("id = ?", maint.ID).
		Update("finished_at", nil).Error; err != nil {
		t.Fatalf("reopen maintenance: %v", err)
	}
	if err := env.bots.Delete(userID, bot.ID); err != errs.ErrBotDeleteConflict {
		t.Fatalf("活动维护运行应阻塞删除，实得 %v", err)
	}
}

// 时区变更：已有汇总按旧有效时区记日，标记上 timezone 与当前不一致 →
// 整表清空重算，date 按新时区重排，明细不变。
func TestBotUsageRollupTimezoneRecompute(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "rolluptz")
	setUserTimezone(t, env, userID, "UTC")
	bot := env.createBot(t, userID)
	// 注入时钟固定在 UTC 凌晨 02:00——东京（+9）已是当天上午 11 点。
	fixed := time.Now().UTC().Truncate(24 * time.Hour).Add(2 * time.Hour)
	env.bots.nowFn = func() time.Time { return fixed }

	// run 记日：UTC 大前天 20:00（UTC+9 已是昨天 05:00）——两个时区把它记到
	// 不同的日子，且两侧都在「昨天或更早」的可汇总范围里。
	created := botUsageDayStart(fixed, time.UTC).Add(-28 * time.Hour)
	run := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = created
		r.CostEstimate = ptrCost(1.0)
	})
	seedUsageRequest(t, env, run.ID, ptrCost(1.0), "tz-1")

	env.bots.scanUsageRollupOnce()
	utcDate := created.UTC().Format("2006-01-02")
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	tokyoDate := created.In(tokyo).Format("2006-01-02")
	if tokyoDate == utcDate {
		t.Fatalf("测试构造失效：该时刻在两个时区同日期")
	}
	var row model.BotUsageDaily
	if err := env.db.Where("bot_id = ?", bot.ID).First(&row).Error; err != nil {
		t.Fatalf("daily row: %v", err)
	}
	if row.Date != utcDate {
		t.Fatalf("UTC 口径下应记 %s，实得 %s", utcDate, row.Date)
	}

	// 有效时区改为东京：标记上的 timezone 被判脏 → 整表清空重算。判脏期间
	// 的读取走实时（基线取自重算前），重算后与实时必须一致。
	setUserTimezone(t, env, userID, "Asia/Tokyo")
	base := usageOverviewJSON(t, env, userID, "7d", "")
	env.bots.scanUsageRollupOnce()
	if got := usageOverviewJSON(t, env, userID, "7d", ""); got != base {
		t.Fatalf("东京口径重算后与实时不一致:\nbase=%s\ngot =%s", base, got)
	}
	var rows []model.BotUsageDaily
	if err := env.db.Where("bot_id = ?", bot.ID).Find(&rows).Error; err != nil {
		t.Fatalf("list daily: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("重算后应恰好一行，实得 %+v", rows)
	}
	if rows[0].Date != tokyoDate {
		t.Fatalf("新时区应记 %s，实得 %s", tokyoDate, rows[0].Date)
	}
	var marks []model.BotUsageRollupDay
	if err := env.db.Where("bot_id = ?", bot.ID).Find(&marks).Error; err != nil {
		t.Fatalf("list marks: %v", err)
	}
	for _, m := range marks {
		if m.Timezone != "Asia/Tokyo" {
			t.Fatalf("标记时区应为 Asia/Tokyo，实得 %s", m.Timezone)
		}
	}
}

// 任务名快照续命：任务删除后重滚同一天，汇总行的 task_name 沿用上一版快照
// 不被冲掉；总览的已删任务行经快照名投影（评审回归——列若只写不读，删除
// 后任务名照旧丢）。
func TestBotUsageRollupTaskNameSurvivesDelete(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "tasknamedel")
	setUserTimezone(t, env, userID, "UTC")
	bot := env.createBot(t, userID)

	fixed := time.Now().UTC().Truncate(time.Hour)
	env.bots.nowFn = func() time.Time { return fixed }

	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = usageDayAt(fixed, 1)
		r.SessionKind = model.BotSessionKindSchedule
		r.ScheduleTaskID = &task.ID
		r.CostEstimate = ptrCost(1.0)
	})
	seedUsageRequest(t, env, run.ID, ptrCost(1.0), "tnd-1")

	if err := env.bots.rollupBotUsage(bot.ID, fixed, true, false); err != nil {
		t.Fatalf("first rollup: %v", err)
	}
	yesterday := usageDayAt(fixed, 1).Format("2006-01-02")
	var row model.BotUsageDaily
	if err := env.db.Where("bot_id = ? AND date = ? AND kind = ?",
		bot.ID, yesterday, model.BotUsageDaySchedule).First(&row).Error; err != nil {
		t.Fatalf("first daily row: %v", err)
	}
	if row.TaskName != task.Name {
		t.Fatalf("首滚应记任务名快照: %+v", row)
	}

	// 删任务并重滚（recent 2 日无条件重算）：名字沿用旧快照。
	if err := env.db.Delete(&model.BotScheduleTask{}, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("delete task: %v", err)
	}
	if err := env.bots.rollupBotUsage(bot.ID, fixed, true, false); err != nil {
		t.Fatalf("re-rollup: %v", err)
	}
	var row2 model.BotUsageDaily
	if err := env.db.Where("bot_id = ? AND date = ? AND kind = ?",
		bot.ID, yesterday, model.BotUsageDaySchedule).First(&row2).Error; err != nil {
		t.Fatalf("re-rolled daily row: %v", err)
	}
	if row2.TaskName != task.Name {
		t.Fatalf("重滚不得冲掉名字快照: %+v", row2)
	}

	// 总览投影：已删任务行显示快照名 + deleted 标记（今天无明细，窗口内
	// 只由昨天的汇总行贡献这条任务）。
	overview, err := env.bots.GetBotUsageOverview(userID, "7d", "")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	var seen *api.BotUsageScheduleTask
	for i := range overview.ScheduleTasks {
		if overview.ScheduleTasks[i].TaskId == task.ID {
			seen = &overview.ScheduleTasks[i]
		}
	}
	if seen == nil {
		t.Fatalf("已删任务应在任务行里出现: %+v", overview.ScheduleTasks)
	}
	if !seen.Deleted || seen.Name != task.Name {
		t.Fatalf("已删任务应带快照名投影: %+v", *seen)
	}
}

// 任务名墓碑（评审回归）：名字墓碑独立于 bot_usage_daily，两种情形都靠它
// 取回——①任务在首滚前删除（没有任何旧快照）；②有效时区变化触发整表清空
// 重建（旧快照随表被擦）。
func TestBotUsageRollupTaskNameTombstone(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "tasktomb")
	setUserTimezone(t, env, userID, "UTC")
	bot := env.createBot(t, userID)

	// 注入时钟固定在 UTC 凌晨 02:00——run 落在 UTC 前天 22:00，东京口径
	// 下也是「昨天或更早」，两个时区都在可汇总范围里。
	fixed := time.Now().UTC().Truncate(24 * time.Hour).Add(2 * time.Hour)
	env.bots.nowFn = func() time.Time { return fixed }

	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedUsageRun(t, env, bot.ID, func(r *model.BotRun) {
		r.CreatedAt = botUsageDayStart(fixed, time.UTC).Add(-28 * time.Hour)
		r.SessionKind = model.BotSessionKindSchedule
		r.ScheduleTaskID = &task.ID
		r.CostEstimate = ptrCost(1.0)
	})
	seedUsageRequest(t, env, run.ID, ptrCost(1.0), "tt-1")

	// 首滚前删任务（走服务路径写下墓碑）：重算时活名与旧快照都没有。
	if err := env.bots.deleteScheduleTask(bot.ID, task.ID, ""); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	if err := env.bots.rollupBotUsage(bot.ID, fixed, true, false); err != nil {
		t.Fatalf("rollup: %v", err)
	}
	assertTaskName := func(want string) {
		t.Helper()
		var rows []model.BotUsageDaily
		if err := env.db.Where("bot_id = ? AND kind = ? AND schedule_task_id = ?",
			bot.ID, model.BotUsageDaySchedule, task.ID).Find(&rows).Error; err != nil {
			t.Fatalf("daily rows: %v", err)
		}
		if len(rows) == 0 {
			t.Fatal("应有 schedule 桶行")
		}
		for _, row := range rows {
			if row.TaskName != want {
				t.Fatalf("任务名应为 %q: %+v", want, row)
			}
		}
	}
	assertTaskName(task.Name)

	// 有效时区变化 → 整表清空重建：旧快照已没了，名字仍要来自墓碑。
	setUserTimezone(t, env, userID, "Asia/Tokyo")
	if err := env.bots.rollupBotUsage(bot.ID, fixed, true, false); err != nil {
		t.Fatalf("re-rollup after tz change: %v", err)
	}
	assertTaskName(task.Name)
}

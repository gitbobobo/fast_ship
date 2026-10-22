package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// 定时任务同一任务不重叠（INT-148）服务层测试：上一轮未结束时到点的一轮
// 记 skipped（不并行）、next_run_at 照常推进、不唤醒执行槽；上一轮结束后
// 正常触发；needs_report 的重叠 skipped 照常进主线事件。不靠睡眠竞态：
// 上一轮未结束直接落 finished_at IS NULL 的 run 行。

// dueWithinGrace 把任务 next_run_at 拨到 3s 前（在 90s 宽限内，属「到期」
// 而非「错过」），返回该时刻供断言比对。
func dueWithinGrace() time.Time {
	return time.Now().UTC().Add(-3 * time.Second)
}

// 上一轮未结束时 cron 到点：本轮记 skipped 终态（摘要写明原因、不设
// StartedAt 与触发上下文）、不唤醒执行槽（假模型零请求）、next_run_at 照常
// 推进；needs_report=false 不追加主线事件。
func TestBotInt148OverlapTickSkippedWhilePreviousRunning(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "sched148busy")
	bot := env.createBot(t, userID)
	due := dueWithinGrace()
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false
		tk.NextRunAt = &due
	})
	prev := seedScheduleRun(t, env, bot.ID, task.ID)

	tickNow := time.Now().UTC()
	env.bots.scheduleTick(tickNow)

	// 本轮记 skipped 终态；除上一轮外只有这一条新 run。
	cur := mustFindRun(t, env, prev.ID)
	var fresh model.BotRun
	if err := env.db.Where("schedule_task_id = ? AND id <> ?", task.ID, prev.ID).
		First(&fresh).Error; err != nil {
		t.Fatalf("本轮 run 应存在: %v", err)
	}
	if fresh.Status != model.BotRunStatusSkipped || fresh.FinishedAt == nil {
		t.Fatalf("重叠到点应落 skipped 终态: %+v", fresh)
	}
	if fresh.StartedAt != nil || fresh.TriggerContextJSON != "" {
		t.Fatalf("重叠 skipped 不应设 StartedAt 与触发上下文（与错过同型）: %+v", fresh)
	}
	if !strings.Contains(fresh.Summary, "尚未结束") {
		t.Fatalf("摘要应写明上一轮未结束的原因: %+v", fresh)
	}
	if !strings.Contains(fresh.Summary, "不并行") {
		t.Fatalf("摘要应写明不并行的语义: %+v", fresh)
	}
	if !strings.Contains(fresh.Summary, prev.ID) {
		t.Fatalf("摘要应带阻塞 run 的 ID（%s），排查卡死时可直接定位: %+v", prev.ID, fresh)
	}
	// 上一轮 run 不受影响。
	if cur.FinishedAt != nil || cur.Status != model.BotRunStatusRunning {
		t.Fatalf("上一轮 run 不应被本轮派发影响: %+v", cur)
	}
	// 任务只有一个未结束 run，就是上一轮——没有新的运行被唤醒。
	var unfinished []model.BotRun
	if err := env.db.Where("schedule_task_id = ? AND finished_at IS NULL", task.ID).
		Find(&unfinished).Error; err != nil {
		t.Fatalf("count unfinished: %v", err)
	}
	if len(unfinished) != 1 || unfinished[0].ID != prev.ID {
		t.Fatalf("未结束的应只有上一轮一条，实得 %+v", unfinished)
	}
	// 没有任何执行槽被唤醒：假模型零请求。
	if n := len(env.fake.requests); n != 0 {
		t.Fatalf("重叠到点不应发起模型请求，实得 %d", n)
	}
	// next_run_at 已推进到 tick 之后，不会每 tick 空转。
	var reloaded model.BotScheduleTask
	if err := env.db.Where("id = ?", task.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.NextRunAt == nil || !reloaded.NextRunAt.After(tickNow) {
		t.Fatalf("next_run_at 应推进到本轮之后，实得 %v", reloaded.NextRunAt)
	}
	// needs_report=false：跳过不追加主线事件。
	if n, _ := countScheduleEvents(t, env, bot.ID); n != 0 {
		t.Fatalf("needs_report=false 的重叠 skipped 不应追加主线事件，实得 %d", n)
	}
}

// 上一轮已结束后到点：正常触发 running 并执行到 completed，next_run_at
// 照常推进。
func TestBotInt148TriggersAfterPreviousFinished(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "本轮执行完成：已汇总昨日动态"})
	userID := createBotTestUser(t, env.db, "sched148free")
	bot := env.createBot(t, userID)
	due := dueWithinGrace()
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false
		tk.NextRunAt = &due
	})
	// 上一轮已结束：seed running 行后补 finished_at。
	prev := seedScheduleRun(t, env, bot.ID, task.ID)
	if err := env.db.Model(&model.BotRun{}).Where("id = ?", prev.ID).
		Update("finished_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("finish previous: %v", err)
	}

	tickNow := time.Now().UTC()
	env.bots.scheduleTick(tickNow)

	var fresh model.BotRun
	if err := env.db.Where("schedule_task_id = ? AND id <> ?", task.ID, prev.ID).
		First(&fresh).Error; err != nil {
		t.Fatalf("本轮 run 应存在: %v", err)
	}
	fresh = *waitForScheduleRunFinished(t, env, fresh.ID)
	if fresh.Status != model.BotRunStatusCompleted {
		t.Fatalf("上一轮结束后到点应正常执行到 completed，实得 %s", fresh.Status)
	}
	if fresh.StartedAt == nil {
		t.Fatalf("正常触发应设 StartedAt: %+v", fresh)
	}
	var reloaded model.BotScheduleTask
	if err := env.db.Where("id = ?", task.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.NextRunAt == nil || !reloaded.NextRunAt.After(tickNow) {
		t.Fatalf("next_run_at 应推进到本轮之后，实得 %v", reloaded.NextRunAt)
	}
}

// needs_report=true 时重叠 skipped 照常进主线，但同一「未结束的上一轮」期间
// 只留一条事件：连续两个周期到点（周期短于运行时长）各落一条 skipped run、
// next_run_at 两次都推进，主线事件仍只有 1 条（去重键钉在未结束的那轮 run
// 上）；上一轮结束后再来一轮重叠，新的未结束 run 产生第二条事件。
func TestBotInt148OverlapSkipEventDedupedPerUnfinishedRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "sched148report")
	bot := env.createBot(t, userID)
	due := dueWithinGrace()
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = true
		tk.NextRunAt = &due
	})
	prev := seedScheduleRun(t, env, bot.ID, task.ID)

	// 第一个周期到点：上一轮未结束，落 skipped 并追加 1 条主线事件。
	firstTick := time.Now().UTC()
	env.bots.scheduleTick(firstTick)
	var firstSkip model.BotRun
	if err := env.db.Where("schedule_task_id = ? AND id <> ?", task.ID, prev.ID).
		Order("created_at ASC").First(&firstSkip).Error; err != nil {
		t.Fatalf("第一个周期的 skipped run 应存在: %v", err)
	}
	if firstSkip.Status != model.BotRunStatusSkipped || firstSkip.FinishedAt == nil {
		t.Fatalf("第一个周期应落 skipped 终态: %+v", firstSkip)
	}
	n, content := countScheduleEvents(t, env, bot.ID)
	if n != 1 {
		t.Fatalf("重叠 skipped 应追加一条主线事件，实得 %d", n)
	}
	if content.TaskID != task.ID || content.Status != string(model.BotRunStatusSkipped) {
		t.Fatalf("事件应指向本任务的 skipped 结果: %+v", content)
	}
	if !strings.Contains(content.Summary, "尚未结束") {
		t.Fatalf("事件摘要应写明上一轮未结束的原因: %+v", content)
	}
	if !strings.Contains(content.Summary, prev.ID) {
		t.Fatalf("事件摘要应带阻塞 run 的 ID（%s）: %+v", prev.ID, content)
	}

	// 第二个周期到点（把 next_run_at 拨回宽限窗口，上一轮仍未结束）：再落
	// 一条 skipped，但主线事件不追加。
	secondDue := firstTick.Add(2 * time.Second)
	if err := env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).
		Update("next_run_at", secondDue).Error; err != nil {
		t.Fatalf("rewind next_run_at: %v", err)
	}
	secondTick := secondDue.Add(3 * time.Second)
	env.bots.scheduleTick(secondTick)

	var skipCount int64
	if err := env.db.Model(&model.BotRun{}).
		Where("schedule_task_id = ? AND id <> ? AND status = ?", task.ID, prev.ID, model.BotRunStatusSkipped).
		Count(&skipCount).Error; err != nil {
		t.Fatalf("count skipped runs: %v", err)
	}
	if skipCount != 2 {
		t.Fatalf("两个周期应各落一条 skipped run，实得 %d", skipCount)
	}
	if n, _ := countScheduleEvents(t, env, bot.ID); n != 1 {
		t.Fatalf("同一未结束期间只应有一条主线事件，实得 %d", n)
	}
	var reloaded model.BotScheduleTask
	if err := env.db.Where("id = ?", task.ID).First(&reloaded).Error; err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.NextRunAt == nil || !reloaded.NextRunAt.After(secondTick) {
		t.Fatalf("第二个周期 next_run_at 也应推进，实得 %v", reloaded.NextRunAt)
	}

	// 上一轮结束后种一条新的未结束 run，再来一轮重叠：去重键换新（按 run
	// 区分），产生第二条事件。
	if err := env.db.Model(&model.BotRun{}).Where("id = ?", prev.ID).
		Update("finished_at", secondTick).Error; err != nil {
		t.Fatalf("finish previous: %v", err)
	}
	seedScheduleRun(t, env, bot.ID, task.ID)
	thirdDue := secondTick.Add(2 * time.Second)
	if err := env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).
		Update("next_run_at", thirdDue).Error; err != nil {
		t.Fatalf("rewind next_run_at (third): %v", err)
	}
	env.bots.scheduleTick(thirdDue.Add(3 * time.Second))

	n, content = countScheduleEvents(t, env, bot.ID)
	if n != 2 {
		t.Fatalf("新的未结束 run 应产生第二条主线事件，实得 %d", n)
	}
	var latestContent botScheduleEventContent
	if err := json.Unmarshal([]byte(latestScheduleEvent(t, env, bot.ID).ContentJSON), &latestContent); err != nil {
		t.Fatalf("decode latest schedule_event: %v", err)
	}
	if latestContent.RunID == content.RunID {
		t.Fatalf("第二条事件应指向新一轮的 skipped run（去重键按 run 区分）: %+v", latestContent)
	}
	if mustFindRun(t, env, prev.ID).FinishedAt == nil {
		t.Fatalf("上一轮 run 已补终态，不应被影响")
	}
}

// latestScheduleEvent 取主线会话最新一条 schedule_event 消息。
func latestScheduleEvent(t *testing.T, env *botTestEnv, botID string) *model.BotMessage {
	t.Helper()
	var row model.BotMessage
	if err := env.db.Where("bot_id = ? AND session_kind = ? AND kind = ?",
		botID, model.BotSessionKindMain, model.BotMessageKindScheduleEvent).
		Order("seq DESC").First(&row).Error; err != nil {
		t.Fatalf("latest schedule event: %v", err)
	}
	return &row
}

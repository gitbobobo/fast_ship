package service

import (
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
)

// INT-138 测试（夜间整理与事件唤醒部分）：夜间整理的触发时刻、日期与
// 配置快照按归属用户有效时区计算（用户未声明时区时回退部署配置），daily
// 文件名同样取归属时区；定时任务失败/跳过事件提交进主线后立即唤醒主线，
// 不等 5s scanLoop 兜底。

// int138SetUserTimezone 直接改用户时区列（模拟设置页/首登写入，绕过 handler）。
func int138SetUserTimezone(t *testing.T, env *botTestEnv, userID, tz string) {
	t.Helper()
	if err := env.db.Model(&model.User{}).Where("id = ?", userID).
		Update("timezone", tz).Error; err != nil {
		t.Fatalf("set timezone: %v", err)
	}
}

// int138NightlyRuns 返回某 bot 的夜间整理运行条数。
func int138NightlyRuns(t *testing.T, env *botTestEnv, botID string) int64 {
	t.Helper()
	var n int64
	if err := env.db.Model(&model.BotMaintenanceRun{}).
		Where("bot_id = ? AND kind = ?", botID, model.BotMaintenanceNightly).
		Count(&n).Error; err != nil {
		t.Fatalf("count nightly: %v", err)
	}
	return n
}

// int138AssertShanghaiNightly 是「有效时区 Asia/Shanghai」下夜间整理的公共
// 断言：UTC 18:59（当地次日 02:59）不触发，UTC 19:00（当地 03:00）触发；
// 触发键取当地日期 nightly:2026-10-17，配置快照记该时区。
func int138AssertShanghaiNightly(t *testing.T, env *botTestEnv, botID string) {
	t.Helper()
	// UTC 18:59 = 上海次日 02:59：未到当地触发时刻。
	env.bots.nowFn = func() time.Time {
		return time.Date(2026, 10, 16, 18, 59, 0, 0, time.UTC)
	}
	env.bots.scanNightlyOnce()
	// 入队发生在 scanNightlyOnce 内（缓冲 channel），若误触发 worker 很快
	// 就会建档；给一小段窗口再数，把「真没触发」和「还没来得及跑」区分开。
	time.Sleep(300 * time.Millisecond)
	if n := int138NightlyRuns(t, env, botID); n != 0 {
		t.Fatalf("当地 02:59 不应触发夜间整理，实得 %d 条", n)
	}

	// UTC 19:00 = 上海次日 03:00：触发，触发键取当地日期。
	env.bots.nowFn = func() time.Time {
		return time.Date(2026, 10, 16, 19, 0, 0, 0, time.UTC)
	}
	env.bots.scanNightlyOnce()
	var run model.BotMaintenanceRun
	waitForBot(t, "夜间运行建档", 10*time.Second, func() bool {
		return env.db.Where("bot_id = ? AND kind = ? AND trigger_key = ?",
			botID, model.BotMaintenanceNightly, "nightly:2026-10-17").
			First(&run).Error == nil
	})
	waitForBot(t, "夜间运行收尾", 10*time.Second, func() bool {
		if err := env.db.Where("id = ?", run.ID).First(&run).Error; err != nil {
			return false
		}
		return run.Status == model.BotMaintenanceSucceeded || run.Status == model.BotMaintenanceFailed
	})
	if run.Status != model.BotMaintenanceSucceeded {
		t.Fatalf("夜间运行应成功: %s %v", run.Status, run.ErrorCode)
	}
	if !strings.Contains(run.ConfigSnapshotJSON, `"timezone":"Asia/Shanghai"`) {
		t.Fatalf("配置快照应记归属用户时区: %s", run.ConfigSnapshotJSON)
	}
}

// TestInt138NightlyUsesOwnerTimezone 归属用户声明 Asia/Shanghai 时夜间整理
// 按该时区算触发时刻与日期。
func TestInt138NightlyUsesOwnerTimezone(t *testing.T) {
	env := setupBotTestEnv(t)
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "int138night")
	bot := env.createBot(t, userID)
	int138SetUserTimezone(t, env, userID, "Asia/Shanghai")
	int138AssertShanghaiNightly(t, env, bot.ID)
}

// TestInt138NightlyDeploymentFallback 用户未声明时区时回退部署配置
// （bots.memory.timezone），行为与声明 Asia/Shanghai 一致。
func TestInt138NightlyDeploymentFallback(t *testing.T) {
	env := setupBotTestEnv(t)
	env.fake.strict = true
	env.cfg.Bots.Memory.Timezone = "Asia/Shanghai"
	userID := createBotTestUser(t, env.db, "int138nightfb")
	bot := env.createBot(t, userID)
	int138AssertShanghaiNightly(t, env, bot.ID)
}

// TestInt138DailyPathUsesOwnerTimezone 同一 UTC 时刻在不同归属时区下得到
// 不同的 daily 文件名（UTC 20:00 = 上海次日 04:00）。
func TestInt138DailyPathUsesOwnerTimezone(t *testing.T) {
	env := setupBotTestEnv(t)
	shUser := createBotTestUser(t, env.db, "int138daily-sh")
	utcUser := createBotTestUser(t, env.db, "int138daily-utc")
	botSH := env.createBot(t, shUser)
	botUTC := env.createBot(t, utcUser)
	int138SetUserTimezone(t, env, shUser, "Asia/Shanghai")
	// utcUser 不声明时区且部署配置为空：有效时区 UTC。

	fixed := time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC)
	rowSH, err := env.bots.bots.Get(botSH.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	rowUTC, err := env.bots.bots.Get(botUTC.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	gotSH := env.bots.botDailyPathForBot(rowSH, fixed)
	gotUTC := env.bots.botDailyPathForBot(rowUTC, fixed)
	if gotSH != "daily/2026-10-17.md" {
		t.Fatalf("Asia/Shanghai 下该时刻应落次日 daily，实得 %s", gotSH)
	}
	if gotUTC != "daily/2026-10-16.md" {
		t.Fatalf("UTC 下该时刻应落当天 daily，实得 %s", gotUTC)
	}
	if gotSH == gotUTC {
		t.Fatalf("同一 UTC 时刻在不同归属时区应得不同 daily 文件名")
	}
}

// int138WaitEventWakeRun 等待 schedule_event 唤醒在主线落一条 run 并返回；
// 触发键必须是 runID+status 的幂等键。
func int138WaitEventWakeRun(t *testing.T, env *botTestEnv, botID, wantKey string) *model.BotRun {
	t.Helper()
	var run model.BotRun
	waitForBot(t, "schedule_event 唤醒主线", 10*time.Second, func() bool {
		return env.db.Where("bot_id = ? AND session_kind = ? AND trigger_source = ?",
			botID, model.BotSessionKindMain, model.BotTriggerScheduleEvent).
			First(&run).Error == nil
	})
	if run.TriggerKey != wantKey {
		t.Fatalf("唤醒键应为 %q，实得 %q", wantKey, run.TriggerKey)
	}
	return &run
}

// int138ScheduleEventCount 返回主线会话里的 schedule_event 条数。
func int138ScheduleEventCount(t *testing.T, env *botTestEnv, botID string) int64 {
	t.Helper()
	var n int64
	if err := env.db.Model(&model.BotMessage{}).
		Where("bot_id = ? AND session_kind = ? AND kind = ?",
			botID, model.BotSessionKindMain, model.BotMessageKindScheduleEvent).
		Count(&n).Error; err != nil {
		t.Fatalf("count schedule_event: %v", err)
	}
	return n
}

// int138WaitEventProjection 消费假模型请求直到某个请求体含 marker（事件
// 投影），返回该请求体原文。
func int138WaitEventProjection(t *testing.T, env *botTestEnv, marker string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("等待含 %q 的模型请求超时", marker)
		}
		select {
		case req := <-env.fake.requests:
			if strings.Contains(string(req.Body), marker) {
				return string(req.Body)
			}
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// int138WaitRunFinished 等主线 run 落定终态。
func int138WaitRunFinished(t *testing.T, env *botTestEnv, runID string) {
	t.Helper()
	waitForBot(t, "主线 run 收尾", 10*time.Second, func() bool {
		fresh, err := env.bots.runs.FindByID(runID)
		return err == nil && fresh.FinishedAt != nil
	})
}

// TestInt138ScheduleFailureEventWakesMainline 定时任务失败事件提交后立即
// 唤醒主线（不启动 scanLoop）：主线 run 带 trigger_source=schedule_event，
// 假模型收到的请求含事件投影。
func TestInt138ScheduleFailureEventWakesMainline(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Status: 500},     // schedule run 的模型请求失败
		fakeModelTurn{Text: "已收到失败汇报"}, // 主线 run 的正常回复
	)
	env.fake.strict = true
	// 500 属可重试分类；本用例只关心失败事件本身，关掉重试让失败一次落定。
	env.cfg.Bots.Runtime.RetryMaxAttempts = -1
	userID := createBotTestUser(t, env.db, "int138schedfail")
	bot := env.createBot(t, userID)
	due := time.Now().UTC().Add(-3 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = true
		tk.NextRunAt = &due
	})

	env.bots.scheduleTick(time.Now().UTC())
	var schedRun model.BotRun
	if err := env.db.Where("schedule_task_id = ?", task.ID).First(&schedRun).Error; err != nil {
		t.Fatalf("schedule run 应已建档: %v", err)
	}
	finished := waitForScheduleRunFinished(t, env, schedRun.ID)
	if finished.Status != model.BotRunStatusFailed {
		t.Fatalf("schedule run 应失败: %+v", finished)
	}
	if n := int138ScheduleEventCount(t, env, bot.ID); n != 1 {
		t.Fatalf("失败事件应落一条 schedule_event，实得 %d", n)
	}

	// 事件提交后立刻唤醒主线：env.bots.Start 从未调用，5s scanLoop 兜底不在场，
	// 这条 run 只能来自 wakeScheduleEvent 的立即唤醒。
	wake := int138WaitEventWakeRun(t, env, bot.ID, "schedule_event:"+schedRun.ID+":failed")
	body := int138WaitEventProjection(t, env, "定时任务运行事件")
	if !strings.Contains(body, `\"status\":\"failed\"`) {
		t.Fatalf("事件投影应带 failed 状态: %s", body)
	}
	int138WaitRunFinished(t, env, wake.ID)
}

// TestInt138ScheduleSkippedEventWakesMainline needs_report 任务错过宽限：
// skipped 事件提交后立即唤醒主线（不启动 scanLoop），假模型收到 skipped
// 投影。
func TestInt138ScheduleSkippedEventWakesMainline(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "已收到跳过汇报"})
	env.fake.strict = true
	userID := createBotTestUser(t, env.db, "int138schedskip")
	bot := env.createBot(t, userID)
	// 计划时间早于错过宽限（90s）：到点只记 skipped，不执行、不发模型请求。
	due := time.Now().UTC().Add(-3 * time.Minute)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = true
		tk.NextRunAt = &due
	})

	env.bots.scheduleTick(time.Now().UTC())
	var schedRun model.BotRun
	if err := env.db.Where("schedule_task_id = ?", task.ID).First(&schedRun).Error; err != nil {
		t.Fatalf("skipped run 应已建档: %v", err)
	}
	if schedRun.Status != model.BotRunStatusSkipped {
		t.Fatalf("错过宽限应记 skipped: %+v", schedRun)
	}
	if n := int138ScheduleEventCount(t, env, bot.ID); n != 1 {
		t.Fatalf("needs_report 任务的 skipped 应落一条事件，实得 %d", n)
	}

	wake := int138WaitEventWakeRun(t, env, bot.ID, "schedule_event:"+schedRun.ID+":skipped")
	body := int138WaitEventProjection(t, env, "定时任务运行事件")
	if !strings.Contains(body, `\"status\":\"skipped\"`) {
		t.Fatalf("事件投影应带 skipped 状态: %s", body)
	}
	int138WaitRunFinished(t, env, wake.ID)
}

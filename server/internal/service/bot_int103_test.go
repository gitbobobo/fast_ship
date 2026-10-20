package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

// 定时任务（INT-103）服务层测试：cron/时区、错过与重启幂等、独立会话、
// 与主线并发、默认放行、事件进主线、静默与运行记录。假模型服务脚本化驱动。

// seedScheduleTask 直接落一个定时任务；next_run_at 缺省取 now 之后的下一次。
func seedScheduleTask(t *testing.T, env *botTestEnv, botID string, mutate func(*model.BotScheduleTask)) *model.BotScheduleTask {
	t.Helper()
	task := &model.BotScheduleTask{
		ID: uuid.NewString(), BotID: botID,
		Name: "每日汇总", CronExpression: "0 9 * * *", Timezone: "Asia/Shanghai",
		Instruction: "汇总昨日 issue 动态并写进 daily 笔记",
		Enabled:     true, NeedsReport: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if mutate != nil {
		mutate(task)
	}
	if task.NextRunAt == nil {
		_, next, err := parseScheduleCron(task.CronExpression, task.Timezone, time.Now().UTC())
		if err != nil {
			t.Fatalf("parse cron: %v", err)
		}
		task.NextRunAt = &next
	}
	if err := env.bots.schedTasks.Create(task); err != nil {
		t.Fatalf("seed schedule task: %v", err)
	}
	return task
}

// seedScheduleRun 直接落一条 schedule 会话的 running run（不经调度器）。
func seedScheduleRun(t *testing.T, env *botTestEnv, botID, taskID string) *model.BotRun {
	t.Helper()
	now := time.Now().UTC()
	taskIDCopy := taskID
	planned := now.Add(-time.Second)
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource:      model.BotTriggerSchedule,
		TriggerKey:         botScheduleTriggerKey(taskID, planned),
		Status:             model.BotRunStatusRunning,
		SessionKind:        model.BotSessionKindSchedule,
		ScheduleTaskID:     &taskIDCopy,
		ScheduledFor:       &planned,
		ConfigSnapshotJSON: "{}",
		CreatedAt:          now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed schedule run: %v", err)
	}
	return run
}

// waitForScheduleRunFinished 等待 schedule run 落定终态并返回最新行。
func waitForScheduleRunFinished(t *testing.T, env *botTestEnv, runID string) *model.BotRun {
	t.Helper()
	var run *model.BotRun
	waitForBot(t, "schedule run 收尾", 15*time.Second, func() bool {
		r, err := env.bots.runs.FindByID(runID)
		if err != nil {
			return false
		}
		if r.FinishedAt != nil {
			run = r
			return true
		}
		return false
	})
	return run
}

// ---------- 1. cron 解析与时区 ----------

func TestBotScheduleCronAndTimezone(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedcron")
	bot := env.createBot(t, userID)

	// 上海 09:00 = UTC 01:00
	task, err := env.bots.createScheduleTask(bot.ID, "早报", "0 9 * * *", "Asia/Shanghai", "生成早报", "", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sh, _ := time.LoadLocation("Asia/Shanghai")
	if got := task.NextRunAt.In(sh).Format("15:04"); got != "09:00" {
		t.Fatalf("上海下次运行应为 09:00，实得 %s", got)
	}
	if task.NextRunAt.UTC().Hour() != 1 || task.NextRunAt.Location() != time.UTC {
		t.Fatalf("next_run_at 应以 UTC 存储且为 01:00，实得 %v", task.NextRunAt)
	}
	if !task.Enabled || task.NeedsReport {
		t.Fatalf("新任务应默认启用且不需汇报: %+v", task)
	}

	// 空 timezone = UTC
	utcTask, err := env.bots.createScheduleTask(bot.ID, "UTC 任务", "30 8 * * *", "", "检查", "", true)
	if err != nil {
		t.Fatalf("create utc: %v", err)
	}
	if utcTask.NextRunAt.UTC().Hour() != 8 || utcTask.Timezone != "" {
		t.Fatalf("空时区应按 UTC 计算: %+v", utcTask)
	}

	// 非法表达式与时区一律拒绝（与调度器同一解析路径）
	for _, bad := range []struct{ cron, tz string }{
		{"*/10 * * * * *", ""},           // 六字段（含秒）
		{"@every 10s", ""},               // 描述符
		{"CRON_TZ=UTC */10 * * * *", ""}, // 内嵌时区前缀
		{"TZ=UTC */10 * * * *", ""},      // 内嵌时区前缀
		{"not a cron", ""},               // 语法错误
		{"*/10 * * * *", "Not/AZone"},    // 非法时区
	} {
		if _, err := env.bots.createScheduleTask(bot.ID, "坏任务", bad.cron, bad.tz, "x", "", false); err == nil {
			t.Fatalf("应拒绝 cron=%q tz=%q", bad.cron, bad.tz)
		}
	}
}

// ---------- 2. 错过宽限与重启不重复 ----------

func TestBotScheduleMissGraceSkipsAndNoDuplicate(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "schedmiss")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	// 计划时间已过期超过宽限 → 只记 skipped，不执行。
	past := time.Now().UTC().Add(-10 * time.Minute)
	env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).Update("next_run_at", past)
	env.bots.scheduleTick(time.Now().UTC())

	var runs []model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).Order("created_at ASC").Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("错过应记一条 run，实得 %d", len(runs))
	}
	if runs[0].Status != model.BotRunStatusSkipped {
		t.Fatalf("应记 skipped，实得 %s", runs[0].Status)
	}
	if runs[0].ScheduledFor == nil || !runs[0].ScheduledFor.Equal(past) {
		t.Fatalf("scheduled_for 应为原计划时间: %+v", runs[0].ScheduledFor)
	}
	if !strings.Contains(runs[0].Summary, "跳过") {
		t.Fatalf("summary 应写明跳过原因: %q", runs[0].Summary)
	}
	if runs[0].StartedAt != nil || runs[0].FinishedAt == nil {
		t.Fatalf("skipped 不应执行，但要有终态时间: %+v", runs[0])
	}
	// next_run_at 已推进到未来。
	fresh, _ := env.bots.schedTasks.FindByID(task.ID)
	if fresh.NextRunAt == nil || !fresh.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("tick 后 next_run_at 应严格晚于 now: %+v", fresh.NextRunAt)
	}

	// 模拟重启后同一计划时间再次到期：同一 trigger_key 不会重复建 run。
	env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).Update("next_run_at", past)
	env.bots.scheduleTick(time.Now().UTC())
	var count int64
	env.db.Model(&model.BotRun{}).Where("schedule_task_id = ?", task.ID).Count(&count)
	if count != 1 {
		t.Fatalf("同一计划时间重复到期不应重复建 run，实得 %d", count)
	}
}

// 到期在宽限内 → 建 running run 并执行到 completed（静默）。
func TestBotScheduleDueWithinGraceRuns(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "scheddue")
	bot := env.createBot(t, userID)
	due := time.Now().UTC().Add(-5 * time.Second) // 宽限内
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NextRunAt = &due
		tk.NeedsReport = true
	})

	env.bots.scheduleTick(time.Now().UTC())
	var runs []model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).Find(&runs)
	if len(runs) != 1 || runs[0].Status != model.BotRunStatusRunning {
		t.Fatalf("宽限内应建一条 running run，实得 %+v", runs)
	}
	run := waitForScheduleRunFinished(t, env, runs[0].ID)
	if run.Status != model.BotRunStatusCompleted {
		t.Fatalf("应完成，实得 %s（%v）", run.Status, run.ErrorMessage)
	}
	if !run.Silent || run.Summary != "" {
		t.Fatalf("[[SILENT]] 应记 silent 且空 summary: silent=%v summary=%q", run.Silent, run.Summary)
	}
}

// ---------- 2.5 派发时冻结有效配置 ----------

// TestBotScheduleDispatchFreezesConfigSnapshot 派发时落定的 run 快照必须记录
// 有效配置：任务未指定模型记机器人当时的 model/base_url，任务指定了用任务的；
// 派发后、执行前编辑机器人不影响已派发的运行。
func TestBotScheduleDispatchFreezesConfigSnapshot(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "快照运行一"}, fakeModelTurn{Text: "快照运行二"})
	userID := createBotTestUser(t, env.db, "schedfreeze")
	bot := env.createBot(t, userID)

	// 场景一：任务不指定模型 —— 快照记机器人当时的配置。
	due := time.Now().UTC().Add(-3 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false
		tk.NextRunAt = &due
	})
	env.bots.scheduleTick(time.Now().UTC())
	var run model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).First(&run)

	var snap botConfigSnapshot
	if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snap); err != nil {
		t.Fatalf("decode snapshot: %v", err)
	}
	if snap.Model != "fake-model" || snap.BaseURL != env.fake.URL() {
		t.Fatalf("快照应记录派发时的有效配置: %+v", snap)
	}
	// 密钥密文与 base_url 成对冻结（派发时写入，不进任何 API 投影）。
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	if len(run.APIKeyEncryptedSnapshot) == 0 ||
		!bytes.Equal(run.APIKeyEncryptedSnapshot, botRow.APIKeyEncrypted) {
		t.Fatalf("快照应冻结派发时的密钥密文")
	}

	// 派发后、执行前编辑机器人（换 provider + 换模型）：这次运行仍按快照执行
	// （执行槽载入时即使读到编辑后的机器人，快照值优先）。地址在 provider 上
	// （INT-133），这里新建一条指向不可达地址的 provider 并把 bot 的引用切过去。
	editedProviderID := env.createProviderAt(t, userID, "http://edited.invalid", "sk-edited-0000")
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).Updates(map[string]interface{}{
		"model": "edited-model", "provider_id": editedProviderID,
	}).Error; err != nil {
		t.Fatalf("edit bot: %v", err)
	}
	edited, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load edited bot: %v", err)
	}
	if runModelOf(&run, edited) != "fake-model" || runBaseURLOf(&run, edited) != env.fake.URL() {
		t.Fatalf("编辑后 runModelOf/runBaseURLOf 应仍取快照值: model=%s url=%s",
			runModelOf(&run, edited), runBaseURLOf(&run, edited))
	}
	req := env.fake.nextRequest(t, 10*time.Second)
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if payload.Model != "fake-model" {
		t.Fatalf("已派发的运行应使用快照模型，实得 %s", payload.Model)
	}
	if !strings.Contains(req.Authorization, "sk-test-abcdef123456") {
		t.Fatalf("已派发的运行应使用派发时的密钥，实得 %s", req.Authorization)
	}
	done := waitForScheduleRunFinished(t, env, run.ID)
	if done.Status != model.BotRunStatusCompleted {
		t.Fatalf("编辑配置不应影响已派发的运行: %s %v", done.Status, done.ErrorMessage)
	}

	// 场景二：任务指定模型 —— 快照用任务的模型。
	bot2 := env.createBot(t, userID)
	due2 := time.Now().UTC().Add(-3 * time.Second)
	task2 := seedScheduleTask(t, env, bot2.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false
		tk.Model = "task-model"
		tk.NextRunAt = &due2
	})
	env.bots.scheduleTick(time.Now().UTC())
	var run2 model.BotRun
	env.db.Where("schedule_task_id = ?", task2.ID).First(&run2)
	var snap2 botConfigSnapshot
	if err := json.Unmarshal([]byte(run2.ConfigSnapshotJSON), &snap2); err != nil {
		t.Fatalf("decode snapshot2: %v", err)
	}
	if snap2.Model != "task-model" || snap2.BaseURL != env.fake.URL() {
		t.Fatalf("任务指定模型时快照应用任务的模型: %+v", snap2)
	}
	req2 := env.fake.nextRequest(t, 10*time.Second)
	var payload2 struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(req2.Body, &payload2); err != nil {
		t.Fatalf("decode request2: %v", err)
	}
	if payload2.Model != "task-model" {
		t.Fatalf("指定模型的任务应按任务模型执行，实得 %s", payload2.Model)
	}
	done2 := waitForScheduleRunFinished(t, env, run2.ID)
	if done2.Status != model.BotRunStatusCompleted {
		t.Fatalf("指定模型的运行应完成: %s %v", done2.Status, done2.ErrorMessage)
	}
}

// ---------- 3. 独立会话 ----------

func TestBotScheduleIndependentSession(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: "主线回复A"},
		fakeModelTurn{Text: "定时汇报：已核对昨日动态"},
	)
	userID := createBotTestUser(t, env.db, "schediso")
	bot := env.createBot(t, userID)

	// 写一条长期记忆（schedule 会话应带上它）。
	if _, _, err := repository.NewBotMemoryRepository(env.db).WriteVersionTx(
		env.db, bot.ID, "MEMORY.md", model.BotMemoryOpCreate, "记忆暗号-9x8y", false,
		nil, model.BotMemoryWriterMain, "", "", uuid.NewString(), time.Now().UTC(),
	); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	// 先跑一轮主线，制造主线历史。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "主线消息-ABC", ClientMessageID: "m1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "主线收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })
	mainReq := env.fake.nextRequest(t, 5*time.Second)
	if !strings.Contains(string(mainReq.Body), "主线消息-ABC") {
		t.Fatalf("主线请求应含主线消息")
	}

	// 触发 schedule run。
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Instruction = "定时指令-XYZ 汇总昨日动态"
		due := time.Now().UTC().Add(-3 * time.Second)
		tk.NextRunAt = &due
	})
	env.bots.scheduleTick(time.Now().UTC())
	var run model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).First(&run)
	run = *waitForScheduleRunFinished(t, env, run.ID)
	if run.Status != model.BotRunStatusCompleted {
		t.Fatalf("schedule run 应完成: %s %v", run.Status, run.ErrorMessage)
	}

	schedReq := env.fake.nextRequest(t, 10*time.Second)
	body := string(schedReq.Body)
	if !strings.Contains(body, "定时指令-XYZ") {
		t.Fatalf("schedule 请求应含任务指令")
	}
	if !strings.Contains(body, "记忆暗号-9x8y") {
		t.Fatalf("schedule 请求应含冻结记忆注入")
	}
	if !strings.Contains(body, "你是 Fast Ship 内置机器人") {
		t.Fatalf("schedule 请求应含系统提示")
	}
	if !strings.Contains(body, "[[SILENT]]") || !strings.Contains(body, "schedule run") {
		t.Fatalf("schedule 请求应含静默约定与运行时上下文")
	}
	if strings.Contains(body, "主线消息-ABC") {
		t.Fatalf("schedule 会话不得包含主线消息")
	}

	// 主线读取面看不到 schedule 会话消息。
	mainMsgs, err := env.bots.messages.ListMainline(bot.ID, 0, 0, 200)
	if err != nil {
		t.Fatalf("list mainline: %v", err)
	}
	for i := range mainMsgs {
		if mainMsgs[i].RunID != nil && *mainMsgs[i].RunID == run.ID {
			t.Fatalf("主线读取面不应返回 schedule 会话消息")
		}
		if mainMsgs[i].SessionKind != model.BotSessionKindMain {
			t.Fatalf("主线读取面只应返回 main 消息: %+v", mainMsgs[i])
		}
	}
}

// ---------- 4. 与主线并发 ----------

func TestBotScheduleConcurrentWithMain(t *testing.T) {
	hold := make(chan struct{})
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: "定时进行中", Hold: hold},
		fakeModelTurn{Text: "主线即时回复"},
	)
	userID := createBotTestUser(t, env.db, "schedconc")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	// schedule run 挂在模型请求上。
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	env.bots.wakeScheduleRun(bot.ID, run.ID)
	env.fake.nextRequest(t, 10*time.Second) // schedule 的请求（挂起中）

	// 主线同时发送并完成，互不让位。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "主线并发消息", ClientMessageID: "c1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "主线完成", 15*time.Second, func() bool {
		var n int64
		env.db.Model(&model.BotRun{}).Where(
			"bot_id = ? AND session_kind = ? AND status = ?",
			bot.ID, model.BotSessionKindMain, model.BotRunStatusCompleted).Count(&n)
		return n >= 1
	})
	fresh, _ := env.bots.runs.FindByID(run.ID)
	if fresh.FinishedAt != nil || fresh.Status != model.BotRunStatusRunning {
		t.Fatalf("主线完成时 schedule run 不应被打断: %+v", fresh)
	}

	close(hold)
	done := waitForScheduleRunFinished(t, env, run.ID)
	if done.Status != model.BotRunStatusCompleted {
		t.Fatalf("放行后 schedule run 应完成: %s", done.Status)
	}
}

// ---------- 4.5 新输入判定按 run 收窄 ----------

// appendScheduleBackgroundResult 给指定 schedule run 名下追加一条平台后台
// 结果消息（等价该 run 的异步写完成投影），seq 取账本末位之后。不落 call 行，
// 消息也就不带 call_id 外键。
func appendScheduleBackgroundResult(t *testing.T, env *botTestEnv, botID string, runID string, callID string) {
	t.Helper()
	seq, err := env.bots.bots.NextMessageSeqTx(env.db, botID)
	if err != nil {
		t.Fatalf("seq: %v", err)
	}
	raw, err := json.Marshal(botBackgroundResultContent{
		CallID: callID, ToolName: "fs_project", Action: "create", Status: "succeeded",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	dedup := "terminal:" + callID
	runIDCopy := runID
	msg := &model.BotMessage{
		ID: uuid.NewString(), BotID: botID, Seq: seq,
		RunID: &runIDCopy,
		Kind:  model.BotMessageKindBackgroundResult, ContentJSON: string(raw),
		SessionKind: model.BotSessionKindSchedule,
		DedupKey:    &dedup, CreatedAt: time.Now().UTC(),
	}
	if err := repository.NewBotMessageRepository(env.db).AppendTx(env.db, msg); err != nil {
		t.Fatalf("append background result: %v", err)
	}
}

// TestBotScheduleInputSinceRunScoped 单元级：schedule 会话的新输入判定按 run
// 收窄，同机器人另一个 schedule run 名下的 background_result 不算自己的新
// 输入（不收窄的查询能看到，证明两条查询确实不同、收窄真正生效）。
func TestBotScheduleInputSinceRunScoped(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedruninput")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	runA := seedScheduleRun(t, env, bot.ID, task.ID)
	runB := seedScheduleRun(t, env, bot.ID, task.ID)
	appendScheduleBackgroundResult(t, env, bot.ID, runB.ID, "call-sibling-"+uuid.NewString()[:8])

	narrowed, err := env.bots.messages.HasModelInputSince(bot.ID, model.BotSessionKindSchedule, runA.ID, runA.InputThroughSeq)
	if err != nil || narrowed {
		t.Fatalf("按 A 的 run 收窄后，B 的消息不应算 A 的新输入: %v %v", narrowed, err)
	}
	unscoped, err := env.bots.messages.HasModelInputSince(bot.ID, model.BotSessionKindSchedule, "", runA.InputThroughSeq)
	if err != nil || !unscoped {
		t.Fatalf("不收窄应能看到 B 的消息（对照）: %v %v", unscoped, err)
	}
	own, err := env.bots.messages.HasModelInputSince(bot.ID, model.BotSessionKindSchedule, runB.ID, 0)
	if err != nil || !own {
		t.Fatalf("B 名下的消息应算 B 自己的新输入: %v %v", own, err)
	}
}

// TestBotScheduleSiblingRunNotBlocking 行为级：A 运行中，B 名下出现更高 seq
// 的 schedule background_result，A 不被 B 的消息卡住收尾、也不据此空跑第二
// 次模型请求，正常落 completed。
func TestBotScheduleSiblingRunNotBlocking(t *testing.T) {
	hold := make(chan struct{})
	env := setupBotTestEnv(t, fakeModelTurn{Text: "A 的定时汇报", Hold: hold})
	userID := createBotTestUser(t, env.db, "schedsibling")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.NeedsReport = false })

	runA := seedScheduleRun(t, env, bot.ID, task.ID)
	env.bots.wakeScheduleRun(bot.ID, runA.ID)
	env.fake.nextRequest(t, 10*time.Second) // A 的第一次模型请求（挂起中）

	// A 运行中：B 的异步写完成，落一条更高 seq 的 schedule 结果消息。
	runB := seedScheduleRun(t, env, bot.ID, task.ID)
	appendScheduleBackgroundResult(t, env, bot.ID, runB.ID, "call-sibling-"+uuid.NewString()[:8])

	close(hold)
	finished := waitForScheduleRunFinished(t, env, runA.ID)
	if finished.Status != model.BotRunStatusCompleted {
		t.Fatalf("A 应正常收尾 completed，不被 B 的消息卡住: %s %v", finished.Status, finished.ErrorMessage)
	}
	// A 只应发过开头那一次模型请求：B 的消息不算 A 的新输入。
	select {
	case extra := <-env.fake.requests:
		t.Fatalf("A 不应因 B 的消息再发模型请求: %.120s", extra.Body)
	case <-time.After(500 * time.Millisecond):
	}
}

// ---------- 5. 默认放行 ----------

func TestBotScheduleDefaultPassAsyncWrite(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-sched-1", Name: "fs_project",
			Args: `{"action":"create","name":"定时创建的项目"}`,
		}}},
		fakeModelTurn{Text: "已创建项目，定时任务完成"},
	)
	userID := createBotTestUser(t, env.db, "schedpass")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.NeedsReport = false })

	run := seedScheduleRun(t, env, bot.ID, task.ID)
	env.bots.wakeScheduleRun(bot.ID, run.ID)
	done := waitForScheduleRunFinished(t, env, run.ID)
	if done.Status != model.BotRunStatusCompleted {
		t.Fatalf("schedule run 应完成: %s %v", done.Status, done.ErrorMessage)
	}

	// 调用直接排队并执行：没有 pending_confirmation，也没有授权记录。
	var call model.BotToolCall
	env.db.Where("bot_id = ?", bot.ID).First(&call)
	if call.Status != model.BotCallStatusSucceeded {
		t.Fatalf("异步写应默认放行并执行成功，实得 %s（%v）", call.Status, call.ErrorMessage)
	}
	var authzCount int64
	env.db.Model(&model.BotAuthorization{}).Where("bot_id = ?", bot.ID).Count(&authzCount)
	if authzCount != 0 {
		t.Fatalf("schedule 会话不应写授权记录，实得 %d", authzCount)
	}
	var pendingCount int64
	env.db.Model(&model.BotToolCall{}).Where("bot_id = ? AND status = ?",
		bot.ID, model.BotCallStatusPendingConfirmation).Count(&pendingCount)
	if pendingCount != 0 {
		t.Fatalf("schedule 会话不应产生待确认调用")
	}
	// 副作用真实发生。
	var projCount int64
	env.db.Table("projects").Where("user_id = ? AND name = ?", userID, "定时创建的项目").Count(&projCount)
	if projCount != 1 {
		t.Fatalf("项目应被真实创建，实得 %d", projCount)
	}
}

// ---------- 6. 事件进主线 ----------

// countScheduleEvents 统计主线会话里的 schedule_event 消息并解码第一条。
func countScheduleEvents(t *testing.T, env *botTestEnv, botID string) (int64, *botScheduleEventContent) {
	t.Helper()
	var rows []model.BotMessage
	if err := env.db.Where("bot_id = ? AND session_kind = ? AND kind = ?",
		botID, model.BotSessionKindMain, model.BotMessageKindScheduleEvent).
		Order("seq ASC").Find(&rows).Error; err != nil {
		t.Fatalf("list events: %v", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	var content botScheduleEventContent
	if err := json.Unmarshal([]byte(rows[0].ContentJSON), &content); err != nil {
		t.Fatalf("decode schedule_event: %v", err)
	}
	if rows[0].RunID != nil {
		t.Fatalf("schedule_event 不应关联具体 run")
	}
	if !rows[0].Kind.IsModelInput() {
		t.Fatalf("schedule_event 应是主线模型输入")
	}
	return int64(len(rows)), &content
}

func TestBotScheduleEventNeedsReportSuccess(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "今日汇总：3 个 issue 有动态"})
	userID := createBotTestUser(t, env.db, "schedrep")
	bot := env.createBot(t, userID)
	due := time.Now().UTC().Add(-3 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = true
		tk.NextRunAt = &due
	})

	env.bots.scheduleTick(time.Now().UTC())
	var run model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).First(&run)
	run = *waitForScheduleRunFinished(t, env, run.ID)
	if run.Status != model.BotRunStatusCompleted || run.Silent {
		t.Fatalf("应为非静默完成: %+v", run)
	}

	n, content := countScheduleEvents(t, env, bot.ID)
	if n != 1 {
		t.Fatalf("需要汇报且非静默应追加 1 条事件，实得 %d", n)
	}
	if content.TaskID != task.ID || content.TaskName != task.Name ||
		content.RunID != run.ID || content.Status != string(model.BotRunStatusCompleted) {
		t.Fatalf("事件内容不完整: %+v", content)
	}
	if !strings.Contains(content.Summary, "汇总") {
		t.Fatalf("事件应带结果摘要: %+v", content)
	}
	// 事件是主线新输入，主线读取面能查到。
	hasNew, err := env.bots.messages.HasModelInputSince(bot.ID, model.BotSessionKindMain, "", 0)
	if err != nil || !hasNew {
		t.Fatalf("schedule_event 应被识别为主线输入: %v %v", hasNew, err)
	}
}

func TestBotScheduleEventSilentAndNoReportSuppressed(t *testing.T) {
	// 成功静默（即使需要汇报）→ 不追加。
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "schedsilent")
	bot := env.createBot(t, userID)
	due := time.Now().UTC().Add(-3 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = true
		tk.NextRunAt = &due
	})
	env.bots.scheduleTick(time.Now().UTC())
	var run model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).First(&run)
	run = *waitForScheduleRunFinished(t, env, run.ID)
	if !run.Silent || run.Summary != "" {
		t.Fatalf("静默运行记录: %+v", run)
	}
	if n, _ := countScheduleEvents(t, env, bot.ID); n != 0 {
		t.Fatalf("成功静默不应追加事件，实得 %d", n)
	}

	// 成功但不需要汇报 → 不追加。
	env2 := setupBotTestEnv(t, fakeModelTurn{Text: "常规执行完成"})
	userID2 := createBotTestUser(t, env2.db, "schednorep")
	bot2 := env2.createBot(t, userID2)
	due2 := time.Now().UTC().Add(-3 * time.Second)
	task2 := seedScheduleTask(t, env2, bot2.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false
		tk.NextRunAt = &due2
	})
	env2.bots.scheduleTick(time.Now().UTC())
	var run2 model.BotRun
	env2.db.Where("schedule_task_id = ?", task2.ID).First(&run2)
	waitForScheduleRunFinished(t, env2, run2.ID)
	if n, _ := countScheduleEvents(t, env2, bot2.ID); n != 0 {
		t.Fatalf("成功且不需汇报不应追加事件，实得 %d", n)
	}
}

func TestBotScheduleEventOnFailure(t *testing.T) {
	env := setupBotTestEnv(t)
	env.fake.strict = true // 脚本耗尽即 500：模拟模型请求失败
	userID := createBotTestUser(t, env.db, "schedfail")
	bot := env.createBot(t, userID)
	due := time.Now().UTC().Add(-3 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false // 失败与 needs_report 无关，必报
		tk.NextRunAt = &due
	})

	env.bots.scheduleTick(time.Now().UTC())
	var run model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).First(&run)
	run = *waitForScheduleRunFinished(t, env, run.ID)
	if run.Status != model.BotRunStatusFailed || run.ErrorCode == nil {
		t.Fatalf("应为失败终态: %+v", run)
	}
	n, content := countScheduleEvents(t, env, bot.ID)
	if n != 1 {
		t.Fatalf("失败必报，实得 %d 条事件", n)
	}
	if content.Status != string(model.BotRunStatusFailed) || content.ErrorCode == "" {
		t.Fatalf("失败事件应带状态与错误码: %+v", content)
	}
	if content.ErrorMessage == "" || run.ErrorMessage == nil || *run.ErrorMessage == "" {
		t.Fatalf("失败事件应带错误信息: %+v", content)
	}
}

// ---------- 7. 运行记录内容 ----------

func TestBotScheduleRunRecord(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{
		Text:  "运行报告内容",
		Usage: &fakeModelUsage{Prompt: 100, Completion: 20, Total: 120, Cached: 0},
	})
	userID := createBotTestUser(t, env.db, "schedrec")
	bot := env.createBot(t, userID)
	due := time.Now().UTC().Add(-3 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NextRunAt = &due
		tk.NeedsReport = false
	})

	env.bots.scheduleTick(time.Now().UTC())
	var run model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).First(&run)
	waitForScheduleRunFinished(t, env, run.ID)

	runs, err := env.bots.ListScheduleTaskRuns(userID, bot.ID, task.ID, 20, 0)
	if err != nil || len(runs) != 1 {
		t.Fatalf("list runs: %v %d", err, len(runs))
	}
	rec := runs[0]
	if rec.Status != string(model.BotRunStatusCompleted) {
		t.Fatalf("记录状态: %+v", rec)
	}
	if rec.Summary != "运行报告内容" || rec.Silent {
		t.Fatalf("记录摘要/静默: %+v", rec)
	}
	if rec.DurationMS == nil || *rec.DurationMS < 0 {
		t.Fatalf("应记录耗时: %+v", rec.DurationMS)
	}
	if rec.TotalTokens == nil || *rec.TotalTokens != 120 ||
		rec.PromptTokens == nil || *rec.PromptTokens != 100 {
		t.Fatalf("应记录 usage: %+v", rec)
	}
	if rec.ScheduledFor == nil || rec.StartedAt == nil || rec.FinishedAt == nil {
		t.Fatalf("应记录计划/起止时间: %+v", rec)
	}
	if rec.TaskID == nil || *rec.TaskID != task.ID {
		t.Fatalf("应记录任务引用: %+v", rec.TaskID)
	}
	// 冻结的 API 形状：关键字段名不得漂移。
	raw, _ := json.Marshal(rec)
	for _, key := range []string{
		`"id"`, `"task_id"`, `"status"`, `"summary"`, `"silent"`,
		`"scheduled_for"`, `"started_at"`, `"finished_at"`, `"duration_ms"`,
		`"total_tokens"`,
	} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("运行记录 JSON 缺字段 %s: %s", key, raw)
		}
	}
}

// ---------- 8. fs_schedule 工具与校验 ----------

func botScheduleCall(t *testing.T, env *botTestEnv, botID, args string) (string, error) {
	t.Helper()
	return env.bots.execScheduleTool(&model.BotToolCall{
		ID: uuid.NewString(), BotID: botID, ArgumentsJSON: args,
	})
}

func TestBotScheduleToolLifecycle(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "schedtool")
	bot := env.createBot(t, userID)

	// create：结果带任务信息与执行时间，供模型向用户转述。
	out, err := botScheduleCall(t, env, bot.ID,
		`{"action":"create","name":"晨报","cron":"0 9 * * *","timezone":"Asia/Shanghai","instruction":"生成晨报","needs_report":true}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.Contains(out, `"next_run_at"`) || !strings.Contains(out, "09:00") {
		t.Fatalf("create 结果应含执行时间: %s", out)
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil || created.ID == "" {
		t.Fatalf("create 结果应含任务 ID: %s", out)
	}

	// list。
	out, err = botScheduleCall(t, env, bot.ID, `{"action":"list"}`)
	if err != nil || !strings.Contains(out, "晨报") {
		t.Fatalf("list: %v %s", err, out)
	}

	// update：改 cron 后 next_run_at 重算。
	out, err = botScheduleCall(t, env, bot.ID,
		`{"action":"update","task_id":"`+created.ID+`","cron":"30 10 * * *"}`)
	if err != nil || !strings.Contains(out, "10:30") {
		t.Fatalf("update: %v %s", err, out)
	}

	// runs：先触发一次执行再查。
	due := time.Now().UTC().Add(-3 * time.Second)
	env.db.Model(&model.BotScheduleTask{}).Where("id = ?", created.ID).Update("next_run_at", due)
	env.bots.scheduleTick(time.Now().UTC())
	var run model.BotRun
	env.db.Where("schedule_task_id = ?", created.ID).First(&run)
	waitForScheduleRunFinished(t, env, run.ID)
	out, err = botScheduleCall(t, env, bot.ID, `{"action":"runs","task_id":"`+created.ID+`"}`)
	if err != nil || !strings.Contains(out, `"status"`) {
		t.Fatalf("runs: %v %s", err, out)
	}

	// disable / enable：停用后 next_run_at 清空，启用后重算到未来。
	out, err = botScheduleCall(t, env, bot.ID, `{"action":"disable","task_id":"`+created.ID+`"}`)
	if err != nil || !strings.Contains(out, `"next_run_at":null`) {
		t.Fatalf("disable: %v %s", err, out)
	}
	out, err = botScheduleCall(t, env, bot.ID, `{"action":"enable","task_id":"`+created.ID+`"}`)
	if err != nil || strings.Contains(out, `"next_run_at":null`) {
		t.Fatalf("enable: %v %s", err, out)
	}

	// delete。
	if _, err := botScheduleCall(t, env, bot.ID, `{"action":"delete","task_id":"`+created.ID+`"}`); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := env.bots.schedTasks.FindByIDAndBot(created.ID, bot.ID); err == nil {
		t.Fatalf("任务应已删除")
	}
	// 运行记录保留（账本供排查）。
	var runCount int64
	env.db.Model(&model.BotRun{}).Where("schedule_task_id = ?", created.ID).Count(&runCount)
	if runCount == 0 {
		t.Fatalf("删除任务后运行记录应保留")
	}

	// 参数校验（走真实受理路径）：create 缺 instruction / 带 task_id；
	// delete 缺 task_id；未知字段拒绝。
	v3 := botToolsetVersionOf(3)
	for _, bad := range []string{
		`{"action":"create","name":"x","cron":"0 9 * * *"}`,
		`{"action":"create","name":"x","cron":"0 9 * * *","instruction":"y","task_id":"abc"}`,
		`{"action":"delete"}`,
		`{"action":"create","name":"x","cron":"0 9 * * *","instruction":"y","unknown_field":1}`,
	} {
		if _, err := botPrepareToolCall(v3, "fs_schedule", bad); err == nil {
			t.Fatalf("受理应拒绝参数: %s", bad)
		}
	}
	// 执行层同样拒绝不存在的任务引用。
	for _, bad := range []string{
		`{"action":"delete"}`,
		`{"action":"runs","task_id":"missing"}`,
	} {
		if _, err := botScheduleCall(t, env, bot.ID, bad); err == nil {
			t.Fatalf("应拒绝参数: %s", bad)
		}
	}
}

// TestBotScheduleDeleteMissingTaskNotFound 删除不存在的任务必须报「定时任务
// 不存在」（40429），不能落成 internal error：仓库层未命中返回
// gorm.ErrRecordNotFound，服务层据此映射。
func TestBotScheduleDeleteMissingTaskNotFound(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "scheddel404")
	bot := env.createBot(t, userID)

	_, err := botScheduleCall(t, env, bot.ID, `{"action":"delete","task_id":"missing"}`)
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrBotScheduleTaskNotFound.Code {
		t.Fatalf("删除不存在的任务应返回 40429，实得 %v", err)
	}
}

// 已发布工具集的 wire 字节 SHA-256：改动任一版本都会让存量会话的前缀与
// tools_hash 失稳，这里钉死。这是跨版本契约，不是实现细节；v4 自 INT-120
// 起冻结（基线在 bot_int120_test.go），v5 自 INT-121、v6 自 INT-123、v7 自
// INT-136 起冻结（基线在下方），v8 是当前发布版本（基线在 bot_int136_test.go）。
const (
	botToolsetV1FrozenHash = "c1da7e95288ed681392529f68a812ec1ac403bcd53cee1414634cca28197ef06"
	botToolsetV2FrozenHash = "4ec300f75e9aff618b820246c85cc497db6e4d8027cbce342696cc629560ca19"
	// v3 在 INT-110 后冻结：schema 修正只进 v4，旧会话继续用旧字节。
	botToolsetV3FrozenHash = "9e420a0b28375b39967b9080dc46b28cb876e40ad04c7a402043e8dfc9c7cd07"
	// v5 在 INT-121 后冻结：fs_schedule 的 run/dry_run 只进 v6（冻结时 14344 字节）。
	botToolsetV5FrozenHash = "779efef0c1086f22765374fd00fff66d2e0b31c4c5fca9283989a5882797b3b0"
	// v6 在 INT-123 后冻结：fs_schedule 的 state_get/state_set 与 state 参数
	// 只进 v7（冻结时 15215 字节）。
	botToolsetV6FrozenHash = "c55b88e31debf5ff87bac9cb7d5b67f756a2395ce17d381944cd0e3b13361b6b"
	// v7 在 INT-136 后冻结：fs_schedule 的确认语义描述只进 v8，动作定义与
	// 参数结构体不变（确认判定在受理端，不进 wire 字节）。
	botToolsetV7FrozenHash = "e275c8ce9cae472cb9b9a97be3cf042e699ee29d63e37509cdf8cb8994cfe8b4"
)

func TestBotScheduleToolsetVersions(t *testing.T) {
	v1 := botToolsetVersionOf(1)
	v2 := botToolsetVersionOf(2)
	v3 := botToolsetVersionOf(3)
	v4 := botToolsetVersionOf(4)
	v5 := botToolsetVersionOf(5)
	v6 := botToolsetVersionOf(6)
	v7 := botToolsetVersionOf(7)
	if v1.hash == v2.hash || v2.hash == v3.hash || v3.hash == v4.hash ||
		v4.hash == v5.hash || v5.hash == v6.hash || v6.hash == v7.hash {
		t.Fatalf("相邻版本的工具集 hash 必须互不相同")
	}
	// 已发布版本的 wire 字节已冻结：v1-v7 的 hash 必须与基线逐字节一致。
	if v1.hash != botToolsetV1FrozenHash || v2.hash != botToolsetV2FrozenHash ||
		v3.hash != botToolsetV3FrozenHash || v4.hash != botToolsetV4FrozenHash ||
		v5.hash != botToolsetV5FrozenHash || v6.hash != botToolsetV6FrozenHash ||
		v7.hash != botToolsetV7FrozenHash {
		t.Fatalf("v1-v7 tools_hash 偏离冻结基线: v1=%s v2=%s v3=%s v4=%s v5=%s v6=%s v7=%s",
			v1.hash, v2.hash, v3.hash, v4.hash, v5.hash, v6.hash, v7.hash)
	}
	if len(v7.schemas) != 11 || len(v6.schemas) != 11 || len(v5.schemas) != 11 ||
		len(v4.schemas) != 11 || len(v3.schemas) != 11 || len(v2.schemas) != 11 ||
		len(v1.schemas) != 10 {
		t.Fatalf("v1-v7 工具数应为 10/11/11/11/11/11/11，实得 %d/%d/%d/%d/%d/%d/%d",
			len(v1.schemas), len(v2.schemas), len(v3.schemas), len(v4.schemas),
			len(v5.schemas), len(v6.schemas), len(v7.schemas))
	}
	// v4/v5 冻结的 fs_schedule：7 个同步动作、冻结参数结构体（不含 run/dry_run）。
	for _, frozen := range []*botToolsetHolder{v4, v5} {
		sched := frozen.byName["fs_schedule"]
		if sched == nil || len(sched.Actions) != 7 {
			t.Fatalf("v%d fs_schedule 应冻结为 7 个动作: %+v", frozen.version, sched)
		}
		if sched.ArgsType.Name() != "botScheduleTaskArgsV5" {
			t.Fatalf("v%d fs_schedule 应使用冻结参数结构体 botScheduleTaskArgsV5，实得 %s",
				frozen.version, sched.ArgsType.Name())
		}
		got := map[string]bool{}
		for _, a := range sched.Actions {
			got[a.Name] = true
			if a.Mode != model.BotExecutionSync || a.RequiresConfirmation {
				t.Fatalf("fs_schedule 动作应全部同步且无需确认: %s", a.Name)
			}
		}
		for _, want := range []string{"list", "create", "update", "delete", "runs", "enable", "disable"} {
			if !got[want] {
				t.Fatalf("v%d fs_schedule 缺动作 %s", frozen.version, want)
			}
		}
		if got["run"] {
			t.Fatalf("v%d fs_schedule 不应含 run 动作", frozen.version)
		}
		raw, _ := json.Marshal(sched.Parameters)
		if strings.Contains(string(raw), `"run"`) {
			t.Fatalf("v%d schema 不得出现 run 枚举: %s", frozen.version, raw)
		}
		if strings.Contains(string(raw), "dry_run") {
			t.Fatalf("v%d schema 不得出现 dry_run: %s", frozen.version, raw)
		}
	}
	// v1/v2 冻结的 fs_schedule 仍是 status 占位。
	for _, v := range map[int64]*botToolsetHolder{1: v1, 2: v2} {
		s := v.byName["fs_schedule"]
		if s == nil || len(s.Actions) != 1 || s.Actions[0].Name != "status" {
			t.Fatalf("v%d fs_schedule 应冻结为 status 占位", v.version)
		}
		if s.ArgsType.Name() != "botScheduleArgs" {
			t.Fatalf("v%d fs_schedule 应使用冻结的占位参数结构体", v.version)
		}
	}
	// v3 冻结的参数结构体：schema enum 仍是历史生成结果（只列 list）。
	if v3.byName["fs_schedule"].ArgsType.Name() != "botScheduleTaskArgsFrozen" {
		t.Fatalf("v3 fs_schedule 应使用冻结参数结构体")
	}
	v3SchemaRaw, _ := json.Marshal(v3.byName["fs_schedule"].Parameters)
	var v3Schema map[string]interface{}
	_ = json.Unmarshal(v3SchemaRaw, &v3Schema)
	v3Props, _ := v3Schema["properties"].(map[string]interface{})
	v3Action, _ := v3Props["action"].(map[string]interface{})
	if enum, _ := v3Action["enum"].([]interface{}); len(enum) != 1 || enum[0] != "list" {
		t.Fatalf("v3 schema 应保持冻结的旧枚举: %v", enum)
	}
	// v3 会话调旧动作 status → 改名提示；v2 会话仍可用 status。
	if _, err := botPrepareToolCall(v3, "fs_schedule", `{"action":"status"}`); err == nil {
		t.Fatalf("v3 不应再接受 fs_schedule/status")
	}
	prepared, err := botPrepareToolCall(v2, "fs_schedule", `{"action":"status"}`)
	if err != nil || prepared.Action != "status" || prepared.Mode != model.BotExecutionSync {
		t.Fatalf("v2 fs_schedule/status 应照常可用: %v %+v", err, prepared)
	}
	// v6（INT-123 起冻结）的 fs_schedule：8 个同步动作，含 run 与 dry_run，
	// 用冻结参数结构体 botScheduleTaskArgsV6。
	sched6 := v6.byName["fs_schedule"]
	if sched6 == nil || len(sched6.Actions) != 8 {
		t.Fatalf("v6 fs_schedule 应有 8 个动作: %+v", sched6)
	}
	got6 := map[string]bool{}
	for _, a := range sched6.Actions {
		got6[a.Name] = true
		if a.Mode != model.BotExecutionSync || a.RequiresConfirmation {
			t.Fatalf("v6 fs_schedule 动作应全部同步且无需确认: %s", a.Name)
		}
	}
	for _, want := range []string{"list", "create", "update", "delete", "runs", "run", "enable", "disable"} {
		if !got6[want] {
			t.Fatalf("v6 fs_schedule 缺动作 %s", want)
		}
	}
	if got6["state_get"] || got6["state_set"] {
		t.Fatalf("v6 fs_schedule 不应含 state_get/state_set 动作")
	}
	if sched6.ArgsType.Name() != "botScheduleTaskArgsV6" {
		t.Fatalf("v6 fs_schedule 应使用冻结参数结构体 botScheduleTaskArgsV6，实得 %s", sched6.ArgsType.Name())
	}
	v6SchemaRaw, _ := json.Marshal(sched6.Parameters)
	var v6Schema map[string]interface{}
	if err := json.Unmarshal(v6SchemaRaw, &v6Schema); err != nil {
		t.Fatalf("decode v6 schema: %v", err)
	}
	v6Props, _ := v6Schema["properties"].(map[string]interface{})
	if v6Props == nil || v6Props["dry_run"] == nil {
		t.Fatalf("v6 schema 应含 dry_run 属性: %s", v6SchemaRaw)
	}
	if v6Props["state"] != nil {
		t.Fatalf("v6 schema 不得出现 state 属性: %s", v6SchemaRaw)
	}
	if strings.Contains(string(v6SchemaRaw), "state_get") {
		t.Fatalf("v6 schema 不得出现 state_get: %s", v6SchemaRaw)
	}
	v6Action, _ := v6Props["action"].(map[string]interface{})
	runInEnum := false
	for _, e := range v6Action["enum"].([]interface{}) {
		if e == "run" {
			runInEnum = true
		}
	}
	if !runInEnum {
		t.Fatalf("v6 action 枚举应含 run: %s", v6SchemaRaw)
	}
	if !strings.Contains(v6Action["description"].(string), "run=task_id") {
		t.Fatalf("v6 action 描述应注出 run 的必填字段: %v", v6Action["description"])
	}
	// v6 受理路径：run 必填 task_id；dry_run 可与 run 同用；state 动作未声明。
	if _, err := botPrepareToolCall(v6, "fs_schedule", `{"action":"run"}`); err == nil {
		t.Fatalf("run 缺 task_id 应被拒绝")
	}
	if p, err := botPrepareToolCall(v6, "fs_schedule", `{"action":"run","task_id":"t1","dry_run":true}`); err != nil || p.Action != "run" {
		t.Fatalf("run+dry_run 应被受理: %v %+v", err, p)
	}
	if _, err := botPrepareToolCall(v6, "fs_schedule", `{"action":"state_get"}`); err == nil {
		t.Fatalf("v6 不应接受 fs_schedule/state_get")
	}
	if _, err := botPrepareToolCall(v6, "fs_schedule", `{"action":"state_set"}`); err == nil {
		t.Fatalf("v6 不应接受 fs_schedule/state_set")
	}
	// v5 会话调 run：动作未声明，拒绝（不偷执行 v6 新动作）。
	if _, err := botPrepareToolCall(v5, "fs_schedule", `{"action":"run","task_id":"t1"}`); err == nil {
		t.Fatalf("v5 不应接受 fs_schedule/run")
	}
	// v7（INT-136 起冻结）的 fs_schedule：动作定义保持 10 个同步动作，
	// state_get/state_set 与 state 参数进 schema；确认判定在受理端
	//（botScheduleConfirmation），不在冻结字节里。
	sched7 := v7.byName["fs_schedule"]
	if sched7 == nil || len(sched7.Actions) != 10 {
		t.Fatalf("v7 fs_schedule 应有 10 个动作: %+v", sched7)
	}
	got7 := map[string]bool{}
	for _, a := range sched7.Actions {
		got7[a.Name] = true
		if a.Mode != model.BotExecutionSync || a.RequiresConfirmation {
			t.Fatalf("v7 fs_schedule 动作应全部同步且无需确认: %s", a.Name)
		}
	}
	for _, want := range []string{
		"list", "create", "update", "delete", "runs", "run", "enable", "disable",
		"state_get", "state_set",
	} {
		if !got7[want] {
			t.Fatalf("v7 fs_schedule 缺动作 %s", want)
		}
	}
	if sched7.ArgsType.Name() != "botScheduleTaskArgs" {
		t.Fatalf("v7 fs_schedule 应使用 botScheduleTaskArgs，实得 %s", sched7.ArgsType.Name())
	}
	if !botReadOnlyActions["fs_schedule/state_get"] || botReadOnlyActions["fs_schedule/state_set"] {
		t.Fatalf("state_get 应计入只读动作，state_set 应按写处理")
	}
	v7SchemaRaw, _ := json.Marshal(sched7.Parameters)
	var v7Schema map[string]interface{}
	if err := json.Unmarshal(v7SchemaRaw, &v7Schema); err != nil {
		t.Fatalf("decode v7 schema: %v", err)
	}
	v7Props, _ := v7Schema["properties"].(map[string]interface{})
	if v7Props == nil || v7Props["state"] == nil {
		t.Fatalf("v7 schema 应含 state 属性: %s", v7SchemaRaw)
	}
	v7Action, _ := v7Props["action"].(map[string]interface{})
	stateActions := map[string]bool{}
	for _, e := range v7Action["enum"].([]interface{}) {
		if e == "state_get" || e == "state_set" {
			stateActions[e.(string)] = true
		}
	}
	if !stateActions["state_get"] || !stateActions["state_set"] {
		t.Fatalf("v7 action 枚举应含 state_get/state_set: %s", v7SchemaRaw)
	}
	// v7 受理路径：state 动作不带 task_id；state 只归 state_set。
	if _, err := botPrepareToolCall(v7, "fs_schedule", `{"action":"state_set","task_id":"t1"}`); err == nil {
		t.Fatalf("state_set 带 task_id 应被拒绝")
	}
	if _, err := botPrepareToolCall(v7, "fs_schedule", `{"action":"list","state":{"a":1}}`); err == nil {
		t.Fatalf("list 携带 state 应被拒绝")
	}
	if p, err := botPrepareToolCall(v7, "fs_schedule", `{"action":"state_set"}`); err != nil || p.Action != "state_set" {
		t.Fatalf("state_set 省略 state 应被受理: %v %+v", err, p)
	}
	if p, err := botPrepareToolCall(v7, "fs_schedule", `{"action":"state_set","state":null}`); err != nil || p.Action != "state_set" {
		t.Fatalf("state_set 显式 null 应被受理: %v %+v", err, p)
	}
	if p, err := botPrepareToolCall(v7, "fs_schedule", `{"action":"state_get"}`); err != nil || p.Action != "state_get" {
		t.Fatalf("state_get 应被受理: %v %+v", err, p)
	}
	// v6 会话不接受 state_get/state_set（动作未声明，不偷执行 v7 新动作）；
	// v7 会话不接受 dry_run 与 state 的非法组合。
	if _, err := botPrepareToolCall(v6, "fs_schedule", `{"action":"state_set","state":{"a":1}}`); err == nil {
		t.Fatalf("v6 不应接受 fs_schedule/state_set")
	}
	if _, err := botPrepareToolCall(v7, "fs_schedule", `{"action":"state_set","dry_run":true}`); err == nil {
		t.Fatalf("state_set 携带 dry_run 应被拒绝")
	}
}

// ---------- 9. 恢复路径的原子性与停用任务的计划 ----------

// 恢复中断的 schedule run 时，失败事件必须与终态同一事务：事务失败时 run
// 不能变成 failed（否则下次启动不再遍历它，事件永远补不回来）。
func TestBotScheduleRecoveryEventAtomic(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedrecov")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	recovered, err := env.bots.runs.FindByID(run.ID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if recovered.Status != model.BotRunStatusFailed || recovered.FinishedAt == nil {
		t.Fatalf("中断的 schedule run 应置 failed: %+v", recovered)
	}
	if n, content := countScheduleEvents(t, env, bot.ID); n != 1 {
		t.Fatalf("恢复应追加 1 条运行事件，实得 %d", n)
	} else if content.Status != string(model.BotRunStatusFailed) || content.ErrorCode != "server_restarted" {
		t.Fatalf("恢复事件应记 server_restarted: %+v", content)
	}

	// 让事件写入失败，验证终态与事件同事务：run 必须保持 running。
	if err := env.db.Exec(`CREATE TRIGGER zz_block_sched_event BEFORE INSERT ON bot_messages
		WHEN NEW.kind = 'schedule_event' BEGIN SELECT RAISE(ABORT, 'blocked'); END`).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	t.Cleanup(func() {
		_ = env.db.Exec(`DROP TRIGGER IF EXISTS zz_block_sched_event`).Error
	})
	second := seedScheduleRun(t, env, bot.ID, task.ID)
	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("second recover: %v", err)
	}
	still := mustFindRun(t, env, second.ID)
	if still.Status != model.BotRunStatusRunning || still.FinishedAt != nil {
		t.Fatalf("事件写入失败时终态必须回滚，run 应保持 running: %+v", still)
	}
}

func mustFindRun(t *testing.T, env *botTestEnv, runID string) *model.BotRun {
	t.Helper()
	run, err := env.bots.runs.FindByID(runID)
	if err != nil {
		t.Fatalf("find run: %v", err)
	}
	return run
}

// 停用的任务改 cron/时区不该重新算出下次计划：只有重新启用才重算。
func TestBotScheduleUpdateDisabledKeepsNoNextRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "scheddis")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, task.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	off, _ := env.bots.schedTasks.FindByID(task.ID)
	if off.NextRunAt != nil {
		t.Fatalf("停用后 next_run_at 应清空: %+v", off.NextRunAt)
	}
	updated, err := env.bots.updateScheduleTask(bot.ID, task.ID, "", "0 10 * * *", "Asia/Shanghai", "", nil, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.NextRunAt != nil {
		t.Fatalf("停用任务改 cron 后仍不应有计划时间: %+v", updated.NextRunAt)
	}
	persisted, _ := env.bots.schedTasks.FindByID(task.ID)
	if persisted.NextRunAt != nil || persisted.Enabled {
		t.Fatalf("库里的停用任务不应有计划时间: %+v", persisted)
	}
	// 重新启用才重算。
	on, err := env.bots.setScheduleTaskEnabled(bot.ID, task.ID, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if on.NextRunAt == nil || !on.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("重新启用应重算未来计划: %+v", on.NextRunAt)
	}
}

// 密钥与地址成对冻结：派发后轮换密钥并把地址指向别处，这次运行必须仍用
// 派发时的密钥与地址（否则会把新密钥发往旧供应商）。
func TestBotScheduleFrozenCredentialPairing(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "schedcred")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false
	})

	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	now := time.Now().UTC()
	planned := now.Add(-time.Second)
	taskID := task.ID
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource:  model.BotTriggerSchedule,
		TriggerKey:     botScheduleTriggerKey(taskID, planned),
		Status:         model.BotRunStatusRunning,
		SessionKind:    model.BotSessionKindSchedule,
		ScheduleTaskID: &taskID, ScheduledFor: &planned,
		ConfigSnapshotJSON:      marshalJSONString(botSnapshotOf(botRow)),
		APIKeyEncryptedSnapshot: append([]byte(nil), botRow.APIKeyEncrypted...),
		CreatedAt:               now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// 派发之后轮换 provider 密钥、换地址（地址与密钥都在 provider 行上，
	// INT-133）：新密钥不得发往旧地址，run 仍按派发时成对冻结的快照执行。
	if err := env.db.Model(&model.AIProvider{}).Where("id = ?", botRow.ProviderID).
		Updates(map[string]interface{}{
			"api_key_encrypted": []byte("rotated-cipher-not-decryptable"),
			"base_url":          "http://edited.invalid/v1",
		}).Error; err != nil {
		t.Fatalf("rotate provider: %v", err)
	}
	env.bots.wakeScheduleRun(bot.ID, run.ID)
	done := waitForScheduleRunFinished(t, env, run.ID)
	if done.Status != model.BotRunStatusCompleted {
		t.Fatalf("应使用冻结密钥正常完成: %s %v", done.Status, done.ErrorMessage)
	}
	req := env.fake.nextRequest(t, 10*time.Second)
	if !strings.Contains(req.Authorization, "sk-test-abcdef123456") {
		t.Fatalf("运行应使用派发时冻结的密钥，实得 Authorization=%q", req.Authorization)
	}
}

// 运行内还有未结算的异步调用时不得收尾：调用结果可能是失败，是运行结果的一部分。
func TestBotScheduleCompletionWaitsForSettledCalls(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedsettle")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	call := seedUnsettledScheduleCall(t, env, run)

	if env.bots.tryFinishScheduleRun(botRow, run) {
		t.Fatalf("还有未结算调用时不应完成")
	}
	fresh, _ := env.bots.runs.FindByID(run.ID)
	if fresh.FinishedAt != nil {
		t.Fatalf("未结算时 run 不应落终态: %+v", fresh)
	}

	// 结算后可以完成（真实失败/成功由模型的下一轮汇报）。
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", call.ID).
		Updates(map[string]interface{}{"status": model.BotCallStatusSucceeded, "finished_at": time.Now().UTC()}).Error; err != nil {
		t.Fatalf("settle call: %v", err)
	}
	if !env.bots.tryFinishScheduleRun(botRow, run) {
		t.Fatalf("调用结算后应能完成")
	}
}

// 等待结算：未结算时到点返回 timeout，结算后返回 settled。
func TestBotScheduleWaitForCallSettlement(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedwait")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	call := seedUnsettledScheduleCall(t, env, run)

	if got := env.bots.waitScheduleCallsSettledUntil(context.Background(), run, time.Now()); got != scheduleWaitTimeout {
		t.Fatalf("未结算且到点应 timeout，实得 %v", got)
	}
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", call.ID).
		Updates(map[string]interface{}{"status": model.BotCallStatusFailed, "finished_at": time.Now().UTC()}).Error; err != nil {
		t.Fatalf("settle call: %v", err)
	}
	if got := env.bots.waitScheduleCallsSettledUntil(context.Background(), run, time.Now().Add(time.Second)); got != scheduleWaitSettled {
		t.Fatalf("结算后应 settled，实得 %v", got)
	}
}

// seedUnsettledScheduleCall 落一次属于该 run 的排队中异步调用（经模型请求关联）。
func seedUnsettledScheduleCall(t *testing.T, env *botTestEnv, run *model.BotRun) *model.BotToolCall {
	t.Helper()
	now := time.Now().UTC()
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, PromptRevision: 1, ToolsetVersion: 3,
		StartedAt: now, FinishedAt: &now,
	}
	if err := env.db.Create(req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: run.BotID, RequestID: req.ID,
		ProviderToolCallID: "prov-settle", ToolName: "fs_project", Action: "create",
		ArgumentsJSON: "{}", ArgumentsHash: "x", ExecutionMode: "async",
		Status: model.BotCallStatusQueued, ExecutionContextJSON: "{}",
		CreatedAt: now,
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("seed call: %v", err)
	}
	return call
}

// 失败终态被停止请求抢先后必须回落 stopped：否则 run 永远停在
// running + stop_requested，无人再消费，还会让机器人删除一直被拒绝。
func TestBotScheduleFailedFallsBackToStopped(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstoprace")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	// 停止请求抢先落库：failed 的 CAS 必须失败。
	if err := env.db.Model(&model.BotRun{}).Where("id = ?", run.ID).
		Update("stop_requested_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("mark stop: %v", err)
	}
	env.bots.finishScheduleFailed(botRow, run, "model_request_failed",
		errs.New(errs.ErrInternal.Code, "模拟失败"))
	fresh := mustFindRun(t, env, run.ID)
	if fresh.Status != model.BotRunStatusStopped || fresh.FinishedAt == nil {
		t.Fatalf("停止抢先时应回落 stopped，实得 %+v", fresh)
	}
}

// 丢失唤醒兜底：扫描能把卡在 running 的 schedule run 重新推回执行槽。
func TestBotScheduleScanResumesStuckRun(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "schedscan")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	env.bots.scanOnce()
	done := waitForScheduleRunFinished(t, env, run.ID)
	if done.Status != model.BotRunStatusCompleted {
		t.Fatalf("重扫应把 run 跑完: %s %v", done.Status, done.ErrorMessage)
	}
}

// 模型覆盖可显式清除：省略 model 保持原值，传空串回到机器人当前模型。
func TestBotScheduleUpdateClearsModelOverride(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedmodel")
	bot := env.createBot(t, userID)
	task, err := env.bots.createScheduleTask(bot.ID, "模型任务", "0 9 * * *", "Asia/Shanghai", "跑一下", "cheap-model", false)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if task.Model != "cheap-model" {
		t.Fatalf("create 应记录模型覆盖: %+v", task.Model)
	}
	// 省略 model（nil）：保持覆盖。
	kept, err := env.bots.updateScheduleTask(bot.ID, task.ID, "改名只", "", "", "", nil, nil)
	if err != nil {
		t.Fatalf("update keep: %v", err)
	}
	if kept.Model != "cheap-model" || kept.Name != "改名只" {
		t.Fatalf("省略 model 应保留原覆盖: %+v", kept)
	}
	// 显式空串：清除覆盖，回到机器人当前模型。
	empty := ""
	cleared, err := env.bots.updateScheduleTask(bot.ID, task.ID, "", "", "", "", &empty, nil)
	if err != nil {
		t.Fatalf("update clear: %v", err)
	}
	if cleared.Model != "" {
		t.Fatalf("传空串应清除模型覆盖，实得 %q", cleared.Model)
	}
	stored, _ := env.bots.schedTasks.FindByID(task.ID)
	if stored.Model != "" {
		t.Fatalf("库里应已清除模型覆盖，实得 %q", stored.Model)
	}
}

// 任务列表分页：total 是真实总数、has_more 提示还有更旧的，不能用截断结果当总量。
func TestBotScheduleListPagination(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedlist")
	bot := env.createBot(t, userID)
	for _, name := range []string{"任务A", "任务B", "任务C"} {
		if _, err := env.bots.createScheduleTask(bot.ID, name, "0 9 * * *", "Asia/Shanghai", "跑一下", "", false); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	type listOut struct {
		Items   []map[string]interface{} `json:"items"`
		Total   int64                    `json:"total"`
		Limit   int                      `json:"limit"`
		Offset  int                      `json:"offset"`
		HasMore bool                     `json:"has_more"`
	}
	decode := func(out string) listOut {
		var parsed listOut
		if err := json.Unmarshal([]byte(out), &parsed); err != nil {
			t.Fatalf("decode list: %v %s", err, out)
		}
		return parsed
	}

	first := decode(mustScheduleCall(t, env, bot.ID, `{"action":"list","limit":2}`))
	if len(first.Items) != 2 || first.Total != 3 || !first.HasMore || first.Limit != 2 || first.Offset != 0 {
		t.Fatalf("首页应为 2/3 且 has_more: %+v", first)
	}
	second := decode(mustScheduleCall(t, env, bot.ID, `{"action":"list","limit":2,"offset":2}`))
	if len(second.Items) != 1 || second.Total != 3 || second.HasMore || second.Offset != 2 {
		t.Fatalf("第二页应为 1/3 且无 has_more: %+v", second)
	}
}

func mustScheduleCall(t *testing.T, env *botTestEnv, botID, args string) string {
	t.Helper()
	out, err := botScheduleCall(t, env, botID, args)
	if err != nil {
		t.Fatalf("call %s: %v", args, err)
	}
	return out
}

// 任务在运行失败前被删掉时，失败事件仍要进主线（失败必报，且用户已无法从
// 任务查询看到这次失败），任务名用派发时记下的那份。
func TestBotScheduleFailureEventAfterTaskDeleted(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "scheddeltask")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	// 派发时会把任务名写进触发上下文；这里补上并删掉任务行。
	if err := env.db.Model(&model.BotRun{}).Where("id = ?", run.ID).
		Update("trigger_context_json", marshalJSONString(map[string]interface{}{
			"task_id": task.ID, "task_name": task.Name,
		})).Error; err != nil {
		t.Fatalf("set trigger context: %v", err)
	}
	if err := env.db.Exec(`DELETE FROM bot_schedule_tasks WHERE id = ?`, task.ID).Error; err != nil {
		t.Fatalf("delete task: %v", err)
	}
	run.TriggerContextJSON = marshalJSONString(map[string]interface{}{
		"task_id": task.ID, "task_name": task.Name,
	})

	env.bots.finishScheduleFailed(botRow, run, "schedule_task_missing",
		errs.New(errs.ErrInternal.Code, "定时任务已删除"))
	n, content := countScheduleEvents(t, env, bot.ID)
	if n != 1 {
		t.Fatalf("任务被删除后失败事件仍应追加，实得 %d", n)
	}
	if content.TaskName != task.Name || content.Status != string(model.BotRunStatusFailed) {
		t.Fatalf("事件应带派发时的任务名与失败状态: %+v", content)
	}
}

// 恢复扫描必须扫完所有未完成的 schedule run：只取最早一页时，最早的若干条
// 若都占着槽，更新的丢失唤醒会被一直饿死。
func TestBotScheduleScanCoversAllUnfinishedRuns(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedscanall")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	const want = 120
	for i := 0; i < want; i++ {
		seedScheduleRun(t, env, bot.ID, task.ID)
	}
	got := env.bots.listUnfinishedScheduleRuns()
	if len(got) != want {
		t.Fatalf("应扫到全部 %d 条未完成 run，实得 %d", want, len(got))
	}
	seen := map[string]bool{}
	for i := range got {
		if seen[got[i].ID] {
			t.Fatalf("分页重复返回了 run %s", got[i].ID)
		}
		seen[got[i].ID] = true
	}
}

// 任务在崩溃前被删掉时，恢复路径的失败事件也不能丢（与 finishScheduleFailed 对称）。
func TestBotScheduleRecoveryEventAfterTaskDeleted(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedrecovdel")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	if err := env.db.Model(&model.BotRun{}).Where("id = ?", run.ID).
		Update("trigger_context_json", marshalJSONString(map[string]interface{}{
			"task_id": task.ID, "task_name": task.Name,
		})).Error; err != nil {
		t.Fatalf("set trigger context: %v", err)
	}
	if err := env.db.Exec(`DELETE FROM bot_schedule_tasks WHERE id = ?`, task.ID).Error; err != nil {
		t.Fatalf("delete task: %v", err)
	}
	reloaded := mustFindRun(t, env, run.ID)

	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	fresh := mustFindRun(t, env, run.ID)
	if fresh.Status != model.BotRunStatusFailed {
		t.Fatalf("中断的 schedule run 应置 failed: %+v", fresh)
	}
	n, content := countScheduleEvents(t, env, bot.ID)
	if n != 1 {
		t.Fatalf("任务被删除后恢复事件仍应追加，实得 %d", n)
	}
	if content.TaskName != task.Name || content.ErrorCode != "server_restarted" {
		t.Fatalf("恢复事件应带派发时的任务名与 server_restarted: %+v", content)
	}
	_ = reloaded
}

// 同刻到期任务超过一批时单轮要排空：否则剩下的会被拖过宽限、误记成 skipped。
func TestBotScheduleDrainsDueBacklog(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "schedbacklog")
	bot := env.createBot(t, userID)
	const n = 60 // 超过 botScheduleDispatchBatch（50）
	due := time.Now().UTC().Add(-3 * time.Second)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("批量任务%02d", i)
		seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
			tk.Name = name
			tk.NeedsReport = false
			tk.NextRunAt = &due
		})
	}
	env.bots.scheduleTick(time.Now().UTC())

	var runs []model.BotRun
	if err := env.db.Where("bot_id = ? AND session_kind = ?", bot.ID, model.BotSessionKindSchedule).Find(&runs).Error; err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != n {
		t.Fatalf("单轮应排空 %d 条到期任务，实得 %d 条 run", n, len(runs))
	}
	for i := range runs {
		if runs[i].Status == model.BotRunStatusSkipped {
			t.Fatalf("宽限内到期的任务不应记 skipped: %s", runs[i].ID)
		}
	}
	// 收尾：等这些 run 结束，避免后台 goroutine 干扰后续用例。
	waitForBot(t, "批量运行收尾", 30*time.Second, func() bool {
		var unfinished int64
		env.db.Model(&model.BotRun{}).
			Where("bot_id = ? AND finished_at IS NULL", bot.ID).Count(&unfinished)
		return unfinished == 0
	})
}

// 任务数上限与列表读取上限一致：建得比看得见还多，用户就管不到看不见的任务。
func TestBotScheduleCreateRespectsTaskCap(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedcap")
	bot := env.createBot(t, userID)
	now := time.Now().UTC()
	next := now.Add(time.Hour)
	for i := 0; i < botScheduleTasksMaxPerBot; i++ {
		task := &model.BotScheduleTask{
			ID: uuid.NewString(), BotID: bot.ID,
			Name: fmt.Sprintf("满额任务%03d", i), CronExpression: "0 9 * * *",
			Instruction: "跑一下", Enabled: true, NextRunAt: &next,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := env.bots.schedTasks.Create(task); err != nil {
			t.Fatalf("seed task %d: %v", i, err)
		}
	}
	_, err := env.bots.createScheduleTask(bot.ID, "超额任务", "0 9 * * *", "Asia/Shanghai", "跑一下", "", false)
	if err == nil {
		t.Fatalf("超过任务数上限时应拒绝创建")
	}
	appErr, ok := err.(*errs.AppError)
	if !ok || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("应为参数错误（提示上限），实得 %T %v", err, err)
	}
}

// 任务在运行期间被删除时，已完成且需要汇报的运行仍要追加事件（用派发时冻结的
// 需要汇报与任务名兜底）。
func TestBotScheduleCompletedEventAfterTaskDeleted(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedcompdel")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = true
	})
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	ctx := marshalJSONString(map[string]interface{}{
		"task_id": task.ID, "task_name": task.Name, "needs_report": true,
	})
	if err := env.db.Model(&model.BotRun{}).Where("id = ?", run.ID).
		Update("trigger_context_json", ctx).Error; err != nil {
		t.Fatalf("set trigger context: %v", err)
	}
	run.TriggerContextJSON = ctx
	if err := env.db.Exec(`DELETE FROM bot_schedule_tasks WHERE id = ?`, task.ID).Error; err != nil {
		t.Fatalf("delete task: %v", err)
	}

	if !env.bots.tryFinishScheduleRun(botRow, run) {
		t.Fatalf("没有未结算调用时应能完成")
	}
	n, content := countScheduleEvents(t, env, bot.ID)
	if n != 1 {
		t.Fatalf("任务被删除后已完成且需汇报的运行仍应追加事件，实得 %d", n)
	}
	if content.TaskName != task.Name || content.Status != string(model.BotRunStatusCompleted) {
		t.Fatalf("事件应带派发时的任务名与完成状态: %+v", content)
	}
}

// 上限检查必须在事务里与插入一起做：定时任务允许并行运行，并发的两次创建
// 分开读会都通过检查、建出超额任务（而列表读取有上限，超额那条用户管不到）。
func TestBotScheduleCreateCapIsAtomic(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedcaprace")
	bot := env.createBot(t, userID)
	now := time.Now().UTC()
	next := now.Add(time.Hour)
	for i := 0; i < botScheduleTasksMaxPerBot-1; i++ {
		task := &model.BotScheduleTask{
			ID: uuid.NewString(), BotID: bot.ID,
			Name: fmt.Sprintf("已存在%03d", i), CronExpression: "0 9 * * *",
			Instruction: "跑一下", Enabled: true, NextRunAt: &next,
			CreatedAt: now, UpdatedAt: now,
		}
		if err := env.bots.schedTasks.Create(task); err != nil {
			t.Fatalf("seed task %d: %v", i, err)
		}
	}

	const workers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := env.bots.createScheduleTask(bot.ID, fmt.Sprintf("并发任务%d", i),
				"0 9 * * *", "Asia/Shanghai", "跑一下", "", false); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("只剩 1 个名额时并发创建应只成功 1 次，实得 %d", success)
	}
	total, err := env.bots.schedTasks.CountByBot(bot.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != botScheduleTasksMaxPerBot {
		t.Fatalf("任务数不得超过上限 %d，实得 %d", botScheduleTasksMaxPerBot, total)
	}
}

// L1：删除任务时请求停止它未完成的运行——任务一删，这些 run 既查不到也停不了，
// 却会一直挡住机器人删除。
func TestBotScheduleDeleteStopsUnfinishedRuns(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "scheddelstop")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	if err := env.bots.deleteScheduleTask(bot.ID, task.ID); err != nil {
		t.Fatalf("delete task: %v", err)
	}
	fresh := mustFindRun(t, env, run.ID)
	if fresh.StopRequestedAt == nil {
		t.Fatalf("删除任务应请求停止未完成的 run: %+v", fresh)
	}
	// 扫描补唤醒后，孤儿 run 在安全点收尾为 stopped。
	env.bots.scanOnce()
	done := waitForScheduleRunFinished(t, env, run.ID)
	if done.Status != model.BotRunStatusStopped {
		t.Fatalf("被停止的孤儿 run 应收尾为 stopped，实得 %s（%v）", done.Status, done.ErrorMessage)
	}
}

// L2：同一计划时间已有运行记录时仍要推进 next_run_at，否则任务永远停在到期集合里
// 每 tick 空转。
func TestBotScheduleDuplicateTriggerAdvancesPlan(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "[[SILENT]]"})
	userID := createBotTestUser(t, env.db, "scheddup")
	bot := env.createBot(t, userID)
	planned := time.Now().UTC().Add(-3 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NextRunAt = &planned
	})
	taskID := task.ID
	existing := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource:  model.BotTriggerSchedule,
		TriggerKey:     botScheduleTriggerKey(taskID, planned),
		Status:         model.BotRunStatusCompleted,
		SessionKind:    model.BotSessionKindSchedule,
		ScheduleTaskID: &taskID, ScheduledFor: &planned,
		ConfigSnapshotJSON: "{}", CreatedAt: planned,
	}
	if err := env.bots.runs.CreateTx(env.db, existing); err != nil {
		t.Fatalf("seed existing run: %v", err)
	}

	env.bots.dispatchDueScheduleTask(task, time.Now().UTC())
	fresh, err := env.bots.schedTasks.FindByID(task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if fresh.NextRunAt == nil || fresh.NextRunAt.Equal(planned) {
		t.Fatalf("撞重复计划时间后仍应推进 next_run_at，实得 %+v", fresh.NextRunAt)
	}
	var count int64
	env.db.Model(&model.BotRun{}).Where("schedule_task_id = ?", task.ID).Count(&count)
	if count != 1 {
		t.Fatalf("不应重复建 run，实得 %d", count)
	}
}

// L3：对「已启用且这次计划还在宽限内等派发」的任务再点启用，不该吃掉这次触发。
func TestBotScheduleEnableKeepsPendingDueRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedenable")
	bot := env.createBot(t, userID)
	due := time.Now().UTC().Add(-5 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NextRunAt = &due
	})
	got, err := env.bots.setScheduleTaskEnabled(bot.ID, task.ID, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if got.NextRunAt == nil || !got.NextRunAt.Equal(due) {
		t.Fatalf("已启用任务再启用应保留待派发的计划，实得 %+v", got.NextRunAt)
	}
	// 停用后再启用才重算。
	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, task.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	back, err := env.bots.setScheduleTaskEnabled(bot.ID, task.ID, true)
	if err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if back.NextRunAt == nil || !back.NextRunAt.After(time.Now().UTC()) {
		t.Fatalf("重新启用应重算未来计划，实得 %+v", back.NextRunAt)
	}
}

// L4：cron 解析失败时任务应被真正暂停（enabled=false），不留「开着但不再跑」。
func TestBotSchedulePauseDisablesTask(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedpause")
	bot := env.createBot(t, userID)
	due := time.Now().UTC().Add(-3 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.CronExpression = "not a cron"
		tk.NextRunAt = &due
	})
	env.bots.scheduleTick(time.Now().UTC())
	fresh, err := env.bots.schedTasks.FindByID(task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if fresh.Enabled || fresh.NextRunAt != nil {
		t.Fatalf("解析失败应暂停（enabled=false 且无下次计划），实得 %+v", fresh)
	}
}

// R1/R2：需要汇报的任务被跳过时要留一条事件，并写明原定计划时间。
func TestBotScheduleSkippedReportsWhenNeeded(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedskiprep")
	bot := env.createBot(t, userID)
	past := time.Now().UTC().Add(-10 * time.Minute)
	reportTask := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Name = "要汇报的"
		tk.NeedsReport = true
		tk.NextRunAt = &past
	})
	quietTask := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Name = "不汇报的"
		tk.NeedsReport = false
		tk.NextRunAt = &past
	})

	env.bots.scheduleTick(time.Now().UTC())
	for _, id := range []string{reportTask.ID, quietTask.ID} {
		var run model.BotRun
		if err := env.db.Where("schedule_task_id = ?", id).First(&run).Error; err != nil {
			t.Fatalf("run for %s: %v", id, err)
		}
		if run.Status != model.BotRunStatusSkipped {
			t.Fatalf("超宽限应记 skipped: %+v", run)
		}
		if !strings.Contains(run.Summary, "原定") {
			t.Fatalf("skipped 摘要应写明原定计划时间: %q", run.Summary)
		}
	}
	n, content := countScheduleEvents(t, env, bot.ID)
	if n != 1 {
		t.Fatalf("只有需要汇报的任务跳过才留事件，实得 %d", n)
	}
	if content.Status != string(model.BotRunStatusSkipped) || content.TaskName != "要汇报的" {
		t.Fatalf("跳过事件内容不符: %+v", content)
	}
}

// 跳过事件也必须 publish：否则订阅方要等下一次别的事件才看得到这一行。
func TestBotScheduleSkippedEventPublishesToSubscribers(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedskippub")
	bot := env.createBot(t, userID)
	past := time.Now().UTC().Add(-10 * time.Minute)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = true
		tk.NextRunAt = &past
	})

	ch, cancel := env.bots.SubscribeEvents(bot.ID)
	defer cancel()
	env.bots.dispatchDueScheduleTask(task, time.Now().UTC())

	select {
	case <-ch:
		// 收到有新事件的信号即可（订阅方按 seq 回读）。
	case <-time.After(2 * time.Second):
		t.Fatalf("跳过事件追加后应发布一次事件通知")
	}
}

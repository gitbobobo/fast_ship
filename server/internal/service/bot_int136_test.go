package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
)

// 定时任务写确认（INT-136）服务层测试：fs_schedule create/敏感字段 update/
// 非 dry_run run 在主线走 pending_confirmation→approve→worker→后台结果；
// dry_run 与只改 name/cron/timezone 的 update 保持同步；schedule 会话禁止
// 管理任务；冻结在 v3–v7 的会话同样要确认；always_allow 按 create/update
// 拒绝、run 按任务放行。

// botToolsetV8FrozenHash 是 INT-136 发布的 v8 wire 字节基线（实现本版时的
// 实测值：b7bf3803…，完整 wire 数组的 SHA-256）。v8 只改 fs_schedule 的工具
// 与动作描述；之后任何改动都会让存量会话升级后的 tools_hash 漂移，必须先
// 递增版本再改字节。
const botToolsetV8FrozenHash = "f6390f8c2b43c8c03fb525a39a17b1ce88962fa8b5d1252259cf63c41ade4ac7"

// countScheduleTasks 统计机器人名下的任务数。
func countScheduleTasks(t *testing.T, env *botTestEnv, botID string) int64 {
	t.Helper()
	var n int64
	if res := env.db.Model(&model.BotScheduleTask{}).Where("bot_id = ?", botID).Count(&n); res.Error != nil {
		t.Fatalf("count tasks: %v", res.Error)
	}
	return n
}

// countScheduleRuns 统计任务名下的运行记录数。
func countScheduleRuns(t *testing.T, env *botTestEnv, taskID string) int64 {
	t.Helper()
	var n int64
	if res := env.db.Model(&model.BotRun{}).Where("schedule_task_id = ?", taskID).Count(&n); res.Error != nil {
		t.Fatalf("count runs: %v", res.Error)
	}
	return n
}

// waitPendingCall 等到出现 pending_confirmation 的调用并返回其 ID。
func waitPendingCall(t *testing.T, env *botTestEnv, botID string) string {
	t.Helper()
	var callID string
	waitForBot(t, "待确认调用", 15*time.Second, func() bool {
		calls, _ := env.bots.ListCalls(ownerOf(t, env, botID), botID, []string{"pending_confirmation"}, 5)
		if len(calls) > 0 {
			callID = calls[0].ID
			return true
		}
		return false
	})
	return callID
}

// ownerOf 反查机器人归属人（测试里已保证机器人存在）。
func ownerOf(t *testing.T, env *botTestEnv, botID string) string {
	t.Helper()
	bot, err := env.bots.bots.Get(botID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	return bot.UserID
}

// ---------- 1. 主线 create：受理→确认→后台结果（端到端） ----------

func TestBotInt136CreateConfirmedAsyncEndToEnd(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-sch-create", Name: "fs_schedule",
			Args: `{"action":"create","name":"INT136晨报","cron":"0 9 * * *","timezone":"Asia/Shanghai","instruction":"生成晨报","needs_report":true}`,
		}}},
		fakeModelTurn{Text: "已受理，等确认"},
		fakeModelTurn{Text: "任务已创建"},
	)
	userID := createBotTestUser(t, env.db, "int136create")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
		Text: "每天九点给我晨报", ClientMessageID: "i1",
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	callID := waitPendingCall(t, env, bot.ID)

	// 受理回执：pending_confirmation + 异步 + 服务端预览；此时库里没有任务。
	call, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("load call: %v", err)
	}
	if !call.RequiresConfirmation || call.ExecutionMode != string(model.BotExecutionAsync) {
		t.Fatalf("create 应为异步待确认: %+v", call)
	}
	if call.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("受理状态应为 pending_confirmation: %s", call.Status)
	}
	var execCtx botCallExecContext
	if err := json.Unmarshal([]byte(call.ExecutionContextJSON), &execCtx); err != nil {
		t.Fatalf("decode exec ctx: %v", err)
	}
	if execCtx.ScopeKind != botScopeScheduleTaskCollection || execCtx.ScopeID != bot.ID {
		t.Fatalf("范围应为任务集合+bot: %+v", execCtx)
	}
	if execCtx.Schedule == nil || !strings.Contains(execCtx.Schedule.NextRunAt, "09:00") {
		t.Fatalf("确认卡预览应含任务时区的下次运行: %+v", execCtx.Schedule)
	}
	if execCtx.Schedule.EffectiveModel != "fake-model" {
		t.Fatalf("预览的生效模型应为机器人当前模型: %+v", execCtx.Schedule)
	}
	if _, err := time.Parse(time.RFC3339, execCtx.Schedule.NextRunAtUTC); err != nil {
		t.Fatalf("next_run_at_utc 应为 RFC3339: %q", execCtx.Schedule.NextRunAtUTC)
	}
	if n := countScheduleTasks(t, env, bot.ID); n != 0 {
		t.Fatalf("确认前不应创建任务，实得 %d", n)
	}

	// 批准：queued → worker 执行 → 任务创建 → 后台结果。
	decided, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "approve"})
	if err != nil || decided.Status != "queued" {
		t.Fatalf("approve: %v %+v", err, decided)
	}
	waitForBot(t, "任务创建", 20*time.Second, func() bool {
		return countScheduleTasks(t, env, bot.ID) == 1
	})
	tasks, err := env.bots.ListScheduleTasks(userID, bot.ID)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("list tasks: %v %d", err, len(tasks))
	}
	if tasks[0].Name != "INT136晨报" || !tasks[0].NeedsReport || !tasks[0].Enabled {
		t.Fatalf("任务字段不符: %+v", tasks[0])
	}

	// 后台结果消息 + 下一轮模型请求能看到它。
	waitForBot(t, "后台结果", 20*time.Second, func() bool {
		msgs, _, _ := env.bots.ListMessages(userID, bot.ID, 0, 0, 50)
		for _, m := range msgs {
			if m.Kind == "background_result" && m.CallID != nil && *m.CallID == callID {
				return true
			}
		}
		return false
	})
	req1 := env.fake.nextRequest(t, 5*time.Second)
	req2 := env.fake.nextRequest(t, 10*time.Second)
	req3 := env.fake.nextRequest(t, 10*time.Second)
	_ = req1
	_ = req2
	// 按 call_id 定位这条调用的后台终态消息：任务名/工具名在历史与工具定义里
	// 本来就有，不能证明后台结果真的进了上下文。
	body := string(req3.Body)
	if !strings.Contains(body, callID) || !strings.Contains(body, "background_result") {
		t.Fatalf("确认后的模型请求应带该调用的后台结果: %.600s", body)
	}
	waitForBot(t, "主线收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })
}

// 拒绝：任务不存在，调用落 rejected 终态。
func TestBotInt136CreateRejectKeepsNoTask(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-sch-reject", Name: "fs_schedule",
			Args: `{"action":"create","name":"不该建","cron":"0 9 * * *","instruction":"x"}`,
		}}},
		fakeModelTurn{Text: "好的，不建了"},
	)
	userID := createBotTestUser(t, env.db, "int136reject")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "建个任务", ClientMessageID: "r1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	callID := waitPendingCall(t, env, bot.ID)
	decided, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "reject"})
	if err != nil || decided.Status != "rejected" {
		t.Fatalf("reject: %v %+v", err, decided)
	}
	waitForBot(t, "主线收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })
	if n := countScheduleTasks(t, env, bot.ID); n != 0 {
		t.Fatalf("拒绝后不应有任务，实得 %d", n)
	}
}

// ---------- 2. run：非 dry_run 要确认；dry_run 同步预览 ----------

func TestBotInt136RunNeedsConfirmationDryRunStaysSync(t *testing.T) {
	// dry_run 阶段先独立跑完；批准非 dry_run 后会同时出现主线 tool_result
	// run 与真实 schedule run，两者抢 subsequent 轮次，所以并发窗口只安排
	// 纯文本轮（[[SILENT]] 与已跑完），避免工具调用轮落进 schedule 会话。
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-run-dry", Name: "fs_schedule",
			Args: `{"action":"run","task_id":"TASKID","dry_run":true}`,
		}}},
		fakeModelTurn{Text: "这是预览"},
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-run-1", Name: "fs_schedule",
			Args: `{"action":"run","task_id":"TASKID"}`,
		}}},
		fakeModelTurn{Text: "已受理，等确认"},
		fakeModelTurn{Text: "[[SILENT]]"},
		fakeModelTurn{Text: "已跑完"},
	)
	userID := createBotTestUser(t, env.db, "int136run")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false
		tk.ID = "TASKID"
	})

	// dry_run=true：同步返回预览，无确认卡、无运行记录。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "先预览", ClientMessageID: "run0"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "主线收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })
	var dryCalls []model.BotToolCall
	env.db.Where("bot_id = ? AND action = ?", bot.ID, "run").Order("created_at ASC").Find(&dryCalls)
	if len(dryCalls) != 1 {
		t.Fatalf("应有 1 次 dry_run 调用，实得 %d", len(dryCalls))
	}
	dry := dryCalls[0]
	if dry.Status != model.BotCallStatusSucceeded {
		t.Fatalf("dry_run 应同步成功: %s %+v", dry.Status, dry.ErrorMessage)
	}
	if dry.RequiresConfirmation || dry.ExecutionMode != string(model.BotExecutionSync) {
		t.Fatalf("dry_run 不应要求确认: %+v", dry)
	}
	if dry.ResultPreviewJSON == nil || !strings.Contains(*dry.ResultPreviewJSON, `"dry_run":true`) || !strings.Contains(*dry.ResultPreviewJSON, "next_run_at") {
		t.Fatalf("dry_run 结果应为预览: %+v", dry.ResultPreviewJSON)
	}
	if n := countScheduleRuns(t, env, task.ID); n != 0 {
		t.Fatalf("dry_run 不应建运行记录，实得 %d", n)
	}

	// 非 dry_run：受理待确认，且没有任何运行记录。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "马上跑一次", ClientMessageID: "run1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	callID := waitPendingCall(t, env, bot.ID)
	if n := countScheduleRuns(t, env, task.ID); n != 0 {
		t.Fatalf("确认前不应有运行记录，实得 %d", n)
	}
	if _, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// 批准后 worker 执行：产生 trigger=manual 的 schedule run 并跑完。
	waitForBot(t, "手动运行出现", 20*time.Second, func() bool {
		return countScheduleRuns(t, env, task.ID) == 1
	})
	var run model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).First(&run)
	if run.TriggerSource != model.BotTriggerManual {
		t.Fatalf("应为 manual 触发: %+v", run)
	}
	if done := waitForScheduleRunFinished(t, env, run.ID); done.Status != model.BotRunStatusCompleted {
		t.Fatalf("手动 run 应完成: %s %v", done.Status, done.ErrorMessage)
	}
	waitForBot(t, "主线收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })
}

// ---------- 3. update：装饰字段同步；敏感字段要确认 ----------

func TestBotInt136UpdateCosmeticSyncSensitiveConfirmed(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-upd-cosmetic", Name: "fs_schedule",
			Args: `{"action":"update","task_id":"TASKID","name":"改名后","cron":"30 8 * * *"}`,
		}}},
		fakeModelTurn{Text: "改好了"},
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-upd-sensitive", Name: "fs_schedule",
			Args: `{"action":"update","task_id":"TASKID","instruction":"INT136新指令"}`,
		}}},
		fakeModelTurn{Text: "已受理，等确认"},
	)
	userID := createBotTestUser(t, env.db, "int136upd")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.ID = "TASKID"
		tk.CronExpression = "0 9 * * *"
	})

	// 只改 name/cron：同步执行、不弹卡。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "改成八点半", ClientMessageID: "u1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "主线收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })
	var calls []model.BotToolCall
	env.db.Where("bot_id = ? AND action = ?", bot.ID, "update").Order("created_at ASC").Find(&calls)
	if len(calls) != 1 {
		t.Fatalf("应有 1 次 update 调用，实得 %d", len(calls))
	}
	if calls[0].Status != model.BotCallStatusSucceeded || calls[0].RequiresConfirmation {
		t.Fatalf("装饰字段 update 应同步成功: %+v", calls[0])
	}
	fresh, err := env.bots.schedTasks.FindByID(task.ID)
	if err != nil || fresh.Name != "改名后" || fresh.CronExpression != "30 8 * * *" {
		t.Fatalf("任务应已更新: %+v %v", fresh, err)
	}

	// 改 instruction：受理待确认。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "换指令", ClientMessageID: "u2"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	callID := waitPendingCall(t, env, bot.ID)
	call, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("load call: %v", err)
	}
	if !call.RequiresConfirmation || call.ExecutionMode != string(model.BotExecutionAsync) {
		t.Fatalf("改 instruction 的 update 应为异步待确认: %+v", call)
	}
	if call.Decision != nil {
		t.Fatalf("未决定前不应有 decision: %+v", call.Decision)
	}
	// 批准后指令更新。
	if _, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	waitForBot(t, "指令更新", 20*time.Second, func() bool {
		tk, err := env.bots.schedTasks.FindByID(task.ID)
		return err == nil && tk.Instruction == "INT136新指令"
	})
	waitForBot(t, "主线收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })
}

// 受理判定是唯一事实源：needs_report/model 任一（含显式 null）都算敏感；
// 只改 name/cron/timezone 保持同步。规则与工具集版本无关。
func TestBotInt136UpdateConfirmationMatrix(t *testing.T) {
	ts := botToolsetVersionOf(8)
	for _, fields := range []string{
		`"instruction":"新指令"`, `"instruction":null`,
		`"needs_report":true`, `"needs_report":false`, `"needs_report":null`,
		`"model":"m2"`, `"model":""`, `"model":null`,
		`"name":"n","model":"m2"`,
	} {
		prepared, err := botPrepareToolCall(ts, "fs_schedule", `{"action":"update","task_id":"t1",`+fields+`}`)
		if err != nil {
			t.Fatalf("update %s 应被受理: %v", fields, err)
		}
		if !prepared.RequiresConfirmation || prepared.Mode != model.BotExecutionAsync {
			t.Fatalf("update %s 应判为需确认异步: %+v", fields, prepared)
		}
	}
	for _, fields := range []string{
		`"name":"n"`, `"cron":"0 10 * * *"`, `"timezone":"UTC"`,
		`"name":"n","cron":"30 8 * * *"`, `"timezone":"Asia/Shanghai","name":"x"`,
	} {
		prepared, err := botPrepareToolCall(ts, "fs_schedule", `{"action":"update","task_id":"t1",`+fields+`}`)
		if err != nil {
			t.Fatalf("update %s 应被受理: %v", fields, err)
		}
		if prepared.RequiresConfirmation || prepared.Mode != model.BotExecutionSync {
			t.Fatalf("update %s 应保持同步: %+v", fields, prepared)
		}
	}
}

// 冻结在 v3–v7 的会话同样要确认：判定在受理端，不随工具集版本变化。
func TestBotInt136FrozenVersionsAlsoConfirm(t *testing.T) {
	for _, v := range []int64{3, 4, 5, 6, 7} {
		prepared, err := botPrepareToolCall(botToolsetVersionOf(v), "fs_schedule",
			`{"action":"create","name":"n","cron":"0 9 * * *","instruction":"i"}`)
		if err != nil {
			t.Fatalf("v%d create 应被受理: %v", v, err)
		}
		if !prepared.RequiresConfirmation || prepared.Mode != model.BotExecutionAsync {
			t.Fatalf("v%d create 应判为需确认异步: %+v", v, prepared)
		}
	}
	// v7 才有 run：非 dry_run 要确认，dry_run 免确认。
	v7 := botToolsetVersionOf(7)
	prepared, err := botPrepareToolCall(v7, "fs_schedule", `{"action":"run","task_id":"t1"}`)
	if err != nil {
		t.Fatalf("v7 run 应被受理: %v", err)
	}
	if !prepared.RequiresConfirmation || prepared.Mode != model.BotExecutionAsync {
		t.Fatalf("v7 run 应判为需确认异步: %+v", prepared)
	}
	prepared, err = botPrepareToolCall(v7, "fs_schedule", `{"action":"run","task_id":"t1","dry_run":true}`)
	if err != nil {
		t.Fatalf("v7 dry_run 应被受理: %v", err)
	}
	if prepared.RequiresConfirmation || prepared.Mode != model.BotExecutionSync {
		t.Fatalf("v7 dry_run 应保持同步: %+v", prepared)
	}
}

// ---------- 4. schedule 会话禁止管理任务 ----------

func TestBotInt136ScheduleSessionCannotManageTasks(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{
			{ID: "call-denied-create", Name: "fs_schedule",
				Args: `{"action":"create","name":"越权任务","cron":"0 9 * * *","instruction":"x"}`},
			{ID: "call-denied-update", Name: "fs_schedule",
				Args: `{"action":"update","task_id":"TASKID","name":"越权改名"}`},
			{ID: "call-denied-run", Name: "fs_schedule",
				Args: `{"action":"run","task_id":"TASKID"}`},
			// enable/disable/delete 也是「管理任务」：enable 能静默重新武装
			// 用户已停用的任务，delete 能抹掉全部任务，同样不能在这个默认
			// 放行写操作的会话里做。
			{ID: "call-denied-enable", Name: "fs_schedule",
				Args: `{"action":"enable","task_id":"TASKID"}`},
			{ID: "call-denied-disable", Name: "fs_schedule",
				Args: `{"action":"disable","task_id":"TASKID"}`},
			{ID: "call-denied-delete", Name: "fs_schedule",
				Args: `{"action":"delete","task_id":"TASKID"}`},
		}},
		fakeModelTurn{Text: "[[SILENT]]"},
	)
	userID := createBotTestUser(t, env.db, "int136denied")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.ID = "TASKID"
		tk.NeedsReport = false
	})

	// 触发一次 schedule run：模型在同一轮里尝试管理任务。
	due := time.Now().UTC().Add(-3 * time.Second)
	env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).Update("next_run_at", due)
	env.bots.scheduleTick(time.Now().UTC())
	var run model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).Order("created_at ASC").First(&run)
	done := waitForScheduleRunFinished(t, env, run.ID)
	if done.Status != model.BotRunStatusCompleted {
		t.Fatalf("schedule run 应完成: %s %v", done.Status, done.ErrorMessage)
	}

	var calls []model.BotToolCall
	env.db.Where("bot_id = ? AND tool_name = ?", bot.ID, "fs_schedule").Order("ordinal ASC").Find(&calls)
	if len(calls) != 6 {
		t.Fatalf("应有 6 次被拒调用，实得 %d", len(calls))
	}
	for _, call := range calls {
		if call.Status != model.BotCallStatusFailed {
			t.Fatalf("%s/%s 应失败: %s", call.ToolName, call.Action, call.Status)
		}
		if call.ErrorCode == nil || *call.ErrorCode != "e40001" {
			t.Fatalf("错误码应为 e40001: %+v", call.ErrorCode)
		}
		if call.ErrorMessage == nil || !strings.Contains(*call.ErrorMessage, "定时任务运行中不能") {
			t.Fatalf("错误应说明原因与出路: %+v", call.ErrorMessage)
		}
	}

	// 数据库无变化：没有新任务、任务字段未动、没有第二次运行。
	if n := countScheduleTasks(t, env, bot.ID); n != 1 {
		t.Fatalf("不应创建新任务，实得 %d", n)
	}
	fresh, err := env.bots.schedTasks.FindByID(task.ID)
	if err != nil || fresh.Name != task.Name || fresh.Instruction != task.Instruction {
		t.Fatalf("任务不应被修改: %+v %v", fresh, err)
	}
	if n := countScheduleRuns(t, env, task.ID); n != 1 {
		t.Fatalf("run 被拒后不应有第二次运行，实得 %d", n)
	}
}

// ---------- 5. 受理期参数校验：必然失败的调用不弹卡 ----------

func TestBotInt136AcceptanceValidatesCronAndTimezone(t *testing.T) {
	ts := botToolsetVersionOf(8)
	_, err := botPrepareToolCall(ts, "fs_schedule",
		`{"action":"create","name":"n","cron":"不是cron","instruction":"i"}`)
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("坏 cron 应在受理期拒绝: %v", err)
	}
	if !strings.Contains(appErr.Message, "cron") {
		t.Fatalf("错误应说明 cron 无效: %q", appErr.Message)
	}
	_, err = botPrepareToolCall(ts, "fs_schedule",
		`{"action":"update","task_id":"t1","instruction":"i","timezone":"Mars/Olympus"}`)
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code || !strings.Contains(appErr.Message, "时区") {
		t.Fatalf("敏感 update 的坏时区应在受理期拒绝: %v", err)
	}
	// 超长指令同样在受理期拒绝：否则弹卡、获批、worker 执行时才失败。
	longInstruction := strings.Repeat("长", botScheduleInstructionMaxLen+1)
	_, err = botPrepareToolCall(ts, "fs_schedule",
		`{"action":"create","name":"n","cron":"0 9 * * *","instruction":"`+longInstruction+`"}`)
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code || !strings.Contains(appErr.Message, "指令过长") {
		t.Fatalf("超长指令应在受理期拒绝: %v", err)
	}
	_, err = botPrepareToolCall(ts, "fs_schedule",
		`{"action":"update","task_id":"t1","instruction":"`+longInstruction+`"}`)
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("敏感 update 的超长指令应在受理期拒绝: %v", err)
	}
	// 免确认的 update 不做受理期校验（执行层会校验），这里只证明受理不受影响。
	if _, err := botPrepareToolCall(ts, "fs_schedule",
		`{"action":"update","task_id":"t1","timezone":"Mars/Olympus"}`); err != nil {
		t.Fatalf("装饰字段 update 仍应受理: %v", err)
	}
}

// ---------- 6. always_allow：create/update 拒绝，run 按任务放行 ----------

func TestBotInt136AlwaysAllowRejectedForCreateAndUpdate(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-aa-create", Name: "fs_schedule",
			Args: `{"action":"create","name":"AA任务","cron":"0 9 * * *","instruction":"x"}`,
		}}},
		fakeModelTurn{Text: "已受理"},
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-aa-update", Name: "fs_schedule",
			Args: `{"action":"update","task_id":"TASKID","instruction":"AA新指令"}`,
		}}},
		fakeModelTurn{Text: "已受理"},
	)
	userID := createBotTestUser(t, env.db, "int136aa")
	bot := env.createBot(t, userID)
	seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.ID = "TASKID" })

	// create 的 always_allow：40001，不建授权，卡片保持待确认。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "建任务", ClientMessageID: "aa1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	createCallID := waitPendingCall(t, env, bot.ID)
	var appErr *errs.AppError
	_, err := env.bots.DecideCall(userID, bot.ID, createCallID, &CallDecisionRequest{Decision: "always_allow"})
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("create 的 always_allow 应 40001: %v", err)
	}
	if !strings.Contains(appErr.Message, "始终允许") {
		t.Fatalf("错误应说明不支持始终允许: %q", appErr.Message)
	}
	if authzs, _ := env.bots.ListAuthorizations(userID, bot.ID, true); len(authzs) != 0 {
		t.Fatalf("不应产生授权: %d", len(authzs))
	}
	// 卡片未被消费：仍可正常批准或拒绝。
	if _, err := env.bots.DecideCall(userID, bot.ID, createCallID, &CallDecisionRequest{Decision: "reject"}); err != nil {
		t.Fatalf("拒绝应仍可用: %v", err)
	}
	waitForBot(t, "主线收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })

	// 敏感 update 的 always_allow：同样 40001。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "改指令", ClientMessageID: "aa2"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	updateCallID := waitPendingCall(t, env, bot.ID)
	if _, err := env.bots.DecideCall(userID, bot.ID, updateCallID, &CallDecisionRequest{Decision: "always_allow"}); !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("update 的 always_allow 应 40001: %v", err)
	}
	if authzs, _ := env.bots.ListAuthorizations(userID, bot.ID, true); len(authzs) != 0 {
		t.Fatalf("不应产生授权: %d", len(authzs))
	}
}

func TestBotInt136RunAlwaysAllowScopesToTask(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-aa-run1", Name: "fs_schedule", Args: `{"action":"run","task_id":"TASKID"}`,
		}}},
		fakeModelTurn{Text: "已受理"},
		// 批准后主线 tool_result run 与真实 schedule run 并发抢轮次，
		// 并发窗口只安排纯文本轮；第二次 run 的工具轮放在其后。
		fakeModelTurn{Text: "[[SILENT]]"},
		fakeModelTurn{Text: "第一次已触发"},
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-aa-run2", Name: "fs_schedule", Args: `{"action":"run","task_id":"TASKID"}`,
		}}},
		fakeModelTurn{Text: "已受理"},
	)
	userID := createBotTestUser(t, env.db, "int136aarun")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.ID = "TASKID"
		tk.NeedsReport = false
	})

	// 第一次 run：确认卡 → always_allow，授权范围=任务 ID。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "跑一次", ClientMessageID: "r1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	callID := waitPendingCall(t, env, bot.ID)
	if _, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "always_allow"}); err != nil {
		t.Fatalf("run 的 always_allow 应可用: %v", err)
	}
	authzs, err := env.bots.ListAuthorizations(userID, bot.ID, true)
	if err != nil || len(authzs) != 1 {
		t.Fatalf("应有 1 条活跃授权: %v %d", err, len(authzs))
	}
	if authzs[0].Action != "run" || authzs[0].ScopeKind != botScopeScheduleTask || authzs[0].ScopeID != task.ID {
		t.Fatalf("授权范围应为单任务: %+v", authzs[0])
	}
	// 等第一次手动 run 跑完，再验证第二次免确认。
	var run1 model.BotRun
	waitForBot(t, "第一次手动运行", 20*time.Second, func() bool {
		if countScheduleRuns(t, env, task.ID) < 1 {
			return false
		}
		env.db.Where("schedule_task_id = ?", task.ID).Order("created_at ASC").First(&run1)
		return true
	})
	waitForScheduleRunFinished(t, env, run1.ID)
	waitForBot(t, "主线收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })

	// 第二次同任务 run：直接排队执行，不再等确认。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "再跑一次", ClientMessageID: "r2"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "第二次 run 完成", 20*time.Second, func() bool {
		return countScheduleRuns(t, env, task.ID) == 2
	})
	waitForBot(t, "主线收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })
	var runs []model.BotToolCall
	env.db.Where("bot_id = ? AND action = ?", bot.ID, "run").Order("created_at ASC").Find(&runs)
	if len(runs) != 2 {
		t.Fatalf("应有 2 次 run 调用，实得 %d", len(runs))
	}
	for _, call := range runs {
		if call.Status == model.BotCallStatusPendingConfirmation {
			t.Fatalf("同任务第二次 run 不应再等确认: %+v", call)
		}
	}
	if runs[1].AuthorizationID == nil || runs[1].Status != model.BotCallStatusSucceeded {
		t.Fatalf("第二次 run 应带授权直接成功: %+v", runs[1])
	}
}

// run 只接受 task_id/dry_run（INT-136 审查）：执行器按 task_id 读库里的任务
// 配置，模型多传的 name/instruction/needs_report 不生效——若放行，确认卡可以
// 展示一份无害指令、实际跑的却是任务里存的另一份。
func TestBotInt136RunRejectsConfigFields(t *testing.T) {
	for _, version := range []int64{6, 7, 8} {
		ts := botToolsetVersionOf(version)
		for _, extra := range []string{
			`,"name":"只读巡检"`,
			`,"instruction":"只统计数量，不要修改任何内容"`,
			`,"needs_report":true`,
			`,"cron":"0 9 * * *"`,
			`,"timezone":"UTC"`,
			`,"model":"m2"`,
		} {
			args := `{"action":"run","task_id":"t1"` + extra + `}`
			if _, err := botPrepareToolCall(ts, "fs_schedule", args); err == nil {
				t.Fatalf("v%d run 带配置字段应被拒绝: %s", version, args)
			}
		}
		if _, err := botPrepareToolCall(ts, "fs_schedule", `{"action":"run","task_id":"t1","dry_run":true}`); err != nil {
			t.Fatalf("v%d run 的 dry_run 预览应仍被受理: %v", version, err)
		}
	}
}

// ---------- 7. 工具集 v8：描述说明确认语义，字节钉死 ----------

func TestBotInt136ToolsetV8(t *testing.T) {
	if botToolsetVersion != 8 {
		t.Fatalf("当前工具集版本应为 8，实得 %d", botToolsetVersion)
	}
	v7 := botToolsetVersionOf(7)
	v8 := botToolsetVersionOf(8)
	if v7.hash != botToolsetV7FrozenHash {
		t.Fatalf("v7 tools_hash 偏离冻结基线: got %s want %s", v7.hash, botToolsetV7FrozenHash)
	}
	if v8.hash != botToolsetV8FrozenHash {
		t.Fatalf("v8 tools_hash 偏离基线: got %s want %s", v8.hash, botToolsetV8FrozenHash)
	}
	if v7.hash == v8.hash {
		t.Fatalf("v8 应与 v7 字节不同")
	}
	sched := v8.byName["fs_schedule"]
	if sched == nil || len(sched.Actions) != 10 {
		t.Fatalf("v8 fs_schedule 应仍是 10 个动作: %+v", sched)
	}
	// 动作清单顺序不变。
	wantOrder := []string{"list", "create", "update", "delete", "runs", "run", "enable", "disable", "state_get", "state_set"}
	for i, want := range wantOrder {
		if sched.Actions[i].Name != want {
			t.Fatalf("v8 动作顺序应不变: %d %s != %s", i, sched.Actions[i].Name, want)
		}
	}
	for _, kw := range []string{
		"需要用户确认", "平台后台结果", "授权该任务以后每次运行默认放行写操作",
		"定时任务运行中不能", "dry_run=true 预览除外",
	} {
		if !strings.Contains(sched.Description, kw) {
			t.Fatalf("v8 fs_schedule 描述应说明确认语义（缺 %q）: %s", kw, sched.Description)
		}
	}
	// create/update/run 的动作描述点明确认；其余动作保持原描述。
	for _, name := range []string{"create", "update", "run"} {
		found := false
		for _, a := range sched.Actions {
			if a.Name == name {
				found = true
				if !strings.Contains(a.Description, "确认") {
					t.Fatalf("v8 %s 动作描述应点明需要确认: %s", name, a.Description)
				}
			}
		}
		if !found {
			t.Fatalf("v8 缺动作 %s", name)
		}
	}
	// v7 字节未被 v8 污染。
	if strings.Contains(v7.byName["fs_schedule"].Description, "pending_confirmation") {
		t.Fatalf("v7 fs_schedule 描述不得被 v8 污染: %s", v7.byName["fs_schedule"].Description)
	}
	if sched.ArgsType.Name() != "botScheduleTaskArgs" {
		t.Fatalf("v8 fs_schedule 应沿用 botScheduleTaskArgs: %s", sched.ArgsType.Name())
	}
}

// enable/delete 同样要确认（INT-136 review）：enable 是重新武装一个「运行内
// 默认放行写操作」的任务、delete 不可逆；disable 是收紧权限、可逆，保持同步。
func TestBotInt136EnableDeleteNeedConfirmation(t *testing.T) {
	ts := botToolsetVersionOf(8)
	for _, action := range []string{"enable", "delete"} {
		prepared, err := botPrepareToolCall(ts, "fs_schedule", `{"action":"`+action+`","task_id":"t1"}`)
		if err != nil {
			t.Fatalf("%s 应被受理: %v", action, err)
		}
		if !prepared.RequiresConfirmation || prepared.Mode != model.BotExecutionAsync {
			t.Fatalf("%s 应判为需确认异步: %+v", action, prepared)
		}
		// 配置字段对 enable/delete 不生效（执行器只按 task_id 读库），放行就会
		// 让确认卡展示一份无害配置、实际按另一份执行——受理端直接拒绝。
		for _, extra := range []string{
			`,"instruction":"只统计数量"`, `,"name":"只读巡检"`,
			`,"cron":"0 9 * * *"`, `,"needs_report":true`, `,"model":"m2"`,
		} {
			if _, err := botPrepareToolCall(ts, "fs_schedule", `{"action":"`+action+`","task_id":"t1"`+extra+`}`); err == nil {
				t.Fatalf("%s 带配置字段应被拒绝: %s", action, extra)
			}
		}
	}
	prepared, err := botPrepareToolCall(ts, "fs_schedule", `{"action":"disable","task_id":"t1"}`)
	if err != nil {
		t.Fatalf("disable 应被受理: %v", err)
	}
	if prepared.RequiresConfirmation || prepared.Mode != model.BotExecutionSync {
		t.Fatalf("disable 应保持同步免确认: %+v", prepared)
	}

	// always_allow：create/update/enable/delete 一律拒绝，run 仍允许。
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-aa-enable", Name: "fs_schedule",
			Args: `{"action":"enable","task_id":"TASKID"}`,
		}}},
		fakeModelTurn{Text: "已受理"},
	)
	userID := createBotTestUser(t, env.db, "int136aaen")
	bot := env.createBot(t, userID)
	seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.ID = "TASKID" })
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "启用任务", ClientMessageID: "aa-en"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	callID := waitPendingCall(t, env, bot.ID)
	var appErr *errs.AppError
	if _, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "always_allow"}); !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("enable 的 always_allow 应 40001: %v", err)
	}
	if authzs, _ := env.bots.ListAuthorizations(userID, bot.ID, true); len(authzs) != 0 {
		t.Fatalf("不应产生授权: %d", len(authzs))
	}
}

// 确认卡批准的是受理时看到的那一版任务（INT-136 review，TOCTOU）：run/enable/
// delete 执行时任务已被改过（例如指令被换成写操作密集的内容）就拒绝，让用户
// 重新看一眼再决定，而不是静默按新配置执行已批准的旧卡。
func TestBotInt136RunRejectedWhenTaskChangedAfterAccept(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-toctou", Name: "fs_schedule", Args: `{"action":"run","task_id":"TASKID"}`,
		}}},
		fakeModelTurn{Text: "已受理"},
		fakeModelTurn{Text: "好的"},
	)
	userID := createBotTestUser(t, env.db, "int136toctou")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.ID = "TASKID"
		tk.Instruction = "只读巡检"
		tk.NeedsReport = false
	})
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "跑一次", ClientMessageID: "t1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	callID := waitPendingCall(t, env, bot.ID)

	// 确认期间任务指令被改（含写操作），执行时必须拒绝。
	changed := "顺便把高优先级问题全部关闭"
	if _, err := env.bots.updateScheduleTask(bot.ID, task.ID, "", "", "", changed, nil, nil, ""); err != nil {
		t.Fatalf("update task: %v", err)
	}
	if _, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatalf("approve: %v", err)
	}
	waitForBot(t, "调用失败", 20*time.Second, func() bool {
		call, err := env.bots.calls.FindByID(callID)
		return err == nil && call.Status == model.BotCallStatusFailed
	})
	call, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("load call: %v", err)
	}
	if call.ErrorMessage == nil || !strings.Contains(*call.ErrorMessage, "确认期间被修改") {
		t.Fatalf("错误应说明任务已改: %+v", call.ErrorMessage)
	}
	if n := countScheduleRuns(t, env, task.ID); n != 0 {
		t.Fatalf("任务已改时不应启动运行，实得 %d", n)
	}
}

// delete 的修订冲突要原样透传业务错误（40001「任务在确认期间被修改」），不能
// 被事务收尾折成内部错误——否则用户看不到「请重新查看后再操作」。
func TestBotInt136DeleteRevisionConflictPropagates(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int136delrev")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	stale := task.UpdatedAt.UTC().Format(time.RFC3339Nano)
	// 改动任务，制造「受理之后被改过」。
	if _, err := env.bots.updateScheduleTask(bot.ID, task.ID, "", "", "", "改过的指令", nil, nil, ""); err != nil {
		t.Fatalf("update task: %v", err)
	}
	err := env.bots.deleteScheduleTask(bot.ID, task.ID, stale)
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code ||
		!strings.Contains(appErr.Message, "确认期间被修改") {
		t.Fatalf("删除的修订冲突应透传 40001: %v", err)
	}
	// 未删除。
	if _, err := env.bots.schedTasks.FindByID(task.ID); err != nil {
		t.Fatalf("冲突时不应删除任务: %v", err)
	}
}

// 受理时目标任务读不到（不存在）要精确报 40429，并让调用失败、不建确认卡。
func TestBotInt136AcceptMissingTaskRejected(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-missing-run", Name: "fs_schedule", Args: `{"action":"run","task_id":"missing"}`,
		}}},
		fakeModelTurn{Text: "好"},
	)
	userID := createBotTestUser(t, env.db, "int136missing")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "跑一下", ClientMessageID: "m1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "调用失败", 15*time.Second, func() bool {
		calls, _ := env.bots.ListCalls(userID, bot.ID, nil, 5)
		return len(calls) == 1 && calls[0].Status == "failed"
	})
	calls, _ := env.bots.ListCalls(userID, bot.ID, nil, 5)
	if calls[0].ErrorCode == nil || *calls[0].ErrorCode != "e40429" {
		t.Fatalf("应回 40429（任务不存在）: %+v", calls[0])
	}
}

// 范围解析失败（目标不存在）时，被拒调用仍要带上已被校验的元数据：否则界面和
// 审计只能看到一条 tool_name/action 为空的泛化失败，查不出是哪个动作触发的 40429。
func TestBotInt136ScopeFailureKeepsCallMetadata(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-scope-fail", Name: "fs_schedule", Args: `{"action":"delete","task_id":"missing"}`,
		}}},
		fakeModelTurn{Text: "好"},
	)
	userID := createBotTestUser(t, env.db, "int136meta")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "删了它", ClientMessageID: "s1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "调用失败", 15*time.Second, func() bool {
		calls, _ := env.bots.ListCalls(userID, bot.ID, nil, 5)
		return len(calls) == 1 && calls[0].Status == "failed"
	})
	calls, _ := env.bots.ListCalls(userID, bot.ID, nil, 5)
	call := calls[0]
	if call.ToolName != "fs_schedule" || call.Action != "delete" {
		t.Fatalf("被拒调用应保留工具与动作: %+v", call)
	}
	if call.ExecutionMode != "async" || !call.RequiresConfirmation {
		t.Fatalf("被拒调用应保留执行模式与确认标记: %+v", call)
	}
	if call.ErrorCode == nil || *call.ErrorCode != "e40429" {
		t.Fatalf("应回 40429: %+v", call.ErrorCode)
	}
}

// 分页参数对「只按 task_id 的动作」同样不生效：放行会让确认卡的原始参数里出现
// 从未生效的字段（INT-136 不变量审计）。
func TestBotInt136PagingParamsRejectedWhereIgnored(t *testing.T) {
	ts := botToolsetVersionOf(8)
	for _, action := range []string{"run", "enable", "delete", "update", "disable", "state_get", "state_set"} {
		args := `{"action":"` + action + `","task_id":"t1","limit":10}`
		if _, err := botPrepareToolCall(ts, "fs_schedule", args); err == nil {
			t.Fatalf("%s 带 limit 应被拒绝: %s", action, args)
		}
		args = `{"action":"` + action + `","task_id":"t1","offset":1}`
		if _, err := botPrepareToolCall(ts, "fs_schedule", args); err == nil {
			t.Fatalf("%s 带 offset 应被拒绝: %s", action, args)
		}
	}
	// list/runs 的分页仍然有效。
	if _, err := botPrepareToolCall(ts, "fs_schedule", `{"action":"list","limit":10,"offset":5}`); err != nil {
		t.Fatalf("list 应接受分页: %v", err)
	}
	if _, err := botPrepareToolCall(ts, "fs_schedule", `{"action":"runs","task_id":"t1","limit":10}`); err != nil {
		t.Fatalf("runs 应接受分页: %v", err)
	}
}

// dry_run 预览是只读：新输入到达时它应当执行完，而不是被标成 not_executed。
func TestBotInt136DryRunIsReadOnly(t *testing.T) {
	dry := &model.BotToolCall{
		ToolName: "fs_schedule", Action: "run",
		ExecutionMode: string(model.BotExecutionSync), ArgumentsJSON: `{"action":"run","task_id":"t1","dry_run":true}`,
	}
	if !botCallIsReadOnly(dry) {
		t.Fatalf("dry_run 预览应视为只读")
	}
	real := &model.BotToolCall{
		ToolName: "fs_schedule", Action: "run",
		ExecutionMode: string(model.BotExecutionSync), ArgumentsJSON: `{"action":"run","task_id":"t1"}`,
	}
	if botCallIsReadOnly(real) {
		t.Fatalf("真实 run 不应视为只读")
	}
}

// 任务小状态写入不是配置变更：state_set 不能推动 updated_at，否则一次运行里的
// 状态写入会让在途的确认卡被误判成「任务在确认期间被修改」。
func TestBotInt136StateSetKeepsTaskRevision(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int136state")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	before, err := env.bots.schedTasks.FindByID(task.ID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, Action: "state_set",
		ArgumentsJSON: `{"action":"state_set","state":{"seen":["a"]}}`,
	}
	// state_set 只允许在 schedule run 内：把调用挂到这次 run 的请求上。
	env.db.Model(&model.BotToolCall{}).Where("id = ?", call.ID)
	reqRow := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1, Status: model.BotModelRequestRunning,
		InputThroughSeq: 0, PrefixHash: "x", ToolsHash: "x", RequestHash: "x",
		TokenEstimator: "bytes/3.5", StartedAt: time.Now().UTC(),
	}
	if err := env.bots.requests.CreateTx(env.db, reqRow); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	call.RequestID = reqRow.ID
	if err := env.bots.calls.CreateTx(env.db, call); err != nil {
		t.Fatalf("seed call: %v", err)
	}
	if _, err := env.bots.execScheduleTool(call); err != nil {
		t.Fatalf("state_set: %v", err)
	}
	after, err := env.bots.schedTasks.FindByID(task.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("state_set 不应推动 updated_at: %v -> %v", before.UpdatedAt, after.UpdatedAt)
	}
	if after.StateJSON == "" || after.StateUpdatedAt == nil {
		t.Fatalf("状态应写入: %+v", after)
	}
}

// 运行时上下文里的围栏标签要字面可见：默认的 json.Marshal 会把 < 转义成
// \u003c，模型就看不到数据边界了。
func TestBotInt136RuntimeContextKeepsLiteralFence(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int136rt")
	bot := env.createBot(t, userID)
	nonce := botNonce(t, env, bot.ID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Name = "任务名"
		tk.NeedsReport = false
	})
	// 造一条已结束运行，让 last_run_summary 带上围栏。
	now := time.Now().UTC()
	finished := now
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID, TriggerSource: model.BotTriggerSchedule,
		TriggerKey: "schedule:" + task.ID + ":" + uuid.NewString(),
		Status:     model.BotRunStatusCompleted, SessionKind: model.BotSessionKindSchedule,
		ScheduleTaskID: &task.ID, Summary: "上次摘要", ConfigSnapshotJSON: "{}",
		TriggerContextJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &finished,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	current := seedScheduleRun(t, env, bot.ID, task.ID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	sess, err := env.bots.newScheduleSession(botRow, current)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	built, err := env.bots.buildScheduleRunContext(botRow, current, sess)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	raw, _ := json.Marshal(built.Request.Messages)
	var elems []struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &elems); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 模型解析完协议 JSON 后看到的是这条消息正文：围栏标签必须字面出现
	// （json.Marshal 默认会把 < 转义成 \u003c，模型就看不到数据边界了）。
	text := elems[len(elems)-1].Content
	// 围栏在 JSON 字符串值里，引号是转义的；尖括号必须是字面量。
	if !strings.Contains(text, `<untrusted_data nonce=\"`+nonce+`\" source=\"schedule_run\">`) {
		t.Fatalf("运行时上下文里的摘要围栏标签应字面可见: %s", text)
	}
	if strings.Contains(text, `\u003c`) {
		t.Fatalf("消息正文里不应有 JSON 转义的尖括号: %s", text)
	}
}

// 生效模型是「跟随机器人」而不是任务固定覆盖时，预览必须标出来：卡片据此
// 说明继承语义（机器人换模型后下次运行会跟着变），而不是承诺一个具体模型。
func TestBotInt136PreviewMarksInheritedModel(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int136model")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	inherited := env.bots.schedulePreviewFromTask(botRow, schedulePreviewInput{
		Name: "n", Cron: "0 9 * * *", Instruction: "i",
	}, nil)
	if inherited == nil || !inherited.ModelInherited || inherited.EffectiveModel != botRow.Model {
		t.Fatalf("未指定模型时应标为继承: %+v", inherited)
	}
	fixedModel := "task-model"
	fixed := env.bots.schedulePreviewFromTask(botRow, schedulePreviewInput{
		Name: "n", Cron: "0 9 * * *", Instruction: "i", ModelPtr: &fixedModel,
	}, nil)
	if fixed == nil || fixed.ModelInherited || fixed.EffectiveModel != fixedModel {
		t.Fatalf("指定模型时不应标为继承: %+v", fixed)
	}
}

// 没有生效配置的确认等于没有确认：预览算不出来（存量配置不合法）时受理端直接
// 拒绝，不建一张没有配置、修订比对也会被跳过的确认卡。
func TestBotInt136AcceptRejectsTaskWithoutPreview(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int136nopreview")
	bot := env.createBot(t, userID)
	// 存量脏数据：指令为空（会让人设校验失败，算不出生效配置）。
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Instruction = "   "
	})
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	runPrep, err := botPrepareToolCall(botToolsetVersionOf(8), "fs_schedule",
		`{"action":"run","task_id":"`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("prepare run: %v", err)
	}
	var appErr *errs.AppError
	if err := env.bots.resolveCallScope(botRow, runPrep, `{"action":"run","task_id":"`+task.ID+`"}`, &botCallExecContext{}); !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("没有生效配置时应拒绝 run: %v", err)
	}
	// delete 是清理坏任务的唯一出口：允许受理，但要保留目标与修订水位。
	delPrep, err := botPrepareToolCall(botToolsetVersionOf(8), "fs_schedule",
		`{"action":"delete","task_id":"`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("prepare delete: %v", err)
	}
	if err := env.bots.resolveCallScope(botRow, delPrep, `{"action":"delete","task_id":"`+task.ID+`"}`, &botCallExecContext{}); err != nil {
		t.Fatalf("delete 应仍可受理: %v", err)
	}
	if delPrep.SchedulePreview == nil || delPrep.SchedulePreview.TaskUpdatedAt == "" {
		t.Fatalf("delete 预览应保留目标与修订水位: %+v", delPrep.SchedulePreview)
	}
}

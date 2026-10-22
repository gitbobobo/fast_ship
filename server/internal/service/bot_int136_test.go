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
	"gorm.io/gorm"
)

// 定时任务写确认（INT-136）服务层测试：fs_schedule create/敏感字段 update/
// 非 dry_run run 在主线走 pending_confirmation→approve→worker→后台结果；
// dry_run 与只改 name/cron/timezone 的 update 保持同步；schedule 会话禁止
// 管理任务；冻结在 v3–v7 的会话同样要确认；always_allow 按 create/update
// 拒绝、run 按任务放行。

// botToolsetV10FrozenHash 是 INT-136 发布的 v10 wire 字节基线（INT-138 占用
// v8、INT-148 占用 v9，本版重编号为 v10）。v10 只改 fs_schedule 的工具与动作描述；之后任何
// 改动都会让存量会话升级后的 tools_hash 漂移，必须先递增版本再改字节。
const botToolsetV10FrozenHash = "fa03273b79fb8e8ac50df8387ff0e9a0bd0dd8a64a265648a94797f6cba5bf6a"

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
		// 受理端同步拒绝从未进异步队列：账本模式必须记 sync，否则排查
		// 时会被误导成异步执行失败。
		if call.ExecutionMode != string(model.BotExecutionSync) {
			t.Fatalf("%s/%s 同步拒绝应记 sync，实得 %q", call.ToolName, call.Action, call.ExecutionMode)
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

// ---------- 7. 工具集 v10：描述说明确认语义，字节钉死 ----------

func TestBotInt136ToolsetV10(t *testing.T) {
	if botToolsetVersion != 10 {
		t.Fatalf("当前工具集版本应为 10，实得 %d", botToolsetVersion)
	}
	v7 := botToolsetVersionOf(7)
	v8 := botToolsetVersionOf(8)
	v9 := botToolsetVersionOf(9)
	v10 := botToolsetVersionOf(10)
	if v7.hash != botToolsetV7FrozenHash {
		t.Fatalf("v7 tools_hash 偏离冻结基线: got %s want %s", v7.hash, botToolsetV7FrozenHash)
	}
	if v8.hash != botToolsetV8FrozenHash {
		t.Fatalf("v8 tools_hash 偏离冻结基线: got %s want %s", v8.hash, botToolsetV8FrozenHash)
	}
	if v9.hash != botToolsetV9FrozenHash {
		t.Fatalf("v9 tools_hash 偏离 INT-148 冻结基线: got %s want %s", v9.hash, botToolsetV9FrozenHash)
	}
	if v10.hash != botToolsetV10FrozenHash {
		t.Fatalf("v10 tools_hash 偏离基线: got %s want %s", v10.hash, botToolsetV10FrozenHash)
	}
	if v8.hash == v9.hash || v9.hash == v10.hash {
		t.Fatalf("v8/v9/v10 相邻版本应字节不同")
	}
	for i, tool := range v9.tools {
		if tool.Name == "fs_schedule" {
			continue
		}
		before, _ := json.Marshal(v9.schemas[i])
		after, _ := json.Marshal(v10.schemas[i])
		if string(before) != string(after) {
			t.Fatalf("v10 不得改写 v9 的 %s wire 字节", tool.Name)
		}
	}
	sched := v10.byName["fs_schedule"]
	if sched == nil || len(sched.Actions) != 10 {
		t.Fatalf("v10 fs_schedule 应仍是 10 个动作: %+v", sched)
	}
	// 动作清单顺序不变。
	wantOrder := []string{"list", "create", "update", "delete", "runs", "run", "enable", "disable", "state_get", "state_set"}
	for i, want := range wantOrder {
		if sched.Actions[i].Name != want {
			t.Fatalf("v10 动作顺序应不变: %d %s != %s", i, sched.Actions[i].Name, want)
		}
	}
	for _, kw := range []string{
		"需要用户确认", "平台后台结果", "授权该任务以后每次运行默认放行写操作",
		"定时任务运行中不能", "dry_run=true 预览除外",
		"省略即用户时区", "instruction 写「意图」", "定时触发也不重叠", "本轮记 skipped",
		"线程结算独立使用 fs_thread settle/unsettle",
	} {
		if !strings.Contains(sched.Description, kw) {
			t.Fatalf("v10 fs_schedule 描述应保留确认、时区、指令与不重叠语义（缺 %q）: %s", kw, sched.Description)
		}
	}
	// create/update/run/enable/delete 的动作描述点明确认。
	for _, name := range []string{"create", "update", "run", "enable", "delete"} {
		found := false
		for _, a := range sched.Actions {
			if a.Name == name {
				found = true
				if !strings.Contains(a.Description, "确认") {
					t.Fatalf("v10 %s 动作描述应点明需要确认: %s", name, a.Description)
				}
			}
		}
		if !found {
			t.Fatalf("v10 缺动作 %s", name)
		}
	}
	// v7/v8/v9 字节未被 v10 污染。
	for _, frozen := range []*botToolsetHolder{v7, v8, v9} {
		if strings.Contains(frozen.byName["fs_schedule"].Description, "pending_confirmation") {
			t.Fatalf("v%d fs_schedule 描述不得被 v10 污染: %s", frozen.version, frozen.byName["fs_schedule"].Description)
		}
	}
	if sched.ArgsType.Name() != "botScheduleTaskArgs" {
		t.Fatalf("v10 fs_schedule 应沿用 botScheduleTaskArgs: %s", sched.ArgsType.Name())
	}
}

func TestBotInt136ToolsetV10ChangeNote(t *testing.T) {
	note910 := botToolsetChangeNote(9, 10)
	for _, want := range []string{"需要用户确认", "instruction/needs_report/model", "enable", "delete"} {
		if !strings.Contains(note910, want) {
			t.Fatalf("9→10 应告知确认语义 %q: %s", want, note910)
		}
	}
	if strings.Contains(note910, "resume_queue") || strings.Contains(note910, "用户时区") {
		t.Fatalf("9→10 不应重告旧版本变化: %s", note910)
	}
	note810 := botToolsetChangeNote(8, 10)
	for _, want := range []string{"需要用户确认", "settle/unsettle", "resume_queue", "pull_requests", "不重叠"} {
		if !strings.Contains(note810, want) {
			t.Fatalf("8→10 应同时告知 INT-136 与 INT-148 %q: %s", want, note810)
		}
	}
	if strings.Contains(note810, "用户时区") {
		t.Fatalf("8→10 不应重告 v8 变化: %s", note810)
	}
	note710 := botToolsetChangeNote(7, 10)
	if !strings.Contains(note710, "用户时区") || !strings.Contains(note710, "需要用户确认") ||
		!strings.Contains(note710, "pull_requests") {
		t.Fatalf("7→10 应级联时区、确认与线程变化: %s", note710)
	}
}

// enable/delete 同样要确认（INT-136 review）：enable 是重新武装一个「运行内
// 默认放行写操作」的任务、delete 不可逆；disable 是收紧权限、可逆，保持同步。
func TestBotInt136EnableDeleteNeedConfirmation(t *testing.T) {
	ts := botToolsetVersionOf(10)
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
	if _, err := env.bots.updateScheduleTask(bot.ID, task.ID, "", "", "", changed, nil, nil, "", false); err != nil {
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
	if _, err := env.bots.updateScheduleTask(bot.ID, task.ID, "", "", "", "改过的指令", nil, nil, "", false); err != nil {
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
	// disable 也拒绝配置字段（执行器只按 task_id 改开关）。
	for _, field := range []string{`"name":"n"`, `"cron":"0 9 * * *"`, `"instruction":"i"`, `"model":"m"`, `"needs_report":true`} {
		if _, err := botPrepareToolCall(ts, "fs_schedule", `{"action":"disable","task_id":"t1",`+field+`}`); err == nil {
			t.Fatalf("disable 带配置字段应被拒绝: %s", field)
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
	// enable 要按 cron 重算下次计划：配置不合法必须拒绝。
	enablePrep, err := botPrepareToolCall(botToolsetVersionOf(8), "fs_schedule",
		`{"action":"enable","task_id":"`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("prepare enable: %v", err)
	}
	var appErr *errs.AppError
	if err := env.bots.resolveCallScope(botRow, enablePrep, `{"action":"enable","task_id":"`+task.ID+`"}`, &botCallExecContext{}); !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("没有生效配置时应拒绝 enable: %v", err)
	}
	// run 不解析 cron、只冻结任务快照：同样允许受理（保留修订水位）。
	runPrep, err := botPrepareToolCall(botToolsetVersionOf(8), "fs_schedule",
		`{"action":"run","task_id":"`+task.ID+`"}`)
	if err != nil {
		t.Fatalf("prepare run: %v", err)
	}
	if err := env.bots.resolveCallScope(botRow, runPrep, `{"action":"run","task_id":"`+task.ID+`"}`, &botCallExecContext{}); err != nil {
		t.Fatalf("run 应仍可受理: %v", err)
	}
	if runPrep.SchedulePreview == nil || runPrep.SchedulePreview.TaskUpdatedAt == "" {
		t.Fatalf("run 预览应保留修订水位: %+v", runPrep.SchedulePreview)
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

// 免确认 update（只改 name/cron/timezone）受理期不读库内任务：存量非法配置
// 不该挡住这类同步修改（执行层会返回具体字段错误），没有确认卡的路径也不
// 记修订水位——否则受理到执行之间 updated_at 一动，同步 update 会以
// 「任务在确认期间被修改」误伤，而这条路径根本没有确认期间。
func TestBotInt136SyncUpdateSkipsPreviewAndWatermark(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int136syncupd")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	args := `{"action":"update","task_id":"` + task.ID + `","name":"改名后的任务"}`
	prep, err := botPrepareToolCall(botToolsetVersionOf(8), "fs_schedule", args)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prep.RequiresConfirmation {
		t.Fatalf("只改 name 应为免确认同步 update")
	}
	var execCtx botCallExecContext
	if err := env.bots.resolveCallScope(botRow, prep, args, &execCtx); err != nil {
		t.Fatalf("免确认 update 受理不应读库校验: %v", err)
	}
	if execCtx.Schedule != nil {
		t.Fatalf("免确认路径不应记预览/修订水位: %+v", execCtx.Schedule)
	}
	if execCtx.ScopeKind != botScopeScheduleTask || execCtx.ScopeID != task.ID {
		t.Fatalf("范围应仍指向目标任务: %+v", execCtx)
	}
	// 受理到执行之间任务被另一路改过：同步路径没有确认卡，执行不应报
	// 「确认期间被修改」。
	if err := env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).
		Update("updated_at", time.Now().Add(time.Hour).UTC()).Error; err != nil {
		t.Fatalf("bump updated_at: %v", err)
	}
	call := &model.BotToolCall{BotID: bot.ID, ArgumentsJSON: args,
		ExecutionContextJSON: marshalExecContext(execCtx)}
	if _, err := env.bots.execScheduleTool(call); err != nil {
		t.Fatalf("同步 update 不应被修订比对挡住: %v", err)
	}
	var reloaded model.BotScheduleTask
	if err := env.db.First(&reloaded, "id = ?", task.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Name != "改名后的任务" {
		t.Fatalf("同步 update 应已生效: %+v", reloaded)
	}
	// 存量非法配置的任务：受理同样放行，执行层返回具体字段错误而非
	// 「现有配置不合法」的笼统拒绝。
	bad := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Instruction = "   "
	})
	badArgs := `{"action":"update","task_id":"` + bad.ID + `","name":"还是改名"}`
	badPrep, err := botPrepareToolCall(botToolsetVersionOf(8), "fs_schedule", badArgs)
	if err != nil {
		t.Fatalf("prepare bad: %v", err)
	}
	var badCtx botCallExecContext
	if err := env.bots.resolveCallScope(botRow, badPrep, badArgs, &badCtx); err != nil {
		t.Fatalf("免确认 update 不应在受理期被存量配置挡住: %v", err)
	}
	badCall := &model.BotToolCall{BotID: bot.ID, ArgumentsJSON: badArgs,
		ExecutionContextJSON: marshalExecContext(badCtx)}
	_, execErr := env.bots.execScheduleTool(badCall)
	if execErr == nil || !strings.Contains(execErr.Error(), "指令") {
		t.Fatalf("执行层应返回具体字段错误（指令不合法）: %v", execErr)
	}
}

// run/delete 的兜底预览如实展示库里那一份配置：存量非法配置的任务上，
// 只剩 task_id 的匿名卡会让用户在看不到指令的情况下批准一次「默认放行
// 写操作」的执行——正是确认卡要防的盲批。
func TestBotInt136RawFallbackPreviewShowsStoredConfig(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int136rawprev")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.Name = "脏数据任务"
		tk.Instruction = "   "
		tk.CronExpression = "not-a-cron"
		// 预设 next_run_at 跳过 helper 的 cron 解析——这条任务模拟的正是
		// 存量非法配置。
		next := time.Now().UTC().Add(time.Hour)
		tk.NextRunAt = &next
	})
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	for _, action := range []string{"run", "delete"} {
		args := `{"action":"` + action + `","task_id":"` + task.ID + `"}`
		prep, err := botPrepareToolCall(botToolsetVersionOf(8), "fs_schedule", args)
		if err != nil {
			t.Fatalf("prepare %s: %v", action, err)
		}
		if err := env.bots.resolveCallScope(botRow, prep, args, &botCallExecContext{}); err != nil {
			t.Fatalf("%s 应仍可受理: %v", action, err)
		}
		preview := prep.SchedulePreview
		if preview == nil {
			t.Fatalf("%s 兜底预览不应为空", action)
		}
		if preview.Name != "脏数据任务" || preview.Instruction != "   " || preview.Cron != "not-a-cron" {
			t.Fatalf("%s 兜底预览应带库存原始配置: %+v", action, preview)
		}
		if preview.TaskUpdatedAt == "" || preview.TaskID != task.ID {
			t.Fatalf("%s 兜底预览应保留目标与修订水位: %+v", action, preview)
		}
		if !preview.ModelInherited || preview.EffectiveModel != botRow.Model {
			t.Fatalf("%s 兜底预览的模型口径应与正常预览一致: %+v", action, preview)
		}
	}
}

// confirmed 标记的口径（INT-136）：经确认卡的 create/敏感 update/enable
// 置位；免确认同步 update、disable 与 REST 启停不改写——它标识「这份配置
// 经确认卡复核过」，供存量复核提示区分上线前后的任务。
func TestBotInt136ConfirmedFlagLifecycle(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int136flag")
	bot := env.createBot(t, userID)

	// 经确认卡的 create：落库即已复核。
	createCall := &model.BotToolCall{BotID: bot.ID, RequiresConfirmation: true,
		ArgumentsJSON: `{"action":"create","name":"已确认任务","cron":"0 9 * * *","instruction":"跑一下"}`}
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(createCall.ArgumentsJSON), &raw); err != nil {
		t.Fatalf("decode args: %v", err)
	}
	preview, err := env.bots.schedulePreviewFromArgs(botRow, raw)
	if err != nil || preview == nil {
		t.Fatalf("preview: %+v %v", preview, err)
	}
	createCall.ExecutionContextJSON = marshalExecContext(botCallExecContext{Schedule: preview})
	if _, err := env.bots.execScheduleTool(createCall); err != nil {
		t.Fatalf("create exec: %v", err)
	}
	var created model.BotScheduleTask
	if err := env.db.First(&created, "bot_id = ? AND name = ?", bot.ID, "已确认任务").Error; err != nil {
		t.Fatalf("load created: %v", err)
	}
	if !created.Confirmed {
		t.Fatalf("经确认卡创建的任务应为已复核")
	}

	// 存量任务（默认未复核）：免确认同步 update 与 disable 都不改写标记。
	legacy := seedScheduleTask(t, env, bot.ID, nil)
	if legacy.Confirmed {
		t.Fatalf("种子任务默认应为未复核")
	}
	for _, args := range []string{
		`{"action":"update","task_id":"` + legacy.ID + `","name":"换个名"}`,
		`{"action":"disable","task_id":"` + legacy.ID + `"}`,
	} {
		call := &model.BotToolCall{BotID: bot.ID, ArgumentsJSON: args}
		if _, err := env.bots.execScheduleTool(call); err != nil {
			t.Fatalf("exec %s: %v", args, err)
		}
		var reloaded model.BotScheduleTask
		if err := env.db.First(&reloaded, "id = ?", legacy.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if reloaded.Confirmed {
			t.Fatalf("免确认动作不应改写 confirmed: %s", args)
		}
	}

	// 带确认卡的 update（哪怕只动 needs_report）：卡上展示了整份合并配置，
	// 批准即复核。
	confUpdate := &model.BotToolCall{BotID: bot.ID, RequiresConfirmation: true,
		ArgumentsJSON: `{"action":"update","task_id":"` + legacy.ID + `","needs_report":false}`}
	if _, err := env.bots.execScheduleTool(confUpdate); err != nil {
		t.Fatalf("confirmed update: %v", err)
	}
	var afterUpdate model.BotScheduleTask
	if err := env.db.First(&afterUpdate, "id = ?", legacy.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !afterUpdate.Confirmed {
		t.Fatalf("带确认卡的 update 应把任务标记为已复核")
	}

	// REST 启停不经确认卡：不改写标记；随后经确认卡的 enable 置位。
	legacy2 := seedScheduleTask(t, env, bot.ID, nil)
	if _, err := env.bots.SetScheduleTaskEnabled(userID, bot.ID, legacy2.ID, false); err != nil {
		t.Fatalf("rest disable: %v", err)
	}
	var restDone model.BotScheduleTask
	if err := env.db.First(&restDone, "id = ?", legacy2.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if restDone.Confirmed {
		t.Fatalf("REST 启停不应改写 confirmed")
	}
	confEnable := &model.BotToolCall{BotID: bot.ID, RequiresConfirmation: true,
		ArgumentsJSON: `{"action":"enable","task_id":"` + legacy2.ID + `"}`}
	if _, err := env.bots.execScheduleTool(confEnable); err != nil {
		t.Fatalf("confirmed enable: %v", err)
	}
	var afterEnable model.BotScheduleTask
	if err := env.db.First(&afterEnable, "id = ?", legacy2.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !afterEnable.Confirmed || !afterEnable.Enabled {
		t.Fatalf("经确认卡的 enable 应启用并标记已复核: %+v", afterEnable)
	}
}

// TestBotInt136UnconfirmedEnableBlocked 未复核任务（confirmed=false，确认机制
// 上线前的存量行）不得绕过确认卡直接启用：内部 helper 是收口，REST 面板与
// 任何不经确认的启用路径都被拒；经确认卡的 enable 自带 confirmed 放行；
// 停用与已复核任务的启停不受影响。
func TestBotInt136UnconfirmedEnableBlocked(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "legacygate")
	bot := env.createBot(t, userID)
	legacy := seedScheduleTask(t, env, bot.ID, nil)
	// Enabled 带 default:true，Create 会吞掉 false 零值，停用要显式 UPDATE。
	if err := env.db.Model(&model.BotScheduleTask{}).Where("id = ?", legacy.ID).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable seed: %v", err)
	}

	// 不经确认的启用被拒（REST 面板走的正是这条路径）。
	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, legacy.ID, true, "", false); err == nil {
		t.Fatalf("未复核任务不经确认的启用应被拒")
	} else if appErr, ok := err.(*errs.AppError); !ok || appErr.Code != 40934 {
		t.Fatalf("错误码应为 40934: %v", err)
	}
	if _, err := env.bots.SetScheduleTaskEnabled(userID, bot.ID, legacy.ID, true); err == nil {
		t.Fatalf("REST 包装路径同样应被拒")
	}
	reloaded := reloadScheduleTask(t, env, legacy.ID)
	if reloaded.Enabled || reloaded.Confirmed {
		t.Fatalf("被拒的启用不得改库: %+v", reloaded)
	}

	// 停用不受限。
	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, legacy.ID, false, "", false); err != nil {
		t.Fatalf("停用未复核任务不应受限: %v", err)
	}
	// 经确认卡的 enable 放行并置位 confirmed；此后普通启停不再受限。
	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, legacy.ID, true, "", true); err != nil {
		t.Fatalf("经确认卡的启用应放行: %v", err)
	}
	reloaded = reloadScheduleTask(t, env, legacy.ID)
	if !reloaded.Enabled || !reloaded.Confirmed {
		t.Fatalf("确认卡启用应启用并标记已复核: %+v", reloaded)
	}
	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, legacy.ID, false, "", false); err != nil {
		t.Fatalf("已复核任务停用: %v", err)
	}
	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, legacy.ID, true, "", false); err != nil {
		t.Fatalf("已复核任务普通启用不应受限: %v", err)
	}
}

// 大小写变体键不能绕过敏感字段判定：受理端 unknown-field 白名单
// （botArgKeys 精确比对结构体 JSON tag）先于敏感判定与执行端 unmarshal
// 运行，"Instruction"/"DRY_RUN" 等变体一律 40001——这道白名单是 INT-136
// 的承重墙，回归测试把它钉死。
func TestBotInt136CaseVariantKeysRejected(t *testing.T) {
	for _, v := range []int64{3, 5, 6, 7} {
		ts := botToolsetVersionOf(v)
		for _, args := range []string{
			`{"action":"update","task_id":"t","Instruction":"evil"}`,
			`{"action":"update","task_id":"t","NEEDS_REPORT":false}`,
			`{"action":"update","task_id":"t","Model":"gpt-x"}`,
			`{"action":"run","task_id":"t","DRY_RUN":true}`,
			`{"action":"create","name":"n","Cron":"0 9 * * *","instruction":"x"}`,
		} {
			if _, err := botPrepareToolCall(ts, "fs_schedule", args); err == nil {
				t.Fatalf("v%d 变体键应被拒: %s", v, args)
			}
		}
	}
}

// 建 run→会话加载窗口的修订比对只覆盖敏感配置：批准后改名/改 cron/停用
// 这类良性改动不再误杀已批准的 run；instruction/model/needs_report 变更
// 仍按 task_changed 拒绝。
func TestBotInt136RunRevisionSensitiveScope(t *testing.T) {
	task := &model.BotScheduleTask{
		Name: "原名", Instruction: "原指令", Model: "gpt-a", NeedsReport: true,
	}
	run := &model.BotRun{TriggerContextJSON: marshalJSONString(map[string]interface{}{
		"task_config_sha": botScheduleSensitiveSHA(task),
	})}

	// 良性改动：name/cron/timezone/enabled 变化不阻断。
	benign := *task
	benign.Name = "改名"
	benign.CronExpression = "0 8 * * *"
	benign.Enabled = false
	if err := botScheduleRunRevisionChanged(run, &benign); err != nil {
		t.Fatalf("良性改动不应被拒: %v", err)
	}
	// 敏感改动：instruction/model/needs_report 任一变化即拒绝。
	for _, mutate := range []func(*model.BotScheduleTask){
		func(tk *model.BotScheduleTask) { tk.Instruction = "新指令" },
		func(tk *model.BotScheduleTask) { tk.Model = "gpt-b" },
		func(tk *model.BotScheduleTask) { tk.NeedsReport = false },
	} {
		changed := *task
		mutate(&changed)
		err := botScheduleRunRevisionChanged(run, &changed)
		if err == nil || !strings.Contains(err.Error(), "被修改") {
			t.Fatalf("敏感改动应被拒: %+v %v", changed, err)
		}
	}
	// 存量 run（只有 task_updated_at）回退到整行比对，行为不变。
	legacy := &model.BotRun{TriggerContextJSON: marshalJSONString(map[string]interface{}{
		"task_updated_at": task.UpdatedAt.UTC().Format(time.RFC3339Nano),
	})}
	if err := botScheduleRunRevisionChanged(legacy, task); err != nil {
		t.Fatalf("水位一致不应拒: %v", err)
	}
	older := *task
	older.UpdatedAt = task.UpdatedAt.Add(time.Second)
	if err := botScheduleRunRevisionChanged(legacy, &older); err == nil {
		t.Fatalf("存量 run 的整行比对应仍生效")
	}
}

// 顶层重复键会让「确认卡看到的」与「执行的」分叉：map 解析取末次值
// （instruction:null → 未提供 → 卡片展示存量指令），结构体解析对 null
// 做 no-op 还原（instruction:"evil" 留在规范化参数里）——批准旧指令实际
// 执行新指令。受理端必须在任何解析之前拒绝重复键。
func TestBotInt136DuplicateKeysRejected(t *testing.T) {
	v7 := botToolsetVersionOf(7)
	for _, args := range []string{
		`{"action":"update","task_id":"t","instruction":"evil","instruction":null}`,
		`{"action":"update","task_id":"t","instruction":null,"instruction":"evil"}`,
		`{"action":"update","task_id":"t","needs_report":true,"needs_report":false}`,
		`{"action":"run","task_id":"t","dry_run":false,"dry_run":true}`,
	} {
		prepared, err := botPrepareToolCall(v7, "fs_schedule", args)
		if err == nil {
			t.Fatalf("重复键应被拒: %s -> %+v", args, prepared)
		}
		if !strings.Contains(err.Error(), "重复字段") {
			t.Fatalf("错误应指明重复字段: %s -> %v", args, err)
		}
	}
	// 良性载荷不受影响（含显式 null）。
	if _, err := botPrepareToolCall(v7, "fs_schedule",
		`{"action":"update","task_id":"t","model":null}`); err != nil {
		t.Fatalf("显式 null 单键应被受理: %v", err)
	}
}

// ---------- 7. 未识别动作 fail closed ----------

// 分类是对动作名的枚举：未来给工具集加动作却漏更新分类表时，默认必须落向
// 最严一侧——受理端要求确认（而不是免确认同步执行），schedule 会话直接拒绝。
// 这是 INT-136 的护栏对新动作的延伸：宁可多弹卡，不能静默放行持久化写。
func TestBotInt136UnknownActionFailsClosed(t *testing.T) {
	needs, err := botScheduleConfirmation("pause", map[string]json.RawMessage{})
	if err != nil || !needs {
		t.Fatalf("未识别动作应按需要确认处理: needs=%v err=%v", needs, err)
	}
	if err := botScheduleManageDenied(&botPreparedCall{
		ToolName: "fs_schedule", Action: "pause",
	}); err == nil {
		t.Fatal("未识别动作应在定时任务会话被拒绝")
	}
	// 已知动作口径不变：只读/降权动作免确认、schedule 会话可用（status 是
	// v2 冻结工具集里 list 的旧名，同样只读）。
	for _, a := range []string{"list", "runs", "state_get", "state_set", "disable", "status"} {
		needs, err := botScheduleConfirmation(a, map[string]json.RawMessage{})
		if err != nil || needs {
			t.Fatalf("%s 应保持免确认: needs=%v err=%v", a, needs, err)
		}
	}
	for _, a := range []string{"list", "runs", "state_get", "state_set", "status"} {
		if err := botScheduleManageDenied(&botPreparedCall{
			ToolName: "fs_schedule", Action: a,
		}); err != nil {
			t.Fatalf("%s 应在定时任务会话可用: %v", a, err)
		}
	}
	for _, a := range []string{"create", "update", "run", "enable", "delete"} {
		if err := botScheduleManageDenied(&botPreparedCall{
			ToolName: "fs_schedule", Action: a,
		}); err == nil {
			t.Fatalf("%s 应在定时任务会话被拒绝", a)
		}
	}
}

// create 省略时区时，确认卡快照必须与执行期 createScheduleTask 同一口径
// （机器人归属用户的有效时区）：否则用户批准的是「UTC 的 9 点」，落地却是
// 用户时区的 9 点，next_run 也对不上（INT-136 复审 P1）。
func TestBotInt136SchedulePreviewUsesOwnerTimezone(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "previewtz")
	bot := env.createBot(t, userID)
	setUserTimezone(t, env, userID, "Asia/Shanghai")
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}

	raw := map[string]json.RawMessage{
		"name":        json.RawMessage(`"晨报"`),
		"cron":        json.RawMessage(`"0 9 * * *"`),
		"instruction": json.RawMessage(`"检查新 issue 并汇报"`),
	}
	preview, err := env.bots.schedulePreviewFromArgs(botRow, raw)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview == nil {
		t.Fatalf("预览不应为空")
	}
	if preview.Timezone != "Asia/Shanghai" || preview.UserTimezone != "Asia/Shanghai" {
		t.Fatalf("省略时区时快照应冻结任务与用户时区，实得 %+v", preview)
	}
	if !strings.Contains(preview.NextRunAt, "09:00:00") {
		t.Fatalf("next_run_at 应为用户本地 09:00，实得 %q", preview.NextRunAt)
	}
	if !strings.Contains(preview.NextRunAtUTC, "T01:00:00Z") {
		t.Fatalf("next_run_at_utc 应为 UTC 01:00，实得 %q", preview.NextRunAtUTC)
	}

	// 显式时区仍以参数为准，不被用户时区覆盖。
	raw["timezone"] = json.RawMessage(`"UTC"`)
	if got, err := env.bots.schedulePreviewFromArgs(botRow, raw); err != nil || got == nil || got.Timezone != "UTC" || got.UserTimezone != "Asia/Shanghai" {
		t.Fatalf("显式任务时区与用户回执时区应分别保留，实得 %+v err=%v", got, err)
	}
}

// 确认型 create 在批准后按受理那一刻的快照时区执行：用户在受理到批准之间
// 改了自己的有效时区，不改变这次批准的落地结果（INT-136 复审 P2：卡上 9 点
// 与落地 9 点必须落在同一时区）。
func TestBotInt136ScheduleCreateUsesSnapshotTimezone(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "snaptz")
	bot := env.createBot(t, userID)
	// 批准时用户的有效时区已是上海，但快照是受理时解析的 UTC。
	setUserTimezone(t, env, userID, "Asia/Shanghai")

	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, ToolName: "fs_schedule", Action: "create",
		ArgumentsJSON: `{"action":"create","name":"晨报","cron":"0 9 * * *","instruction":"检查新 issue"}`,
		ExecutionContextJSON: marshalExecContext(botCallExecContext{
			Schedule: &botScheduleCallPreview{
				Name: "晨报", Cron: "0 9 * * *", Timezone: "UTC",
				Instruction: "检查新 issue", ModelInherited: true,
			},
		}),
	}
	out, err := env.bots.execScheduleCreate(call, &botScheduleTaskArgs{
		Action: "create", Name: "晨报", Cron: "0 9 * * *", Instruction: "检查新 issue",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created struct {
		ID       string `json:"id"`
		Timezone string `json:"timezone"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode: %v %s", err, out)
	}
	task, err := env.bots.schedTasks.FindByID(created.ID)
	if err != nil || task == nil {
		t.Fatalf("load task: %v", err)
	}
	if task.Timezone != "UTC" {
		t.Fatalf("应按快照时区落库（UTC），实得 %q", task.Timezone)
	}
	var receipt map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	for _, field := range []string{"user_timezone", "next_run_at_user"} {
		if _, ok := receipt[field]; ok {
			t.Fatalf("旧快照没有用户时区时应省略 %s: %s", field, out)
		}
	}
}

func scheduleUserReadProbe(t *testing.T, env *botTestEnv, readErr error) *int {
	t.Helper()
	reads := 0
	const name = "test:schedule_user_read_probe"
	if err := env.db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "users" {
			reads++
			if readErr != nil {
				tx.AddError(readErr)
			}
		}
	}); err != nil {
		t.Fatalf("register user read probe: %v", err)
	}
	t.Cleanup(func() {
		if err := env.db.Callback().Query().Remove(name); err != nil {
			t.Errorf("remove user read probe: %v", err)
		}
	})
	return &reads
}

func TestBotInt136ScheduleAcceptRejectsUserTimezoneReadFailure(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "accepttzerr")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	// 不存在的用户会按未声明回退；注入真实查询错误才覆盖失败关闭分支。
	readErr := errors.New("user timezone query failed")
	reads := scheduleUserReadProbe(t, env, readErr)
	for _, args := range []string{
		`{"action":"create","name":"n","cron":"0 9 * * *","instruction":"i"}`,
		`{"action":"create","name":"n","cron":"0 9 * * *","timezone":"UTC","instruction":"i"}`,
		`{"action":"update","task_id":"` + task.ID + `","instruction":"new"}`,
		`{"action":"enable","task_id":"` + task.ID + `"}`,
	} {
		prepared, err := botPrepareToolCall(botToolsetVersionOf(8), "fs_schedule", args)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		execCtx := botCallExecContext{}
		before := *reads
		err = env.bots.resolveCallScope(botRow, prepared, args, &execCtx)
		if !errors.Is(err, readErr) || *reads != before+1 {
			t.Fatalf("应只查询一次并返回时区读库错误: args=%s reads=%d err=%v", args, *reads-before, err)
		}
		if prepared.SchedulePreview != nil || execCtx.Schedule != nil {
			t.Fatalf("受理失败不应留下快照: prepared=%+v ctx=%+v", prepared.SchedulePreview, execCtx.Schedule)
		}
	}
	var calls int64
	if err := env.db.Model(&model.BotToolCall{}).Where("bot_id = ?", bot.ID).Count(&calls).Error; err != nil {
		t.Fatalf("count calls: %v", err)
	}
	if calls != 0 {
		t.Fatalf("受理失败不应建确认卡，实得 %d", calls)
	}
	for _, action := range []string{"run", "delete"} {
		args := `{"action":"` + action + `","task_id":"` + task.ID + `"}`
		prepared, err := botPrepareToolCall(botToolsetVersionOf(8), "fs_schedule", args)
		if err != nil {
			t.Fatalf("prepare %s: %v", action, err)
		}
		before := *reads
		execCtx := botCallExecContext{}
		if err := env.bots.resolveCallScope(botRow, prepared, args, &execCtx); err != nil {
			t.Fatalf("%s 不应依赖用户时区: %v", action, err)
		}
		if *reads != before || execCtx.Schedule == nil || execCtx.Schedule.UserTimezone != "" {
			t.Fatalf("%s 不应查询或冻结无用的用户时区: reads=%d snap=%+v", action, *reads-before, execCtx.Schedule)
		}
	}
}

func TestBotInt136ScheduleCreateReceiptUsesSnapshotUserTimezone(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "receipttz")
	bot := env.createBot(t, userID)
	report := true
	snap := &botScheduleCallPreview{
		Name: "approved", Cron: "0 9 * * *", Timezone: "America/New_York",
		UserTimezone: "Asia/Shanghai", Instruction: "approved instruction",
		NeedsReport: &report, EffectiveModel: "approved-model",
	}
	// 执行期即使用户查询报错也必须成功，而不只是「换时区后碰巧结果相同」。
	reads := scheduleUserReadProbe(t, env, errors.New("unexpected user read"))
	for _, currentTimezone := range []string{"UTC", "Asia/Tokyo", ""} {
		setUserTimezone(t, env, userID, currentTimezone)
		call := &model.BotToolCall{ID: uuid.NewString(), BotID: bot.ID, RequiresConfirmation: true,
			ExecutionContextJSON: marshalExecContext(botCallExecContext{Schedule: snap})}
		out, err := env.bots.execScheduleCreate(call, &botScheduleTaskArgs{
			Name: "unapproved", Cron: "0 1 * * *", Timezone: "UTC", Instruction: "unapproved",
		})
		if err != nil {
			t.Fatalf("create with user timezone %q: %v", currentTimezone, err)
		}
		var receipt struct {
			ID            string `json:"id"`
			UserTimezone  string `json:"user_timezone"`
			NextRunAtUser string `json:"next_run_at_user"`
		}
		if err := json.Unmarshal([]byte(out), &receipt); err != nil {
			t.Fatalf("decode receipt: %v %s", err, out)
		}
		task, err := env.bots.schedTasks.FindByID(receipt.ID)
		if err != nil {
			t.Fatalf("load task: %v", err)
		}
		if task.Name != snap.Name || task.CronExpression != snap.Cron || task.Timezone != snap.Timezone ||
			task.Instruction != snap.Instruction || !task.NeedsReport || task.Model != snap.EffectiveModel {
			t.Fatalf("落库配置必须全部取自快照: %+v", task)
		}
		if task.NextRunAt == nil {
			t.Fatal("created task missing next run")
		}
		want := task.NextRunAt.In(timeOrUTC(snap.UserTimezone)).Format("2006-01-02 15:04:05 MST")
		if receipt.UserTimezone != snap.UserTimezone || receipt.NextRunAtUser != want || *reads != 0 {
			t.Fatalf("回执应只用受理快照: receipt=%+v want=%s user_reads=%d", receipt, want, *reads)
		}
	}
	snap.ModelInherited = true
	call := &model.BotToolCall{BotID: bot.ID,
		ExecutionContextJSON: marshalExecContext(botCallExecContext{Schedule: snap})}
	out, err := env.bots.execScheduleCreate(call, &botScheduleTaskArgs{})
	if err != nil {
		t.Fatalf("create inherited: %v", err)
	}
	var receipt struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &receipt); err != nil {
		t.Fatalf("decode inherited: %v", err)
	}
	task, err := env.bots.schedTasks.FindByID(receipt.ID)
	if err != nil || task.Model != "" || *reads != 0 {
		t.Fatalf("继承模型应保存跟随策略且不读用户: task=%+v reads=%d err=%v", task, *reads, err)
	}
}

func TestBotInt136SchedulePreviewKeepsMergedNextRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "mergednext")
	bot := env.createBot(t, userID)
	setUserTimezone(t, env, userID, "Asia/Tokyo")
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	// 调度推进不改配置水位；这次计划可以已过期，不能从 now 重算把它藏掉。
	due := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(task *model.BotScheduleTask) {
		task.NextRunAt = &due
	})
	reads := scheduleUserReadProbe(t, env, nil)
	preview := func(action, fields string) *botScheduleCallPreview {
		t.Helper()
		args := `{"action":"` + action + `","task_id":"` + task.ID + `"` + fields + `}`
		prepared, err := botPrepareToolCall(botToolsetVersionOf(8), "fs_schedule", args)
		if err != nil {
			t.Fatalf("prepare %s: %v", action, err)
		}
		before := *reads
		execCtx := botCallExecContext{}
		if err := env.bots.resolveCallScope(botRow, prepared, args, &execCtx); err != nil {
			t.Fatalf("resolve %s: %v", action, err)
		}
		wantReads := 0
		if action == "update" || action == "enable" {
			wantReads = 1
		}
		if *reads-before != wantReads || execCtx.Schedule == nil {
			t.Fatalf("%s 时区应只在需要时解析一次: reads=%d snap=%+v", action, *reads-before, execCtx.Schedule)
		}
		if wantReads == 1 && execCtx.Schedule.UserTimezone != "Asia/Tokyo" {
			t.Fatalf("%s 应冻结回执时区: %+v", action, execCtx.Schedule)
		}
		return execCtx.Schedule
	}
	for _, action := range []string{"update", "enable", "run", "delete"} {
		fields := ""
		if action == "update" {
			fields = `,"instruction":"new instruction"`
		}
		got := preview(action, fields)
		wantLocal := due.In(timeOrUTC(task.Timezone)).Format("2006-01-02 15:04:05 MST")
		if got.NextRunAt != wantLocal || got.NextRunAtUTC != due.Format(time.RFC3339) {
			t.Fatalf("%s 应展示合并结果的原计划: got=%+v want=%s", action, got, wantLocal)
		}
	}
	if err := env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).
		UpdateColumns(map[string]interface{}{"enabled": false, "next_run_at": nil}).Error; err != nil {
		t.Fatalf("disable task: %v", err)
	}
	for _, fields := range []string{`,"instruction":"new instruction"`, `,"instruction":"new instruction","cron":"30 10 * * *"`} {
		got := preview("update", fields)
		if got.NextRunAt != "" || got.NextRunAtUTC != "" {
			t.Fatalf("停用任务的 update 不能凭空给出未来计划: %+v", got)
		}
	}
	// enable 与执行器同一谓词预测重新武装，但预测不能写回原本无计划的任务行。
	before := time.Now().UTC()
	got := preview("enable", "")
	next, err := time.Parse(time.RFC3339, got.NextRunAtUTC)
	if err != nil || !next.After(before) || !strings.Contains(got.NextRunAt, "09:00:00") {
		t.Fatalf("停用任务 enable 应预览重新武装的计划: %+v err=%v", got, err)
	}
	unchanged, err := env.bots.schedTasks.FindByID(task.ID)
	if err != nil {
		t.Fatalf("reload disabled task: %v", err)
	}
	if unchanged.NextRunAt != nil || unchanged.Enabled || !unchanged.UpdatedAt.Equal(task.UpdatedAt) {
		t.Fatalf("卡片预测不能写回任务行: %+v", unchanged)
	}
	if err := env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).
		UpdateColumn("enabled", true).Error; err != nil {
		t.Fatalf("enable without next run: %v", err)
	}
	if got := preview("enable", ""); got.NextRunAt == "" || got.NextRunAtUTC == "" {
		t.Fatalf("已启用但无计划也应按 enable 谓词预测: %+v", got)
	}
}

func TestBotInt136ScheduleCreateRejectsMissingSnapshot(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "missingsnap")
	bot := env.createBot(t, userID)
	reads := scheduleUserReadProbe(t, env, errors.New("unexpected user read"))
	for _, execContext := range []string{"", "{", "{}", `{"schedule":{}}`, `{"schedule":{"timezone":" "}}`} {
		call := &model.BotToolCall{BotID: bot.ID, ExecutionContextJSON: execContext}
		_, err := env.bots.execScheduleCreate(call, &botScheduleTaskArgs{
			Name: "n", Cron: "0 9 * * *", Timezone: "UTC", Instruction: "i",
		})
		var appErr *errs.AppError
		if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code || !strings.Contains(appErr.Message, "重新发起") {
			t.Fatalf("缺失或不完整快照应明确拒绝重发: ctx=%q err=%v", execContext, err)
		}
	}
	if n := countScheduleTasks(t, env, bot.ID); n != 0 || *reads != 0 {
		t.Fatalf("无快照 create 不应建任务或读用户: tasks=%d reads=%d", n, *reads)
	}
}

func TestBotInt136ScheduleUpdateEnableReceiptUsesSnapshot(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "updatereceipttz")
	bot := env.createBot(t, userID)
	setUserTimezone(t, env, userID, "UTC")
	reads := scheduleUserReadProbe(t, env, errors.New("unexpected user read"))
	for _, action := range []string{"update", "enable"} {
		for _, snapshot := range []struct {
			userTimezone string
			missing      bool
		}{{userTimezone: "Asia/Shanghai"}, {}, {missing: true}} {
			userTimezone := snapshot.userTimezone
			task := seedScheduleTask(t, env, bot.ID, func(task *model.BotScheduleTask) {
				task.Enabled = action != "enable"
			})
			call := &model.BotToolCall{BotID: bot.ID, RequiresConfirmation: true,
				ExecutionContextJSON: marshalExecContext(botCallExecContext{
					Schedule: &botScheduleCallPreview{
						UserTimezone:  userTimezone,
						TaskUpdatedAt: task.UpdatedAt.UTC().Format(time.RFC3339Nano),
					},
				})}
			if snapshot.missing {
				call.ExecutionContextJSON = ""
			}
			args := &botScheduleTaskArgs{TaskID: task.ID, Instruction: "approved instruction"}
			var out string
			var err error
			if action == "update" {
				out, err = env.bots.execScheduleUpdate(call, args)
			} else {
				out, err = env.bots.execScheduleEnable(call, args, true)
			}
			if err != nil {
				t.Fatalf("%s with snapshot user timezone %q: %v", action, userTimezone, err)
			}
			var receipt map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &receipt); err != nil {
				t.Fatalf("decode %s: %v", action, err)
			}
			if *reads != 0 {
				t.Fatalf("%s 回执不应重新查询用户时区: %d", action, *reads)
			}
			if userTimezone == "" {
				if receipt["user_timezone"] != nil || receipt["next_run_at_user"] != nil {
					t.Fatalf("%s 旧快照应省略用户时间: %s", action, out)
				}
				continue
			}
			var gotTimezone, local, utc string
			if err := json.Unmarshal(receipt["user_timezone"], &gotTimezone); err != nil {
				t.Fatalf("decode user timezone: %v", err)
			}
			if err := json.Unmarshal(receipt["next_run_at_user"], &local); err != nil {
				t.Fatalf("decode local time: %v", err)
			}
			if err := json.Unmarshal(receipt["next_run_at_utc"], &utc); err != nil {
				t.Fatalf("decode UTC time: %v", err)
			}
			next, err := time.Parse(time.RFC3339, utc)
			if err != nil {
				t.Fatalf("parse UTC time: %v", err)
			}
			want := next.In(timeOrUTC(userTimezone)).Format("2006-01-02 15:04:05 MST")
			if gotTimezone != userTimezone || local != want {
				t.Fatalf("%s 应沿用受理时区: timezone=%s local=%s want=%s", action, gotTimezone, local, want)
			}
		}
	}
}

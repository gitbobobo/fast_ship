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

// 定时任务持久小状态（INT-123）测试：state_get/state_set 在 schedule run 里
// 往返并跨运行持久化、字节数上限、非 schedule 会话拒绝、改任务不丢状态、
// 与长期记忆隔离、list 投影 has_state、运行时上下文带上一次运行信息。
// 工具集 v7 的动作与冻结基线（v1–v6 hash）见 bot_int103_test.go。

// seedStateRequestAndCall 为指定 run 落一条模型请求与调用账本行，让
// state_get/state_set 经 runIDOfCall 反查到所属任务（真实路径由
// commitAssistantTurn 建立同一关联）。
func seedStateRequestAndCall(t *testing.T, env *botTestEnv, run *model.BotRun, args string) *model.BotToolCall {
	t.Helper()
	var n int64
	if err := env.db.Model(&model.BotModelRequest{}).Where("run_id = ?", run.ID).Count(&n).Error; err != nil {
		t.Fatalf("count requests: %v", err)
	}
	now := time.Now().UTC()
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: n + 1,
		Status: model.BotModelRequestSucceeded, PromptRevision: 1,
		ToolsetVersion: botToolsetVersion,
		StartedAt:      now, FinishedAt: &now,
	}
	if err := env.db.Create(req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: run.BotID, RequestID: req.ID,
		ProviderToolCallID: "prov-state-" + uuid.NewString()[:8],
		ToolName:           "fs_schedule",
		ArgumentsJSON:      args, ArgumentsHash: "x", ExecutionMode: "sync",
		Status:               model.BotCallStatusSucceeded,
		ExecutionContextJSON: "{}",
		CreatedAt:            now,
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("seed call: %v", err)
	}
	return call
}

// botScheduleStateCall 在指定 schedule run 的上下文里执行一次 fs_schedule 调用。
func botScheduleStateCall(t *testing.T, env *botTestEnv, run *model.BotRun, args string) (string, error) {
	t.Helper()
	call := seedStateRequestAndCall(t, env, run, args)
	return env.bots.execScheduleTool(call)
}

// mustStateCall 同上，但执行失败即 Fatal。
func mustStateCall(t *testing.T, env *botTestEnv, run *model.BotRun, args string) string {
	t.Helper()
	out, err := botScheduleStateCall(t, env, run, args)
	if err != nil {
		t.Fatalf("state call %s: %v", args, err)
	}
	return out
}

// reloadScheduleTask 重读任务行。
func reloadScheduleTask(t *testing.T, env *botTestEnv, taskID string) *model.BotScheduleTask {
	t.Helper()
	task, err := env.bots.schedTasks.FindByID(taskID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	return task
}

// ---------- 1. 往返与跨运行持久化（走真实 schedule 会话） ----------

// run1 里模型调 state_set 写入状态，run 结束后状态持久；run2 里 state_get
// 读回同一 JSON，用户 REST 投影带 state/state_updated_at。
func TestBotInt123StateRoundTripPersistsAcrossRuns(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-state-set", Name: "fs_schedule",
			Args: `{"action":"state_set","state":{"last_id":"abc-1","seen":["x"]}}`,
		}}},
		fakeModelTurn{Text: "[[SILENT]]"},
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-state-get", Name: "fs_schedule",
			Args: `{"action":"state_get"}`,
		}}},
		fakeModelTurn{Text: "[[SILENT]]"},
	)
	userID := createBotTestUser(t, env.db, "schedstate123")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false
	})

	// run1：state_set。
	due := time.Now().UTC().Add(-3 * time.Second)
	env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).Update("next_run_at", due)
	env.bots.scheduleTick(time.Now().UTC())
	var run1 model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).Order("created_at ASC").First(&run1)
	done1 := waitForScheduleRunFinished(t, env, run1.ID)
	if done1.Status != model.BotRunStatusCompleted {
		t.Fatalf("run1 应完成: %s %v", done1.Status, done1.ErrorMessage)
	}

	stored := reloadScheduleTask(t, env, task.ID)
	if stored.StateJSON != `{"last_id":"abc-1","seen":["x"]}` {
		t.Fatalf("状态应按紧凑 JSON 持久化: %q", stored.StateJSON)
	}
	if stored.StateUpdatedAt == nil {
		t.Fatalf("写入后 state_updated_at 不应为空")
	}

	// run2：state_get 读回。
	due2 := time.Now().UTC().Add(-3 * time.Second)
	env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).Update("next_run_at", due2)
	env.bots.scheduleTick(time.Now().UTC())
	var runs []model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).Order("created_at ASC").Find(&runs)
	if len(runs) != 2 {
		t.Fatalf("应有 2 条 run，实得 %d", len(runs))
	}
	done2 := waitForScheduleRunFinished(t, env, runs[1].ID)
	if done2.Status != model.BotRunStatusCompleted {
		t.Fatalf("run2 应完成: %s %v", done2.Status, done2.ErrorMessage)
	}

	// run2 的 tool_reply 就是 state_get 的回执：状态原文、任务与上限都在。
	var replies []model.BotMessage
	if err := env.db.Where("bot_id = ? AND run_id = ? AND kind = ?",
		bot.ID, runs[1].ID, model.BotMessageKindToolReply).Order("seq ASC").Find(&replies).Error; err != nil {
		t.Fatalf("list replies: %v", err)
	}
	found := false
	for i := range replies {
		var reply botToolReplyContent
		if err := json.Unmarshal([]byte(replies[i].ContentJSON), &reply); err != nil {
			t.Fatalf("decode reply: %v", err)
		}
		if reply.ProviderToolCallID != "call-state-get" {
			continue
		}
		found = true
		for _, want := range []string{
			`"ok":true`, `"task_id":"` + task.ID + `"`,
			`"state":{"last_id":"abc-1","seen":["x"]}`,
			`"state_updated_at":"`, `"max_bytes":8192`,
		} {
			if !strings.Contains(reply.Content, want) {
				t.Fatalf("state_get 回执缺 %s: %s", want, reply.Content)
			}
		}
	}
	if !found {
		t.Fatalf("run2 应有 state_get 的协议回复: %+v", replies)
	}

	// 用户 REST 投影：state 是原始 JSON 文本（无损）、state_updated_at 是 RFC3339。
	resp, err := env.bots.GetScheduleTask(userID, bot.ID, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if resp.State == nil || *resp.State != `{"last_id":"abc-1","seen":["x"]}` {
		t.Fatalf("REST 投影应逐字带状态原文: %v", resp.State)
	}
	if resp.StateUpdatedAt == nil {
		t.Fatalf("REST 投影应带 state_updated_at")
	}
	if _, err := time.Parse(time.RFC3339, *resp.StateUpdatedAt); err != nil {
		t.Fatalf("state_updated_at 应为 RFC3339: %v", *resp.StateUpdatedAt)
	}
}

// ---------- 2. 清空与字节上限（执行器级） ----------

func TestBotInt123StateSetClearAndOversize(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstateops")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.NeedsReport = false })
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	// 写入：回执带 bytes/max_bytes/cleared，库里的 JSON 是紧凑形式。
	out := mustStateCall(t, env, run, `{"action":"state_set","state":{ "k" : "v" }}`)
	if !strings.Contains(out, `"bytes":9`) || !strings.Contains(out, `"cleared":false`) ||
		!strings.Contains(out, `"max_bytes":8192`) {
		t.Fatalf("state_set 回执不符: %s", out)
	}
	if got := reloadScheduleTask(t, env, task.ID).StateJSON; got != `{"k":"v"}` {
		t.Fatalf("状态应为紧凑 JSON: %q", got)
	}

	// 省略 state → 清空；state_updated_at 回到 NULL。
	out = mustStateCall(t, env, run, `{"action":"state_set"}`)
	if !strings.Contains(out, `"cleared":true`) || !strings.Contains(out, `"bytes":0`) {
		t.Fatalf("省略 state 应清空: %s", out)
	}
	cleared := reloadScheduleTask(t, env, task.ID)
	if cleared.StateJSON != "" || cleared.StateUpdatedAt != nil {
		t.Fatalf("清空后应无状态: %+v", cleared)
	}

	// 显式 null → 同样清空。
	mustStateCall(t, env, run, `{"action":"state_set","state":{"a":1}}`)
	out = mustStateCall(t, env, run, `{"action":"state_set","state":null}`)
	if !strings.Contains(out, `"cleared":true`) {
		t.Fatalf("null 应清空: %s", out)
	}

	// 超上限：8193 字符的字符串 + 两个引号 = 8195 字节 → 40001，消息含当前
	// 字节数与上限。
	_, err := botScheduleStateCall(t, env, run,
		`{"action":"state_set","state":"`+strings.Repeat("a", 8193)+`"}`)
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("超限应返回 40001，实得 %v", err)
	}
	if !strings.Contains(appErr.Message, "8195") || !strings.Contains(appErr.Message, "8192") {
		t.Fatalf("错误消息应含当前字节数与上限: %q", appErr.Message)
	}
	if got := reloadScheduleTask(t, env, task.ID).StateJSON; got != "" {
		t.Fatalf("失败写入不得改状态: %q", got)
	}

	// 恰好 8192 字节放行。
	out = mustStateCall(t, env, run,
		`{"action":"state_set","state":"`+strings.Repeat("a", 8190)+`"}`)
	if !strings.Contains(out, `"bytes":8192`) {
		t.Fatalf("恰好 8192 字节应放行: %s", out)
	}
	// state_get：无状态返回 null；有状态返回 JSON 值与更新时间。
	mustStateCall(t, env, run, `{"action":"state_set"}`)
	out = mustStateCall(t, env, run, `{"action":"state_get"}`)
	for _, want := range []string{`"state":null`, `"state_updated_at":null`, `"max_bytes":8192`} {
		if !strings.Contains(out, want) {
			t.Fatalf("空状态 state_get 回执缺 %s: %s", want, out)
		}
	}
	mustStateCall(t, env, run, `{"action":"state_set","state":[1,2]}`)
	out = mustStateCall(t, env, run, `{"action":"state_get"}`)
	if !strings.Contains(out, `"state":[1,2]`) || !strings.Contains(out, `"state_updated_at":"`) {
		t.Fatalf("有状态 state_get 回执不符: %s", out)
	}
	_ = userID
}

// 数字字面量逐字保留：state_get 回执与 REST 投影都原文透传状态 JSON，不解成
// float64——大整数（去重 ID 常见）不走精度往返，float64 装不下的 1e400 也不再
// 解析失败。
func TestBotInt123StateJSONFidelityPreservesNumberLiterals(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstatefidelity")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.NeedsReport = false })
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	const bigInt = `12345678901234567890`
	mustStateCall(t, env, run, `{"action":"state_set","state":{"n":`+bigInt+`,"f":1e400}}`)

	out := mustStateCall(t, env, run, `{"action":"state_get"}`)
	for _, want := range []string{`"n":` + bigInt, `"f":1e400`} {
		if !strings.Contains(out, want) {
			t.Fatalf("state_get 回执应逐字保留 %s: %s", want, out)
		}
	}

	resp, err := env.bots.GetScheduleTask(userID, bot.ID, task.ID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if resp.State == nil {
		t.Fatalf("REST 投影应带状态原文")
	}
	for _, want := range []string{bigInt, "1e400"} {
		if !strings.Contains(*resp.State, want) {
			t.Fatalf("REST 投影应逐字保留数字字面量 %s: %s", want, *resp.State)
		}
	}
	_ = userID
}

// ---------- 3. 非 schedule 会话拒绝 ----------

func TestBotInt123StateRejectedOutsideScheduleRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstateguard")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)

	// 无调用账本关联（主线直调、缺 request）。
	for _, args := range []string{`{"action":"state_get"}`, `{"action":"state_set","state":{"a":1}}`} {
		_, err := botScheduleCall(t, env, bot.ID, args)
		var appErr *errs.AppError
		if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
			t.Fatalf("非 schedule 会话应 40001: %v", err)
		}
		if appErr.Message != "state_get/state_set 只能在定时任务运行中使用" {
			t.Fatalf("拒绝消息不符: %q", appErr.Message)
		}
	}

	// 挂在主线 run 名下的调用同样拒绝。
	now := time.Now().UTC()
	mainRun := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "main:" + uuid.NewString(),
		Status: model.BotRunStatusRunning, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, mainRun); err != nil {
		t.Fatalf("seed main run: %v", err)
	}
	_, err := botScheduleStateCall(t, env, mainRun, `{"action":"state_get"}`)
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("主线 run 应拒绝 state_get: %v", err)
	}
	// 任务存在与否不影响判定：state 动作不收 task_id，主线一律不可用。
	_ = task
	_ = userID
}

// ---------- 4. 改任务不丢状态 ----------

func TestBotInt123StateSurvivesUpdateAndToggle(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstatekeep")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	mustStateCall(t, env, run, `{"action":"state_set","state":{"since":"2026-01-01"}}`)

	// 改指令/cron/模型。
	if _, err := env.bots.updateScheduleTask(bot.ID, task.ID, "改名", "30 10 * * *", "Asia/Shanghai", "新指令", nil, nil); err != nil {
		t.Fatalf("update: %v", err)
	}
	// 开关启停。
	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, task.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, err := env.bots.setScheduleTaskEnabled(bot.ID, task.ID, true); err != nil {
		t.Fatalf("enable: %v", err)
	}

	stored := reloadScheduleTask(t, env, task.ID)
	if stored.StateJSON != `{"since":"2026-01-01"}` || stored.StateUpdatedAt == nil {
		t.Fatalf("改任务/开关不得触碰状态: %+v", stored)
	}

	// 用户清空后状态消失；重复清空幂等。
	if _, err := env.bots.ClearScheduleTaskState(userID, bot.ID, task.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, err := env.bots.ClearScheduleTaskState(userID, bot.ID, task.ID); err != nil {
		t.Fatalf("clear again: %v", err)
	}
	cleared := reloadScheduleTask(t, env, task.ID)
	if cleared.StateJSON != "" || cleared.StateUpdatedAt != nil {
		t.Fatalf("清空后应无状态: %+v", cleared)
	}
	// 不存在的任务 → 40429。
	_, err := env.bots.ClearScheduleTaskState(userID, bot.ID, "missing")
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrBotScheduleTaskNotFound.Code {
		t.Fatalf("清空不存在的任务应 40429: %v", err)
	}
}

// 状态写入与清空都不推动任务的配置时间 updated_at：GORM Updates 会隐式
// autoUpdateTime 改写它，状态有独立的 state_updated_at，配置时间应保持不动。
func TestBotInt123StateWriteKeepsUpdatedAt(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstatetouch")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	before := reloadScheduleTask(t, env, task.ID).UpdatedAt
	time.Sleep(30 * time.Millisecond)

	mustStateCall(t, env, run, `{"action":"state_set","state":{"k":"v"}}`)
	written := reloadScheduleTask(t, env, task.ID)
	if !written.UpdatedAt.Equal(before) {
		t.Fatalf("state_set 不得改写 updated_at: %v -> %v", before, written.UpdatedAt)
	}
	if written.StateUpdatedAt == nil {
		t.Fatalf("state_updated_at 应更新")
	}

	if _, err := env.bots.ClearScheduleTaskState(userID, bot.ID, task.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}
	cleared := reloadScheduleTask(t, env, task.ID)
	if !cleared.UpdatedAt.Equal(before) {
		t.Fatalf("清空不得改写 updated_at: %v -> %v", before, cleared.UpdatedAt)
	}
	if cleared.StateJSON != "" || cleared.StateUpdatedAt != nil {
		t.Fatalf("清空后应无状态: %+v", cleared)
	}
}

// ---------- 5. 与长期记忆隔离、不影响主线读取面 ----------

func TestBotInt123StateIsNotMemoryAndMainlineUntouched(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstateiso")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	countRows := func(table string) int64 {
		t.Helper()
		var n int64
		if err := env.db.Table(table).Count(&n).Error; err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		return n
	}
	mainBefore, err := env.bots.messages.ListMainline(bot.ID, 0, 0, 200)
	if err != nil {
		t.Fatalf("list mainline: %v", err)
	}

	mustStateCall(t, env, run, `{"action":"state_set","state":{"dedupe":["id-1","id-2"]}}`)
	mustStateCall(t, env, run, `{"action":"state_get"}`)

	if n := countRows("bot_memory_versions"); n != 0 {
		t.Fatalf("状态写入不得产生记忆版本，实得 %d", n)
	}
	if n := countRows("bot_memory_files"); n != 0 {
		t.Fatalf("状态写入不得建记忆文件，实得 %d", n)
	}
	mainAfter, err := env.bots.messages.ListMainline(bot.ID, 0, 0, 200)
	if err != nil || len(mainAfter) != len(mainBefore) {
		t.Fatalf("状态写入不得进主线读取面: %v %d vs %d", err, len(mainAfter), len(mainBefore))
	}
}

// ---------- 6. list/管理动作的 has_state 投影 ----------

func TestBotInt123ListProjectionHasState(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstateproj")
	bot := env.createBot(t, userID)
	withState := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.Name = "有状态" })
	withoutState := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.Name = "无状态" })
	run := seedScheduleRun(t, env, bot.ID, withState.ID)
	mustStateCall(t, env, run, `{"action":"state_set","state":{"n":1}}`)

	var listOut struct {
		Items []map[string]interface{} `json:"items"`
	}
	if err := json.Unmarshal([]byte(mustScheduleCall(t, env, bot.ID, `{"action":"list"}`)), &listOut); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	states := map[string]bool{}
	for _, item := range listOut.Items {
		id, _ := item["id"].(string)
		v, _ := item["has_state"].(bool)
		states[id] = v
	}
	if !states[withState.ID] {
		t.Fatalf("有状态任务应 has_state=true: %v", listOut.Items)
	}
	if states[withoutState.ID] {
		t.Fatalf("无状态任务应 has_state=false: %v", listOut.Items)
	}
	// 管理动作（update/enable）共用的瘦身投影同样带 has_state。
	out := mustScheduleCall(t, env, bot.ID, `{"action":"update","task_id":"`+withoutState.ID+`","name":"改名后"}`)
	if !strings.Contains(out, `"has_state":false`) {
		t.Fatalf("update 投影应带 has_state: %s", out)
	}
	_ = userID
}

// ---------- 7. 运行时上下文：上一次运行 + note 里的状态说明 ----------

func TestBotInt123RuntimeContextLastRunAndStateNote(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstatert")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.NeedsReport = false })

	buildRT := func(run *model.BotRun) botScheduleRuntimeContext {
		t.Helper()
		botRow, err := env.bots.bots.Get(bot.ID)
		if err != nil {
			t.Fatalf("load bot: %v", err)
		}
		sess, err := env.bots.newScheduleSession(botRow, run)
		if err != nil {
			t.Fatalf("session: %v", err)
		}
		built, err := env.bots.buildScheduleRunContext(botRow, run, sess)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		last := built.Request.Messages[len(built.Request.Messages)-1]
		raw, _ := json.Marshal(last)
		var wire struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		prefix := botPlatformPrefix(botProjectionNonce(botRow), "运行时上下文") + "\n"
		if !strings.HasPrefix(wire.Content, prefix) {
			t.Fatalf("末条消息应为运行时上下文: %q", wire.Content)
		}
		var rt botScheduleRuntimeContext
		if err := json.Unmarshal([]byte(strings.TrimPrefix(wire.Content, prefix)), &rt); err != nil {
			t.Fatalf("unmarshal rt: %v", err)
		}
		return rt
	}

	// 首次运行：无上一次运行，last_run_* 省略；note 说明小状态用法。
	run1 := seedScheduleRun(t, env, bot.ID, task.ID)
	rt1 := buildRT(run1)
	if rt1.LastRunAt != "" || rt1.LastRunStatus != "" || rt1.LastRunSummary != "" {
		t.Fatalf("无上一次运行时 last_run_* 应省略: %+v", rt1)
	}
	for _, kw := range []string{"state_get", "state_set", "上次已报的 ID"} {
		if !strings.Contains(rt1.Note, kw) {
			t.Fatalf("note 应说明任务小状态用法（缺 %q）: %q", kw, rt1.Note)
		}
	}

	// 制造一次已结束运行（completed + 摘要）后再构建：带上一次运行三字段。
	finished := time.Now().UTC().Add(-time.Minute)
	env.db.Model(&model.BotRun{}).Where("id = ?", run1.ID).Updates(map[string]interface{}{
		"status": model.BotRunStatusCompleted, "finished_at": finished,
		"summary": "上次汇总：2 条新增",
	})
	run2 := seedScheduleRun(t, env, bot.ID, task.ID)
	rt2 := buildRT(run2)
	if rt2.LastRunStatus != string(model.BotRunStatusCompleted) {
		t.Fatalf("last_run_status 应为 completed: %+v", rt2)
	}
	if rt2.LastRunSummary != "上次汇总：2 条新增" {
		t.Fatalf("last_run_summary 不符: %q", rt2.LastRunSummary)
	}
	if _, err := time.Parse(time.RFC3339, rt2.LastRunAt); err != nil {
		t.Fatalf("last_run_at 应为 RFC3339: %q", rt2.LastRunAt)
	}
	_ = userID
}

// 超过折叠阈值（6000 字符）的 state_get 回执必须原文进上下文：折叠成
// fs_result.read 引用会让模型拿不到状态内容，而续读只会回同样的大块。
func TestBotInt123StateGetReplyExemptFromFold(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-state-set-big", Name: "fs_schedule",
			Args: `{"action":"state_set","state":{"seen":["` + strings.Repeat("x", 6200) + `"]}}`,
		}}},
		fakeModelTurn{Text: "[[SILENT]]"},
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{
			ID: "call-state-get-big", Name: "fs_schedule",
			Args: `{"action":"state_get"}`,
		}}},
		fakeModelTurn{Text: "[[SILENT]]"},
	)
	userID := createBotTestUser(t, env.db, "schedstatefold")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) { tk.NeedsReport = false })

	// run1：写入超过折叠阈值的状态。
	due := time.Now().UTC().Add(-3 * time.Second)
	env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).Update("next_run_at", due)
	env.bots.scheduleTick(time.Now().UTC())
	var run1 model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).Order("created_at ASC").First(&run1)
	if done := waitForScheduleRunFinished(t, env, run1.ID); done.Status != model.BotRunStatusCompleted {
		t.Fatalf("run1 应完成: %s %v", done.Status, done.ErrorMessage)
	}

	// run2：state_get 读回，回执超过折叠阈值。
	due2 := time.Now().UTC().Add(-3 * time.Second)
	env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).Update("next_run_at", due2)
	env.bots.scheduleTick(time.Now().UTC())
	var runs []model.BotRun
	env.db.Where("schedule_task_id = ?", task.ID).Order("created_at ASC").Find(&runs)
	if len(runs) != 2 {
		t.Fatalf("应有 2 条 run，实得 %d", len(runs))
	}
	if done2 := waitForScheduleRunFinished(t, env, runs[1].ID); done2.Status != model.BotRunStatusCompleted {
		t.Fatalf("run2 应完成: %s %v", done2.Status, done2.ErrorMessage)
	}

	// 为 run2 重建投影：state_get 回执原文在列，不出现折叠引用。
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	sess, err := env.bots.newScheduleSession(botRow, &runs[1])
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	built, err := env.bots.buildScheduleRunContext(botRow, &runs[1], sess)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	raw, _ := json.Marshal(built.Request.Messages)
	wire := string(raw)
	if !strings.Contains(wire, strings.Repeat("x", 6200)) {
		t.Fatalf("state_get 回执应原文进入上下文（含状态正文）")
	}
	if strings.Contains(wire, "[已折叠]") {
		t.Fatalf("state_get 回执不得被折叠")
	}
	_ = userID
}

// 冻结工具集决定 note 是否宣传 state：toolset_version=6 的降级会话没有
// state_get/state_set，note 不得诱导调用必然失败的动作；v7 会话保持宣传。
func TestBotInt123StateNoteFollowsFrozenToolset(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstatenote")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	buildNote := func() string {
		t.Helper()
		botRow, err := env.bots.bots.Get(bot.ID)
		if err != nil {
			t.Fatalf("load bot: %v", err)
		}
		sess, err := env.bots.newScheduleSession(botRow, run)
		if err != nil {
			t.Fatalf("session: %v", err)
		}
		built, err := env.bots.buildScheduleRunContext(botRow, run, sess)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		last := built.Request.Messages[len(built.Request.Messages)-1]
		raw, _ := json.Marshal(last)
		var wire struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(raw, &wire); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		var rt botScheduleRuntimeContext
		rtPrefix := botPlatformPrefix(botProjectionNonce(botRow), "运行时上下文") + "\n"
		if err := json.Unmarshal([]byte(strings.TrimPrefix(wire.Content, rtPrefix)), &rt); err != nil {
			t.Fatalf("unmarshal rt: %v", err)
		}
		return rt.Note
	}

	v7Note := buildNote()
	for _, kw := range []string{"state_get", "state_set", "上次已报的 ID"} {
		if !strings.Contains(v7Note, kw) {
			t.Fatalf("v7 会话 note 应说明任务小状态（缺 %q）: %q", kw, v7Note)
		}
	}

	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("toolset_version", 6).Error; err != nil {
		t.Fatalf("downgrade toolset: %v", err)
	}
	v6Note := buildNote()
	for _, kw := range []string{"state_get", "state_set"} {
		if strings.Contains(v6Note, kw) {
			t.Fatalf("v6 会话 note 不得宣传 state 动作: %q", v6Note)
		}
	}
	_ = userID
}

// 上限按 UTF-8 字节而非字符计：中文 3 字节/字符，2730 字 = 8190 字节 + 2 引号
// 恰好 8192 放行；2731 字 = 8195 字节拒绝。
func TestBotInt123StateLimitCountsUTF8Bytes(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "schedstateutf8")
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	out := mustStateCall(t, env, run,
		`{"action":"state_set","state":"`+strings.Repeat("状", 2730)+`"}`)
	if !strings.Contains(out, `"bytes":8192`) {
		t.Fatalf("2730 个汉字应恰好 8192 字节放行: %s", out)
	}

	_, err := botScheduleStateCall(t, env, run,
		`{"action":"state_set","state":"`+strings.Repeat("状", 2731)+`"}`)
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("多字节超限应 40001，实得 %v", err)
	}
	if !strings.Contains(appErr.Message, "8195") {
		t.Fatalf("错误消息应含按字节计的当前大小: %q", appErr.Message)
	}
	_ = userID
}

// fs_help 文本随新动作更新：state_get/state_set 的可用范围、大小上限与清空
// 语义要写明，否则模型只能靠报错学习。
func TestBotInt123HelpTextMentionsStateActions(t *testing.T) {
	for _, kw := range []string{"state_get", "state_set", "8192", "省略或 null 即清空", "定时任务运行"} {
		if !strings.Contains(botHelpText, kw) {
			t.Fatalf("fs_schedule 帮助应说明任务小状态（缺 %q）", kw)
		}
	}
}

// ---------- 8. 平台提示词规则 8 ----------

func TestBotInt123PlatformPromptMentionsState(t *testing.T) {
	full := buildBotSystemPrompt("", "t-nonce")
	for _, kw := range []string{"state_get", "state_set", "跨运行保留的小状态", "仅新增时提醒"} {
		if !strings.Contains(full, kw) {
			t.Fatalf("平台提示词应说明任务小状态（缺 %q）", kw)
		}
	}
	// INT-123 把工具集版本升到 7；后续版本（INT-136 起 v8）只增不减，
	// 这里钉下限保护 state 动作不被回退。
	if botToolsetVersion < 7 {
		t.Fatalf("工具集版本应不低于 7: %d", botToolsetVersion)
	}
}

// ---------- 9. v7 升级告知文案 ----------

// 存量 v6 机器人升级到 v7 时必须收到本次变化的说明，而不是回退到 v2 兑底文案
// （后者只讲 fs_memory，会让用户以为这次升级与定时任务状态无关）。
func TestBotInt123ToolsetChangeNoteHasV7(t *testing.T) {
	note := botToolsetChangeNote(6, 7)
	for _, want := range []string{"state_get", "state_set", "8192"} {
		if !strings.Contains(note, want) {
			t.Fatalf("6→7 升级说明应描述任务小状态（缺 %q）: %q", want, note)
		}
	}
	if strings.Contains(note, "fs_memory") {
		t.Fatalf("6→7 不得复用 v2 兑底文案: %q", note)
	}
}

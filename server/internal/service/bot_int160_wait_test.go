package service

// INT-160 fs_thread/wait 测试：受理即 running 不进 worker、idle 稳定窗防抖、
// needs_reply/error 立即触发、启动宽限 not_started、超时分类（unchanged /
// progressing / degraded / thread_gone）、取消与重启分支、防循环限额与重复挂、
// 定时任务会话 15 分钟上限。工具集 v12 与冻结基线（v1–v11 hash 不动）。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

// botToolsetV12FrozenHash 是 INT-160 发布的 v12 wire 字节基线：wait 动作与
// on/timeout_minutes 参数只进本版；之后任何改动都必须先递增版本。
const botToolsetV12FrozenHash = "89ddf49802db9adc48b77fcbb59cd8dde91ae64254426cc175d2cd4e98e6d3c0"

func shrinkWaitTimers(env *botTestEnv) {
	env.bots.waitCfg = botWaitEvalConfig{
		IdleStable:   80 * time.Millisecond,
		StartGrace:   300 * time.Millisecond,
		DetailBudget: 3 * time.Second,
	}
}

// seedWaitRun 落一条已收尾的主线（或定时任务）会话 run 与请求行，供
// commitAssistantTurn 受理调用。受理只读 run 行的 session_kind，不要求活动
// 状态——用已收尾的 run 避开「每 bot 唯一活动主线 run」的索引，也让收尾
// 唤醒拉起的后台会话与测试种子互不竞争。
func seedWaitRun(t *testing.T, env *botTestEnv, botID string, schedule bool) (*model.BotRun, *model.BotModelRequest) {
	t.Helper()
	now := time.Now().UTC()
	kind := model.BotSessionKindMain
	trigger := model.BotTriggerUserMessage
	if schedule {
		kind = model.BotSessionKindSchedule
		trigger = model.BotTriggerSchedule
	}
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource:      trigger,
		TriggerKey:         "wake:" + uuid.NewString(),
		Status:             model.BotRunStatusCompleted,
		SessionKind:        kind,
		ConfigSnapshotJSON: "{}",
		CreatedAt:          now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, PromptRevision: 1,
		ToolsetVersion: botToolsetVersion,
		StartedAt:      now, FinishedAt: &now,
	}
	if err := env.db.Create(req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	return run, req
}

// acceptWaitCall 走真实 commitAssistantTurn 受理一次 fs_thread/wait。
func acceptWaitCall(t *testing.T, env *botTestEnv, botID string, schedule bool, args string) *model.BotToolCall {
	t.Helper()
	botRow, err := env.bots.bots.Get(botID)
	if err != nil {
		t.Fatalf("get bot row: %v", err)
	}
	run, req := seedWaitRun(t, env, botID, schedule)
	reply := &botModelReply{ToolCalls: []botModelToolCall{{
		ID: "prov-wait-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args,
	}}}
	msgID := "msg-" + uuid.NewString()
	calls, err := env.bots.commitAssistantTurn(botRow, run, req, reply, msgID)
	if err != nil {
		t.Fatalf("commit assistant turn: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("应受理 1 个调用，实得 %d", len(calls))
	}
	return calls[0]
}

// waitCallTerminal 驱动评估器（生产由 5s 扫描驱动）并轮询调用落终态。
func waitCallTerminal(t *testing.T, env *botTestEnv, callID, what string) *model.BotToolCall {
	t.Helper()
	var out *model.BotToolCall
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		env.bots.evaluateThreadWaits()
		if c, err := env.bots.calls.FindByID(callID); err == nil && c.Status.IsTerminal() {
			out = c
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if out == nil {
		t.Fatalf("等待 %s 超时", what)
	}
	return out
}

// driveWaitEvaluation 周期驱动评估器直到条件成立（不依赖 5s 扫描循环）。
func driveWaitEvaluation(t *testing.T, env *botTestEnv, until func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		env.bots.evaluateThreadWaits()
		if until() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("驱动评估器超时")
}

// waitResultJSON 解析调用结果里的结构化 wait 收尾对象。
func waitResultJSON(t *testing.T, call *model.BotToolCall) map[string]interface{} {
	t.Helper()
	raw := ""
	if call.ResultJSON != nil {
		raw = *call.ResultJSON
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode wait result %q: %v", raw, err)
	}
	return out
}

// waitHubReady 等待环境的 shell 订阅就绪（活跃面 Ready）。
func waitHubReady(t *testing.T, env *botTestEnv, userID, envID string) {
	t.Helper()
	waitForBot(t, "hub ready", 5*time.Second, func() bool {
		v, err := env.bots.threads.PeekShell(userID, envID)
		return err == nil && v != nil && v.State == t3ShellReady
	})
}

const int160RunningThread = "th-wait-run"
const int160IdleThread = "th-wait-idle"

// pairWaitT3 配对一个带「running + idle」两线程快照的环境，并补 th-wait-run
// 的详情投影（收尾详情读取用）。
func pairWaitT3(t *testing.T, env *botTestEnv, userID string) (*fakeT3Environment, string) {
	t.Helper()
	threads := []any{
		shellThread(int160RunningThread, "在跑", "running", map[string]any{
			"activeRunId":        "run-1",
			"latestRunId":        "run-1",
			"latestRunStartedAt": "2026-10-20T00:00:00.000Z",
		}),
		shellThread(int160IdleThread, "平静", "idle", nil),
	}
	fake, envID := pairBotT3WithSnapshot(t, env, userID, "等待机",
		shellSnapshotFixture([]any{map[string]any{"id": "p-1", "title": "P"}}, threads))
	fake.mu.Lock()
	fake.projections[int160RunningThread] = threadProjection(int160RunningThread,
		[]any{map[string]any{"id": "run-1", "status": "running", "ordinal": 1}},
		[]map[string]any{
			{"position": 0, "visibility": "local", "sourceThreadId": int160RunningThread, "sourceItemId": "w1",
				"item": map[string]any{"id": "w1", "type": "assistant_message", "status": "completed", "text": "已经开始处理"}},
		})
	fake.projections[int160IdleThread] = threadProjection(int160IdleThread, []any{}, nil)
	fake.mu.Unlock()
	shrinkWaitTimers(env)
	waitHubReady(t, env, userID, envID)
	return fake, envID
}

// ---------- 1. 工具集 v12：wait 进活跃面，v1–v11 字节冻结 ----------

func TestBotInt160ToolsetV12(t *testing.T) {
	v11 := botToolsetVersionOf(11)
	v12 := botToolsetVersionOf(12)
	if len(v12.schemas) != len(v11.schemas) {
		t.Fatalf("v12 不新增工具: %d vs %d", len(v12.schemas), len(v11.schemas))
	}
	if v12.hash == v11.hash {
		t.Fatalf("v12 与 v11 的 tools_hash 不应相同")
	}
	if v12.hash != botToolsetV12FrozenHash {
		t.Fatalf("v12 tools_hash 偏离基线: got %s want %s", v12.hash, botToolsetV12FrozenHash)
	}
	// v1–v11 各冻结基线原样通过：v12 的演进不得回写任何已发布版本。
	for _, tc := range []struct {
		version int64
		want    string
	}{
		{1, botToolsetV1FrozenHash}, {2, botToolsetV2FrozenHash}, {3, botToolsetV3FrozenHash},
		{4, botToolsetV4FrozenHash}, {5, botToolsetV5FrozenHash}, {6, botToolsetV6FrozenHash},
		{7, botToolsetV7FrozenHash}, {8, botToolsetV8FrozenHash}, {9, botToolsetV9FrozenHash},
		{10, botToolsetV10FrozenHash}, {11, botToolsetV11FrozenHash},
	} {
		if got := botToolsetVersionOf(tc.version).hash; got != tc.want {
			t.Fatalf("v%d tools_hash 偏离冻结基线: got %s want %s", tc.version, got, tc.want)
		}
	}
	// v9–v11 的 fs_thread 冻结在 botThreadArgsV9Frozen；v12 用活跃结构体。
	for _, version := range []int64{9, 10, 11} {
		thread := botToolsetVersionOf(version).byName["fs_thread"]
		if thread.ArgsType.Name() != "botThreadArgsV9Frozen" {
			t.Fatalf("v%d fs_thread 应冻结在 botThreadArgsV9Frozen，实得 %s", version, thread.ArgsType.Name())
		}
		if _, ok := v12.byAction["fs_thread/wait"]; !ok {
			t.Fatalf("v12 应含 fs_thread/wait")
		}
	}
	thread := v12.byName["fs_thread"]
	if thread.ArgsType.Name() != "botThreadArgs" {
		t.Fatalf("v12 fs_thread 应使用活跃参数结构体，实得 %s", thread.ArgsType.Name())
	}
	if len(thread.Actions) != 12 {
		t.Fatalf("v12 fs_thread 应有 12 个动作，实得 %d", len(thread.Actions))
	}
	waitDef := v12.byAction["fs_thread/wait"]
	if waitDef.Mode != model.BotExecutionAsync || waitDef.RequiresConfirmation {
		t.Fatalf("wait 应为异步、不需确认: %+v", waitDef)
	}
	if botReadOnlyActions["fs_thread/wait"] {
		t.Fatalf("wait 不是同步读取动作，不得列入只读")
	}
	rule := botActionRules["fs_thread/wait"]
	if len(rule.Required) != 2 || rule.Required[0].Name != "env_id" || rule.Required[1].Name != "thread_id" {
		t.Fatalf("wait 的字段规则应为 env_id+thread_id: %+v", rule)
	}
	if len(rule.Forbidden) == 0 {
		t.Fatalf("wait 应声明不接受的字段")
	}

	// 受理校验：缺 thread_id、非法 on、越界 timeout、禁止字段都拒绝。
	if _, err := botPrepareToolCall(v12, "fs_thread", `{"action":"wait","env_id":"e1"}`); err == nil {
		t.Fatalf("wait 缺 thread_id 应被拒绝")
	}
	if _, err := botPrepareToolCall(v12, "fs_thread", `{"action":"wait","env_id":"e1","thread_id":"t1","on":"idle,pr"}`); err == nil {
		t.Fatalf("wait 的 on 不接受 pr")
	}
	if _, err := botPrepareToolCall(v12, "fs_thread", `{"action":"wait","env_id":"e1","thread_id":"t1","timeout_minutes":31}`); err == nil {
		t.Fatalf("wait 的 timeout_minutes 越界应被拒绝")
	}
	if _, err := botPrepareToolCall(v12, "fs_thread", `{"action":"wait","env_id":"e1","thread_id":"t1","text":"x"}`); err == nil {
		t.Fatalf("wait 不接受 text")
	}
	prepared, err := botPrepareToolCall(v12, "fs_thread",
		`{"action":"wait","env_id":"e1","thread_id":"t1","on":"needs_reply","timeout_minutes":30}`)
	if err != nil {
		t.Fatalf("wait 应被受理: %v", err)
	}
	if prepared.Mode != model.BotExecutionAsync || prepared.RequiresConfirmation {
		t.Fatalf("wait 受理应为异步免确认: %+v", prepared)
	}
	// 缺省参数（on/timeout_minutes 省略）合法。
	if _, err := botPrepareToolCall(v12, "fs_thread", `{"action":"wait","env_id":"e1","thread_id":"t1"}`); err != nil {
		t.Fatalf("wait 缺省参数应被受理: %v", err)
	}
	// 工具与动作描述写明 wait 语义，且不含 watch/watching 字样（PR 监听字段
	// watching 是既有口径，只约束 wait 自己的描述与帮助条目）。
	for _, want := range []string{"wait", "outcome", "running"} {
		if !strings.Contains(thread.Description, want) && !strings.Contains(waitDef.Description, want) {
			t.Fatalf("v12 fs_thread 描述应含 %q", want)
		}
	}
	// watch 字样只允许出现在规格规定的 outcome 取值 watch_degraded 里。
	stripOutcome := func(s string) string {
		return strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(s), "watch_degraded", ""), "watching", "")
	}
	if strings.Contains(stripOutcome(thread.Description), "watch") ||
		strings.Contains(stripOutcome(waitDef.Description), "watch") {
		t.Fatalf("wait 描述不得出现 watch 字样（watch_degraded 取值除外）")
	}
	waitBullet := ""
	for _, line := range strings.Split(botHelpText, "\n") {
		if strings.Contains(line, "fs_thread wait") {
			waitBullet = line
		}
	}
	if waitBullet == "" || strings.Contains(stripOutcome(waitBullet), "watch") {
		t.Fatalf("帮助文本应有 wait 条目且不含 watch 字样（watch_degraded 取值除外）: %q", waitBullet)
	}
	if !strings.Contains(waitBullet, "not_started") || !strings.Contains(waitBullet, "already_waiting") {
		t.Fatalf("帮助文本 wait 条目应写明 outcome 语义: %q", waitBullet)
	}
}

// ---------- 2. 受理即 running、不进 worker 队列 ----------

func TestBotInt160WaitAcceptedRunningNotQueued(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitaccept")
	bot := env.createBot(t, userID)
	_, envID := pairWaitT3(t, env, userID)

	call := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160RunningThread+`"}`)
	if call.Status != model.BotCallStatusRunning || call.StartedAt == nil {
		t.Fatalf("wait 受理即 running 并写 started_at: %+v", call)
	}
	if call.ExecutionMode != string(model.BotExecutionAsync) || call.RequiresConfirmation {
		t.Fatalf("wait 应是异步免确认: %+v", call)
	}

	// 不进 worker 队列：ListQueued 不返回它，扫描推送也不改变状态。
	queued, err := env.bots.calls.ListQueued(64)
	if err != nil {
		t.Fatalf("list queued: %v", err)
	}
	for _, c := range queued {
		if c.ID == call.ID {
			t.Fatalf("wait 不得进 queued 队列")
		}
	}
	env.bots.scanQueuedCalls()
	fresh, err := env.bots.calls.FindByID(call.ID)
	if err != nil || fresh.Status != model.BotCallStatusRunning {
		t.Fatalf("扫描后 wait 应保持 running: %v %+v", err, fresh)
	}

	// 回执：status=running 且带 waiting_for。
	var replies []model.BotMessage
	if err := env.db.Where("bot_id = ? AND call_id = ? AND kind = ?",
		bot.ID, call.ID, model.BotMessageKindToolReply).Find(&replies).Error; err != nil {
		t.Fatalf("list replies: %v", err)
	}
	if len(replies) != 1 {
		t.Fatalf("wait 应恰好一条回执，实得 %d", len(replies))
	}
	var receipt botToolReplyContent
	if err := json.Unmarshal([]byte(replies[0].ContentJSON), &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt.Status != string(model.BotCallStatusRunning) {
		t.Fatalf("回执 status 应为 running: %+v", receipt)
	}
	if !strings.Contains(receipt.Content, "waiting_for") {
		t.Fatalf("回执应带 waiting_for: %s", receipt.Content)
	}

	// 评估现场已注册（基线可观察：挂上时线程已在跑 → started）。
	if !env.bots.waitRegistered(call.ID) {
		t.Fatalf("受理后应注册评估现场")
	}
	entry := func() *botWaitEntry {
		env.bots.waitMu.Lock()
		defer env.bots.waitMu.Unlock()
		return env.bots.waits[call.ID]
	}()
	if entry == nil || !entry.started {
		t.Fatalf("挂上时线程在跑，应直接视为已开始: %+v", entry)
	}

	// 收尾清理：取消掉，避免残留 running 调用干扰断言。
	if _, ok := env.bots.cancelBotCall(fresh); !ok {
		t.Fatalf("running 的 wait 应可取消")
	}
	driveWaitEvaluation(t, env, func() bool {
		c, err := env.bots.calls.FindByID(call.ID)
		return err == nil && c.Status == model.BotCallStatusCancelled
	})
}

// ---------- 3. idle 稳定窗防抖触发 ----------

func TestBotInt160WaitIdleDebounceTrigger(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitidle")
	bot := env.createBot(t, userID)
	fake, envID := pairWaitT3(t, env, userID)

	call := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160RunningThread+`","on":"idle"}`)

	// 立即评估一轮：线程仍在跑，不得命中。
	env.bots.evaluateThreadWaits()
	if c, _ := env.bots.calls.FindByID(call.ID); c.Status != model.BotCallStatusRunning {
		t.Fatalf("线程未 idle 前不得收尾: %+v", c.Status)
	}

	// 推送增量：线程转 idle（run 结束）。稳定窗未满前不得命中。
	fake.pushThreadDelta(t, shellThread(int160RunningThread, "在跑", "completed", nil))
	env.bots.evaluateThreadWaits()
	if c, _ := env.bots.calls.FindByID(call.ID); c.Status != model.BotCallStatusRunning {
		t.Fatalf("idle 未稳定前不得收尾: %+v", c.Status)
	}

	done := waitCallTerminal(t, env, call.ID, "idle 稳定后触发")
	if done.Status != model.BotCallStatusSucceeded {
		t.Fatalf("触发应落 succeeded: %+v", done)
	}
	result := waitResultJSON(t, done)
	if result["outcome"] != "triggered" {
		t.Fatalf("outcome 应为 triggered: %v", result["outcome"])
	}
	matched, _ := result["matched"].([]interface{})
	if len(matched) != 1 || matched[0] != "idle" {
		t.Fatalf("命中项应为 idle: %v", matched)
	}
	// 变化对比：status running→idle、run_status running→completed。
	changes, _ := result["changes"].(map[string]interface{})
	if changes == nil {
		t.Fatalf("唤醒应带变化对比: %v", result)
	}
	statusChange, _ := changes["status"].(map[string]interface{})
	if statusChange == nil || statusChange["before"] != "running" || statusChange["after"] != "idle" {
		t.Fatalf("status 前后对比不符: %v", changes["status"])
	}
	// 详情读取补齐：queue.held、条目摘要。
	if _, has := result["queue_held"]; !has {
		t.Fatalf("唤醒应带 queue_held: %v", result)
	}
	recent, _ := result["recent_items"].([]interface{})
	if len(recent) == 0 {
		t.Fatalf("唤醒应带最近条目摘要: %v", result)
	}
}

// ---------- 4. needs_reply / error 立即触发 ----------

func TestBotInt160WaitImmediateTriggers(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitimmediate")
	bot := env.createBot(t, userID)
	fake, envID := pairWaitT3(t, env, userID)

	// needs_reply：推送 pendingRuntimeRequest，一轮评估即命中（无防抖）。
	call1 := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160RunningThread+`","on":"needs_reply"}`)
	fake.pushThreadDelta(t, shellThread(int160RunningThread, "在跑", "running", map[string]any{
		"activeRunId":        "run-1",
		"latestRunId":        "run-1",
		"latestRunStartedAt": "2026-10-20T00:00:00.000Z",
		"pendingRuntimeRequest": map[string]any{
			"id": "req-1", "kind": "approval", "createdAt": "2026-10-22T00:00:00.000Z",
		},
	}))
	done1 := waitCallTerminal(t, env, call1.ID, "needs_reply 触发")
	r1 := waitResultJSON(t, done1)
	if r1["outcome"] != "triggered" {
		t.Fatalf("needs_reply 应触发: %v", r1)
	}
	if matched, _ := r1["matched"].([]interface{}); len(matched) != 1 || matched[0] != "needs_reply" {
		t.Fatalf("命中项应为 needs_reply: %v", r1["matched"])
	}

	// error：线程已终态（前一用例已收尾），重新挂上等待 error；
	// 推送 failed 状态与新一轮 latestRunStartedAt → 立即命中。
	call2 := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160RunningThread+`","on":"error"}`)
	fake.pushThreadDelta(t, shellThread(int160RunningThread, "挂了", "failed", map[string]any{
		"latestRunId":        "run-2",
		"latestRunStartedAt": "2026-10-22T01:00:00.000Z",
	}))
	done2 := waitCallTerminal(t, env, call2.ID, "error 触发")
	r2 := waitResultJSON(t, done2)
	if r2["outcome"] != "triggered" {
		t.Fatalf("error 应触发: %v", r2)
	}
	if matched, _ := r2["matched"].([]interface{}); len(matched) != 1 || matched[0] != "error" {
		t.Fatalf("命中项应为 error: %v", r2["matched"])
	}
}

// ---------- 5. 启动宽限：线程根本没开始 → not_started ----------

func TestBotInt160WaitNotStarted(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitnotstart")
	bot := env.createBot(t, userID)
	_, envID := pairWaitT3(t, env, userID)

	// 挂在一个平静线程上（缺省条件含 idle）：若没有启动宽限抑制，idle 稳定窗
	// 会抢在宽限之前命中；正确行为是以 not_started 收尾。
	call := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160IdleThread+`"}`)
	done := waitCallTerminal(t, env, call.ID, "not_started 收尾")
	if done.Status != model.BotCallStatusSucceeded {
		t.Fatalf("not_started 应落 succeeded: %+v", done)
	}
	result := waitResultJSON(t, done)
	if result["outcome"] != "not_started" {
		t.Fatalf("outcome 应为 not_started: %v", result["outcome"])
	}
}

// ---------- 6. 超时分类 ----------

func TestBotInt160WaitTimeoutClassifications(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waittimeout")
	bot := env.createBot(t, userID)
	fake, envID := pairWaitT3(t, env, userID)

	forceDeadline := func(callID string, degrade bool) {
		env.bots.waitMu.Lock()
		defer env.bots.waitMu.Unlock()
		e, ok := env.bots.waits[callID]
		if !ok {
			t.Fatalf("评估现场应存在")
		}
		e.deadline = time.Now().Add(20 * time.Millisecond)
		if degrade {
			e.notReadyTicks += 50
		}
	}

	// timeout_unchanged：监听可信、确实无变化。
	call1 := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160IdleThread+`","on":"needs_reply"}`)
	forceDeadline(call1.ID, false)
	done1 := waitCallTerminal(t, env, call1.ID, "timeout_unchanged 收尾")
	r1 := waitResultJSON(t, done1)
	if r1["outcome"] != "timeout_unchanged" {
		t.Fatalf("outcome 应为 timeout_unchanged: %v", r1)
	}

	// timeout_progressing：有更新（activity 变化）但未命中触发条件。
	call2 := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160RunningThread+`","on":"needs_reply"}`)
	fake.pushThreadDelta(t, shellThread(int160RunningThread, "在跑", "running", map[string]any{
		"activeRunId":        "run-9",
		"latestRunId":        "run-9",
		"latestRunStartedAt": "2026-10-22T02:00:00.000Z",
	}))
	forceDeadline(call2.ID, false)
	done2 := waitCallTerminal(t, env, call2.ID, "timeout_progressing 收尾")
	r2 := waitResultJSON(t, done2)
	if r2["outcome"] != "timeout_progressing" {
		t.Fatalf("outcome 应为 timeout_progressing: %v", r2)
	}
	if changes, _ := r2["changes"].(map[string]interface{}); len(changes) == 0 {
		t.Fatalf("progressing 应带变化对比: %v", r2)
	}

	// watch_degraded：观察不可信时不得把「没听到」说成「没动」。
	call3 := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160IdleThread+`","on":"needs_reply"}`)
	forceDeadline(call3.ID, true)
	done3 := waitCallTerminal(t, env, call3.ID, "watch_degraded 收尾")
	r3 := waitResultJSON(t, done3)
	if r3["outcome"] != "watch_degraded" {
		t.Fatalf("outcome 应为 watch_degraded: %v", r3)
	}

	// thread_gone：可信面上线程被移除，立即收尾。
	call4 := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160RunningThread+`","on":"idle"}`)
	// 等归档面同步完成（thread_gone 断言要求归档面可信，G1）。
	waitForBot(t, "archive ready", 5*time.Second, func() bool {
		v, err := env.bots.threads.PeekShell(userID, envID)
		return err == nil && v != nil && v.Snaps != nil && v.Snaps.ArchiveErr == nil
	})
	fake.pushThreadRemoval(t, int160RunningThread)
	done4 := waitCallTerminal(t, env, call4.ID, "thread_gone 收尾")
	r4 := waitResultJSON(t, done4)
	if r4["outcome"] != "thread_gone" {
		t.Fatalf("outcome 应为 thread_gone: %v", r4)
	}
}

// ---------- 7. 取消：cancel_requested_at → cancelled ----------

func TestBotInt160WaitCancel(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitcancel")
	bot := env.createBot(t, userID)
	_, envID := pairWaitT3(t, env, userID)

	call := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160RunningThread+`","on":"idle"}`)
	fresh, _ := env.bots.calls.FindByID(call.ID)
	if _, ok := env.bots.cancelBotCall(fresh); !ok {
		t.Fatalf("running 的 wait 应可取消")
	}
	// 取消只记标记：evaluator 落唯一终态。
	done := waitCallTerminal(t, env, call.ID, "cancelled 收尾")
	if done.Status != model.BotCallStatusCancelled {
		t.Fatalf("应落 cancelled: %+v", done.Status)
	}
	if done.EffectMayHaveOccurred {
		t.Fatalf("wait 无副作用，取消不得标 effect: %+v", done)
	}
	result := waitResultJSON(t, done)
	if result["outcome"] != "cancelled" {
		t.Fatalf("outcome 应为 cancelled: %v", result["outcome"])
	}
	// 唤醒消息（平台后台结果）带 wait_result。
	var msgs []model.BotMessage
	if err := env.db.Where("bot_id = ? AND call_id = ? AND kind = ?",
		bot.ID, call.ID, model.BotMessageKindBackgroundResult).Find(&msgs).Error; err != nil {
		t.Fatalf("list background results: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("应恰好一条后台结果，实得 %d", len(msgs))
	}
	var content botBackgroundResultContent
	if err := json.Unmarshal([]byte(msgs[0].ContentJSON), &content); err != nil {
		t.Fatalf("decode background result: %v", err)
	}
	if len(content.WaitResult) == 0 || !strings.Contains(string(content.WaitResult), "cancelled") {
		t.Fatalf("后台结果应带 wait_result: %s", msgs[0].ContentJSON)
	}
}

// ---------- 8. 重启恢复：server_restarted 且 effect=false ----------

func TestBotInt160WaitRecoveryServerRestarted(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitrecover")
	bot := env.createBot(t, userID)
	run, req := seedWaitRun(t, env, bot.ID, false)
	now := time.Now().UTC()
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: req.ID,
		ProviderToolCallID: "prov-wait-rec", ToolName: "fs_thread", Action: "wait",
		ArgumentsJSON: `{"action":"wait","env_id":"e1","thread_id":"t1"}`,
		ArgumentsHash: "x", ExecutionMode: string(model.BotExecutionAsync),
		Status: model.BotCallStatusRunning, StartedAt: &now,
		ExecutionContextJSON: "{}", CreatedAt: now,
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("seed wait call: %v", err)
	}
	// 回执已存在（异步受理时追加），恢复只补终态。
	receipt := map[string]interface{}{"accepted": true, "call_id": call.ID, "status": "running"}
	rawReceipt, _ := json.Marshal(receipt)
	msg := &model.BotMessage{
		ID: uuid.NewString(), BotID: bot.ID, Seq: 1,
		RunID: &run.ID, RequestID: &req.ID, CallID: &call.ID,
		Kind: model.BotMessageKindToolReply,
		ContentJSON: string(mustJSON(botToolReplyContent{
			CallID: call.ID, ProviderToolCallID: call.ProviderToolCallID,
			ToolName: "fs_thread", Action: "wait",
			Status: "running", Content: string(rawReceipt),
		})),
		DedupKey: strPtrBot("receipt:" + call.ID), CreatedAt: now,
	}
	if err := env.db.Create(msg).Error; err != nil {
		t.Fatalf("seed receipt: %v", err)
	}
	// 直插的回执占了 seq 1：同步推进账本水位，恢复流程的后续消息才拿得到号。
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("next_message_seq", 2).Error; err != nil {
		t.Fatalf("bump message seq: %v", err)
	}

	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	done, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("reload call: %v", err)
	}
	if done.Status != model.BotCallStatusFailed {
		t.Fatalf("恢复应置 failed: %+v", done.Status)
	}
	if done.EffectMayHaveOccurred {
		t.Fatalf("wait 无副作用，恢复不得标 effect: %+v", done)
	}
	result := waitResultJSON(t, done)
	if result["outcome"] != "server_restarted" {
		t.Fatalf("outcome 应为 server_restarted: %v", result["outcome"])
	}
	var bg []model.BotMessage
	if err := env.db.Where("bot_id = ? AND call_id = ? AND kind = ?",
		bot.ID, call.ID, model.BotMessageKindBackgroundResult).Find(&bg).Error; err != nil {
		t.Fatalf("list background: %v", err)
	}
	if len(bg) != 1 || !strings.Contains(bg[0].ContentJSON, "wait_result") {
		t.Fatalf("恢复后台结果应带 wait_result: %+v", bg)
	}
}

// ---------- 9. 防循环限额与重复挂 ----------

func TestBotInt160WaitLimits(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitlimits")
	bot := env.createBot(t, userID)
	_, envID := pairWaitT3(t, env, userID)

	wait := func(thread string) string {
		return `{"action":"wait","env_id":"` + envID + `","thread_id":"` + thread + `"}`
	}
	// 同线程重复挂：立即以 succeeded 落终态，结果指向已有调用。
	first := acceptWaitCall(t, env, bot.ID, false, wait(int160RunningThread))
	dup := acceptWaitCall(t, env, bot.ID, false, wait(int160RunningThread))
	dupFresh, err := env.bots.calls.FindByID(dup.ID)
	if err != nil {
		t.Fatalf("reload dup: %v", err)
	}
	if dupFresh.Status != model.BotCallStatusSucceeded {
		t.Fatalf("重复挂应立即 succeeded: %+v", dupFresh.Status)
	}
	dupResult := waitResultJSON(t, dupFresh)
	if dupResult["already_waiting"] != true || dupResult["waiting_call_id"] != first.ID {
		t.Fatalf("重复挂结果应指向已有调用: %v", dupResult)
	}

	// 每机器人同时 3 个：first + 两个新线程到达上限，第四个被拒。
	second := acceptWaitCall(t, env, bot.ID, false, wait(int160IdleThread))
	third := acceptWaitCall(t, env, bot.ID, false, `{"action":"wait","env_id":"`+envID+`","thread_id":"th-x"}`)
	if second.Status != model.BotCallStatusRunning || third.Status != model.BotCallStatusRunning {
		t.Fatalf("前 3 个 wait 应受理: %s %s", second.Status, third.Status)
	}
	fourth := acceptWaitCall(t, env, bot.ID, false, `{"action":"wait","env_id":"`+envID+`","thread_id":"th-y"}`)
	if fourth.Status != model.BotCallStatusFailed || fourth.ErrorCode == nil || *fourth.ErrorCode != "e40001" {
		t.Fatalf("第 4 个 wait 应被拒绝（40001）: %+v %v", fourth.Status, fourth.ErrorCode)
	}
	if fourth.ErrorMessage == nil || !strings.Contains(*fourth.ErrorMessage, "已达上限") {
		t.Fatalf("拒绝应提示已达上限: %v", fourth.ErrorMessage)
	}

	// 同一线程 1 小时内 6 次：补 6 条已终态调用行后拒绝新挂。
	for i := 0; i < botWaitMaxPerThreadHour; i++ {
		now := time.Now().UTC()
		_, req := seedWaitRun(t, env, bot.ID, false)
		row := &model.BotToolCall{
			ID: uuid.NewString(), BotID: bot.ID, RequestID: req.ID,
			ProviderToolCallID: "prov-wait-h" + uuid.NewString()[:4],
			ToolName:           "fs_thread", Action: "wait",
			ArgumentsJSON: wait(int160IdleThread), ArgumentsHash: "x",
			ExecutionMode: string(model.BotExecutionAsync),
			Status:        model.BotCallStatusSucceeded, FinishedAt: &now,
			ExecutionContextJSON: "{}", CreatedAt: now,
		}
		if err := env.db.Create(row).Error; err != nil {
			t.Fatalf("seed hourly wait: %v", err)
		}
	}
	// 先放开并发上限（取消两个 running），让线程小时计数成为命中原因。
	freshSecond, _ := env.bots.calls.FindByID(second.ID)
	freshThird, _ := env.bots.calls.FindByID(third.ID)
	env.bots.cancelBotCall(freshSecond)
	env.bots.cancelBotCall(freshThird)
	driveWaitEvaluation(t, env, func() bool { return true })
	sixth := acceptWaitCall(t, env, bot.ID, false, wait(int160IdleThread))
	if sixth.Status != model.BotCallStatusFailed || sixth.ErrorCode == nil || *sixth.ErrorCode != "e40001" {
		t.Fatalf("超小时限额应拒绝（40001）: %+v %v", sixth.Status, sixth.ErrorCode)
	}
}

// ---------- 10. 定时任务会话：timeout_minutes ≤ 15 ----------

func TestBotInt160WaitScheduleSessionCap(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitsched")
	bot := env.createBot(t, userID)
	_, envID := pairWaitT3(t, env, userID)

	over := acceptWaitCall(t, env, bot.ID, true,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160RunningThread+`","timeout_minutes":20}`)
	if over.Status != model.BotCallStatusFailed || over.ErrorCode == nil || *over.ErrorCode != "e40001" {
		t.Fatalf("定时任务会话内 20 分钟应被拒绝（40001）: %+v %v", over.Status, over.ErrorCode)
	}
	if over.ErrorMessage == nil || !strings.Contains(*over.ErrorMessage, "15") {
		t.Fatalf("拒绝应说明 15 分钟上限: %v", over.ErrorMessage)
	}

	okCall := acceptWaitCall(t, env, bot.ID, true,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160RunningThread+`","timeout_minutes":15}`)
	if okCall.Status != model.BotCallStatusRunning {
		t.Fatalf("15 分钟应受理: %+v", okCall.Status)
	}
	// 收尾清理。
	fresh, _ := env.bots.calls.FindByID(okCall.ID)
	env.bots.cancelBotCall(fresh)
	driveWaitEvaluation(t, env, func() bool { return true })
}

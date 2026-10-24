package service

// INT-160 fs_thread/wait 测试：受理即 running 不进 worker、idle 稳定窗防抖、
// needs_reply/error 立即触发、启动宽限 not_started、超时分类（unchanged /
// progressing / degraded / thread_gone）、取消与重启分支、防循环限额与重复挂、
// 定时任务会话 15 分钟上限。工具集 v13 与冻结基线（v1–v12 hash 不动）。

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// botToolsetV13FrozenHash 是 INT-160 发布的 v13 wire 字节基线：wait 动作与
// on/timeout_minutes 参数只进本版（v12 是 INT-159 的 launch 执行配置）；
// 之后任何改动都必须先递增版本。
const botToolsetV13FrozenHash = "a7e747528c0039844a1808eafd436918673f22d3be8d55a81490009317f69e47"

func shrinkWaitTimers(env *botTestEnv) {
	env.bots.waitCfg = botWaitEvalConfig{
		IdleStable:       80 * time.Millisecond,
		StartGrace:       300 * time.Millisecond,
		DetailBudget:     3 * time.Second,
		ScanDetailBudget: 3 * time.Second,
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

// ---------- 1. 工具集 v13：wait 进活跃面，v1–v12 字节冻结 ----------

func TestBotInt160ToolsetV13(t *testing.T) {
	v12 := botToolsetVersionOf(12)
	v13 := botToolsetVersionOf(13)
	if len(v13.schemas) != len(v12.schemas) {
		t.Fatalf("v13 不新增工具: %d vs %d", len(v13.schemas), len(v12.schemas))
	}
	if v13.hash == v12.hash {
		t.Fatalf("v13 与 v12 的 tools_hash 不应相同")
	}
	if v13.hash != botToolsetV13FrozenHash {
		t.Fatalf("v13 tools_hash 偏离基线: got %s want %s", v13.hash, botToolsetV13FrozenHash)
	}
	// v1–v12 各冻结基线原样通过：v13 的演进不得回写任何已发布版本。
	for _, tc := range []struct {
		version int64
		want    string
	}{
		{1, botToolsetV1FrozenHash}, {2, botToolsetV2FrozenHash}, {3, botToolsetV3FrozenHash},
		{4, botToolsetV4FrozenHash}, {5, botToolsetV5FrozenHash}, {6, botToolsetV6FrozenHash},
		{7, botToolsetV7FrozenHash}, {8, botToolsetV8FrozenHash}, {9, botToolsetV9FrozenHash},
		{10, botToolsetV10FrozenHash}, {11, botToolsetV11FrozenHash}, {12, botToolsetV12FrozenHash},
	} {
		if got := botToolsetVersionOf(tc.version).hash; got != tc.want {
			t.Fatalf("v%d tools_hash 偏离冻结基线: got %s want %s", tc.version, got, tc.want)
		}
	}
	// v9–v11 的 fs_thread 冻结在 botThreadArgsV9Frozen，v12 冻结在
	// botThreadArgsV12Frozen；v13 用活跃结构体。
	for _, version := range []int64{9, 10, 11} {
		thread := botToolsetVersionOf(version).byName["fs_thread"]
		if thread.ArgsType.Name() != "botThreadArgsV9Frozen" {
			t.Fatalf("v%d fs_thread 应冻结在 botThreadArgsV9Frozen，实得 %s", version, thread.ArgsType.Name())
		}
	}
	if th := v12.byName["fs_thread"]; th.ArgsType.Name() != "botThreadArgsV12Frozen" {
		t.Fatalf("v13 fs_thread 应冻结在 botThreadArgsV12Frozen，实得 %s", th.ArgsType.Name())
	}
	if _, ok := v12.byAction["fs_thread/wait"]; ok {
		t.Fatalf("v13 不得含 fs_thread/wait（wait 只进 v13）")
	}
	thread := v13.byName["fs_thread"]
	if thread.ArgsType.Name() != "botThreadArgs" {
		t.Fatalf("v13 fs_thread 应使用活跃参数结构体，实得 %s", thread.ArgsType.Name())
	}
	if len(thread.Actions) != 12 {
		t.Fatalf("v13 fs_thread 应有 12 个动作，实得 %d", len(thread.Actions))
	}
	waitDef := v13.byAction["fs_thread/wait"]
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
	if _, err := botPrepareToolCall(v13, "fs_thread", `{"action":"wait","env_id":"e1"}`); err == nil {
		t.Fatalf("wait 缺 thread_id 应被拒绝")
	}
	if _, err := botPrepareToolCall(v13, "fs_thread", `{"action":"wait","env_id":"e1","thread_id":"t1","on":"idle,pr"}`); err == nil {
		t.Fatalf("wait 的 on 不接受 pr")
	}
	if _, err := botPrepareToolCall(v13, "fs_thread", `{"action":"wait","env_id":"e1","thread_id":"t1","timeout_minutes":31}`); err == nil {
		t.Fatalf("wait 的 timeout_minutes 越界应被拒绝")
	}
	if _, err := botPrepareToolCall(v13, "fs_thread", `{"action":"wait","env_id":"e1","thread_id":"t1","text":"x"}`); err == nil {
		t.Fatalf("wait 不接受 text")
	}
	prepared, err := botPrepareToolCall(v13, "fs_thread",
		`{"action":"wait","env_id":"e1","thread_id":"t1","on":"needs_reply","timeout_minutes":30}`)
	if err != nil {
		t.Fatalf("wait 应被受理: %v", err)
	}
	if prepared.Mode != model.BotExecutionAsync || prepared.RequiresConfirmation {
		t.Fatalf("wait 受理应为异步免确认: %+v", prepared)
	}
	// 缺省参数（on/timeout_minutes 省略）合法。
	if _, err := botPrepareToolCall(v13, "fs_thread", `{"action":"wait","env_id":"e1","thread_id":"t1"}`); err != nil {
		t.Fatalf("wait 缺省参数应被受理: %v", err)
	}
	// 工具与动作描述写明 wait 语义，且不含 watch/watching 字样（PR 监听字段
	// watching 是既有口径，只约束 wait 自己的描述与帮助条目）。
	for _, want := range []string{"wait", "outcome", "running"} {
		if !strings.Contains(thread.Description, want) && !strings.Contains(waitDef.Description, want) {
			t.Fatalf("v13 fs_thread 描述应含 %q", want)
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

// 挂上时线程靠 activityRunStatus/后台任务投影 running（根 run 已结束、无
// activeRunId）：扫描前转 idle 应按 idle 触发收尾，不得误报 not_started
// （复审 P2：挂上/收编的 running 判定须与评估器同源）。
func TestBotInt160WaitAttachBackgroundRunning(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitbgrun")
	bot := env.createBot(t, userID)

	const bgThread = "th-wait-bg"
	threads := []any{
		shellThread(bgThread, "后台在跑", "completed", map[string]any{
			"activityRunStatus":  "running",
			"latestRunId":        "run-bg1",
			"latestRunStartedAt": "2026-10-20T00:00:00.000Z",
		}),
	}
	fake, envID := pairBotT3WithSnapshot(t, env, userID, "后台等待机",
		shellSnapshotFixture([]any{map[string]any{"id": "p-1", "title": "P"}}, threads))
	fake.mu.Lock()
	fake.projections[bgThread] = threadProjection(bgThread, []any{}, nil)
	fake.mu.Unlock()
	shrinkWaitTimers(env)
	waitHubReady(t, env, userID, envID)

	call := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+bgThread+`","on":"idle"}`)

	// 后台活动在下轮扫描前结束：推送去掉 activityRunStatus 的 idle 投影。
	fake.pushThreadDelta(t, shellThread(bgThread, "后台在跑", "completed", map[string]any{
		"latestRunId":        "run-bg1",
		"latestRunStartedAt": "2026-10-20T00:00:00.000Z",
	}))

	done := waitCallTerminal(t, env, call.ID, "后台 running 转 idle 触发")
	if done.Status != model.BotCallStatusSucceeded {
		t.Fatalf("应落 succeeded: %+v", done)
	}
	result := waitResultJSON(t, done)
	if result["outcome"] != "triggered" {
		t.Fatalf("不应是 not_started，outcome=%v", result["outcome"])
	}
	matched, _ := result["matched"].([]interface{})
	if len(matched) != 1 || matched[0] != "idle" {
		t.Fatalf("命中项应为 idle: %v", matched)
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
	// 取消竞速落到 cancelRaceCols 时结果是覆写体——必须重建
	// waiting_for/attached_at，不得退化成最小体丢上下文（复审 Note）。
	if wf, _ := result["waiting_for"].([]interface{}); len(wf) == 0 {
		t.Fatalf("cancelled 结果应带 waiting_for: %v", result)
	}
	if result["attached_at"] == nil {
		t.Fatalf("cancelled 结果应带 attached_at: %v", result)
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
	// 终态调用的回执不能误报 pending_confirmation（复审 P2）：撞重行直落
	// succeeded，回执 status 与 already_waiting 语义一致。
	var dupReplies []model.BotMessage
	if err := env.db.Where("bot_id = ? AND call_id = ? AND kind = ?",
		bot.ID, dup.ID, model.BotMessageKindToolReply).Find(&dupReplies).Error; err != nil {
		t.Fatalf("list dup replies: %v", err)
	}
	if len(dupReplies) != 1 {
		t.Fatalf("重复挂应恰好一条回执，实得 %d", len(dupReplies))
	}
	var dupReceipt botToolReplyContent
	if err := json.Unmarshal([]byte(dupReplies[0].ContentJSON), &dupReceipt); err != nil {
		t.Fatalf("decode dup receipt: %v", err)
	}
	if dupReceipt.Status != string(model.BotCallStatusSucceeded) {
		t.Fatalf("终态重复挂的回执应为 succeeded，实得 %s", dupReceipt.Status)
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

// ---------- 11. 复审第 4 轮回归 ----------

// Codex P1：同一线程 1 小时额度先于重复判定——已存在 running wait 且额度
// 满时必须拒绝；先返 already_waiting 会让「对同一线程反复挂 wait」绕过防
// 循环上限（每条 already_waiting 回执都会唤醒模型）。
func TestBotInt160WaitDupAtHourlyCapRejected(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitdupcap")
	bot := env.createBot(t, userID)
	_, envID := pairWaitT3(t, env, userID)

	wait := `{"action":"wait","env_id":"` + envID + `","thread_id":"` + int160RunningThread + `"}`
	first := acceptWaitCall(t, env, bot.ID, false, wait)
	if first.Status != model.BotCallStatusRunning {
		t.Fatalf("首个 wait 应在跑: %s", first.Status)
	}
	// 额度窗口内已有 6 条该线程的终态 wait 行。
	for i := 0; i < botWaitMaxPerThreadHour; i++ {
		now := time.Now().UTC()
		_, req := seedWaitRun(t, env, bot.ID, false)
		row := &model.BotToolCall{
			ID: uuid.NewString(), BotID: bot.ID, RequestID: req.ID,
			ProviderToolCallID: "prov-wh-" + uuid.NewString()[:4],
			ToolName:           "fs_thread", Action: "wait",
			ArgumentsJSON: wait, ArgumentsHash: "x",
			ExecutionMode: string(model.BotExecutionAsync),
			Status:        model.BotCallStatusSucceeded, FinishedAt: &now,
			ExecutionContextJSON: "{}", CreatedAt: now,
		}
		if err := env.db.Create(row).Error; err != nil {
			t.Fatalf("seed hourly wait: %v", err)
		}
	}
	// 重复挂存在（first 在跑）但额度已满：必须拒而不是 already_waiting。
	dup := acceptWaitCall(t, env, bot.ID, false, wait)
	if dup.Status != model.BotCallStatusFailed || dup.ErrorCode == nil || *dup.ErrorCode != "e40001" {
		t.Fatalf("额度满的重复挂应拒绝（40001）而非 already_waiting: %+v %v", dup.Status, dup.ErrorCode)
	}
	// 事务内复核同序：额度满时同样先拦。
	verdict := env.bots.recheckWaitAcceptance(env.db, bot.ID,
		botThreadArgs{EnvID: envID, ThreadID: int160RunningThread})
	if verdict.limErr == nil || verdict.dupCallID != "" {
		t.Fatalf("复核应先报额度而非重复: %+v", verdict)
	}
}

// Codex P2：视图读得到但线程不在可信面上（归档面失败不可信）时，启动宽限
// 到期也应收尾 watch_degraded，不能占着槽位拖到超时。
func TestBotInt160WaitUnobservableDegradedAtGrace(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitdegrade")
	bot := env.createBot(t, userID)

	// 归档面失败（G1）：活跃面 Ready，归档不可信——不在快照里的线程
	// known=false 且不能断言不存在。
	if err := env.db.AutoMigrate(&model.T3Environment{}); err != nil {
		t.Fatalf("migrate t3 env: %v", err)
	}
	fake := newFakeT3Environment(t)
	fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-x", "存在", "idle", nil)})
	fake.archiveFail = true
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	fake.baseURL = server.URL

	hub := NewT3ShellHub(zap.NewNop())
	t.Cleanup(hub.Close)
	repo := repository.NewT3EnvironmentRepository(env.db)
	svc := NewT3ThreadsService(repo, env.cfg, zap.NewNop(), hub)
	svc.budgets = t3RemoteBudgets{Round: 5 * time.Second, PerEnvironment: 2 * time.Second,
		ThreadDetail: 5 * time.Second, ItemDetail: 5 * time.Second, ShellFirstSync: 2 * time.Second}
	env.bots.threads = svc
	envSvc := NewT3EnvironmentService(repo, env.cfg, zap.NewNop(), hub)
	created, err := envSvc.Create(context.Background(), userID,
		&CreateT3EnvironmentRequest{Name: "降级机", PairingUrl: fake.pairingURL(fake.baseURL)})
	if err != nil {
		t.Fatalf("pair: %v", err)
	}
	envID := created.Id
	shrinkWaitTimers(env)
	waitHubReady(t, env, userID, envID)
	// 归档面确实不可信后再挂（G1 状态机事实）。
	waitForBot(t, "archive degraded", 5*time.Second, func() bool {
		v, err := env.bots.threads.PeekShell(userID, envID)
		return err == nil && v != nil && v.Snaps != nil && v.Snaps.ArchiveErr != nil
	})

	call := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"th-missing"}`)
	done := waitCallTerminal(t, env, call.ID, "宽限期 watch_degraded")
	result := waitResultJSON(t, done)
	if result["outcome"] != "watch_degraded" {
		t.Fatalf("观察不可信时宽限收尾应 watch_degraded: %v", result)
	}
}

// bot P2：挂上时线程已在等待应答/已出错是「已有结果」的触发态——started
// 立即成立，条件当轮评估，不得拖到启动宽限误报 not_started。
func TestBotInt160WaitAttachAlreadyWaitingOrFailed(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitprestate")
	bot := env.createBot(t, userID)

	const waitingThread = "th-wait-pre"
	const failedThread = "th-fail-pre"
	threads := []any{
		shellThread(waitingThread, "等审批", "completed", map[string]any{
			"pendingRuntimeRequest": map[string]any{
				"id": "req-pre", "kind": "approval", "createdAt": "2026-10-20T00:00:00.000Z",
			},
		}),
		shellThread(failedThread, "已出错", "failed", nil),
	}
	fake, envID := pairBotT3WithSnapshot(t, env, userID, "预态机",
		shellSnapshotFixture([]any{map[string]any{"id": "p-1", "title": "P"}}, threads))
	fake.mu.Lock()
	fake.projections[waitingThread] = threadProjection(waitingThread, []any{}, nil)
	fake.projections[failedThread] = threadProjection(failedThread, []any{}, nil)
	fake.mu.Unlock()
	shrinkWaitTimers(env)
	waitHubReady(t, env, userID, envID)

	call1 := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+waitingThread+`","on":"needs_reply"}`)
	done1 := waitCallTerminal(t, env, call1.ID, "已等待应答应立即触发")
	r1 := waitResultJSON(t, done1)
	if r1["outcome"] != "triggered" {
		t.Fatalf("已处于 needs_reply 的线程挂 wait 应立即触发: %v", r1["outcome"])
	}
	if m, _ := r1["matched"].([]interface{}); len(m) != 1 || m[0] != "needs_reply" {
		t.Fatalf("命中项应为 needs_reply: %v", m)
	}

	call2 := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+failedThread+`","on":"error"}`)
	done2 := waitCallTerminal(t, env, call2.ID, "已出错应立即触发")
	r2 := waitResultJSON(t, done2)
	if r2["outcome"] != "triggered" {
		t.Fatalf("已出错的线程挂 wait 应立即触发: %v", r2["outcome"])
	}
	if m, _ := r2["matched"].([]interface{}); len(m) != 1 || m[0] != "error" {
		t.Fatalf("命中项应为 error: %v", m)
	}
}

// ---------- 第 5 轮复审回归 ----------

// Bot P2：一批 wait 同轮收尾时详情读取共享单轮总预算——不能把 5s 扫描循环
// 拖住 N×DetailBudget。两个线程的投影读取都被掐死时，一轮评估的总耗时必须
// 在共享预算量级，两个 wait 都按「详情失败」如实收尾。
func TestBotInt160WaitScanDetailBudgetShared(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitbudget")
	bot := env.createBot(t, userID)

	th1, th2 := "th-budget-1", "th-budget-2"
	pending := map[string]any{
		"pendingRuntimeRequest": map[string]any{
			"id": "req-b", "kind": "approval", "createdAt": "2026-10-20T00:00:00.000Z",
		},
	}
	fake, envID := pairBotT3WithSnapshot(t, env, userID, "预算机",
		shellSnapshotFixture([]any{map[string]any{"id": "p-1", "title": "P"}},
			[]any{shellThread(th1, "等1", "completed", pending), shellThread(th2, "等2", "completed", pending)}))
	fake.mu.Lock()
	fake.projections[th1] = threadProjection(th1, []any{}, nil)
	fake.projections[th2] = threadProjection(th2, []any{}, nil)
	// 详情读取挂起：两个线程的投影请求都被掐死，只能等调用方 ctx 断开。
	fake.blockProjection = map[string]chan struct{}{th1: make(chan struct{}), th2: make(chan struct{})}
	fake.mu.Unlock()
	env.bots.waitCfg = botWaitEvalConfig{
		IdleStable:       80 * time.Millisecond,
		StartGrace:       300 * time.Millisecond,
		DetailBudget:     5 * time.Second,
		ScanDetailBudget: 200 * time.Millisecond,
	}
	waitHubReady(t, env, userID, envID)

	call1 := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+th1+`","on":"needs_reply"}`)
	call2 := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+th2+`","on":"needs_reply"}`)

	start := time.Now()
	env.bots.evaluateThreadWaits()
	elapsed := time.Since(start)
	// 共享预算 200ms：两条串行详情各等满 DetailBudget 的旧行为是 ~10s，
	// 现在整轮必须在共享预算量级内返回。
	if elapsed > 3*time.Second {
		t.Fatalf("一轮评估被详情读取拖住 %v（共享预算 200ms，预期 <3s）", elapsed)
	}
	for _, id := range []string{call1.ID, call2.ID} {
		c, err := env.bots.calls.FindByID(id)
		if err != nil || !c.Status.IsTerminal() {
			t.Fatalf("wait %s 应已收尾: %+v err=%v", id, c, err)
		}
		r := waitResultJSON(t, c)
		if r["outcome"] != "triggered" {
			t.Fatalf("触发事实不依赖详情读取，应为 triggered: %v", r)
		}
		if _, hasNote := r["note"]; !hasNote {
			t.Fatalf("详情读取失败应在 note 说明: %v", r)
		}
	}
}

// Bot Note：T3 服务未接线（threads=nil）时评估器不得整体罢工——取消要立即
// 落终态，其余 wait 在启动宽限后如实落 watch_degraded，不能晾到进程重启。
func TestBotInt160WaitThreadsUnavailable(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitnothreads")
	bot := env.createBot(t, userID)
	_, envID := pairBotT3WithSnapshot(t, env, userID, "断线机",
		shellSnapshotFixture([]any{map[string]any{"id": "p-1", "title": "P"}},
			[]any{shellThread("th-x", "在跑", "running", map[string]any{"activeRunId": "run-1"})}))
	shrinkWaitTimers(env)
	waitHubReady(t, env, userID, envID)

	callCancel := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"th-x","on":"idle"}`)
	callGrace := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"th-y","on":"idle"}`)
	// 断线：T3 服务整体不可用。
	env.bots.threads = nil
	now := time.Now().UTC()
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", callCancel.ID).
		Update("cancel_requested_at", &now).Error; err != nil {
		t.Fatalf("mark cancel: %v", err)
	}

	done1 := waitCallTerminal(t, env, callCancel.ID, "断线后取消")
	if done1.Status != model.BotCallStatusCancelled {
		t.Fatalf("断线后取消应落 cancelled: %s", done1.Status)
	}
	done2 := waitCallTerminal(t, env, callGrace.ID, "断线后宽限收尾")
	r2 := waitResultJSON(t, done2)
	if r2["outcome"] != "watch_degraded" {
		t.Fatalf("观察不可信时应落 watch_degraded: %v", r2["outcome"])
	}
}

// Bot P2：running wait 每（bot,env,thread）至多一个是硬约束——部分唯一索引
// 兜底应用层判重漏掉的并发窗口，第二条 running 插入必须撞索引失败。
func TestBotInt160WaitRunningUniqueIndex(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitidx")
	bot := env.createBot(t, userID)
	_, req := seedWaitRun(t, env, bot.ID, false)

	newWaitRow := func(threadID string) *model.BotToolCall {
		return &model.BotToolCall{
			ID: uuid.NewString(), BotID: bot.ID, RequestID: req.ID,
			ProviderToolCallID: "prov-" + uuid.NewString()[:8], Ordinal: 1,
			ToolName: "fs_thread", Action: "wait",
			ArgumentsJSON:        `{"action":"wait","env_id":"env-1","thread_id":"` + threadID + `"}`,
			ArgumentsHash:        "h-" + uuid.NewString()[:8],
			ExecutionMode:        string(model.BotExecutionAsync),
			Status:               model.BotCallStatusRunning,
			ExecutionContextJSON: "{}", PolicyJSON: "{}",
			CreatedAt: time.Now().UTC(),
		}
	}
	repo := repository.NewBotToolCallRepository(env.db)
	if err := repo.CreateTx(env.db, newWaitRow("th-dup")); err != nil {
		t.Fatalf("第一条 running wait 应可插入: %v", err)
	}
	err := repo.CreateTx(env.db, newWaitRow("th-dup"))
	if err == nil {
		t.Fatalf("同线程第二条 running wait 必须被唯一索引拒绝")
	}
	if !strings.Contains(err.Error(), "idx_bot_tool_calls_running_wait") &&
		!strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("应报唯一索引冲突: %v", err)
	}
	// 不同线程不受约束。
	if err := repo.CreateTx(env.db, newWaitRow("th-other")); err != nil {
		t.Fatalf("不同线程的 running wait 应可插入: %v", err)
	}
	// 已收尾的行不占唯一位（部分索引只盖 running）。
	terminal := newWaitRow("th-dup")
	terminal.Status = model.BotCallStatusSucceeded
	now := time.Now().UTC()
	terminal.FinishedAt = &now
	if err := repo.CreateTx(env.db, terminal); err != nil {
		t.Fatalf("已收尾的同线程 wait 应可插入: %v", err)
	}
}

// Bot P2：两个并发 commitAssistantTurn 同挂一线程的 wait——commitMu 串行化
// 受理评估到提交，后到者事务内复核必然看见先到的已提交行，落 already_waiting
// 而不是两条 running。
func TestBotInt160WaitConcurrentCommitDeduped(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitconc")
	bot := env.createBot(t, userID)
	_, envID := pairBotT3WithSnapshot(t, env, userID, "并发机",
		shellSnapshotFixture([]any{map[string]any{"id": "p-1", "title": "P"}},
			[]any{shellThread("th-c", "在跑", "running", map[string]any{"activeRunId": "run-1"})}))
	shrinkWaitTimers(env)
	waitHubReady(t, env, userID, envID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	args := `{"action":"wait","env_id":"` + envID + `","thread_id":"th-c","on":"idle"}`

	var wg sync.WaitGroup
	results := make([]*model.BotToolCall, 2)
	errsOut := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run, req := seedWaitRun(t, env, bot.ID, false)
			reply := &botModelReply{ToolCalls: []botModelToolCall{{
				ID: "prov-conc-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args,
			}}}
			calls, cerr := env.bots.commitAssistantTurn(botRow, run, req, reply, "msg-"+uuid.NewString())
			if cerr == nil && len(calls) == 1 {
				results[i] = calls[0]
			} else {
				errsOut[i] = cerr
			}
		}(i)
	}
	wg.Wait()
	// 无锁时两个事务可同时通过判重各落一条 running；有锁后必然一挂一撞重。
	// 后到者需等 post-commit 的 already_waiting 终态落定再断言。
	time.Sleep(100 * time.Millisecond)
	running, dup := 0, 0
	for i, c := range results {
		if c == nil {
			t.Fatalf("并发提交 %d 失败: %v", i, errsOut[i])
		}
		fresh, ferr := env.bots.calls.FindByID(c.ID)
		if ferr != nil {
			t.Fatalf("reload call: %v", ferr)
		}
		switch {
		case fresh.Status == model.BotCallStatusRunning:
			running++
		case fresh.Status == model.BotCallStatusSucceeded:
			r := waitResultJSON(t, fresh)
			if r["outcome"] == "already_waiting" {
				dup++
			}
		}
	}
	if running != 1 || dup != 1 {
		t.Fatalf("并发挂 wait 应恰为 1 running + 1 already_waiting，实得 running=%d dup=%d", running, dup)
	}
}

// Codex P2：同响应 send「继续」+ wait 指向同一线程——wait 的基线必须在
// 兄弟写派发之前采定，否则快速完成的写会让 wait 采到发送后现场，宽限后
// 误报 not_started。
func TestBotInt160WaitBaselineBeforeSiblingSend(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "waitsibling")
	bot := env.createBot(t, userID)
	fake, envID := pairPolicyT3(t, env, userID)
	shrinkWaitTimers(env)
	run, _ := seedWaitRun(t, env, bot.ID, false)

	// send 的 dispatchCommand 一到 fake 就把线程推成 running(run-new)：
	// 模拟「继续」送达后线程立即开跑。同时记录「RPC 到达时 wait 是否已
	// 注册」——修复后 wait 先于兄弟写派发，该事实必须恒真。
	var sentMu sync.Mutex
	var waitCallID string
	sent, waitWasAttached := false, false
	fake.onDispatch = func(payload json.RawMessage) {
		var p struct {
			Type     string `json:"type"`
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(payload, &p) != nil || p.ThreadID != int160PolicyIdleThread {
			return
		}
		sentMu.Lock()
		sent = true
		env.bots.waitMu.Lock()
		_, waitWasAttached = env.bots.waits[waitCallID]
		env.bots.waitMu.Unlock()
		sentMu.Unlock()
		fake.pushThreadDelta(t, shellThread(int160PolicyIdleThread, "待续", "running", map[string]any{
			"activeRunId":        "run-new",
			"latestRunId":        "run-new",
			"latestRunStartedAt": "2026-10-24T00:00:00.000Z",
		}))
	}
	argsSend := `{"action":"send","env_id":"` + envID + `","thread_id":"` + int160PolicyIdleThread + `","text":"请继续"}`
	argsWait := `{"action":"wait","env_id":"` + envID + `","thread_id":"` + int160PolicyIdleThread + `","on":"idle"}`
	calls := commitBotReply(t, env, bot.ID, run, "", []botModelToolCall{
		{ID: "prov-sibling-send-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: argsSend},
		{ID: "prov-sibling-wait-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: argsWait},
	})
	if len(calls) != 2 {
		t.Fatalf("应受理 2 个调用，实得 %d", len(calls))
	}
	waitCall := calls[1]
	sentMu.Lock()
	waitCallID = waitCall.ID
	sentMu.Unlock()
	if waitCall.Status != model.BotCallStatusRunning {
		t.Fatalf("wait 应受理即 running: %s", waitCall.Status)
	}

	// wait 挂在 send 派发之前：基线的 latestRunID 必须是发送前的 run-old，
	// 不能采到 send 生效后的 run-new（否则无人再变 run 身份，wait 会误报
	// not_started）。
	waitForBot(t, "wait attach", 5*time.Second, func() bool {
		env.bots.waitMu.Lock()
		defer env.bots.waitMu.Unlock()
		_, ok := env.bots.waits[waitCall.ID]
		return ok
	})
	waitForBot(t, "send dispatched", 5*time.Second, func() bool {
		sentMu.Lock()
		defer sentMu.Unlock()
		return sent
	})
	env.bots.waitMu.Lock()
	base := env.bots.waits[waitCall.ID].baseline.latestRunID
	env.bots.waitMu.Unlock()
	if base != "run-old" {
		t.Fatalf("wait 基线应采到发送前现场 run-old，实得 %q", base)
	}
	sentMu.Lock()
	attached := waitWasAttached
	sentMu.Unlock()
	if !attached {
		t.Fatalf("send 的 dispatch RPC 到达时 wait 必须已完成注册")
	}
}

// ---------- 15. 复审第 7 轮：快照滞后于线程创建不误判 thread_gone ----------

// TestBotInt160WaitStaleSnapshotGrace 验证 baseline 还没见过线程时，可信视图
// 里的缺失先按启动宽限挂起（notReadyTicks 累计），等宽限耗尽才落
// thread_gone——快照滞后于 launch/send 是常态，首轮评估不能直接判死。
func TestBotInt160WaitStaleSnapshotGrace(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160wait15")
	fake, envID := pairWaitT3(t, env, userID)
	bot := env.createBot(t, userID)
	shrinkWaitTimers(env)

	// 刚 launch 的线程还没进快照：可信视图（归档就绪）里查无此线程。
	waitForBot(t, "archive ready", 5*time.Second, func() bool {
		v, err := env.bots.threads.PeekShell(userID, envID)
		return err == nil && v != nil && v.Snaps != nil && v.Snaps.ArchiveErr == nil
	})
	call := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"th-launching","on":"idle"}`)

	// 宽限内：缺失只计 notReadyTicks，不落 thread_gone。
	env.bots.evaluateThreadWaits()
	c, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("find call: %v", err)
	}
	if c.Status.IsTerminal() {
		t.Fatalf("宽限内缺失不应落终态，实得 %s", c.Status)
	}

	// 宽限耗尽仍查无此线程才判 thread_gone；中途线程出现则正常挂起。
	fake.pushThreadDelta(t, shellThread("th-launching", "新线程", "running",
		map[string]any{"activeRunId": "run-n"}))
	driveWaitEvaluation(t, env, func() bool {
		env.bots.waitMu.Lock()
		defer env.bots.waitMu.Unlock()
		e := env.bots.waits[call.ID]
		return e != nil && e.baseline.known && e.started
	})
}

// ---------- 16. 复审第 9 轮：on 类型错误、可信缺席轮次、去重行恢复 ----------

// TestBotInt160WaitOnTypeError 验证 on 传非字符串（数组/数字等）显式报错，
// 不静默放大为全量条件——与 timeout_minutes 同口径。
func TestBotInt160WaitOnTypeError(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160wait16")
	_, envID := pairWaitT3(t, env, userID)
	bot := env.createBot(t, userID)

	for _, bad := range []string{
		`{"action":"wait","env_id":"` + envID + `","thread_id":"t","on":["idle"]}`,
		`{"action":"wait","env_id":"` + envID + `","thread_id":"t","on":123}`,
		`{"action":"wait","env_id":"` + envID + `","thread_id":"t","on":true}`,
	} {
		call := acceptWaitCall(t, env, bot.ID, false, bad)
		if call.Status != model.BotCallStatusFailed || call.ErrorCode == nil ||
			*call.ErrorCode != "e40001" {
			t.Fatalf("on 类型错误应落 invalid_params: %+v", call)
		}
	}
}

// TestBotInt160WaitTrustedAbsenceNotNotReady 验证 baseline.known=false 的 wait
// 在可信视图里查无此线程（宽限内）：缺席是可信观察，不计 notReadyTicks——
// 否则快照滞后几十秒就把随后的收尾降格成 watch_degraded。
func TestBotInt160WaitTrustedAbsenceNotNotReady(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160wait16b")
	_, envID := pairWaitT3(t, env, userID)
	bot := env.createBot(t, userID)
	shrinkWaitTimers(env)

	waitForBot(t, "archive ready", 5*time.Second, func() bool {
		v, err := env.bots.threads.PeekShell(userID, envID)
		return err == nil && v != nil && v.Snaps != nil && v.Snaps.ArchiveErr == nil
	})
	call := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"th-launching","on":"idle"}`)

	env.bots.evaluateThreadWaits()
	env.bots.evaluateThreadWaits()
	env.bots.waitMu.Lock()
	e := env.bots.waits[call.ID]
	var ticks int
	if e != nil {
		ticks = e.notReadyTicks
	}
	env.bots.waitMu.Unlock()
	if ticks != 0 {
		t.Fatalf("可信缺席不应计不可信轮次，实得 notReadyTicks=%d", ticks)
	}
	c, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("find call: %v", err)
	}
	if c.Status.IsTerminal() {
		t.Fatalf("宽限内可信缺席不落终态: %s", c.Status)
	}
}

// TestBotInt160WaitRecoveryDedupedRow 验证 EnsureBotSchema 去重留下的
// failed+unfinished 行（status=failed、finished_at NULL）能被
// RecoverOnStartup 捡起并按 server_restarted 正常收尾——补后台终态消息。
func TestBotInt160WaitRecoveryDedupedRow(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160wait16c")
	bot := env.createBot(t, userID)
	run, req := seedWaitRun(t, env, bot.ID, false)
	now := time.Now().UTC()
	// 去重后置形态：status=failed、finished_at 仍 NULL。
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: req.ID,
		ProviderToolCallID: "prov-wait-dedup", ToolName: "fs_thread", Action: "wait",
		ArgumentsJSON: `{"action":"wait","env_id":"e1","thread_id":"t1"}`,
		ArgumentsHash: "x", ExecutionMode: string(model.BotExecutionAsync),
		Status: model.BotCallStatusFailed, StartedAt: &now,
		ErrorCode:            strPtrBot("server_restarted"),
		ExecutionContextJSON: "{}", CreatedAt: now,
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("seed deduped wait: %v", err)
	}
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
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("next_message_seq", 2).Error; err != nil {
		t.Fatalf("bump message seq: %v", err)
	}

	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recover: %v", err)
	}
	done, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if done.FinishedAt == nil {
		t.Fatalf("去重行应被恢复收尾（finished_at 落定）: %+v", done)
	}
	var bg []model.BotMessage
	if err := env.db.Where("bot_id = ? AND call_id = ? AND kind = ?",
		bot.ID, call.ID, model.BotMessageKindBackgroundResult).Find(&bg).Error; err != nil {
		t.Fatalf("list background: %v", err)
	}
	if len(bg) != 1 || !strings.Contains(bg[0].ContentJSON, "wait_result") {
		t.Fatalf("去重行恢复应补后台终态消息: %+v", bg)
	}
}

// ---------- 17. 复审第 11 轮：收编解析失败收尾 + 受理失败清 started_at ----------

// TestBotInt160WaitAdoptInvalidParams 验证兜底收编遇到参数解析失败的 running
// wait 时不再静默跳过——直落 failed 终态并补后台结果，否则该行永远挂起
// （不评估、不超时、取消也不生效）。
func TestBotInt160WaitAdoptInvalidParams(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160wait17")
	bot := env.createBot(t, userID)
	_, req := seedWaitRun(t, env, bot.ID, false)
	now := time.Now().UTC()
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: req.ID,
		ProviderToolCallID: "prov-bad-wait", ToolName: "fs_thread", Action: "wait",
		ArgumentsJSON: `{"action":"wait","env_id":"e1","thread_id":"t1","on":"bogus"}`,
		ArgumentsHash: "x", ExecutionMode: string(model.BotExecutionAsync),
		Status: model.BotCallStatusRunning, StartedAt: &now,
		ExecutionContextJSON: "{}", CreatedAt: now,
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("seed bad wait: %v", err)
	}

	env.bots.waitAdoptUnregistered()
	done, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if done.Status != model.BotCallStatusFailed || done.FinishedAt == nil {
		t.Fatalf("解析失败的收编行应落 failed 终态: %+v", done)
	}
	env.bots.waitMu.Lock()
	_, registered := env.bots.waits[call.ID]
	env.bots.waitMu.Unlock()
	if registered {
		t.Fatalf("解析失败的行不得入注册表")
	}
}

// TestBotInt160WaitFailAcceptedClearsStartedAt 验证事务内复核改判受理失败时
// started_at 一并清空——终态行不能带着未消费的执行起点。
func TestBotInt160WaitFailAcceptedClearsStartedAt(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160wait17b")
	bot := env.createBot(t, userID)

	now := time.Now().UTC()
	entry := &botPreparedEntry{
		call: &model.BotToolCall{
			ID: uuid.NewString(), BotID: bot.ID, StartedAt: &now,
		},
	}
	failAcceptedEntry(entry, now, errs.New(errs.ErrInvalidParams.Code, "x"))
	if entry.call.StartedAt != nil {
		t.Fatalf("受理失败应清空 started_at")
	}
}

// TestBotInt160WaitDupTerminalClearsStartedAt 验证重复挂的 wait 直落
// already_waiting 终态时 started_at 一并清空——与 failAcceptedEntry 同
// 一缺陷类：从未执行的终态行不能携带执行起点（第 13 轮 P2）。
func TestBotInt160WaitDupTerminalClearsStartedAt(t *testing.T) {
	now := time.Now().UTC()
	call := &model.BotToolCall{
		ID: uuid.NewString(), StartedAt: &now,
	}
	waitStampDuplicateTerminal(call, "call-existing", []string{"idle"}, now)
	if call.StartedAt != nil {
		t.Fatalf("重复挂终态应清空 started_at")
	}
	if call.Status != model.BotCallStatusSucceeded || call.ResultJSON == nil ||
		!strings.Contains(*call.ResultJSON, "already_waiting") {
		t.Fatalf("重复挂应落 already_waiting 终态: %+v", call)
	}
}

// TestBotInt160WaitAdoptMissingTarget 验证缺 env_id/thread_id 的脏行同样
// 直落 failed 终态——收编面不留 running 孤儿（第 14 轮 Note）。
func TestBotInt160WaitAdoptMissingTarget(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160wait18")
	bot := env.createBot(t, userID)
	_, req := seedWaitRun(t, env, bot.ID, false)
	now := time.Now().UTC()
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: req.ID,
		ProviderToolCallID: "prov-notarget-wait", ToolName: "fs_thread", Action: "wait",
		ArgumentsJSON: `{"action":"wait"}`,
		ArgumentsHash: "x", ExecutionMode: string(model.BotExecutionAsync),
		Status: model.BotCallStatusRunning, StartedAt: &now,
		ExecutionContextJSON: "{}", CreatedAt: now,
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("seed no-target wait: %v", err)
	}

	env.bots.waitAdoptUnregistered()
	done, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if done.Status != model.BotCallStatusFailed || done.FinishedAt == nil {
		t.Fatalf("缺目标字段的收编行应落 failed 终态: %+v", done)
	}
}

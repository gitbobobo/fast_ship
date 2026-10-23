package service

// INT-160 合并复审修复的针对性测试：同批/并发受理限额（同批判重、同批计数
// 入限额、同批共享「继续」配额、插入事务内二次复核）、finalizeCall 取消竞速
// CAS、后台工作运行中的线程不误报 not_started、超时分类计入条目进展证据、
// 定时任务强制汇报携带被拒清单、误判计数同事务与并发不丢更新、确认卡上的
// 反向操作计入误判、授权列表 active 口径排除过期行。

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

// commitBotReply 用既有 run 受理一轮模型回复（多调用同批场景）。
func commitBotReply(t *testing.T, env *botTestEnv, botID string, run *model.BotRun, text string, calls []botModelToolCall) []*model.BotToolCall {
	t.Helper()
	botRow, err := env.bots.bots.Get(botID)
	if err != nil {
		t.Fatalf("get bot row: %v", err)
	}
	now := time.Now().UTC()
	req := &model.BotModelRequest{
		// seedWaitRun 已为该 run 落过 Ordinal 1 的请求行：本轮回复排 2。
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 2,
		Status: model.BotModelRequestSucceeded, PromptRevision: 1,
		ToolsetVersion: botToolsetVersion,
		StartedAt:      now, FinishedAt: &now,
	}
	if err := env.db.Create(req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	accepted, err := env.bots.commitAssistantTurn(botRow, run, req, &botModelReply{Text: text, ToolCalls: calls}, "msg-"+uuid.NewString())
	if err != nil {
		t.Fatalf("commit assistant turn: %v", err)
	}
	return accepted
}

// ---------- 1. 同批受理限额 ----------

func TestBotInt160BatchWaitSameThreadDedup(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixbatch1")
	bot := env.createBot(t, userID)
	_, envID := pairWaitT3(t, env, userID)
	run, _ := seedWaitRun(t, env, bot.ID, false)

	args := `{"action":"wait","env_id":"` + envID + `","thread_id":"` + int160RunningThread + `"}`
	calls := commitBotReply(t, env, bot.ID, run, "", []botModelToolCall{
		{ID: "prov-bw1-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args},
		{ID: "prov-bw2-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args},
	})
	if len(calls) != 2 {
		t.Fatalf("应受理 2 个调用，实得 %d", len(calls))
	}
	// 重复挂的 already_waiting 终态在提交后立即落库：从账本重读断言
	//（finalizeCall 不回写内存结构体）。
	first, err := env.bots.calls.FindByID(calls[0].ID)
	if err != nil || first.Status != model.BotCallStatusRunning {
		t.Fatalf("首个 wait 应受理即 running: %v %+v", err, first)
	}
	dup, err := env.bots.calls.FindByID(calls[1].ID)
	if err != nil {
		t.Fatalf("reload dup: %v", err)
	}
	if dup.Status != model.BotCallStatusSucceeded {
		t.Fatalf("同批同线程后到的 wait 应立即 already_waiting: %s", dup.Status)
	}
	result := waitResultJSON(t, dup)
	if result["already_waiting"] != true || result["waiting_call_id"] != calls[0].ID {
		t.Fatalf("重复挂应指向同批先到的调用: %v (want %s)", result["waiting_call_id"], calls[0].ID)
	}
}

func TestBotInt160BatchWaitPerBotLimit(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixbatch2")
	bot := env.createBot(t, userID)
	_, envID := pairWaitT3(t, env, userID)
	run, _ := seedWaitRun(t, env, bot.ID, false)

	wait := func(thread string) botModelToolCall {
		return botModelToolCall{ID: "prov-bw-" + uuid.NewString()[:8], Name: "fs_thread",
			Arguments: `{"action":"wait","env_id":"` + envID + `","thread_id":"` + thread + `"}`}
	}
	calls := commitBotReply(t, env, bot.ID, run, "", []botModelToolCall{
		wait(int160RunningThread), wait(int160IdleThread), wait("th-batch3"), wait("th-batch4"),
	})
	if len(calls) != 4 {
		t.Fatalf("应受理 4 个调用，实得 %d", len(calls))
	}
	for i := 0; i < 3; i++ {
		if calls[i].Status != model.BotCallStatusRunning {
			t.Fatalf("同批前 3 个 wait 应受理: calls[%d]=%s", i, calls[i].Status)
		}
	}
	if calls[3].Status != model.BotCallStatusFailed || calls[3].ErrorCode == nil || *calls[3].ErrorCode != "e40001" {
		t.Fatalf("同回复 4 个不同线程的 wait 不得突破每 bot 上限: %+v %v", calls[3].Status, calls[3].ErrorCode)
	}
	if calls[3].ErrorMessage == nil || !strings.Contains(*calls[3].ErrorMessage, "已达上限") {
		t.Fatalf("拒绝应提示已达上限: %v", calls[3].ErrorMessage)
	}
}

func TestBotInt160BatchSendQuotaShared(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixbatch3")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)
	run, _ := seedWaitRun(t, env, bot.ID, false)

	args := `{"action":"send","env_id":"` + envID + `","thread_id":"` + int160PolicyIdleThread + `","text":"继续"}`
	calls := commitBotReply(t, env, bot.ID, run, "", []botModelToolCall{
		{ID: "prov-bs1-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args},
		{ID: "prov-bs2-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args},
		{ID: "prov-bs3-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args},
	})
	if calls[0].Status != model.BotCallStatusQueued || calls[1].Status != model.BotCallStatusQueued {
		t.Fatalf("同批前两条「继续」应自动放行: %s %s", calls[0].Status, calls[1].Status)
	}
	if calls[2].Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("同回复 3 条「继续」不得共享旧配额全部放行，第 3 条应降级: %s", calls[2].Status)
	}
	pol := decodePolicyOf(t, calls[2])
	if pol["auto"] != false {
		t.Fatalf("配额降级卡不应自动放行: %v", pol)
	}
	if reason, _ := pol["downgrade_reason"].(string); !strings.Contains(reason, "额度") {
		t.Fatalf("降级原因应说明额度用尽: %q", reason)
	}
	// 收尾清理：钉住自动调用，避免后台 worker 真去执行。
	settleAutoCall(t, env, calls[0].ID)
	settleAutoCall(t, env, calls[1].ID)
}

// 复审 P2（135355d 第 2 轮）：always-allow 接管的降级候选行同样带
// auto=true+rule_id=send_continue，不得计入同批无人值守配额账本。账本键
// 与授权作用域同源，实测上同桶里 tier1 与 always-allow 不会共存（授权在
// 则一路接管），但账本口径必须与 SQL 侧 source='tier1' 过滤一致——
// quota.used 写进 policy facts，污染会直接反映在审计事实里。
func TestBotInt160BatchAlwaysAllowNotCountedInQuota(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixaa")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	// 先关规则造一张「继续」确认卡 → always_allow，拿到该线程的发送授权。
	if _, err := env.bots.SetWritePolicyRuleEnabled(userID, bot.ID, botPolicyRuleSendContinue, false); err != nil {
		t.Fatalf("disable rule: %v", err)
	}
	seed := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"send","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`","text":"继续"}`)
	if seed.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("规则关闭后应落确认卡: %s", seed.Status)
	}
	if _, err := env.bots.DecideCall(userID, bot.ID, seed.ID, &CallDecisionRequest{Decision: "always_allow"}); err != nil {
		t.Fatalf("always_allow: %v", err)
	}
	settleAutoCall(t, env, seed.ID)
	if _, err := env.bots.SetWritePolicyRuleEnabled(userID, bot.ID, botPolicyRuleSendContinue, true); err != nil {
		t.Fatalf("enable rule: %v", err)
	}

	// 同批 4 条「继续」：前两条额度内 tier1 自动放行，第 3、4 条额度超限
	// → always-allow 接管（tier1 分支先于授权分支，额度内不走 authz）。
	run, _ := seedWaitRun(t, env, bot.ID, false)
	args := `{"action":"send","env_id":"` + envID + `","thread_id":"` + int160PolicyIdleThread + `","text":"继续"}`
	calls := commitBotReply(t, env, bot.ID, run, "", []botModelToolCall{
		{ID: "prov-aa1-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args},
		{ID: "prov-aa2-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args},
		{ID: "prov-aa3-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args},
		{ID: "prov-aa4-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args},
	})
	for i, c := range calls[:2] {
		pol := decodePolicyOf(t, c)
		if c.Status != model.BotCallStatusQueued || pol["source"] != "tier1" {
			t.Fatalf("calls[%d] 应 tier1 自动放行: %s %v", i, c.Status, pol)
		}
	}
	for i, c := range calls[2:] {
		pol := decodePolicyOf(t, c)
		if c.Status != model.BotCallStatusQueued || pol["source"] != "always_allow" {
			t.Fatalf("calls[%d] 额度超限后应 always-allow 接管: %s %v", i+2, c.Status, pol)
		}
	}
	// 第 4 条的审计事实：always-allow 行未污染账本，quota.used 保持真实的
	// 2（被污染会读成 3）。
	last := decodePolicyOf(t, calls[3])
	facts, _ := last["facts"].(map[string]interface{})
	quota, _ := facts["quota"].(map[string]interface{})
	if used, _ := quota["used"].(float64); used != 2 {
		t.Fatalf("always-allow 行不得计入无人值守账本，quota.used 应=2，实得 %v", quota["used"])
	}
	for _, c := range calls {
		settleAutoCall(t, env, c.ID)
	}
}

// TestBotInt160WaitRecheckVerdicts 直测插入事务内的 wait 受理二次复核：
// 并发请求抢先挂上同线程 → 判重；并发上限已满 → 超限。
func TestBotInt160WaitRecheckVerdicts(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixrecheck")
	bot := env.createBot(t, userID)
	_, envID := pairWaitT3(t, env, userID)

	w := func(thread string) string {
		return `{"action":"wait","env_id":"` + envID + `","thread_id":"` + thread + `"}`
	}
	first := acceptWaitCall(t, env, bot.ID, false, w(int160RunningThread))
	acceptWaitCall(t, env, bot.ID, false, w(int160IdleThread))
	acceptWaitCall(t, env, bot.ID, false, w("th-recheck3"))

	verdict := env.bots.recheckWaitAcceptance(env.db, bot.ID, botThreadArgs{EnvID: envID, ThreadID: int160RunningThread})
	if verdict.dupCallID != first.ID || verdict.limErr != nil {
		t.Fatalf("同线程已有 running wait 应判重到它: %+v (want %s)", verdict, first.ID)
	}
	verdict = env.bots.recheckWaitAcceptance(env.db, bot.ID, botThreadArgs{EnvID: envID, ThreadID: "th-recheck4"})
	if verdict.limErr == nil || verdict.dupCallID != "" {
		t.Fatalf("并发上限已满应判超限: %+v", verdict)
	}
}

// TestBotInt160SendQuotaRecheckVerdict 直测插入事务内的「继续」配额二次复核。
func TestBotInt160SendQuotaRecheckVerdict(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixrecheck2")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	args := botThreadArgs{Action: "send", EnvID: envID, ThreadID: int160PolicyIdleThread, Text: "继续"}
	if err := env.bots.recheckSendQuota(env.db, bot.ID, args, time.Now().UTC()); err != nil {
		t.Fatalf("额度未用尽不应判超限: %v", err)
	}
	sendArgs := `{"action":"send","env_id":"` + envID + `","thread_id":"` + int160PolicyIdleThread + `","text":"继续"}`
	c1 := acceptPolicyCall(t, env, bot.ID, false, "fs_thread", sendArgs)
	c2 := acceptPolicyCall(t, env, bot.ID, false, "fs_thread", sendArgs)
	settleAutoCall(t, env, c1.ID)
	settleAutoCall(t, env, c2.ID)
	if err := env.bots.recheckSendQuota(env.db, bot.ID, args, time.Now().UTC()); err == nil {
		t.Fatalf("额度用尽后受理二次复核应判超限")
	}
}

// ---------- 2. finalizeCall 取消竞态 CAS ----------

// seedRunningAsyncWait 直插一条 running 的 wait 调用（带 run/request 行），供
// 取消竞速用例直接驱动 finalizeCall。
func seedRunningAsyncWait(t *testing.T, env *botTestEnv, botID string) (*model.BotRun, *model.BotToolCall) {
	t.Helper()
	run, req := seedWaitRun(t, env, botID, false)
	now := time.Now().UTC()
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: botID, RequestID: req.ID,
		ProviderToolCallID: "prov-race-" + uuid.NewString()[:8],
		ToolName:           "fs_thread", Action: "wait",
		ArgumentsJSON: `{"action":"wait","env_id":"e1","thread_id":"t1"}`,
		ArgumentsHash: "x", ExecutionMode: string(model.BotExecutionAsync),
		Status: model.BotCallStatusRunning, StartedAt: &now,
		ExecutionContextJSON: "{}", CreatedAt: now,
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("seed wait call: %v", err)
	}
	return run, call
}

func TestBotInt160FinalizeCancelRaceConvertsToCancelled(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixrace")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	run, call := seedRunningAsyncWait(t, env, bot.ID)
	// 取消标记在执行与收尾之间落上：成功终态不得覆盖取消。
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", call.ID).
		Update("cancel_requested_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("mark cancel: %v", err)
	}
	outcome := botToolOutcome{ResultJSON: `{"outcome":"triggered"}`, PreviewJSON: `{"outcome":"triggered"}`, ResultChars: 20}
	env.bots.finalizeCall(botRow, run.ID, call, model.BotCallStatusSucceeded, outcome, nil, false)

	fresh, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("reload call: %v", err)
	}
	if fresh.Status != model.BotCallStatusCancelled {
		t.Fatalf("取消竞速应改判 cancelled，实得 %s（result=%v）", fresh.Status, fresh.ResultJSON)
	}
	if fresh.EffectMayHaveOccurred {
		t.Fatalf("wait 无副作用，取消竞速不得标 effect")
	}
	r := waitResultJSON(t, fresh)
	if r["outcome"] != "cancelled" {
		t.Fatalf("结果体应覆写为 outcome=cancelled: %v", r)
	}
}

func TestBotInt160FinalizeRecheckFailureCancelRaceKeepsEffectFalse(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixrace2")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	run, call := seedRunningAsyncWait(t, env, bot.ID)
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", call.ID).
		Update("cancel_requested_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("mark cancel: %v", err)
	}
	// 复检失败发生在任何外部写入之前：即便与取消竞速叠上，effect 也恒 false。
	recheckErr := &botPolicyRecheckError{"确认卡已过期"}
	env.bots.finalizeCall(botRow, run.ID, call, model.BotCallStatusFailed,
		botBuildOutcomeRaw(call.ID, "", recheckErr), recheckErr, false)

	fresh, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("reload call: %v", err)
	}
	if fresh.Status != model.BotCallStatusCancelled {
		t.Fatalf("取消标记在窗口内落上应改判 cancelled: %s", fresh.Status)
	}
	if fresh.EffectMayHaveOccurred {
		t.Fatalf("复检失败路径的 effect 恒 false")
	}
	if fresh.ErrorCode == nil || *fresh.ErrorCode != "policy_recheck_failed" {
		t.Fatalf("错误码应保留复检失败口径: %v", fresh.ErrorCode)
	}
}

func TestBotInt160CancelBotCallAfterTerminalReportsFailure(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixrace3")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	run, call := seedRunningAsyncWait(t, env, bot.ID)
	stale, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("read call: %v", err)
	}
	// 初读之后调用落了成功终态：迟到的取消不得谎报成功。
	env.bots.finalizeCall(botRow, run.ID, call, model.BotCallStatusSucceeded,
		botToolOutcome{ResultJSON: "{}", PreviewJSON: "{}", ResultChars: 2}, nil, false)
	if _, ok := env.bots.cancelBotCall(stale); ok {
		t.Fatalf("调用已落终态时取消应报告失败")
	}
	fresh, _ := env.bots.calls.FindByID(call.ID)
	if fresh.Status != model.BotCallStatusSucceeded || fresh.CancelRequestedAt != nil {
		t.Fatalf("终态不得被取消路径改动: %+v", fresh.Status)
	}
}

// ---------- 3. 后台工作运行中的线程不误报 not_started ----------

func TestBotInt160WaitBackgroundWorkCountsAsStarted(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixbg")
	bot := env.createBot(t, userID)
	const bgThread = "th-wait-bg"
	threads := []any{
		// 根 run 已结束（无 activeRunId、status=completed），但后台 subagent
		// 仍让线程处于 running 语义。
		shellThread(bgThread, "后台忙", "completed", map[string]any{
			"activityRunStatus": "running",
		}),
	}
	fake, envID := pairBotT3WithSnapshot(t, env, userID, "后台机",
		shellSnapshotFixture([]any{map[string]any{"id": "p-1", "title": "P"}}, threads))
	fake.mu.Lock()
	fake.projections[bgThread] = threadProjection(bgThread, []any{}, nil)
	fake.mu.Unlock()
	shrinkWaitTimers(env)
	waitHubReady(t, env, userID, envID)

	call := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+bgThread+`","on":"needs_reply"}`)
	entryOf := func() *botWaitEntry {
		env.bots.waitMu.Lock()
		defer env.bots.waitMu.Unlock()
		return env.bots.waits[call.ID]
	}
	// 挂上即视为已开始：activityRunStatus 把线程投影为 running 时若等到下
	// 轮扫描才判定，活动可能已经结束——started 永远立不起来，收尾会误报
	// not_started（复审 P2：挂上/收编与评估器共用 waitObsRunning）。
	if entry := entryOf(); !entry.started {
		t.Fatalf("后台工作让线程处于 running 语义，挂上即应视为已开始: %+v", entry)
	}
	env.bots.evaluateThreadWaits()
	if entry := entryOf(); !entry.started {
		t.Fatalf("后台工作让线程处于 running 语义，应视为已开始: %+v", entry)
	}
	// 启动宽限滑过之后仍不得误报 not_started。
	time.Sleep(400 * time.Millisecond)
	env.bots.evaluateThreadWaits()
	fresh, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("reload call: %v", err)
	}
	if fresh.Status != model.BotCallStatusRunning {
		t.Fatalf("后台运行中的线程不得误报 not_started: %s result=%v", fresh.Status, fresh.ResultJSON)
	}
}

// ---------- 4. 超时分类计入条目进展证据 ----------

func TestBotInt160WaitTimeoutCountsItemProgress(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixprog")
	bot := env.createBot(t, userID)
	fake, envID := pairWaitT3(t, env, userID)

	call := acceptWaitCall(t, env, bot.ID, false,
		`{"action":"wait","env_id":"`+envID+`","thread_id":"`+int160RunningThread+`","on":"needs_reply"}`)
	// 同一 run 持续产出：shell 投影字段（status/run_status/activity/PRs 与
	// latest_run 身份）全不变，只有条目在更新。
	fake.mu.Lock()
	fake.projections[int160RunningThread] = threadProjection(int160RunningThread,
		[]any{map[string]any{"id": "run-1", "status": "running", "ordinal": 1}},
		[]map[string]any{
			{"position": 0, "visibility": "local", "sourceThreadId": int160RunningThread, "sourceItemId": "w1",
				"item": map[string]any{"id": "w1", "type": "assistant_message", "status": "completed",
					"text": "已经开始处理", "updatedAt": time.Now().UTC().Format(time.RFC3339Nano)}},
		})
	fake.mu.Unlock()
	env.bots.waitMu.Lock()
	env.bots.waits[call.ID].deadline = time.Now().Add(20 * time.Millisecond)
	env.bots.waitMu.Unlock()

	done := waitCallTerminal(t, env, call.ID, "超时收尾")
	r := waitResultJSON(t, done)
	if r["outcome"] != "timeout_progressing" {
		t.Fatalf("同一 run 持续产出条目应判 timeout_progressing，实得 %v（note=%v）", r["outcome"], r["note"])
	}
}

// ---------- 5. 定时任务强制汇报携带被拒清单 ----------

func TestBotInt160ScheduleForceReportCarriesDeniedList(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixforce")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = false // needs_report=false 也要被强制汇报压过
	})
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	// 静默哨兵 + tier3 stop：同步拒绝，且拒绝原因必须进主线汇报。
	commitBotReply(t, env, bot.ID, run, botScheduleSentinel, []botModelToolCall{{
		ID: "prov-stop-" + uuid.NewString()[:8], Name: "fs_thread",
		Arguments: `{"action":"stop","env_id":"` + envID + `","thread_id":"` + int160PolicyIdleThread + `"}`,
	}})

	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	if !env.bots.tryFinishScheduleRun(botRow, run) {
		t.Fatalf("强制汇报轮应能完成收尾")
	}
	fresh, err := env.bots.runs.FindByID(run.ID)
	if err != nil || fresh.Status != model.BotRunStatusCompleted {
		t.Fatalf("run 应落 completed: %v %+v", err, fresh.Status)
	}
	var rows []model.BotMessage
	if err := env.db.Where("bot_id = ? AND session_kind = ? AND kind = ?",
		bot.ID, model.BotSessionKindMain, model.BotMessageKindScheduleEvent).
		Order("seq ASC").Find(&rows).Error; err != nil {
		t.Fatalf("list schedule events: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("静默 + needs_report=false 被强制汇报压过，应有恰好一条主线事件，实得 %d", len(rows))
	}
	var content botScheduleEventContent
	if err := json.Unmarshal([]byte(rows[0].ContentJSON), &content); err != nil {
		t.Fatalf("decode schedule_event: %v", err)
	}
	if !strings.Contains(content.Summary, "fs_thread/stop") {
		t.Fatalf("汇报应带被拒动作清单: %q", content.Summary)
	}
	if !strings.Contains(content.Summary, "定时任务会话") {
		t.Fatalf("汇报应带拒绝原因: %q", content.Summary)
	}
}

// ---------- 6. 误判计数同事务（并发不丢更新、通知不丢失） ----------

func TestBotInt160MisjudgmentConcurrentRecordIsAtomic(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixmisjudge")
	bot := env.createBot(t, userID)

	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			env.bots.recordPolicyMisjudgment(bot.ID, botPolicyRuleSettleIdle, "并发误判")
		}()
	}
	wg.Wait()

	row := policyRuleRowOf(t, env, bot.ID, botPolicyRuleSettleIdle)
	if row == nil {
		t.Fatalf("误判后应有规则行")
	}
	if row.Enabled || row.FalsePositiveCount24h != 2 {
		t.Fatalf("并发计数不得丢更新，达到阈值即自动关闭: %+v", row)
	}
	// 关闭与通知同事务：主线通知恰好一条（不会因崩溃/并发重复或丢失）。
	var msgs []model.BotMessage
	if err := env.db.Where("bot_id = ? AND kind = ?", bot.ID, model.BotMessageKindLifecycle).
		Find(&msgs).Error; err != nil {
		t.Fatalf("list lifecycle: %v", err)
	}
	notices := 0
	for _, m := range msgs {
		if strings.Contains(m.ContentJSON, "policy_rule_autodisabled") &&
			strings.Contains(m.ContentJSON, botPolicyRuleSettleIdle) {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("自动降级通知应恰好一条，实得 %d", notices)
	}
}

// ---------- 7. 确认卡上的反向操作计入误判 ----------

func TestBotInt160MisjudgmentInverseApproval(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixinverse")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	// 自动放行 unsettle（busy 线程已知 → tier1 auto）。
	u := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"unsettle","env_id":"`+envID+`","thread_id":"`+int160PolicyBusyThread+`"}`)
	if u.Status != model.BotCallStatusQueued {
		t.Fatalf("unsettle 应自动放行: %s", u.Status)
	}
	settleAutoCall(t, env, u.ID)

	// 同线程 settle 落降级确认卡（运行中的线程不满足 settle 前置）。
	s := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"settle","env_id":"`+envID+`","thread_id":"`+int160PolicyBusyThread+`"}`)
	if s.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("运行中线程 settle 应落确认卡: %s", s.Status)
	}
	if _, err := env.bots.DecideCall(userID, bot.ID, s.ID, &CallDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatalf("approve settle: %v", err)
	}
	row := policyRuleRowOf(t, env, bot.ID, botPolicyRuleUnsettle)
	if row == nil || row.FalsePositiveCount24h != 1 {
		t.Fatalf("批准方向相反的 settle 卡应计入 unsettle 规则误判: %+v", row)
	}
}

// ---------- 8. 授权列表 active 口径排除过期行 ----------

func TestBotInt160AuthzActiveListExcludesExpired(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160fixauthz")
	bot := env.createBot(t, userID)

	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	expired := &model.BotAuthorization{ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_thread", Action: "stop", ScopeKind: "thread", ScopeID: "th-1",
		ConstraintsJSON: "{}", RuleHash: "h-expired", CreatedAt: time.Now().UTC(), ExpiresAt: &past}
	live := &model.BotAuthorization{ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_thread", Action: "send", ScopeKind: "thread", ScopeID: "th-1",
		ConstraintsJSON: "{}", RuleHash: "h-live", CreatedAt: time.Now().UTC(), ExpiresAt: &future}
	for _, row := range []*model.BotAuthorization{expired, live} {
		if err := env.db.Create(row).Error; err != nil {
			t.Fatalf("seed authz: %v", err)
		}
	}

	active, err := env.bots.authz.ListByBot(bot.ID, true)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 || active[0].ID != live.ID {
		t.Fatalf("active 口径应排除过期未撤销的行: %+v", active)
	}
	all, err := env.bots.authz.ListByBot(bot.ID, false)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("非 active 口径保留过期行（留痕不删）: %d", len(all))
	}
}

// ---------- 8. 复审第 4 轮回归 ----------

// send_continue 的拒批误判只认正文命中「继续」模板的卡：botPolicyRuleForAction
// 按动作映射，任意 send 都算同类会让一张内容无关的拒批卡污染误判计数。
func TestBotInt160MisjudgeRejectNonTemplateSend(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160reject")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	// 先落两条自动「继续」（30 分钟误判窗口的原料 + 占满每小时额度）。
	for i := 0; i < 2; i++ {
		c := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
			`{"action":"send","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`","text":"继续"}`)
		settleAutoCall(t, env, c.ID)
	}

	// 非模板 send 落 tier3 确认卡 → 拒批不得记 send_continue 误判。
	card := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"send","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`","text":"把报告删掉"}`)
	if card.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("非模板 send 应落确认卡: %s", card.Status)
	}
	if _, err := env.bots.DecideCall(userID, bot.ID, card.ID, &CallDecisionRequest{Decision: "reject"}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if row := policyRuleRowOf(t, env, bot.ID, botPolicyRuleSendContinue); row != nil && row.FalsePositiveCount24h != 0 {
		t.Fatalf("内容无关的拒批卡不得记 send_continue 误判: %+v", row)
	}

	// 对照：命中模板的「继续」卡（额度耗尽降级）被拒 → 记一次误判。
	// 不用关规则造卡——recordPolicyMisjudgment 对已关闭规则不再累计。
	card2 := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"send","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`","text":"继续"}`)
	if card2.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("额度耗尽的「继续」应降级成确认卡: %s", card2.Status)
	}
	if _, err := env.bots.DecideCall(userID, bot.ID, card2.ID, &CallDecisionRequest{Decision: "reject"}); err != nil {
		t.Fatalf("reject: %v", err)
	}
	row := policyRuleRowOf(t, env, bot.ID, botPolicyRuleSendContinue)
	if row == nil || row.FalsePositiveCount24h != 1 {
		t.Fatalf("命中模板的拒批卡应记一次误判: %+v", row)
	}
}

// 强制汇报的拒绝清单必须带真实降级原因（policy_json.downgrade_reason），
// 受理端通用文案只说「不可执行」看不出为何被拒。
func TestBotInt160ScheduleDeniedNoteShowsDowngradeReason(t *testing.T) {
	msg := "需用户确认的动作在定时任务会话中不可执行"
	rows := []model.BotToolCall{{
		ToolName: "fs_thread", Action: "launch", ErrorMessage: &msg,
		PolicyJSON: `{"tier":"tier3","source":"schedule_denied","downgrade_reason":"launch 新建线程属于高影响动作，定时任务里须回主线确认"}`,
	}}
	note := botScheduleDeniedNote(rows)
	if !strings.Contains(note, "launch 新建线程属于高影响动作") {
		t.Fatalf("拒绝清单应带真实降级原因: %q", note)
	}
	// 无降级原因时回退通用文案，不致空白。
	rows[0].PolicyJSON = `{}`
	note = botScheduleDeniedNote(rows)
	if !strings.Contains(note, msg) {
		t.Fatalf("缺降级原因时应回退 ErrorMessage: %q", note)
	}
}

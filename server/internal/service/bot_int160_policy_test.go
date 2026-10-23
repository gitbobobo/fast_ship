package service

// INT-160 分级放行测试：tier1/tier2/tier3 判定矩阵（send 模板命中/非模板/
// 无线程状态/配额满降级；settle/unsettle 前置不满足降级）、定时任务会话
// tier3 同步拒绝与强制汇报计数、过期确认卡执行前复检失败、误判计数与规则
// 自动降级、存量「始终允许」迁移（留痕不删）与新授权 30 天过期、规则开关。

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	int160PolicyIdleThread   = "th-policy-idle"
	int160PolicyBusyThread   = "th-policy-busy"
	int160PolicyUnknownEnvID = "env-none"
)

// pairPolicyT3 配对一个「idle 有痕迹 + running」两线程的环境：idle 线程带
// latestRunId（「继续」的运行痕迹判据）与详情投影（settle 的 queue.held 读取）。
func pairPolicyT3(t *testing.T, env *botTestEnv, userID string) (*fakeT3Environment, string) {
	t.Helper()
	threads := []any{
		shellThread(int160PolicyIdleThread, "待续", "idle", map[string]any{
			"latestRunId":        "run-old",
			"latestRunStartedAt": "2026-10-20T00:00:00.000Z",
		}),
		shellThread(int160PolicyBusyThread, "在跑", "running", map[string]any{
			"activeRunId": "run-1",
			"latestRunId": "run-1",
		}),
	}
	fake, envID := pairBotT3WithSnapshot(t, env, userID, "分档机",
		shellSnapshotFixture([]any{map[string]any{"id": "p-1", "title": "P"}}, threads))
	fake.mu.Lock()
	fake.projections[int160PolicyIdleThread] = threadProjection(int160PolicyIdleThread, []any{}, nil)
	fake.mu.Unlock()
	waitHubReady(t, env, userID, envID)
	return fake, envID
}

// acceptPolicyCall 走真实 commitAssistantTurn 受理一次工具调用（主线或定时
// 任务会话），返回账本行。
func acceptPolicyCall(t *testing.T, env *botTestEnv, botID string, schedule bool, toolName, args string) *model.BotToolCall {
	t.Helper()
	botRow, err := env.bots.bots.Get(botID)
	if err != nil {
		t.Fatalf("get bot row: %v", err)
	}
	run, req := seedWaitRun(t, env, botID, schedule)
	reply := &botModelReply{ToolCalls: []botModelToolCall{{
		ID: "prov-policy-" + uuid.NewString()[:8], Name: toolName, Arguments: args,
	}}}
	calls, err := env.bots.commitAssistantTurn(botRow, run, req, reply, "msg-"+uuid.NewString())
	if err != nil {
		t.Fatalf("commit assistant turn: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("应受理 1 个调用，实得 %d", len(calls))
	}
	return calls[0]
}

// settleAutoCall 尽快把自动调用钉成终态，避免后台 worker 真的去执行写操作
// （配额计数按 policy_json，不按状态）。worker 可能已把行抢到 running：
// 两种在途状态都试 CAS，直到行到终态为止。
func settleAutoCall(t *testing.T, env *botTestEnv, callID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		now := time.Now().UTC()
		cols := map[string]interface{}{"status": model.BotCallStatusSucceeded, "finished_at": now}
		okQ, _ := env.bots.calls.UpdateWhere(callID, string(model.BotCallStatusQueued), cols)
		okR, _ := env.bots.calls.UpdateWhere(callID, string(model.BotCallStatusRunning), cols)
		if okQ || okR {
			return
		}
		if call, err := env.bots.calls.FindByID(callID); err == nil && call.Status.IsTerminal() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("钉住自动调用超时: %s", callID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func decodePolicyOf(t *testing.T, call *model.BotToolCall) map[string]interface{} {
	t.Helper()
	if call.PolicyJSON == "" {
		t.Fatalf("调用 %s 没有写 policy_json", call.ID)
	}
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(call.PolicyJSON), &out); err != nil {
		t.Fatalf("decode policy_json %q: %v", call.PolicyJSON, err)
	}
	return out
}

// policyRuleRowOf 读规则开关行（测试断言用；无行返回 nil）。
func policyRuleRowOf(t *testing.T, env *botTestEnv, botID, ruleID string) *model.BotWritePolicyRule {
	t.Helper()
	row, err := repository.NewBotWritePolicyRuleRepository(env.db).GetTx(env.db, botID, ruleID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		t.Fatalf("read rule row: %v", err)
	}
	return row
}

// ---------- 1. tier1：send「继续」模板 ----------

func TestBotInt160PolicySendContinueAuto(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	call := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"send","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`","text":"请继续"}`)
	if call.Status != model.BotCallStatusQueued {
		t.Fatalf("模板「继续」应自动排队，实得 %s", call.Status)
	}
	if call.AuthorizationID != nil {
		t.Fatalf("tier1 策略放行不建授权记录")
	}
	pol := decodePolicyOf(t, call)
	if pol["tier"] != "tier1" || pol["auto"] != true || pol["rule_id"] != botPolicyRuleSendContinue {
		t.Fatalf("policy 口径不符: %v", pol)
	}
	// 前置事实进 facts：线程状态与配额。
	facts, _ := pol["facts"].(map[string]interface{})
	if facts == nil || facts["thread"] == nil || facts["quota"] == nil {
		t.Fatalf("facts 应带线程与配额事实: %v", pol)
	}
	settleAutoCall(t, env, call.ID)
}

func TestBotInt160PolicySendNonTemplateIsTier3(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy2")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	call := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"send","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`","text":"不用再等了，直接收尾"}`)
	if call.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("非模板 send 主线应落确认卡，实得 %s", call.Status)
	}
	pol := decodePolicyOf(t, call)
	if pol["tier"] != "tier3" || pol["auto"] != false {
		t.Fatalf("非模板 send 应是 tier3 确认卡: %v", pol)
	}
}

func TestBotInt160PolicySendContinueDowngrades(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy3")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	// 目标线程不可信（env 不存在 → 视图非 Ready）：降级为带过期的确认卡。
	call := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"send","env_id":"`+int160PolicyUnknownEnvID+`","thread_id":"th-x","text":"继续"}`)
	if call.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("不可信线程应降级确认卡，实得 %s", call.Status)
	}
	pol := decodePolicyOf(t, call)
	if pol["tier"] != "tier1" || pol["auto"] != false || pol["rule_id"] != botPolicyRuleSendContinue {
		t.Fatalf("降级卡应保留 tier1 候选事实: %v", pol)
	}
	if reason, _ := pol["downgrade_reason"].(string); reason == "" {
		t.Fatalf("降级卡应写明原因: %v", pol)
	}
	if _, hasExpiry := pol["expiry"]; !hasExpiry {
		t.Fatalf("降级卡应带过期时间: %v", pol)
	}

	// 运行中的线程不插话：同样降级。
	call2 := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"send","env_id":"`+envID+`","thread_id":"`+int160PolicyBusyThread+`","text":"继续"}`)
	if call2.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("运行中线程应降级确认卡，实得 %s", call2.Status)
	}
	pol2 := decodePolicyOf(t, call2)
	if pol2["tier"] != "tier1" || pol2["auto"] != false {
		t.Fatalf("运行中线程降级卡口径不符: %v", pol2)
	}
}

func TestBotInt160PolicySendQuotaExhausted(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy4")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	args := `{"action":"send","env_id":"` + envID + `","thread_id":"` + int160PolicyIdleThread + `","text":"继续"}`
	first := acceptPolicyCall(t, env, bot.ID, false, "fs_thread", args)
	second := acceptPolicyCall(t, env, bot.ID, false, "fs_thread", args)
	if first.Status != model.BotCallStatusQueued || second.Status != model.BotCallStatusQueued {
		t.Fatalf("前两次应自动放行: %s %s", first.Status, second.Status)
	}
	settleAutoCall(t, env, first.ID)
	settleAutoCall(t, env, second.ID)

	third := acceptPolicyCall(t, env, bot.ID, false, "fs_thread", args)
	if third.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("配额用尽应降级确认卡，实得 %s", third.Status)
	}
	pol := decodePolicyOf(t, third)
	if pol["tier"] != "tier1" || pol["auto"] != false {
		t.Fatalf("配额降级卡口径不符: %v", pol)
	}
	reason, _ := pol["downgrade_reason"].(string)
	if !strings.Contains(reason, "额度") {
		t.Fatalf("降级原因应说明额度用尽: %q", reason)
	}
	// 审计可追溯：降级记录指向最近一次消耗额度的自动调用。
	if got, _ := pol["approved_call_id"].(string); got != first.ID && got != second.ID {
		t.Fatalf("approved_call_id 应指向最近的自动调用: %v (want %s or %s)", pol["approved_call_id"], first.ID, second.ID)
	}
}

// ---------- 2. tier1：settle / unsettle ----------

func TestBotInt160PolicySettle(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy5")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	// 平静线程（idle、无待应答、无后台任务、队列未挂起）→ tier1 自动。
	call := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"settle","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	if call.Status != model.BotCallStatusQueued {
		t.Fatalf("平静线程 settle 应自动排队，实得 %s", call.Status)
	}
	pol := decodePolicyOf(t, call)
	if pol["tier"] != "tier1" || pol["auto"] != true || pol["rule_id"] != botPolicyRuleSettleIdle {
		t.Fatalf("settle 自动放行口径不符: %v", pol)
	}
	settleAutoCall(t, env, call.ID)

	// 运行中的线程 → 降级确认卡（结算会让它从巡检视野消失）。
	call2 := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"settle","env_id":"`+envID+`","thread_id":"`+int160PolicyBusyThread+`"}`)
	if call2.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("运行中线程 settle 应降级，实得 %s", call2.Status)
	}
	pol2 := decodePolicyOf(t, call2)
	if pol2["tier"] != "tier1" || pol2["auto"] != false {
		t.Fatalf("settle 降级卡口径不符: %v", pol2)
	}
	if reason, _ := pol2["downgrade_reason"].(string); reason == "" {
		t.Fatalf("settle 降级应写明原因: %v", pol2)
	}
}

func TestBotInt160PolicyUnsettle(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy6")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	call := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"unsettle","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	if call.Status != model.BotCallStatusQueued {
		t.Fatalf("已知线程 unsettle 应自动排队，实得 %s", call.Status)
	}
	pol := decodePolicyOf(t, call)
	if pol["tier"] != "tier1" || pol["auto"] != true || pol["rule_id"] != botPolicyRuleUnsettle {
		t.Fatalf("unsettle 自动放行口径不符: %v", pol)
	}
	settleAutoCall(t, env, call.ID)

	call2 := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"unsettle","env_id":"`+int160PolicyUnknownEnvID+`","thread_id":"th-x"}`)
	if call2.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("未知线程 unsettle 应降级，实得 %s", call2.Status)
	}
}

// ---------- 3. tier3 与定时任务会话 ----------

func TestBotInt160PolicyTier3OnMain(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy7")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	// stop 不在 tier1：主线照旧确认卡，policy 记 tier3。
	call := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"stop","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	if call.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("stop 主线应落确认卡，实得 %s", call.Status)
	}
	pol := decodePolicyOf(t, call)
	if pol["tier"] != "tier3" || pol["auto"] != false {
		t.Fatalf("stop 应是 tier3 确认卡: %v", pol)
	}

	// launch 同为 tier3。
	call2 := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"launch","env_id":"`+envID+`","project_id":"p-1","text":"开一个线程"}`)
	pol2 := decodePolicyOf(t, call2)
	if pol2["tier"] != "tier3" {
		t.Fatalf("launch 应是 tier3: %v", pol2)
	}
}

func TestBotInt160PolicyScheduleDeniesTier3(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy8")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	// tier3 在定时任务会话同步拒绝：failed + 40001 + 说明文案，账本记回 sync。
	call := acceptPolicyCall(t, env, bot.ID, true, "fs_thread",
		`{"action":"stop","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	if call.Status != model.BotCallStatusFailed {
		t.Fatalf("定时任务会话 stop 应同步拒绝，实得 %s", call.Status)
	}
	if call.ExecutionMode != string(model.BotExecutionSync) {
		t.Fatalf("受理端同步拒绝应记回 sync，实得 %s", call.ExecutionMode)
	}
	if call.ErrorCode == nil || *call.ErrorCode != "e40001" {
		t.Fatalf("拒绝错误码应为 e40001: %v", call.ErrorCode)
	}
	if call.ErrorMessage == nil || !strings.Contains(*call.ErrorMessage, "定时任务会话中不可执行") {
		t.Fatalf("拒绝应说明不可执行并要求汇报: %v", call.ErrorMessage)
	}
	pol := decodePolicyOf(t, call)
	if pol["tier"] != "tier3" || pol["schedule_denied"] != true {
		t.Fatalf("拒绝路径应写 schedule_denied: %v", pol)
	}

	// Issue 写动作同为 tier3（create 的范围只按 project_id 参数解析，无需
	// 真实 Issue 存在）。
	call2 := acceptPolicyCall(t, env, bot.ID, true, "fs_issue",
		`{"action":"create","project_id":"p-any","title":"巡检建卡"}`)
	if call2.Status != model.BotCallStatusFailed {
		t.Fatalf("定时任务会话 Issue 写应同步拒绝，实得 %s", call2.Status)
	}
	pol2 := decodePolicyOf(t, call2)
	if pol2["schedule_denied"] != true {
		t.Fatalf("Issue 写拒绝应写 schedule_denied: %v", pol2)
	}
}

func TestBotInt160PolicyScheduleKeepsTier1AndTier2(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy9")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	// tier1 在定时任务会话照常自动执行。
	call := acceptPolicyCall(t, env, bot.ID, true, "fs_thread",
		`{"action":"unsettle","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	if call.Status != model.BotCallStatusQueued {
		t.Fatalf("定时任务会话 tier1 应自动排队，实得 %s", call.Status)
	}
	pol := decodePolicyOf(t, call)
	if pol["tier"] != "tier1" || pol["auto"] != true {
		t.Fatalf("定时任务会话 tier1 口径不符: %v", pol)
	}
	settleAutoCall(t, env, call.ID)

	// tier2 维持 INT-103 默认放行：fs_project create 排队，policy 记
	// schedule_default。
	call2 := acceptPolicyCall(t, env, bot.ID, true, "fs_project", `{"action":"create","name":"巡检建项"}`)
	if call2.Status != model.BotCallStatusQueued {
		t.Fatalf("定时任务会话 tier2 应默认放行，实得 %s", call2.Status)
	}
	pol2 := decodePolicyOf(t, call2)
	if pol2["tier"] != "tier2" || pol2["auto"] != true || pol2["source"] != "schedule_default" {
		t.Fatalf("tier2 默认放行口径不符: %v", pol2)
	}
	settleAutoCall(t, env, call2.ID)
}

// TestBotInt160PolicyScheduleForceReportCount 验证强制汇报的计数来源：拒绝
// 行带 schedule_denied 标记，按 run 关联可数出来（tryFinishScheduleRun 据此
// 在 needs_report=false 时也追加主线事件）。
func TestBotInt160PolicyScheduleForceReportCount(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy9b")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	call := acceptPolicyCall(t, env, bot.ID, true, "fs_thread",
		`{"action":"stop","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	runID := env.bots.runIDOfCall(call)
	n, err := env.bots.calls.CountScheduleDeniedByRun(runID)
	if err != nil {
		t.Fatalf("count denied: %v", err)
	}
	if n != 1 {
		t.Fatalf("拒绝行应计入 run 的强制汇报计数，实得 %d", n)
	}
	// 主线会话的 tier3 确认卡不计数。
	mainCall := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"stop","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	runID2 := env.bots.runIDOfCall(mainCall)
	n2, err := env.bots.calls.CountScheduleDeniedByRun(runID2)
	if err != nil {
		t.Fatalf("count denied: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("主线确认卡不应计数，实得 %d", n2)
	}
}

// ---------- 4. 执行前复检 ----------

// seedDowngradedCard 直接落一张降级确认卡（queued + tier1 降级 policy），
// 绕开 DecideCall 与后台 worker 的竞速。账本行有真实外键，先补 run+request。
func seedDowngradedCard(t *testing.T, env *botTestEnv, botID, args, policyJSON string) *model.BotToolCall {
	t.Helper()
	_, req := seedWaitRun(t, env, botID, false)
	now := time.Now().UTC()
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: botID, RequestID: req.ID,
		ProviderToolCallID: "prov-policy", ToolName: "fs_thread", Action: "send",
		ArgumentsJSON: args, ArgumentsHash: "x", ExecutionMode: string(model.BotExecutionAsync),
		RequiresConfirmation: true, Status: model.BotCallStatusQueued,
		ExecutionContextJSON: `{"scope_kind":"thread"}`, PolicyJSON: policyJSON,
		CreatedAt: now,
	}
	if err := env.db.Create(call).Error; err != nil {
		t.Fatalf("seed call: %v", err)
	}
	return call
}

func TestBotInt160PolicyRecheckExpired(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy10")
	bot := env.createBot(t, userID)

	past := time.Now().UTC().Add(-time.Minute)
	expired, _ := json.Marshal(botWritePolicyRecord{
		Tier: "tier1", RuleID: botPolicyRuleSendContinue, Auto: false,
		Source: "confirmation", DowngradeReason: "配额用尽",
		Expiry: &past,
	})
	args := `{"action":"send","env_id":"e","thread_id":"th","text":"继续"}`
	call := seedDowngradedCard(t, env, bot.ID, args, string(expired))

	env.bots.executeQueuedCall(call.ID)
	fresh, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("find call: %v", err)
	}
	if fresh.Status != model.BotCallStatusFailed {
		t.Fatalf("过期卡执行应失败，实得 %s", fresh.Status)
	}
	if fresh.ErrorCode == nil || *fresh.ErrorCode != "policy_recheck_failed" {
		t.Fatalf("错误码应为 policy_recheck_failed: %v", fresh.ErrorCode)
	}
	if fresh.EffectMayHaveOccurred {
		t.Fatalf("复检失败发生在执行前，不应标副作用")
	}
}

func TestBotInt160PolicyRecheckPassesAndSkips(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy11")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}

	// 复检通过：线程当前可信且空闲（复检不查配额）。
	future := time.Now().UTC().Add(botPolicyCardExpiry)
	valid, _ := json.Marshal(botWritePolicyRecord{
		Tier: "tier1", RuleID: botPolicyRuleSendContinue, Auto: false,
		Source: "confirmation", DowngradeReason: "配额用尽", Expiry: &future,
	})
	args := `{"action":"send","env_id":"` + envID + `","thread_id":"` + int160PolicyIdleThread + `","text":"继续"}`
	var rec botWritePolicyRecord
	if err := json.Unmarshal(valid, &rec); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	if recheckErr := env.bots.policyRecheckCall(botRow, &model.BotToolCall{
		ToolName: "fs_thread", Action: "send", ArgumentsJSON: args, PolicyJSON: string(valid),
	}); recheckErr != nil {
		t.Fatalf("有效卡复检应通过: %v", recheckErr)
	}

	// 过期卡直接在复检入口被拒（不发起外部写入）。
	expiredRec := rec
	past := time.Now().UTC().Add(-time.Minute)
	expiredRec.Expiry = &past
	expiredRaw, _ := json.Marshal(expiredRec)
	recheckErr := env.bots.policyRecheckCall(botRow, &model.BotToolCall{
		ToolName: "fs_thread", Action: "send", ArgumentsJSON: args, PolicyJSON: string(expiredRaw),
	})
	if recheckErr == nil {
		t.Fatalf("过期卡复检应失败")
	}
	if _, ok := recheckErr.(*botPolicyRecheckError); !ok {
		t.Fatalf("复检失败应是专用错误类型: %T", recheckErr)
	}

	// 非 tier1 卡（tier2/tier3）不复检：直接放行执行。
	if recheckErr := env.bots.policyRecheckCall(botRow, &model.BotToolCall{
		ToolName: "fs_thread", Action: "stop", PolicyJSON: `{"tier":"tier3","auto":false}`,
	}); recheckErr != nil {
		t.Fatalf("tier3 卡不应复检: %v", recheckErr)
	}

	// 自动放行同样复检前置条件（INT-160 复审 P2-13）：受理与执行之间线程
	// 可能已变；复检一律不查配额。线程当前可信且空闲 → 通过。
	autoRec, _ := json.Marshal(botWritePolicyRecord{
		Tier: "tier1", RuleID: botPolicyRuleSendContinue, Auto: true, Source: "tier1",
	})
	if recheckErr := env.bots.policyRecheckCall(botRow, &model.BotToolCall{
		ToolName: "fs_thread", Action: "send", ArgumentsJSON: args, PolicyJSON: string(autoRec),
	}); recheckErr != nil {
		t.Fatalf("自动放行前置仍满足时应通过复检: %v", recheckErr)
	}
	// 线程已不可信（env 不存在）→ 复检失败，专用错误类型。
	if recheckErr := env.bots.policyRecheckCall(botRow, &model.BotToolCall{
		ToolName: "fs_thread", Action: "send",
		ArgumentsJSON: `{"action":"send","env_id":"env-none","thread_id":"th-x","text":"继续"}`,
		PolicyJSON:    string(autoRec),
	}); recheckErr == nil {
		t.Fatalf("自动放行前置已失效时应复检失败")
	} else if _, ok := recheckErr.(*botPolicyRecheckError); !ok {
		t.Fatalf("复检失败应是专用错误类型: %T", recheckErr)
	}
}

// ---------- 5. 误判计数与自动降级 ----------

func TestBotInt160PolicyMisjudgmentAutoDisables(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy12")
	bot := env.createBot(t, userID)

	env.bots.recordPolicyMisjudgment(bot.ID, botPolicyRuleSettleIdle, "第一次误判")
	row := policyRuleRowOf(t, env, bot.ID, botPolicyRuleSettleIdle)
	if row == nil {
		t.Fatalf("误判后应有规则行")
	}
	if !row.Enabled || row.FalsePositiveCount24h != 1 {
		t.Fatalf("第一次误判只计数不降级: %+v", row)
	}
	env.bots.recordPolicyMisjudgment(bot.ID, botPolicyRuleSettleIdle, "第二次误判")
	row = policyRuleRowOf(t, env, bot.ID, botPolicyRuleSettleIdle)
	if row == nil {
		t.Fatalf("误判后应有规则行")
	}
	if row.Enabled || row.AutoDisabledAt == nil {
		t.Fatalf("两次误判应自动降级: %+v", row)
	}
	// 主线收到「规则已自动降级回确认」的生命周期通知。
	var msgs []model.BotMessage
	if err := env.db.Where("bot_id = ? AND kind = ?", bot.ID, model.BotMessageKindLifecycle).
		Find(&msgs).Error; err != nil {
		t.Fatalf("list lifecycle: %v", err)
	}
	found := false
	for _, m := range msgs {
		if strings.Contains(m.ContentJSON, "policy_rule_autodisabled") &&
			strings.Contains(m.ContentJSON, botPolicyRuleSettleIdle) {
			found = true
		}
	}
	if !found {
		t.Fatalf("应有规则自动降级的主线通知")
	}

	// 规则关闭后 settle 不再自动放行：tier1 候选降级为确认卡。
	_, envID := pairPolicyT3(t, env, userID)
	call := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"settle","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	if call.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("规则关闭后 settle 应落确认卡，实得 %s", call.Status)
	}
	pol := decodePolicyOf(t, call)
	if pol["auto"] != false {
		t.Fatalf("规则关闭后不应自动放行: %v", pol)
	}

	// 手动重新启用：清自动降级痕迹，恢复自动放行。
	if _, err := env.bots.SetWritePolicyRuleEnabled(userID, bot.ID, botPolicyRuleSettleIdle, true); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	call2 := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"settle","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	if call2.Status != model.BotCallStatusQueued {
		t.Fatalf("重新启用后 settle 应回到自动放行，实得 %s", call2.Status)
	}
	settleAutoCall(t, env, call2.ID)
}

func TestBotInt160PolicyMisjudgmentHooks(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy13")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	// 自动放行 settle（queued）后机器人自己受理方向相反的 unsettle：不记
	// 误判——误判只计「用户」反向操作（复审 P2 修正：正常巡检的
	// settle→unsettle 往返不是用户反转）。
	settle := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"settle","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	settleAutoCall(t, env, settle.ID)
	acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"unsettle","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	if row := policyRuleRowOf(t, env, bot.ID, botPolicyRuleSettleIdle); row != nil && row.FalsePositiveCount24h != 0 {
		t.Fatalf("机器人自发 settle→unsettle 往返不应记误判: %+v", row)
	}

	// 用户批准一张反向确认卡才记误判：关闭 unsettle 规则让反向操作落成
	// 确认卡，批准后按事实记一次。
	if _, err := env.bots.SetWritePolicyRuleEnabled(userID, bot.ID, botPolicyRuleUnsettle, false); err != nil {
		t.Fatalf("disable unsettle rule: %v", err)
	}
	unsettleCard := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"unsettle","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	if unsettleCard.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("规则关闭后 unsettle 应落确认卡，实得 %s", unsettleCard.Status)
	}
	if _, err := env.bots.DecideCall(userID, bot.ID, unsettleCard.ID, &CallDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatalf("approve unsettle card: %v", err)
	}
	row := policyRuleRowOf(t, env, bot.ID, botPolicyRuleSettleIdle)
	if row == nil {
		t.Fatalf("误判后应有规则行")
	}
	if row.FalsePositiveCount24h != 1 {
		t.Fatalf("用户批准反向确认卡应记一次误判: %+v", row)
	}

	// 自动「继续」后用户批准 stop：第二个钩子。
	cont := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"send","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`","text":"继续"}`)
	settleAutoCall(t, env, cont.ID)
	stop := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"stop","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`"}`)
	if _, err := env.bots.DecideCall(userID, bot.ID, stop.ID, &CallDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatalf("approve stop: %v", err)
	}
	row = policyRuleRowOf(t, env, bot.ID, botPolicyRuleSendContinue)
	if row == nil {
		t.Fatalf("误判后应有规则行")
	}
	if row.FalsePositiveCount24h != 1 {
		t.Fatalf("自动「继续」后批准 stop 应记一次误判: %+v", row)
	}
}

// ---------- 6. 存量授权迁移与限时授权 ----------

func TestBotInt160PolicyAuthorizationMigration(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy14")
	bot := env.createBot(t, userID)

	legacy := &model.BotAuthorization{ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_issue", Action: "create", ScopeKind: "issue_project", ScopeID: "p1",
		ConstraintsJSON: "{}", RuleHash: "hash-legacy", CreatedAt: time.Now().UTC()}
	revokedRow := &model.BotAuthorization{ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_issue", Action: "comment", ScopeKind: "issue_project", ScopeID: "p1",
		ConstraintsJSON: "{}", RuleHash: "hash-revoked", CreatedAt: time.Now().UTC(),
		RevokedAt: nowPtr()}
	future := time.Now().UTC().Add(24 * time.Hour)
	limited := &model.BotAuthorization{ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_schedule", Action: "run", ScopeKind: "schedule_task", ScopeID: "t1",
		ConstraintsJSON: "{}", RuleHash: "hash-limited", CreatedAt: time.Now().UTC(),
		ExpiresAt: &future}
	for _, row := range []*model.BotAuthorization{legacy, revokedRow, limited} {
		if err := env.db.Create(row).Error; err != nil {
			t.Fatalf("seed authz: %v", err)
		}
	}

	// 幂等：跑两次结果一致。
	if err := env.bots.MigrateTieredAuthorization(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := env.bots.MigrateTieredAuthorization(); err != nil {
		t.Fatalf("migrate twice: %v", err)
	}

	var after model.BotAuthorization
	if err := env.db.Where("id = ?", legacy.ID).First(&after).Error; err != nil {
		t.Fatalf("reload legacy: %v", err)
	}
	if after.RevokedAt == nil {
		t.Fatalf("存量永久授权应被撤销（留痕不删）")
	}
	// 留痕不删：行还在，审计关联不断。每次重读都用新变量——GORM 会把结构体
	// 已有的主键追加成额外条件。
	var reloaded model.BotAuthorization
	if err := env.db.Where("id = ?", revokedRow.ID).First(&reloaded).Error; err != nil || reloaded.RevokedAt == nil {
		t.Fatalf("已撤销行不应被迁移触碰: %v", err)
	}
	var limitedAfter model.BotAuthorization
	if err := env.db.Where("id = ?", limited.ID).First(&limitedAfter).Error; err != nil || limitedAfter.RevokedAt != nil {
		t.Fatalf("已带过期时间的授权不应被迁移触碰: %v", err)
	}

	// 过期未撤销的行不再命中匹配（查询条件直接挡掉）。
	past := time.Now().UTC().Add(-time.Hour)
	expired := &model.BotAuthorization{ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_thread", Action: "send", ScopeKind: "thread", ScopeID: "th-1",
		ConstraintsJSON: "{}", RuleHash: botRuleHash(botAuthorizationRule{
			ToolName: "fs_thread", Action: "send", ScopeKind: "thread", ScopeID: "th-1",
		}), CreatedAt: time.Now().UTC(), ExpiresAt: &past}
	if err := env.db.Create(expired).Error; err != nil {
		t.Fatalf("seed expired: %v", err)
	}
	prepared := &botPreparedCall{ToolName: "fs_thread", Action: "send",
		ScopeKind: "thread", ScopeID: "th-1"}
	if authz := env.bots.matchAuthorization(bot.ID, prepared); authz != nil {
		t.Fatalf("过期授权不应命中")
	}
}

func TestBotInt160PolicyNewAlwaysAllowExpires(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy15")
	bot := env.createBot(t, userID)

	// 主线确认卡 → always_allow：新授权必须带 ≈30 天过期。
	call := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"stop","env_id":"e","thread_id":"th-1"}`)
	if _, err := env.bots.DecideCall(userID, bot.ID, call.ID, &CallDecisionRequest{Decision: "always_allow"}); err != nil {
		t.Fatalf("always_allow: %v", err)
	}
	fresh, err := env.bots.calls.FindByID(call.ID)
	if err != nil || fresh.AuthorizationID == nil {
		t.Fatalf("always_allow 应建授权并回填: %v", err)
	}
	authz, err := env.bots.authz.FindByID(*fresh.AuthorizationID)
	if err != nil {
		t.Fatalf("find authz: %v", err)
	}
	if authz.ExpiresAt == nil {
		t.Fatalf("新建授权应带过期时间")
	}
	expires := *authz.ExpiresAt
	days := time.Until(expires).Hours() / 24
	if days < 29 || days > 31 {
		t.Fatalf("新授权应约 30 天有效，实得 %.1f 天", days)
	}

	// 命中后同类调用自动排队（既有 always-allow 行为不变，只是现在限时）。
	again := acceptPolicyCall(t, env, bot.ID, false, "fs_thread",
		`{"action":"stop","env_id":"e","thread_id":"th-1"}`)
	if again.Status != model.BotCallStatusQueued || again.AuthorizationID == nil {
		t.Fatalf("限时授权内同类调用应自动排队: %s %v", again.Status, again.AuthorizationID)
	}
	settleAutoCall(t, env, again.ID)
}

// ---------- 7. 规则开关 API ----------

func TestBotInt160PolicyRuleToggleAPI(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy16")
	bot := env.createBot(t, userID)

	rules, err := env.bots.ListWritePolicyRules(userID, bot.ID)
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	if len(rules) != len(botWritePolicyRuleCatalog) {
		t.Fatalf("应列出目录全量规则: %d", len(rules))
	}
	for _, r := range rules {
		if !r.Enabled {
			t.Fatalf("缺行规则应按启用展示: %+v", r)
		}
	}

	disabled, err := env.bots.SetWritePolicyRuleEnabled(userID, bot.ID, botPolicyRuleSendContinue, false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if disabled.Enabled {
		t.Fatalf("disable 应生效: %+v", disabled)
	}
	rules, err = env.bots.ListWritePolicyRules(userID, bot.ID)
	if err != nil {
		t.Fatalf("list rules: %v", err)
	}
	for _, r := range rules {
		if r.RuleID == botPolicyRuleSendContinue && r.Enabled {
			t.Fatalf("关闭后的规则应展示为禁用: %+v", r)
		}
	}

	if _, err := env.bots.SetWritePolicyRuleEnabled(userID, bot.ID, "no-such-rule", true); err == nil {
		t.Fatalf("未知规则应报错")
	}
	// 越权：别人的机器人不可见。
	otherUser := createBotTestUser(t, env.db, "int160policy16b")
	if _, err := env.bots.SetWritePolicyRuleEnabled(otherUser, bot.ID, botPolicyRuleSendContinue, true); err == nil {
		t.Fatalf("跨用户开关应报错")
	}
}

// Bot P1：always_allow 持久授权的执行复检豁免——受理端裁决「授权优先于
// 降级」，执行前复检不能再用同一组 tier1 前置条件推翻用户明示的授权。
// 线程已不可信（env 不存在）时 source=tier1 会复检失败，always_allow 不。
func TestBotInt160RecheckSkipsAlwaysAllow(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policyskip")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}

	args := `{"action":"send","env_id":"env-none","thread_id":"th-x","text":"继续"}`
	allowRec, _ := json.Marshal(botWritePolicyRecord{
		Tier: "tier1", RuleID: botPolicyRuleSendContinue, Auto: true, Source: "always_allow",
	})
	if recheckErr := env.bots.policyRecheckCall(botRow, &model.BotToolCall{
		ToolName: "fs_thread", Action: "send", ArgumentsJSON: args, PolicyJSON: string(allowRec),
	}); recheckErr != nil {
		t.Fatalf("always_allow 不应被 tier1 前置条件复检推翻: %v", recheckErr)
	}

	// 对照：同记录 source=tier1 在前置不满足时必须复检失败（守卫豁免边界）。
	tier1Rec, _ := json.Marshal(botWritePolicyRecord{
		Tier: "tier1", RuleID: botPolicyRuleSendContinue, Auto: true, Source: "tier1",
	})
	recheckErr := env.bots.policyRecheckCall(botRow, &model.BotToolCall{
		ToolName: "fs_thread", Action: "send", ArgumentsJSON: args, PolicyJSON: string(tier1Rec),
	})
	if recheckErr == nil {
		t.Fatalf("source=tier1 前置已失效时应复检失败")
	}
	if _, ok := recheckErr.(*botPolicyRecheckError); !ok {
		t.Fatalf("复检失败应是专用错误类型: %T", recheckErr)
	}
}

// Bot P2：每小时 2 次「继续」配额是硬上限——两个并发 commitAssistantTurn
// 在 commitMu 下串行评估，后到者看见先到者已提交的 tier1 行，不得双双
// 越过配额（旧实现两个事务各读自己的已提交视图可同时放行）。
func TestBotInt160ConcurrentSendQuota(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policyconc")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	args := `{"action":"send","env_id":"` + envID + `","thread_id":"` + int160PolicyIdleThread + `","text":"请继续"}`

	// 先用掉 1 次额度，剩 1 次给两路并发抢。
	first := acceptPolicyCall(t, env, bot.ID, false, "fs_thread", args)
	if first.Status != model.BotCallStatusQueued {
		t.Fatalf("第一笔应自动放行，实得 %s", first.Status)
	}
	settleAutoCall(t, env, first.ID)

	var wg sync.WaitGroup
	results := make([]*model.BotToolCall, 2)
	errsOut := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run, req := seedWaitRun(t, env, bot.ID, false)
			reply := &botModelReply{ToolCalls: []botModelToolCall{{
				ID: "prov-quota-" + uuid.NewString()[:8], Name: "fs_thread", Arguments: args,
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

	auto := 0
	for i, c := range results {
		if c == nil {
			t.Fatalf("并发提交 %d 失败: %v", i, errsOut[i])
		}
		pol := decodePolicyOf(t, c)
		if c.Status == model.BotCallStatusQueued && pol["source"] == "tier1" {
			auto++
		}
	}
	if auto != 1 {
		t.Fatalf("额度只剩 1 次时两路并发应恰放行一笔 tier1 自动，实得 %d", auto)
	}
}

// ---------- 6. 复审第 7 轮回归 ----------

// TestBotInt160PolicyAlwaysAllowRewritesSource 验证降级卡经「始终允许」批准后
// policy_json.source 在同事务改写为 always_allow，执行时不再走确认复检。
func TestBotInt160PolicyAlwaysAllowRewritesSource(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy15")
	_, envID := pairPolicyT3(t, env, userID)
	bot := env.createBot(t, userID)

	future := time.Now().UTC().Add(time.Hour)
	pol, _ := json.Marshal(botWritePolicyRecord{
		Tier: "tier1", RuleID: botPolicyRuleSendContinue, Auto: false,
		Source: "confirmation", DowngradeReason: "快照滞后", Expiry: &future,
	})
	call := seedDowngradedCard(t, env, bot.ID,
		`{"action":"send","env_id":"`+envID+`","thread_id":"`+int160PolicyIdleThread+`","text":"继续"}`,
		string(pol))
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", call.ID).
		Update("status", string(model.BotCallStatusPendingConfirmation)).Error; err != nil {
		t.Fatalf("置 pending: %v", err)
	}

	if _, err := env.bots.DecideCall(userID, bot.ID, call.ID,
		&CallDecisionRequest{Decision: "always_allow"}); err != nil {
		t.Fatalf("always_allow: %v", err)
	}
	fresh, err := env.bots.calls.FindByID(call.ID)
	if err != nil {
		t.Fatalf("find call: %v", err)
	}
	var stored map[string]interface{}
	if err := json.Unmarshal([]byte(fresh.PolicyJSON), &stored); err != nil {
		t.Fatalf("policy json: %v", err)
	}
	if stored["source"] != "always_allow" {
		t.Fatalf("批准后 source 应改写为 always_allow: %v", stored)
	}
}

// TestBotInt160PolicyNilTaskForceReport 验证 ScheduleTaskID 为空的 schedule
// run 出现 tier3 拒绝时，强制汇报仍追加主线事件（needs_report=false 也压不过）。
func TestBotInt160PolicyNilTaskForceReport(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy16")
	bot := env.createBot(t, userID)

	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource:      model.BotTriggerSchedule,
		TriggerKey:         "wake:" + uuid.NewString(),
		Status:             model.BotRunStatusRunning,
		SessionKind:        model.BotSessionKindSchedule,
		ConfigSnapshotJSON: "{}",
		TriggerContextJSON: `{"needs_report":false,"task_name":"孤儿定时"}`,
		CreatedAt:          now, StartedAt: &now,
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
	denied := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: req.ID,
		ProviderToolCallID: "prov-denied", ToolName: "fs_thread", Action: "stop",
		ArgumentsJSON: "{}", ArgumentsHash: "x", ExecutionMode: string(model.BotExecutionAsync),
		Status: model.BotCallStatusFailed, ExecutionContextJSON: `{"scope_kind":"thread"}`,
		PolicyJSON: `{"tier":"tier3","schedule_denied":true,"reason":"高危写"}`,
		CreatedAt:  now, FinishedAt: &now,
	}
	if err := env.db.Create(denied).Error; err != nil {
		t.Fatalf("seed denied: %v", err)
	}

	var before int64
	env.db.Model(&model.BotMessage{}).
		Where("bot_id = ? AND kind = ?", bot.ID, string(model.BotMessageKindScheduleEvent)).
		Count(&before)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	if !env.bots.tryFinishScheduleRun(botRow, run) {
		t.Fatalf("孤儿任务运行应收尾")
	}
	var after int64
	env.db.Model(&model.BotMessage{}).
		Where("bot_id = ? AND kind = ?", bot.ID, string(model.BotMessageKindScheduleEvent)).
		Count(&after)
	if after <= before {
		t.Fatalf("ScheduleTaskID 为空的运行被拒后仍应追加强制汇报事件")
	}
}

// TestBotInt160PolicyQueueHeldUnknown 验证 settle 前置检查读不到队列状态时，
// facts 记 queue_held="unknown" 而不是 false——读取失败与「队列没挂起」是两码事。
func TestBotInt160PolicyQueueHeldUnknown(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy17")
	// 线程在快照里（已知、空闲、无活动运行），但不种详情投影——
	// policyQueueHeld 的 Detail 读取会失败。
	_, envID := pairBotT3WithSnapshot(t, env, userID, "无投影",
		shellSnapshotFixture([]any{map[string]any{"id": "p-1", "title": "P"}},
			[]any{shellThread("th-nq", "无投影", "idle", nil)}))
	bot := env.createBot(t, userID)
	waitHubReady(t, env, userID, envID)

	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	facts := map[string]interface{}{}
	ok, reason := env.bots.policyCheckSettlePreconditions(botRow,
		botThreadArgs{EnvID: envID, ThreadID: "th-nq"}, facts, time.Now().UTC(), nil)
	if ok {
		t.Fatalf("队列状态读不到应保守降级")
	}
	if facts["queue_held"] != "unknown" {
		t.Fatalf("读取失败应记 unknown 而非 false: %v", facts)
	}
	if reason == "" {
		t.Fatalf("应给出降级原因")
	}
}

// TestBotInt160PolicySettleQueueHeldPrefetched 验证受理路径的 settle 前置检查
// 只查预取表不做 Detail 网络读取（第 13 轮 P2：锁内不得有网络 IO）：
// 表内命中用表内值（held=true → 降级），未预取到的线程按读取失败记
// queue_held="unknown" 保守降级。
func TestBotInt160PolicySettleQueueHeldPrefetched(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policy18")
	_, envID := pairBotT3WithSnapshot(t, env, userID, "预取表",
		shellSnapshotFixture([]any{map[string]any{"id": "p-1", "title": "P"}},
			[]any{shellThread("th-pf", "预取表线程", "idle", nil)}))
	bot := env.createBot(t, userID)
	waitHubReady(t, env, userID, envID)

	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	facts := map[string]interface{}{}
	args := botThreadArgs{EnvID: envID, ThreadID: "th-pf"}

	// 预取表命中 held=true：不做 Detail 读取（hub 里本就没有投影可读，
	// 走了实时读会记 unknown 而不是 true），直接按挂起降级。
	ok, reason := env.bots.policyCheckSettlePreconditions(botRow, args, facts,
		time.Now().UTC(), map[string]queueHeldResult{
			envID + "\x00" + "th-pf": {held: true, ok: true},
		})
	if ok || facts["queue_held"] != true {
		t.Fatalf("预取 held=true 应按挂起降级: ok=%v facts=%v", ok, facts)
	}
	if reason == "" {
		t.Fatalf("应给出降级原因")
	}

	// 预取表无该线程键（预取漏抓或参数写坏）：按读取失败降级，不得在锁内
	// 补做网络读取（若走了实时读，本断言拿到的会是 unknown 之外的结果）。
	facts2 := map[string]interface{}{}
	ok2, _ := env.bots.policyCheckSettlePreconditions(botRow, args, facts2,
		time.Now().UTC(), map[string]queueHeldResult{})
	if ok2 || facts2["queue_held"] != "unknown" {
		t.Fatalf("未预取到应按读取失败记 unknown: ok=%v facts=%v", ok2, facts2)
	}
}

// Bot Note：执行前复检把复检时刻的事实写回 policy_json（facts.recheck），
// 受理时刻快照保留在 facts 顶层——复检依据对审计可见而不是死赋值。
func TestBotInt160RecheckWritesFactsBack(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "int160policyrb")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}

	args := `{"action":"send","env_id":"env-none","thread_id":"th-x","text":"继续"}`
	// 受理时刻的既有 facts 原样保留，复检结论挂 facts.recheck。
	policyRec, _ := json.Marshal(botWritePolicyRecord{
		Tier: "tier1", RuleID: botPolicyRuleSendContinue, Auto: true, Source: "tier1",
		Facts: map[string]interface{}{"thread": map[string]interface{}{"known": true}},
	})
	call := &model.BotToolCall{
		ToolName: "fs_thread", Action: "send", ArgumentsJSON: args, PolicyJSON: string(policyRec),
	}
	recheckErr := env.bots.policyRecheckCall(botRow, call)
	if recheckErr == nil {
		t.Fatalf("线程不可信时应复检失败")
	}
	written := botWritePolicyRecordOf(call)
	if written == nil || written.Facts == nil {
		t.Fatalf("复检后 policy_json 应仍带 facts: %s", call.PolicyJSON)
	}
	recheck, ok := written.Facts["recheck"].(map[string]interface{})
	if !ok {
		t.Fatalf("facts.recheck 应写回复检依据: %s", call.PolicyJSON)
	}
	if recheck["failed_reason"] == "" {
		t.Fatalf("facts.recheck 应含 failed_reason: %v", recheck)
	}
	// 受理时刻的顶层快照不被覆写。
	top, ok := written.Facts["thread"].(map[string]interface{})
	if !ok || top["known"] != true {
		t.Fatalf("受理时刻 facts 快照应保留: %s", call.PolicyJSON)
	}
	if written.Facts["recheck_at"] == "" {
		t.Fatalf("应记录复检时刻: %s", call.PolicyJSON)
	}
}

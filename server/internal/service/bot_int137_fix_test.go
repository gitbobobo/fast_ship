package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

// INT-137 对抗审查修复的回归测试：429+insufficient_quota 不重试、辅助请求
// 超时记账、宽限停止的「有进展则不硬杀」、硬停止不取消别的 run 的槽、水位
// 只升不降、熔断窗口不延长与凭证一致性、R6 压缩边界闸门。

// ---------- 429 + insufficient_quota 不重试 ----------

// 429 携带 insufficient_quota 是「额度用完」：分类为 provider_quota_exhausted，
// 不按限流重试，恰好一条模型请求。
func TestBot429InsufficientQuotaNotRetried(t *testing.T) {
	quota := fakeModelTurn{
		Status:    http.StatusTooManyRequests,
		ErrorBody: `{"error":{"message":"You exceeded your current quota, please check your plan and billing details.","type":"insufficient_quota","code":"insufficient_quota"}}`,
	}
	// 多放几轮脚本：若错误地按限流重试，会消费到后面的轮次并被抓包。
	env := setupBotTestEnv(t, quota, quota, quota)
	int137RunRuntime(env)
	userID := createBotTestUser(t, env.db, "botquota")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "q1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "quota 收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })

	run := lastRunOf(t, env, bot.ID)
	if run.Status != model.BotRunStatusFailed || run.ErrorCode == nil || *run.ErrorCode != "provider_quota_exhausted" {
		t.Fatalf("429+insufficient_quota 应 failed/provider_quota_exhausted，实得 %s %v", run.Status, run.ErrorCode)
	}
	if reqs := runModelRequests(t, env, run.ID); len(reqs) != 1 {
		t.Fatalf("额度用完不得重试，模型请求应恰好 1 条，实得 %d", len(reqs))
	}
}

// ---------- 辅助请求：失败记账与总超时 ----------

// 辅助请求（摘要/归档）的看门狗超时必须记 model_stream_timeout，而不是旧的
// maintenance_request_failed；总超时（FirstChunk+Idle+30s）只是防泄漏兜底，
// 看门狗先触发。
func TestBotAuxRequestTimeoutRecordedAsStreamTimeout(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{HoldFor: 5 * time.Second})
	env.cfg.Bots.Runtime.StreamFirstChunkTimeoutSeconds = 1
	env.cfg.Bots.Runtime.StreamIdleTimeoutSeconds = 1
	userID := createBotTestUser(t, env.db, "auxto")
	bot := env.createBot(t, userID)

	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	maintRun := &model.BotMaintenanceRun{
		ID: uuid.NewString(), BotID: bot.ID,
		Kind: model.BotMaintenanceCompaction, TriggerKey: "int137-aux-timeout",
		Status:             model.BotMaintenanceRunning,
		ConfigSnapshotJSON: "{}", InputSnapshotJSON: "{}", ResultJSON: "{}",
		CreatedAt: time.Now().UTC(),
	}
	if err := env.db.Create(maintRun).Error; err != nil {
		t.Fatalf("seed maint run: %v", err)
	}

	start := time.Now()
	_, callErr := env.bots.auxModelRequest(context.Background(), botRow, maintRun,
		model.BotPurposeSummary, botRow.Model,
		[]openaiMessage{{Role: "user", Content: "摘要这段对话"}}, 64)
	if callErr == nil {
		t.Fatalf("挂住的上游应超时失败")
	}
	var timeout *botStreamTimeoutError
	if !errors.As(callErr, &timeout) {
		t.Fatalf("辅助请求超时应返回 *botStreamTimeoutError，实得 %v", callErr)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("总超时应长于看门狗（1s 首包），实际等了 %v", elapsed)
	}
	var req model.BotMaintenanceRequest
	if err := env.db.Where("maintenance_run_id = ?", maintRun.ID).First(&req).Error; err != nil {
		t.Fatalf("load maint request: %v", err)
	}
	if req.ErrorCode == nil || *req.ErrorCode != "model_stream_timeout" {
		t.Fatalf("辅助请求行应记 model_stream_timeout，实得 %v", req.ErrorCode)
	}
}

// ---------- 宽限停止：有进展则不硬杀；不取消别的 run 的槽 ----------

// seedMainRun 落一条主线 running run（不经模型），供停止链路单测使用。
func seedMainRun(t *testing.T, env *botTestEnv, botID string) *model.BotRun {
	t.Helper()
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "wake:" + uuid.NewString(),
		TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		SessionKind:        model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed main run: %v", err)
	}
	return run
}

// seedModelRequestOrdinal 直接落一条模型请求行，制造「run 已有进展」。
func seedModelRequestOrdinal(t *testing.T, env *botTestEnv, runID string, ordinal, inputThroughSeq int64) {
	t.Helper()
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: runID, Ordinal: ordinal,
		Status: model.BotModelRequestSucceeded, StartedAt: time.Now().UTC(),
		InputThroughSeq: inputThroughSeq,
	}
	if err := env.bots.requests.CreateTx(env.db, req); err != nil {
		t.Fatalf("seed model request: %v", err)
	}
}

// 宽限到期时 run 已带新输入发起了下一次请求（最大 ordinal 增长）：不硬取消、
// 不推进水位；重复点击（ordinal=-1）立即硬取消。
func TestBotHardStopGraceSkippedWhenProgressMade(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "stopprogress")
	bot := env.createBot(t, userID)
	run := seedMainRun(t, env, bot.ID)
	seedModelRequestOrdinal(t, env, run.ID, 1, 0)

	// 占住执行槽并绑定该 run，模拟正在跑的模型流。
	cancelCalled := false
	if env.bots.rt.acquire(bot.ID, nil) == nil {
		t.Fatalf("acquire slot")
	}
	cancel := context.CancelFunc(func() { cancelCalled = true })
	env.bots.rt.setCancel(bot.ID, cancel)
	env.bots.rt.bindRun(bot.ID, run.ID)

	// 受理时 ordinal=1，之后 run 又发起 ordinal=2 且该请求已包含停止消息
	// （input_through_seq 覆盖 stopSeq）：宽限到期不硬杀。
	seedModelRequestOrdinal(t, env, run.ID, 2, 1)
	env.bots.hardStopRun(bot.ID, run.ID, 1, 1)
	if cancelCalled {
		t.Fatalf("宽限期内已有新请求，不得硬取消（否则丢掉停止之后的新消息）")
	}
	fresh := mustFindRun(t, env, run.ID)
	if fresh.InputThroughSeq != 0 {
		t.Fatalf("未硬杀时不得推进水位，实得 %d", fresh.InputThroughSeq)
	}
	if fresh.StopRequestedAt != nil {
		t.Fatalf("已有新请求时不得写 stop_requested（原子条件应落空）: %+v", fresh)
	}

	// 重复点击（acceptedOrdinal=-1）：跳过进展判断，立即硬取消并消费停止消息。
	env.bots.hardStopRun(bot.ID, run.ID, 1, -1)
	if !cancelCalled {
		t.Fatalf("重复点击应立即硬取消")
	}
	fresh = mustFindRun(t, env, run.ID)
	if fresh.StopRequestedAt == nil || fresh.InputThroughSeq != 1 {
		t.Fatalf("硬取消应置 stop_requested 并推进水位到停止消息 seq: %+v", fresh)
	}
}

// 循环在停止消息追加前就构建好上下文、只是登记晚了一步：该请求 ordinal 更大
// 但 input_through_seq 未覆盖停止消息，不算「已把停止交给模型」——宽限到期
// 仍要硬取消，否则停止会被拖到看门狗超时。
func TestBotHardStopCancelsRequestBuiltBeforeStop(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "stopoldctx")
	bot := env.createBot(t, userID)
	run := seedMainRun(t, env, bot.ID)
	seedModelRequestOrdinal(t, env, run.ID, 1, 0)

	cancelCalled := false
	if env.bots.rt.acquire(bot.ID, nil) == nil {
		t.Fatalf("acquire slot")
	}
	env.bots.rt.setCancel(bot.ID, context.CancelFunc(func() { cancelCalled = true }))
	env.bots.rt.bindRun(bot.ID, run.ID)

	// ordinal=2 但 input_through_seq=0：上下文在停止消息（seq=1）之前构建。
	seedModelRequestOrdinal(t, env, run.ID, 2, 0)
	env.bots.hardStopRun(bot.ID, run.ID, 1, 1)
	if !cancelCalled {
		t.Fatalf("更早构建的上下文不算进展，宽限到期应硬取消")
	}
	fresh := mustFindRun(t, env, run.ID)
	if fresh.StopRequestedAt == nil {
		t.Fatalf("硬取消应写 stop_requested: %+v", fresh)
	}
}

// 硬停止不得取消到别的 run：槽被重入换绑成新 run 后，旧 run 的取消指令落空。
// cancelBotContext 只读 run 的会话判别与 id，不查库，两条 run 在内存构造即可
// （部分唯一索引每 bot 只放行一条未结束主线 run）。
func TestBotCancelBotContextOnlyCancelsBoundRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "stopslot")
	bot := env.createBot(t, userID)
	oldRun := &model.BotRun{ID: uuid.NewString(), BotID: bot.ID, SessionKind: model.BotSessionKindMain}
	newRun := &model.BotRun{ID: uuid.NewString(), BotID: bot.ID, SessionKind: model.BotSessionKindMain}

	cancelCalled := false
	if env.bots.rt.acquire(bot.ID, nil) == nil {
		t.Fatalf("acquire slot")
	}
	env.bots.rt.setCancel(bot.ID, context.CancelFunc(func() { cancelCalled = true }))
	env.bots.rt.bindRun(bot.ID, oldRun.ID)

	// 槽仍绑定旧 run：取消旧 run 生效。
	env.bots.cancelBotContext(bot.ID, oldRun)
	if !cancelCalled {
		t.Fatalf("槽绑定旧 run 时应取消旧 run 的 context")
	}

	// 重入换绑成新 run：旧 run 的硬停止不得再取消。
	cancelCalled = false
	env.bots.rt.bindRun(bot.ID, newRun.ID)
	env.bots.cancelBotContext(bot.ID, oldRun)
	if cancelCalled {
		t.Fatalf("槽已换绑新 run，不得取消到别人的 context")
	}
	env.bots.cancelBotContext(bot.ID, newRun)
	if !cancelCalled {
		t.Fatalf("槽绑定新 run 时应取消新 run 的 context")
	}
}

// ---------- 水位只升不降 ----------

// 硬停止与主线循环并发写水位时，持旧 seq 的一方不得把已推进的水位覆盖回去。
func TestBotInputWatermarksOnlyMoveForward(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "wm")
	bot := env.createBot(t, userID)
	run := seedMainRun(t, env, bot.ID)

	if ok, err := env.bots.runs.AdvanceInputThroughSeq(run.ID, 5); err != nil || !ok {
		t.Fatalf("advance 5: %v %v", ok, err)
	}
	if ok, err := env.bots.runs.AdvanceInputThroughSeq(run.ID, 3); err != nil || ok {
		t.Fatalf("旧 seq 3 不得覆盖 5: %v %v", ok, err)
	}
	fresh := mustFindRun(t, env, run.ID)
	if fresh.InputThroughSeq != 5 {
		t.Fatalf("run 水位应保持 5，实得 %d", fresh.InputThroughSeq)
	}

	if err := env.bots.bots.SetOfferedInputSeq(bot.ID, 5); err != nil {
		t.Fatalf("set offered 5: %v", err)
	}
	if err := env.bots.bots.SetOfferedInputSeq(bot.ID, 3); err != nil {
		t.Fatalf("set offered 3: %v", err)
	}
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	if botRow.OfferedInputSeq != 5 {
		t.Fatalf("offered 水位应保持 5，实得 %d", botRow.OfferedInputSeq)
	}
}

// ---------- 熔断：窗口不延长、迟到失败不重开 ----------

// 同一熔断窗口内的第二次失败：until 不延长、reason 不变——去重键钉在原
// 窗口上，不会产生第二条主线事件。
func TestBotCircuitWindowNotExtendedOnRepeatFailure(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "circwindow")
	providerID := env.createProvider(t, userID, "sk-x-0000")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "b", ProviderID: providerID, Model: "fake-model"})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	env.bots.openProviderCircuitOnFailure(botRow, run, "provider_auth_failed")
	first := circuitOf(t, env, providerID)
	if first.CircuitOpenUntil == nil {
		t.Fatalf("首次失败应打开熔断")
	}

	// 更晚的二次失败：窗口保持原值（不延长、不换去重键）。
	env.bots.openProviderCircuitOnFailure(botRow, run, "provider_quota_exhausted")
	second := circuitOf(t, env, providerID)
	if !second.CircuitOpenUntil.Equal(*first.CircuitOpenUntil) {
		t.Fatalf("窗口内二次失败不得延长 until: first=%v second=%v", *first.CircuitOpenUntil, *second.CircuitOpenUntil)
	}
	if second.CircuitReason != first.CircuitReason {
		t.Fatalf("窗口内二次失败不得改写 reason: %s -> %s", first.CircuitReason, second.CircuitReason)
	}

	// 窗口已过后再失败：允许重新打开（新窗口、新去重键）。真实序列是「窗口
	// 过后下一次计划运行又失败」——用新 run（StartedAt 晚于第一次熔断打开）；
	// 复用旧 run 会撞上「打开熔断本身也推进 provider.updated_at」的守卫。
	// 过期改写用 UpdateColumn 只动熔断列：Update 会连 updated_at 一起推进，
	// 那是「用户更新凭证」路径的行为。
	past := time.Now().UTC().Add(-time.Minute)
	if err := env.db.Model(&model.AIProvider{}).Where("id = ?", providerID).
		UpdateColumn("circuit_open_until", past).Error; err != nil {
		t.Fatalf("expire circuit: %v", err)
	}
	run2 := seedScheduleRun(t, env, bot.ID, task.ID)
	env.bots.openProviderCircuitOnFailure(botRow, run2, "provider_auth_failed")
	reopened := circuitOf(t, env, providerID)
	if reopened.CircuitOpenUntil == nil || !reopened.CircuitOpenUntil.After(time.Now().UTC()) {
		t.Fatalf("窗口过期后应能重新打开: %+v", reopened)
	}
}

// 迟到的失败：run 开始之后用户已更新过凭证（updated_at 后移），不得重新打开
// 熔断——那次失败针对的是已被换掉的旧配置。
func TestBotCircuitNotReopenedAfterCredentialUpdate(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "circstale")
	providerID := env.createProvider(t, userID, "sk-x-0000")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "b", ProviderID: providerID, Model: "fake-model"})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	// 模拟用户在 run 开始后更新了凭证/地址（含测试连接、清除熔断的路径）：
	// 配置版本前移到 run 开始之后，迟到的旧凭证失败不得再熔断。
	if err := env.db.Model(&model.AIProvider{}).Where("id = ?", providerID).
		Update("config_version_at", time.Now().UTC().Add(2*time.Second)).Error; err != nil {
		t.Fatalf("touch provider: %v", err)
	}
	env.bots.openProviderCircuitOnFailure(botRow, run, "provider_auth_failed")
	if p := circuitOf(t, env, providerID); p.CircuitOpenUntil != nil {
		t.Fatalf("凭证已更新后迟到的失败不得重开熔断: %+v", p)
	}
}

// 熔断归属按 run 派发时冻结的 provider_id：用户中途给机器人换了 provider，
// 迟到失败不得打到新 provider 上。
func TestBotCircuitUsesRunFrozenProviderID(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "circfreeze")
	oldProviderID := env.createProvider(t, userID, "sk-old-0000")
	newProviderID := env.createProvider(t, userID, "sk-new-0000")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "b", ProviderID: oldProviderID, Model: "fake-model"})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)

	// run 派发时冻结的快照指向 old provider；之后机器人换绑到 new provider。
	snap := botSnapshotOf(mustBotOf(t, env, bot.ID))
	snap.ProviderID = oldProviderID
	raw, _ := json.Marshal(snap)
	if err := env.db.Model(&model.BotRun{}).Where("id = ?", run.ID).
		Update("config_snapshot_json", string(raw)).Error; err != nil {
		t.Fatalf("freeze snapshot: %v", err)
	}
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("provider_id", newProviderID).Error; err != nil {
		t.Fatalf("switch provider: %v", err)
	}

	// 收尾读 run 与真实路径一致：finishScheduleFailed 拿到的是循环入口从库里
	// 读出的 run（带派发时冻结的快照）。
	run = mustFindRun(t, env, run.ID)
	botRow := mustBotOf(t, env, bot.ID)
	env.bots.openProviderCircuitOnFailure(botRow, run, "provider_auth_failed")
	if p := circuitOf(t, env, newProviderID); p.CircuitOpenUntil != nil {
		t.Fatalf("迟到失败不得熔断新 provider: %+v", p)
	}
	if p := circuitOf(t, env, oldProviderID); p.CircuitOpenUntil == nil {
		t.Fatalf("应按 run 冻结的 provider 打开熔断")
	}
}

func mustBotOf(t *testing.T, env *botTestEnv, botID string) *model.Bot {
	t.Helper()
	b, err := env.bots.bots.Get(botID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	return b
}

// ---------- 测试连接：密钥不一致不清熔断 ----------

// 测试连接用未保存的新密钥探通：不清熔断（保存路径自会清除）；密钥与库存
// 一致时才清除。
func TestBotCircuitTestWithDifferentKeyDoesNotClear(t *testing.T) {
	srv := newFakeModelServer(t, fakeModelTurn{Text: "pong"})

	env := setupBotTestEnv(t)
	svc := newProviderTestService(t, env)
	repo := repository.NewAIProviderRepository(env.db)
	userID := createBotTestUser(t, env.db, "circtestkey")
	providerID := env.createProviderAt(t, userID, srv.URL(), "sk-stored-0000")
	until := time.Now().UTC().Add(30 * time.Minute)
	if _, err := repo.OpenCircuit(providerID, until, "provider_auth_failed", time.Now().UTC()); err != nil {
		t.Fatalf("open circuit: %v", err)
	}

	probe := func(key string) {
		t.Helper()
		res, err := svc.Test(userID, &TestAIProviderRequest{
			ProviderID: strPtrBot(providerID),
			CatalogID:  strPtrBot("custom"),
			BaseURL:    strPtrBot(srv.URL()),
			APIKey:     strPtrBot(key),
			Model:      strPtrBot("fake-model"),
		})
		if err != nil || !res.Ok {
			t.Fatalf("探测应成功: %v %+v", err, res)
		}
	}

	// 新密钥探通：不清熔断。
	probe("sk-fresh-9999")
	if p := circuitOf(t, env, providerID); p.CircuitOpenUntil == nil {
		t.Fatalf("测试连接用未保存的新密钥不得清除熔断")
	}
	// 库存同一密钥探通：清除。
	probe("sk-stored-0000")
	if p := circuitOf(t, env, providerID); p.CircuitOpenUntil != nil || p.CircuitReason != "" {
		t.Fatalf("密钥与库存一致的探测成功应清除熔断: %+v", p)
	}
}

// ---------- R6：压缩边界闸门 ----------

// 同一批 rows：flag=false（未发布过压缩）旧 toolset_updated 保持 system；
// flag=true（已发布过压缩）按 user 投影。发布水位由 PublishTx 写 1。
func TestBotLegacyToolsetProjectionGateByRenderVersion(t *testing.T) {
	raw := `{"event":"toolset_updated","detail":{"from_version":1,"to_version":2,"note":"旧会话"}}`
	rows := []model.BotMessage{
		{ID: "m1", Seq: 1, Kind: model.BotMessageKindLifecycle, ContentJSON: raw},
	}

	msgs, _, _, err := botProjectMessages(rows, nil, "", false)
	if err != nil {
		t.Fatalf("project (gate closed): %v", err)
	}
	if len(msgs) != 1 || msgs[0].OfSystem == nil {
		t.Fatalf("闸门关闭（未发布过压缩）必须保持 system 角色，实际 %+v", msgs)
	}
	want := "[工具集变更] 以下是平台可信系统事件（非用户发言）：\n" + raw
	if got := msgs[0].OfSystem.Content.OfString.Value; got != want {
		t.Fatalf("闸门关闭时投影正文必须逐字节不变:\n got=%q\nwant=%q", got, want)
	}

	msgs, _, _, err = botProjectMessages(rows, nil, "", true)
	if err != nil {
		t.Fatalf("project (gate open): %v", err)
	}
	if len(msgs) != 1 || msgs[0].OfUser == nil {
		t.Fatalf("闸门打开（已发布过压缩）必须按 user 角色，实际 %+v", msgs)
	}
	if !strings.HasPrefix(msgs[0].OfUser.Content.OfString.Value, "[平台事件]") {
		t.Fatalf("闸门打开后应带 [平台事件] 前缀，实际 %q", msgs[0].OfUser.Content.OfString.Value)
	}
}

// PublishTx 的建行与更新都把 LifecycleRenderVersion 写 1：任何一次压缩发布
// （含 fold-only）之后闸门即打开。
func TestBotPublishTxOpensLifecycleRenderGate(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "r6gate")
	bot := env.createBot(t, userID)
	repo := repository.NewBotContextRepository(env.db)

	if _, err := repo.PublishTx(env.db, bot.ID, 0, 3, "摘要", "[]", "{}", time.Now().UTC()); err != nil {
		t.Fatalf("first publish: %v", err)
	}
	state, err := repo.Find(bot.ID)
	if err != nil {
		t.Fatalf("find state: %v", err)
	}
	if state.LifecycleRenderVersion != 1 {
		t.Fatalf("首次发布应写 LifecycleRenderVersion=1，实得 %d", state.LifecycleRenderVersion)
	}
	if _, err := repo.PublishTx(env.db, bot.ID, state.Generation, 5, "摘要2", "[]", "{}", time.Now().UTC()); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	state, err = repo.Find(bot.ID)
	if err != nil {
		t.Fatalf("find state 2: %v", err)
	}
	if state.LifecycleRenderVersion != 1 {
		t.Fatalf("更新发布后 LifecycleRenderVersion 应为 1，实得 %d", state.LifecycleRenderVersion)
	}
}

// 槽尚未绑定 run（bindSlotRun 之前的窗口）时，取消指令必须落空——旧停止晚到
// 一步不能取消刚装上、还没绑定的新 context。
func TestBotCancelBotContextSkipsUnboundSlot(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "stopunbound")
	bot := env.createBot(t, userID)
	run := &model.BotRun{ID: uuid.NewString(), BotID: bot.ID, SessionKind: model.BotSessionKindMain}

	cancelCalled := false
	if env.bots.rt.acquire(bot.ID, nil) == nil {
		t.Fatalf("acquire slot")
	}
	env.bots.rt.setCancel(bot.ID, context.CancelFunc(func() { cancelCalled = true }))
	env.bots.cancelBotContext(bot.ID, run)
	if cancelCalled {
		t.Fatalf("槽还没绑定 run 时不得取消：晚到的停止不能打中新 context")
	}
}

// 上游正常关闭但没有 finish_reason：按断流失败处理。已输出正文时不得重试，
// 半截回复不得提交为成功（INT-137 补齐普通 EOF 验收）。
func TestBotEOFWithoutFinishReasonFails(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "半截回答", NoFinish: true})
	userID := createBotTestUser(t, env.db, "eofnofinish")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "你好", ClientMessageID: "eof1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "eof run settled", 10*time.Second, func() bool { return env.botSettled(bot.ID) })

	runs, err := env.bots.ListRuns(userID, bot.ID, 5)
	if err != nil || len(runs) == 0 {
		t.Fatalf("list runs: %v", err)
	}
	run := runs[0]
	if run.Status != string(model.BotRunStatusFailed) || run.ErrorCode == nil || *run.ErrorCode != botModelErrUnavailable {
		t.Fatalf("无完成标记的 EOF 应失败为 provider_unavailable，实得 %s %v", run.Status, run.ErrorCode)
	}
	var requests int64
	env.db.Model(&model.BotModelRequest{}).Where("run_id = ?", run.ID).Count(&requests)
	if requests != 1 {
		t.Fatalf("已输出片段后不得重试，实得 %d 条请求", requests)
	}
	var assistants int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?", bot.ID, model.BotMessageKindAssistant).Count(&assistants)
	if assistants != 0 {
		t.Fatalf("半截回复不得提交为 assistant 消息，实得 %d 条", assistants)
	}
}

// 请求登记参与停止协议：run 已请求停止时不得再登记新请求（否则停止 UPDATE
// 命中后仍会发起请求并推进水位，停止之后的新输入就再没人处理）。
func TestBotBeginModelRequestRefusesAfterStop(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "stopreg")
	bot := env.createBot(t, userID)
	run := seedMainRun(t, env, bot.ID)
	now := time.Now().UTC()
	if err := env.db.Model(&model.BotRun{}).Where("id = ?", run.ID).
		Update("stop_requested_at", now).Error; err != nil {
		t.Fatalf("mark stop: %v", err)
	}
	built := &botBuiltContext{InputThroughSeq: 1}
	if _, err := env.bots.beginModelRequest(mustBotOf(t, env, bot.ID), run, built); !errors.Is(err, errBotRunStopBeforeRequest) {
		t.Fatalf("已请求停止时登记请求应被拒绝，实得 %v", err)
	}
	var n int64
	env.db.Model(&model.BotModelRequest{}).Where("run_id = ?", run.ID).Count(&n)
	if n != 0 {
		t.Fatalf("被拒绝的登记不得落请求行，实得 %d", n)
	}
}

package service

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
)

// 定时任务熔断（INT-137）：schedule run 因认证失效/额度用完失败打开 provider
// 熔断；窗口内到点记 skipped 且同一次熔断只往主线留一条事件；用户更新凭证或
// 测试连接成功清除。5xx 与主线失败不熔断。

// circuitOf 读 provider 行的熔断标记。
func circuitOf(t *testing.T, env *botTestEnv, providerID string) *model.AIProvider {
	t.Helper()
	var p model.AIProvider
	if err := env.db.Where("id = ?", providerID).First(&p).Error; err != nil {
		t.Fatalf("load provider: %v", err)
	}
	return &p
}

// countCircuitEvents 统计主线上钉了熔断去重键的 schedule_event 条数。
func countCircuitEvents(t *testing.T, env *botTestEnv, botID string) int64 {
	t.Helper()
	var n int64
	if err := env.db.Model(&model.BotMessage{}).Where(
		"bot_id = ? AND kind = ? AND dedup_key LIKE ?",
		botID, model.BotMessageKindScheduleEvent, "circuit:provider:%").Count(&n).Error; err != nil {
		t.Fatalf("count circuit events: %v", err)
	}
	return n
}

// 认证失败的 schedule run 打开 provider 熔断；窗口内到点记 skipped，同一熔断
// 只留一条主线事件，窗口内重复到点不新增事件。
func TestBotCircuitOpensAndSkipsWithSingleEvent(t *testing.T) {
	env := setupBotTestEnv(t)
	// 所有请求统一 401：模型调用按 provider_auth_failed 收尾。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"authentication_error","code":"invalid_api_key"}}`)
	}))
	t.Cleanup(srv.Close)

	userID := createBotTestUser(t, env.db, "circ")
	providerID := env.createProviderAt(t, userID, srv.URL, "sk-dead-0000")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "c", ProviderID: providerID, Model: "m"})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	due := time.Now().UTC().Add(-3 * time.Second)
	task := seedScheduleTask(t, env, bot.ID, func(tk *model.BotScheduleTask) {
		tk.NeedsReport = true
		tk.NextRunAt = &due
	})

	env.bots.scheduleTick(time.Now().UTC())
	var run model.BotRun
	if err := env.db.Where("schedule_task_id = ?", task.ID).First(&run).Error; err != nil {
		t.Fatalf("run: %v", err)
	}
	run = *waitForScheduleRunFinished(t, env, run.ID)
	if run.Status != model.BotRunStatusFailed || run.ErrorCode == nil || *run.ErrorCode != "provider_auth_failed" {
		t.Fatalf("401 应落 provider_auth_failed，实得 %+v", run)
	}

	// 熔断打开：circuit_open_until 晚于现在，reason 记分类码。
	var provider *model.AIProvider
	waitForBot(t, "provider 熔断打开", 5*time.Second, func() bool {
		provider = circuitOf(t, env, providerID)
		return provider.CircuitOpenUntil != nil
	})
	if provider.CircuitReason != "provider_auth_failed" || !provider.CircuitOpenUntil.After(time.Now().UTC()) {
		t.Fatalf("熔断标记不符: %+v", provider)
	}

	// 熔断期间到点：记 skipped，摘要写明原因与恢复方式。
	due2 := time.Now().UTC().Add(-3 * time.Second)
	if err := env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).
		Update("next_run_at", due2).Error; err != nil {
		t.Fatalf("reset due: %v", err)
	}
	env.bots.scheduleTick(time.Now().UTC())
	var skipped model.BotRun
	if err := env.db.Where("schedule_task_id = ? AND status = ?", task.ID, model.BotRunStatusSkipped).
		First(&skipped).Error; err != nil {
		t.Fatalf("skipped run: %v", err)
	}
	if skipped.FinishedAt == nil || !strings.Contains(skipped.Summary, "认证失败") ||
		!strings.Contains(skipped.Summary, "熔断至") {
		t.Fatalf("熔断跳过摘要应写明原因与恢复方式: %+v", skipped)
	}
	// 同一次熔断只留一条主线事件（失败事件 + 熔断事件共 2 条 schedule_event）。
	if n := countCircuitEvents(t, env, bot.ID); n != 1 {
		t.Fatalf("同一熔断应只留一条事件，实得 %d", n)
	}
	if n, _ := countScheduleEvents(t, env, bot.ID); n != 2 {
		t.Fatalf("主线应有失败+熔断两条事件，实得 %d", n)
	}

	// 窗口内再次到点：仍 skipped，事件不新增。
	due3 := time.Now().UTC().Add(-3 * time.Second)
	if err := env.db.Model(&model.BotScheduleTask{}).Where("id = ?", task.ID).
		Update("next_run_at", due3).Error; err != nil {
		t.Fatalf("reset due3: %v", err)
	}
	env.bots.scheduleTick(time.Now().UTC())
	var skippedCount int64
	env.db.Model(&model.BotRun{}).Where("schedule_task_id = ? AND status = ?",
		task.ID, model.BotRunStatusSkipped).Count(&skippedCount)
	if skippedCount != 2 {
		t.Fatalf("两次到点都应记 skipped，实得 %d", skippedCount)
	}
	if n := countCircuitEvents(t, env, bot.ID); n != 1 {
		t.Fatalf("重复到点不应新增熔断事件，实得 %d", n)
	}
	if n, _ := countScheduleEvents(t, env, bot.ID); n != 2 {
		t.Fatalf("重复到点不应新增主线事件，实得 %d", n)
	}
}

// 5xx（provider_unavailable）失败不打开熔断：再试可能会好，交给重试而不是熔断。
func TestBotCircuitNotOpenedOnUnavailable(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "circ5xx")
	providerID := env.createProvider(t, userID, "sk-x-0000")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "b", ProviderID: providerID, Model: "fake-model"})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	task := seedScheduleTask(t, env, bot.ID, nil)
	run := seedScheduleRun(t, env, bot.ID, task.ID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	env.bots.finishScheduleFailed(botRow, run, "provider_unavailable",
		errors.New("AI 提供商服务暂时不可用，请稍后重试"))
	if p := circuitOf(t, env, providerID); p.CircuitOpenUntil != nil || p.CircuitReason != "" {
		t.Fatalf("5xx 不应打开熔断: %+v", p)
	}
}

// 主线 run 的认证失败不打开熔断：主线不熔断（用户可能已充值），熔断只管
// 定时任务。
func TestBotCircuitNotOpenedOnMainlineFailure(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "circmain")
	providerID := env.createProvider(t, userID, "sk-x-0000")
	bot, err := env.bots.Create(userID, &CreateBotRequest{Name: "b", ProviderID: providerID, Model: "fake-model"})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "mainline-auth",
		TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		SessionKind:        model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("load bot: %v", err)
	}
	env.bots.finishFailed(botRow, run, "provider_auth_failed",
		errors.New("AI 提供商认证失败：密钥无效或已失效"))
	if fresh := mustFindRun(t, env, run.ID); fresh.Status != model.BotRunStatusFailed {
		t.Fatalf("主线 run 应落 failed，实得 %s", fresh.Status)
	}
	if p := circuitOf(t, env, providerID); p.CircuitOpenUntil != nil || p.CircuitReason != "" {
		t.Fatalf("主线失败不应打开熔断: %+v", p)
	}
}

// 更新 provider 时提供 api_key 或 base_url 即清除熔断；只改名字不动熔断。
func TestBotCircuitClearedByUpdate(t *testing.T) {
	env := setupBotTestEnv(t)
	svc := newProviderTestService(t, env)
	repo := repository.NewAIProviderRepository(env.db)
	userID := createBotTestUser(t, env.db, "circclear")
	providerID := env.createProvider(t, userID, "sk-old-0000")
	until := time.Now().UTC().Add(30 * time.Minute)
	if _, err := repo.OpenCircuit(providerID, until, "provider_auth_failed"); err != nil {
		t.Fatalf("open circuit: %v", err)
	}

	// 只改名字：不算换凭证，熔断保持。
	if _, err := svc.Update(userID, providerID, &UpdateAIProviderRequest{Name: strPtrBot("只是改名")}); err != nil {
		t.Fatalf("update name: %v", err)
	}
	if p := circuitOf(t, env, providerID); p.CircuitOpenUntil == nil {
		t.Fatalf("只改名字不应清除熔断")
	}

	// 换 key：清除熔断。
	if _, err := svc.Update(userID, providerID, &UpdateAIProviderRequest{APIKey: strPtrBot("sk-new-1111")}); err != nil {
		t.Fatalf("update key: %v", err)
	}
	if p := circuitOf(t, env, providerID); p.CircuitOpenUntil != nil || p.CircuitReason != "" {
		t.Fatalf("换 key 应清除熔断: %+v", p)
	}

	// 改 base_url（custom provider）：同样清除。
	if _, err := repo.OpenCircuit(providerID, until, "provider_quota_exhausted"); err != nil {
		t.Fatalf("reopen circuit: %v", err)
	}
	if _, err := svc.Update(userID, providerID, &UpdateAIProviderRequest{BaseURL: strPtrBot("https://gw2.internal.example.com/v1")}); err != nil {
		t.Fatalf("update base_url: %v", err)
	}
	if p := circuitOf(t, env, providerID); p.CircuitOpenUntil != nil || p.CircuitReason != "" {
		t.Fatalf("改 base_url 应清除熔断: %+v", p)
	}
}

// 测试连接带 provider_id 且探测成功时清除熔断；非归属人调用返回 404 且不清除。
func TestBotCircuitClearedByTestSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"demo-model"}]}`)
			return
		}
		writeFakeChatSSE(w, "pong")
	}))
	t.Cleanup(srv.Close)

	env := setupBotTestEnv(t)
	svc := newProviderTestService(t, env)
	repo := repository.NewAIProviderRepository(env.db)
	userID := createBotTestUser(t, env.db, "circtest")
	providerID := env.createProviderAt(t, userID, srv.URL, "sk-x-0000")
	until := time.Now().UTC().Add(30 * time.Minute)
	if _, err := repo.OpenCircuit(providerID, until, "provider_auth_failed"); err != nil {
		t.Fatalf("open circuit: %v", err)
	}

	req := &TestAIProviderRequest{
		ProviderID: strPtrBot(providerID),
		CatalogID:  strPtrBot("custom"),
		BaseURL:    strPtrBot(srv.URL),
		APIKey:     strPtrBot("sk-x-0000"),
		Model:      strPtrBot("demo-model"),
	}
	// 非归属人：404，不探测也不清除。
	other := createBotTestUser(t, env.db, "circother")
	if _, err := svc.Test(other, req); err != errs.ErrAIProviderNotFound {
		t.Fatalf("非归属人应 40433，实得 %v", err)
	}
	if p := circuitOf(t, env, providerID); p.CircuitOpenUntil == nil {
		t.Fatalf("非归属人调用不应清除熔断")
	}
	// 不存在的 provider 同样 404。
	if _, err := svc.Test(userID, &TestAIProviderRequest{
		ProviderID: strPtrBot("missing-provider"), APIKey: strPtrBot("sk-x"),
	}); err != errs.ErrAIProviderNotFound {
		t.Fatalf("不存在的 provider 应 40433，实得 %v", err)
	}

	// 归属人探测成功：清除熔断。
	res, err := svc.Test(userID, req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if !res.Ok {
		t.Fatalf("探测应成功: %+v", res)
	}
	if p := circuitOf(t, env, providerID); p.CircuitOpenUntil != nil || p.CircuitReason != "" {
		t.Fatalf("探测成功应清除熔断: %+v", p)
	}
}

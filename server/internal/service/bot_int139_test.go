package service

// INT-139 回归测试：R7 rune 安全截断、R8 记忆写入者按会话判别、
// R11 前缀缓存窗口内的激活门控与如实通知、R12 残缺工具调用流的
// 合成/丢弃口径、目录计价的成本估算。

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
)

// ---------- R7：错误截断按 rune，不切 UTF-8 ----------

func TestBotErrMessageTruncatesByRune(t *testing.T) {
	// 600 个中文字符 ×3 字节：按字节切会在第 500 字节处切出半个汉字。
	long := strings.Repeat("错", 600)
	got := botErrMessage(errors.New(long))
	if !utf8.ValidString(got) {
		t.Fatalf("截断结果必须是合法 UTF-8")
	}
	if n := utf8.RuneCountInString(got); n != 500 {
		t.Fatalf("应按字符截到 500，实得 %d", n)
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("不得含替换字符: %.40s", got)
	}
	// 短串原样返回。
	short := "plain error"
	if botErrMessage(errors.New(short)) != short {
		t.Fatalf("短串不应被截断")
	}
}

func TestBotProbeErrTextTruncatesByRune(t *testing.T) {
	long := strings.Repeat("探", 400)
	got := botProbeErrText(errors.New(long))
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != 300 {
		t.Fatalf("探测错误应按 300 字符截断，实得 %d rune", utf8.RuneCountInString(got))
	}
}

// ---------- R8：记忆写入者按 run 会话判别 ----------

func seedInt139RunReqCall(t *testing.T, env *botTestEnv, botID, kind string) *model.BotToolCall {
	t.Helper()
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "int139-" + uuid.NewString(),
		Status: model.BotRunStatusRunning, SessionKind: kind,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	req := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, StartedAt: now,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4",
	}
	if err := env.db.Create(req).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
	return &model.BotToolCall{ID: uuid.NewString(), BotID: botID, RequestID: req.ID}
}

func TestBotMemoryWriterFollowsSessionKind(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "memw")
	bot := env.createBot(t, userID)

	content := "记一条"
	for _, tc := range []struct {
		kind string
		path string
		want model.BotMemoryWriterKind
	}{
		{model.BotSessionKindMain, "USER.md", model.BotMemoryWriterMain},
		{model.BotSessionKindSchedule, "MEMORY.md", model.BotMemoryWriterScheduled},
	} {
		call := seedInt139RunReqCall(t, env, bot.ID, tc.kind)
		if _, err := env.bots.execMemoryWrite(call, &botMemoryArgs{
			Action: "create", Path: tc.path, Content: &content,
		}); err != nil {
			t.Fatalf("execMemoryWrite(%s): %v", tc.kind, err)
		}
		var ver model.BotMemoryVersion
		err := env.db.Joins("JOIN bot_memory_files f ON f.id = bot_memory_versions.file_id").
			Where("f.bot_id = ? AND f.path = ?", bot.ID, tc.path).
			Order("bot_memory_versions.version DESC").First(&ver).Error
		if err != nil {
			t.Fatalf("读记忆版本(%s): %v", tc.kind, err)
		}
		if ver.WriterKind != tc.want {
			t.Fatalf("%s 会话写入者应为 %s，实得 %s", tc.kind, tc.want, ver.WriterKind)
		}
	}
}

// ---------- R12：残缺工具调用流 ----------

func TestBotStreamChatSynthesizesMissingToolCallID(t *testing.T) {
	f := newFakeModelServer(t, fakeModelTurn{
		ToolCalls: []fakeModelToolCall{
			{ID: "", Name: "fs_project", Args: `{"action":"list","page":1,"page_size":10}`},
		},
	})
	client := botNewClient(f.URL(), "sk-test-abcdef123456")
	reply, err := botStreamChat(context.Background(), &client, int137DirectRequest(),
		botStreamTimeouts{FirstChunk: 5 * time.Second, Idle: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(reply.ToolCalls) != 1 {
		t.Fatalf("缺 id 的工具调用应保留执行，实得 %d 个", len(reply.ToolCalls))
	}
	if reply.ToolCalls[0].ID == "" {
		t.Fatalf("缺 id 应合成平台 id")
	}
	if reply.SynthesizedToolCallIDs != 1 {
		t.Fatalf("合成计数应为 1，实得 %d", reply.SynthesizedToolCallIDs)
	}
}

func TestBotStreamChatDropsNamelessToolCall(t *testing.T) {
	f := newFakeModelServer(t, fakeModelTurn{
		ToolCalls: []fakeModelToolCall{
			{ID: "call-x", Name: "", Args: `{"action":"list"}`},
			{ID: "call-ok", Name: "fs_project", Args: `{"action":"list","page":1,"page_size":10}`},
		},
	})
	client := botNewClient(f.URL(), "sk-test-abcdef123456")
	reply, err := botStreamChat(context.Background(), &client, int137DirectRequest(),
		botStreamTimeouts{FirstChunk: 5 * time.Second, Idle: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].ID != "call-ok" {
		t.Fatalf("缺名调用应丢弃、正常调用保留: %+v", reply.ToolCalls)
	}
	if reply.DroppedToolCalls != 1 {
		t.Fatalf("丢弃计数应为 1，实得 %d", reply.DroppedToolCalls)
	}
}

// 端到端：上游发缺 id 的工具调用 → 合成 id、照常执行、协议回复配对完整。
func TestBotRunToolCallSynthesizedIDExecuted(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{
			{ID: "", Name: "fs_project", Args: `{"action":"list","page":1,"page_size":10}`},
		}},
		fakeModelTurn{Text: "查完了", Usage: &fakeModelUsage{Prompt: 8, Completion: 3, Total: 11}},
	)
	userID := createBotTestUser(t, env.db, "r12e2e")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "r12-1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "缺 id 调用收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })

	run := lastRunOf(t, env, bot.ID)
	if run.Status != model.BotRunStatusCompleted {
		t.Fatalf("合成 id 的调用应正常跑完，实得 %s", run.Status)
	}
	var calls []model.BotToolCall
	env.db.Where("bot_id = ?", bot.ID).Find(&calls)
	if len(calls) != 1 {
		t.Fatalf("应恰好一条工具调用记录，实得 %d", len(calls))
	}
	if calls[0].ProviderToolCallID == "" || !strings.HasPrefix(calls[0].ProviderToolCallID, "call_") {
		t.Fatalf("provider_tool_call_id 应为合成 id，实得 %q", calls[0].ProviderToolCallID)
	}
	if calls[0].Status != model.BotCallStatusSucceeded {
		t.Fatalf("合成 id 的调用应执行成功，实得 %s", calls[0].Status)
	}
}

// ---------- R11：前缀缓存窗口内的激活门控 ----------

func int139SeedMainRequest(t *testing.T, env *botTestEnv, botID string, kind string, startedAt time.Time) {
	t.Helper()
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "r11-" + uuid.NewString(),
		Status: model.BotRunStatusCompleted, SessionKind: kind,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	// 窗口热度按 finished_at 量（上游缓存 TTL 从服务完成起算）：种子的请求
	// 都已结束，finished 与 started 同时刻画，进行中请求走 nil。
	finished := startedAt.Add(2 * time.Second)
	if err := env.db.Create(&model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestSucceeded, StartedAt: startedAt, FinishedAt: &finished,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4",
	}).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}
}

func TestMainlinePrefixCacheExpired(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "r11w")

	// 各情形用独立机器人：口径只看「最近一次主线请求」，混在同一 bot 上会
	// 互相覆盖。
	fresh := env.createBot(t, userID)
	if !env.bots.mainlinePrefixCacheExpired(fresh.ID) {
		t.Fatalf("无主线请求应可激活")
	}
	schedOnly := env.createBot(t, userID)
	int139SeedMainRequest(t, env, schedOnly.ID, model.BotSessionKindSchedule, time.Now().UTC())
	if !env.bots.mainlinePrefixCacheExpired(schedOnly.ID) {
		t.Fatalf("仅定时任务请求不计入主线窗口")
	}
	recent := env.createBot(t, userID)
	int139SeedMainRequest(t, env, recent.ID, model.BotSessionKindMain, time.Now().UTC())
	if env.bots.mainlinePrefixCacheExpired(recent.ID) {
		t.Fatalf("窗口内请求应判为推迟")
	}
	old := env.createBot(t, userID)
	int139SeedMainRequest(t, env, old.ID, model.BotSessionKindMain,
		time.Now().UTC().Add(-env.cfg.Bots.Runtime.PromptCacheRetain()-time.Minute))
	if !env.bots.mainlinePrefixCacheExpired(old.ID) {
		t.Fatalf("窗口外请求应判为可激活")
	}
}

// finished_at 为 nil 的行不是「在飞请求」——入口判定时本 run 已持有 bot 槽，
// 残留 nil 行来自 panic/中断收尾（INT-139 复审）。热度按 started_at 量：
// 刚 panic 的请求仍算热，几天前的残留不得把激活无限推迟。
func TestMainlinePrefixCacheExpiredUnfinishedLeftover(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "r11nil")

	seedRaw := func(botID string, startedAt time.Time, finishedAt *time.Time, errCode string) {
		run := &model.BotRun{
			ID: uuid.NewString(), BotID: botID,
			TriggerSource: model.BotTriggerUserMessage, TriggerKey: "r11r-" + uuid.NewString(),
			Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
			ConfigSnapshotJSON: "{}", CreatedAt: startedAt, StartedAt: &startedAt, FinishedAt: &startedAt,
		}
		if err := env.db.Create(run).Error; err != nil {
			t.Fatalf("seed run: %v", err)
		}
		req := &model.BotModelRequest{
			ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
			Status: model.BotModelRequestFailed, StartedAt: startedAt, FinishedAt: finishedAt,
			PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4",
		}
		if errCode != "" {
			req.ErrorCode = &errCode
		}
		if err := env.db.Create(req).Error; err != nil {
			t.Fatalf("seed request: %v", err)
		}
	}

	retain := env.cfg.Bots.Runtime.PromptCacheRetain()

	// panic 残留的未完成行：started_at 在窗口外 → 可激活。
	staleLeftover := env.createBot(t, userID)
	seedRaw(staleLeftover.ID, time.Now().UTC().Add(-retain-time.Minute), nil, "")
	if !env.bots.mainlinePrefixCacheExpired(staleLeftover.ID) {
		t.Fatalf("残留的未完成行不得无限推迟激活")
	}
	// 残留行的 started_at 仍在窗口内 → 推迟（上游刚被联系过）。
	freshLeftover := env.createBot(t, userID)
	seedRaw(freshLeftover.ID, time.Now().UTC(), nil, "")
	if env.bots.mainlinePrefixCacheExpired(freshLeftover.ID) {
		t.Fatalf("最近启动的请求残留仍应判为缓存热")
	}
	// server_restarted 的 finished_at 是恢复记账时刻：旧 started_at +
	// 新 finished_at 不得把中断很久的请求误判成刚热。
	restarted := env.createBot(t, userID)
	recentFinish := time.Now().UTC()
	seedRaw(restarted.ID, time.Now().UTC().Add(-retain-time.Minute), &recentFinish, "server_restarted")
	if !env.bots.mainlinePrefixCacheExpired(restarted.ID) {
		t.Fatalf("server_restarted 记账时刻不得被当作上游活动时间")
	}
}

// int139WaitNewRunSettled 等待「发送后新建」的 run 收尾：种子的历史 run 会
// 让 botSettled 提前为真，必须按 run 数增加等。
func int139WaitNewRunSettled(t *testing.T, env *botTestEnv, botID string, wantRuns int64) {
	t.Helper()
	waitForBot(t, "新 run 收尾", 15*time.Second, func() bool {
		var c int64
		env.db.Model(&model.BotRun{}).Where("bot_id = ?", botID).Count(&c)
		if c < wantRuns {
			return false
		}
		var active int64
		env.db.Model(&model.BotRun{}).Where("bot_id = ? AND status IN ?",
			botID, []string{string(model.BotRunStatusQueued), string(model.BotRunStatusRunning)}).Count(&active)
		return active == 0
	})
}

// 窗口内的过期机器人：run 入口不激活，通知如实写「下一个压缩点生效」，
// 每版本只告知一次。
func TestBotActivationDeferredInsideCacheWindow(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok", Usage: &fakeModelUsage{Prompt: 5, Completion: 2, Total: 7}})
	userID := createBotTestUser(t, env.db, "r11defer")
	bot := env.createBot(t, userID)
	// 先把库内行降回旧水位（含告知水位），再造一条窗口内的主线请求。
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Updates(map[string]interface{}{"toolset_version": 9, "last_told_version": 9}).Error; err != nil {
		t.Fatalf("降级水位: %v", err)
	}
	int139SeedMainRequest(t, env, bot.ID, model.BotSessionKindMain, time.Now().UTC())

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "r11-1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	int139WaitNewRunSettled(t, env, bot.ID, 2)

	var row model.Bot
	env.db.First(&row, "id = ?", bot.ID)
	if row.ToolsetVersion != 9 {
		t.Fatalf("窗口内不应在入口激活，toolset 应仍 9，实得 %d", row.ToolsetVersion)
	}
	var msgs []model.BotMessage
	env.db.Where("bot_id = ? AND dedup_key = ?", bot.ID, fmt.Sprintf("toolset_notice:%d", botToolsetVersion)).Find(&msgs)
	if len(msgs) != 1 {
		t.Fatalf("每个版本只应告知一次，实得 %d 条", len(msgs))
	}
	if !strings.Contains(msgs[0].ContentJSON, "下一个压缩点生效") ||
		!strings.Contains(msgs[0].ContentJSON, `"activation":"next_compaction"`) {
		t.Fatalf("推迟激活的通知应如实标注: %s", msgs[0].ContentJSON)
	}

	// 再来一条：窗口仍热（上一步的请求刚写过），水位不变、通知不重复。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "again", ClientMessageID: "r11-2"}); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	int139WaitNewRunSettled(t, env, bot.ID, 3)
	env.db.First(&row, "id = ?", bot.ID)
	if row.ToolsetVersion != 9 {
		t.Fatalf("持续活跃的会话仍应推迟激活，实得 %d", row.ToolsetVersion)
	}
	env.db.Where("bot_id = ? AND dedup_key = ?", bot.ID, fmt.Sprintf("toolset_notice:%d", botToolsetVersion)).Find(&msgs)
	if len(msgs) != 1 {
		t.Fatalf("通知不得重复发送，实得 %d 条", len(msgs))
	}
}

// 窗口外（旧请求或全新会话）：入口立即激活，通知写「已生效」。
func TestBotActivationImmediateOutsideCacheWindow(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok", Usage: &fakeModelUsage{Prompt: 5, Completion: 2, Total: 7}})
	userID := createBotTestUser(t, env.db, "r11now")
	bot := env.createBot(t, userID)
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Updates(map[string]interface{}{"toolset_version": 9, "last_told_version": 9}).Error; err != nil {
		t.Fatalf("降级水位: %v", err)
	}
	int139SeedMainRequest(t, env, bot.ID, model.BotSessionKindMain,
		time.Now().UTC().Add(-env.cfg.Bots.Runtime.PromptCacheRetain()-time.Minute))

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "r11-3"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	int139WaitNewRunSettled(t, env, bot.ID, 2)

	var row model.Bot
	env.db.First(&row, "id = ?", bot.ID)
	if row.ToolsetVersion != botToolsetVersion {
		t.Fatalf("窗口外应在入口激活到 v%d，实得 %d", botToolsetVersion, row.ToolsetVersion)
	}
	var msg model.BotMessage
	err := env.db.Where("bot_id = ? AND dedup_key = ?", bot.ID, fmt.Sprintf("toolset_notice:%d", botToolsetVersion)).First(&msg).Error
	if err != nil {
		t.Fatalf("应有升级通知: %v", err)
	}
	if !strings.Contains(msg.ContentJSON, "已生效") || !strings.Contains(msg.ContentJSON, `"activation":"active"`) {
		t.Fatalf("立即激活的通知应写「已生效」: %s", msg.ContentJSON)
	}
}

// ---------- 费用估算（目录计价） ----------

func TestBotCostEstimateFromCatalog(t *testing.T) {
	// deepseek-flash：input 0.15 / output 0.6 / cache_read 0.003（$/MTok）。
	// 1000 prompt、400 cached、200 output = (600×0.15 + 400×0.003 + 200×0.6)/1e6
	got, ok := botCostEstimate("deepseek", "deepseek-flash", 1000, 400, 200)
	if !ok {
		t.Fatalf("有价模型应可计价")
	}
	want := 0.0002112
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("成本不符：want %v got %v", want, got)
	}
	// 缓存价缺失时回退普通输入价：构造一个无价读模型不可行，用单价核验
	// 无价情形——未录入/自定义 provider 都不可计价。
	if _, ok := botCostEstimate("custom", "fake-model", 100, 0, 10); ok {
		t.Fatalf("自定义 provider 不得计价")
	}
	if _, ok := botCostEstimate("deepseek", "no-such-model", 100, 0, 10); ok {
		t.Fatalf("目录外的模型不得计价")
	}
}

// 端到端：catalog 归属为 deepseek 的机器人跑完一轮后，请求行与 run 行都落
// 成本，metrics 投影带出同一份值；自定义 provider 保持 NULL。
func TestBotRunCostEstimateEndToEnd(t *testing.T) {
	usage := &fakeModelUsage{Prompt: 1000, Completion: 200, Total: 1200, Cached: 400}
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok", Usage: usage})
	userID := createBotTestUser(t, env.db, "costbot")
	bot := env.createBot(t, userID)

	// 把 provider 指到目录里的 deepseek，模型换成有价模型。
	botRow, _ := env.bots.bots.Get(bot.ID)
	if err := env.db.Model(&model.AIProvider{}).Where("id = ?", botRow.ProviderID).
		Update("catalog_id", "deepseek").Error; err != nil {
		t.Fatalf("provider catalog: %v", err)
	}
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Update("model", "deepseek-flash").Error; err != nil {
		t.Fatalf("bot model: %v", err)
	}

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "cost-1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "计价 run 收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })

	run := lastRunOf(t, env, bot.ID)
	if run.Status != model.BotRunStatusCompleted {
		t.Fatalf("run 应完成: %s", run.Status)
	}
	want := 0.0002112
	if run.CostEstimate == nil || math.Abs(*run.CostEstimate-want) > 1e-9 {
		t.Fatalf("run 成本应为 %v，实得 %v", want, run.CostEstimate)
	}
	if run.CostSource == nil || *run.CostSource != "catalog" {
		t.Fatalf("cost_source 应为 catalog: %v", run.CostSource)
	}
	reqs := runModelRequests(t, env, run.ID)
	if len(reqs) != 1 || reqs[0].CostEstimate == nil || math.Abs(*reqs[0].CostEstimate-want) > 1e-9 {
		t.Fatalf("请求行成本不符: %+v", reqs)
	}

	// metrics 投影带同一份成本。
	snap, err := env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Metrics == nil || snap.Metrics.Request == nil ||
		snap.Metrics.Request.CostEstimate == nil ||
		math.Abs(*snap.Metrics.Request.CostEstimate-want) > 1e-9 {
		t.Fatalf("metrics 成本不符: %+v", snap.Metrics)
	}
}

// 无价/自定义 provider：成本保持 NULL，不落 0。
func TestBotRunCostEstimateNullForCustom(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok", Usage: &fakeModelUsage{Prompt: 5, Completion: 2, Total: 7}})
	userID := createBotTestUser(t, env.db, "costnone")
	bot := env.createBot(t, userID)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "cost-2"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "自定义 run 收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })
	run := lastRunOf(t, env, bot.ID)
	if run.CostEstimate != nil || run.CostSource != nil {
		t.Fatalf("自定义 provider 成本应为 NULL: %+v", run.CostEstimate)
	}
	reqs := runModelRequests(t, env, run.ID)
	if len(reqs) == 0 || reqs[len(reqs)-1].CostEstimate != nil {
		t.Fatalf("请求行成本应为 NULL")
	}
}

// ---------- 规则 8：平台提示词 v12 精简 ----------

func TestBotPlatformPromptV12SimplifiedScheduleRule(t *testing.T) {
	prompt := buildBotSystemPrompt("", "nonce-x")
	// 精简后的规则 8 保留核心指引，细节移交 fs_help 与工具描述。
	if !strings.Contains(prompt, "fs_help") {
		t.Fatalf("平台提示词应指向 fs_help")
	}
	// 已移出前缀的细节不应再出现在固定平台提示词里。
	for _, gone := range []string{"state_get", "state_set", "dry_run", "任务名与执行时间"} {
		if strings.Contains(prompt, gone) {
			t.Fatalf("规则 8 细节 %q 应已移出平台提示词", gone)
		}
	}
	if botPlatformPromptVersion != 12 {
		t.Fatalf("平台提示词版本应为 12，实得 %d", botPlatformPromptVersion)
	}
	// fs_help 仍承载完整细节。
	if !strings.Contains(botHelpText, "state_get") || !strings.Contains(botHelpText, "dry_run") {
		t.Fatalf("fs_help 应保留调度细节")
	}
}

// ---------- 复审回归（INT-139 R1） ----------

// schedule 会话用最新版提示词/工具集，但不得推进 bots 行激活水位——否则
// 主线窗口内的推迟判定被洗掉，「下一个压缩点生效」通知随之失真。
func TestScheduleSessionResolveKeepsRowWatermark(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "r11sched")
	created := env.createBot(t, userID)
	if err := env.db.Model(&model.Bot{}).Where("id = ?", created.ID).
		Updates(map[string]interface{}{
			"toolset_version": 9, "platform_prompt_version": 11,
			"system_prompt_text": "OLD-STALE-PROMPT",
		}).Error; err != nil {
		t.Fatalf("降级水位: %v", err)
	}
	bot, err := env.bots.bots.Get(created.ID)
	if err != nil {
		t.Fatalf("读 bot: %v", err)
	}

	if err := env.bots.activateBotPromptForSession(bot); err != nil {
		t.Fatalf("session 解析: %v", err)
	}
	if bot.ToolsetVersion != botToolsetVersion || bot.PlatformPromptVersion != botPlatformPromptVersion {
		t.Fatalf("schedule 会话内应推进到最新版本: toolset=%d prompt=%d",
			bot.ToolsetVersion, bot.PlatformPromptVersion)
	}
	if bot.SystemPromptText != buildBotSystemPrompt(bot.PersonaPrompt, bot.UntrustedNonce) {
		t.Fatalf("会话内提示词应按当前人设重建")
	}
	var row model.Bot
	env.db.First(&row, "id = ?", created.ID)
	if row.ToolsetVersion != 9 || row.PlatformPromptVersion != 11 ||
		row.SystemPromptText != "OLD-STALE-PROMPT" {
		t.Fatalf("会话内解析不得写回激活水位: toolset=%d prompt=%d",
			row.ToolsetVersion, row.PlatformPromptVersion)
	}
}

// usage 送达后断流的失败请求：tokens 与成本仍是计费事实，请求行必须落库
// （run 总账只累计成功请求，不变）。
func TestFailedRequestWithUsageStillPriced(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "costfail")
	created := env.createBot(t, userID)
	bot, err := env.bots.bots.Get(created.ID)
	if err != nil {
		t.Fatalf("读 bot: %v", err)
	}
	if err := env.db.Model(&model.AIProvider{}).Where("id = ?", bot.ProviderID).
		Update("catalog_id", "deepseek").Error; err != nil {
		t.Fatalf("provider catalog: %v", err)
	}
	bot.CatalogID = "deepseek"
	bot.Model = "deepseek-flash"

	now := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: created.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "r11-" + uuid.NewString(),
		Status: model.BotRunStatusRunning, SessionKind: model.BotSessionKindMain,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	row := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestRunning, StartedAt: now,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4",
	}
	if err := env.db.Create(row).Error; err != nil {
		t.Fatalf("seed request: %v", err)
	}

	reply := &botModelReply{UsageComplete: true, PromptTokens: 1000, CachedTokens: 400, CompletionTokens: 200, TotalTokens: 1200}
	env.bots.finishModelRequest(row, reply, errors.New("stream reset"), run, bot)

	var got model.BotModelRequest
	env.db.First(&got, "id = ?", row.ID)
	if got.Status != model.BotModelRequestFailed || got.PromptTokens == nil || *got.PromptTokens != 1000 {
		t.Fatalf("失败请求的 usage 应落库: %+v", got)
	}
	want := 0.0002112
	if got.CostEstimate == nil || math.Abs(*got.CostEstimate-want) > 1e-9 {
		t.Fatalf("失败请求的成本应照计: %v", got.CostEstimate)
	}
}

// 显式 0 价（免费模型）是已知价格：可计价、估算 $0，而非 NULL。
func TestBotCostEstimateHonorsExplicitZero(t *testing.T) {
	// agnes-2.0-flash：models.dev 目录里 input/output 均为显式 0。
	got, ok := botCostEstimate("agnes", "agnes-2.0-flash", 1000, 100, 50)
	if !ok {
		t.Fatalf("显式 0 价的模型应可计价")
	}
	if got != 0 {
		t.Fatalf("免费模型成本应为 0，实得 %v", got)
	}
}

// 推迟通知在激活落库时收口（INT-139 复审）：窗口内发出「下一个压缩点生效」
// 后，窗口一过入口激活，须追加一条 toolset_activated「已生效」事件——
// 账本 append-only 不改写原通知，否则激活后的请求会把推迟文案投影到新
// 提示词旁，模型读到的状态与实况相反。
func TestBotDeferredNoticeSettledOnActivation(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok", Usage: &fakeModelUsage{Prompt: 5, Completion: 2, Total: 7}})
	userID := createBotTestUser(t, env.db, "r11settle")
	bot := env.createBot(t, userID)
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Updates(map[string]interface{}{"toolset_version": 9, "last_told_version": 9}).Error; err != nil {
		t.Fatalf("降级水位: %v", err)
	}
	int139SeedMainRequest(t, env, bot.ID, model.BotSessionKindMain, time.Now().UTC())

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "r11-s1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	int139WaitNewRunSettled(t, env, bot.ID, 2)

	dedup := fmt.Sprintf("toolset_notice:%d", botToolsetVersion)
	var msg model.BotMessage
	if err := env.db.Where("bot_id = ? AND dedup_key = ?", bot.ID, dedup).First(&msg).Error; err != nil {
		t.Fatalf("应有推迟通知: %v", err)
	}
	if !strings.Contains(msg.ContentJSON, `"activation":"next_compaction"`) {
		t.Fatalf("首条通知应为推迟语义: %s", msg.ContentJSON)
	}

	// 把所有主线请求的 finished_at 拨回窗口外，下一条消息走入口激活。
	old := time.Now().UTC().Add(-env.cfg.Bots.Runtime.PromptCacheRetain() - time.Minute)
	if err := env.db.Model(&model.BotModelRequest{}).
		Where("run_id IN (SELECT id FROM bot_runs WHERE bot_id = ?)", bot.ID).
		Update("finished_at", old).Error; err != nil {
		t.Fatalf("回拨请求时间: %v", err)
	}
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "again", ClientMessageID: "r11-s2"}); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	int139WaitNewRunSettled(t, env, bot.ID, 3)

	var row model.Bot
	env.db.First(&row, "id = ?", bot.ID)
	if row.ToolsetVersion != botToolsetVersion {
		t.Fatalf("窗口外应激活到 v%d，实得 %d", botToolsetVersion, row.ToolsetVersion)
	}
	// 账本是 append-only：原通知保持「下一个压缩点生效」不动，激活以追加的
	// toolset_activated 事件收口，模型按序读到的是真实时间线。
	env.db.First(&msg, "id = ?", msg.ID)
	if !strings.Contains(msg.ContentJSON, `"activation":"next_compaction"`) {
		t.Fatalf("原通知不应被改写: %s", msg.ContentJSON)
	}
	var act model.BotMessage
	err := env.db.Where("bot_id = ? AND dedup_key = ?", bot.ID,
		fmt.Sprintf("toolset_activated:%d", botToolsetVersion)).First(&act).Error
	if err != nil {
		t.Fatalf("激活落库应补发 toolset_activated 事件: %v", err)
	}
	if !strings.Contains(act.ContentJSON, `"toolset_activated"`) ||
		!strings.Contains(act.ContentJSON, `"activation":"active"`) ||
		!strings.Contains(act.ContentJSON, "已生效") {
		t.Fatalf("activated 事件内容不对: %s", act.ContentJSON)
	}
	var cnt int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND dedup_key = ?", bot.ID, dedup).Count(&cnt)
	if cnt != 1 {
		t.Fatalf("推迟通知仍只应有一条，实得 %d 条", cnt)
	}
}

// 跨版本并存的多条推迟通知只收口一次（INT-139 复审）：去重键字典序会让
// 「toolset_notice:10」排在「:9」前面，按通知逐条收口会把旧版本说成
// 「已生效」且乱序；实现只发一条 to_version=当前激活版本的事件。
func TestBotSettleEmitsSingleActivatedEventAcrossVersions(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok", Usage: &fakeModelUsage{Prompt: 5, Completion: 2, Total: 7}})
	userID := createBotTestUser(t, env.db, "r11multi")
	bot := env.createBot(t, userID)
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Updates(map[string]interface{}{"toolset_version": 8, "last_told_version": 8}).Error; err != nil {
		t.Fatalf("降级水位: %v", err)
	}
	// 手工种两条不同版本的推迟通知（模拟窗口期跨过版本升级）。
	for i, v := range []int64{9, 10} {
		dedup := fmt.Sprintf("toolset_notice:%d", v)
		raw := fmt.Sprintf(`{"event":"toolset_updated","render":"user","detail":{"from_version":%d,"to_version":%d,"note":"x（下一个压缩点生效）","activation":"next_compaction"}}`, v-1, v)
		if err := env.db.Create(&model.BotMessage{
			ID: uuid.NewString(), BotID: bot.ID, Seq: int64(100 + i),
			Kind: model.BotMessageKindLifecycle, ContentJSON: raw,
			DedupKey: &dedup, CreatedAt: time.Now().UTC(),
		}).Error; err != nil {
			t.Fatalf("seed notice v%d: %v", v, err)
		}
	}
	int139SeedMainRequest(t, env, bot.ID, model.BotSessionKindMain,
		time.Now().UTC().Add(-env.cfg.Bots.Runtime.PromptCacheRetain()-time.Minute))

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "r11-m1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	int139WaitNewRunSettled(t, env, bot.ID, 2)

	var acts []model.BotMessage
	env.db.Where("bot_id = ? AND dedup_key GLOB ?", bot.ID, "toolset_activated:*").Find(&acts)
	if len(acts) != 1 {
		t.Fatalf("多条推迟通知也只应收口一次，实得 %d 条", len(acts))
	}
	if !strings.Contains(acts[0].ContentJSON, fmt.Sprintf(`"to_version":%d`, botToolsetVersion)) {
		t.Fatalf("收口事件应记本次实际激活版本 v%d: %s", botToolsetVersion, acts[0].ContentJSON)
	}
}

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
	"gorm.io/gorm"
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

	// panic 残留行的真实形状是 running + finished_at=nil（finishRunAfterPanic
	// 只关 run，不关请求行）；server_restarted 由恢复流程落成 failed。
	seedRaw := func(botID string, status model.BotModelRequestStatus, startedAt time.Time, finishedAt *time.Time, errCode string) {
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
			Status: status, StartedAt: startedAt, FinishedAt: finishedAt,
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
	seedRaw(staleLeftover.ID, model.BotModelRequestRunning, time.Now().UTC().Add(-retain-time.Minute), nil, "")
	if !env.bots.mainlinePrefixCacheExpired(staleLeftover.ID) {
		t.Fatalf("残留的未完成行不得无限推迟激活")
	}
	// 残留行的 started_at 仍在窗口内 → 推迟（上游刚被联系过）。
	freshLeftover := env.createBot(t, userID)
	seedRaw(freshLeftover.ID, model.BotModelRequestRunning, time.Now().UTC(), nil, "")
	if env.bots.mainlinePrefixCacheExpired(freshLeftover.ID) {
		t.Fatalf("最近启动的请求残留仍应判为缓存热")
	}
	// server_restarted 的 finished_at 是恢复记账时刻：旧 started_at +
	// 新 finished_at 不得把中断很久的请求误判成刚热。
	restarted := env.createBot(t, userID)
	recentFinish := time.Now().UTC()
	seedRaw(restarted.ID, model.BotModelRequestFailed, time.Now().UTC().Add(-retain-time.Minute), &recentFinish, "server_restarted")
	if !env.bots.mainlinePrefixCacheExpired(restarted.ID) {
		t.Fatalf("server_restarted 记账时刻不得被当作上游活动时间")
	}
}

// 本地失败的请求行（failed 且无首段、无 usage——上游从未处理这段前缀）
// 不计入前缀热度（INT-139 云端复审 Note）：否则一条 transport 失败残留
// 会把激活推迟盖在并不存在的缓存上；热度应回看更早的真实上游请求。
func TestMainlinePrefixCacheExpiredLocalFailures(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "r11lf")

	seedFailed := func(botID string, startedAt time.Time, withDelta bool) *model.BotModelRequest {
		run := &model.BotRun{
			ID: uuid.NewString(), BotID: botID,
			TriggerSource: model.BotTriggerUserMessage, TriggerKey: "r11lf-" + uuid.NewString(),
			Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
			ConfigSnapshotJSON: "{}", CreatedAt: startedAt, StartedAt: &startedAt, FinishedAt: &startedAt,
		}
		if err := env.db.Create(run).Error; err != nil {
			t.Fatalf("seed run: %v", err)
		}
		finished := startedAt.Add(2 * time.Second)
		code := "request_failed"
		req := &model.BotModelRequest{
			ID: uuid.NewString(), RunID: run.ID, Ordinal: 10,
			Status: model.BotModelRequestFailed, ErrorCode: &code,
			StartedAt: startedAt, FinishedAt: &finished,
			PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4",
		}
		if withDelta {
			delta := startedAt.Add(time.Second)
			req.FirstDeltaAt = &delta
		}
		if err := env.db.Create(req).Error; err != nil {
			t.Fatalf("seed request: %v", err)
		}
		return req
	}

	// 唯一一条「请求」是最近的本地失败 → 上游什么也没见过 → 可激活。
	localOnly := env.createBot(t, userID)
	seedFailed(localOnly.ID, time.Now().UTC(), false)
	if !env.bots.mainlinePrefixCacheExpired(localOnly.ID) {
		t.Fatalf("本地失败行不得计入前缀热度")
	}
	// 本地失败在窗口内、更早的真实请求在窗口外 → 回看旧行，可激活。
	retain := env.cfg.Bots.Runtime.PromptCacheRetain()
	fallback := env.createBot(t, userID)
	int139SeedMainRequest(t, env, fallback.ID, model.BotSessionKindMain,
		time.Now().UTC().Add(-retain-time.Minute))
	seedFailed(fallback.ID, time.Now().UTC(), false)
	if !env.bots.mainlinePrefixCacheExpired(fallback.ID) {
		t.Fatalf("热度应回看失败行之前的真实上游请求")
	}
	// 失败但已有首段（上游确实处理过）→ 仍是热度证据。
	evidenced := env.createBot(t, userID)
	seedFailed(evidenced.ID, time.Now().UTC(), true)
	if env.bots.mainlinePrefixCacheExpired(evidenced.ID) {
		t.Fatalf("触达上游后失败的请求仍应算缓存热")
	}
	// 纯工具调用流失败：无正文首段、无 usage，但 chunk 带回了
	// provider_request_id → 上游确实处理过前缀，仍算热度证据
	// （INT-139 云端复审 codex）。
	toolCallOnly := env.createBot(t, userID)
	req := seedFailed(toolCallOnly.ID, time.Now().UTC(), false)
	if err := env.db.Model(req).Update("provider_request_id", "chatcmpl-tc").Error; err != nil {
		t.Fatalf("seed provider_request_id: %v", err)
	}
	if env.bots.mainlinePrefixCacheExpired(toolCallOnly.ID) {
		t.Fatalf("带 provider_request_id 的失败行仍应算缓存热")
	}
}

// 热度排序按上游实际接触时刻（COALESCE(finished_at, started_at)）而非
// started_at（INT-139 云端复审）：一条更早开始、刚结束的长跑请求仍热，
// 不得被一条更晚开始的冷请求盖过；server_restarted 的 finished_at 是
// 恢复记账时刻，排序同样回退 started_at，不得排在真实热度前面。
func TestMainlinePrefixCacheOrderingByContactTime(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "r11ord")
	retain := env.cfg.Bots.Runtime.PromptCacheRetain()

	seedAt := func(botID string, ordinal int64, status model.BotModelRequestStatus,
		errCode string, startedAt, finishedAt time.Time, withDelta bool) {
		run := &model.BotRun{
			ID: uuid.NewString(), BotID: botID,
			TriggerSource: model.BotTriggerUserMessage, TriggerKey: "r11o-" + uuid.NewString(),
			Status: model.BotRunStatusCompleted, SessionKind: model.BotSessionKindMain,
			ConfigSnapshotJSON: "{}", CreatedAt: startedAt, StartedAt: &startedAt, FinishedAt: &startedAt,
		}
		if err := env.db.Create(run).Error; err != nil {
			t.Fatalf("seed run: %v", err)
		}
		req := &model.BotModelRequest{
			ID: uuid.NewString(), RunID: run.ID, Ordinal: ordinal,
			Status: status, StartedAt: startedAt, FinishedAt: &finishedAt,
			PrefixHash: "x", ToolsHash: "x", RequestHash: "x", TokenEstimator: "chars/4",
		}
		if errCode != "" {
			req.ErrorCode = &errCode
		}
		if withDelta {
			delta := startedAt.Add(time.Second)
			req.FirstDeltaAt = &delta
		}
		if err := env.db.Create(req).Error; err != nil {
			t.Fatalf("seed request: %v", err)
		}
	}

	// 长跑请求 A：2 个窗口前开始、刚刚结束（仍热）；请求 B：1.5 个窗口前
	// 开始并结束（冷）。按 started_at 排序 B 会盖住 A 误判为冷。
	now := time.Now().UTC()
	bot := env.createBot(t, userID)
	seedAt(bot.ID, 1, model.BotModelRequestSucceeded, "", now.Add(-2*retain), now.Add(-time.Second), false)
	seedAt(bot.ID, 2, model.BotModelRequestSucceeded, "", now.Add(-retain-retain/2), now.Add(-retain-time.Minute), false)
	if env.bots.mainlinePrefixCacheExpired(bot.ID) {
		t.Fatalf("刚结束的长跑请求仍热，不得被更晚开始的冷请求盖过")
	}

	// server_restarted 行的 finished_at 是恢复记账时刻：带首段证据使其
	// 不被本地失败过滤跳过，排序必须回退 started_at，不得把 5 分钟前
	// 真实完成的请求盖掉。
	bot2 := env.createBot(t, userID)
	seedAt(bot2.ID, 1, model.BotModelRequestSucceeded, "", now.Add(-5*time.Minute), now.Add(-4*time.Minute), false)
	seedAt(bot2.ID, 2, model.BotModelRequestFailed, "server_restarted", now.Add(-2*retain), now, true)
	if env.bots.mainlinePrefixCacheExpired(bot2.ID) {
		t.Fatalf("server_restarted 记账时刻不得盖住 5 分钟前的真实热度")
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
	if row.ActivationDeferredAt == nil {
		t.Fatal("首次推迟应落 activation_deferred_at 起点")
	}
	deferredAt := *row.ActivationDeferredAt
	var msgs []model.BotMessage
	env.db.Where("bot_id = ? AND dedup_key = ?", bot.ID, fmt.Sprintf("toolset_notice:%d", botToolsetVersion)).Find(&msgs)
	if len(msgs) != 1 {
		t.Fatalf("每个版本只应告知一次，实得 %d 条", len(msgs))
	}
	if !strings.Contains(msgs[0].ContentJSON, "下一个压缩点生效") ||
		!strings.Contains(msgs[0].ContentJSON, `"activation":"next_compaction"`) {
		t.Fatalf("推迟激活的通知应如实标注: %s", msgs[0].ContentJSON)
	}

	// 再来一条：窗口仍热（上一步的请求刚写过），水位不变、通知不重复，
	// 推迟起点不顺延（封顶从第一次推迟量起）。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "again", ClientMessageID: "r11-2"}); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	int139WaitNewRunSettled(t, env, bot.ID, 3)
	env.db.First(&row, "id = ?", bot.ID)
	if row.ToolsetVersion != 9 {
		t.Fatalf("持续活跃的会话仍应推迟激活，实得 %d", row.ToolsetVersion)
	}
	if row.ActivationDeferredAt == nil || !row.ActivationDeferredAt.Equal(deferredAt) {
		t.Fatalf("后续推迟不应顺延起点: %v", row.ActivationDeferredAt)
	}
	env.db.Where("bot_id = ? AND dedup_key = ?", bot.ID, fmt.Sprintf("toolset_notice:%d", botToolsetVersion)).Find(&msgs)
	if len(msgs) != 1 {
		t.Fatalf("通知不得重复发送，实得 %d 条", len(msgs))
	}
}

// 推迟封顶（INT-139 云端复审）：持续活跃、始终不压缩的会话不能把升级
// 无限推迟——activation_deferred_at 距今超过一个保留窗口时，即使前缀
// 缓存仍热也强制入口激活，并清掉推迟标记。
func TestBotActivationDeferralCapped(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok", Usage: &fakeModelUsage{Prompt: 5, Completion: 2, Total: 7}})
	userID := createBotTestUser(t, env.db, "r11cap")
	bot := env.createBot(t, userID)
	old := time.Now().UTC().Add(-env.cfg.Bots.Runtime.PromptCacheRetain() - time.Minute)
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Updates(map[string]interface{}{
			"toolset_version":        9,
			"last_told_version":      9,
			"activation_deferred_at": old,
		}).Error; err != nil {
		t.Fatalf("降级水位: %v", err)
	}
	// 窗口内仍有热请求——推迟不封顶的场景下不会激活。
	int139SeedMainRequest(t, env, bot.ID, model.BotSessionKindMain, time.Now().UTC())

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "r11-c1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	int139WaitNewRunSettled(t, env, bot.ID, 2)

	var row model.Bot
	env.db.First(&row, "id = ?", bot.ID)
	if row.ToolsetVersion != botToolsetVersion {
		t.Fatalf("推迟超一个窗口应强制入口激活到 v%d，实得 %d", botToolsetVersion, row.ToolsetVersion)
	}
	if row.ActivationDeferredAt != nil {
		t.Fatalf("激活落库应清空推迟标记，实得 %v", row.ActivationDeferredAt)
	}
}

// 「下一个压缩点生效」的判定是本 run 入口显式传下来的推迟决策，而不是
// 副本版本落后的反推（INT-139 云端复审）：入口激活失败留下的落后副本
// 没有推迟决策，下一次入口会立即重试——此时不得写推迟文案，通知先不发、
// last_told 不推进，待激活成功后如实告知。
func TestTellToolsetFollowsDeferredDecisionNotStaleCopy(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "r11flag")
	bot := env.createBot(t, userID)
	// 行与副本同步降回旧水位，扮演「入口激活尝试失败」后的样子。
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot.ID).
		Updates(map[string]interface{}{"toolset_version": 9, "last_told_version": 9}).Error; err != nil {
		t.Fatalf("降级水位: %v", err)
	}
	fresh, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	run := &model.BotRun{ID: uuid.NewString(), BotID: bot.ID}

	dedup := fmt.Sprintf("toolset_notice:%d", botToolsetVersion)
	env.bots.maybeTellToolset(fresh, run, false)
	var n int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND dedup_key = ?", bot.ID, dedup).Count(&n)
	if n != 0 {
		t.Fatalf("激活失败的落后副本不得写推迟通知，实得 %d 条", n)
	}
	var row model.Bot
	env.db.First(&row, "id = ?", bot.ID)
	if row.LastToldVersion != 9 {
		t.Fatalf("未发通知不得推进告知水位，实得 %d", row.LastToldVersion)
	}

	// 同一落后副本、入口确实走了推迟分支 → 通知按推迟文案落库。
	env.bots.maybeTellToolset(fresh, run, true)
	var msg model.BotMessage
	if err := env.db.Where("bot_id = ? AND dedup_key = ?", bot.ID, dedup).First(&msg).Error; err != nil {
		t.Fatalf("推迟分支应写推迟通知: %v", err)
	}
	if !strings.Contains(msg.ContentJSON, `"activation":"next_compaction"`) {
		t.Fatalf("推迟通知应标 next_compaction: %s", msg.ContentJSON)
	}

	// 入口判过推迟、随后同一 run 内压缩边界已激活（副本版本被
	// installBoundarySnapshot 推进）：第一轮通知写入失败的重试不得按
	// 入口时刻的 deferred 再写推迟文案——那会成为永远等不到收口的孤儿
	// （INT-139 云端复审）。按副本当前版本复核，写「已生效」。
	bot2 := env.createBot(t, userID)
	if err := env.db.Model(&model.Bot{}).Where("id = ?", bot2.ID).
		Update("last_told_version", botToolsetVersion-1).Error; err != nil {
		t.Fatalf("降级告知水位: %v", err)
	}
	activatedCopy, err := env.bots.bots.Get(bot2.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	activatedCopy.LastToldVersion = botToolsetVersion - 1
	env.bots.maybeTellToolset(activatedCopy, run, true)
	var msg2 model.BotMessage
	if err := env.db.Where("bot_id = ? AND dedup_key = ?", bot2.ID, dedup).First(&msg2).Error; err != nil {
		t.Fatalf("已激活的重试通知应写库: %v", err)
	}
	if !strings.Contains(msg2.ContentJSON, `"activation":"active"`) {
		t.Fatalf("run 内已激活后重试应写已生效: %s", msg2.ContentJSON)
	}
}

// markActivationDeferred 的版本守卫与行事实回读（INT-139 云端复审）：
// 副本陈旧时行可能已被别的事务激活并清掉 deferred_at，不带守卫的更新会
// 给已激活行重落一个无意义起点；未命中时回读行事实对齐副本，并据实
// 返回「本 run 是否真推迟」。
func TestMarkActivationDeferredVersionGuard(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "r11guard")

	// 行已在当前版本（视同别的事务刚激活）：陈旧副本来记推迟起点 → 被挡，
	// 副本回读对齐行事实（版本刷新、无推迟标记），返回 false。
	activated := env.createBot(t, userID)
	staleCopy, err := env.bots.bots.Get(activated.ID)
	if err != nil {
		t.Fatalf("get bot: %v", err)
	}
	staleCopy.ToolsetVersion = botToolsetVersion - 1
	if env.bots.markActivationDeferred(staleCopy) {
		t.Fatalf("行已激活时不得把本 run 判为推迟")
	}
	var row model.Bot
	env.db.First(&row, "id = ?", activated.ID)
	if row.ActivationDeferredAt != nil {
		t.Fatalf("已激活的行不得被陈旧副本重落推迟起点: %v", row.ActivationDeferredAt)
	}
	if staleCopy.ActivationDeferredAt != nil || staleCopy.ToolsetVersion != botToolsetVersion {
		t.Fatalf("副本应按行事实对齐（已激活、无推迟标记）: v=%d deferred=%v",
			staleCopy.ToolsetVersion, staleCopy.ActivationDeferredAt)
	}

	// 行确实陈旧：正常落起点，返回 true，副本同步标记。
	stale := env.createBot(t, userID)
	if err := env.db.Model(&model.Bot{}).Where("id = ?", stale.ID).
		Update("toolset_version", botToolsetVersion-1).Error; err != nil {
		t.Fatalf("降级水位: %v", err)
	}
	staleCopy2, _ := env.bots.bots.Get(stale.ID)
	if !env.bots.markActivationDeferred(staleCopy2) {
		t.Fatalf("陈旧行应判为真推迟")
	}
	// 复用上文的 row 会让 gorm 把旧主键追加成第二条件——换独立变量。
	var row2 model.Bot
	env.db.First(&row2, "id = ?", stale.ID)
	if row2.ActivationDeferredAt == nil {
		t.Fatalf("陈旧行应正常落推迟起点")
	}
	if staleCopy2.ActivationDeferredAt == nil {
		t.Fatalf("更新命中后副本应同步推迟标记")
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

// 已收口通知不得替新版本再发事件（INT-139 云端复审）：v10 的推迟通知已被
// toolset_activated:10 收口，但 append-only 原文仍是 next_compaction。
// 后来 v11 未推迟直接激活时，收口扫描不得把 v10 的旧通知当 pending 误补
// 一条 toolset_activated:11——v11 从未被推迟过。
func TestBotSettleSkipsSettledNoticeForNewerVersion(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok"})
	userID := createBotTestUser(t, env.db, "r11skip")
	bot := env.createBot(t, userID)
	for i, kv := range [][2]string{
		{"toolset_notice:10", `{"event":"toolset_updated","render":"user","detail":{"from_version":9,"to_version":10,"note":"x（下一个压缩点生效）","activation":"next_compaction"}}`},
		{"toolset_activated:10", `{"event":"toolset_activated","render":"user","detail":{"to_version":10,"note":"已生效","activation":"active"}}`},
	} {
		dedup := kv[0]
		if err := env.db.Create(&model.BotMessage{
			ID: uuid.NewString(), BotID: bot.ID, Seq: int64(100 + i),
			Kind: model.BotMessageKindLifecycle, ContentJSON: kv[1],
			DedupKey: &dedup, CreatedAt: time.Now().UTC(),
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", kv[0], err)
		}
	}

	if err := env.db.Transaction(func(tx *gorm.DB) error {
		return env.bots.settleToolsetNoticesTx(tx, bot.ID, 11)
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	var cnt int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND dedup_key = ?", bot.ID, "toolset_activated:11").Count(&cnt)
	if cnt != 0 {
		t.Fatalf("v11 未推迟却补发了收口事件，实得 %d 条", cnt)
	}
}

// 反向用例：确实存在未收口的新版本推迟通知时仍要补发——已收口的旧版本
// 不抵消新版本自己的 pending 状态。
func TestBotSettleFiresForPendingNewerNotice(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "ok"})
	userID := createBotTestUser(t, env.db, "r11pend")
	bot := env.createBot(t, userID)
	for i, kv := range [][2]string{
		{"toolset_notice:10", `{"event":"toolset_updated","render":"user","detail":{"from_version":9,"to_version":10,"note":"x（下一个压缩点生效）","activation":"next_compaction"}}`},
		{"toolset_activated:10", `{"event":"toolset_activated","render":"user","detail":{"to_version":10,"note":"已生效","activation":"active"}}`},
		{"toolset_notice:11", `{"event":"toolset_updated","render":"user","detail":{"from_version":10,"to_version":11,"note":"y（下一个压缩点生效）","activation":"next_compaction"}}`},
	} {
		dedup := kv[0]
		if err := env.db.Create(&model.BotMessage{
			ID: uuid.NewString(), BotID: bot.ID, Seq: int64(100 + i),
			Kind: model.BotMessageKindLifecycle, ContentJSON: kv[1],
			DedupKey: &dedup, CreatedAt: time.Now().UTC(),
		}).Error; err != nil {
			t.Fatalf("seed %s: %v", kv[0], err)
		}
	}

	if err := env.db.Transaction(func(tx *gorm.DB) error {
		return env.bots.settleToolsetNoticesTx(tx, bot.ID, 11)
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}
	var act model.BotMessage
	if err := env.db.Where("bot_id = ? AND dedup_key = ?", bot.ID, "toolset_activated:11").First(&act).Error; err != nil {
		t.Fatalf("存在未收口的新版本推迟通知时应补发 activated: %v", err)
	}
	if !strings.Contains(act.ContentJSON, `"to_version":11`) {
		t.Fatalf("收口事件应记实际激活版本 v11: %s", act.ContentJSON)
	}
}

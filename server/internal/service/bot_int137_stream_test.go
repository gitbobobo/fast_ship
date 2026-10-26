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
	"github.com/openai/openai-go/v2"
)

// INT-137 流式看门狗、未输出前的有限重试与错误归类。适配层契约用直连
// botStreamChat 的单测钉住；run 级行为（重试次数、错误码、可操作事件）
// 用假服务脚本走完整主线。

// int137RunRuntime 把运行参数调到测试档：秒级看门狗、50ms 退避基数。
// 重试上限保持默认 2。
func int137RunRuntime(env *botTestEnv) {
	env.cfg.Bots.Runtime.StreamFirstChunkTimeoutSeconds = 1
	env.cfg.Bots.Runtime.StreamIdleTimeoutSeconds = 1
	env.cfg.Bots.Runtime.RetryBaseDelayMs = 50
}

// lastRunOf 取该 bot 最新一条 run 行。
func lastRunOf(t *testing.T, env *botTestEnv, botID string) model.BotRun {
	t.Helper()
	var run model.BotRun
	env.db.Where("bot_id = ?", botID).Order("created_at DESC").First(&run)
	return run
}

// runModelRequests 取该 run 的全部模型请求行（按序）。
func runModelRequests(t *testing.T, env *botTestEnv, runID string) []model.BotModelRequest {
	t.Helper()
	var reqs []model.BotModelRequest
	env.db.Where("run_id = ?", runID).Order("ordinal ASC").Find(&reqs)
	return reqs
}

// ---------- 适配层契约：看门狗与停止的区分 ----------

func int137DirectRequest() botStreamRequest {
	return botStreamRequest{
		Model:    "fake-model",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	}
}

func TestBotStreamChatWatchdogFirstChunk(t *testing.T) {
	f := newFakeModelServer(t, fakeModelTurn{HoldFor: 2 * time.Second})
	client := botNewClient(f.URL(), "sk-test-abcdef123456")

	reply, err := botStreamChat(context.Background(), &client, "https://relay.example.com/v1", int137DirectRequest(),
		botStreamTimeouts{FirstChunk: 100 * time.Millisecond, Idle: 100 * time.Millisecond}, nil)
	var timeout *botStreamTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("首包超时应返回 *botStreamTimeoutError，实得 %v", err)
	}
	if timeout.Phase != "first_chunk" || timeout.Timeout != 100*time.Millisecond {
		t.Fatalf("应记 first_chunk 阶段与当时阈值: %+v", timeout)
	}
	if reply == nil || reply.Text != "" || len(reply.ToolCalls) > 0 {
		t.Fatalf("无片段失败应返回空的部分回复: %+v", reply)
	}
}

func TestBotStreamChatWatchdogIdle(t *testing.T) {
	f := newFakeModelServer(t, fakeModelTurn{Text: "开头", StallAfterText: 2 * time.Second})
	client := botNewClient(f.URL(), "sk-test-abcdef123456")

	reply, err := botStreamChat(context.Background(), &client, "https://relay.example.com/v1", int137DirectRequest(),
		botStreamTimeouts{FirstChunk: 5 * time.Second, Idle: 100 * time.Millisecond}, nil)
	var timeout *botStreamTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("片段间空闲超时应返回 *botStreamTimeoutError，实得 %v", err)
	}
	if timeout.Phase != "idle" {
		t.Fatalf("应记 idle 阶段，实得 %s", timeout.Phase)
	}
	if reply == nil || reply.Text != "开头" {
		t.Fatalf("已聚合的部分正文应一并返回: %+v", reply)
	}
}

func TestBotStreamChatStopCancelIsNotTimeout(t *testing.T) {
	hold := make(chan struct{})
	defer close(hold)
	f := newFakeModelServer(t, fakeModelTurn{Hold: hold})
	client := botNewClient(f.URL(), "sk-test-abcdef123456")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// 父 ctx 先于看门狗（500ms）取消：模拟用户停止的硬取消。
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	_, err := botStreamChat(ctx, &client, "https://relay.example.com/v1", int137DirectRequest(),
		botStreamTimeouts{FirstChunk: 500 * time.Millisecond, Idle: 500 * time.Millisecond}, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("用户停止应保留取消错误，实得 %v", err)
	}
	var timeout *botStreamTimeoutError
	if errors.As(err, &timeout) {
		t.Fatalf("用户停止不得改写成超时错误: %v", err)
	}
}

// ---------- run 级行为 ----------

func TestBotStreamFirstChunkTimeoutReleasesSlot(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{HoldFor: 3 * time.Second})
	int137RunRuntime(env)
	userID := createBotTestUser(t, env.db, "botfirstchunk")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "fc1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "首包超时收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })

	run := lastRunOf(t, env, bot.ID)
	if run.Status != model.BotRunStatusFailed {
		t.Fatalf("首包超时后 run 应 failed，实得 %s", run.Status)
	}
	if run.ErrorCode == nil || *run.ErrorCode != "model_stream_timeout" {
		t.Fatalf("run error_code 应为 model_stream_timeout，实得 %v", run.ErrorCode)
	}
	reqs := runModelRequests(t, env, run.ID)
	if len(reqs) != 1 {
		t.Fatalf("超时不重试，模型请求应恰好 1 条，实得 %d", len(reqs))
	}
	if reqs[0].ErrorCode == nil || *reqs[0].ErrorCode != "model_stream_timeout" {
		t.Fatalf("请求行应记 model_stream_timeout，实得 %v", reqs[0].ErrorCode)
	}

	// 执行槽已释放：同一 bot 的新消息能被正常处理并跑完。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "再来一句", ClientMessageID: "fc2"}); err != nil {
		t.Fatalf("send 2: %v", err)
	}
	waitForBot(t, "第二个 run 完成", 15*time.Second, func() bool {
		next := lastRunOf(t, env, bot.ID)
		return next.ID != run.ID && next.Status == model.BotRunStatusCompleted
	})
}

func TestBotStreamIdleTimeoutMidStream(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "开头", StallAfterText: 3 * time.Second})
	int137RunRuntime(env)
	userID := createBotTestUser(t, env.db, "botidle")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "id1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "空闲超时收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })

	run := lastRunOf(t, env, bot.ID)
	if run.Status != model.BotRunStatusFailed || run.ErrorCode == nil || *run.ErrorCode != "model_stream_timeout" {
		t.Fatalf("流中途卡住应 failed/model_stream_timeout，实得 %s %v", run.Status, run.ErrorCode)
	}
	if reqs := runModelRequests(t, env, run.ID); len(reqs) != 1 {
		t.Fatalf("超时已输出也不重试，请求应恰好 1 条，实得 %d", len(reqs))
	}
}

func TestBotRetryOn429ThenSuccess(t *testing.T) {
	limited := fakeModelTurn{
		Status:     http.StatusTooManyRequests,
		RetryAfter: "1",
		ErrorBody:  `{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`,
	}
	env := setupBotTestEnv(t, limited, limited, fakeModelTurn{
		Text:  "成功了",
		Usage: &fakeModelUsage{Prompt: 5, Completion: 3, Total: 8},
	})
	int137RunRuntime(env)
	userID := createBotTestUser(t, env.db, "bot429")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "r1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "429 重试后完成", 20*time.Second, func() bool { return env.botSettled(bot.ID) })

	run := lastRunOf(t, env, bot.ID)
	if run.Status != model.BotRunStatusCompleted {
		t.Fatalf("两次 429 后第三次应成功，实得 %s（%v）", run.Status, run.ErrorMessage)
	}
	reqs := runModelRequests(t, env, run.ID)
	if len(reqs) != 3 {
		t.Fatalf("应有 3 条模型请求记录（失败 2 + 成功 1），实得 %d", len(reqs))
	}
	for i, want := range []model.BotModelRequestStatus{model.BotModelRequestFailed, model.BotModelRequestFailed, model.BotModelRequestSucceeded} {
		if reqs[i].Status != want {
			t.Fatalf("请求 %d 状态应 %s，实得 %s", i+1, want, reqs[i].Status)
		}
	}
	for i := 0; i < 2; i++ {
		if reqs[i].ErrorCode == nil || *reqs[i].ErrorCode != "provider_rate_limited" {
			t.Fatalf("429 请求行应记 provider_rate_limited，实得 %v", reqs[i].ErrorCode)
		}
	}
	// 相邻请求间隔 ≥ Retry-After（1s）：退避必须真等，而不是立刻重放。
	var ats []time.Time
	for i := 0; i < 3; i++ {
		ats = append(ats, env.fake.nextRequest(t, 5*time.Second).At)
	}
	for i := 1; i < len(ats); i++ {
		if gap := ats[i].Sub(ats[i-1]); gap < 900*time.Millisecond {
			t.Fatalf("重试应等待 Retry-After≈1s，第 %d 次间隔仅 %v", i, gap)
		}
	}
}

func TestBotRetryExhausted500Fails(t *testing.T) {
	broken := fakeModelTurn{Status: http.StatusInternalServerError}
	env := setupBotTestEnv(t, broken, broken, broken)
	int137RunRuntime(env)
	userID := createBotTestUser(t, env.db, "bot500")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "s1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "500 耗尽重试", 20*time.Second, func() bool { return env.botSettled(bot.ID) })

	run := lastRunOf(t, env, bot.ID)
	if run.Status != model.BotRunStatusFailed || run.ErrorCode == nil || *run.ErrorCode != "provider_unavailable" {
		t.Fatalf("连续 3 次 500 应 failed/provider_unavailable，实得 %s %v", run.Status, run.ErrorCode)
	}
	reqs := runModelRequests(t, env, run.ID)
	if len(reqs) != 3 {
		t.Fatalf("首次 + 2 次重试应恰好 3 条请求，实得 %d", len(reqs))
	}
	for i, req := range reqs {
		if req.Status != model.BotModelRequestFailed || req.ErrorCode == nil || *req.ErrorCode != "provider_unavailable" {
			t.Fatalf("请求 %d 应 failed/provider_unavailable，实得 %s %v", i+1, req.Status, req.ErrorCode)
		}
	}
}

func TestBotNoRetryAfterPartialOutput(t *testing.T) {
	// 先发了部分正文再断开：已有输出，失败必须直接落终态，不重放请求。
	env := setupBotTestEnv(t, fakeModelTurn{Text: "写到一半", BreakAfterText: true})
	int137RunRuntime(env)
	userID := createBotTestUser(t, env.db, "botpartial")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "p1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "断流收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })

	run := lastRunOf(t, env, bot.ID)
	if run.Status != model.BotRunStatusFailed || run.ErrorCode == nil || *run.ErrorCode != "provider_unavailable" {
		t.Fatalf("已输出后断流应 failed/provider_unavailable，实得 %s %v", run.Status, run.ErrorCode)
	}
	if reqs := runModelRequests(t, env, run.ID); len(reqs) != 1 {
		t.Fatalf("已输出片段后不得重试，请求应恰好 1 条，实得 %d", len(reqs))
	}
}

func TestBotAuthFailedCarriesAction(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{
		Status:    http.StatusUnauthorized,
		ErrorBody: `{"error":{"message":"invalid api key","type":"auth_error"}}`,
	})
	int137RunRuntime(env)
	userID := createBotTestUser(t, env.db, "bot401")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "a1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "401 收尾", 15*time.Second, func() bool { return env.botSettled(bot.ID) })

	run := lastRunOf(t, env, bot.ID)
	if run.Status != model.BotRunStatusFailed || run.ErrorCode == nil || *run.ErrorCode != "provider_auth_failed" {
		t.Fatalf("401 应 failed/provider_auth_failed，实得 %s %v", run.Status, run.ErrorCode)
	}
	if reqs := runModelRequests(t, env, run.ID); len(reqs) != 1 {
		t.Fatalf("401 不可重试，请求应恰好 1 条，实得 %d", len(reqs))
	}

	// run_error 事件：中文 message + detail + 去设置页的 action。
	var ev model.BotEvent
	if err := env.db.Where("bot_id = ? AND event_type = ?", bot.ID, model.BotEventError).
		Order("seq DESC").First(&ev).Error; err != nil {
		t.Fatalf("应有 run_error 事件: %v", err)
	}
	var envelope botEventEnvelope
	if err := json.Unmarshal([]byte(ev.PayloadJSON), &envelope); err != nil {
		t.Fatalf("解析事件: %v", err)
	}
	payload, _ := envelope.Payload.(map[string]interface{})
	if payload == nil {
		t.Fatalf("run_error 事件应带 payload: %s", ev.PayloadJSON)
	}
	if msg, _ := payload["message"].(string); msg == "" || !strings.Contains(msg, "认证失败") {
		t.Fatalf("payload.message 应为中文认证失败说明，实得 %q", msg)
	}
	if detail, _ := payload["detail"].(string); detail == "" {
		t.Fatalf("payload.detail 应带上游原始报错: %v", payload)
	}
	action, _ := payload["action"].(map[string]interface{})
	if action == nil || action["kind"] != "ai_providers_settings" {
		t.Fatalf("payload.action.kind 应为 ai_providers_settings，实得 %v", payload["action"])
	}
}

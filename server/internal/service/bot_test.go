package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
	"gorm.io/gorm"
)

// 机器人（INT-101）服务层测试：假 OpenAI 兼容服务脚本化驱动主线。
// 测试库保持默认连接池（worker 与测试并发借连接）；单连接纪律由
// TestBotSingleConn 单独覆盖。

type fakeModelToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

type fakeModelUsage struct {
	Prompt     int64 `json:"prompt_tokens"`
	Completion int64 `json:"completion_tokens"`
	Total      int64 `json:"total_tokens"`
	Cached     int64 `json:"cached_tokens"`
}

type fakeModelTurn struct {
	Text      string
	ToolCalls []fakeModelToolCall
	Usage     *fakeModelUsage
	// Hold 非 nil 时请求阻塞到该 channel 可读或请求 context 结束——测试用它
	// 把「模型请求进行中」精确控制在时点上。
	Hold <-chan struct{}
	// BreakAfterText 流完文本后立刻断开连接：模拟「发了部分正文就报错」
	// 的上游故障（草稿已挂出但永远等不来 message_commit）。
	BreakAfterText bool
}

type fakeModelRequest struct {
	Body []byte
}

type fakeModelServer struct {
	srv      *httptest.Server
	mu       sync.Mutex
	turns    []fakeModelTurn
	requests chan fakeModelRequest
	// shutdown 在 Cleanup 中先于 srv.Close 关闭：卡在 Hold 上的处理协程
	// 得以退出，避免测试收尾在 Server.Close 上永久等待。
	shutdown chan struct{}
}

// newFakeModelServer 脚本耗尽后回退为普通文本回复，未预期的模型请求不会
// 打挂测试进程。
func newFakeModelServer(t *testing.T, turns ...fakeModelTurn) *fakeModelServer {
	t.Helper()
	f := &fakeModelServer{requests: make(chan fakeModelRequest, 64), shutdown: make(chan struct{}), turns: turns}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		// 坏密钥：替换密钥的测试必须真的用新密钥，假服务按头拒绝。
		if strings.Contains(r.Header.Get("Authorization"), "bad-replacement") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"invalid api key","type":"auth_error"}}`)
			return
		}
		// no-tools-model：只拒绝带工具的请求，模拟「能聊天不能调工具」的
		// 供应商，验证聚合测试结果不给假成功。
		if strings.Contains(string(body), "no-tools-model") && strings.Contains(string(body), "fs_probe_echo") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"tools is not supported","type":"invalid_request_error"}}`)
			return
		}
		select {
		case f.requests <- fakeModelRequest{Body: body}:
		default:
			// 缓冲满即丢弃：不阻塞服务协程；需要断言请求体的测试会及时消费。
		}
		f.mu.Lock()
		var turn fakeModelTurn
		if len(f.turns) > 0 {
			turn = f.turns[0]
			f.turns = f.turns[1:]
		} else {
			turn = fakeModelTurn{Text: "(脚本外的默认回复)"}
		}
		f.mu.Unlock()
		if turn.Hold != nil {
			select {
			case <-turn.Hold:
			case <-f.shutdown:
				return
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		writeChunk := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
		chunk := func(delta string, toolCalls []map[string]interface{}, finish string) string {
			deltaObj := map[string]interface{}{}
			if delta != "" {
				deltaObj["content"] = delta
			}
			if toolCalls != nil {
				deltaObj["tool_calls"] = toolCalls
			}
			choice := map[string]interface{}{"index": 0, "delta": deltaObj, "finish_reason": finish}
			m := map[string]interface{}{
				"id": "chatcmpl-test", "object": "chat.completion.chunk",
				"created": 1700000000, "model": "fake-model",
				"choices": []map[string]interface{}{choice},
			}
			raw, _ := json.Marshal(m)
			return string(raw)
		}
		finish := "stop"
		if len(turn.ToolCalls) > 0 {
			finish = "tool_calls"
		}
		if turn.Text != "" {
			// 拆成两片：验证流式聚合与 UTF-8 分片拼接。
			runes := []rune(turn.Text)
			half := len(runes) / 2
			writeChunk(chunk(string(runes[:half]), nil, ""))
			if half > 0 {
				writeChunk(chunk(string(runes[half:]), nil, ""))
			}
			if turn.BreakAfterText {
				panic(http.ErrAbortHandler)
			}
		}
		for i, tc := range turn.ToolCalls {
			writeChunk(chunk("", []map[string]interface{}{{
				"index":    i,
				"id":       tc.ID,
				"type":     "function",
				"function": map[string]interface{}{"name": tc.Name, "arguments": tc.Args},
			}}, ""))
		}
		writeChunk(chunk("", nil, finish))
		if turn.Usage != nil {
			// cached_tokens 在 prompt_tokens_details 下（与上游 wire 格式一致）。
			usageRaw, _ := json.Marshal(map[string]interface{}{
				"id": "chatcmpl-test", "object": "chat.completion.chunk",
				"created": 1700000000, "model": "fake-model",
				"choices": []map[string]interface{}{},
				"usage": map[string]interface{}{
					"prompt_tokens":         turn.Usage.Prompt,
					"completion_tokens":     turn.Usage.Completion,
					"total_tokens":          turn.Usage.Total,
					"prompt_tokens_details": map[string]int64{"cached_tokens": turn.Usage.Cached},
				},
			})
			writeChunk(string(usageRaw))
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() { close(f.shutdown) })
	return f
}

func (f *fakeModelServer) URL() string { return f.srv.URL }

func (f *fakeModelServer) nextRequest(t *testing.T, timeout time.Duration) fakeModelRequest {
	t.Helper()
	select {
	case req := <-f.requests:
		return req
	case <-time.After(timeout):
		t.Fatalf("等待模型请求超时")
		return fakeModelRequest{}
	}
}

// ---------- 测试装配 ----------

type botTestEnv struct {
	*testServices
	bots *BotService
	fake *fakeModelServer
}

func setupBotTestEnv(t *testing.T, turns ...fakeModelTurn) *botTestEnv {
	t.Helper()
	base := setupTestServices(t)
	// 生产纪律是单连接池（SetMaxOpenConns(1)）：多连接 + 共享缓存内存库在
	// 并发短事务下会出现 shared-cache 表锁死锁（SQLite extended 6），机器人
	// 的主线/worker/决定并发正好踩中。测试与生产保持同一纪律。
	limitPoolToSingleConn(t, base)
	if err := base.db.AutoMigrate(repository.BotModels()...); err != nil {
		t.Fatalf("migrate bot models: %v", err)
	}
	if err := repository.EnsureBotSchema(base.db); err != nil {
		t.Fatalf("ensure bot schema: %v", err)
	}
	projectService := NewProjectService(
		repository.NewProjectRepository(base.db),
		repository.NewVersionRepository(base.db),
		repository.NewIssueSyncStateRepository(base.db),
		repository.NewUserGitHubTokenRepository(base.db),
		base.storage, base.cfg, zap.NewNop())
	t3EnvRepo := repository.NewT3EnvironmentRepository(base.db)
	t3Threads := NewT3ThreadsService(t3EnvRepo, base.cfg, zap.NewNop())
	t3Binding := NewT3BindingService(t3Threads, base.projectRepo, repository.NewProjectT3BindingRepository(base.db))
	fake := newFakeModelServer(t, turns...)
	bots := NewBotService(base.db, base.cfg, zap.NewNop(), projectService, base.issueService, t3Threads, t3Binding)
	return &botTestEnv{testServices: base, bots: bots, fake: fake}
}

func createBotTestUser(t *testing.T, db *gorm.DB, name string) string {
	t.Helper()
	return createTestUser(t, db, name).ID
}

func (e *botTestEnv) createBot(t *testing.T, userID string) *BotResponse {
	t.Helper()
	bot, err := e.bots.Create(userID, &CreateBotRequest{
		Name:    "bot-" + uuid.NewString()[:8],
		BaseURL: e.fake.URL(),
		Model:   "fake-model",
		APIKey:  "sk-test-abcdef123456",
	})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	return bot
}

func waitForBot(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时", what)
}

func (e *botTestEnv) activeRunOrNothing(botID string) *model.BotRun {
	run, err := e.bots.runs.FindActiveByBot(botID)
	if err != nil {
		return nil
	}
	return run
}

// botSettled 报告「至少存在一个 run 且没有活动 run」——等待主线收尾的正确
// 谓词（仅看无活动 run 会把尚未启动的 run 误判为完成）。
func (e *botTestEnv) botSettled(botID string) bool {
	var count int64
	env_db := e.db
	_ = env_db
	e.db.Model(&model.BotRun{}).Where("bot_id = ?", botID).Count(&count)
	if count == 0 {
		return false
	}
	return e.activeRunOrNothing(botID) == nil
}

// rawMessages 提取请求体里 messages/tools 的原始字节（保留 wire 顺序）。
func rawMessages(t *testing.T, body []byte) (elems []json.RawMessage, tools json.RawMessage) {
	t.Helper()
	var payload struct {
		Messages []json.RawMessage `json:"messages"`
		Tools    json.RawMessage   `json:"tools"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	return payload.Messages, payload.Tools
}

// seedBotCallFixture 直接落最小 run/请求/调用账本（绕过模型），供取消与恢复
// 测试使用；返回 runID 与 callID。
func seedBotCallFixture(t *testing.T, env *botTestEnv, botID string, status model.BotToolCallStatus, mode string) (string, string) {
	t.Helper()
	bot, err := env.bots.bots.Get(botID)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}
	// 夹具 run 直接落成终态：部分唯一索引保证每机器人至多一个活动 run，
	// 多个夹具调用不能都挂在活动 run 上。
	now2 := time.Now().UTC()
	run := &model.BotRun{
		ID: uuid.NewString(), BotID: botID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "fixture:" + uuid.NewString(),
		TriggerContextJSON: "{}", Status: model.BotRunStatusCompleted,
		ConfigSnapshotJSON: "{}", CreatedAt: now2, StartedAt: &now2, FinishedAt: &now2,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	reqRow := &model.BotModelRequest{
		ID: uuid.NewString(), RunID: run.ID, Ordinal: 1,
		Status: model.BotModelRequestRunning, InputThroughSeq: 0,
		PromptRevision: bot.PromptRevision, ToolsetVersion: bot.ToolsetVersion,
		PrefixHash: "x", ToolsHash: "x", RequestHash: "x",
		TokenEstimator: "chars/4", StartedAt: time.Now().UTC(),
	}
	if err := env.bots.requests.CreateTx(env.db, reqRow); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	now := time.Now().UTC()
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: botID, RequestID: reqRow.ID,
		ProviderToolCallID: "prov-" + uuid.NewString()[:8],
		ToolName:           "fs_project", Action: "create",
		ArgumentsJSON: `{"action":"create","name":"x"}`, ArgumentsHash: "x",
		ExecutionMode: mode, RequiresConfirmation: true,
		Status: status, ExecutionContextJSON: "{}", CreatedAt: now,
	}
	if status == model.BotCallStatusRunning {
		call.StartedAt = &now
	}
	if err := env.bots.calls.CreateTx(env.db, call); err != nil {
		t.Fatalf("seed call: %v", err)
	}
	return run.ID, call.ID
}

// ---------- 批次一：配置、密钥、归属 ----------

func TestBotConfigLifecycle(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botcfg")

	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "我的机器人", PersonaPrompt: "简洁回答。",
		BaseURL: env.fake.URL(), Model: "m1", APIKey: "sk-live-99998888",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if bot.PromptRevision != 1 || bot.APIKeyLast4 != "8888" {
		t.Fatalf("unexpected create result: %+v", bot)
	}

	// 产品决策不要求机器人名称唯一：同名可建。
	if _, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "我的机器人", BaseURL: env.fake.URL(), Model: "m1", APIKey: "sk-x-1111",
	}); err != nil {
		t.Fatalf("同名机器人应允许创建: %v", err)
	}

	got, err := env.bots.Get(userID, bot.ID)
	if err != nil || got.Name != "我的机器人" {
		t.Fatalf("get: %v %+v", err, got)
	}

	updated, err := env.bots.Update(userID, bot.ID, &UpdateBotRequest{
		PersonaPrompt: strPtrBot("更简洁。"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.PromptRevision != 2 || updated.PersonaPrompt != "更简洁。" {
		t.Fatalf("persona revision: %+v", updated)
	}
	if updated.APIKeyLast4 != "8888" {
		t.Fatalf("key 应保留: %+v", updated)
	}
	if _, err := env.bots.Update(userID, bot.ID, &UpdateBotRequest{APIKey: strPtrBot("")}); err == nil {
		t.Fatalf("空串 key 应被拒绝")
	}
	rotated, err := env.bots.Update(userID, bot.ID, &UpdateBotRequest{APIKey: strPtrBot("sk-new-7777")})
	if err != nil || rotated.APIKeyLast4 != "7777" {
		t.Fatalf("key rotate: %v %+v", err, rotated)
	}

	if _, err := env.bots.Create(userID, &CreateBotRequest{Name: "x", BaseURL: env.fake.URL(), Model: "m", APIKey: ""}); err == nil {
		t.Fatalf("缺 api_key 应拒绝")
	}

	items, err := env.bots.List(userID)
	if err != nil || len(items) != 2 {
		t.Fatalf("list: %v %d", err, len(items))
	}

	if err := env.bots.Delete(userID, bot.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := env.bots.Get(userID, bot.ID); !errors.Is(err, errs.ErrBotNotFound) {
		t.Fatalf("deleted bot should 40424: %v", err)
	}
}

func TestBotKeyNotLeaked(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botkey")
	plain := "sk-supersecret-4242"
	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "k", BaseURL: env.fake.URL(), Model: "m", APIKey: plain,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	resp2, _ := env.bots.Get(userID, bot.ID)
	items, _ := env.bots.List(userID)
	for _, r := range []*BotResponse{bot, resp2} {
		if strings.Contains(botJSON(r), plain) {
			t.Fatalf("响应泄露明文 key")
		}
	}
	for _, it := range items {
		if strings.Contains(botJSON(it), plain) {
			t.Fatalf("列表泄露明文 key")
		}
	}
	var row model.Bot
	if err := env.db.Where("id = ?", bot.ID).First(&row).Error; err != nil {
		t.Fatalf("load row: %v", err)
	}
	if strings.Contains(string(row.APIKeyEncrypted), plain) {
		t.Fatalf("库里出现明文 key")
	}
	if string(row.APIKeyEncrypted) == plain {
		t.Fatalf("key 未加密")
	}
}

func TestBotConnection(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botconn")

	res, err := env.bots.TestConnection(userID, &TestConnectionRequest{
		BaseURL: strPtrBot(env.fake.URL()), Model: strPtrBot("fake-model"), APIKey: strPtrBot("sk-conn-1"),
	})
	if err != nil || !res.Ok {
		t.Fatalf("test connection: %v %+v", err, res)
	}
	for _, c := range res.Checks {
		if !c.Passed {
			t.Fatalf("check %s failed: %s", c.Name, c.Detail)
		}
	}

	res, err = env.bots.TestConnection(userID, &TestConnectionRequest{
		BaseURL: strPtrBot("http://127.0.0.1:1"), Model: strPtrBot("m"), APIKey: strPtrBot("sk-secret-7777"),
	})
	if err != nil {
		t.Fatalf("坏地址应返回检查结果而不是报错: %v", err)
	}
	if res.Ok {
		t.Fatalf("坏地址不应通过")
	}
	if strings.Contains(botJSON(res), "sk-secret-7777") {
		t.Fatalf("错误详情泄露 key")
	}

	bot, err := env.bots.Create(userID, &CreateBotRequest{
		Name: "c", BaseURL: env.fake.URL(), Model: "fake-model", APIKey: "sk-saved-3131",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	res, err = env.bots.TestConnection(userID, &TestConnectionRequest{BotID: &bot.ID})
	if err != nil || !res.Ok {
		t.Fatalf("bot_id connection: %v %+v", err, res)
	}

	// 聚合成功要求流式与工具调用都过：能聊天不能调工具的供应商跑不了
	// 工具循环，不能给假成功。
	res, err = env.bots.TestConnection(userID, &TestConnectionRequest{BotID: &bot.ID,
		Model: strPtrBot("no-tools-model")})
	if err != nil {
		t.Fatalf("tool probe: %v", err)
	}
	if res.Ok {
		t.Fatalf("工具探测失败时聚合结果不得为 ok: %+v", res)
	}
	for _, c := range res.Checks {
		if c.Name == "tool_call" && c.Passed {
			t.Fatalf("no-tools-model 的工具探测应失败: %+v", res)
		}
	}

	// 表单填了替换密钥就测新密钥：坏的新密钥不得被旧密钥救活。
	res, err = env.bots.TestConnection(userID, &TestConnectionRequest{BotID: &bot.ID,
		APIKey: strPtrBot("sk-bad-replacement-9999")})
	if err != nil {
		t.Fatalf("replacement key: %v", err)
	}
	if res.Ok || res.Checks[0].Passed {
		t.Fatalf("坏替换密钥应测试失败（不得回退已存密钥）: %+v", res)
	}

	if _, err := env.bots.TestConnection(userID, &TestConnectionRequest{}); err == nil {
		t.Fatalf("无配置且无 bot_id 应 40001")
	}
}

func TestBotOwnership(t *testing.T) {
	env := setupBotTestEnv(t)
	owner := createBotTestUser(t, env.db, "botowner")
	stranger := createBotTestUser(t, env.db, "botstranger")

	bot, err := env.bots.Create(owner, &CreateBotRequest{
		Name: "o", BaseURL: env.fake.URL(), Model: "m", APIKey: "sk-owner-1111",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := env.bots.Get(stranger, bot.ID); !errors.Is(err, errs.ErrBotNotFound) {
		t.Fatalf("陌生人 get 应 40424: %v", err)
	}
	if _, err := env.bots.Update(stranger, bot.ID, &UpdateBotRequest{Name: strPtrBot("hijack")}); err == nil {
		t.Fatalf("陌生人 update 应拒绝")
	}
	if err := env.bots.Delete(stranger, bot.ID); err == nil {
		t.Fatalf("陌生人 delete 应拒绝")
	}
	if _, err := env.bots.SendMessage(stranger, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "c1"}); err == nil {
		t.Fatalf("陌生人发消息应拒绝")
	}
	if _, err := env.bots.ListRuns(stranger, bot.ID, 10); err == nil {
		t.Fatalf("陌生人查 run 应拒绝")
	}
	if _, err := env.bots.Get(owner, bot.ID); err != nil {
		t.Fatalf("owner get: %v", err)
	}
}

// ---------- 批次二：账本、幂等、主线、SSE 前缀 ----------

func TestBotAppendOnly(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botappend")
	bot := env.createBot(t, userID)

	result, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "第一条", ClientMessageID: "a1"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if result.Message.Seq != 1 || !result.Queued {
		t.Fatalf("unexpected message: %+v", result.Message)
	}

	// 防 UPDATE 守护触发器：任何 UPDATE 直接失败。
	err = env.db.Exec("UPDATE bot_messages SET content_json = ? WHERE bot_id = ?", `{"text":"篡改"}`, bot.ID).Error
	if err == nil {
		t.Fatalf("bot_messages 应禁止 UPDATE")
	}
	if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("守护触发器错误信息不符: %v", err)
	}

	dup := "dup:" + uuid.NewString()
	seq, err := repository.NewBotRepository(env.db).NextMessageSeqTx(env.db, bot.ID)
	if err != nil {
		t.Fatalf("seq: %v", err)
	}
	msg := &model.BotMessage{ID: uuid.NewString(), BotID: bot.ID, Seq: seq, Kind: model.BotMessageKindLifecycle,
		ContentJSON: "{}", DedupKey: &dup, CreatedAt: time.Now().UTC()}
	if err := repository.NewBotMessageRepository(env.db).AppendTx(env.db, msg); err != nil {
		t.Fatalf("append: %v", err)
	}
	msg2 := *msg
	msg2.ID = uuid.NewString()
	if err := repository.NewBotMessageRepository(env.db).AppendTx(env.db, &msg2); err == nil {
		t.Fatalf("同 dedup_key 应唯一约束失败")
	}

	items, _, err := env.bots.ListMessages(userID, bot.ID, 0, 0, 50)
	if err != nil || len(items) != 2 {
		t.Fatalf("list 应含用户消息与去重用 lifecycle 夹具: %v %d", err, len(items))
	}
}

// TestBotListMessagesDefaultPage 分页契约：两边界缺省（聊天首屏）与只给
// before（「加载更早」）都取距账本末尾最近的 limit 条；只给 after 取其后
// 最早的 limit 条；页内一律 seq 正序。
func TestBotListMessagesDefaultPage(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botpage")
	bot := env.createBot(t, userID)

	msgs := repository.NewBotMessageRepository(env.db)
	const total = 7
	for i := 0; i < total; i++ {
		seq, err := repository.NewBotRepository(env.db).NextMessageSeqTx(env.db, bot.ID)
		if err != nil {
			t.Fatalf("seq: %v", err)
		}
		dedup := fmt.Sprintf("page:%d", i)
		msg := &model.BotMessage{ID: uuid.NewString(), BotID: bot.ID, Seq: seq,
			Kind: model.BotMessageKindLifecycle, ContentJSON: "{}", DedupKey: &dedup,
			CreatedAt: time.Now().UTC()}
		if err := msgs.AppendTx(env.db, msg); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	seqs := func(items []BotMessageResponse) []int64 {
		out := make([]int64, 0, len(items))
		for _, m := range items {
			out = append(out, m.Seq)
		}
		return out
	}

	items, _, err := env.bots.ListMessages(userID, bot.ID, 0, 0, 3)
	if err != nil {
		t.Fatalf("default page: %v", err)
	}
	if got := seqs(items); len(got) != 3 || got[0] != 5 || got[1] != 6 || got[2] != 7 {
		t.Fatalf("缺省应返回最新 3 条且正序: %v", got)
	}

	items, _, err = env.bots.ListMessages(userID, bot.ID, 5, 0, 3)
	if err != nil {
		t.Fatalf("before page: %v", err)
	}
	if got := seqs(items); len(got) != 3 || got[0] != 2 || got[1] != 3 || got[2] != 4 {
		t.Fatalf("before=5 应返回其前最近 3 条且正序: %v", got)
	}

	items, _, err = env.bots.ListMessages(userID, bot.ID, 0, 5, 10)
	if err != nil {
		t.Fatalf("after page: %v", err)
	}
	if got := seqs(items); len(got) != 2 || got[0] != 6 || got[1] != 7 {
		t.Fatalf("after=5 应返回其后全部且正序: %v", got)
	}
}

func TestBotIdempotentSend(t *testing.T) {
	testBotIdempotentSend(t)
}

// TestBotIdempotency 与 TestBotIdempotentSend 同场景（设计批次三验收命令
// 用 Idempotency 命名）。
func TestBotIdempotency(t *testing.T) {
	testBotIdempotentSend(t)
}

func testBotIdempotentSend(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "收到"})
	userID := createBotTestUser(t, env.db, "botidem")
	bot := env.createBot(t, userID)

	first, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "你好", ClientMessageID: "same-key"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	again, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "你好", ClientMessageID: "same-key"})
	if err != nil {
		t.Fatalf("idempotent resend: %v", err)
	}
	if again.Message.ID != first.Message.ID {
		t.Fatalf("同键同正文应返回已有消息")
	}
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "不一样", ClientMessageID: "same-key"}); !errors.Is(err, errs.ErrBotClientMessageConflict) {
		t.Fatalf("同键不同正文应 40925: %v", err)
	}
	waitForBot(t, "run completed", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})
	var runs []model.BotRun
	env.db.Where("bot_id = ?", bot.ID).Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("重复投递只应有一个 run: %d [%s]", len(runs), dumpRuns(t, env, bot.ID))
	}
}

func TestBotSyncTool(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{ID: "call-1", Name: "fs_project", Args: `{"action":"list","page":1,"page_size":10}`}}},
	)
	userID := createBotTestUser(t, env.db, "botsync")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "列一下项目", ClientMessageID: "s1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	waitForBot(t, "run completed", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})

	messages, _, err := env.bots.ListMessages(userID, bot.ID, 0, 0, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	kinds := []string{}
	for _, m := range messages {
		kinds = append(kinds, m.Kind)
	}
	// user → assistant(tool_calls) → tool_reply → assistant(最终答复)。
	if len(kinds) != 4 || kinds[0] != "user" || kinds[1] != "assistant" || kinds[2] != "tool_reply" || kinds[3] != "assistant" {
		t.Fatalf("消息序列不符: %v runs=[%s] msgs=%s", kinds, dumpRuns(t, env, bot.ID), dumpMessages(t, env, bot.ID))
	}

	calls, _ := env.bots.ListCalls(userID, bot.ID, nil, 10)
	if len(calls) != 1 || calls[0].Status != "succeeded" || calls[0].ExecutionMode != "sync" {
		t.Fatalf("sync call: %+v", calls)
	}
	if calls[0].ProviderToolCallID != "call-1" {
		t.Fatalf("provider id 配对失败: %+v", calls[0])
	}
}

func TestBotSafePoint(t *testing.T) {
	hold := make(chan struct{})
	env := setupBotTestEnv(t, fakeModelTurn{Text: "第一轮回答", Hold: hold})
	userID := createBotTestUser(t, env.db, "botsafe")
	bot := env.createBot(t, userID)

	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "先问一句", ClientMessageID: "p1"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// 模型请求进行中（Hold）插入新输入：不取消进行中的请求，安全点被看到。
	env.fake.nextRequest(t, 10*time.Second)
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "插队消息", ClientMessageID: "p2"}); err != nil {
		t.Fatalf("send2: %v", err)
	}
	close(hold)

	second := env.fake.nextRequest(t, 15*time.Second)
	elems, _ := rawMessages(t, second.Body)
	found := false
	for _, e := range elems {
		if strings.Contains(string(e), "插队消息") {
			found = true
		}
	}
	if !found {
		t.Fatalf("后续模型请求未包含插队消息")
	}
	waitForBot(t, "run completed", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})
	messages, _, _ := env.bots.ListMessages(userID, bot.ID, 0, 0, 50)
	// user x2 + assistant x2。
	if len(messages) != 4 {
		t.Fatalf("消息数应为 4: %d kinds=%v runs=[%s]", len(messages), func() []string {
			ks := []string{}
			for _, m := range messages {
				ks = append(ks, m.Kind+":"+string(m.Content))
			}
			return ks
		}(), dumpRuns(t, env, bot.ID))
	}
}

func TestBotSingleRun(t *testing.T) {
	hold := make(chan struct{})
	env := setupBotTestEnv(t, fakeModelTurn{Text: "r1", Hold: hold})
	userID := createBotTestUser(t, env.db, "botsingle")
	bot := env.createBot(t, userID)

	for i := 0; i < 3; i++ {
		if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{
			Text: fmt.Sprintf("msg-%d", i), ClientMessageID: fmt.Sprintf("k%d", i),
		}); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	env.fake.nextRequest(t, 10*time.Second)
	close(hold)
	waitForBot(t, "run completed", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})
	var runs []model.BotRun
	env.db.Where("bot_id = ?", bot.ID).Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("同一机器人至多一个主线 run: %d [%s]", len(runs), dumpRuns(t, env, bot.ID))
	}
	if runs[0].Status != model.BotRunStatusCompleted {
		t.Fatalf("run 状态: %s", runs[0].Status)
	}
}

func TestBotPrefix(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{Text: "第一答"},
	)
	userID := createBotTestUser(t, env.db, "botprefix")
	bot := env.createBot(t, userID)

	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "问题一", ClientMessageID: "q1"})
	req1 := env.fake.nextRequest(t, 10*time.Second)
	waitForBot(t, "第一轮完成", 15*time.Second, func() bool {
		msgs, _, _ := env.bots.ListMessages(userID, bot.ID, 0, 0, 50)
		return len(msgs) >= 2
	})
	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "问题二", ClientMessageID: "q2"})
	req2 := env.fake.nextRequest(t, 10*time.Second)
	waitForBot(t, "run completed", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})

	// 字节级：第二轮请求的 tools 与第一轮完全一致；第二轮 messages 去掉末尾
	// 运行时上下文后，以第一轮 messages 去掉末尾上下文为字节前缀（不可变
	// 前缀只追加、不改写）。
	elems1, tools1 := rawMessages(t, req1.Body)
	elems2, tools2 := rawMessages(t, req2.Body)
	if string(tools1) != string(tools2) {
		t.Fatalf("tools 字节不一致")
	}
	prefix1 := elems1[:len(elems1)-1]
	if len(elems2)-1 < len(prefix1) {
		t.Fatalf("第二轮前缀变短")
	}
	for i := range prefix1 {
		if string(prefix1[i]) != string(elems2[i]) {
			t.Fatalf("不可变前缀在第 %d 条消息漂移:\n%s\nvs\n%s", i, prefix1[i], elems2[i])
		}
	}

	// 库里的请求记录：同一次请求重复构建 hash 稳定；tools 哈希跨请求一致。
	var reqs []model.BotModelRequest
	env.db.Joins("JOIN bot_runs ON bot_runs.id = bot_model_requests.run_id").
		Where("bot_runs.bot_id = ?", bot.ID).Order("bot_model_requests.ordinal ASC").Find(&reqs)
	if len(reqs) < 2 {
		t.Fatalf("应有两次模型请求: %d", len(reqs))
	}
	if reqs[0].ToolsHash != reqs[1].ToolsHash || reqs[0].ToolsHash == "" {
		t.Fatalf("tools 哈希漂移")
	}
	run, _ := env.bots.runs.FindByID(reqs[1].RunID)
	botRow, _ := env.bots.bots.Get(bot.ID)
	built1, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build1: %v", err)
	}
	built2, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build2: %v", err)
	}
	if built1.PrefixHash != built2.PrefixHash || built1.RequestHash != built2.RequestHash {
		t.Fatalf("同状态重复构建 hash 漂移")
	}
}

func TestBotUsage(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{
		Text:  "带 usage",
		Usage: &fakeModelUsage{Prompt: 11, Completion: 7, Total: 18, Cached: 4},
	})
	userID := createBotTestUser(t, env.db, "botusage")
	bot := env.createBot(t, userID)
	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "u1"})
	waitForBot(t, "run completed", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})
	runs, _ := env.bots.ListRuns(userID, bot.ID, 5)
	if len(runs) != 1 || !runs[0].UsageComplete {
		t.Fatalf("usage_complete 应为 true: %+v", runs)
	}
	if runs[0].PromptTokens == nil || *runs[0].PromptTokens != 11 ||
		runs[0].CachedTokens == nil || *runs[0].CachedTokens != 4 ||
		runs[0].TotalTokens == nil || *runs[0].TotalTokens != 18 {
		t.Fatalf("usage 统计不符: %+v", runs[0])
	}
	var req model.BotModelRequest
	env.db.Where("run_id = ?", runs[0].ID).First(&req)
	if req.TotalTokens == nil || *req.TotalTokens != 18 {
		t.Fatalf("请求级 usage: %+v", req)
	}
}

func TestBotUsageMissing(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "无 usage"})
	userID := createBotTestUser(t, env.db, "botusage2")
	bot := env.createBot(t, userID)
	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "u2"})
	waitForBot(t, "run completed", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})
	runs, _ := env.bots.ListRuns(userID, bot.ID, 5)
	if len(runs) != 1 {
		t.Fatalf("runs: %d", len(runs))
	}
	if runs[0].UsageComplete {
		t.Fatalf("无 usage chunk 时 usage_complete 应为 false")
	}
	if runs[0].PromptTokens != nil || runs[0].TotalTokens != nil {
		t.Fatalf("缺失 usage 不应伪造统计: %+v", runs[0])
	}
}

// ---------- 批次三：写确认、授权、取消、恢复、结果 ----------

func TestBotApproval(t *testing.T) {
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{ID: "call-w", Name: "fs_project", Args: `{"action":"create","name":"机器人建的项目","description":"描述"}`}}},
	)
	userID := createBotTestUser(t, env.db, "botappr")
	bot := env.createBot(t, userID)

	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "帮我建个项目", ClientMessageID: "w1"})

	var callID string
	waitForBot(t, "待确认调用", 15*time.Second, func() bool {
		calls, _ := env.bots.ListCalls(userID, bot.ID, []string{"pending_confirmation"}, 5)
		if len(calls) > 0 {
			callID = calls[0].ID
			return true
		}
		return false
	})

	// 回执先于结果：批准前只有 receipt，没有终态。
	msgs, _, _ := env.bots.ListMessages(userID, bot.ID, 0, 0, 50)
	hasReceipt := false
	for _, m := range msgs {
		if m.Kind == "tool_reply" {
			hasReceipt = true
		}
		if m.Kind == "background_result" {
			t.Fatalf("批准前不应有终态结果消息")
		}
	}
	if !hasReceipt {
		t.Fatalf("应有异步回执消息")
	}

	decided, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "approve"})
	if err != nil || decided.Status != "queued" {
		t.Fatalf("approve: %v %+v", err, decided)
	}
	// 重复相同决定幂等；不同决定冲突。
	if _, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "approve"}); err != nil {
		t.Fatalf("重复 approve 应幂等: %v", err)
	}
	if _, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "reject"}); !errors.Is(err, errs.ErrBotCallStateConflict) {
		t.Fatalf("冲突决定应 40926: %v", err)
	}

	waitForBot(t, "项目创建", 20*time.Second, func() bool {
		projects, _, err := env.bots.projects.List(userID, 1, 50)
		return err == nil && len(projects) > 0
	})
	projects, _, _ := env.bots.projects.List(userID, 1, 50)
	if projects[0].Name != "机器人建的项目" || projects[0].Description != "描述" {
		t.Fatalf("项目内容不符: %+v", projects[0])
	}
	waitForBot(t, "run 继续", 20*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})
	msgs, _, _ = env.bots.ListMessages(userID, bot.ID, 0, 0, 50)
	hasBackground := false
	for _, m := range msgs {
		if m.Kind == "background_result" && m.CallID != nil && *m.CallID == callID {
			hasBackground = true
		}
	}
	if !hasBackground {
		t.Fatalf("应有平台后台结果消息")
	}
}

func TestBotReject(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{
		ToolCalls: []fakeModelToolCall{{ID: "call-r", Name: "fs_project", Args: `{"action":"create","name":"不该建"}`}}},
	)
	userID := createBotTestUser(t, env.db, "botreject")
	bot := env.createBot(t, userID)
	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "建项目", ClientMessageID: "r1"})

	var callID string
	waitForBot(t, "待确认", 15*time.Second, func() bool {
		calls, _ := env.bots.ListCalls(userID, bot.ID, []string{"pending_confirmation"}, 5)
		if len(calls) > 0 {
			callID = calls[0].ID
			return true
		}
		return false
	})
	decided, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: "reject"})
	if err != nil || decided.Status != "rejected" {
		t.Fatalf("reject: %v %+v", err, decided)
	}
	waitForBot(t, "终态结果", 10*time.Second, func() bool {
		msgs, _, _ := env.bots.ListMessages(userID, bot.ID, 0, 0, 50)
		for _, m := range msgs {
			if m.Kind == "background_result" {
				return true
			}
		}
		return false
	})
	projects, _, _ := env.bots.projects.List(userID, 1, 50)
	if len(projects) != 0 {
		t.Fatalf("拒绝后不应创建项目")
	}
}

func TestBotAuthorization(t *testing.T) {
	env := setupBotTestEnv(t,
		// run1 请求1：发起 a1（待确认）
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{ID: "call-a1", Name: "fs_project", Args: `{"action":"create","name":"授权项目1"}`}}},
		// run1 请求2：看到回执，先答复用户（pending 不阻塞主线）
		fakeModelTurn{Text: "已受理，等确认"},
		// run2 请求：后台结果送达后模型再发起 a2（同规则应免确认）
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{ID: "call-a2", Name: "fs_project", Args: `{"action":"create","name":"授权项目2"}`}}},
		fakeModelTurn{Text: "第二个也建好了"},
	)
	userID := createBotTestUser(t, env.db, "botauth")
	bot := env.createBot(t, userID)

	// 第一次：确认卡片 → always_allow。
	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "建项目", ClientMessageID: "au1"})
	var call1ID string
	waitForBot(t, "待确认", 15*time.Second, func() bool {
		calls, _ := env.bots.ListCalls(userID, bot.ID, []string{"pending_confirmation"}, 5)
		if len(calls) > 0 {
			call1ID = calls[0].ID
			return true
		}
		return false
	})
	if _, err := env.bots.DecideCall(userID, bot.ID, call1ID, &CallDecisionRequest{Decision: "always_allow"}); err != nil {
		t.Fatalf("always_allow: %v", err)
	}
	authzs, err := env.bots.ListAuthorizations(userID, bot.ID, true)
	if err != nil || len(authzs) != 1 {
		t.Fatalf("应有 1 条活跃授权: %v %d", err, len(authzs))
	}
	if authzs[0].Action != "create" || authzs[0].ScopeKind != "project_collection" || authzs[0].ScopeID != userID {
		t.Fatalf("授权范围不符: %+v", authzs[0])
	}

	// run1 在后台继续消化脚本（a2 的调用在 run2），按名字等待而不是按数量。
	waitForBot(t, "项目1 创建", 20*time.Second, func() bool {
		return botProjectExists(t, env, userID, "授权项目1")
	})

	// 第二次同规则调用：免确认直接排队执行。
	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "再建一个", ClientMessageID: "au2"})
	waitForBot(t, "项目2 创建", 20*time.Second, func() bool {
		return botProjectExists(t, env, userID, "授权项目2")
	})
	calls, _ := env.bots.ListCalls(userID, bot.ID, nil, 10)
	for _, c := range calls {
		if c.Status == "pending_confirmation" {
			t.Fatalf("同规则第二次调用不应再等确认: %+v calls=%+v runs=[%s]", c, calls, dumpRuns(t, env, bot.ID))
		}
	}
}

func botProjectExists(t *testing.T, env *botTestEnv, userID, name string) bool {
	t.Helper()
	projects, _, _ := env.bots.projects.List(userID, 1, 50)
	for _, p := range projects {
		if p.Name == name {
			return true
		}
	}
	return false
}

func TestBotRevoke(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botrevoke")
	bot := env.createBot(t, userID)

	// 直接落一条带授权的 queued 调用（等价「已批准、待认领」状态）。
	now := time.Now().UTC()
	authz := &model.BotAuthorization{
		ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_project", Action: "create",
		ScopeKind: "project_collection", ScopeID: userID,
		ConstraintsJSON: "{}", RuleHash: "hash-revoke", CreatedAt: now,
	}
	if err := env.db.Create(authz).Error; err != nil {
		t.Fatalf("seed authz: %v", err)
	}
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionAsync))
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).
		Update("authorization_id", authz.ID).Error; err != nil {
		t.Fatalf("bind authz: %v", err)
	}

	if err := env.bots.RevokeAuthorization(userID, bot.ID, authz.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if err := env.bots.RevokeAuthorization(userID, bot.ID, authz.ID); err != nil {
		t.Fatalf("重复 revoke 应幂等: %v", err)
	}
	active, _ := env.bots.ListAuthorizations(userID, bot.ID, true)
	if len(active) != 0 {
		t.Fatalf("撤销后不应有活跃授权")
	}
	revoked, _ := env.bots.ListAuthorizations(userID, bot.ID, false)
	if len(revoked) != 1 || revoked[0].RevokedAt == nil {
		t.Fatalf("撤销保留审计行: %+v", revoked)
	}
	// 尚未认领的授权调用回到待确认。
	fresh, err := env.bots.GetCall(userID, bot.ID, callID)
	if err != nil || fresh.Status != "pending_confirmation" || fresh.AuthorizationID != nil {
		t.Fatalf("撤销应让 queued 调用回到待确认: %v %+v", err, fresh)
	}
}

func TestBotCancelCall(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botcancel")
	bot := env.createBot(t, userID)
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionAsync))

	after, err := env.bots.CancelCall(userID, bot.ID, callID)
	if err != nil || after.Status != "cancelled" {
		t.Fatalf("cancel queued: %v %+v", err, after)
	}
	if after.EffectMayHaveOccurred {
		t.Fatalf("未执行的取消不应标记副作用")
	}
	// 重复取消幂等。
	again, err := env.bots.CancelCall(userID, bot.ID, callID)
	if err != nil || again.Status != "cancelled" {
		t.Fatalf("cancel idempotent: %v %+v", err, again)
	}
	// 终态唯一：迟到执行不能覆盖 cancelled。
	env.bots.executeQueuedCall(callID)
	final, _ := env.bots.calls.FindByID(callID)
	if final.Status != model.BotCallStatusCancelled || final.FinishedAt == nil {
		t.Fatalf("迟到结果覆盖了 cancelled 终态: %s", final.Status)
	}
	// 终态结果消息恰好一条。
	msgs, _, _ := env.bots.ListMessages(userID, bot.ID, 0, 0, 100)
	terminal := 0
	for _, m := range msgs {
		if m.Kind == "background_result" && m.CallID != nil && *m.CallID == callID {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("终态结果消息应恰好一条: %d", terminal)
	}
	// 未终结调用存在时删除冲突；全部终态后可删除。
	seedBotCallFixture(t, env, bot.ID, model.BotCallStatusPendingConfirmation, string(model.BotExecutionAsync))
	if err := env.bots.Delete(userID, bot.ID); !errors.Is(err, errs.ErrBotDeleteConflict) {
		t.Fatalf("有未终结调用时删除应 40927: %v", err)
	}
	for {
		calls, _ := env.bots.ListCalls(userID, bot.ID, []string{"pending_confirmation", "queued", "running"}, 50)
		if len(calls) == 0 {
			break
		}
		for _, c := range calls {
			if _, err := env.bots.CancelCall(userID, bot.ID, c.ID); err != nil {
				t.Fatalf("cleanup cancel: %v", err)
			}
		}
		break
	}
	// 取消产生的后台结果会立即唤醒处理它的 run：等它结束后再删除。
	waitForBot(t, "取消 run 结束", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})
	if err := env.bots.Delete(userID, bot.ID); err != nil {
		t.Fatalf("终态后删除应成功: %v", err)
	}
}

func TestBotStopRun(t *testing.T) {
	hold := make(chan struct{})
	env := setupBotTestEnv(t, fakeModelTurn{Text: "长回答", Hold: hold})
	userID := createBotTestUser(t, env.db, "botstop")
	bot := env.createBot(t, userID)

	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "开始", ClientMessageID: "st1"})
	env.fake.nextRequest(t, 10*time.Second)

	runs, _ := env.bots.ListRuns(userID, bot.ID, 5)
	if len(runs) != 1 {
		t.Fatalf("runs: %d", len(runs))
	}
	stopped, err := env.bots.RequestStop(userID, bot.ID, runs[0].ID)
	if err != nil || !stopped.StopRequested {
		t.Fatalf("stop: %v %+v", err, stopped)
	}
	close(hold)
	waitForBot(t, "run stopped", 15*time.Second, func() bool {
		fresh, e := env.bots.GetRun(userID, bot.ID, runs[0].ID)
		return e == nil && fresh.Status == "stopped" && fresh.FinishedAt != nil
	})
}

func TestBotCancelOwnCallTool(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botcancelt")
	bot := env.createBot(t, userID)
	_, targetID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionAsync))
	target, _ := env.bots.calls.FindByID(targetID)

	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: target.RequestID,
		ProviderToolCallID: "prov-x", ToolName: "fs_cancel", Action: "cancel",
		ArgumentsJSON: fmt.Sprintf(`{"action":"cancel","call_id":%q}`, targetID),
		ArgumentsHash: "x", ExecutionMode: "sync", Status: model.BotCallStatusQueued,
		ExecutionContextJSON: "{}", CreatedAt: time.Now().UTC(),
	}
	if err := env.bots.calls.CreateTx(env.db, call); err != nil {
		t.Fatalf("seed: %v", err)
	}
	content, err := env.bots.botExecuteSyncTool(context.Background(), call)
	if err != nil {
		t.Fatalf("fs_cancel: %v", err)
	}
	if !strings.Contains(content, `"status":"cancelled"`) {
		t.Fatalf("fs_cancel 回复不符: %s", content)
	}
}

func TestBotCapabilityAndHelp(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botcap")
	bot := env.createBot(t, userID)

	for _, tc := range []struct {
		tool, action, args, contains string
	}{
		{"fs_schedule", "status", `{"action":"status"}`, `"available":false`},
		{"fs_memory", "status", `{"action":"status"}`, `"capability":"memory"`},
		{"fs_help", "read", "{}", "工具说明"},
	} {
		call := &model.BotToolCall{
			ID: uuid.NewString(), BotID: bot.ID, RequestID: "req-x",
			ProviderToolCallID: "prov", ToolName: tc.tool, Action: tc.action,
			ArgumentsJSON: tc.args, ArgumentsHash: "x", ExecutionMode: "sync",
			Status: model.BotCallStatusQueued, ExecutionContextJSON: "{}",
			CreatedAt: time.Now().UTC(),
		}
		content, err := env.bots.botExecuteSyncTool(context.Background(), call)
		if err != nil {
			t.Fatalf("%s: %v", tc.tool, err)
		}
		if !strings.Contains(content, tc.contains) {
			t.Fatalf("%s 回复不含 %s: %s", tc.tool, tc.contains, content)
		}
	}
}

func TestBotResultTruncation(t *testing.T) {
	// 超预算结果：截断预览不切断 UTF-8；fs_result.read 按 Unicode 字符续读。
	long := strings.Repeat("汉", botResultBudgetChars+500)
	full := botJSON(map[string]string{"data": long})
	out := botBuildOutcomeRaw("call-x", full, nil)
	if !out.Truncated {
		t.Fatalf("应截断")
	}
	var preview struct {
		Truncated  bool   `json:"truncated"`
		NextOffset int64  `json:"next_offset"`
		Preview    string `json:"preview"`
	}
	if err := json.Unmarshal([]byte(out.PreviewJSON), &preview); err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !preview.Truncated || preview.NextOffset != botResultBudgetChars {
		t.Fatalf("preview meta: %+v", preview)
	}
	if len([]rune(preview.Preview)) != botResultBudgetChars {
		t.Fatalf("preview 长度不符")
	}
	if strings.ContainsRune(preview.Preview, 0xFFFD) {
		t.Fatalf("预览不应切断 UTF-8")
	}
	// 完整结果保存在 result_json，续读能取到后半段。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botresult")
	bot := env.createBot(t, userID)
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusSucceeded, string(model.BotExecutionSync))
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).
		Updates(map[string]interface{}{"result_json": out.ResultJSON, "result_preview_json": out.PreviewJSON,
			"result_chars": out.ResultChars, "truncated": true}).Error; err != nil {
		t.Fatalf("store result: %v", err)
	}
	reader := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: "req-x",
		ProviderToolCallID: "prov", ToolName: "fs_result", Action: "read",
		ArgumentsJSON: fmt.Sprintf(`{"action":"read","call_id":%q,"offset":%d,"limit":10}`, callID, botResultBudgetChars),
		ArgumentsHash: "x", ExecutionMode: "sync", Status: model.BotCallStatusQueued,
		ExecutionContextJSON: "{}", CreatedAt: time.Now().UTC(),
	}
	content, err := env.bots.botExecuteSyncTool(context.Background(), reader)
	if err != nil {
		t.Fatalf("fs_result.read: %v", err)
	}
	var page struct {
		Offset   int64  `json:"offset"`
		Content  string `json:"content"`
		HasMore  bool   `json:"has_more"`
		TotalLen int64  `json:"total_chars"`
	}
	if err := json.Unmarshal([]byte(content), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if page.Offset != botResultBudgetChars || len([]rune(page.Content)) != 10 || !page.HasMore {
		t.Fatalf("续读结果不符: %+v", page)
	}
	// 越权读取别的机器人：按不存在处理。
	other := env.createBot(t, userID)
	_, otherCall := seedBotCallFixture(t, env, other.ID, model.BotCallStatusSucceeded, "sync")
	env.db.Model(&model.BotToolCall{}).Where("id = ?", otherCall).
		Update("result_json", "{}")
	peek := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID, RequestID: "req-x",
		ProviderToolCallID: "prov", ToolName: "fs_result", Action: "read",
		ArgumentsJSON: fmt.Sprintf(`{"action":"read","call_id":%q}`, otherCall),
		ArgumentsHash: "x", ExecutionMode: "sync", Status: model.BotCallStatusQueued,
		ExecutionContextJSON: "{}", CreatedAt: time.Now().UTC(),
	}
	if _, err := env.bots.botExecuteSyncTool(context.Background(), peek); !errors.Is(err, errs.ErrBotCallNotFound) {
		t.Fatalf("越权读取应 40426: %v", err)
	}
}

func TestBotRecovery(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botrecover")
	bot := env.createBot(t, userID)

	// 崩溃现场：queued 异步、pending 异步、running 异步（带回执）、
	// running 同步（无回执）、活动主线 run。
	seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionAsync))
	seedBotCallFixture(t, env, bot.ID, model.BotCallStatusPendingConfirmation, string(model.BotExecutionAsync))
	_, asyncRunningID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusRunning, string(model.BotExecutionAsync))
	_, syncRunningID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusRunning, string(model.BotExecutionSync))
	// 异步 running 已有回执。
	seq, _ := repository.NewBotRepository(env.db).NextMessageSeqTx(env.db, bot.ID)
	receipt := &model.BotMessage{
		ID: uuid.NewString(), BotID: bot.ID, Seq: seq, CallID: &asyncRunningID,
		Kind: model.BotMessageKindToolReply, ContentJSON: "{}",
		DedupKey: strPtrBot("receipt:" + asyncRunningID), CreatedAt: time.Now().UTC(),
	}
	if err := repository.NewBotMessageRepository(env.db).AppendTx(env.db, receipt); err != nil {
		t.Fatalf("receipt: %v", err)
	}
	activeRun := &model.BotRun{
		ID: uuid.NewString(), BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "recovery-fixture",
		TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		ConfigSnapshotJSON: "{}", CreatedAt: time.Now().UTC(),
	}
	if err := env.bots.runs.CreateTx(env.db, activeRun); err != nil {
		t.Fatalf("seed active run: %v", err)
	}

	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recovery: %v", err)
	}

	fresh, err := env.bots.GetRun(userID, bot.ID, activeRun.ID)
	if err != nil || fresh.Status != "failed" || fresh.ErrorCode == nil || *fresh.ErrorCode != "server_restarted" {
		t.Fatalf("run 恢复: %v %+v", err, fresh)
	}
	calls, _ := env.bots.ListCalls(userID, bot.ID, nil, 20)
	statusCount := map[string]int{}
	for _, c := range calls {
		statusCount[c.Status]++
	}
	if statusCount["running"] != 0 {
		t.Fatalf("恢复后不应有 running 调用: %v", statusCount)
	}
	if statusCount["queued"] != 1 || statusCount["pending_confirmation"] != 1 {
		t.Fatalf("queued/pending 应保留: %v", statusCount)
	}
	var asyncRunning model.BotToolCall
	env.db.Where("id = ?", asyncRunningID).First(&asyncRunning)
	if asyncRunning.Status != model.BotCallStatusFailed || !asyncRunning.EffectMayHaveOccurred {
		t.Fatalf("执行中崩溃的异步调用应 failed + effect 标记: %+v", asyncRunning)
	}
	var syncRunning model.BotToolCall
	env.db.Where("id = ?", syncRunningID).First(&syncRunning)
	if syncRunning.Status != model.BotCallStatusFailed || syncRunning.EffectMayHaveOccurred {
		t.Fatalf("同步读取中断不应标记 effect: %+v", syncRunning)
	}
	// 同步中断补了「已被中断」协议回复；异步已有回执不再补第二条。
	var syncReplies int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND call_id = ? AND kind = ?",
		bot.ID, syncRunningID, model.BotMessageKindToolReply).Count(&syncReplies)
	if syncReplies != 1 {
		t.Fatalf("同步中断应补恰好一条回复: %d", syncReplies)
	}
	var asyncReplies int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND call_id = ? AND kind = ?",
		bot.ID, asyncRunningID, model.BotMessageKindToolReply).Count(&asyncReplies)
	if asyncReplies != 1 {
		t.Fatalf("已有回执的调用恢复不应重复追加回复: %d", asyncReplies)
	}

	// 恢复通知触发 recovery run；重复恢复不重复追加通知。
	waitForBot(t, "recovery run", 15*time.Second, func() bool {
		runs, _ := env.bots.ListRuns(userID, bot.ID, 10)
		for _, r := range runs {
			if r.TriggerSource == "recovery" {
				return true
			}
		}
		return false
	})
	waitForBot(t, "recovery run 完成", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})
	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recovery 重复执行: %v", err)
	}
	notices, _, _ := env.bots.ListMessages(userID, bot.ID, 0, 0, 100)
	count := 0
	for _, m := range notices {
		if m.Kind == "lifecycle" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("恢复通知应恰好一条: %d", count)
	}
}

// ---------- 事务纪律：单连接池 ----------

func TestBotSingleConn(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{Text: "单连接也通"})
	limitPoolToSingleConn(t, env.testServices)
	userID := createBotTestUser(t, env.db, "botsingleconn")
	bot := env.createBot(t, userID)

	completeWithin(t, "BotSendMessage", func() error {
		_, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "sc1"})
		return err
	})
	waitForBot(t, "run completed (单连接)", 20*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})
}

// ---------- 测试小工具 ----------

func strPtrBot(s string) *string { return &s }

// dumpMessages 失败诊断：打印消息 kind 与内容摘要。
func dumpMessages(t *testing.T, env *botTestEnv, botID string) string {
	t.Helper()
	msgs, _, _ := env.bots.ListMessages(botUserIDOf(t, env, botID), botID, 0, 0, 100)
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		content := string(m.Content)
		if len(content) > 120 {
			content = content[:120]
		}
		parts = append(parts, fmt.Sprintf("%d:%s:%s", m.Seq, m.Kind, content))
	}
	return strings.Join(parts, " | ")
}

func botUserIDOf(t *testing.T, env *botTestEnv, botID string) string {
	t.Helper()
	bot, err := env.bots.bots.Get(botID)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}
	return bot.UserID
}

// dumpRuns 失败诊断：打印 run 行的关键字段。
func dumpRuns(t *testing.T, env *botTestEnv, botID string) string {
	t.Helper()
	var runs []model.BotRun
	env.db.Where("bot_id = ?", botID).Order("created_at").Find(&runs)
	parts := make([]string, 0, len(runs))
	for _, r := range runs {
		errMsg := ""
		if r.ErrorMessage != nil {
			errMsg = *r.ErrorMessage
		}
		parts = append(parts, fmt.Sprintf("{key:%s status:%s err:%s}", r.TriggerKey, r.Status, errMsg))
	}
	return strings.Join(parts, ", ")
}

func botJSON(v interface{}) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

// TestBotReceipt 异步受理的回执先于任何终态：accepted=true 且 status=pending。
func TestBotReceipt(t *testing.T) {
	env := setupBotTestEnv(t, fakeModelTurn{
		ToolCalls: []fakeModelToolCall{{ID: "call-rc", Name: "fs_project", Args: `{"action":"create","name":"回执项目"}`}}},
	)
	userID := createBotTestUser(t, env.db, "botreceipt")
	bot := env.createBot(t, userID)
	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "建项目", ClientMessageID: "rc1"})

	var callID string
	waitForBot(t, "待确认", 15*time.Second, func() bool {
		calls, _ := env.bots.ListCalls(userID, bot.ID, []string{"pending_confirmation"}, 5)
		if len(calls) > 0 {
			callID = calls[0].ID
			return true
		}
		return false
	})
	msgs, _, _ := env.bots.ListMessages(userID, bot.ID, 0, 0, 50)
	receiptSeq, terminalSeq := int64(-1), int64(-1)
	for _, m := range msgs {
		if m.Kind == "tool_reply" && m.CallID != nil && *m.CallID == callID {
			receiptSeq = m.Seq
			var content botToolReplyContent
			if err := json.Unmarshal(m.Content, &content); err != nil {
				t.Fatalf("receipt content: %v", err)
			}
			var payload map[string]interface{}
			if err := json.Unmarshal([]byte(content.Content), &payload); err != nil {
				t.Fatalf("receipt payload: %v", err)
			}
			if payload["accepted"] != true || payload["status"] != "pending_confirmation" {
				t.Fatalf("回执内容不符: %v", payload)
			}
		}
		if m.Kind == "background_result" {
			terminalSeq = m.Seq
		}
	}
	if receiptSeq == -1 {
		t.Fatalf("回执缺失")
	}
	if terminalSeq != -1 {
		t.Fatalf("终态不应先于回执出现")
	}
}

// TestBotAsync 排队的授权调用被 worker 认领执行：终态、结果与后台结果消息
// 一次性落定。
func TestBotAsync(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botasync")
	bot := env.createBot(t, userID)
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionAsync))
	env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).
		Update("arguments_json", `{"action":"create","name":"异步执行的项目"}`)

	env.bots.executeQueuedCall(callID)

	fresh, err := env.bots.GetCall(userID, bot.ID, callID)
	if err != nil || fresh.Status != "succeeded" {
		t.Fatalf("异步执行: %v %+v", err, fresh)
	}
	if !botProjectExists(t, env, userID, "异步执行的项目") {
		t.Fatalf("异步写操作未生效")
	}
	msgs, _, _ := env.bots.ListMessages(userID, bot.ID, 0, 0, 100)
	hasBackground := false
	for _, m := range msgs {
		if m.Kind == "background_result" && m.CallID != nil && *m.CallID == callID {
			hasBackground = true
		}
	}
	if !hasBackground {
		t.Fatalf("异步执行应有后台结果消息")
	}
}

func TestBotIssueUpdatePreservesBody(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botbody")
	bot := env.createBot(t, userID)

	project, err := env.bots.projects.Create(userID, &CreateProjectRequest{Name: "正文保留项目"})
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	original := "原始正文，只改标题时不得被清空"
	issue, err := env.bots.issues.CreateInternalIssue(project.Id, userID,
		CreateInternalIssueRequest{Title: "原标题", Body: &original})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// 只改标题：参数不带 body，不得把 body 覆写成空串。
	newTitle := "新标题"
	call := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_issue", Action: "update",
		ArgumentsJSON: fmt.Sprintf(`{"action":"update","issue_id":%q,"title":%q}`, issue.Id, newTitle),
		ExecutionMode: string(model.BotExecutionAsync), Status: model.BotCallStatusRunning,
	}
	if _, err := env.bots.runIssueWrite(context.Background(), call); err != nil {
		t.Fatalf("runIssueWrite(仅标题): %v", err)
	}
	fresh, err := env.bots.issues.Get(issue.Id, userID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fresh.Title != newTitle {
		t.Fatalf("标题应更新: %+v", fresh.Title)
	}
	if fresh.Body != original {
		t.Fatalf("字段级更新不得清空正文: %q", fresh.Body)
	}

	// 显式带 body：允许替换。
	newBody := "替换后的正文"
	call2 := &model.BotToolCall{
		ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_issue", Action: "update",
		ArgumentsJSON: fmt.Sprintf(`{"action":"update","issue_id":%q,"body":%q}`, issue.Id, newBody),
		ExecutionMode: string(model.BotExecutionAsync), Status: model.BotCallStatusRunning,
	}
	if _, err := env.bots.runIssueWrite(context.Background(), call2); err != nil {
		t.Fatalf("runIssueWrite(带正文): %v", err)
	}
	fresh2, err := env.bots.issues.Get(issue.Id, userID)
	if err != nil {
		t.Fatalf("get2: %v", err)
	}
	if fresh2.Body != newBody {
		t.Fatalf("显式 body 应替换: %q", fresh2.Body)
	}
}

func TestBotDeltaBufferRecurrentFlush(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botdelta")
	bot := env.createBot(t, userID)

	// 事件带 run 外键：先落一个真实 run 夹具。
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: "run-delta", BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "delta-buffer",
		TriggerContextJSON: "{}", Status: model.BotRunStatusCompleted,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	d := env.bots.newDeltaBuffer(bot.ID, "run-delta", "msg-delta")
	d.onDelta("第一段")
	time.Sleep(150 * time.Millisecond) // 定时器先落一批
	d.onDelta("第二段")
	d.flush() // 流末尾再落一批
	d.flush() // 空缓冲：不重复落事件

	var bodies []string
	if err := env.db.Model(&model.BotEvent{}).
		Where("bot_id = ?", bot.ID).
		Order("seq asc").
		Pluck("payload_json", &bodies).Error; err != nil {
		t.Fatalf("events: %v", err)
	}
	var text string
	count := 0
	for _, b := range bodies {
		var env0 botEventEnvelope
		if err := json.Unmarshal([]byte(b), &env0); err != nil || env0.Kind != "text_delta" {
			continue
		}
		count++
		// 增量信封必须带预分配的 assistant 消息 ID，客户端才能挂草稿。
		if env0.MessageID != "msg-delta" {
			t.Fatalf("text_delta 应携带 message_id: %+v", env0)
		}
		if m, ok := env0.Payload.(map[string]interface{}); ok {
			if s, ok := m["text"].(string); ok {
				text += s
			}
		}
	}
	if count != 2 {
		t.Fatalf("应分两批落事件，实际 %d: %v", count, bodies)
	}
	if text != "第一段第二段" {
		t.Fatalf("SSE 增量应覆盖完整文本: %q", text)
	}
}

func TestBotEnsureRunRecoveryTrigger(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botrecover")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}

	// 无模型输入时普通唤醒不建 run。
	run, found, err := env.bots.ensureRun(botRow, botWake{Trigger: model.BotTriggerUserMessage, Key: "plain"})
	if err != nil {
		t.Fatalf("ensureRun(user): %v", err)
	}
	if found {
		t.Fatalf("无输入不应建 run: %+v", run)
	}

	// recovery 唤醒例外：恢复通知是 lifecycle 消息（不在模型输入类里），
	// 但必须建 run，否则重启后的恢复通知永远到不了模型。
	run, found, err = env.bots.ensureRun(botRow, botWake{Trigger: model.BotTriggerRecovery, Key: "recovery:t1"})
	if err != nil {
		t.Fatalf("ensureRun(recovery): %v", err)
	}
	if !found || run == nil || run.Status != model.BotRunStatusRunning {
		t.Fatalf("recovery 唤醒应建活动 run: found=%v run=%+v", found, run)
	}

	// 活动_run 仍在时，重复 recovery 唤醒复用同一 run（唯一索引兜底）。
	run2, found2, err := env.bots.ensureRun(botRow, botWake{Trigger: model.BotTriggerRecovery, Key: "recovery:t2"})
	if err != nil || !found2 || run2.ID != run.ID {
		t.Fatalf("重复 recovery 唤醒应复用活动 run: %v %v %+v", err, found2, run2)
	}
}

func TestBotContextToolRepliesBeforeDeferredUser(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botorder")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}

	// 账本：assistant 双工具调用 → tool(A) → user（延迟输入）→ tool(B)。
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: "run-order", BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "order",
		TriggerContextJSON: "{}", Status: model.BotRunStatusCompleted,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now, FinishedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	msgs := repository.NewBotMessageRepository(env.db)
	appendMsg := func(kind model.BotMessageKind, content interface{}) {
		t.Helper()
		raw, err := json.Marshal(content)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		seq, err := env.bots.bots.NextMessageSeqTx(env.db, bot.ID)
		if err != nil {
			t.Fatalf("seq: %v", err)
		}
		msg := &model.BotMessage{ID: uuid.NewString(), BotID: bot.ID, Seq: seq,
			Kind: kind, ContentJSON: string(raw), CreatedAt: time.Now().UTC()}
		if err := msgs.AppendTx(env.db, msg); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	appendMsg(model.BotMessageKindUser, botUserContent{Text: "帮我查两个问题"})
	appendMsg(model.BotMessageKindAssistant, botAssistantContent{ToolCalls: []botAssistantToolCall{
		{ProviderToolCallID: "callA", ToolName: "fs_issue", Action: "list", Arguments: "{}"},
		{ProviderToolCallID: "callB", ToolName: "fs_issue", Action: "list", Arguments: "{}"},
	}})
	appendMsg(model.BotMessageKindToolReply, botToolReplyContent{ProviderToolCallID: "callA", Content: "A 结果"})
	appendMsg(model.BotMessageKindUser, botUserContent{Text: "追加的话"})
	appendMsg(model.BotMessageKindToolReply, botToolReplyContent{ProviderToolCallID: "callB", Content: "B 结果"})

	built, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	raw, err := json.Marshal(built.Request.Messages)
	if err != nil {
		t.Fatalf("marshal messages: %v", err)
	}
	var roles []struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &roles); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// 期望协议顺序：system + 首条 user + assistant + tool(A)、tool(B) 连续
	// 成块 + 延迟 user + 末尾运行时上下文 user（buildRunContext 恒定追加）。
	want := []string{"system", "user", "assistant", "tool", "tool", "user", "user"}
	if len(roles) != len(want) {
		t.Fatalf("消息数不符: %v", roles)
	}
	for i, r := range roles {
		if r.Role != want[i] {
			t.Fatalf("第 %d 条角色应为 %s，实际 %s（tool 响应必须连续）: %v", i, want[i], r.Role, roles)
		}
	}
}

func TestBotChatSnapshotConsistent(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botsnap")
	bot := env.createBot(t, userID)

	// 直接经 SendMessage 落一条 user 消息与事件，快照应同时含两者。
	if _, err := env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "快照", ClientMessageID: "snap-1"}); err != nil {
		t.Fatalf("send: %v", err)
	}

	snap, err := env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.Bot.ID != bot.ID {
		t.Fatalf("bot 不符: %+v", snap.Bot)
	}
	if len(snap.Messages) != 1 {
		t.Fatalf("消息数不符: %+v", snap.Messages)
	}
	var uc botUserContent
	if err := json.Unmarshal(snap.Messages[0].Content, &uc); err != nil || uc.Text != "快照" {
		t.Fatalf("消息不符: %+v", snap.Messages[0])
	}
	// 水位必须不小于快照内任何事件重放所需的下界：游标从 next_event_seq-1
	// 续读不能重放快照已含内容。
	if snap.NextEventSeq < 1 {
		t.Fatalf("next_event_seq 应为正: %d", snap.NextEventSeq)
	}

	// 一次事件（user_message 的 SSE 投影）seq < NextEventSeq：事件先落库、
	// 水位在其后，订阅重放不会漏也不会重复这条消息。
	var eventSeqs []int64
	if err := env.db.Model(&model.BotEvent{}).Where("bot_id = ?", bot.ID).
		Order("seq asc").Pluck("seq", &eventSeqs).Error; err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(eventSeqs) == 0 || eventSeqs[len(eventSeqs)-1] >= snap.NextEventSeq {
		t.Fatalf("事件应全部落在水位之前: events=%v watermark=%d", eventSeqs, snap.NextEventSeq)
	}
}

func TestBotRecoveryQueuedSyncCall(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botqsync")
	bot := env.createBot(t, userID)

	// 夹具：提交后、执行前崩溃留下的 queued 同步调用。
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionSync))
	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recovery: %v", err)
	}

	fresh, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if fresh.Status != model.BotCallStatusNotExecuted || fresh.FinishedAt == nil {
		t.Fatalf("queued 同步调用应恢复为 not_executed: %+v", fresh.Status)
	}

	// 协议回复必须补齐（每个 provider_tool_call_id 恰好一条 tool 消息）。
	var replyCount int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?",
		bot.ID, model.BotMessageKindToolReply).Count(&replyCount)
	if replyCount != 1 {
		t.Fatalf("应恰好补一条 tool 回复: %d", replyCount)
	}

	// tool_result 终态事件随事务落库，其他标签页不重读也能翻卡。
	var eventCount int64
	env.db.Model(&model.BotEvent{}).Where("bot_id = ? AND event_type = ?",
		bot.ID, model.BotEventToolResult).Count(&eventCount)
	if eventCount != 1 {
		t.Fatalf("应补一条 tool_result 事件: %d", eventCount)
	}

	// 幂等：重复恢复不再补第二条回复。
	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recovery2: %v", err)
	}
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?",
		bot.ID, model.BotMessageKindToolReply).Count(&replyCount)
	if replyCount != 1 {
		t.Fatalf("重复恢复不得补第二条回复: %d", replyCount)
	}
}

func TestBotSyncFinalizeEmitsToolResult(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botsync")
	bot := env.createBot(t, userID)

	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusRunning, string(model.BotExecutionSync))
	call, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	runID := env.bots.runIDOfCall(call)
	botModel, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}
	env.bots.finalizeCall(botModel, runID, call, model.BotCallStatusSucceeded,
		botToolOutcome{ResultJSON: "{}", PreviewJSON: "{}", ResultChars: 2}, nil, false)

	// 同步调用终态：tool_result 事件与回复同事务落库，卡片不靠重读翻终态。
	// （BotEvent 无 call_id 列，调用标识在事件信封里。）
	var events []model.BotEvent
	env.db.Where("bot_id = ? AND event_type = ?",
		bot.ID, model.BotEventToolResult).Find(&events)
	if len(events) != 1 {
		t.Fatalf("同步终态应补一条 tool_result 事件: %d", len(events))
	}
	var env0 botEventEnvelope
	if err := json.Unmarshal([]byte(events[0].PayloadJSON), &env0); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if env0.CallID != callID {
		t.Fatalf("事件信封应带 call_id: %+v", env0)
	}
	if m, ok := env0.Payload.(map[string]interface{}); ok {
		if m["id"] != callID || m["status"] != string(model.BotCallStatusSucceeded) {
			t.Fatalf("tool_result 应带完整调用投影: %v", m)
		}
	} else {
		t.Fatalf("载荷应为对象: %v", env0.Payload)
	}
}

func TestBotContextRejectsUnclosedToolBlock(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botunclosed")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}

	// 恢复缺口兜底：账本停在 assistant tool_calls、没有回复 → 拒绝发请求。
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: "run-unclosed", BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "unclosed",
		TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	raw, _ := json.Marshal(botAssistantContent{ToolCalls: []botAssistantToolCall{
		{ProviderToolCallID: "callX", ToolName: "fs_issue", Action: "list", Arguments: "{}"},
	}})
	msg := &model.BotMessage{ID: uuid.NewString(), BotID: bot.ID,
		Seq: 1, Kind: model.BotMessageKindAssistant, ContentJSON: string(raw), CreatedAt: now}
	if err := repository.NewBotMessageRepository(env.db).AppendTx(env.db, msg); err != nil {
		t.Fatalf("append: %v", err)
	}

	if _, err := env.bots.buildRunContext(botRow, run); err == nil {
		t.Fatalf("未闭合 tool 块应拒绝构建上下文")
	}
}

func TestBotRevokeEventCarriesRestoredCall(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botrev2")
	bot := env.createBot(t, userID)

	now := time.Now().UTC()
	authz := &model.BotAuthorization{
		ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_project", Action: "create",
		ScopeKind: "project_collection", ScopeID: userID,
		ConstraintsJSON: "{}", RuleHash: "hash-rev2", CreatedAt: now,
	}
	if err := env.db.Create(authz).Error; err != nil {
		t.Fatalf("seed authz: %v", err)
	}
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionAsync))
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).
		Update("authorization_id", authz.ID).Error; err != nil {
		t.Fatalf("bind authz: %v", err)
	}

	if err := env.bots.RevokeAuthorization(userID, bot.ID, authz.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// 撤销事件必须带恢复后的完整调用投影：订阅端合并即恢复审批控件，
	// 卡片不能停在 queued 等重读。
	var events []model.BotEvent
	env.db.Where("bot_id = ? AND event_type = ?", bot.ID, model.BotEventToolCall).
		Order("seq asc").Find(&events)
	var restored *botEventEnvelope
	for i := range events {
		var env0 botEventEnvelope
		if err := json.Unmarshal([]byte(events[i].PayloadJSON), &env0); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if env0.CallID == callID {
			restored = &env0
		}
	}
	if restored == nil {
		t.Fatalf("撤销应发 tool_call 事件")
	}
	m, ok := restored.Payload.(map[string]interface{})
	if !ok || m["id"] != callID || m["status"] != string(model.BotCallStatusPendingConfirmation) {
		t.Fatalf("撤销事件应带 pending_confirmation 投影: %v", restored.Payload)
	}
}

func TestBotClaimRechecksAuthorization(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botclaim")
	bot := env.createBot(t, userID)

	// 竞速窗口：认领前授权已被撤销但快速路径检查刚通过（撤销提交在
	// 预检与认领之间）。认领事务内的授权复核必须挡住它。
	now := time.Now().UTC()
	authz := &model.BotAuthorization{
		ID: uuid.NewString(), BotID: bot.ID,
		ToolName: "fs_project", Action: "create",
		ScopeKind: "project_collection", ScopeID: userID,
		ConstraintsJSON: "{}", RuleHash: "hash-claim", CreatedAt: now,
	}
	if err := env.db.Create(authz).Error; err != nil {
		t.Fatalf("seed authz: %v", err)
	}
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionAsync))
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).
		Update("authorization_id", authz.ID).Error; err != nil {
		t.Fatalf("bind authz: %v", err)
	}

	// 只撤销授权行，不走过 revertQueuedCall 的退回路径——复现「撤销已提交、
	// 退回更新输给认领」的时序。
	if err := env.db.Model(&model.BotAuthorization{}).Where("id = ?", authz.ID).
		Update("revoked_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("revoke raw: %v", err)
	}

	env.bots.executeQueuedCall(callID)

	fresh, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if fresh.Status != model.BotCallStatusPendingConfirmation {
		t.Fatalf("已撤销授权不得认领执行，应退回待确认: %+v", fresh.Status)
	}
	if fresh.FinishedAt != nil {
		t.Fatalf("调用不得被执行: %+v", fresh.FinishedAt)
	}
}

func TestBotCloseUnstartedSyncAtomic(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botunstarted")
	bot := env.createBot(t, userID)

	// 新输入/停止触发的收尾：终态、协议回复与事件同一事务——崩溃窗口不存在。
	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionSync))
	call, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	env.bots.closeUnstartedSyncCalls([]*model.BotToolCall{call}, "stop_requested")

	fresh, err := env.bots.calls.FindByID(callID)
	if err != nil || fresh.Status != model.BotCallStatusNotExecuted || fresh.FinishedAt == nil {
		t.Fatalf("应落 not_executed 终态: %v %+v", err, fresh)
	}
	var replyCount, eventCount int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?",
		bot.ID, model.BotMessageKindToolReply).Count(&replyCount)
	env.db.Model(&model.BotEvent{}).Where("bot_id = ? AND event_type = ?",
		bot.ID, model.BotEventToolResult).Count(&eventCount)
	if replyCount != 1 || eventCount != 1 {
		t.Fatalf("回复与事件应同事务落库: replies=%d events=%d", replyCount, eventCount)
	}

	// 重复收尾幂等：终态行不再变化。
	env.bots.closeUnstartedSyncCalls([]*model.BotToolCall{call}, "stop_requested")
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?",
		bot.ID, model.BotMessageKindToolReply).Count(&replyCount)
	if replyCount != 1 {
		t.Fatalf("重复收尾不得补第二条回复: %d", replyCount)
	}
}

func TestBotCancelQueuedCallAtomic(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botcancel2")
	bot := env.createBot(t, userID)

	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionAsync))
	call, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("call: %v", err)
	}

	// 原始 run 已结束后取消：终态、结果消息、事件同事务；并立即唤醒主线。
	fresh, cancelled := env.bots.cancelBotCall(call)
	if !cancelled || fresh.Status != model.BotCallStatusCancelled {
		t.Fatalf("cancel: %+v %+v", cancelled, fresh)
	}

	var resultMsgs int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?",
		bot.ID, model.BotMessageKindBackgroundResult).Count(&resultMsgs)
	if resultMsgs != 1 {
		t.Fatalf("取消结果消息应同事务落库: %d", resultMsgs)
	}

	// 幂等：重复取消不补第二条结果消息。
	again, _ := env.bots.cancelBotCall(fresh)
	if again.Status != model.BotCallStatusCancelled {
		t.Fatalf("repeat cancel: %+v", again)
	}
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?",
		bot.ID, model.BotMessageKindBackgroundResult).Count(&resultMsgs)
	if resultMsgs != 1 {
		t.Fatalf("重复取消不得补第二条结果消息: %d", resultMsgs)
	}
}

func TestBotStartWorkerPoolOnce(t *testing.T) {
	env := setupBotTestEnv(t)
	env.bots.ensureWorkers()
	firstCh := env.bots.workCh
	env.bots.Start()
	if env.bots.workCh != firstCh {
		t.Fatalf("Start 不得重建 worker 池（会搁浅原 worker 与取消句柄）")
	}
}

func TestBotRejectWakesRuntime(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botreject2")
	bot := env.createBot(t, userID)

	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusPendingConfirmation, string(model.BotExecutionAsync))
	if _, err := env.bots.DecideCall(userID, bot.ID, callID, &CallDecisionRequest{Decision: string(model.BotDecisionReject)}); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// reject 是落库的模型输入：主线应被立即唤醒并消费，run 不能一直挂着。
	waitForBot(t, "reject run 结束", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})
}

func TestBotCancelAfterSuccessfulWrite(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botcancelwin")
	bot := env.createBot(t, userID)

	_, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusRunning, string(model.BotExecutionAsync))
	call, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	runID := env.bots.runIDOfCall(call)
	botModel, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}

	// 写入成功返回但取消已受理：终态 cancelled，副作用标记可能发生。
	if err := env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).
		Update("cancel_requested_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("mark cancel: %v", err)
	}
	env.bots.finalizeCall(botModel, runID, call, model.BotCallStatusSucceeded,
		botToolOutcome{ResultJSON: "{}", PreviewJSON: "{}", ResultChars: 2}, nil, false)

	fresh, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if fresh.Status != model.BotCallStatusCancelled {
		t.Fatalf("取消已受理时成功写入也应终态 cancelled: %+v", fresh.Status)
	}
	if !fresh.EffectMayHaveOccurred {
		t.Fatalf("成功写入 + 取消应标记副作用可能发生: %+v", fresh)
	}
}

func TestBotPanicFinishesRun(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botpanic")
	bot := env.createBot(t, userID)

	// 落一条 running run，模拟 runBot panic 后的恢复路径。
	now := time.Now().UTC()
	run := &model.BotRun{
		ID: "run-panic", BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "panic",
		TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	// 直接验证 panic 恢复路径：落库终态 + 槽释放。
	env.bots.finishRunAfterPanic(bot.ID, "boom")

	fresh, err := env.bots.runs.FindByID("run-panic")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if fresh.Status != model.BotRunStatusFailed || fresh.FinishedAt == nil {
		t.Fatalf("panic 后落库 run 必须置 failed: %+v", fresh.Status)
	}
	// 活动槽已被恢复路径释放：后续唤醒可正常进槽。
	if env.bots.rt.acquire(bot.ID, &botWake{Trigger: model.BotTriggerUserMessage, Key: "after"}) == nil {
		t.Fatalf("panic 后槽应已释放")
	}
}

func TestBotDiscardDraftOnModelFailure(t *testing.T) {
	// 上游发完部分正文就断连：run 失败，且必须发 message_discard 让客户端
	// 丢弃挂出的草稿——否则残缺回复被当成完整回答一直挂着。
	env := setupBotTestEnv(t, fakeModelTurn{Text: "写到一半", BreakAfterText: true})
	userID := createBotTestUser(t, env.db, "botdiscard")
	bot := env.createBot(t, userID)
	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "d1"})
	waitForBot(t, "run failed", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})

	var runs []model.BotRun
	env.db.Where("bot_id = ?", bot.ID).Find(&runs)
	if len(runs) != 1 || runs[0].Status != model.BotRunStatusFailed {
		t.Fatalf("run 应失败: %+v", runs)
	}

	// 部分正文曾挂出（text_delta），失败后必须紧跟 message_discard。
	var events []model.BotEvent
	env.db.Where("bot_id = ? AND event_type = ?", bot.ID, model.BotEventDelta).
		Order("seq asc").Find(&events)
	kinds := make([]string, 0, len(events))
	for i := range events {
		var env0 botEventEnvelope
		if err := json.Unmarshal([]byte(events[i].PayloadJSON), &env0); err == nil {
			kinds = append(kinds, env0.Kind)
		}
	}
	hasDelta, hasDiscard := false, false
	for _, k := range kinds {
		if k == "text_delta" {
			hasDelta = true
		}
		if k == "message_discard" {
			hasDiscard = true
		}
	}
	if !hasDelta || !hasDiscard {
		t.Fatalf("失败流应先 delta 后 discard: %v", kinds)
	}
	// 账本不得提交残缺的 assistant 消息。
	var msgCount int64
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?",
		bot.ID, model.BotMessageKindAssistant).Count(&msgCount)
	if msgCount != 0 {
		t.Fatalf("失败流不得提交 assistant 消息: %d", msgCount)
	}
}

func TestBotSnapshotIncludesOldUnfinishedCalls(t *testing.T) {
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botsnapcall")
	bot := env.createBot(t, userID)

	// 100 条终态调用挤占页面，第 101 条 pending_confirmation 是老账本里的
	// 待确认卡片：快照必须收录它，否则聊天页无法批准。
	for i := 0; i < 100; i++ {
		seedBotCallFixture(t, env, bot.ID, model.BotCallStatusSucceeded, string(model.BotExecutionAsync))
	}
	_, pendingID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusPendingConfirmation, string(model.BotExecutionAsync))

	snap, err := env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	found := false
	for _, c := range snap.Calls {
		if c.ID == pendingID {
			found = true
		}
	}
	if !found {
		t.Fatalf("快照必须收录未终态调用（待确认卡片可操作）: %d", len(snap.Calls))
	}
}

func TestBotContextWindowSlides(t *testing.T) {
	// 账本超过抓取上限后窗口必须滑动：新输入永远能进上下文，run 能正常
	// 收尾（否则同页空转重建、模型被反复调用）。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botwindow")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}

	now := time.Now().UTC()
	run := &model.BotRun{
		ID: "run-window", BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "window",
		TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	msgs := repository.NewBotMessageRepository(env.db)
	for seq := 1; seq <= botHistoryFetchLimit+2; seq++ {
		msg := &model.BotMessage{ID: uuid.NewString(), BotID: bot.ID, Seq: int64(seq),
			Kind:        model.BotMessageKindUser,
			ContentJSON: `{"text":"旧消息"}`, CreatedAt: now}
		if seq == botHistoryFetchLimit+2 {
			msg.ContentJSON = `{"text":"最新输入"}`
		}
		if err := msgs.AppendTx(env.db, msg); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	built, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 消费水位必须到达账本末尾：最新的输入已进入本轮上下文。
	if built.InputThroughSeq < int64(botHistoryFetchLimit+2) {
		t.Fatalf("窗口滑动后消费水位应达账本末尾: %d", built.InputThroughSeq)
	}
	raw, err := json.Marshal(built.Request.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), "最新输入") {
		t.Fatalf("最新输入必须进入本轮上下文")
	}
}

func TestBotRecoveryQueuedSyncPaged(t *testing.T) {
	// 全局 queued 超过一页时，恢复必须取全所有排队同步调用（分页直到取尽）。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botpaged")
	bot := env.createBot(t, userID)

	const total = 260
	callIDs := make([]string, 0, total)
	for i := 0; i < total; i++ {
		_, id := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusQueued, string(model.BotExecutionSync))
		callIDs = append(callIDs, id)
	}
	if err := env.bots.RecoverOnStartup(); err != nil {
		t.Fatalf("recovery: %v", err)
	}
	var remaining int64
	env.db.Model(&model.BotToolCall{}).
		Where("id IN ? AND status = ?", callIDs, model.BotCallStatusQueued).
		Count(&remaining)
	if remaining != 0 {
		t.Fatalf("全部排队同步调用都应被恢复补终态，剩余 %d", remaining)
	}
}

func TestBotSnapshotUnfinishedBeyondPage(t *testing.T) {
	// 未终态调用超过快照其余部分时必须分页取全，不允许 100 条硬截断。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botsnapall")
	bot := env.createBot(t, userID)

	const total = 130
	pendingIDs := make([]string, 0, total)
	for i := 0; i < total; i++ {
		_, id := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusPendingConfirmation, string(model.BotExecutionAsync))
		pendingIDs = append(pendingIDs, id)
	}
	snap, err := env.bots.GetChatSnapshot(userID, bot.ID, 50)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap.Calls) < total {
		t.Fatalf("快照应收录全部未终态调用: %d < %d", len(snap.Calls), total)
	}
	seen := map[string]bool{}
	for _, c := range snap.Calls {
		seen[c.ID] = true
	}
	for _, id := range pendingIDs {
		if !seen[id] {
			t.Fatalf("待确认调用缺失: %s", id)
		}
	}
}

func TestBotContextWindowAlignedToBlock(t *testing.T) {
	// 滑窗起点落在 tool 回复块中间时，开头的孤儿 tool 回复必须被丢弃：
	// 裸 tool 角色消息会被 OpenAI 兼容端拒绝。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botalign")
	bot := env.createBot(t, userID)
	botRow, err := env.bots.bots.Get(bot.ID)
	if err != nil {
		t.Fatalf("bot: %v", err)
	}

	now := time.Now().UTC()
	run := &model.BotRun{
		ID: "run-align", BotID: bot.ID,
		TriggerSource: model.BotTriggerUserMessage, TriggerKey: "align",
		TriggerContextJSON: "{}", Status: model.BotRunStatusRunning,
		ConfigSnapshotJSON: "{}", CreatedAt: now, StartedAt: &now,
	}
	if err := env.bots.runs.CreateTx(env.db, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	msgs := repository.NewBotMessageRepository(env.db)
	appendMsg := func(kind model.BotMessageKind, raw string) {
		t.Helper()
		seq, err := env.bots.bots.NextMessageSeqTx(env.db, bot.ID)
		if err != nil {
			t.Fatalf("seq: %v", err)
		}
		msg := &model.BotMessage{ID: uuid.NewString(), BotID: bot.ID, Seq: seq,
			Kind: kind, ContentJSON: raw, CreatedAt: now}
		if err := msgs.AppendTx(env.db, msg); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// 窗口起点（maxSeq-limit）落在 assistant 行上，tool 回复成为窗口第一行
	//——assistant 在窗外，回复是孤儿。
	appendMsg(model.BotMessageKindUser, `{"text":"旧消息1"}`)
	appendMsg(model.BotMessageKindUser, `{"text":"旧消息2"}`)
	appendMsg(model.BotMessageKindAssistant,
		`{"tool_calls":[{"call_id":"c1","provider_tool_call_id":"callA","tool_name":"fs_issue","action":"list","arguments":"{}"}]}`)
	appendMsg(model.BotMessageKindToolReply,
		`{"provider_tool_call_id":"callA","content":"结果","status":"succeeded"}`)
	for seq := 5; seq <= botHistoryFetchLimit+3; seq++ {
		appendMsg(model.BotMessageKindUser, `{"text":"旧消息"}`)
	}
	appendMsg(model.BotMessageKindUser, `{"text":"触发新构建"}`)

	built, err := env.bots.buildRunContext(botRow, run)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	raw, err := json.Marshal(built.Request.Messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// 裸 tool 回复（assistant 在窗外）不得出现：开头的孤儿回复被丢弃。
	for _, frag := range []string{`"role":"tool"`} {
		if strings.Contains(string(raw), frag) {
			t.Fatalf("窗外 assistant 的孤儿 tool 回复不得进入协议消息")
		}
	}
	if !strings.Contains(string(raw), "触发新构建") {
		t.Fatalf("新输入必须仍在协议消息里")
	}
}

func TestBotInvalidCallPreservesRawArguments(t *testing.T) {
	// 模型发来坏参数（非法 JSON）的调用：校验失败的即时回复照发，但
	// assistant 账本必须保留原始参数——下一轮回放时空参数会被校验历史
	// 参数的供应商拒绝，用户消息将永远到不了模型。
	env := setupBotTestEnv(t,
		fakeModelTurn{ToolCalls: []fakeModelToolCall{{ID: "call-bad", Name: "fs_issue", Args: `{bad json`}}},
		fakeModelTurn{Text: "这次参数是对的"},
	)
	userID := createBotTestUser(t, env.db, "botbadargs")
	bot := env.createBot(t, userID)
	env.bots.SendMessage(userID, bot.ID, &SendMessageRequest{Text: "hi", ClientMessageID: "b1"})
	waitForBot(t, "第一轮结束", 15*time.Second, func() bool {
		return env.botSettled(bot.ID)
	})

	var ledgerArgs string
	env.db.Model(&model.BotMessage{}).Where("bot_id = ? AND kind = ?",
		bot.ID, model.BotMessageKindAssistant).Limit(1).Pluck("content_json", &ledgerArgs)
	if !strings.Contains(ledgerArgs, "{bad json") {
		t.Fatalf("assistant 账本必须保留原始坏参数: %s", ledgerArgs)
	}
}

func TestBotFinalizeRechecksCancelInTx(t *testing.T) {
	// 取消标记在终态事务前一刻才写入也必须被认出：accepted 取消不能以
	// effect=false 落 succeeded。
	env := setupBotTestEnv(t)
	userID := createBotTestUser(t, env.db, "botfinrec")
	bot := env.createBot(t, userID)
	runID, callID := seedBotCallFixture(t, env, bot.ID, model.BotCallStatusRunning, string(model.BotExecutionSync))

	// 模拟竞速窗口：finalize 读取前先在库里记上取消标记。
	env.db.Model(&model.BotToolCall{}).Where("id = ?", callID).
		Update("cancel_requested_at", time.Now().UTC())
	call, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	env.bots.finalizeCall(&model.Bot{ID: bot.ID}, runID, call,
		model.BotCallStatusSucceeded, botToolOutcome{ResultJSON: "{}"}, nil, false)

	fresh, err := env.bots.calls.FindByID(callID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fresh.Status != model.BotCallStatusCancelled {
		t.Fatalf("受理过的取消必须落 cancelled，实际 %s", fresh.Status)
	}
	if !fresh.EffectMayHaveOccurred {
		t.Fatalf("成功返回也可能已生效，副作用标记必须为 true")
	}
}

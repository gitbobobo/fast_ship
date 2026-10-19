package router

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// 机器人端到端（INT-101）：httptest.Server 扮演 Chat Completions，channel
// 精确控制模型返回 / 工具完成 / 新消息 / 取消的时点，不靠 sleep 构造竞态。

type e2eTurn struct {
	Text      string           `json:"-"`
	ToolName  string           `json:"-"`
	ToolArgs  string           `json:"-"`
	ToolID    string           `json:"-"`
	Usage     map[string]int64 `json:"-"`
	Hold      chan struct{}    `json:"-"`
	AfterHold string           `json:"-"`
}

type e2eFakeModel struct {
	srv *httptest.Server
	mu  sync.Mutex
	// schedule 由测试注入：每个请求按序取一个 turn；耗尽后默认纯文本。
	schedule []e2eTurn
	requests chan e2eCapturedRequest
	shutdown chan struct{}
}

type e2eCapturedRequest struct {
	Body []byte
}

func newE2EFakeModel(t *testing.T) *e2eFakeModel {
	t.Helper()
	f := &e2eFakeModel{requests: make(chan e2eCapturedRequest, 64), shutdown: make(chan struct{})}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 模型窗口探测（INT-115）不进请求通道，也不消耗 schedule。
		if r.URL.Path == "/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, _ := io.ReadAll(r.Body)
		select {
		case f.requests <- e2eCapturedRequest{Body: body}:
		default:
		}
		f.mu.Lock()
		var turn e2eTurn
		if len(f.schedule) > 0 {
			turn = f.schedule[0]
			f.schedule = f.schedule[1:]
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
		write := func(payload string) {
			fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
		chunkOf := func(delta string, finish string) string {
			raw, _ := json.Marshal(map[string]interface{}{
				"id": "cmpl-e2e", "object": "chat.completion.chunk", "created": 1700000000,
				"model":   "e2e-model",
				"choices": []map[string]interface{}{{"index": 0, "delta": map[string]interface{}{"content": delta}, "finish_reason": finish}},
			})
			return string(raw)
		}
		text := turn.Text
		if turn.AfterHold != "" {
			text = turn.AfterHold
		}
		if text != "" {
			write(chunkOf(text, ""))
		}
		if turn.ToolID != "" {
			raw, _ := json.Marshal(map[string]interface{}{
				"id": "cmpl-e2e", "object": "chat.completion.chunk", "created": 1700000000,
				"model": "e2e-model",
				"choices": []map[string]interface{}{{"index": 0, "delta": map[string]interface{}{
					"tool_calls": []map[string]interface{}{{
						"index": 0, "id": turn.ToolID, "type": "function",
						"function": map[string]interface{}{"name": turn.ToolName, "arguments": turn.ToolArgs},
					}},
				}, "finish_reason": ""}},
			})
			write(string(raw))
			write(chunkOf("", "tool_calls"))
		} else {
			write(chunkOf("", "stop"))
		}
		if turn.Usage != nil {
			raw, _ := json.Marshal(map[string]interface{}{
				"id": "cmpl-e2e", "object": "chat.completion.chunk", "created": 1700000000,
				"model": "e2e-model", "choices": []map[string]interface{}{},
				"usage": turn.Usage,
			})
			write(string(raw))
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(f.srv.Close)
	t.Cleanup(func() { close(f.shutdown) })
	return f
}

func (f *e2eFakeModel) URL() string { return f.srv.URL }

func (f *e2eFakeModel) setSchedule(turns ...e2eTurn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.schedule = turns
}

func (f *e2eFakeModel) nextRequest(t *testing.T, timeout time.Duration) []byte {
	t.Helper()
	select {
	case req := <-f.requests:
		return req.Body
	case <-time.After(timeout):
		t.Fatalf("等待模型请求超时")
		return nil
	}
}

// e2eEnv 提供机器人 E2E 的路由环境、JWT 与假模型。
type e2eEnv struct {
	env    *routerTestEnv
	fake   *e2eFakeModel
	auth   routerAuthResult
	jwt    string
	userID string
}

func setupE2E(t *testing.T) *e2eEnv {
	t.Helper()
	env := setupRouterTestEnv(t)
	fake := newE2EFakeModel(t)
	auth := registerAndLoginRouterUser(t, env.router, "bote2e", "bote2e@example.com", "Password123")
	return &e2eEnv{env: env, fake: fake, auth: auth, jwt: "Bearer " + auth.Token, userID: auth.UserID}
}

func (e *e2eEnv) doJSON(t *testing.T, method, path string, body interface{}) (int, map[string]interface{}) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", e.jwt)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.env.router.ServeHTTP(rec, req)
	var envelope map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &envelope)
	return rec.Code, envelope
}

func (e *e2eEnv) createBot(t *testing.T) string {
	t.Helper()
	code, body := e.doJSON(t, http.MethodPost, "/api/bots", map[string]interface{}{
		"name": "e2e-bot", "base_url": e.fake.URL(), "model": "e2e-model", "api_key": "sk-e2e-8888",
	})
	if code != http.StatusOK {
		t.Fatalf("create bot: %d %v", code, body)
	}
	data := body["data"].(map[string]interface{})
	return data["id"].(string)
}

func envelopeData(envelope map[string]interface{}) map[string]interface{} {
	data, _ := envelope["data"].(map[string]interface{})
	return data
}

func envelopeItems(envelope map[string]interface{}) []interface{} {
	data, _ := envelope["data"].(map[string]interface{})
	items, _ := data["items"].([]interface{})
	return items
}

func waitForE2E(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时", what)
}

// safeRecorder 是并发安全的 SSE 记录器。`httptest.ResponseRecorder` 的 Body
// 不是并发安全的：订阅端 goroutine 持续写入、测试端同时轮询读取，`-race` 下
// 会报数据竞争，也可能读到撕裂的帧导致等待超时。这里用互斥锁保护状态，
// 读走方法、写实现 http.ResponseWriter + http.Flusher（gin 的 SSE 依赖）。
type safeRecorder struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	code    int
	flushed bool
}

func newSafeRecorder() *safeRecorder {
	return &safeRecorder{header: make(http.Header), code: http.StatusOK}
}

func (r *safeRecorder) Header() http.Header { return r.header }

func (r *safeRecorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.code = code
}

func (r *safeRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.Write(b)
}

func (r *safeRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushed = true
}

func (r *safeRecorder) bodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

func (r *safeRecorder) statusCode() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.code
}

func (r *safeRecorder) flushSeen() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushed
}

// waitForE2EDump 带诊断的超时等待：失败时打印 run/消息概要。
func (e *e2eEnv) dumpState(t *testing.T, botID string) string {
	t.Helper()
	runs := e.botRuns(t, botID)
	runDesc := make([]string, 0, len(runs))
	for _, r := range runs {
		run := r.(map[string]interface{})
		runDesc = append(runDesc, fmt.Sprintf("%v/%v", run["trigger_key"], run["status"]))
	}
	msgs := e.botMessages(t, botID)
	msgDesc := make([]string, 0, len(msgs))
	for _, m := range msgs {
		msg := m.(map[string]interface{})
		content := fmt.Sprint(msg["content"])
		if len(content) > 300 {
			content = content[:300]
		}
		msgDesc = append(msgDesc, fmt.Sprintf("%v:%v:%s", msg["seq"], msg["kind"], content))
	}
	return "runs=[" + strings.Join(runDesc, ", ") + "] msgs=[" + strings.Join(msgDesc, " | ") + "]"
}

func (e *e2eEnv) botRuns(t *testing.T, botID string) []interface{} {
	t.Helper()
	code, body := e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/runs?limit=100", nil)
	if code != http.StatusOK {
		t.Fatalf("list runs: %d %v", code, body)
	}
	return envelopeItems(body)
}

func (e *e2eEnv) runFinished(t *testing.T, botID string) bool {
	runs := e.botRuns(t, botID)
	if len(runs) == 0 {
		return false
	}
	for _, r := range runs {
		run := r.(map[string]interface{})
		if run["finished_at"] == nil {
			return false
		}
	}
	return len(runs) > 0
}

func (e *e2eEnv) botMessages(t *testing.T, botID string) []interface{} {
	t.Helper()
	code, body := e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/messages?limit=200", nil)
	if code != http.StatusOK {
		t.Fatalf("list messages: %d %v", code, body)
	}
	return envelopeItems(body)
}

func (e *e2eEnv) botCalls(t *testing.T, botID, status string) []interface{} {
	t.Helper()
	path := "/api/bots/" + botID + "/calls?limit=200"
	if status != "" {
		path += "&status=" + status
	}
	code, body := e.doJSON(t, http.MethodGet, path, nil)
	if code != http.StatusOK {
		t.Fatalf("list calls: %d %v", code, body)
	}
	return envelopeItems(body)
}

// TestBotE2E 是设计第 7 节批次五要求的全链路场景聚合；每个子测试用独立
// 路由环境（独立内存库）与独立假模型。
func TestBotE2E(t *testing.T) {
	t.Run("SyncToolFullChain", func(t *testing.T) {
		e := setupE2E(t)
		e.fake.setSchedule(e2eTurn{ToolName: "fs_project", ToolID: "c1",
			ToolArgs: `{"action":"list","page":1,"page_size":10}`})
		botID := e.createBot(t)
		code, body := e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
			map[string]interface{}{"text": "列出项目", "client_message_id": "m1"})
		if code != http.StatusOK {
			t.Fatalf("send: %d %v", code, body)
		}
		waitForE2E(t, "run finished", 20*time.Second, func() bool { return e.runFinished(t, botID) })
		kinds := []string{}
		for _, m := range e.botMessages(t, botID) {
			kinds = append(kinds, m.(map[string]interface{})["kind"].(string))
		}
		// 用户 → 模型(工具调用) → 同步工具回复 → 最终答复。
		if len(kinds) != 4 || kinds[0] != "user" || kinds[1] != "assistant" || kinds[2] != "tool_reply" || kinds[3] != "assistant" {
			t.Fatalf("消息序列不符: %v", kinds)
		}
	})

	t.Run("WriteApprovalFlow", func(t *testing.T) {
		e := setupE2E(t)
		botID := e.createBot(t)
		// 建一个 Fast Ship 项目供 issue 落点。
		code, body := e.doJSON(t, http.MethodPost, "/api/projects", map[string]interface{}{"name": "e2e-" + uuid.NewString()[:8]})
		if code != http.StatusOK {
			t.Fatalf("create project: %d %v", code, body)
		}
		projectID := envelopeData(body)["id"].(string)

		// 写调用（确认卡片）。
		e.fake.setSchedule(e2eTurn{ToolName: "fs_issue", ToolID: "c2",
			ToolArgs: fmt.Sprintf(`{"action":"create","project_id":%q,"title":"机器人提的 issue","body":"正文"}`, projectID)})
		e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
			map[string]interface{}{"text": "提个 issue", "client_message_id": "w1"})

		var callID string
		deadlineCard := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadlineCard) {
			calls := e.botCalls(t, botID, "pending_confirmation")
			if len(calls) > 0 {
				callID = calls[0].(map[string]interface{})["id"].(string)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if callID == "" {
			t.Fatalf("待确认卡片未出现: %s", e.dumpState(t, botID))
		}

		// 回执先于结果。
		findKind := func(kind string, callIDDest *string) bool {
			for _, m := range e.botMessages(t, botID) {
				msg := m.(map[string]interface{})
				if msg["kind"] == kind {
					if callIDDest != nil {
						*callIDDest, _ = msg["call_id"].(string)
					}
					return true
				}
			}
			return false
		}
		if !findKind("tool_reply", nil) {
			t.Fatalf("回执缺失")
		}
		if findKind("background_result", nil) {
			t.Fatalf("批准前不应有后台终态")
		}

		// 卡片批准。
		code, body = e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/calls/"+callID+"/decision",
			map[string]interface{}{"decision": "approve"})
		if code != http.StatusOK {
			t.Fatalf("approve: %d %v", code, body)
		}
		// 业务写入发生。
		waitForE2E(t, "issue 创建", 30*time.Second, func() bool {
			code, body := e.doJSON(t, http.MethodGet, "/api/projects/"+projectID+"/issues?page=1&page_size=10", nil)
			if code != http.StatusOK {
				return false
			}
			items, _ := envelopeData(body)["items"].([]interface{})
			for _, it := range items {
				if it.(map[string]interface{})["title"] == "机器人提的 issue" {
					return true
				}
			}
			return false
		})
		// 后台结果唤醒模型继续。先等后台结果消息落地：worker 落后台结果与「第
		// 一轮结束」是两个时刻（唤醒可能重建 run，也可能落到仍在活动的那轮），
		// 只看 runFinished 会与这条消息竞速——机器负载高时旧写法会偶发失败。
		waitForE2E(t, "后台结果", 30*time.Second, func() bool { return findKind("background_result", nil) })
		waitForE2E(t, "run finished", 30*time.Second, func() bool { return e.runFinished(t, botID) })
		var bgCall string
		if !findKind("background_result", &bgCall) {
			t.Fatalf("后台结果消息缺失")
		}
		if bgCall != callID {
			t.Fatalf("后台结果未配对调用: %s vs %s", bgCall, callID)
		}
	})

	t.Run("RejectAlwaysAllowRevoke", func(t *testing.T) {
		e := setupE2E(t)
		botID := e.createBot(t)
		// 每个 pending 调用会让主线多一次「继续」请求（回执进上下文），
		// 脚本按请求序给足回合。
		e.fake.setSchedule(
			e2eTurn{ToolName: "fs_project", ToolID: "c3", ToolArgs: `{"action":"create","name":"拒掉的项目"}`},
			e2eTurn{Text: "已受理，等确认"},
			e2eTurn{ToolName: "fs_project", ToolID: "c4", ToolArgs: `{"action":"create","name":"授权的项目"}`},
			e2eTurn{Text: "已授权受理"},
			e2eTurn{ToolName: "fs_project", ToolID: "c5", ToolArgs: `{"action":"create","name":"撤销后的项目"}`},
			e2eTurn{Text: "已收到请求"},
		)
		e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
			map[string]interface{}{"text": "建项目", "client_message_id": "ra1"})
		var callID string
		waitForE2E(t, "待确认", 20*time.Second, func() bool {
			calls := e.botCalls(t, botID, "pending_confirmation")
			if len(calls) > 0 {
				callID = calls[0].(map[string]interface{})["id"].(string)
				return true
			}
			return false
		})
		// 拒绝。
		code, _ := e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/calls/"+callID+"/decision",
			map[string]interface{}{"decision": "reject"})
		if code != http.StatusOK {
			t.Fatalf("reject: %d", code)
		}
		waitForE2E(t, "拒绝终态", 20*time.Second, func() bool {
			calls := e.botCalls(t, botID, "rejected")
			return len(calls) == 1
		})
		// always_allow：第二条调用免确认。
		e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
			map[string]interface{}{"text": "再建", "client_message_id": "ra2"})
		waitForE2E(t, "always_allow 卡片", 20*time.Second, func() bool {
			return len(e.botCalls(t, botID, "pending_confirmation")) == 1
		})
		pending := e.botCalls(t, botID, "pending_confirmation")[0].(map[string]interface{})
		code, _ = e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/calls/"+pending["id"].(string)+"/decision",
			map[string]interface{}{"decision": "always_allow"})
		if code != http.StatusOK {
			t.Fatalf("always_allow: %d", code)
		}
		// 撤销后第三次同规则调用回到待确认。
		code, body := e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/authorizations?active=true", nil)
		if code != http.StatusOK {
			t.Fatalf("list authz: %d %v", code, body)
		}
		authzs := envelopeItems(body)
		if len(authzs) != 1 {
			t.Fatalf("活跃授权应为 1: %d", len(authzs))
		}
		authzID := authzs[0].(map[string]interface{})["id"].(string)
		code, _ = e.doJSON(t, http.MethodDelete, "/api/bots/"+botID+"/authorizations/"+authzID, nil)
		if code != http.StatusOK {
			t.Fatalf("revoke: %d", code)
		}
		e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
			map[string]interface{}{"text": "还要建", "client_message_id": "ra3"})
		waitForE2E(t, "撤销后回到待确认", 20*time.Second, func() bool {
			return len(e.botCalls(t, botID, "pending_confirmation")) >= 1
		})
	})

	t.Run("NewInputDuringModelAndTool", func(t *testing.T) {
		hold := make(chan struct{})
		e := setupE2E(t)
		e.fake.setSchedule(e2eTurn{Text: "正在想", Hold: hold})
		botID := e.createBot(t)
		e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
			map[string]interface{}{"text": "第一句", "client_message_id": "ni1"})
		e.fake.nextRequest(t, 15*time.Second)
		// 模型进行中插入新消息。
		e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
			map[string]interface{}{"text": "插队一句", "client_message_id": "ni2"})
		close(hold)
		// 后续请求必须包含插队消息。
		second := e.fake.nextRequest(t, 15*time.Second)
		if !strings.Contains(string(second), "插队一句") {
			t.Fatalf("后续请求未包含插队消息")
		}
		waitForE2E(t, "run finished", 20*time.Second, func() bool { return e.runFinished(t, botID) })
	})

	t.Run("StopSendsUserMessage", func(t *testing.T) {
		hold := make(chan struct{})
		e := setupE2E(t)
		e.fake.setSchedule(e2eTurn{Text: "长回答", Hold: hold}, e2eTurn{Text: "好的，已停止"})
		botID := e.createBot(t)
		e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
			map[string]interface{}{"text": "开始", "client_message_id": "sc1"})
		e.fake.nextRequest(t, 15*time.Second)
		runs := e.botRuns(t, botID)
		if len(runs) != 1 {
			t.Fatalf("runs: %d", len(runs))
		}
		runID := runs[0].(map[string]interface{})["id"].(string)
		code, body := e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/runs/"+runID+"/stop", nil)
		if code != http.StatusOK {
			t.Fatalf("stop: %d %v", code, body)
		}
		// 「停止」等价于一条用户消息：不硬取消当前回复，run 继续跑，由机器人
		// 结合上下文决定后续动作。
		runs = e.botRuns(t, botID)
		if runs[0].(map[string]interface{})["stop_requested"] != false {
			t.Fatalf("停止按钮不得硬取消当前回复: %v", runs[0])
		}
		close(hold)
		waitForE2E(t, "run settled", 20*time.Second, func() bool {
			runs := e.botRuns(t, botID)
			return len(runs) == 1 && runs[0].(map[string]interface{})["status"] == "completed"
		})
	})

	t.Run("DuplicateDeliveries", func(t *testing.T) {
		e := setupE2E(t)
		e.fake.setSchedule(e2eTurn{Text: "收到"})
		botID := e.createBot(t)
		payload := map[string]interface{}{"text": "同一句", "client_message_id": "dup-1"}
		code1, body1 := e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages", payload)
		code2, body2 := e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages", payload)
		if code1 != http.StatusOK || code2 != http.StatusOK {
			t.Fatalf("dup send: %d %d", code1, code2)
		}
		m1 := envelopeData(body1)["message"].(map[string]interface{})
		m2 := envelopeData(body2)["message"].(map[string]interface{})
		if m1["id"] != m2["id"] {
			t.Fatalf("重复投递应返回同一消息")
		}
		waitForE2E(t, "run finished", 20*time.Second, func() bool { return e.runFinished(t, botID) })
		// 用户消息恰好一条。
		users := 0
		for _, m := range e.botMessages(t, botID) {
			if m.(map[string]interface{})["kind"] == "user" {
				users++
			}
		}
		if users != 1 {
			t.Fatalf("用户消息应去重为一条: %d", users)
		}
	})

	t.Run("SSEOrderAndReplay", func(t *testing.T) {
		runSSEScenario(t)
	})

	t.Run("RequestSchemaAndPrefixStable", func(t *testing.T) {
		e := setupE2E(t)
		e.fake.setSchedule(e2eTurn{Text: "第一答"})
		botID := e.createBot(t)
		e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
			map[string]interface{}{"text": "q1", "client_message_id": "px1"})
		body1 := e.fake.nextRequest(t, 15*time.Second)
		waitForE2E(t, "第一轮", 20*time.Second, func() bool { return e.runFinished(t, botID) })
		e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
			map[string]interface{}{"text": "q2", "client_message_id": "px2"})
		body2 := e.fake.nextRequest(t, 15*time.Second)
		waitForE2E(t, "run finished", 20*time.Second, func() bool { return e.runFinished(t, botID) })

		// schema：tools 字节数组稳定；messages 前缀逐字节稳定（去掉末尾运行时上下文）。
		var p1, p2 struct {
			Model         string            `json:"model"`
			Messages      []json.RawMessage `json:"messages"`
			Tools         json.RawMessage   `json:"tools"`
			StreamOptions json.RawMessage   `json:"stream_options"`
		}
		if err := json.Unmarshal(body1, &p1); err != nil {
			t.Fatalf("decode1: %v", err)
		}
		if err := json.Unmarshal(body2, &p2); err != nil {
			t.Fatalf("decode2: %v", err)
		}
		if string(p1.Tools) != string(p2.Tools) {
			t.Fatalf("tools 字节不稳定")
		}
		if p1.Model != "e2e-model" {
			t.Fatalf("model 字段缺失")
		}
		if !strings.Contains(string(p1.StreamOptions), "include_usage") {
			t.Fatalf("缺少 stream_options.include_usage")
		}
		prefix1 := p1.Messages[:len(p1.Messages)-1]
		for i, raw := range prefix1 {
			if string(raw) != string(p2.Messages[i]) {
				t.Fatalf("前缀消息 %d 字节漂移", i)
			}
		}
		// 首条是系统提示（包含平台规则），末条是运行时上下文。
		if !strings.Contains(string(p1.Messages[0]), "Fast Ship") || !strings.Contains(string(p1.Messages[0]), "system") {
			t.Fatalf("首条应为系统提示")
		}
		var last map[string]interface{}
		if err := json.Unmarshal(p1.Messages[len(p1.Messages)-1], &last); err != nil {
			t.Fatalf("decode last: %v", err)
		}
		if last["role"] != "user" || !strings.Contains(fmt.Sprint(last["content"]), "运行时上下文") {
			t.Fatalf("末条应为运行时上下文: %v", last)
		}
	})

	t.Run("RecoveryAfterRestart", func(t *testing.T) {
		// 用同一个 SQLite 文件重建 runtime：直接复用 router 环境的库，
		// 在 BotService 上重新执行 RecoverOnStartup。
		e := setupE2E(t)
		botID := e.createBot(t)
		// 伪造进行中的 run（相当于崩溃现场）。
		var botRow struct{ ID string }
		if err := e.env.db.Raw("SELECT id FROM bots WHERE id = ?", botID).Scan(&botRow).Error; err != nil {
			t.Fatalf("load bot: %v", err)
		}
		e.env.db.Exec(`INSERT INTO bot_runs (id, bot_id, trigger_source, trigger_key, trigger_context_json,
			status, config_snapshot_json, input_through_seq, created_at, started_at)
			VALUES (?, ?, 'user_message', 'crash-1', '{}', 'running', '{}', 0, datetime('now'), datetime('now'))`,
			uuid.NewString(), botID)

		// 同一 SQLite 上重建 runtime 的恢复阶段：路由环境持有装配好的
		// BotService，再次执行 RecoverOnStartup 等价进程重启后的恢复。
		if err := e.env.botService.RecoverOnStartup(); err != nil {
			t.Fatalf("recover: %v", err)
		}
		waitForE2E(t, "崩溃 run 标记 failed", 20*time.Second, func() bool {
			runs := e.botRuns(t, botID)
			for _, r := range runs {
				run := r.(map[string]interface{})
				if run["trigger_key"] == "crash-1" {
					return run["status"] == "failed" && run["finished_at"] != nil
				}
			}
			return false
		})
	})
}

// parseSSEFrames 解析 SSE 文本为帧序列。
type sseFrameData struct {
	seq   int64
	event string
	data  string
}

func parseSSEFrames(t *testing.T, raw string) []sseFrameData {
	t.Helper()
	frames := []sseFrameData{}
	cur := sseFrameData{}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			fmt.Sscanf(line, "id: %d", &cur.seq)
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if cur.event != "" {
				frames = append(frames, cur)
			}
			cur = sseFrameData{}
		}
	}
	return frames
}

// ---------- 命名验收测试（设计第 7 节批次二/三命令直接可用） ----------

// TestBotMessages 主线消息端点：发送、幂等、冲突与分页。
func TestBotMessages(t *testing.T) {
	e := setupE2E(t)
	e.fake.setSchedule(e2eTurn{Text: "收到"})
	botID := e.createBot(t)

	code, body := e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
		map[string]interface{}{"text": "第一句", "client_message_id": "msg-key-1"})
	if code != http.StatusOK {
		t.Fatalf("send: %d %v", code, body)
	}
	first := envelopeData(body)["message"].(map[string]interface{})
	if first["seq"].(float64) != 1 || first["kind"] != "user" {
		t.Fatalf("消息不符: %v", first)
	}
	// 同键同正文幂等；同键不同正文 40925。
	code, body = e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
		map[string]interface{}{"text": "第一句", "client_message_id": "msg-key-1"})
	if code != http.StatusOK || envelopeData(body)["message"].(map[string]interface{})["id"] != first["id"] {
		t.Fatalf("幂等重发应返回同一消息: %d %v", code, body)
	}
	code, body = e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
		map[string]interface{}{"text": "另一句", "client_message_id": "msg-key-1"})
	if code != http.StatusConflict || int(body["code"].(float64)) != 40925 {
		t.Fatalf("同键不同正文应 40925: %d %v", code, body)
	}
	// 缺 client_message_id 40001。
	code, _ = e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
		map[string]interface{}{"text": "缺键"})
	if code != http.StatusBadRequest {
		t.Fatalf("缺 client_message_id 应 400: %d", code)
	}
	waitForE2E(t, "run finished", 20*time.Second, func() bool { return e.runFinished(t, botID) })

	code, body = e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/messages?after_seq=1&limit=10", nil)
	if code != http.StatusOK {
		t.Fatalf("分页: %d", code)
	}
	items := envelopeData(body)["items"].([]interface{})
	if len(items) != 1 {
		t.Fatalf("after_seq=1 应只返回后续消息: %d", len(items))
	}
}

// TestBotSSE SSE 顺序与断线重放。
func TestBotSSE(t *testing.T) {
	runSSEScenario(t)
}

// TestBotDecision 决定端点：approve 幂等、冲突决定 40926。
func TestBotDecision(t *testing.T) {
	e := setupE2E(t)
	e.fake.setSchedule(e2eTurn{ToolName: "fs_project", ToolID: "d1",
		ToolArgs: `{"action":"create","name":"决定流程项目"}`})
	botID := e.createBot(t)
	e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
		map[string]interface{}{"text": "建项目", "client_message_id": "de1"})
	var callID string
	waitForE2E(t, "待确认", 20*time.Second, func() bool {
		calls := e.botCalls(t, botID, "pending_confirmation")
		if len(calls) > 0 {
			callID = calls[0].(map[string]interface{})["id"].(string)
			return true
		}
		return false
	})
	code, body := e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/calls/"+callID+"/decision",
		map[string]interface{}{"decision": "approve"})
	if code != http.StatusOK {
		t.Fatalf("approve: %d %v", code, body)
	}
	code, _ = e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/calls/"+callID+"/decision",
		map[string]interface{}{"decision": "approve"})
	if code != http.StatusOK {
		t.Fatalf("重复 approve 应幂等: %d", code)
	}
	code, body = e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/calls/"+callID+"/decision",
		map[string]interface{}{"decision": "reject"})
	if code != http.StatusConflict || int(body["code"].(float64)) != 40926 {
		t.Fatalf("冲突决定应 40926: %d %v", code, body)
	}
	code, _ = e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/calls/"+callID+"/decision",
		map[string]interface{}{"decision": "nonsense"})
	if code != http.StatusBadRequest {
		t.Fatalf("非法决定应 400: %d", code)
	}
}

// TestBotCall 调用端点：列表过滤、详情与完整结果读取、取消幂等。
func TestBotCall(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	seedBotCallViaAPI(t, e, botID)

	var callID string
	waitForE2E(t, "待确认", 20*time.Second, func() bool {
		calls := e.botCalls(t, botID, "pending_confirmation")
		if len(calls) > 0 {
			callID = calls[0].(map[string]interface{})["id"].(string)
			return true
		}
		return false
	})
	code, body := e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/calls/"+callID, nil)
	if code != http.StatusOK || envelopeData(body)["id"] != callID {
		t.Fatalf("call 详情: %d %v", code, body)
	}
	code, _ = e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/calls/"+callID+"/cancel", nil)
	if code != http.StatusOK {
		t.Fatalf("cancel: %d", code)
	}
	code, _ = e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/calls/"+callID+"/cancel", nil)
	if code != http.StatusOK {
		t.Fatalf("重复 cancel 应幂等: %d", code)
	}
	calls := e.botCalls(t, botID, "cancelled")
	if len(calls) != 1 {
		t.Fatalf("cancelled 过滤: %d", len(calls))
	}
}

// TestBotAuthorization 授权端点：列表与撤销幂等。
func TestBotAuthorization(t *testing.T) {
	e := setupE2E(t)
	botID := e.createBot(t)
	e.fake.setSchedule(e2eTurn{ToolName: "fs_project", ToolID: "z1",
		ToolArgs: `{"action":"create","name":"授权端点项目"}`})
	e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
		map[string]interface{}{"text": "建项目", "client_message_id": "az1"})
	var callID string
	waitForE2E(t, "待确认", 20*time.Second, func() bool {
		calls := e.botCalls(t, botID, "pending_confirmation")
		if len(calls) > 0 {
			callID = calls[0].(map[string]interface{})["id"].(string)
			return true
		}
		return false
	})
	if code, _ := e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/calls/"+callID+"/decision",
		map[string]interface{}{"decision": "always_allow"}); code != http.StatusOK {
		t.Fatalf("always_allow: %d", code)
	}
	code, body := e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/authorizations?active=true", nil)
	if code != http.StatusOK || len(envelopeItems(body)) != 1 {
		t.Fatalf("活跃授权应为 1: %d %v", code, body)
	}
	authzID := envelopeItems(body)[0].(map[string]interface{})["id"].(string)
	if code, _ := e.doJSON(t, http.MethodDelete, "/api/bots/"+botID+"/authorizations/"+authzID, nil); code != http.StatusOK {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := e.doJSON(t, http.MethodDelete, "/api/bots/"+botID+"/authorizations/"+authzID, nil); code != http.StatusOK {
		t.Fatalf("重复 revoke 应幂等: %d", code)
	}
	code, body = e.doJSON(t, http.MethodGet, "/api/bots/"+botID+"/authorizations?active=true", nil)
	if code != http.StatusOK || len(envelopeItems(body)) != 0 {
		t.Fatalf("撤销后活跃授权应为 0: %d %v", code, body)
	}
}

// seedBotCallViaAPI 经主线造一个待确认调用（发消息 + 脚本工具回合）。
func seedBotCallViaAPI(t *testing.T, e *e2eEnv, botID string) {
	t.Helper()
	e.fake.setSchedule(e2eTurn{ToolName: "fs_project", ToolID: "seed-" + uuid.NewString()[:8],
		ToolArgs: `{"action":"create","name":"端点测试项目"}`})
	e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
		map[string]interface{}{"text": "建项目", "client_message_id": "seed-" + uuid.NewString()[:8]})
}

// runSSEScenario 是 SSE 顺序与断线重放场景（TestBotE2E 与 TestBotSSE 共用）。
func runSSEScenario(t *testing.T) {
	e := setupE2E(t)
	e.fake.setSchedule(e2eTurn{Text: "流式回答"})
	botID := e.createBot(t)

	// 先订阅再发送：事件经 SSE 流到达。context 可取消——SSE 断开只结束
	// 订阅，httptest 不会自动断开。
	streamCtx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()
	req := httptest.NewRequest(http.MethodGet, "/api/bots/"+botID+"/events", nil).WithContext(streamCtx)
	req.Header.Set("Authorization", e.jwt)
	rec := newSafeRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.env.router.ServeHTTP(rec, req)
	}()

	// 等订阅建立（SSE 头部写出）。
	waitForE2E(t, "SSE 建立头", 10*time.Second, func() bool {
		return rec.flushSeen() || rec.statusCode() == http.StatusOK
	})
	e.doJSON(t, http.MethodPost, "/api/bots/"+botID+"/messages",
		map[string]interface{}{"text": "开始流式", "client_message_id": "sse1"})
	waitForE2E(t, "事件到齐", 20*time.Second, func() bool {
		return strings.Contains(rec.bodyString(), "event: done")
	})

	frames := parseSSEFrames(t, rec.bodyString())
	if len(frames) == 0 {
		t.Fatalf("无 SSE 帧")
	}
	// 严格递增的事件 seq。
	lastSeq := int64(0)
	for _, fr := range frames {
		if fr.seq <= lastSeq {
			t.Fatalf("事件 seq 不递增: %d after %d", fr.seq, lastSeq)
		}
		lastSeq = fr.seq
	}
	types := []string{}
	for _, fr := range frames {
		types = append(types, fr.event)
	}
	if types[len(types)-1] != "done" {
		t.Fatalf("done 应最后到达: %v", types)
	}
	hasCommit := false
	for _, fr := range frames {
		if fr.event == "delta" && strings.Contains(fr.data, "message_commit") {
			hasCommit = true
			// message_commit 携带完整消息：其他标签页据此直接渲染。
			var frame struct {
				MessageID string          `json:"message_id"`
				Payload   json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal([]byte(fr.data), &frame); err != nil {
				t.Fatalf("解析 message_commit: %v", err)
			}
			if frame.MessageID == "" {
				t.Fatalf("message_commit 缺 message_id: %s", fr.data)
			}
			if !json.Valid(frame.Payload) || !strings.Contains(string(frame.Payload), `"content"`) {
				t.Fatalf("message_commit 载荷应为完整消息: %s", fr.data)
			}
		}
	}
	if !hasCommit {
		t.Fatalf("缺少 message_commit")
	}

	// 断开（只结束订阅，不取消 run）。
	cancelStream()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("SSE 订阅未随断开结束")
	}

	// 断线重放：after_seq=0 重放全部，事件 id 递增且与已收一致。
	replayCtx, cancelReplay := context.WithCancel(context.Background())
	defer cancelReplay()
	req2 := httptest.NewRequest(http.MethodGet, "/api/bots/"+botID+"/events?after_seq=0", nil).WithContext(replayCtx)
	req2.Header.Set("Authorization", e.jwt)
	rec2 := newSafeRecorder()
	replayDone := make(chan struct{})
	go func() {
		defer close(replayDone)
		e.env.router.ServeHTTP(rec2, req2)
	}()
	waitForE2E(t, "重放完成", 20*time.Second, func() bool {
		return strings.Contains(rec2.bodyString(), "event: done")
	})
	replayFrames := parseSSEFrames(t, rec2.bodyString())
	cancelReplay()
	<-replayDone
	if len(replayFrames) != len(frames) {
		t.Fatalf("重放帧数不符: %d vs %d", len(replayFrames), len(frames))
	}
	for i := range frames {
		if frames[i].seq != replayFrames[i].seq || frames[i].event != replayFrames[i].event {
			t.Fatalf("重放帧不符: %d", i)
		}
	}
}

package service

// INT-229 回归与基准：流式聚合（正文与工具调用分片）改用 strings.Builder
// 后，文字、工具参数、顺序、错误取消语义与旧实现逐字节一致；分配基准给出
// 可复现的长文本对照。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// int229Chunk 组一个 OpenAI 流式 chunk 的 SSE 载荷；toolCalls 传 nil 表示
// 纯正文/finish chunk，形状与 bot_test.go 的假服务一致。
func int229Chunk(t *testing.T, content string, toolCalls []map[string]interface{}, finish string) string {
	t.Helper()
	delta := map[string]interface{}{}
	if content != "" {
		delta["content"] = content
	}
	if toolCalls != nil {
		delta["tool_calls"] = toolCalls
	}
	m := map[string]interface{}{
		"id": "chatcmpl-int229", "object": "chat.completion.chunk",
		"created": 1700000000, "model": "fake-model",
		"choices": []map[string]interface{}{
			{"index": 0, "delta": delta, "finish_reason": finish},
		},
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	return string(raw)
}

// int229ToolPart 组一个流式工具调用分片：id/name/arguments 空串时不输出
// 该键，与上游残缺流一致。
func int229ToolPart(t *testing.T, index int, id, name, args string) map[string]interface{} {
	t.Helper()
	fn := map[string]interface{}{}
	if name != "" {
		fn["name"] = name
	}
	if args != "" {
		fn["arguments"] = args
	}
	part := map[string]interface{}{
		"index":    index,
		"type":     "function",
		"function": fn,
	}
	if id != "" {
		part["id"] = id
	}
	return part
}

// newInt229SSEServer 起一个逐条发送给定 SSE 载荷的假服务；abort 为真时
// 发完载荷直接断开连接（模拟上游中途故障）。
func newInt229SSEServer(t *testing.T, abort bool, payloads ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, p := range payloads {
			fmt.Fprintf(w, "data: %s\n\n", p)
			flusher.Flush()
		}
		if abort {
			panic(http.ErrAbortHandler)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// 确定性交错流：正文分片与工具调用分片交错到达，index 1 先于 index 0
// 出现，name/arguments 各自跨多个 chunk 拼接。聚合结果必须逐字节一致，
// 工具调用顺序按 index 首次出现排布。
func TestBotStreamChatInterleavedFragments(t *testing.T) {
	text := "你好，世界！Hello!好的"
	// 按 rune 边界切成多片，覆盖多字节字符被拆片的拼接。
	runes := []rune(text)
	splitText := []string{string(runes[:3]), string(runes[3:5]), string(runes[5:10]), string(runes[10:])}
	argsB := `{"action":"read","path":"USER.md","limit":100}`
	argsA := `{"action":"list","page":1}`
	payloads := []string{
		int229Chunk(t, splitText[0], nil, ""),
		int229Chunk(t, "", []map[string]interface{}{int229ToolPart(t, 1, "call-b", "fs_proj", "")}, ""),
		int229Chunk(t, splitText[1], nil, ""),
		int229Chunk(t, "", []map[string]interface{}{int229ToolPart(t, 1, "", "ect", argsB[:12])}, ""),
		int229Chunk(t, splitText[2], nil, ""),
		int229Chunk(t, "", []map[string]interface{}{int229ToolPart(t, 0, "call-a", "fs_mem", "")}, ""),
		int229Chunk(t, splitText[3], nil, ""),
		int229Chunk(t, "", []map[string]interface{}{int229ToolPart(t, 1, "", "", argsB[12:])}, ""),
		int229Chunk(t, "", []map[string]interface{}{int229ToolPart(t, 0, "", "ory", argsA)}, ""),
		int229Chunk(t, "", nil, "tool_calls"),
	}
	srv := newInt229SSEServer(t, false, payloads...)
	client := botNewClient(srv.URL, "sk-test-abcdef123456")
	reply, err := botStreamChat(context.Background(), &client, "https://relay.example.com/v1",
		int137DirectRequest(), botStreamTimeouts{FirstChunk: 5 * time.Second, Idle: 5 * time.Second}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if reply.Text != text {
		t.Fatalf("正文拼接不一致: %q", reply.Text)
	}
	if !reply.SawOutput {
		t.Fatalf("应有输出标记")
	}
	if reply.FinishReason != "tool_calls" {
		t.Fatalf("finish_reason 应为 tool_calls，实得 %q", reply.FinishReason)
	}
	if len(reply.ToolCalls) != 2 {
		t.Fatalf("应有 2 个工具调用，实得 %d 个: %+v", len(reply.ToolCalls), reply.ToolCalls)
	}
	// 顺序按 index 首次出现：index 1 先出现，排第一。
	if got := reply.ToolCalls[0]; got.ID != "call-b" || got.Name != "fs_project" || got.Arguments != argsB {
		t.Fatalf("工具调用[0]（index 1）聚合不一致: %+v", got)
	}
	if got := reply.ToolCalls[1]; got.ID != "call-a" || got.Name != "fs_memory" || got.Arguments != argsA {
		t.Fatalf("工具调用[1]（index 0）聚合不一致: %+v", got)
	}
}

// 断流残片：正文与工具调用分片收到一半连接断开，已聚合的部分必须可见
// （调用方按「本次尝试是否已有输出」决定能否重试），错误原样透出。
func TestBotStreamChatPartialFragmentsVisibleOnError(t *testing.T) {
	payloads := []string{
		int229Chunk(t, "半截正文", nil, ""),
		int229Chunk(t, "", []map[string]interface{}{int229ToolPart(t, 0, "call-x", "fs_project", `{"action":"li`)}, ""),
	}
	srv := newInt229SSEServer(t, true, payloads...)
	client := botNewClient(srv.URL, "sk-test-abcdef123456")
	reply, err := botStreamChat(context.Background(), &client, "https://relay.example.com/v1",
		int137DirectRequest(), botStreamTimeouts{FirstChunk: 5 * time.Second, Idle: 5 * time.Second}, nil)
	if err == nil {
		t.Fatalf("断流应报错")
	}
	if reply.Text != "半截正文" {
		t.Fatalf("失败时已收正文应可见: %q", reply.Text)
	}
	if len(reply.ToolCalls) != 1 || reply.ToolCalls[0].Name != "fs_project" ||
		reply.ToolCalls[0].Arguments != `{"action":"li` {
		t.Fatalf("失败时半截工具调用应原样保留: %+v", reply.ToolCalls)
	}
	if !reply.SawOutput {
		t.Fatalf("失败但已有输出，SawOutput 应为真")
	}
}

// BenchmarkBotStreamChatLongText 可复现长文本分配基准：512 字节 × 512 片
// 的确定性正文流，对比循环内 += 与 Builder 的分配开销。脚本自带服务，
// 不依赖外部资源；看门狗关零，不把定时器开销混进聚合对照。
func BenchmarkBotStreamChatLongText(b *testing.B) {
	const chunkSize = 512
	const chunks = 512
	piece := strings.Repeat("a", chunkSize)
	payloads := make([]string, 0, chunks+2)
	for i := 0; i < chunks; i++ {
		m := map[string]interface{}{
			"id": "chatcmpl-bench", "object": "chat.completion.chunk",
			"created": 1700000000, "model": "fake-model",
			"choices": []map[string]interface{}{
				{"index": 0, "delta": map[string]interface{}{"content": piece}, "finish_reason": ""},
			},
		}
		raw, _ := json.Marshal(m)
		payloads = append(payloads, string(raw))
	}
	finish, _ := json.Marshal(map[string]interface{}{
		"id": "chatcmpl-bench", "object": "chat.completion.chunk",
		"created": 1700000000, "model": "fake-model",
		"choices": []map[string]interface{}{
			{"index": 0, "delta": map[string]interface{}{}, "finish_reason": "stop"},
		},
	})
	payloads = append(payloads, string(finish), "[DONE]")

	b.StopTimer()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, p := range payloads {
			fmt.Fprintf(w, "data: %s\n\n", p)
			flusher.Flush()
		}
	}))
	client := botNewClient(srv.URL, "sk-test-abcdef123456")
	b.StartTimer()

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		reply, err := botStreamChat(context.Background(), &client, "https://relay.example.com/v1",
			int137DirectRequest(), botStreamTimeouts{}, nil)
		if err != nil {
			b.Fatalf("stream: %v", err)
		}
		if len(reply.Text) != chunkSize*chunks {
			b.Fatalf("正文长度不符: %d", len(reply.Text))
		}
	}
	b.StopTimer()
	srv.Close()
}

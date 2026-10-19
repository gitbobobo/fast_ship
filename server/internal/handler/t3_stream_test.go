package handler

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/service"
)

// 线程事件流（INT-132）：handler 层走真实 hub——用不可达地址的环境让常驻订阅
// 的监督循环失败并发布状态事件，验证「订阅注册 → 发布 → SSE 帧」整条链路与
// 帧格式。订阅必须在 Ensure 之前注册（先等首帧，首帧写出前订阅已注册），
// 否则 entry 首次失败事件会在订阅者到场前发生并被状态缓存去重。
func TestT3Handler_StreamThreadEvents(t *testing.T) {
	gin.SetMode(gin.TestMode)
	hub := service.NewT3ShellHub(nil)
	defer hub.Close()
	threads := service.NewT3ThreadsService(nil, nil, nil, hub)
	h := NewT3EnvironmentHandler(nil, threads)

	engine := gin.New()
	engine.GET("/api/threads/stream", func(c *gin.Context) {
		c.Set(middleware.ContextKeyUserID, "user-1")
		h.StreamThreadEvents(c)
	})
	srv := httptest.NewServer(engine)
	defer srv.Close()

	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, srv.URL+"/api/threads/stream", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}

	// 首帧必须是无 environment_id 的全局刷新信号。
	first := readSSEEvent(t, bufio.NewScanner(resp.Body))
	if first.event != "shell" || strings.TrimSpace(first.data) != `{"full":true}` {
		t.Fatalf("first frame = event %q data %q, want shell/{\"full\":true}", first.event, first.data)
	}

	// 订阅已注册：现在预热一个不可达环境，监督循环的失败会发布 failed 状态。
	env := &model.T3Environment{
		ID:      "env-stream-1",
		UserID:  "user-1",
		Name:    "T3",
		Address: "http://127.0.0.1:1",
	}
	if err := hub.Ensure(env, "tok"); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	scanner := bufio.NewScanner(resp.Body)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for a state event frame")
		}
		ev := readSSEEvent(t, scanner)
		if ev.event != "shell" {
			continue
		}
		if strings.Contains(ev.data, `"state":"failed"`) && strings.Contains(ev.data, `"environment_id":"env-stream-1"`) {
			break
		}
	}

	// 客户端断开：handler 随请求 ctx 退出（断言方式是 cancel 后 Do 侧正常收尾，
	// 挂住的 handler 由 -race 泄漏检查与后续用例兜底）。
	cancel()
}

// readSSEEvent 读一个事件块（到空行为止），返回 event 与 data。
func readSSEEvent(t *testing.T, scanner *bufio.Scanner) (ev struct {
	event string
	data  string
},
) {
	t.Helper()
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			ev.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if ev.event != "" || ev.data != "" {
				return ev
			}
		}
	}
	t.Fatalf("stream ended before a complete event: %v", scanner.Err())
	return ev
}

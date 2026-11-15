package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// fakeT3WSServer 是按 effect rpc JSON 帧协议说话的最小 T3 WS 服务，
// 帧形状对照 apps/server/src/ws.ts（RpcSerialization.layerJson）：
// Request → Chunk(+Ack 门控) → Exit。
type fakeT3WSServer struct {
	t              *testing.T
	mu             sync.Mutex // 保护 gotRequests（handler 与测试 goroutine 各自读写）
	wantBearer     string
	projectionExit json.RawMessage // 一次性请求返回的 success value（可选）

	gotRequests     []map[string]any
	failWithError   map[string]any // 非空时对所有请求返回该 Fail 错误
	refuseAuth      bool           // 握手时拒绝 Bearer
	wrongProtocolOK bool           // true 时不校验 orchestrationProtocol query
	silent          bool           // true 时收到请求只记录不应答（客户端只能等取消/超时）
}

// requestCount 读取已记录的请求帧数（handler 在另一 goroutine 追加，加锁读）。
func (f *fakeT3WSServer) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.gotRequests)
}

func (f *fakeT3WSServer) handler() websocket.Handler {
	return websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		if f.refuseAuth || (f.wantBearer != "" && conn.Request().Header.Get("Authorization") != "Bearer "+f.wantBearer) {
			// 握手已完成（x/net 服务端 Handler 在 101 之后才进来），
			// 用错误帧表达拒绝；真实的 401 分支由 HTTP 探测覆盖。
			_ = sendT3Frame(conn, map[string]any{
				"_tag":      "Exit",
				"requestId": 0,
				"exit": map[string]any{
					"_tag": "Failure",
					"cause": []any{map[string]any{
						"_tag": "Fail",
						"error": map[string]any{
							"_tag":    "EnvironmentAuthInvalidError",
							"code":    "auth_invalid",
							"reason":  "invalid_credential",
							"traceId": "t-ws",
							"message": "The environment rejected this client's credentials (invalid_credential).",
						},
					}},
				},
			})
			return
		}
		for {
			var data []byte
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if err := websocket.Message.Receive(conn, &data); err != nil {
				return
			}
			var frame struct {
				Tag     string          `json:"_tag"`
				ID      json.RawMessage `json:"id"`
				Method  string          `json:"tag"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(data, &frame); err != nil {
				f.t.Errorf("fake server: bad frame %s", data)
				return
			}
			switch frame.Tag {
			case "Request":
				var id any
				_ = json.Unmarshal(frame.ID, &id)
				f.mu.Lock()
				f.gotRequests = append(f.gotRequests, map[string]any{"id": id, "tag": frame.Method, "payload": json.RawMessage(frame.Payload)})
				f.mu.Unlock()
				if f.silent {
					continue
				}
				if f.failWithError != nil {
					_ = sendT3Frame(conn, exitFailureFrame(id, f.failWithError))
					return
				}
				// 这个 fake 只服务一次性请求：除 silent/failWithError 外，一切方法
				// 都以 projectionExit 成功退出，不区分具体 RPC 名字。
				_ = sendT3Frame(conn, exitSuccessFrame(id, f.projectionExit))
			case "Eof", "Ping":
			}
		}
	})
}

func sendT3Frame(conn *websocket.Conn, frame any) error {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	data, _ := json.Marshal(frame)
	return websocket.Message.Send(conn, data)
}

func exitSuccessFrame(id any, value json.RawMessage) map[string]any {
	return map[string]any{
		"_tag":      "Exit",
		"requestId": id,
		"exit":      map[string]any{"_tag": "Success", "value": value},
	}
}

func exitFailureFrame(id any, failError map[string]any) map[string]any {
	return map[string]any{
		"_tag":      "Exit",
		"requestId": id,
		"exit": map[string]any{
			"_tag":  "Failure",
			"cause": []any{map[string]any{"_tag": "Fail", "error": failError}},
		},
	}
}

func startFakeT3WSServer(t *testing.T, fake *fakeT3WSServer) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			http.NotFound(w, r)
			return
		}
		if !fake.wrongProtocolOK && r.URL.Query().Get("orchestrationProtocol") != "2" {
			w.WriteHeader(http.StatusUpgradeRequired)
			_, _ = w.Write([]byte(`{"code":"orchestration_protocol_incompatible"}`))
			return
		}
		fake.handler().ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func TestOneShotCall_ExitSuccess(t *testing.T) {
	fake := &fakeT3WSServer{
		t:              t,
		wantBearer:     "tok-2",
		projectionExit: json.RawMessage(`{"schemaVersion":1,"snapshotSequence":9,"projects":[],"threads":[],"archivedThreads":[]}`),
	}
	serverURL := startFakeT3WSServer(t, fake)
	wsBase := "ws" + strings.TrimPrefix(serverURL, "http")

	session, err := dialT3RPC(context.Background(), wsBase, "tok-2")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, err := session.call(ctx, "orchestration.getArchivedShellSnapshot", map[string]any{})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(value) == 0 {
		t.Fatalf("expected success value")
	}
}

// TestDialT3RPC_HandshakeBudget WS 握手必须受 ctx 预算约束（panel r1 #9：
// 挂起的握手不能绕过读超时）。
func TestDialT3RPC_HandshakeBudget(t *testing.T) {
	// 接受 TCP 但永不完成升级的假服务端。
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	}))
	defer hang.Close()
	wsBase := "ws" + strings.TrimPrefix(hang.URL, "http")

	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := dialT3RPC(ctx, wsBase, "token"); err == nil {
		t.Fatalf("expected handshake budget to abort the dial")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("handshake budget did not bound the dial, elapsed = %v", elapsed)
	}
}

// TestWSCall_CancelInterruptsBlockedRead 覆盖审查 W4：建连后 ctx 取消必须
// 立即打断阻塞中的读（靠连接关闭），不等到单帧读超时；取消后的下一个调用
// 在写出前即失败，不再发出任何 RPC 帧。
func TestWSCall_CancelInterruptsBlockedRead(t *testing.T) {
	// silent：收到请求只记录不应答，客户端的读只能靠取消或超时解除。
	fake := &fakeT3WSServer{t: t, wantBearer: "tok-3", silent: true}
	serverURL := startFakeT3WSServer(t, fake)
	wsBase := "ws" + strings.TrimPrefix(serverURL, "http")

	session, err := dialT3RPC(context.Background(), wsBase, "tok-3")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := session.call(ctx, "orchestration.getArchivedShellSnapshot", map[string]any{})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // 等调用进入阻塞读
	started := time.Now()
	cancel()
	if err := <-done; err == nil {
		t.Fatalf("expected cancellation to fail the blocked call")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("cancel must interrupt the blocked read immediately, elapsed = %v", elapsed)
	}
	if got := fake.requestCount(); got != 1 {
		t.Fatalf("expected exactly the one in-flight request frame, got %d", got)
	}

	// 取消后的调用必须因 ctx 取消在写出前失败（连接已关也一样要能归因到取消），
	// 连接上不出现新请求帧。
	if _, err := session.call(ctx, "orchestration.getArchivedShellSnapshot", map[string]any{}); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled call must fail with context.Canceled before writing, got %v", err)
	}
	if got := fake.requestCount(); got != 1 {
		t.Fatalf("canceled call must not send another request frame, got %d total", got)
	}
}

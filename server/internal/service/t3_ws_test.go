package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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
	snapshotValue  json.RawMessage // subscribeShell 首块 value
	projectionExit json.RawMessage // 一次性请求返回的 success value（可选）

	gotRequests     []map[string]any
	gotAcks         atomic.Int64 // handler goroutine 写、测试 goroutine 读，原子计数避免 data race
	gotInterrupts   atomic.Int64
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
				switch frame.Method {
				case "orchestration.subscribeShell":
					_ = sendT3Frame(conn, map[string]any{
						"_tag":      "Chunk",
						"requestId": id,
						"values":    []json.RawMessage{f.snapshotValue},
					})
					// 首块无需 Ack 即可发出；等待 Ack 后若来了 Interrupt 就退出。
					if f.waitForAckOrInterrupt(conn) {
						return
					}
					_ = sendT3Frame(conn, exitSuccessFrame(id, f.snapshotValue))
				case "orchestration.getArchivedShellSnapshot":
					_ = sendT3Frame(conn, exitSuccessFrame(id, f.projectionExit))
				default:
					_ = sendT3Frame(conn, exitSuccessFrame(id, f.projectionExit))
				}
			case "Ack":
				f.gotAcks.Add(1)
			case "Interrupt":
				f.gotInterrupts.Add(1)
			case "Eof", "Ping":
			}
		}
	})
}

// waitForAckOrInterrupt 返回是否收到 Interrupt。
func (f *fakeT3WSServer) waitForAckOrInterrupt(conn *websocket.Conn) bool {
	for {
		var data []byte
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := websocket.Message.Receive(conn, &data); err != nil {
			return true // 连接关闭视作结束
		}
		var frame struct {
			Tag string `json:"_tag"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			return true
		}
		switch frame.Tag {
		case "Ack":
			f.gotAcks.Add(1)
		case "Interrupt":
			f.gotInterrupts.Add(1)
			return true
		}
	}
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

func TestDialAndSubscribeShell_HappyPath(t *testing.T) {
	snapshot := json.RawMessage(`{
		"kind": "snapshot",
		"snapshot": {
			"schemaVersion": 1,
			"snapshotSequence": 3,
			"projects": [{"id": "p-1", "title": "Fast Ship"}],
			"threads": [{"id": "th-1", "projectId": "p-1", "title": "线程一", "status": "running"}],
			"archivedThreads": [{"id": "th-2", "projectId": "p-1", "title": "归档", "status": "idle", "archivedAt": "2026-10-01T00:00:00.000Z"}]
		}
	}`)
	fake := &fakeT3WSServer{t: t, wantBearer: "tok-1", snapshotValue: snapshot}
	serverURL := startFakeT3WSServer(t, fake)
	wsBase := "ws" + strings.TrimPrefix(serverURL, "http")

	session, err := dialT3RPC(context.Background(), wsBase, "tok-1")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var got t3ShellSnapshot
	err = session.callFirstChunkValue(ctx, "orchestration.subscribeShell", map[string]any{}, func(value json.RawMessage) (bool, error) {
		var item struct {
			Kind     string          `json:"kind"`
			Snapshot t3ShellSnapshot `json:"snapshot"`
		}
		if err := json.Unmarshal(value, &item); err != nil {
			return true, err
		}
		if item.Kind != "snapshot" {
			return false, nil
		}
		got = item.Snapshot
		return true, nil
	})
	if err != nil {
		t.Fatalf("subscribeShell: %v", err)
	}

	if len(got.Threads) != 1 || got.Threads[0].ID != "th-1" {
		t.Fatalf("threads = %+v", got.Threads)
	}
	if len(got.ArchivedThreads) != 1 || got.ArchivedThreads[0].ID != "th-2" {
		t.Fatalf("archived = %+v", got.ArchivedThreads)
	}
	if len(got.Projects) != 1 || got.Projects[0].Title != "Fast Ship" {
		t.Fatalf("projects = %+v", got.Projects)
	}

	// 请求帧形状：_tag=Request、tag=方法名、payload={}、headers=[]
	if len(fake.gotRequests) != 1 {
		t.Fatalf("requests = %+v", fake.gotRequests)
	}
	if fake.gotRequests[0]["tag"] != "orchestration.subscribeShell" {
		t.Fatalf("method = %v", fake.gotRequests[0]["tag"])
	}
	if string(fake.gotRequests[0]["payload"].(json.RawMessage)) != "{}" {
		t.Fatalf("payload = %v", fake.gotRequests[0]["payload"])
	}
	// Ack/Interrupt 由服务端 goroutine 异步读取，轮询等待后再断言。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && fake.gotInterrupts.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if fake.gotAcks.Load() < 1 {
		t.Fatalf("expected at least one Ack, got %d", fake.gotAcks.Load())
	}
	if fake.gotInterrupts.Load() != 1 {
		t.Fatalf("expected one Interrupt, got %d", fake.gotInterrupts.Load())
	}
}

func TestSubscribeShell_AuthzFailureMapsToCredentialRejected(t *testing.T) {
	snapshot := json.RawMessage(`{"kind":"snapshot","snapshot":{"schemaVersion":1,"snapshotSequence":1,"projects":[],"threads":[],"archivedThreads":[]}}`)
	// Bearer 匹配（HTTP 探测已单独覆盖凭证失效），让请求进入方法级授权失败分支。
	fake := &fakeT3WSServer{t: t, wantBearer: "expired", snapshotValue: snapshot}
	fake.failWithError = map[string]any{
		"_tag":          "EnvironmentAuthorizationError",
		"message":       "The authenticated token is missing required scope: orchestration:read.",
		"requiredScope": "orchestration:read",
	}
	serverURL := startFakeT3WSServer(t, fake)
	wsBase := "ws" + strings.TrimPrefix(serverURL, "http")

	session, err := dialT3RPC(context.Background(), wsBase, "expired")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer session.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = session.callFirstChunkValue(ctx, "orchestration.subscribeShell", map[string]any{}, func(json.RawMessage) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatalf("expected authorization error")
	}
	if _, msg := t3StatusFromError(err); msg == "" {
		t.Fatalf("expected status mapping, got %v", err)
	}
	if !errors.Is(err, errT3CredentialRejected) {
		t.Fatalf("expected credential error classification, got %v", err)
	}
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

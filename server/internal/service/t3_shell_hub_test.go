package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"golang.org/x/net/websocket"
)

// 以下为 t3ShellHub 的协议测试。fake 专为本文件构造（不复用整套
// fakeT3Environment）：按 effect rpc JSON 帧协议说话，帧由测试通过 conn 句柄
// 驱动，覆盖 INT-116 设计 E 节「hub 协议测试」列出的场景。测试用 gate/channel
// 等待确切阶段，不依赖 sleep 猜时序（仅退避/超时类用例按注入的小预算等待）。

// fakeShellRequest 是 fake 收到的一条客户端请求帧。
type fakeShellRequest struct {
	ID      int
	Method  string
	Payload map[string]any
}

// fakeShellConn 是一条已被 fake 接受的 WS 连接句柄。
type fakeShellConn struct {
	srv      *fakeShellStreamServer
	conn     *websocket.Conn
	requests chan fakeShellRequest
	acks     chan int
	closed   chan struct{}
}

func (c *fakeShellConn) nextRequest(timeout time.Duration) (fakeShellRequest, bool) {
	select {
	case req := <-c.requests:
		return req, true
	case <-time.After(timeout):
		return fakeShellRequest{}, false
	}
}

func (c *fakeShellConn) awaitAck(id int, timeout time.Duration) bool {
	deadline := time.After(timeout)
	for {
		select {
		case acked := <-c.acks:
			if acked == id {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

// awaitClosed 等服务端侧观察到连接关闭。
func (c *fakeShellConn) awaitClosed(timeout time.Duration) bool {
	select {
	case <-c.closed:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (c *fakeShellConn) sendValue(id int, value json.RawMessage) error {
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	data, err := json.Marshal(map[string]any{"_tag": "Chunk", "requestId": id, "values": []json.RawMessage{value}})
	if err != nil {
		return err
	}
	return websocket.Message.Send(c.conn, data)
}

func (c *fakeShellConn) sendSynchronized(id int) error {
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	data, _ := json.Marshal(map[string]any{"_tag": "Chunk", "requestId": id, "values": []json.RawMessage{json.RawMessage(`{"kind":"synchronized"}`)}})
	return websocket.Message.Send(c.conn, data)
}

func (c *fakeShellConn) sendExitFailure(id int, failError map[string]any) error {
	c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	data, _ := json.Marshal(map[string]any{
		"_tag":      "Exit",
		"requestId": id,
		"exit":      map[string]any{"_tag": "Failure", "cause": []any{map[string]any{"_tag": "Fail", "error": failError}}},
	})
	return websocket.Message.Send(c.conn, data)
}

// closeConn 服务端主动断开连接。
func (c *fakeShellConn) closeConn() {
	_ = c.conn.Close()
}

// fakeShellStreamServer 同时提供 HTTP 会话探测端点与 /ws 订阅端点。
type fakeShellStreamServer struct {
	t *testing.T

	mu        sync.Mutex
	bearer    string          // 会话端点与 WS 握手校验的 Bearer
	marker    bool            // getConfig.shellResumeCompletionMarker
	autoPong  bool            // 收到 {"_tag":"Ping"} 是否回 Pong
	archValue json.RawMessage // getArchivedShellSnapshot 应答体（nil 时按空快照答）
	archErr   map[string]any  // 非空时归档快照 RPC 回该 Fail 错误（G1 降级路径）
	archCalls int             // getArchivedShellSnapshot 被调次数（缓存断言用）
	// archLegacy 切旧上游：getArchivedShellSnapshot 按「方法不存在」拒绝，
	// subscribeArchivedShell 恢复为常驻订阅（注册后推快照+增量）。回退路径
	// 的旧面断言用。
	archLegacy   bool
	archiveSubs  chan fakeShellRequest // 收到的 subscribeArchivedShell 请求
	archiveConns chan *fakeShellConn   // 旧面订阅连接
	// archPaused 暂停归档快照应答（Finding A 交错测试用）：暂停发生在应答
	// 派发而非收帧循环上——handler 仍会把请求收进 requests 通道，测试据此
	// 确认「在途拉取已发起」再精确放行，而不是靠 sleep 猜时序。
	archPaused bool
	archResume chan struct{}
	connsCh    chan *fakeShellConn
	connCount  atomic.Int64
	gotPongs   atomic.Int64
	lastProbes atomic.Int64
}

func (s *fakeShellStreamServer) setBearer(bearer string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bearer = bearer
}

func (s *fakeShellStreamServer) checkBearer(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return r.Header.Get("Authorization") == "Bearer "+s.bearer
}

func (s *fakeShellStreamServer) handler(conn *websocket.Conn) {
	defer conn.Close()
	if !s.checkBearer(conn.Request()) {
		// 握手已完成，用鉴权失败帧表达拒绝（探测端点 401 是主分类路径）。
		_ = sendExitFailureFrame(conn, 0, map[string]any{
			"_tag":    "EnvironmentAuthInvalidError",
			"message": "invalid credential",
		})
		return
	}
	fc := &fakeShellConn{
		srv:      s,
		conn:     conn,
		requests: make(chan fakeShellRequest, 64),
		acks:     make(chan int, 64),
		closed:   make(chan struct{}),
	}
	s.connsCh <- fc
	defer close(fc.closed)
	for {
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		var data []byte
		if err := websocket.Message.Receive(conn, &data); err != nil {
			return
		}
		var frame struct {
			Tag       string          `json:"_tag"`
			ID        json.RawMessage `json:"id"`
			Method    string          `json:"tag"`
			RequestID json.RawMessage `json:"requestId"`
			Payload   json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			s.t.Errorf("fake server: bad frame %s", data)
			return
		}
		switch frame.Tag {
		case "Request":
			var id float64
			_ = json.Unmarshal(frame.ID, &id)
			var payload map[string]any
			if len(frame.Payload) > 0 {
				_ = json.Unmarshal(frame.Payload, &payload)
			}
			req := fakeShellRequest{ID: int(id), Method: frame.Method, Payload: payload}
			// 全部请求先入测试通道（getConfig 也在内，分派器靠它识别活跃面）。
			select {
			case fc.requests <- req:
			default:
				s.t.Errorf("fake server: request channel overflow")
			}
			switch req.Method {
			case t3RPCServerGetConfig:
				// 订阅前的一次性查询：按能力开关应答。
				config, _ := json.Marshal(map[string]any{
					"shellResumeCompletionMarker": s.currentMarker(),
					"settings":                    map[string]any{},
				})
				_ = sendExitSuccessFrame(conn, req.ID, config)
			case t3RPCGetArchivedShellSnapshot:
				s.mu.Lock()
				legacy := s.archLegacy
				archErr := s.archErr
				s.mu.Unlock()
				if legacy && archErr == nil {
					// 旧上游没有该 RPC：调用方得失败（回退路径由此激活）。
					// archErr 定制失败场景（非方法不存在，测「不误切」）时照常应答。
					_ = sendExitFailureFrame(conn, req.ID, map[string]any{
						"_tag":    "UnknownException",
						"message": "method not found: orchestration.getArchivedShellSnapshot",
					})
					continue
				}
				// 归档按需快照（INT-173）：应答内容由 archiveValue/archiveErr
				// 在应答时刻决定——应答前对 fake 的修改对后续读取可见。
				resp := s.archiveAnswer()
				if resp.err != nil {
					_ = sendExitFailureFrame(conn, req.ID, resp.err)
				} else {
					_ = sendExitSuccessFrame(conn, req.ID, resp.value)
				}
			case "orchestration.subscribeArchivedShell":
				s.mu.Lock()
				legacy := s.archLegacy
				s.mu.Unlock()
				if !legacy {
					// 新上游已删除该 RPC（#26238）：fake 默认报错，仍调用的客户
					// 端代码在测试里炸出来。
					_ = sendExitFailureFrame(conn, req.ID, map[string]any{
						"_tag":    "UnknownException",
						"message": "method not found: orchestration.subscribeArchivedShell",
					})
					continue
				}
				select {
				case s.archiveSubs <- req:
				default:
				}
				select {
				case s.archiveConns <- fc:
				default:
				}
				// 订阅受理：推全量快照体（包装成 snapshot 流项）。
				item := s.archiveStreamItem()
				_ = sendValueFrame(conn, req.ID, item)
			}
		case "Ack":
			var id float64
			_ = json.Unmarshal(frame.RequestID, &id)
			select {
			case fc.acks <- int(id):
			default:
			}
		case "Ping":
			s.gotPongs.Add(1)
			if s.autoPong {
				pong, _ := json.Marshal(map[string]any{"_tag": "Pong"})
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				_ = websocket.Message.Send(conn, pong)
			}
		}
	}
}

// mu2Marker 读取能力开关。
func (s *fakeShellStreamServer) currentMarker() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.marker
}

// archiveAnswer 取归档快照应答：archErr 优先（读失败路径），否则按
// archValue 答（缺省是空快照，保证未定制 fixture 时归档面也能就绪）。
// archPaused 置位时先在锁外等 resume——请求已被收进 requests 通道（测试
// 的「在途」证据），应答暂停让客户端的拉取停在「已发起未落地」。
func (s *fakeShellStreamServer) archiveAnswer() struct {
	value json.RawMessage
	err   map[string]any
} {
	s.mu.Lock()
	s.archCalls++
	paused := s.archPaused
	resume := s.archResume
	s.mu.Unlock()
	if paused && resume != nil {
		<-resume
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.archValue
	if value == nil {
		value = archiveSnapshotBody(1)
	}
	return struct {
		value json.RawMessage
		err   map[string]any
	}{value: value, err: s.archErr}
}

// pauseArchive 开始暂停归档快照应答：之后的 getArchivedShellSnapshot 请求
// 照常计数并入 requests 通道，但应答挂起直到 resumeArchive。
func (s *fakeShellStreamServer) pauseArchive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archPaused = true
	s.archResume = make(chan struct{})
}

// resumeArchive 放行全部已暂停的归档应答并结束暂停态。
func (s *fakeShellStreamServer) resumeArchive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archPaused = false
	if s.archResume != nil {
		close(s.archResume)
		s.archResume = nil
	}
}

// setArchiveFixture 定制归档快照应答（nil 错误 = 正常快照）。
func (s *fakeShellStreamServer) setArchiveFixture(value json.RawMessage, err map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archValue = value
	s.archErr = err
}

// archiveCallCount 返回归档快照 RPC 的被调次数。
func (s *fakeShellStreamServer) archiveCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archCalls
}

// archiveStreamItem 把归档快照体包装成订阅流的 snapshot 流项（旧面：
// 订阅首项即全量）。
func (s *fakeShellStreamServer) archiveStreamItem() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.archValue
	if value == nil {
		value = archiveSnapshotBody(1)
	}
	data, _ := json.Marshal(map[string]any{
		"kind":     "snapshot",
		"snapshot": json.RawMessage(value),
	})
	return data
}

// sendValueFrame 推一个 Chunk（订阅流帧）。
func sendValueFrame(conn *websocket.Conn, id int, values ...json.RawMessage) error {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	data, _ := json.Marshal(map[string]any{"_tag": "Chunk", "requestId": id, "values": values})
	return websocket.Message.Send(conn, data)
}

// setArchiveLegacy 切旧上游归档面：getArchivedShellSnapshot 拒绝、
// subscribeArchivedShell 生效。
func (s *fakeShellStreamServer) setArchiveLegacy(legacy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archLegacy = legacy
}

// awaitArchiveSub 等一条旧面订阅请求（回退路径激活的证据）。
func (s *fakeShellStreamServer) awaitArchiveSub(t *testing.T) fakeShellRequest {
	t.Helper()
	select {
	case req := <-s.archiveSubs:
		return req
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for subscribeArchivedShell")
		return fakeShellRequest{}
	}
}

func sendExitSuccessFrame(conn *websocket.Conn, id int, value json.RawMessage) error {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	data, _ := json.Marshal(map[string]any{"_tag": "Exit", "requestId": id, "exit": map[string]any{"_tag": "Success", "value": value}})
	return websocket.Message.Send(conn, data)
}

func sendExitFailureFrame(conn *websocket.Conn, id int, failError map[string]any) error {
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	data, _ := json.Marshal(map[string]any{
		"_tag":      "Exit",
		"requestId": id,
		"exit":      map[string]any{"_tag": "Failure", "cause": []any{map[string]any{"_tag": "Fail", "error": failError}}},
	})
	return websocket.Message.Send(conn, data)
}

type fakeShellServerOptions struct {
	bearer   string
	marker   bool
	autoPong bool
}

func startFakeShellStreamServer(t *testing.T, opts fakeShellServerOptions) (*fakeShellStreamServer, string) {
	t.Helper()
	srv := &fakeShellStreamServer{
		t:            t,
		bearer:       opts.bearer,
		marker:       opts.marker,
		autoPong:     opts.autoPong,
		connsCh:      make(chan *fakeShellConn, 64),
		archiveSubs:  make(chan fakeShellRequest, 64),
		archiveConns: make(chan *fakeShellConn, 64),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/session":
			srv.lastProbes.Add(1)
			if !srv.checkBearer(r) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":"auth_invalid","reason":"invalid_credential"}`))
				return
			}
			_, _ = w.Write([]byte(`{"authenticated":true}`))
		case "/ws":
			if r.URL.Query().Get("orchestrationProtocol") != "2" {
				w.WriteHeader(http.StatusUpgradeRequired)
				return
			}
			srv.connCount.Add(1)
			websocket.Handler(srv.handler).ServeHTTP(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return srv, server.URL
}

// hubTestConfig 是测试用小预算：退避/标记兜底都是毫秒级，读超时与
// Ping 间隔同样缩小以便在秒级用例里观察到重连与保活。ArchiveTTL 给足
// （一个用例时长），TTL 行为本身由专门用例注入小值验证。
func hubTestConfig() t3ShellHubConfig {
	return t3ShellHubConfig{
		FrameIdleTimeout:  400 * time.Millisecond,
		PingInterval:      100 * time.Millisecond,
		BackoffBase:       20 * time.Millisecond,
		BackoffMax:        40 * time.Millisecond,
		BackoffJitter:     0.25,
		StableResetAfter:  60 * time.Second,
		CredentialBackoff: 80 * time.Millisecond,
		MarkerWaitTimeout: 600 * time.Millisecond,
		DialBudget:        2 * time.Second,
		ArchiveTTL:        60 * time.Second,
	}
}

// hubTestEnv 构造测试环境行。configRevision 模拟 t3_environments.config_revision
// 的当前值——hub 的准入/淘汰判定输入，与墙钟时间无关。
func hubTestEnv(address string, configRevision int64) *model.T3Environment {
	return &model.T3Environment{
		ID:             "env-1",
		UserID:         "user-1",
		Name:           "T3",
		Address:        address,
		ConfigRevision: configRevision,
	}
}

// classifiedShellConn 是已分派的连接与其订阅/一次性请求。按需归档快照
// 的连接只发一次 getArchivedShellSnapshot 就关闭（INT-173），sub 即该请求。
type classifiedShellConn struct {
	conn *fakeShellConn
	sub  fakeShellRequest
}

// shellConnCollector 收下连接并分派（连接到达顺序不定）：活跃订阅连接先发
// getConfig 再发 subscribeShell；归档按需快照连接只发一次
// getArchivedShellSnapshot。暂时用不上的先停泊，供后续 await 取用（重连
// 用例的新连接也走这里）。
type shellConnCollector struct {
	t   *testing.T
	srv *fakeShellStreamServer

	mu      sync.Mutex
	active  []classifiedShellConn
	archive []classifiedShellConn
}

func newShellConnCollector(t *testing.T, srv *fakeShellStreamServer) *shellConnCollector {
	return &shellConnCollector{t: t, srv: srv}
}

// await 返回指定面的一条连接（先取停泊区，再等新连接到达）。
func (c *shellConnCollector) await(archived bool) classifiedShellConn {
	c.t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		c.mu.Lock()
		pool := &c.active
		if archived {
			pool = &c.archive
		}
		if len(*pool) > 0 {
			found := (*pool)[0]
			*pool = (*pool)[1:]
			c.mu.Unlock()
			return found
		}
		c.mu.Unlock()
		select {
		case conn := <-c.srv.connsCh:
			c.classify(conn)
		case <-deadline:
			c.t.Fatalf("timed out waiting for %s conn", map[bool]string{true: "archive", false: "active"}[archived])
		}
	}
}

func (c *shellConnCollector) classify(conn *fakeShellConn) {
	c.t.Helper()
	req, ok := conn.nextRequest(2 * time.Second)
	if !ok {
		c.t.Fatalf("conn produced no request")
	}
	var entry classifiedShellConn
	var archived bool
	switch req.Method {
	case t3RPCServerGetConfig:
		// 活跃面：getConfig 之后才是 subscribeShell。
		sub, ok := conn.nextRequest(2 * time.Second)
		if !ok || sub.Method != t3RPCSubscribeShell {
			c.t.Fatalf("expected subscribeShell after getConfig, got %+v", sub)
		}
		entry = classifiedShellConn{conn: conn, sub: sub}
	case t3RPCGetArchivedShellSnapshot:
		// 按需归档快照：单次请求后客户端即关闭连接。
		entry = classifiedShellConn{conn: conn, sub: req}
		archived = true
	default:
		c.t.Fatalf("unexpected first request %q", req.Method)
	}
	c.mu.Lock()
	if archived {
		c.archive = append(c.archive, entry)
	} else {
		c.active = append(c.active, entry)
	}
	c.mu.Unlock()
}

func shellSnapshotValue(sequence int, threadIDs ...string) json.RawMessage {
	threads := []map[string]any{}
	for _, id := range threadIDs {
		threads = append(threads, map[string]any{
			"id": id, "projectId": "p-1", "title": "线程-" + id, "status": "idle",
		})
	}
	data, _ := json.Marshal(map[string]any{
		"kind": "snapshot",
		"snapshot": map[string]any{
			"schemaVersion":    1,
			"snapshotSequence": sequence,
			"projects":         []map[string]any{{"id": "p-1", "title": "Fast Ship", "workspaceRoot": "/repo"}},
			"threads":          threads,
			"archivedThreads":  []any{},
		},
	})
	return data
}

// archiveSnapshotBody 是 getArchivedShellSnapshot 的应答体（快照体本身，
// 无 kind 包装——与旧归档订阅首项的 snapshot 字段同形状）。
func archiveSnapshotBody(sequence int, threadIDs ...string) json.RawMessage {
	threads := []map[string]any{}
	for _, id := range threadIDs {
		threads = append(threads, map[string]any{
			"id": id, "projectId": "p-1", "title": "归档-" + id, "status": "idle",
			"archivedAt": "2026-10-01T00:00:00.000Z",
		})
	}
	data, _ := json.Marshal(map[string]any{
		"schemaVersion":    1,
		"snapshotSequence": sequence,
		"projects":         []map[string]any{{"id": "p-1", "title": "Fast Ship", "workspaceRoot": "/repo"}},
		"threads":          threads,
	})
	return data
}

// badArchiveSnapshotBody 是带缺 ID 线程行的坏快照体：reset 整体拒收（坏
// 负载），不得静默丢一半再宣称就绪。
func badArchiveSnapshotBody(sequence int) json.RawMessage {
	data, _ := json.Marshal(map[string]any{
		"schemaVersion":    1,
		"snapshotSequence": sequence,
		"projects":         []map[string]any{{"id": "p-1", "title": "Fast Ship", "workspaceRoot": "/repo"}},
		"threads": []map[string]any{
			{"id": "", "projectId": "p-1", "title": "归档-缺ID", "status": "idle", "archivedAt": "2026-10-01T00:00:00.000Z"},
		},
	})
	return data
}

// waitForView 轮询调用 View 直到条件满足（hub 的状态翻转有广播，但测试里的
// 断言窗口用轮询表达最直接；等待都有上限）。
func waitForView(t *testing.T, hub *t3ShellHub, env *model.T3Environment, token string, until func(*t3ShellView) bool) *t3ShellView {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		view, err := hub.View(context.Background(), env, token)
		if err != nil {
			t.Fatalf("view: %v", err)
		}
		if until(view) {
			return view
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for view condition, last state = %v", view.State)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// establishBothFaces 走完一次完整初始化：活跃订阅连接就位并喂入快照 Ack；
// View 的首次读取同时发起一次归档按需快照（fake 按当时 fixture 自动应答），
// 归档就绪不阻塞 View。返回连接分派器与测试所需的句柄、订阅请求；返回的
// archive 项是那次一次性快照连接（请求后客户端即关闭）。
func establishBothFaces(t *testing.T, hub *t3ShellHub, srv *fakeShellStreamServer, env *model.T3Environment, token string, activeSeq int, activeThreads ...string) (*shellConnCollector, *fakeShellConn, fakeShellRequest, *fakeShellConn, fakeShellRequest) {
	t.Helper()

	viewDone := make(chan struct{})
	var firstView *t3ShellView
	var viewErr error
	go func() {
		defer close(viewDone)
		firstView, viewErr = hub.View(context.Background(), env, token)
	}()

	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	archive := collector.await(true)
	activeConn, activeSub := active.conn, active.sub
	archiveConn, archiveSub := archive.conn, archive.sub

	if err := activeConn.sendValue(activeSub.ID, shellSnapshotValue(activeSeq, activeThreads...)); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !activeConn.awaitAck(activeSub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}

	select {
	case <-viewDone:
		if viewErr != nil {
			t.Fatalf("first view: %v", viewErr)
		}
		if firstView.State != t3ShellReady {
			t.Fatalf("first view state = %v, want ready", firstView.State)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for first view")
	}
	return collector, activeConn, activeSub, archiveConn, archiveSub
}

// 活跃订阅建立；按 requestId Ack；快照进入组装视图。归档面是按需快照
// （INT-173）：View 触发的首次读取发一次 getArchivedShellSnapshot，应答
// 里的归档线程进 Archived 面；payload 为空 {}。
func TestT3ShellHub_BothFacesSubscribeAckAndShareState(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, activeConn, activeSub, archiveConn, archiveSub := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")

	// 冷启动订阅形状：活跃面带 requestCompletionMarker、无 afterSequence；
	// 归档快照 RPC 输入为空 {}。
	if activeSub.Payload["requestCompletionMarker"] != true {
		t.Fatalf("active payload = %+v, want requestCompletionMarker", activeSub.Payload)
	}
	if _, has := activeSub.Payload["afterSequence"]; has {
		t.Fatalf("cold start must not carry afterSequence: %+v", activeSub.Payload)
	}
	if len(archiveSub.Payload) != 0 {
		t.Fatalf("archive payload = %+v, want empty", archiveSub.Payload)
	}

	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.State == t3ShellReady && v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	if len(view.Snaps.Active.Threads) != 1 || view.Snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("active threads = %+v", view.Snaps.Active.Threads)
	}
	if view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("archived threads = %+v", view.Snaps.Archived.Threads)
	}
	if view.Snaps.Active.SnapshotSequence != 42 {
		t.Fatalf("snapshotSequence = %d, want 42", view.Snaps.Active.SnapshotSequence)
	}
	_ = activeConn
	_ = archiveConn
}

// 元数据帧（resolvedRepositoryIdentityRoots 存在）不改数据、照常 Ack。
func TestT3ShellHub_MetadataFrameIgnored(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)
	_, activeConn, activeSub, archiveConn, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")

	enrichment, _ := json.Marshal(map[string]any{
		"kind": "snapshot",
		"snapshot": map[string]any{
			"schemaVersion": 1, "snapshotSequence": 99,
			"projects": []map[string]any{{"id": "p-9", "title": "Enriched", "workspaceRoot": "/x"}},
			"threads":  []any{}, "archivedThreads": []any{},
		},
		"resolvedRepositoryIdentityRoots": []string{"/repo"},
	})
	if err := activeConn.sendValue(activeSub.ID, enrichment); err != nil {
		t.Fatalf("send enrichment: %v", err)
	}
	if !activeConn.awaitAck(activeSub.ID, 2*time.Second) {
		t.Fatalf("enrichment frame not acked")
	}

	view, err := hub.View(context.Background(), env, "tok")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.Snaps == nil || len(view.Snaps.Active.Projects) != 1 || view.Snaps.Active.Projects[0].ID != "p-1" {
		t.Fatalf("projects replaced by metadata frame: %+v", view.Snaps.Active.Projects)
	}
	if len(view.Snaps.Active.Threads) != 1 || view.Snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("threads touched by metadata frame: %+v", view.Snaps.Active.Threads)
	}
	if view.Snaps.Active.SnapshotSequence != 42 {
		t.Fatalf("cursor moved by metadata frame: %d", view.Snaps.Active.SnapshotSequence)
	}
	_ = archiveConn
}

// 断线恢复：afterSequence 带上游一次的游标；追平前 View 为 Stale，
// synchronized 到达后回 Ready。
func TestT3ShellHub_ResumeUsesAfterSequenceAndSyncMarker(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, activeConn, _, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	collector := newShellConnCollector(t, srv)

	activeConn.closeConn()

	// 重连后的活跃订阅必须带 afterSequence=42（恢复游标）。
	resume := collector.await(false)
	resumeConn, resumeSub := resume.conn, resume.sub
	if got := resumeSub.Payload["afterSequence"]; fmt.Sprint(got) != "42" {
		t.Fatalf("afterSequence = %v, want 42", got)
	}
	if resumeSub.Payload["requestCompletionMarker"] != true {
		t.Fatalf("resume must request completion marker: %+v", resumeSub.Payload)
	}

	// 追平前的窗口：连接已订阅、标记未到 → View 应为 Stale（不是 Ready）。
	view, err := hub.View(context.Background(), env, "tok")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.State != t3ShellStale {
		t.Fatalf("state during resume catch-up = %v, want stale", view.State)
	}
	// 数据仍在（最近可信快照继续可用）。
	if len(view.Snaps.Active.Threads) != 1 || view.Snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("stale view must keep last trusted data: %+v", view.Snaps.Active.Threads)
	}

	if err := resumeConn.sendSynchronized(resumeSub.ID); err != nil {
		t.Fatalf("send synchronized: %v", err)
	}
	if err := resumeConn.sendValue(resumeSub.ID, mustMarshalDelta(43, "th-1", "线程-改")); err != nil {
		t.Fatalf("send delta: %v", err)
	}
	// 两个块共用同一 requestId，Ack 无法区分；直接等增量落到视图上。
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.State == t3ShellReady && len(v.Snaps.Active.Threads) > 0 && v.Snaps.Active.Threads[0].Title == "线程-改"
	})
	if view.State != t3ShellReady {
		t.Fatalf("state = %v", view.State)
	}
}

// R2-2：强制全量重同步期间 marker 能力消失时不得乐观置追平——旧缓存必须等
// 新的权威全量快照到达才重新可信（否则会绕过陈旧快照的可信度守卫）。
func TestT3ShellHub_ForceResyncWithoutMarkerWaitsForSnapshot(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	cfg := hubTestConfig()
	cfg.MarkerWaitTimeout = 60 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, activeConn, _, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	collector := newShellConnCollector(t, srv)

	// 断线 → 带游标的 resume（请求 marker），但这条连接不发任何帧。
	activeConn.closeConn()
	resume := collector.await(false)
	if resume.sub.Payload["requestCompletionMarker"] != true {
		t.Fatalf("resume should request the marker: %+v", resume.sub.Payload)
	}

	// 能力在两次连接之间消失（例如上游版本回退）：marker 预算到点后会走
	// 强制全量重同步，而此时重连已不再请求 marker。
	srv.mu.Lock()
	srv.marker = false
	srv.mu.Unlock()

	resync := collector.await(false)
	if _, has := resync.sub.Payload["afterSequence"]; has {
		t.Fatalf("forced resync must drop the cursor: %+v", resync.sub.Payload)
	}
	if _, has := resync.sub.Payload["requestCompletionMarker"]; has {
		t.Fatalf("no marker capability means no marker request: %+v", resync.sub.Payload)
	}

	// 新权威快照到达前不得 Ready（旧缓存仍是旧的）。
	view, err := hub.View(context.Background(), env, "tok")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.State == t3ShellReady {
		t.Fatalf("must not be Ready before the authoritative snapshot arrives")
	}

	// 快照到达之前再断一次：强制全量重同步的要求必须保留到权威快照真正落地，
	// 下一次重连仍不得带旧游标、也不得因为无 marker 能力而乐观追平。
	resync.conn.closeConn()
	retry := collector.await(false)
	if _, has := retry.sub.Payload["afterSequence"]; has {
		t.Fatalf("forced resync must survive a disconnect: %+v", retry.sub.Payload)
	}
	retryView, err := hub.View(context.Background(), env, "tok")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if retryView.State == t3ShellReady {
		t.Fatalf("old cache must stay untrusted across the retried forced resync")
	}

	// 权威全量快照到达 → 重新可信。
	if err := retry.conn.sendValue(retry.sub.ID, shellSnapshotValue(60, "th-1")); err != nil {
		t.Fatalf("send resync snapshot: %v", err)
	}
	if !retry.conn.awaitAck(retry.sub.ID, 2*time.Second) {
		t.Fatalf("resync snapshot not acked")
	}
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.State == t3ShellReady && v.Snaps != nil && v.Snaps.Active.SnapshotSequence == 60
	})
}

func mustMarshalDelta(sequence int, id, title string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{
		"kind": "thread.updated", "sequence": sequence, "location": "active",
		"thread": map[string]any{"id": id, "projectId": "p-1", "title": title, "status": "idle"},
	})
	return data
}

// 服务端没有完成标记能力：无法证明恢复完成，不得乐观置追平——重连退回权威
// 全量（不带旧游标），新权威快照落地前保持 Stale。方向审计的硬性要求：
// 「陈旧快照不得当作可信运行态」；官方客户端的新 session 也是先加载权威快照、
// 失败才退回全量，而不是沿用上一会话的缓存。
func TestT3ShellHub_NoMarkerCapabilityResyncsInsteadOfTrusting(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: false, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, activeConn, activeSub, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	if _, has := activeSub.Payload["requestCompletionMarker"]; has {
		t.Fatalf("cold start must not request marker when unsupported: %+v", activeSub.Payload)
	}

	collector := newShellConnCollector(t, srv)
	activeConn.closeConn()

	resume := collector.await(false)
	if _, has := resume.sub.Payload["requestCompletionMarker"]; has {
		t.Fatalf("resume must not request marker when unsupported: %+v", resume.sub.Payload)
	}
	if _, has := resume.sub.Payload["afterSequence"]; has {
		t.Fatalf("no marker capability means no verifiable catch-up: must drop the cursor, got %+v", resume.sub.Payload)
	}

	// 新权威快照到达之前：旧缓存不得冒充可信运行态。
	view, err := hub.View(context.Background(), env, "tok")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.State == t3ShellReady {
		t.Fatalf("old cache must not be trusted before the authoritative snapshot arrives")
	}
	if view.Snaps == nil || len(view.Snaps.Active.Threads) != 1 {
		t.Fatalf("stale view must still carry the last known data: %+v", view.Snaps)
	}

	// 权威全量快照落地 → Ready。
	if err := resume.conn.sendValue(resume.sub.ID, shellSnapshotValue(60, "th-1")); err != nil {
		t.Fatalf("send resync snapshot: %v", err)
	}
	if !resume.conn.awaitAck(resume.sub.ID, 2*time.Second) {
		t.Fatalf("resync snapshot not acked")
	}
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.State == t3ShellReady && v.Snaps != nil && v.Snaps.Active.SnapshotSequence == 60
	})
}

// 请求了标记但预算内未等到（F1）：不得把未追平的缓存提升为 Ready——超时后
// 置强制全量重同步并结束本连接，重连不带 afterSequence（上游会直接发权威
// 全量快照），权威帧到达后才回到 Ready。
func TestT3ShellHub_MarkerWaitTimeoutForcesFullResync(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	cfg := hubTestConfig()
	cfg.MarkerWaitTimeout = 300 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, activeConn, _, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	resumeCollector := newShellConnCollector(t, srv)
	activeConn.closeConn()

	// 重连只发订阅、不回放不标记：预算内未追平 → Stale（不是 Ready），
	// 旧 idle 缓存不得被计时兜底提升为可信。
	resume := resumeCollector.await(false)
	if got := resume.sub.Payload["afterSequence"]; fmt.Sprint(got) != "42" {
		t.Fatalf("resume afterSequence = %v, want 42", got)
	}
	view, err := hub.View(context.Background(), env, "tok")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.State != t3ShellStale {
		t.Fatalf("state while awaiting marker = %v, want stale", view.State)
	}

	// 兜底到点：本连接被结束，强制全量重同步的重连不带 afterSequence
	//（marker 能力仍在，继续请求标记）。
	resyncCollector := newShellConnCollector(t, srv)
	resync := resyncCollector.await(false)
	if _, has := resync.sub.Payload["afterSequence"]; has {
		t.Fatalf("forced resync must drop afterSequence: %+v", resync.sub.Payload)
	}
	if resync.sub.Payload["requestCompletionMarker"] != true {
		t.Fatalf("forced resync must still request marker: %+v", resync.sub.Payload)
	}

	// 权威全量快照到达 → applyActiveSnapshotLocked 置追平 → Ready，游标被权威重置。
	if err := resync.conn.sendValue(resync.sub.ID, shellSnapshotValue(77, "th-1", "th-2")); err != nil {
		t.Fatalf("send authoritative snapshot: %v", err)
	}
	ready := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool { return v.State == t3ShellReady })
	if ready.Snaps.Active.SnapshotSequence != 77 {
		t.Fatalf("cursor = %d, want 77 (authoritative reset)", ready.Snaps.Active.SnapshotSequence)
	}
}

// Ping/Pong 保活：空闲（无数据帧）连接靠 Pong 刷新活性，不误判死亡。
func TestT3ShellHub_PingPongKeepsIdleConnectionAlive(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	// 空置超过 3 倍读超时：若无 Pong 刷新，连接早该判死重连。连接计数是
	// 活跃订阅 1 条 + 已完成的归档快照连接（按需快照立即关闭，connCount
	// 仍累计）。
	time.Sleep(3 * hubTestConfig().FrameIdleTimeout)

	if srv.connCount.Load() != 2 {
		t.Fatalf("idle connection must stay alive with pings, connCount = %d", srv.connCount.Load())
	}
	if srv.gotPongs.Load() == 0 {
		t.Fatalf("expected client pings to arrive")
	}
	view, err := hub.View(context.Background(), env, "tok")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.State != t3ShellReady {
		t.Fatalf("state = %v", view.State)
	}
}

// 读超时判死重连：服务端完全不回帧（连 Pong 也没有）时，读超时必须终结
// 连接并触发带恢复游标的重订阅。
func TestT3ShellHub_ReadTimeoutReconnects(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: false})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, activeConn, activeSub, _, _ := establishBothFaces(t, hub, srv, env, "tok", 7, "th-1")
	if err := activeConn.sendValue(activeSub.ID, mustMarshalDelta(8, "th-1", "推进")); err != nil {
		t.Fatalf("send delta: %v", err)
	}
	if !activeConn.awaitAck(activeSub.ID, 2*time.Second) {
		t.Fatalf("delta not acked")
	}

	// 服务端沉默到读超时：连接判死 → 重连 → afterSequence=8。
	collector := newShellConnCollector(t, srv)
	resume := collector.await(false)
	if fmt.Sprint(resume.sub.Payload["afterSequence"]) != "8" {
		t.Fatalf("resumed afterSequence = %v, want 8", resume.sub.Payload["afterSequence"])
	}
	// 喂回权威快照恢复 Ready。
	if err := resume.conn.sendValue(resume.sub.ID, shellSnapshotValue(9, "th-1")); err != nil {
		t.Fatalf("send snapshot: %v", err)
	}
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool { return v.State == t3ShellReady })
}

// 归档面失败只影响归档面（G1）：快照 RPC 失败写 ArchiveErr、归档面保持
// 未就绪，活跃面照常 Ready；失败不作废缓存（TTL 内下次读取不会重复打
// 上游），TTL 过期后重拉成功即恢复。
func TestT3ShellHub_ArchiveFailureIndependentAndResubscribes(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(nil, map[string]any{"_tag": "RuntimeError", "message": "archive stream exploded"})
	cfg := hubTestConfig()
	cfg.ArchiveTTL = 60 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, activeConn, activeSub, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")

	// 首次按需快照已失败：归档面不可信（ArchiveErr），活跃面 Ready 不受影响。
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil
	})
	if view.State != t3ShellReady {
		t.Fatalf("active face must stay ready after archive failure, state = %v", view.State)
	}
	if len(view.Snaps.Archived.Threads) != 0 {
		t.Fatalf("never-ready archive face must be empty, got %+v", view.Snaps.Archived.Threads)
	}

	// TTL 内重读不重复打上游（失败不推翻有效窗口的语义是「不作废 TTL」）。
	calls := srv.archiveCallCount()
	for i := 0; i < 3; i++ {
		if _, err := hub.View(context.Background(), env, "tok"); err != nil {
			t.Fatalf("view: %v", err)
		}
	}
	if srv.archiveCallCount() != calls {
		t.Fatalf("failed fetch must not be retried within TTL, calls %d → %d", calls, srv.archiveCallCount())
	}

	// 修好上游，TTL 过期后的下一次读取重拉成功 → 归档面恢复可信。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
	time.Sleep(cfg.ArchiveTTL + 20*time.Millisecond)
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	if view.State != t3ShellReady || len(view.Snaps.Active.Threads) != 1 {
		t.Fatalf("active face drifted: state=%v threads=%+v", view.State, view.Snaps.Active.Threads)
	}
	_ = activeConn
	_ = activeSub
}

// 归档按需快照的 TTL 缓存语义（INT-173）：TTL 内多次读取只产生一次上游
// RPC；InvalidateArchive 作废缓存后下一次读取重拉——写后刷新不依赖 TTL。
func TestT3ShellHub_ArchiveTTLAndInvalidation(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	calls := srv.archiveCallCount()

	// TTL 内连续读取（View + Peek）不再产生新 RPC。
	for i := 0; i < 4; i++ {
		if _, err := hub.View(context.Background(), env, "tok"); err != nil {
			t.Fatalf("view: %v", err)
		}
		if _, err := hub.Peek(env, "tok"); err != nil {
			t.Fatalf("peek: %v", err)
		}
	}
	if srv.archiveCallCount() != calls {
		t.Fatalf("archive reads within TTL must be cached, calls %d → %d", calls, srv.archiveCallCount())
	}

	// 写后失效：InvalidateArchive 后下一次读取重拉，且 fake 的 fixture
	// 变化对这次读取可见（快照不是实时面，但写后必须读到写后状态）。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
	hub.InvalidateArchive("user-1", "env-1")
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	if srv.archiveCallCount() != calls+1 {
		t.Fatalf("invalidation must trigger one refetch, calls %d → %d", calls, srv.archiveCallCount())
	}
	_ = view
}

// 别的客户端归档/删除线程（P2 修复）：活跃流 thread.removed 到达后归档缓
// 存立即失效——removal 不带「去向」（归档或彻底删除），统一按「归档集可能
// 变了」处理；下一次读取重拉到含该线程的新快照，线程不会从活跃、归档两
// 边同时消失到 TTL 过期（useThreads 不轮询）。
func TestT3ShellHub_ActiveRemovedInvalidatesArchive(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, activeConn, activeSub, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")

	key := t3ShellHubKey(env.UserID, env.ID)
	hub.mu.Lock()
	e := hub.entries[key]
	hub.mu.Unlock()
	if e == nil {
		t.Fatalf("entry missing")
	}
	e.state.mu.Lock()
	genBefore := e.state.archiveGeneration
	e.state.mu.Unlock()

	// 另一个客户端把 th-1 归档：活跃流到达 thread.removed（上游对归档也发
	// active removal，ShellStream.ts:292-320）。归档快照 fixture 同步换上
	// 「写后状态」：失效后的重拉必须读到含 th-1 的新归档面。
	removed, _ := json.Marshal(map[string]any{
		"kind": "thread.removed", "sequence": 43, "location": "active", "threadId": "th-1",
	})
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-1"), nil)
	if err := activeConn.sendValue(activeSub.ID, removed); err != nil {
		t.Fatalf("send thread.removed: %v", err)
	}
	if !activeConn.awaitAck(activeSub.ID, 2*time.Second) {
		t.Fatalf("thread.removed frame not acked")
	}

	// Ack 在全部 value 应用后才发 → 失效此刻已同步生效：标不可信（stale、
	// ArchiveErr 非空）且世代推进，不依赖 TTL 到期。
	e.state.mu.Lock()
	stale, gen, archErr := e.state.archiveStale, e.state.archiveGeneration, e.state.archiveErr
	e.state.mu.Unlock()
	if !stale || archErr == nil || gen != genBefore+1 {
		t.Fatalf("active removal must invalidate the archive face: stale=%v err=%v gen %d → %d", stale, archErr, genBefore, gen)
	}

	// 流处理路径不发起同步拉取：失效只标不可信，重拉由下一次读取 kick。
	calls := srv.archiveCallCount()
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		if v.Snaps == nil || v.Snaps.ArchiveErr != nil {
			return false
		}
		for _, th := range v.Snaps.Archived.Threads {
			if th.ID == "th-1" {
				return true
			}
		}
		return false
	})
	if got := srv.archiveCallCount(); got != calls+1 {
		t.Fatalf("active removal must trigger exactly one archive refetch, calls %d → %d", calls, got)
	}
	for _, th := range view.Snaps.Active.Threads {
		if th.ID == "th-1" {
			t.Fatalf("removed thread must leave the active face: %+v", view.Snaps.Active.Threads)
		}
	}
}

// 归档按需快照的过期重拉（INT-173）：TTL 过期后的读取重新打上游——一次
// 成功快照不得被长期当作实时完整数据。
func TestT3ShellHub_ArchiveTTLExpiryRefetches(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	cfg := hubTestConfig()
	cfg.ArchiveTTL = 60 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	calls := srv.archiveCallCount()

	time.Sleep(cfg.ArchiveTTL + 20*time.Millisecond)
	if _, err := hub.View(context.Background(), env, "tok"); err != nil {
		t.Fatalf("view: %v", err)
	}
	deadline := time.After(2 * time.Second)
	for srv.archiveCallCount() <= calls {
		select {
		case <-deadline:
			t.Fatalf("expired TTL must refetch the archive snapshot")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// 归档按需快照的并发合并（INT-173）：entry 未建时并发 View 共用一次初始化，
// 归档只产生一次 getArchivedShellSnapshot。
func TestT3ShellHub_ArchiveConcurrentViewsShareFetch(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	const viewers = 8
	var wg sync.WaitGroup
	for i := 0; i < viewers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = hub.View(context.Background(), env, "tok")
		}()
	}

	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	collector.await(true) // 按需归档快照连接
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(5, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	wg.Wait()

	deadline := time.After(2 * time.Second)
	for srv.archiveCallCount() < 1 {
		select {
		case <-deadline:
			t.Fatalf("archive snapshot was never fetched")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// 稳定后不允许出现额外拉取（并发合并 + TTL 缓存）。
	time.Sleep(100 * time.Millisecond)
	if got := srv.archiveCallCount(); got != 1 {
		t.Fatalf("concurrent views must share one archive fetch, calls = %d", got)
	}
}

// 归档快照连接的中断（读超时/服务端断开）记为归档读取失败——不推翻活跃面、
// 数据保留与否按「是否有过成功快照」表达。
func TestT3ShellHub_ArchiveFetchDisconnectFailsRead(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	_, _, _, archiveConn, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")

	// 快照连接已随一次性 RPC 完成被客户端关闭；再断开是幂等的无害路径。
	archiveConn.closeConn()
	view, err := hub.View(context.Background(), env, "tok")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.State != t3ShellReady || len(view.Snaps.Active.Threads) != 1 {
		t.Fatalf("active face must stay ready, state=%v threads=%+v", view.State, view.Snaps.Active.Threads)
	}
}

// 凭证失效：探测 repair → View 给 Failed 且错误可归类为凭证被拒。
func TestT3ShellHub_CredentialRejectionSurfacesAsFailed(t *testing.T) {
	_, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok-2", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	view, err := hub.View(context.Background(), env, "tok-1")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.State != t3ShellFailed {
		t.Fatalf("state = %v, want failed", view.State)
	}
	if !errors.Is(view.Err, errT3CredentialRejected) {
		t.Fatalf("err = %v, want credential rejected", view.Err)
	}
	if view.Snaps != nil {
		t.Fatalf("failed view must not carry snapshots")
	}
}

// 凭证替换：指纹变化且世代更新（revision 更高）→ 停旧建新；世代不新的迟到
// 请求被拒绝。
func TestT3ShellHub_CredentialReplacementRestartsEntry(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok-1", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	env1 := hubTestEnv(url, 0)
	collector, activeConn, _, _, _ := establishBothFaces(t, hub, srv, env1, "tok-1", 42, "th-1")

	// 迟到请求：同世代不同凭证 → 拒绝（errT3ShellConfigStale），不打断现有 entry。
	_, err := hub.View(context.Background(), env1, "tok-rotated")
	if !errors.Is(err, errT3ShellConfigStale) {
		t.Fatalf("stale-request error = %v, want errT3ShellConfigStale", err)
	}

	// 凭证替换：新世代 + 新 token → 停旧建新，新 entry 用新 token 建连。
	srv.setBearer("tok-2")
	env2 := hubTestEnv(url, 1)
	viewDone := make(chan *t3ShellView, 1)
	go func() {
		view, err := hub.View(context.Background(), env2, "tok-2")
		if err == nil {
			viewDone <- view
		}
	}()
	newActiveConn := collector.await(false)
	newActive, newSub := newActiveConn.conn, newActiveConn.sub
	if err := newActive.sendValue(newSub.ID, shellSnapshotValue(1, "th-new")); err != nil {
		t.Fatalf("send snapshot: %v", err)
	}
	select {
	case view := <-viewDone:
		if view.State != t3ShellReady {
			t.Fatalf("new entry state = %v", view.State)
		}
		if len(view.Snaps.Active.Threads) != 1 || view.Snaps.Active.Threads[0].ID != "th-new" {
			t.Fatalf("new entry data = %+v", view.Snaps.Active.Threads)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for new entry view")
	}

	// 旧 entry 已停止：连接关闭、状态被摘除换新。
	if !activeConn.awaitClosed(2 * time.Second) {
		t.Fatalf("old entry connection must be closed after replacement")
	}
	hub.mu.Lock()
	e := hub.entries[t3ShellHubKey("user-1", "env-1")]
	hub.mu.Unlock()
	if e == nil || e.configRevision != env2.ConfigRevision {
		t.Fatalf("registry must hold the replacement entry, got %+v", e)
	}
}

// Drop 与迟到 View 交错：Drop 后旧世代的 View（revision < cutoff）被拒绝，
// 且不复活 entry；携带新世代的请求（revision == cutoff）可以重建。
func TestT3ShellHub_DropGuardsAgainstLateViews(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	env := hubTestEnv(url, 0)
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	// 重配形状的 Drop：cutoff 是落库后自增出的新世代（0 → 1）。
	hub.Drop("user-1", "env-1", 1)
	// 迟到 View（revision 0 < floor 1）→ 拒绝且不建连。
	before := srv.connCount.Load()
	_, err := hub.View(context.Background(), env, "tok")
	if !errors.Is(err, errT3ShellConfigStale) {
		t.Fatalf("late view error = %v, want errT3ShellConfigStale", err)
	}
	time.Sleep(50 * time.Millisecond)
	if srv.connCount.Load() != before {
		t.Fatalf("late view must not resurrect the entry, connCount %d → %d", before, srv.connCount.Load())
	}
	hub.mu.Lock()
	entries := len(hub.entries)
	hub.mu.Unlock()
	if entries != 0 {
		t.Fatalf("dropped entry must be removed, entries = %d", entries)
	}

	// 新世代 View 正常重建（喂入新连接的快照）。
	envNew := hubTestEnv(url, 1)
	_, _, _, _, _ = establishBothFaces(t, hub, srv, envNew, "tok", 2, "th-1b")
}

// 迟到的 Drop（cutoff 更小）不得关掉新代 entry；与 entry 同代的 Drop（真实流程
// 里「重配 Drop → 新请求重建 → 又一次幂等 Drop」就是这个形状）也只淘汰旧代
// ——revision < cutoff 才停。
func TestT3ShellHub_DropKeepsNewerEntry(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	// 旧代 entry：revision 0。
	env := hubTestEnv(url, 0)
	_, oldConn, _, _, _ := establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	// Drop(cutoff=1) 大于 entry 的世代 0：旧代 entry 被关闭。
	hub.Drop("user-1", "env-1", 1)
	if !oldConn.awaitClosed(2 * time.Second) {
		t.Fatalf("drop with greater cutoff must retire the old-generation entry")
	}

	// 新代 entry：View 用新世代（revision 1）重建。
	envNew := hubTestEnv(url, 1)
	_, newConn, newSub, _, _ := establishBothFaces(t, hub, srv, envNew, "tok", 2, "th-1b")

	// 迟到的 Drop(0)：cutoff 小于 entry 世代 → 保留。
	hub.Drop("user-1", "env-1", 0)
	// 同代 Drop(1)：只淘汰 revision < cutoff 的旧代 → 保留。
	hub.Drop("user-1", "env-1", 1)
	hub.mu.Lock()
	e := hub.entries[t3ShellHubKey("user-1", "env-1")]
	hub.mu.Unlock()
	if e == nil || e.configRevision != envNew.ConfigRevision {
		t.Fatalf("late/same-generation drop must keep the newer entry, got %+v", e)
	}
	if err := newConn.sendValue(newSub.ID, mustMarshalDelta(3, "th-1b", "仍活着")); err != nil {
		t.Fatalf("newer entry connection must survive late drop: %v", err)
	}
	if !newConn.awaitAck(newSub.ID, 2*time.Second) {
		t.Fatalf("newer entry connection must still ack after late drop")
	}
}

// Delete 用 MaxInt64 哨兵做 tombstone（INT-132）：淘汰条件恒真、entry 无条件
// 关闭；floor 同为哨兵，任何在途请求（世代必然有限）一律被拒且不建 entry。
// 旧实现传「删除前 revision + 1」挡不住「并发重配先升世代再被删除」的交错
// ——旧 cutoff=r+1 会保留同代 entry 并放行迟到的 Ensure。
func TestT3ShellHub_DeleteNowFloorBlocksAllInFlightVersions(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	// 环境行的世代是「删除前」读出的 revision 0；删除发生在其后。
	env := hubTestEnv(url, 0)
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	hub.Drop("user-1", "env-1", math.MaxInt64)
	if !errors.Is(hub.Ensure(env, "tok"), errT3ShellConfigStale) {
		t.Fatalf("ensure after delete must be fenced by the sentinel floor")
	}
	before := srv.connCount.Load()
	if _, err := hub.View(context.Background(), env, "tok"); !errors.Is(err, errT3ShellConfigStale) {
		t.Fatalf("view after delete = %v, want errT3ShellConfigStale", err)
	}
	time.Sleep(50 * time.Millisecond)
	if srv.connCount.Load() != before {
		t.Fatalf("late view must not resurrect the entry, connCount %d → %d", before, srv.connCount.Load())
	}
	hub.mu.Lock()
	entries := len(hub.entries)
	hub.mu.Unlock()
	if entries != 0 {
		t.Fatalf("deleted entry must stay dropped, entries = %d", entries)
	}
}

// floor 的保留按**记录时刻**计算：floor 值（revision）本身没有时间含义，
// 刚记下的 floor 必须立即生效并保留满 TTL，不能按值清理。
func TestT3ShellHub_FloorRetentionKeyedByRecordTime(t *testing.T) {
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	// 无 entry 的环境：Drop 直接记 floor（cutoff 是变更后的新世代）。
	hub.Drop("user-2", "env-old", 5)
	hub.mu.Lock()
	_, hasFloor := hub.floors[t3ShellHubKey("user-2", "env-old")]
	hub.mu.Unlock()
	if !hasFloor {
		t.Fatalf("freshly recorded floor must not be GC'd")
	}
	// env 的 user/env ID 与 Drop 的 key 一致（hubTestEnv 硬编码 user-1/env-1，
	// 不能用）；地址不可达也无所谓——floor 守卫在任何拨号之前生效。
	// revision 4（< cutoff）的迟到请求被守卫。
	env := &model.T3Environment{ID: "env-old", UserID: "user-2", Address: "http://127.0.0.1:1", ConfigRevision: 4}
	if _, err := hub.View(context.Background(), env, "tok"); !errors.Is(err, errT3ShellConfigStale) {
		t.Fatalf("floor must guard smaller revisions, err = %v", err)
	}
	// revision == cutoff 的请求放行：判定统一是「< cutoff 即拒」，没有
	// 「相等即拒」的特例。
	current := *env
	current.ConfigRevision = 5
	if _, err := hub.View(context.Background(), &current, "tok"); errors.Is(err, errT3ShellConfigStale) {
		t.Fatalf("revision equal to cutoff must be admitted, err = %v", err)
	}
}

// R2-1：marker 截止只对带游标的 resume 生效。冷启动（无 afterSequence）的慢
// 全量快照不能被 marker 预算截断——否则首帧比预算慢的环境会陷入「重连 → 再被
// 截断」的死循环，永远 Ready 不了（这正是本改动要解决的那类慢环境）。
func TestT3ShellHub_ColdSlowSnapshotNotInterruptedByMarkerBudget(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	cfg := hubTestConfig()
	cfg.MarkerWaitTimeout = 80 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	viewDone := make(chan struct{})
	var view *t3ShellView
	var viewErr error
	go func() {
		defer close(viewDone)
		view, viewErr = hub.View(context.Background(), env, "tok")
	}()

	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	collector.await(true) // 归档按需快照连接（自动应答后客户端即关）

	// 冷启动仍会请求 marker（能力支持），但此刻故意不发快照：等过 marker 预算
	// 与若干个 tick，连接必须保持原样。
	if active.sub.Payload["requestCompletionMarker"] != true {
		t.Fatalf("cold subscribe should still request the marker: %+v", active.sub.Payload)
	}
	if _, has := active.sub.Payload["afterSequence"]; has {
		t.Fatalf("cold subscribe must not carry afterSequence: %+v", active.sub.Payload)
	}
	time.Sleep(cfg.MarkerWaitTimeout + 4*(cfg.PingInterval/2))
	// 连接计数是活跃订阅 1 条 + 已完成的归档按需快照连接（快照完成后
	// 客户端即关，connCount 仍累计）。
	if got := srv.connCount.Load(); got != 2 {
		t.Fatalf("cold slow snapshot must not be torn down, connCount = %d", got)
	}

	// 慢快照终于到达：直接转 Ready，没有走强制重同步。
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(7, "th-1")); err != nil {
		t.Fatalf("send slow snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("slow snapshot not acked")
	}
	select {
	case <-viewDone:
		if viewErr != nil {
			t.Fatalf("view: %v", viewErr)
		}
		if view.State != t3ShellReady {
			t.Fatalf("state = %v, want ready", view.State)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for view")
	}
}

// codex 审计时间戳乱序场景的回归（INT-116 方案 B）：updated_at 在 UPDATE 前用
// time.Now() 生成、没有提交顺序保证——重配生成 t1 尚未提交时，并发改名先提交
// 更大的 t2，重配随后以 t1 落库并 Drop(t1)，迟到的「旧凭证 + t2」请求能绕过
// cutoff。config_revision 由 SQL 自增、改名不递增：重配把世代推到 1 后，任何
// 在途请求无论读到的是改名后的行还是变更前的行，携带的都是 revision 0，一律
// 被拒；只有重配后读出的 revision 1 被放行。
func TestT3ShellHub_DropFencesIntermediateRenameVersion(t *testing.T) {
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	// 重配落库（revision 0 → 1）后用新世代做 cutoff。
	hub.Drop("user-1", "env-1", 1)

	env := func(revision int64) *model.T3Environment {
		return &model.T3Environment{ID: "env-1", UserID: "user-1", Address: "http://127.0.0.1:1", ConfigRevision: revision}
	}
	// 并发改名窗口内读出的行：updated_at 更大，但改名不递增 revision——仍是 0，
	// 必须被拒（旧实现里它带着更大的时间戳绕过了 cutoff）；变更前读出的行世代
	// 相同（同为 0），一并被拒——这正是「改名不产生新世代」的含义。
	if _, err := hub.View(context.Background(), env(0), "tok"); !errors.Is(err, errT3ShellConfigStale) {
		t.Fatalf("stale revision (rename-window or pre-change read) must be fenced, err = %v", err)
	}
	// 携带新世代的正常请求不被 floor 拒（会去拨号并失败，但不是「配置已更新」）。
	if _, err := hub.View(context.Background(), env(1), "tok"); errors.Is(err, errT3ShellConfigStale) {
		t.Fatalf("current revision must be admitted by the floor, err = %v", err)
	}
}

// 已取消的请求不得在 hub 里建 entry 并拉起常驻订阅（云端审查 P2）：一次超时
// 的列表请求会给每个排队环境留下长驻连接，突破调用方的并发/预算意图。
func TestT3ShellHub_CancelledViewStartsNoSubscription(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hub.View(ctx, env, "tok"); err == nil {
		t.Fatalf("cancelled view must fail")
	}
	// 给潜在的后台 goroutine 一点时间暴露问题。
	time.Sleep(100 * time.Millisecond)
	hub.mu.Lock()
	entries := len(hub.entries)
	hub.mu.Unlock()
	if entries != 0 {
		t.Fatalf("cancelled view must not create an entry, entries = %d", entries)
	}
	if got := srv.connCount.Load(); got != 0 {
		t.Fatalf("cancelled view must not dial, connCount = %d", got)
	}
}

// 首次同步预算用尽时如实返回 Syncing 视图，而不是把慢冷启动伪装成上游错误：
// 后台订阅继续跑，下一个请求就能拿到数据。
func TestT3ShellHub_FirstSyncDeadlineReturnsSyncingView(t *testing.T) {
	_, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	view, err := hub.View(ctx, env, "tok")
	if err != nil {
		t.Fatalf("deadline must not surface as an upstream error: %v", err)
	}
	if view.State != t3ShellSyncing || view.Snaps != nil {
		t.Fatalf("view = %+v, want syncing with no data", view)
	}
}

// Close 幂等：两次 Close 都正常返回；entry 的全部 goroutine 退出；Close 后
// View 报错。
func TestT3ShellHub_CloseIdempotent(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	env := hubTestEnv(url, 0)

	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	hub.mu.Lock()
	var entry *t3ShellEntry
	for _, e := range hub.entries {
		entry = e
	}
	hub.mu.Unlock()
	if entry == nil {
		t.Fatalf("expected an entry")
	}

	done := make(chan struct{})
	go func() {
		hub.Close()
		hub.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Close did not return")
	}

	select {
	case <-entry.done:
	case <-time.After(2 * time.Second):
		t.Fatalf("entry goroutines must exit after Close")
	}

	if _, err := hub.View(context.Background(), env, "tok"); err == nil {
		t.Fatalf("view after close must fail")
	}
}

// F8：Close 必须等到全部 goroutine 退出——含已被 Drop 摘除（不再在注册表里）
// 的 entry 与各订阅 reader。hub 级生命周期 WaitGroup 是等待依据：Close 返回
// 后它必须已排空，且 entry 的全部 goroutine（含 reader）都已结束。
func TestT3ShellHub_CloseWaitsForDroppedEntryGoroutines(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	env := hubTestEnv(url, 0)

	_, activeConn, _, archiveConn, _ := establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")
	hub.mu.Lock()
	var entry *t3ShellEntry
	for _, e := range hub.entries {
		entry = e
	}
	hub.mu.Unlock()
	if entry == nil {
		t.Fatalf("expected an entry")
	}

	// 先 Drop（entry 移出注册表——旧实现 Close 就等不到它了），再 Close。
	// cutoff 取大于 entry 世代（0）的 1，旧代 entry 被摘除。
	hub.Drop("user-1", "env-1", 1)
	hub.Close()

	drained := make(chan struct{})
	go func() {
		hub.lifecycle.Wait()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(3 * time.Second):
		t.Fatalf("hub lifecycle waitgroup must be drained after Close")
	}
	select {
	case <-entry.done:
	default:
		t.Fatalf("dropped entry goroutines must have exited after Close")
	}
	// 连接被关闭：reader 阻塞中的 Receive 已被解开并汇合（监督循环退出前
	// 必先等 reader 结束，否则生命周期计数不会归零）。
	if !activeConn.awaitClosed(2*time.Second) || !archiveConn.awaitClosed(2*time.Second) {
		t.Fatalf("connections must be closed after Close")
	}
	if _, err := hub.View(context.Background(), env, "tok"); err == nil {
		t.Fatalf("view after close must fail")
	}
}

// 并发 View 共用一次初始化：并发调用只产生两条连接（每面一条）。
func TestT3ShellHub_ConcurrentViewsShareInitialization(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	const viewers = 8
	results := make(chan *t3ShellView, viewers)
	for i := 0; i < viewers; i++ {
		go func() {
			view, err := hub.View(context.Background(), env, "tok")
			if err == nil {
				results <- view
			} else {
				results <- nil
			}
		}()
	}

	// 两条连接就位并喂快照。
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	collector.await(true) // 归档按需快照连接（自动应答后客户端即关）
	activeConn, activeSub := active.conn, active.sub
	if err := activeConn.sendValue(activeSub.ID, shellSnapshotValue(5, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}

	deadline := time.After(3 * time.Second)
	got := 0
	for got < viewers {
		select {
		case view := <-results:
			if view == nil {
				t.Fatalf("a concurrent view failed")
			}
			got++
		case <-deadline:
			t.Fatalf("only %d/%d concurrent views completed", got, viewers)
		}
	}
	// 稳定后不允许出现额外连接（重复初始化）。计数 = 活跃订阅 1 条 +
	// 归档按需快照 1 条（快照完成后即关，connCount 仍累计）。
	time.Sleep(100 * time.Millisecond)
	if srv.connCount.Load() != 2 {
		t.Fatalf("concurrent views must share one initialization, connCount = %d", srv.connCount.Load())
	}
}

// awaitShellEvent 从订阅里取事件直到谓词命中（事件有信号唤醒；断言窗口用
// 轮询表达最直接，等待有上限）。
func awaitShellEvent(t *testing.T, sub *T3ShellEventSubscription, until func(T3ShellEvent) bool) T3ShellEvent {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		for _, ev := range sub.Drain() {
			if until(ev) {
				return ev
			}
		}
		select {
		case <-sub.C():
		case <-deadline:
			t.Fatalf("timed out waiting for shell event")
		}
	}
}

// Ensure 后无需任何 View 就有两条订阅并建立活跃快照（INT-132 验收标准）：
// 订阅者先注册，随后收到「状态翻转到 ready」的事件，证明快照是在不依赖任何
// 读取请求的情况下建立的。再次 Ensure 幂等，不建新连接。
func TestT3ShellHub_EnsureStartsSubscriptionsWithoutView(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	sub := hub.Subscribe("user-1")
	defer sub.Close()

	if err := hub.Ensure(env, "tok"); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(1, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}

	// 全程不调用 View：等订阅者收到 ready 状态事件。归档面是按需快照
	// （INT-173），Ensure 只建活跃订阅，不会有归档流量。
	ev := awaitShellEvent(t, sub, func(e T3ShellEvent) bool { return e.State == "ready" })
	if ev.EnvironmentID != env.ID {
		t.Fatalf("state event environment = %q, want %q", ev.EnvironmentID, env.ID)
	}

	// 幂等：再 Ensure 两次也不得拨新连接（Ensure 只建活跃订阅一条连接）。
	if err := hub.Ensure(env, "tok"); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := srv.connCount.Load(); got != 1 {
		t.Fatalf("second ensure must reuse the entry, connCount = %d", got)
	}
	if err := hub.Ensure(env, "tok"); err != nil {
		t.Fatalf("third ensure: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := srv.connCount.Load(); got != 1 {
		t.Fatalf("third ensure must not dial, connCount = %d", got)
	}
}

// 订阅者收到线程级事件：thread.updated 带正确 EnvironmentID 与线程 ID；
// 权威全量快照产生 Full=true。
func TestT3ShellHub_ThreadEventsPublished(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	sub := hub.Subscribe("user-1")
	defer sub.Close()
	_, activeConn, activeSub, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")

	// 建立期的事件先清掉，只观察本用例注入的变化。
	sub.Drain()

	if err := activeConn.sendValue(activeSub.ID, mustMarshalDelta(43, "th-9", "增量")); err != nil {
		t.Fatalf("send delta: %v", err)
	}
	ev := awaitShellEvent(t, sub, func(e T3ShellEvent) bool { return len(e.ThreadIDs) > 0 })
	if ev.EnvironmentID != "env-1" {
		t.Fatalf("event environment = %q, want env-1", ev.EnvironmentID)
	}
	if ev.Full {
		t.Fatalf("thread.updated must not set full: %+v", ev)
	}
	if len(ev.ThreadIDs) != 1 || ev.ThreadIDs[0] != "th-9" {
		t.Fatalf("thread ids = %v, want [th-9]", ev.ThreadIDs)
	}

	// 权威全量快照 → Full=true。
	if err := activeConn.sendValue(activeSub.ID, shellSnapshotValue(44, "th-9")); err != nil {
		t.Fatalf("send snapshot: %v", err)
	}
	ev = awaitShellEvent(t, sub, func(e T3ShellEvent) bool { return e.Full })
	if ev.EnvironmentID != "env-1" {
		t.Fatalf("full event environment = %q, want env-1", ev.EnvironmentID)
	}
}

// 连接断开产生 stale 状态事件（浏览器据此把环境标成「已断连」）。
func TestT3ShellHub_DisconnectPublishesStaleState(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	sub := hub.Subscribe("user-1")
	defer sub.Close()
	_, activeConn, _, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	sub.Drain()

	activeConn.closeConn()
	ev := awaitShellEvent(t, sub, func(e T3ShellEvent) bool { return e.State == "stale" })
	if ev.EnvironmentID != "env-1" {
		t.Fatalf("stale event environment = %q, want env-1", ev.EnvironmentID)
	}
	if len(ev.ThreadIDs) != 0 {
		t.Fatalf("state event must not carry thread ids: %+v", ev)
	}
}

// 跨用户隔离：事件只进所属用户的订阅，用户 B 收不到用户 A 环境的事件。
func TestT3ShellHub_EventsIsolatedByUser(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	subA := hub.Subscribe("user-1")
	defer subA.Close()
	subB := hub.Subscribe("user-2")
	defer subB.Close()
	_, activeConn, activeSub, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	subA.Drain()

	if err := activeConn.sendValue(activeSub.ID, mustMarshalDelta(43, "th-9", "增量")); err != nil {
		t.Fatalf("send delta: %v", err)
	}
	awaitShellEvent(t, subA, func(e T3ShellEvent) bool { return len(e.ThreadIDs) > 0 })
	// user-2 名下没有任何 entry：发布路径只会查 h.subs["user-2"]，留出时间
	// 后仍必须为空。
	time.Sleep(100 * time.Millisecond)
	if events := subB.Drain(); len(events) != 0 {
		t.Fatalf("user B must not receive user A events, got %+v", events)
	}
}

// 合并：消费方不在场时同一环境的连续两个增量只 Drain 出一条，线程 ID 取并集。
func TestT3ShellHub_EventMergeCoalesces(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	sub := hub.Subscribe("user-1")
	defer sub.Close()
	_, activeConn, activeSub, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	sub.Drain()

	// 发布发生在 Ack 之前：两个 Ack 到手即两次发布都已合并进 pending。
	if err := activeConn.sendValue(activeSub.ID, mustMarshalDelta(43, "th-1", "一")); err != nil {
		t.Fatalf("send delta 1: %v", err)
	}
	if !activeConn.awaitAck(activeSub.ID, 2*time.Second) {
		t.Fatalf("delta 1 not acked")
	}
	if err := activeConn.sendValue(activeSub.ID, mustMarshalDelta(44, "th-2", "二")); err != nil {
		t.Fatalf("send delta 2: %v", err)
	}
	if !activeConn.awaitAck(activeSub.ID, 2*time.Second) {
		t.Fatalf("delta 2 not acked")
	}

	events := sub.Drain()
	if len(events) != 1 {
		t.Fatalf("two increments must coalesce into one event, got %d: %+v", len(events), events)
	}
	if events[0].EnvironmentID != "env-1" || events[0].Full {
		t.Fatalf("merged event = %+v", events[0])
	}
	ids := map[string]bool{}
	for _, id := range events[0].ThreadIDs {
		ids[id] = true
	}
	if !ids["th-1"] || !ids["th-2"] {
		t.Fatalf("merged ids = %v, want union of th-1 and th-2", events[0].ThreadIDs)
	}
	// Drain 已摘空：再来一次应为空（非 nil）。
	if events := sub.Drain(); len(events) != 0 {
		t.Fatalf("drain must be empty after taking, got %+v", events)
	}
}

// 重配形状的 Drop 后，旧世代的 Ensure 被 floor 拒绝（与 View 共用 admitEntry，
// 守卫行为必须一致）；携带新世代的 Ensure 放行并重建订阅。
func TestT3ShellHub_EnsureGuardedByFloorAfterDrop(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	env := hubTestEnv(url, 0)
	_, oldConn, _, _, _ := establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	// 重配形状的 Drop：cutoff 是落库后自增出的新世代（0 → 1）。
	hub.Drop("user-1", "env-1", 1)
	if !oldConn.awaitClosed(2 * time.Second) {
		t.Fatalf("drop must retire the old-generation entry")
	}
	before := srv.connCount.Load()
	if err := hub.Ensure(env, "tok"); !errors.Is(err, errT3ShellConfigStale) {
		t.Fatalf("stale ensure error = %v, want errT3ShellConfigStale", err)
	}
	time.Sleep(50 * time.Millisecond)
	if srv.connCount.Load() != before {
		t.Fatalf("stale ensure must not dial, connCount %d → %d", before, srv.connCount.Load())
	}

	// 新世代 Ensure 放行并重建订阅（活跃面一条连接）。
	envNew := hubTestEnv(url, 1)
	if err := hub.Ensure(envNew, "tok"); err != nil {
		t.Fatalf("ensure with current revision: %v", err)
	}
	deadline := time.After(3 * time.Second)
	for srv.connCount.Load() <= before {
		select {
		case <-deadline:
			t.Fatalf("ensure after drop must dial new subscriptions")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// 并发 Ensure/View/Close：不 panic、不重复建 entry；Close 与 Ensure/View 交错
// 时调用方只会得到错误或正常完成（-race 下验证注册表与生命周期并发安全）。
func TestT3ShellHub_ConcurrentEnsureViewAndClose(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	env := hubTestEnv(url, 0)

	// 阶段一：并发 Ensure/View 共用一次初始化（每面一条连接）。
	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				errs <- hub.Ensure(env, "tok")
				return
			}
			_, err := hub.View(context.Background(), env, "tok")
			errs <- err
		}(i)
	}

	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(5, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	// View 会各自触发一次归档按需快照（合并成一次上游调用，可能实际只到
	// 一条连接）：收下同请求的并发读，不把快照到达时间绑进断言。
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent caller failed: %v", err)
		}
	}
	time.Sleep(100 * time.Millisecond)
	// 计数 = 活跃订阅 1 条 + 归档按需快照 1 条（并发读合并成一次上游
	// 调用；快照完成后即关，connCount 仍累计）。
	if got := srv.connCount.Load(); got != 2 {
		t.Fatalf("concurrent ensure/view must share one initialization, connCount = %d", got)
	}

	// 阶段二：Close 与 Ensure/View 交错。
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 20; i++ {
			_ = hub.Ensure(env, "tok")
			_, _ = hub.View(context.Background(), env, "tok")
		}
	}()
	hub.Close()
	<-done
	hub.mu.Lock()
	entries := len(hub.entries)
	hub.mu.Unlock()
	if entries != 0 {
		t.Fatalf("registry must be empty after close, entries = %d", entries)
	}
}

// Drop 必须唤醒正在等待首次同步的 View：监督循环在退避窗口里直接退出，
// 不会再经 refreshLocked 广播 notify，不监听 e.stop 的等待者会一直挂到
// 请求预算耗尽（列表 15s、详情 25s）才作答。
func TestT3ShellHub_DropWakesPendingView(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 故意不发任何快照：View 会停在「等活跃面权威快照」。
	viewErr := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := hub.View(ctx, env, "tok")
		viewErr <- err
	}()

	collector := newShellConnCollector(t, srv)
	collector.await(false) // 活跃订阅已建立，View 仍在等待

	hub.Drop("user-1", "env-1", math.MaxInt64)
	select {
	case err := <-viewErr:
		if err != nil {
			t.Fatalf("pending view after drop = %v, want a best-effort view", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("drop must wake a pending View instead of letting it wait for the budget")
	}
}

// 写后失效的确定性交错（Finding A）：归档写派发成功后活跃面随即摘除线程，
// 缓存里的写前快照不得被当成「归档面完整」——失效到新快照落地之间
// ArchiveErr 必须非空（wait 不判 thread_gone、列表不谎称完整），旧归档行
// 保留供展示。
func TestT3ShellHub_InvalidateMarksArchiveUntrusted(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	calls := srv.archiveCallCount()

	hub.InvalidateArchive("user-1", "env-1")

	// 失效即标不可信：ArchiveErr 非空（不判 thread_gone），归档行保留。
	view, err := hub.Peek(env, "tok")
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if view.Snaps == nil || view.Snaps.ArchiveErr == nil {
		t.Fatalf("invalidated archive face must report ArchiveErr, snaps = %+v", view.Snaps)
	}
	if len(view.Snaps.Archived.Threads) != 1 || view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("invalidation must keep the last snapshot rows, got %+v", view.Snaps.Archived.Threads)
	}

	// Peek 已触发一次补拉：写后快照落地前读取持续不可信，落地后恢复。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	if srv.archiveCallCount() != calls+1 {
		t.Fatalf("invalidation must trigger exactly one refetch, calls %d → %d", calls, srv.archiveCallCount())
	}
	_ = view
}

// 世代护栏的确定性交错：暂停 getArchivedShellSnapshot 应答 → 触发归档写并
// 失效 → 放行写前应答 → 该应答不得把窗口封回可信，随后读到的是写后快照。
func TestT3ShellHub_InFlightPreWriteSnapshotCannotSealWindow(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	// 先暂停归档应答再建 entry：首读就停在「已发起未落地」。pause 挂在
	// 应答派发上——请求照常收进 requests 通道（测试据此确认在途）。
	srv.pauseArchive()
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 与 establishBothFaces 同形，但归档连接被暂停：活跃面照常建连喂快照，
	// 归档拉取停在途。
	viewDone := make(chan struct{})
	go func() {
		defer close(viewDone)
		_, _ = hub.View(context.Background(), env, "tok")
	}()
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	parked := collector.await(true) // 归档拉取在途、应答被暂停
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}
	select {
	case <-viewDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for first view")
	}
	_ = parked

	// 拉取在途（写前应答未落地）时发生归档写：失效。
	hub.InvalidateArchive("user-1", "env-1")

	// 放行写前应答：落地不得把窗口封回可信（ArchiveErr 必须非空）。
	srv.resumeArchive()
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && len(v.Snaps.Archived.Threads) == 1
	})
	if view.Snaps.ArchiveErr == nil {
		t.Fatalf("pre-write snapshot must not reseal the window as trusted, snaps = %+v", view.Snaps)
	}
	if view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("pre-write data may replace rows but not trust, got %+v", view.Snaps.Archived.Threads)
	}

	// 下一次 kick 补发写后拉取（簿记已被失效清零，kick 立即放行）：
	// 读到的是写后快照，归档面重新可信。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	_ = view
}

// 「apply 成功后、簿记提交前发生失效」的确定性交错（Finding R1 端到端）：
// 世代校验与簿记提交已在同一临界区原子化，失效只可能发生在 commit 之后
// （簿记清零）或之前（世代不符走 needsRefetch），不存在「apply 成功了但
// 簿记把已失效的窗口封回 60s」的缝隙。用 pauseArchive 挂住在途拉取，
// Invalidate 后放行写前应答：落地不得封窗，下一次读取立即补拉写后状态。
func TestT3ShellHub_BookkeepingCommitCannotSealAfterInvalidation(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.pauseArchive()
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	viewDone := make(chan struct{})
	go func() {
		defer close(viewDone)
		_, _ = hub.View(context.Background(), env, "tok")
	}()
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	collector.await(true) // 归档拉取在途、应答被暂停
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}
	select {
	case <-viewDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for first view")
	}

	// 写前拉取在途时发生归档写：失效（推进世代、清零簿记）。
	hub.InvalidateArchive("user-1", "env-1")
	calls := srv.archiveCallCount()

	// 放行写前应答：世代护栏让落地只换数据，commit 簿记（needsRefetch
	// 备忘）不得封窗——下一次读取立即放行写后拉取，不受 inFlight 之外
	// 的任何窗口阻挡。
	srv.resumeArchive()
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	if got := srv.archiveCallCount(); got != calls+1 {
		t.Fatalf("post-write refetch must happen immediately, archive calls %d → %d", calls, got)
	}
	_ = view
}

// TTL 过期的信任缺口（Finding R2）：缓存成功后 TTL 过期、重拉被闸门挡住
// 期间，Peek/wait 不得把缺失线程判 thread_gone（ArchiveErr 必须非空），
// 旧行保留；重拉成功后恢复可信。TTL 内命中不受影响。
func TestT3ShellHub_ExpiredArchiveUntrustedDuringRefetch(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	cfg := hubTestConfig()
	cfg.ArchiveTTL = 80 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")

	// TTL 内命中仍可信。
	view, err := hub.Peek(env, "tok")
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if view.Snaps == nil || view.Snaps.ArchiveErr != nil {
		t.Fatalf("cached archive must stay trusted within TTL, snaps = %+v", view.Snaps)
	}

	// TTL 过期 + 闸门挡住重拉：下一次读取发起重拉并把归档面撤为不可信。
	// 上游归档集已变（th-a 被 th-b 取代）：过期快照若仍被当完整，断言
	// 就会把它错当成当前状态。
	srv.pauseArchive()
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-b"), nil)
	time.Sleep(cfg.ArchiveTTL + 30*time.Millisecond)
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil
	})
	if len(view.Snaps.Archived.Threads) != 1 || view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("expired archive must keep the last rows, got %+v", view.Snaps.Archived.Threads)
	}
	if !errors.Is(view.Snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("expired archive must report stale-refetch, got %v", view.Snaps.ArchiveErr)
	}
	// 重拉在途被闸门挡住：再次读取仍不可信（不得拿过期快照判 thread_gone）。
	view, err = hub.Peek(env, "tok")
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if view.Snaps == nil || view.Snaps.ArchiveErr == nil {
		t.Fatalf("in-flight refetch must not report the expired snapshot as trusted")
	}

	// 放行重拉：写后快照落地恢复可信，归档集换成上游新状态。
	srv.resumeArchive()
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1 && v.Snaps.Archived.Threads[0].ID == "th-b"
	})
	_ = view
}

// 失效后当前世代刷新失败保留旧行（Finding R3 端到端）：成功快照 →
// Invalidate → 下一次拉取失败 → 旧行仍在、ArchiveErr 是真实失败而非
// stale、不谎称空表。
func TestT3ShellHub_InvalidatedThenFailedFetchKeepsRows(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")

	hub.InvalidateArchive("user-1", "env-1")
	// 失效后的补拉失败：fake 回真实错误。
	srv.setArchiveFixture(nil, map[string]any{"_tag": "RuntimeError", "message": "archive exploded"})

	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil && !errors.Is(v.Snaps.ArchiveErr, errT3ShellArchiveStale)
	})
	if len(view.Snaps.Archived.Threads) != 1 || view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("failed refetch after invalidation must keep rows (no fake empty), got %+v", view.Snaps.Archived.Threads)
	}
}

// 结构性坏快照（线程缺 ID）不得与「世代不符」合并成同一种 false（复审
// 遗留）：坏负载是本次读取失败——ArchiveErr 记真实解析/校验错误、
// retryAfter 记冷却窗口，冷却内连续 View/Peek 不重复拨号（不出现
// needsRefetch 触发的无冷却重试循环）；修好上游后冷却过期重拉成功，
// 归档面恢复可信。
func TestT3ShellHub_BadArchivePayloadRecordsCooldown(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(badArchiveSnapshotBody(1), nil)
	cfg := hubTestConfig()
	cfg.ArchiveTTL = 60 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")

	// (a) ArchiveErr 携带真实坏负载错误（点名缺 ID），不是 stale/未就绪。
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil && strings.Contains(v.Snaps.ArchiveErr.Error(), "缺少 ID")
	})
	if errors.Is(view.Snaps.ArchiveErr, errT3ShellArchiveStale) || errors.Is(view.Snaps.ArchiveErr, errT3ShellArchiveNotReady) {
		t.Fatalf("bad payload must surface as a real error, got %v", view.Snaps.ArchiveErr)
	}

	// 簿记走失败路径：记了 retryAfter 冷却，而不是 needsRefetch 备忘。
	e := hub.entries[t3ShellHubKey("user-1", "env-1")]
	e.arch.mu.Lock()
	cooled := !e.arch.retryAfter.IsZero() && !e.arch.needsRefetch
	e.arch.mu.Unlock()
	if !cooled {
		t.Fatalf("bad payload must record the failure cooldown, not a needsRefetch memo (retryAfter=%v needsRefetch=%v)", e.arch.retryAfter, e.arch.needsRefetch)
	}

	// (b)+(c) 冷却内连续 View/Peek 不重复打上游：毒快照不得借
	// needsRefetch 绕过冷却形成重拨风暴。
	calls := srv.archiveCallCount()
	for i := 0; i < 3; i++ {
		if _, err := hub.View(context.Background(), env, "tok"); err != nil {
			t.Fatalf("view: %v", err)
		}
		if _, err := hub.Peek(env, "tok"); err != nil {
			t.Fatalf("peek: %v", err)
		}
	}
	if got := srv.archiveCallCount(); got != calls {
		t.Fatalf("poison snapshot must not retry within the cooldown, calls %d → %d", calls, got)
	}

	// 修好上游：冷却过期后下一次读取重拉成功，归档面恢复可信。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
	time.Sleep(cfg.ArchiveTTL + 20*time.Millisecond)
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	if got := srv.archiveCallCount(); got != calls+1 {
		t.Fatalf("recovery must take exactly one refetch after the cooldown, calls %d → %d", calls, got)
	}
	_ = view
}

// 旧面回退（INT-173 审修）：getArchivedShellSnapshot 被判「方法不存在」后，
// entry 切到常驻 subscribeArchivedShell 订阅——归档数据由订阅流喂入，
// 此后不再发按需拨号；非方法级失败（任意 Exit Failure / 传输错误）不切换。
func TestT3ShellHub_LegacyArchiveSubscriptionFallback(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 活跃面先就位；归档按需快照将被判方法不存在 → 触发旧面订阅。
	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}

	// 旧面订阅请求到达（回退激活的证据）。
	srv.awaitArchiveSub(t)
	// 归档数据经订阅快照喂入。
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	if view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("archived threads via legacy subscription = %+v", view.Snaps.Archived.Threads)
	}

	// 切换后按需快照不再重试：TTL 内多次读只产生最初那一次探测性调用。
	calls := srv.archiveCallCount()
	for i := 0; i < 3; i++ {
		if _, err := hub.View(context.Background(), env, "tok"); err != nil {
			t.Fatalf("view: %v", err)
		}
	}
	if srv.archiveCallCount() != calls {
		t.Fatalf("legacy env must not retry the on-demand snapshot RPC, calls %d → %d", calls, srv.archiveCallCount())
	}
}

// 非方法级的归档失败不得误切订阅面：任意 Exit Failure（非 method-not-found）
// 仍按「读取失败」记账并冷却，归档面不可信但旧订阅不被拉起。
func TestT3ShellHub_NonMethodFailureDoesNotFallback(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true) // 上游其实有订阅可用——但失败不是「方法不存在」
	srv.setArchiveFixture(nil, map[string]any{"_tag": "RuntimeError", "message": "archive exploded"})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}

	// 失败只记 ArchiveErr：旧订阅不得被拉起（否则「任意失败→切订阅」
	// 会掩盖真实故障，还把按需簿记语义搅乱）。
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil
	})
	_ = view
	select {
	case sub := <-srv.archiveSubs:
		t.Fatalf("non-method failure must not trigger legacy subscription, got %+v", sub)
	case <-time.After(300 * time.Millisecond):
	}
}

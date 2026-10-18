package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

	mu         sync.Mutex
	bearer     string // 会话端点与 WS 握手校验的 Bearer
	marker     bool   // getConfig.shellResumeCompletionMarker
	autoPong   bool   // 收到 {"_tag":"Ping"} 是否回 Pong
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
			if req.Method == t3RPCServerGetConfig {
				// 订阅前的一次性查询：按能力开关应答。
				config, _ := json.Marshal(map[string]any{
					"shellResumeCompletionMarker": s.currentMarker(),
					"settings":                    map[string]any{},
				})
				_ = sendExitSuccessFrame(conn, req.ID, config)
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
		t:        t,
		bearer:   opts.bearer,
		marker:   opts.marker,
		autoPong: opts.autoPong,
		connsCh:  make(chan *fakeShellConn, 64),
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

// hubTestConfig 是测试用小预算：退避/空闲/标记兜底都是毫秒级，读超时与
// Ping 间隔同样缩小以便在秒级用例里观察到重连与保活。
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
		IdleCheckInterval: 20 * time.Millisecond,
		IdleTTL:           150 * time.Millisecond,
	}
}

func hubTestEnv(address string, updatedAt time.Time) *model.T3Environment {
	return &model.T3Environment{
		ID:        "env-1",
		UserID:    "user-1",
		Name:      "T3",
		Address:   address,
		UpdatedAt: updatedAt,
	}
}

// classifiedShellConn 是已分派的连接与其订阅请求。
type classifiedShellConn struct {
	conn *fakeShellConn
	sub  fakeShellRequest
}

// shellConnCollector 收下连接并按面分派（连接到达顺序不定）：活跃连接先发
// getConfig 再发 subscribeShell；归档连接只发 subscribeArchivedShell。暂时
// 用不上的面先停泊，供后续 await 取用（重连用例的新连接也走这里）。
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
	case t3RPCSubscribeArchivedShell:
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

func archiveSnapshotValue(sequence int, threadIDs ...string) json.RawMessage {
	threads := []map[string]any{}
	for _, id := range threadIDs {
		threads = append(threads, map[string]any{
			"id": id, "projectId": "p-1", "title": "归档-" + id, "status": "idle",
			"archivedAt": "2026-10-01T00:00:00.000Z",
		})
	}
	data, _ := json.Marshal(map[string]any{
		"kind": "snapshot",
		"snapshot": map[string]any{
			"schemaVersion":    1,
			"snapshotSequence": sequence,
			"projects":         []map[string]any{{"id": "p-1", "title": "Fast Ship", "workspaceRoot": "/repo"}},
			"threads":          threads,
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

// establishBothFaces 走完一次完整初始化：两条连接就位、双面快照喂入并 Ack、
// View 到手 Ready。返回连接分派器与测试所需的句柄、订阅请求。
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
	if err := archiveConn.sendValue(archiveSub.ID, archiveSnapshotValue(1, "th-a")); err != nil {
		t.Fatalf("send archive snapshot: %v", err)
	}
	if !archiveConn.awaitAck(archiveSub.ID, 2*time.Second) {
		t.Fatalf("archive snapshot not acked")
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

// 活跃+归档两条连接并发建立；按 requestId Ack；快照进入组装视图。
func TestT3ShellHub_BothFacesSubscribeAckAndShareState(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

	_, activeConn, activeSub, archiveConn, archiveSub := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")

	// 冷启动订阅形状：活跃面带 requestCompletionMarker、无 afterSequence；
	// 归档订阅输入为空 {}（无 afterSequence，协议 ws.ts:1777-1811）。
	if activeSub.Payload["requestCompletionMarker"] != true {
		t.Fatalf("active payload = %+v, want requestCompletionMarker", activeSub.Payload)
	}
	if _, has := activeSub.Payload["afterSequence"]; has {
		t.Fatalf("cold start must not carry afterSequence: %+v", activeSub.Payload)
	}
	if len(archiveSub.Payload) != 0 {
		t.Fatalf("archive payload = %+v, want empty", archiveSub.Payload)
	}

	view, err := hub.View(context.Background(), env, "tok")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.State != t3ShellReady {
		t.Fatalf("state = %v", view.State)
	}
	if len(view.Snaps.Active.Threads) != 1 || view.Snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("active threads = %+v", view.Snaps.Active.Threads)
	}
	if len(view.Snaps.Archived.Threads) != 1 || view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("archived threads = %+v", view.Snaps.Archived.Threads)
	}
	if view.Snaps.ArchiveErr != nil {
		t.Fatalf("archiveErr = %v", view.Snaps.ArchiveErr)
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
	env := hubTestEnv(url, time.Now())
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
	if len(view.Snaps.Active.Projects) != 1 || view.Snaps.Active.Projects[0].ID != "p-1" {
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
	env := hubTestEnv(url, time.Now())

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

func mustMarshalDelta(sequence int, id, title string) json.RawMessage {
	data, _ := json.Marshal(map[string]any{
		"kind": "thread.updated", "sequence": sequence, "location": "active",
		"thread": map[string]any{"id": id, "projectId": "p-1", "title": title, "status": "idle"},
	})
	return data
}

// 服务端无完成标记能力：不请求 marker，恢复后不等标记立即视为追平。
func TestT3ShellHub_NoMarkerCapabilitySyncsImmediately(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: false, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

	_, activeConn, activeSub, _, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	if _, has := activeSub.Payload["requestCompletionMarker"]; has {
		t.Fatalf("cold start must not request marker when unsupported: %+v", activeSub.Payload)
	}

	collector := newShellConnCollector(t, srv)
	activeConn.closeConn()

	resumeSub := collector.await(false).sub
	if _, has := resumeSub.Payload["requestCompletionMarker"]; has {
		t.Fatalf("resume must not request marker when unsupported: %+v", resumeSub.Payload)
	}
	if fmt.Sprint(resumeSub.Payload["afterSequence"]) != "42" {
		t.Fatalf("afterSequence = %v, want 42", resumeSub.Payload["afterSequence"])
	}

	// 不发任何帧：能力不支持时立即视为追平 → View 应很快 Ready。
	ready := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool { return v.State == t3ShellReady })
	if len(ready.Snaps.Active.Threads) != 1 || ready.Snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("data must survive optimistic resume: %+v", ready.Snaps.Active.Threads)
	}
}

// 请求了标记但预算内未等到（F1）：不得把未追平的缓存提升为 Ready——超时后
// 置强制全量重同步并结束本连接，重连不带 afterSequence（上游会直接发权威
// 全量快照），权威帧到达后才回到 Ready。
func TestT3ShellHub_MarkerWaitTimeoutForcesFullResync(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	cfg := hubTestConfig()
	cfg.MarkerWaitTimeout = 300 * time.Millisecond
	// 等 marker 的窗口长于默认测试 IdleTTL，拉长以隔离回收行为（本用例的
	// 等待段没有 View 轮询为 entry 续命）。
	cfg.IdleTTL = 10 * time.Second
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

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

	// 权威全量快照到达 → applyActiveSnapshot 置追平 → Ready，游标被权威重置。
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
	cfg := hubTestConfig()
	// 空闲回收 TTL 拉长：本用例观察的是「无数据帧的连接靠 Pong 存活」，
	// 不能让回收器在观察窗口内把 entry 收走（回收行为由 IdleReclaim 覆盖）。
	cfg.IdleTTL = 10 * time.Second
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	// 空置超过 3 倍读超时：若无 Pong 刷新，连接早该判死重连。
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
	cfg := hubTestConfig()
	// 静默期（等读超时）长于默认测试 IdleTTL，拉长以隔离回收行为。
	cfg.IdleTTL = 10 * time.Second
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

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

// 归档面失败只影响归档面（G1）：清空归档面 + ArchiveErr，活跃面照常 Ready；
// 随后归档流独立重订阅并恢复。
func TestT3ShellHub_ArchiveFailureIndependentAndResubscribes(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

	_, activeConn, activeSub, archiveConn, archiveSub := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	archiveCollector := newShellConnCollector(t, srv)

	if err := archiveConn.sendExitFailure(archiveSub.ID, map[string]any{"_tag": "RuntimeError", "message": "archive stream exploded"}); err != nil {
		t.Fatalf("send archive failure: %v", err)
	}

	// 失败后：活跃面 Ready 不受影响；归档面清空并带 ArchiveErr。
	var view *t3ShellView
	var err error
	deadline := time.After(3 * time.Second)
	for {
		view, err = hub.View(context.Background(), env, "tok")
		if err != nil {
			t.Fatalf("view: %v", err)
		}
		if view.Snaps.ArchiveErr != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for archive failure to surface")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if view.State != t3ShellReady {
		t.Fatalf("active face must stay ready after archive failure, state = %v", view.State)
	}
	if len(view.Snaps.Archived.Threads) != 0 {
		t.Fatalf("failed archive face must be cleared, got %+v", view.Snaps.Archived.Threads)
	}

	// 归档流独立重订阅（活跃连接不动）。
	retry := archiveCollector.await(true)
	retryConn, retrySub := retry.conn, retry.sub
	if err := retryConn.sendValue(retrySub.ID, archiveSnapshotValue(2, "th-a", "th-b")); err != nil {
		t.Fatalf("send archive snapshot: %v", err)
	}
	if !retryConn.awaitAck(retrySub.ID, 2*time.Second) {
		t.Fatalf("retry archive snapshot not acked")
	}
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	if view.State != t3ShellReady || len(view.Snaps.Active.Threads) != 1 {
		t.Fatalf("active face drifted: state=%v threads=%+v", view.State, view.Snaps.Active.Threads)
	}
	_ = activeConn
	_ = activeSub
}

// F2：归档连接的任何终止（这里是无 Exit Failure、无解析错误的直接断开）
// 都必须撤销归档面可信度——ArchiveErr 置位、归档命中不再可信、活跃面照常
// Ready；归档重连并收到新全量快照后恢复。
func TestT3ShellHub_ArchiveConnClosureRevokesTrust(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

	_, activeConn, activeSub, archiveConn, _ := establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	archiveCollector := newShellConnCollector(t, srv)

	archiveConn.closeConn()

	var view *t3ShellView
	var err error
	deadline := time.After(3 * time.Second)
	for {
		view, err = hub.View(context.Background(), env, "tok")
		if err != nil {
			t.Fatalf("view: %v", err)
		}
		if view.Snaps.ArchiveErr != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("archive trust must be revoked after the archive conn died")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if view.State != t3ShellReady {
		t.Fatalf("active face must stay ready, state = %v", view.State)
	}
	if len(view.Snaps.Archived.Threads) != 0 {
		t.Fatalf("archive face must be cleared, got %+v", view.Snaps.Archived.Threads)
	}
	if len(view.Snaps.Active.Threads) != 1 || view.Snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("active threads drifted: %+v", view.Snaps.Active.Threads)
	}

	// 归档独立重订阅（活跃连接不动）并收到新快照 → 恢复可信。
	retry := archiveCollector.await(true)
	if err := retry.conn.sendValue(retry.sub.ID, archiveSnapshotValue(2, "th-a")); err != nil {
		t.Fatalf("send archive snapshot: %v", err)
	}
	if !retry.conn.awaitAck(retry.sub.ID, 2*time.Second) {
		t.Fatalf("retry archive snapshot not acked")
	}
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	if view.State != t3ShellReady || len(view.Snaps.Active.Threads) != 1 {
		t.Fatalf("active face drifted after archive recovery: state=%v", view.State)
	}
	_ = activeSub
	_ = activeConn
}

// 凭证失效：探测 repair → View 给 Failed 且错误可归类为凭证被拒。
func TestT3ShellHub_CredentialRejectionSurfacesAsFailed(t *testing.T) {
	_, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok-2", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

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

// 凭证替换：指纹变化且版本更新 → 停旧建新；版本不新的迟到请求被拒绝。
func TestT3ShellHub_CredentialReplacementRestartsEntry(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok-1", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	v1 := time.Now()
	env1 := hubTestEnv(url, v1)
	collector, activeConn, _, _, _ := establishBothFaces(t, hub, srv, env1, "tok-1", 42, "th-1")

	// 迟到请求：同版本不同凭证 → 拒绝（errT3ShellConfigStale），不打断现有 entry。
	_, err := hub.View(context.Background(), env1, "tok-rotated")
	if !errors.Is(err, errT3ShellConfigStale) {
		t.Fatalf("stale-request error = %v, want errT3ShellConfigStale", err)
	}

	// 凭证替换：新版本 + 新 token → 停旧建新，新 entry 用新 token 建连。
	srv.setBearer("tok-2")
	env2 := hubTestEnv(url, v1.Add(time.Second))
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
	if e == nil || !e.version.Equal(env2.UpdatedAt) {
		t.Fatalf("registry must hold the replacement entry, got %+v", e)
	}
}

// Drop 与迟到 View 交错：Drop 后旧版本的 View 被拒绝，且不复活 entry。
func TestT3ShellHub_DropGuardsAgainstLateViews(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	v1 := time.Now()
	env := hubTestEnv(url, v1)
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	hub.Drop("user-1", "env-1", v1.Add(time.Second))
	// 迟到 View（版本不晚于 floor）→ 拒绝且不建连。
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

	// 新版本 View 正常重建（喂入新连接的快照）。
	envNew := hubTestEnv(url, v1.Add(2*time.Second))
	_, _, _, _, _ = establishBothFaces(t, hub, srv, envNew, "tok", 2, "th-1b")
}

// F3：迟到的 Drop(旧版本) 不得关掉新代 entry——重配提交后 View 已用新凭证
// 重建 entry 时，后到的 Drop 只应记 floor，不能把新 entry 关掉。
func TestT3ShellHub_DropKeepsNewerEntry(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	v1 := time.Now()
	env := hubTestEnv(url, v1)
	_, oldConn, _, _, _ := establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	// floor ≥ entry 版本：旧代 entry 被关闭（原语义保持）。
	hub.Drop("user-1", "env-1", v1)
	if !oldConn.awaitClosed(2 * time.Second) {
		t.Fatalf("drop with floor >= entry version must retire the entry")
	}

	// 新代 entry：View 用更高版本重建。
	envNew := hubTestEnv(url, v1.Add(time.Second))
	_, newConn, newSub, _, _ := establishBothFaces(t, hub, srv, envNew, "tok", 2, "th-1b")

	// 迟到的 Drop(v1)：新 entry 版本晚于 floor → 保留，连接照常可用。
	hub.Drop("user-1", "env-1", v1)
	hub.mu.Lock()
	e := hub.entries[t3ShellHubKey("user-1", "env-1")]
	hub.mu.Unlock()
	if e == nil || !e.version.Equal(envNew.UpdatedAt) {
		t.Fatalf("late drop must keep the newer entry, got %+v", e)
	}
	if err := newConn.sendValue(newSub.ID, mustMarshalDelta(3, "th-1b", "仍活着")); err != nil {
		t.Fatalf("newer entry connection must survive late drop: %v", err)
	}
	if !newConn.awaitAck(newSub.ID, 2*time.Second) {
		t.Fatalf("newer entry connection must still ack after late drop")
	}
}

// F3：Delete 用删除时刻做 floor——它晚于任何在途请求读到的版本，迟到 View
// （哪怕带着删除前刚读出的行）一律被拒且不建 entry。
func TestT3ShellHub_DeleteNowFloorBlocksAllInFlightVersions(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	// 环境行的版本是「删除前」读出的时刻（过去）；删除发生在其后。
	env := hubTestEnv(url, time.Now().Add(-time.Hour))
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	hub.Drop("user-1", "env-1", time.Now().UTC())
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

// F3 补充：floor 的保留按**记录时刻**计算，不按 floor 值——很久以前的版本
// 做的 floor 也要立即生效；若按值清理（版本早于 1 小时前即清），长期未动
// 的环境一重配 floor 就会被清掉、守卫失效。
func TestT3ShellHub_FloorRetentionKeyedByRecordTime(t *testing.T) {
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()

	// 无 entry 的环境：Drop 直接记 floor，floor 版本是「很久前读出的行」。
	oldVersion := time.Now().UTC().Add(-2 * time.Hour)
	hub.Drop("user-2", "env-old", oldVersion)
	hub.mu.Lock()
	_, hasFloor := hub.floors[t3ShellHubKey("user-2", "env-old")]
	hub.mu.Unlock()
	if !hasFloor {
		t.Fatalf("freshly recorded floor must not be GC'd even when its version is old")
	}
	// env 的 user/env ID 与 Drop 的 key 一致（hubTestEnv 硬编码 user-1/env-1，
	// 不能用）；地址不可达也无所谓——floor 守卫在任何拨号之前生效。
	env := &model.T3Environment{ID: "env-old", UserID: "user-2", Address: "http://unused.example", UpdatedAt: oldVersion}
	if _, err := hub.View(context.Background(), env, "tok"); !errors.Is(err, errT3ShellConfigStale) {
		t.Fatalf("old floor must still guard, err = %v", err)
	}
}

// 首次同步预算用尽时如实返回 Syncing 视图，而不是把慢冷启动伪装成上游错误：
// 后台订阅继续跑，下一个请求就能拿到数据。
func TestT3ShellHub_FirstSyncDeadlineReturnsSyncingView(t *testing.T) {
	_, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

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

// 空闲两级回收：超过 IdleDisconnect 先断连保值（内存快照还在，View 返回
// Stale），下次 View 唤醒并用 afterSequence 增量恢复。
func TestT3ShellHub_IdlePauseResumesWithAfterSequence(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	cfg := hubTestConfig()
	cfg.IdleDisconnect = 60 * time.Millisecond
	cfg.IdleTTL = 30 * time.Second
	cfg.IdleCheckInterval = 15 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

	collector, _, _, _, _ := establishBothFaces(t, hub, srv, env, "tok", 5, "th-1")

	// 不再 View：等暂停生效（超 IdleDisconnect + 几个 tick）。
	time.Sleep(cfg.IdleDisconnect + 4*cfg.IdleCheckInterval)

	view, err := hub.View(context.Background(), env, "tok")
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	if view.State != t3ShellStale {
		t.Fatalf("paused view state = %v, want stale", view.State)
	}
	if view.Snaps == nil || len(view.Snaps.Active.Threads) != 1 || view.Snaps.Active.Threads[0].ID != "th-1" {
		t.Fatalf("pause must keep the in-memory snapshot, got %+v", view.Snaps)
	}

	// 唤醒后重连必须带恢复游标（不是从零重拉全量）。
	resumed := collector.await(false)
	if got := resumed.sub.Payload["afterSequence"]; got != float64(5) {
		t.Fatalf("resume payload = %+v, want afterSequence=5", resumed.sub.Payload)
	}
	if err := resumed.conn.sendValue(resumed.sub.ID, shellSnapshotValue(9, "th-1", "th-2")); err != nil {
		t.Fatalf("send resumed snapshot: %v", err)
	}
	if !resumed.conn.awaitAck(resumed.sub.ID, 2*time.Second) {
		t.Fatalf("resumed snapshot not acked")
	}
	ready := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool { return v.State == t3ShellReady })
	if len(ready.Snaps.Active.Threads) != 2 {
		t.Fatalf("active threads after resume = %+v", ready.Snaps.Active.Threads)
	}
}

// F4：setConnCancel 与暂停/停止交错——在已暂停（或已停止）的 entry 上登记
// 连接必须立刻取消它，否则监督循环「过 waitIfPaused 闸后再登记」的窗口会让
// reaper 漏掉这条连接（挂到空闲淘汰为止）。
func TestT3ShellEntry_SetConnCancelDuringPauseOrStop(t *testing.T) {
	assertCancelled := func(t *testing.T, name string, cancelled chan struct{}) {
		t.Helper()
		select {
		case <-cancelled:
		default:
			t.Fatalf("%s: connection registered in this state must be cancelled immediately", name)
		}
	}

	// 已暂停：登记即取消。
	paused := &t3ShellEntry{wake: make(chan struct{}), stop: make(chan struct{})}
	paused.mu.Lock()
	paused.paused = true
	paused.mu.Unlock()
	cancelled := make(chan struct{})
	paused.setConnCancel(false, func() { close(cancelled) })
	assertCancelled(t, "paused", cancelled)

	// 已停止：同样立即取消。
	stopped := &t3ShellEntry{wake: make(chan struct{}), stop: make(chan struct{})}
	stopped.shutdown()
	cancelled2 := make(chan struct{})
	stopped.setConnCancel(true, func() { close(cancelled2) })
	assertCancelled(t, "stopped", cancelled2)

	// 正常路径：活的 entry 上登记不触发取消。
	live := &t3ShellEntry{wake: make(chan struct{}), stop: make(chan struct{})}
	cancelled3 := make(chan struct{})
	live.setConnCancel(false, func() { close(cancelled3) })
	select {
	case <-cancelled3:
		t.Fatalf("live entry: connection must not be cancelled on registration")
	default:
	}
}

// F4：reaper 的决策必须使用同一临界区内采样的 lastUsed——刚被 View 刷新过
// lastUsed 的 entry 不得被旧采样误暂停/误淘汰；确实空闲时同一决策会取消
// 两条连接。
func TestT3ShellHub_ReapTickRespectsFreshLastUsed(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	cfg := hubTestConfig()
	cfg.IdleDisconnect = 50 * time.Millisecond
	cfg.IdleTTL = 10 * time.Second
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

	_, activeConn, activeSub, archiveConn, _ := establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")
	hub.mu.Lock()
	var entry *t3ShellEntry
	for _, candidate := range hub.entries {
		entry = candidate
	}
	hub.mu.Unlock()
	if entry == nil {
		t.Fatalf("expected an entry")
	}

	// lastUsed 刚被 View 刷新（视作空闲阈值的远未来）：单次决策不暂停也不
	// 淘汰，连接照常可用。
	hub.mu.Lock()
	entry.lastUsed = time.Now().Add(time.Hour)
	hub.mu.Unlock()
	if hub.reapTick(entry) {
		t.Fatalf("fresh entry must not be evicted")
	}
	if err := activeConn.sendValue(activeSub.ID, mustMarshalDelta(2, "th-1", "still-alive")); err != nil {
		t.Fatalf("fresh entry connection must stay alive: %v", err)
	}
	if !activeConn.awaitAck(activeSub.ID, 2*time.Second) {
		t.Fatalf("fresh entry connection must still ack")
	}

	// lastUsed 拨回超过 IdleDisconnect：同一临界区决策出暂停，两条连接被取消；
	// entry 保留（淘汰要等 IdleTTL）。
	hub.mu.Lock()
	entry.lastUsed = time.Now().Add(-cfg.IdleDisconnect - time.Second)
	hub.mu.Unlock()
	if hub.reapTick(entry) {
		t.Fatalf("idle-disconnect tick must not evict")
	}
	if !activeConn.awaitClosed(2*time.Second) || !archiveConn.awaitClosed(2*time.Second) {
		t.Fatalf("pause must cancel both connections")
	}
	hub.mu.Lock()
	_, stillRegistered := hub.entries[t3ShellHubKey("user-1", "env-1")]
	hub.mu.Unlock()
	if !stillRegistered {
		t.Fatalf("paused entry must stay registered until IdleTTL")
	}
}

// 空闲回收：超过 IdleTTL 没 View 的 entry 自停并摘除；再次 View 正常重建。
func TestT3ShellHub_IdleReclaim(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	cfg := hubTestConfig()
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, time.Now())

	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 1, "th-1")

	// 不再 View：等待 idleTTL + 检查周期 + 退避余量。
	time.Sleep(cfg.IdleTTL + 3*cfg.IdleCheckInterval)

	hub.mu.Lock()
	entries := len(hub.entries)
	hub.mu.Unlock()
	if entries != 0 {
		t.Fatalf("idle entry must be reclaimed, entries = %d", entries)
	}

	// 回收后的 View 重建订阅（喂入新连接的快照）。
	before := srv.connCount.Load()
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 2, "th-1b")
	if srv.connCount.Load() <= before {
		t.Fatalf("view after reclaim must dial new subscriptions, connCount %d → %d", before, srv.connCount.Load())
	}
}

// Close 幂等：两次 Close 都正常返回；entry 的全部 goroutine 退出；Close 后
// View 报错。
func TestT3ShellHub_CloseIdempotent(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	env := hubTestEnv(url, time.Now())

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
	env := hubTestEnv(url, time.Now())

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
	hub.Drop("user-1", "env-1", time.Now().Add(time.Hour))
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
	env := hubTestEnv(url, time.Now())

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
	archive := collector.await(true)
	activeConn, activeSub := active.conn, active.sub
	archiveConn, archiveSub := archive.conn, archive.sub
	if err := activeConn.sendValue(activeSub.ID, shellSnapshotValue(5, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if err := archiveConn.sendValue(archiveSub.ID, archiveSnapshotValue(1, "th-a")); err != nil {
		t.Fatalf("send archive snapshot: %v", err)
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
	// 稳定后不允许出现额外连接（重复初始化）。
	time.Sleep(100 * time.Millisecond)
	if srv.connCount.Load() != 2 {
		t.Fatalf("concurrent views must share one initialization, connCount = %d", srv.connCount.Load())
	}
}

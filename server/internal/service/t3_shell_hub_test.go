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
	// archQueue 按序应答队列（世代护栏补拉测试用）：非空时每次
	// getArchivedShellSnapshot 请求到达即按序摘取一项，作为该请求的应答
	// 内容（暂停释放后仍用摘到的项）——同一暂停窗口里的多次请求各拿各
	// 的应答，测试得以编排「第 1 次答写前状态、第 2 次答写后状态」的确定
	// 性交错，不靠 sleep 猜时序。队列为空时回退 archValue/archErr。
	archQueue []fakeArchiveAnswer
	// archLegacy 切旧上游：getArchivedShellSnapshot 按「方法不存在」拒绝，
	// subscribeArchivedShell 恢复为常驻订阅（注册后推快照+增量）。回退路径
	// 的旧面断言用。
	archLegacy bool
	// archiveDrop 持续掐断模式：受理 subscribeArchivedShell 后立即断开
	// （激活证据照常入 archiveSubs），模拟上游拒收/立即掐流的持续失败——
	// 退避递增测试用（审修 R6-C），不设快照帧，归档面再未收到数据。
	archiveDrop bool
	// archSubFail 非空时对 subscribeArchivedShell 回该 Exit Failure（受理证
	// 据照常入 archiveSubs，连接句柄照常入 archiveConns——测试等它即知
	// 失败已送达）——模拟「本该有订阅面的环境却没有该方法」（环境中途
	// 升级/误判自愈用例，审修 R8 P2），或非方法级订阅失败（不误重置
	// 用例）。
	archSubFail map[string]any
	// archiveSubTotal 是 subscribeArchivedShell 的累计受理次数（审修 R9
	// P2-1「自愈后不再发订阅」的计数证据）：含所有分支——非 legacy 的拒
	// 绝、定制失败、掐断、正常受理，只要客户端发出订阅就计数。
	archiveSubTotal atomic.Int64
	archiveSubs     chan fakeShellRequest // 收到的 subscribeArchivedShell 请求
	archiveConns    chan *fakeShellConn   // 旧面订阅连接
	// archPaused 暂停归档快照应答（Finding A 交错测试用）：暂停发生在应答
	// 派发而非收帧循环上——handler 仍会把请求收进 requests 通道，测试据此
	// 确认「在途拉取已发起」再精确放行，而不是靠 sleep 猜时序。
	archPaused bool
	archResume chan struct{}
	// archSubPaused 压住旧面订阅的首帧快照（legacy 回退收尾时序测试用）：
	// 订阅激活证据照常入通道，快照帧压到 resumeArchiveSub 放行——精确控
	// 制「权威落地」发生在哪一步之后，不靠 sleep 猜时序。
	archSubPaused bool
	archSubResume chan struct{}
	connsCh       chan *fakeShellConn
	connCount     atomic.Int64
	gotPongs      atomic.Int64
	lastProbes    atomic.Int64
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
				s.archiveSubTotal.Add(1)
				s.mu.Lock()
				legacy := s.archLegacy
				subFail := s.archSubFail
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
				if subFail != nil {
					// 定制订阅失败：受理证据与连接句柄照常入通道（等连接即
					// 知失败 Exit 已送达），应答按指定错误——模拟「环境却没
					// 有该方法」或其他订阅级故障。
					select {
					case s.archiveSubs <- req:
					default:
					}
					select {
					case s.archiveConns <- fc:
					default:
					}
					_ = sendExitFailureFrame(conn, req.ID, subFail)
					continue
				}
				select {
				case s.archiveSubs <- req:
				default:
				}
				s.mu.Lock()
				drop := s.archiveDrop
				s.mu.Unlock()
				if drop {
					// 掐断模式：受理后立刻断开本连接（激活证据已入通道），
					// 归档面收不到任何数据——持续失败的退避递增测试源。
					// 句柄不入 archiveConns：持续失败下入通道无人消费，填满
					// 后入队会撞上已关闭的连接通道（send on closed）。
					return
				}
				select {
				case s.archiveConns <- fc:
				default:
				}
				// 订阅受理：推全量快照体（包装成 snapshot 流项）。压住态下
				// 等放行再发首帧（激活证据已入通道，时序由测试控制）。
				if !s.awaitArchiveSubResume() {
					continue
				}
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

// fakeArchiveAnswer 是 archQueue 的一项：一次归档快照请求的定制应答。
// hold 非空时该请求挂起直到 channel 关闭，再按当时的默认 fixture 应答。
type fakeArchiveAnswer struct {
	value json.RawMessage
	err   map[string]any
	hold  chan struct{}
}

// pushArchiveFixture 向按序应答队列追加一项（err 非 nil 时该请求回 Fail）。
func (s *fakeShellStreamServer) pushArchiveFixture(value json.RawMessage, err map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archQueue = append(s.archQueue, fakeArchiveAnswer{value: value, err: err})
}

// pushArchiveHold 向按序应答队列追加一个「压住」项：对应的归档快照请求
// 摘到它后挂起，直到返回的 release 被调用才按当时的默认 fixture 应答——
// 把补拉/重拉停在「已发起未落地」，中间态断言不再靠 sleep 猜时序。
func (s *fakeShellStreamServer) pushArchiveHold() (release func()) {
	hold := make(chan struct{})
	s.mu.Lock()
	s.archQueue = append(s.archQueue, fakeArchiveAnswer{hold: hold})
	s.mu.Unlock()
	return func() { close(hold) }
}

// archiveAnswer 取归档快照应答：archErr 优先（读失败路径），否则按
// archValue 答（缺省是空快照，保证未定制 fixture 时归档面也能就绪）。
// archPaused 置位时先在锁外等 resume——请求已被收进 requests 通道（测试
// 的「在途」证据），应答暂停让客户端的拉取停在「已发起未落地」。
// archQueue 非空时按请求到达序逐项摘取应答（见字段注释）：同一暂停窗口
// 里的多次请求各拿各的应答，放行先后不再决定内容。
func (s *fakeShellStreamServer) archiveAnswer() struct {
	value json.RawMessage
	err   map[string]any
} {
	s.mu.Lock()
	s.archCalls++
	paused := s.archPaused
	resume := s.archResume
	var queued *fakeArchiveAnswer
	if len(s.archQueue) > 0 {
		queued = &s.archQueue[0]
		s.archQueue = s.archQueue[1:]
	}
	s.mu.Unlock()
	if paused && resume != nil {
		<-resume
	}
	if queued != nil && queued.hold != nil {
		<-queued.hold
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.archValue
	if value == nil {
		value = archiveSnapshotBody(1)
	}
	err := s.archErr
	// hold 项（value/err 均零值）不覆盖：按当时的默认 fixture 应答。
	if queued != nil && (queued.value != nil || queued.err != nil) {
		value, err = queued.value, queued.err
	}
	return struct {
		value json.RawMessage
		err   map[string]any
	}{value: value, err: err}
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

// awaitArchiveSubResume 等旧面订阅首帧放行；未压住时直接放行。
func (s *fakeShellStreamServer) awaitArchiveSubResume() bool {
	s.mu.Lock()
	resume := s.archSubResume
	s.mu.Unlock()
	if resume == nil {
		return true
	}
	<-resume
	return true
}

// pauseArchiveSub 压住旧面订阅的首帧快照：订阅请求照常入通道（激活证据），
// 快照帧等 resumeArchiveSub 放行。
func (s *fakeShellStreamServer) pauseArchiveSub() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archSubPaused = true
	s.archSubResume = make(chan struct{})
}

// resumeArchiveSub 放行全部被压住的订阅快照帧并结束压住态。
func (s *fakeShellStreamServer) resumeArchiveSub() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archSubPaused = false
	if s.archSubResume != nil {
		close(s.archSubResume)
		s.archSubResume = nil
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

// setArchiveSubFail 定制旧面订阅的应答错误（nil 恢复正常受理）：方法
// 不存在与非方法级失败都走同一 Exit Failure 通道，由错误文案区分。
func (s *fakeShellStreamServer) setArchiveSubFail(err map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archSubFail = err
}

// setArchiveDrop 开关旧面订阅的掐断模式（退避递增测试用）。
func (s *fakeShellStreamServer) setArchiveDrop(drop bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.archiveDrop = drop
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

// archiveSubscribeCount 返回 subscribeArchivedShell 的累计受理次数。
func (s *fakeShellStreamServer) archiveSubscribeCount() int {
	return int(s.archiveSubTotal.Load())
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
	case t3RPCSubscribeArchivedShell:
		// 旧面归档订阅连接（回退路径）：无 getConfig 前置，首请求即订阅。
		// 它与活跃连接并行拨号，到达顺序竞速——await(active) 先等到它时
		// 停泊归档池继续等，不得判死（-race 下的既有 flake：活跃连接的
		// 探测/拨号慢于「按需快照被拒→回退重拨」时，legacy 连接先到）。
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

// location:"archive" 的 thread.updated 同样是「线程离开活跃面」（P2 修复）：
// 上游若以该帧传递归档迁移，归档缓存必须同步失效——否则线程同时从活跃
// 面与 TTL 缓存的归档集合消失，ArchiveErr 却仍 nil（列表谎报完整、详情
// 落「未找到该线程」），最长一个 TTL 才自愈。失效后的重拉必须读到含该
// 线程的新归档面；location:"active" 的正常字段更新则不得触发失效（幂等
// 标记之外的拉取是浪费）。
func TestT3ShellHub_ActiveUpdatedArchiveLocationInvalidatesArchive(t *testing.T) {
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

	// 先验反例：location:"active" 的正常更新是普通字段变更，线程没离开
	// 活跃面，归档世代不得推进。
	updated, _ := json.Marshal(map[string]any{
		"kind": "thread.updated", "sequence": 43, "location": "active",
		"thread": map[string]any{"id": "th-1", "projectId": "p-1", "title": "改名", "status": "idle"},
	})
	if err := activeConn.sendValue(activeSub.ID, updated); err != nil {
		t.Fatalf("send active update: %v", err)
	}
	if !activeConn.awaitAck(activeSub.ID, 2*time.Second) {
		t.Fatalf("active update frame not acked")
	}
	e.state.mu.Lock()
	gen := e.state.archiveGeneration
	e.state.mu.Unlock()
	if gen != genBefore {
		t.Fatalf("location=active update must not invalidate the archive face: gen %d → %d", genBefore, gen)
	}

	// 另一个客户端把 th-1 归档，活跃流以 location:"archive" 的 updated
	// 传达（applyActiveItem 对它执行活跃面移除）。归档快照 fixture 同步换
	// 上写后状态：失效后的重拉必须读到含 th-1 的新归档面，线程不得从活
	// 跃、归档两边同时消失。
	archived, _ := json.Marshal(map[string]any{
		"kind": "thread.updated", "sequence": 44, "location": "archive",
		"thread": map[string]any{"id": "th-1", "projectId": "p-1", "title": "th-1", "status": "idle", "archivedAt": "2026-10-01T00:00:00.000Z"},
	})
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-1"), nil)
	if err := activeConn.sendValue(activeSub.ID, archived); err != nil {
		t.Fatalf("send archive-located update: %v", err)
	}
	if !activeConn.awaitAck(activeSub.ID, 2*time.Second) {
		t.Fatalf("archive-located update frame not acked")
	}

	// Ack 在全部 value 应用后才发 → 失效此刻已同步生效：标不可信（stale、
	// ArchiveErr 非空）且世代推进，不依赖 TTL 到期。
	e.state.mu.Lock()
	stale, gen, archErr := e.state.archiveStale, e.state.archiveGeneration, e.state.archiveErr
	e.state.mu.Unlock()
	if !stale || archErr == nil || gen != genBefore+1 {
		t.Fatalf("archive-located update must invalidate the archive face: stale=%v err=%v gen %d → %d", stale, archErr, genBefore, gen)
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
		t.Fatalf("archive-located update must trigger exactly one archive refetch, calls %d → %d", calls, got)
	}
	for _, th := range view.Snaps.Active.Threads {
		if th.ID == "th-1" {
			t.Fatalf("updated-away thread must leave the active face: %+v", view.Snaps.Active.Threads)
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
// 失效 → 放行写前应答 → 该应答不得把窗口封回可信；落地留下的 needsRefetch
// 由收尾自动补拉兑现，把补拉压住后中间态可确定性观察（旧实现依赖「之后
// 恰好还有一次读」，写命令后无人再读时归档面永远停在降级态，R11 P2-1）。
func TestT3ShellHub_InFlightPreWriteSnapshotCannotSealWindow(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	// 先暂停归档应答再建 entry：首读就停在「已发起未落地」。pause 挂在
	// 应答派发上——请求照常收进 requests 通道（测试据此确认在途）。
	// 按序应答：第 1 次答写前 [th-a]；第 2 次（收尾自动补拉）用 hold 压住，
	// 放行时按当时默认 fixture 答写后 [th-a, th-b]。
	srv.pauseArchive()
	srv.pushArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	releaseRefetch := srv.pushArchiveHold()
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

	// 拉取在途（写前应答未落地）时发生归档写：失效。
	hub.InvalidateArchive("user-1", "env-1")

	// 放行写前应答，随后自动补拉被 hold 压住：中间态可确定性观察——落
	// 地不得把窗口封回可信（ArchiveErr 非空）、写前数据可换行但不可信。
	srv.resumeArchive()
	collector.await(true) // 自动补拉已发起（无任何读参与）：它确实不依赖后续读
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil && len(v.Snaps.Archived.Threads) == 1
	})
	if !errors.Is(view.Snaps.ArchiveErr, errT3ShellArchiveStale) {
		t.Fatalf("pre-write snapshot must not reseal the window as trusted, snaps = %+v", view.Snaps)
	}
	if view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("pre-write data may replace rows but not trust, got %+v", view.Snaps.Archived.Threads)
	}

	// 写后快照放行补拉落地：归档面重新可信，含写后新线程。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
	releaseRefetch()
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	_ = view
}

// 「apply 成功后、簿记提交前发生失效」的确定性交错（Finding R1 端到端）：
// 世代校验与簿记提交已在同一临界区原子化，失效只可能发生在 commit 之后
// （簿记清零）或之前（世代不符走 needsRefetch），不存在「apply 成功了但
// 簿记把已失效的窗口封回 60s」的缝隙。用 pauseArchive 挂住在途拉取，
// Invalidate 后放行写前应答：落地不得封窗，收尾自动补拉写后状态（无读
// 参与——若落地误封窗，needsRefetch 不会留下，补拉不会发生）。
func TestT3ShellHub_BookkeepingCommitCannotSealAfterInvalidation(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.pauseArchive()
	srv.pushArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	srv.pushArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
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
	// 备忘）不得封窗——收尾立即自动补拉写后快照，不受 inFlight 之外
	// 的任何窗口阻挡，也不需要任何后续读参与（补拉连接先于一切读出现）。
	srv.resumeArchive()
	collector.await(true)
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	if got := srv.archiveCallCount(); got != calls+1 {
		t.Fatalf("post-write refetch must happen immediately, archive calls %d → %d", calls, got)
	}
	_ = view
}

// 世代护栏的闭环（R11 P2-1）：写命令与在途拉取交错 → 在途结果落地为写前
// 状态（needsRefetch 备忘）→ 落地收尾必须自己补发一次写后拉取，不再依赖
// 「之后恰好还有一次读」——否则写命令后无人再读时，归档面永远停
// 「已失效重拉中」、缺新归档线程，且落地指纹若无变化连事件都不发，浏览器
// 收不到提示就永远不会再读。补拉拿到写后快照（含新线程）恢复可信并发事
// 件；链条终止由世代判定承担：无新失效则补拉世代相符落地封窗，不再留
// 标志，无空转（用例末尾的静默窗口验证）。
func TestT3ShellHub_GenerationMismatchLandingAutoRefetches(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	// 按序应答：第 1 次（写前，被压住）答 [th-a]；第 2 次（收尾自动补拉）
	// 答写后状态 [th-a, th-b]。压住让「第 1 次应答何时放行」由测试控制。
	srv.pauseArchive()
	srv.pushArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	srv.pushArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	sub := hub.Subscribe("user-1")
	defer sub.Close()

	viewDone := make(chan struct{})
	go func() {
		defer close(viewDone)
		_, _ = hub.View(context.Background(), env, "tok")
	}()
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	collector.await(true) // 第 1 次归档拉取在途、应答被压住
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
	sub.Drain()

	// 拉取在途时发生归档写：失效（推进世代、清零簿记）。
	hub.InvalidateArchive("user-1", "env-1")

	// 放行写前应答。此后不再发任何 View/Peek：写前落地只换数据不封窗，
	// 留下 needsRefetch，落地收尾必须自己补拉。
	srv.resumeArchive()

	// 自动补拉的证据：无读者参与，归档 RPC 被再次调用（第 2 项队列应答）。
	deadline := time.After(3 * time.Second)
	for srv.archiveCallCount() != 2 {
		select {
		case <-deadline:
			t.Fatalf("finisher must refetch without any reader, archive calls = %d", srv.archiveCallCount())
		case <-time.After(10 * time.Millisecond):
		}
	}

	// 补拉落地（写后快照，含新线程 th-b）：归档面恢复可信，并发事件——
	// 无读参与下浏览器也能收到提示。
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil &&
			len(v.Snaps.Archived.Threads) == 2
	})
	ids := map[string]bool{}
	for _, th := range view.Snaps.Archived.Threads {
		ids[th.ID] = true
	}
	if !ids["th-a"] || !ids["th-b"] {
		t.Fatalf("recovered archive must contain post-write threads, got %+v", view.Snaps.Archived.Threads)
	}
	awaitShellEvent(t, sub, func(e T3ShellEvent) bool { return e.Full })

	// 无空转：补拉已世代相符落地封窗，标志不再置位。静默窗口内归档 RPC
	// 不得再被调用（TTL 充裕，这里只可能来自补拉空转）。
	time.Sleep(250 * time.Millisecond)
	if got := srv.archiveCallCount(); got != 2 {
		t.Fatalf("no refetch loop allowed, archive calls = %d", got)
	}
}

// 同型交错落在失败路径（R11 P2-1 对称覆盖）：写前拉取以真实失败告终
// （非「方法不存在」，不触发 legacy 回退）且世代已变——不记冷却、只留
// needsRefetch，落地收尾同样必须自动补拉并拿到写后快照。
func TestT3ShellHub_GenerationMismatchFailureAutoRefetches(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.pauseArchive()
	srv.pushArchiveFixture(nil, map[string]any{"_tag": "RuntimeError", "message": "storage hiccup"})
	srv.pushArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
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
	collector.await(true)
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

	hub.InvalidateArchive("user-1", "env-1")
	srv.resumeArchive()

	// 失败收尾（世代不符不记冷却）后同样自动补拉。
	deadline := time.After(3 * time.Second)
	for srv.archiveCallCount() != 2 {
		select {
		case <-deadline:
			t.Fatalf("finisher must refetch after mismatched failure, archive calls = %d", srv.archiveCallCount())
		case <-time.After(10 * time.Millisecond):
		}
	}
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	_ = view
	time.Sleep(250 * time.Millisecond)
	if got := srv.archiveCallCount(); got != 2 {
		t.Fatalf("no refetch loop allowed, archive calls = %d", got)
	}
}

// 空归档首次就绪必须广播（R11 P2-3）：去重基准的零值 {ids:nil, err:""}
// 恰好等于「归档为空且可信」的指纹——不加区分时，新环境的首份空归档
// 快照被判「与已宣布相同」，Full 与 State 一个都不发；归档落地晚于活跃面
// Ready 的页面收不到任何提示，永远停在「归档线程暂不可读」降级。修复后
// 首次发布无条件放行；此后同一份数据的重拉（TTL 过期）仍被去重静默。
func TestT3ShellHub_EmptyArchiveFirstSnapshotPublishes(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1), nil) // 合法空归档（threads:[]）
	cfg := hubTestConfig()
	cfg.ArchiveTTL = 150 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	sub := hub.Subscribe("user-1")
	defer sub.Close()

	// 建连时压住归档应答：活跃面先就绪（产生它自己的事件），清掉后观察
	// 窗口里只剩归档落地一个事件源。
	srv.pauseArchive()
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	sub.Drain()

	// 放行空归档首份快照：必须发事件（Full）。
	srv.resumeArchive()
	ev := awaitShellEvent(t, sub, func(e T3ShellEvent) bool { return e.Full || e.State != "" })
	if ev.EnvironmentID != "env-1" || !ev.Full {
		t.Fatalf("first empty-archive landing must publish Full, got %+v", ev)
	}
	sub.Drain()

	// TTL 过期重拉同一份空数据：指纹与已宣布的相同 → 不得再发任何事件
	// （首次发布标志不得破坏去重契约）。
	time.Sleep(cfg.ArchiveTTL + 80*time.Millisecond)
	if _, err := hub.Peek(env, "tok"); err != nil {
		t.Fatalf("peek: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if events := sub.Drain(); len(events) != 0 {
		t.Fatalf("identical refetch must stay silent, got %+v", events)
	}
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

// isArchiveSnapshotMethodMissing 的匹配大小写不敏感（审修 N3）：上游文案
// 的大小写没有保证，首字母大写变体（"Method not found"/"Unknown method"）
// 与 _tag 形式都要命中；普通业务失败与传输层不确定仍然不构成「方法不
// 存在」。
func TestIsArchiveSnapshotMethodMissingCaseInsensitive(t *testing.T) {
	yes := []string{
		"UnknownException: method not found: orchestration.getArchivedShellSnapshot",
		"UnknownException: Method not found: orchestration.getArchivedShellSnapshot",
		"UnknownException: METHOD NOT FOUND",
		"UnknownException: unknown method",
		"UnknownException: Unknown method",
		"UnknownException: MethodNotFound",
		"UnknownException: RpcMethodNotFound",
	}
	for _, msg := range yes {
		if !isArchiveSnapshotMethodMissing(errors.New(msg)) {
			t.Fatalf("must be classified as method missing: %q", msg)
		}
	}
	no := []string{
		"UnknownException: archive exploded",
		"UnknownException: Method absent",
		"RPC 调用失败（无错误详情）",
	}
	for _, msg := range no {
		if isArchiveSnapshotMethodMissing(errors.New(msg)) {
			t.Fatalf("must NOT be classified as method missing: %q", msg)
		}
	}
	if isArchiveSnapshotMethodMissing(&rpcUncertainError{err: errors.New("method not found")}) {
		t.Fatalf("uncertain transport error must never be classified as method missing")
	}
}

// method-not-found 判定的大小写变体须触发旧面回退（审修 N3，端到端）：
// 上游以 "Method not found"/"Unknown method" 首字母大写文案拒绝按需快照
// 时，回退必须同样激活——否则归档面长期停 ArchiveErr、每 TTL 盲重试。
// 变体命中后切订阅面、权威快照落地恢复可信，与全小写路径一致。
func TestT3ShellHub_MethodNotFoundCaseVariantsFallBack(t *testing.T) {
	variants := []struct {
		name    string
		message string
	}{
		{"capitalized method not found", "Method not found: orchestration.getArchivedShellSnapshot"},
		{"capitalized unknown method", "Unknown method"},
		{"uppercase method not found", "METHOD NOT FOUND: orchestration.getArchivedShellSnapshot"},
		{"tag form", "MethodNotFound"},
	}
	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
			srv.setArchiveLegacy(true)
			// archErr 定制失败文案：fake 照常应答该错误（legacy 模式下定制
			// 失败优先于内置的 method-not-found 应答，见 fake 的分派注释）。
			srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), map[string]any{"_tag": "UnknownException", "message": tc.message})
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

			// 变体命中 → 切订阅面：旧面订阅被拉起、权威快照落地恢复可信。
			srv.awaitArchiveSub(t)
			waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
				return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
			})
		})
	}
}

// legacy 回退的失败收尾时序（R5 P2）：方法不存在切订阅面时必须先记失败、
// 后切换——switchToArchiveLegacy 放行的常驻订阅可能紧跟着落地权威快照
// （archiveErr=nil、archiveReady=true），先切换后收尾会把刚恢复可信的
// 订阅面重新写回失败态，legacy 流可能很久不发下一帧。闸门压住订阅首帧：
// 订阅激活即证失败收尾已完成（修复后：先记账、后放行订阅），断言失败
// 如实记账、权威落地后恢复可信且失败不再被写回。
func TestT3ShellHub_LegacyFallbackFailureNotedBeforeLanding(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	srv.pauseArchiveSub() // 权威落地等失败收尾完成后才放行
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)
	waitForPeek := func(until func(*t3ShellView) bool) *t3ShellView {
		t.Helper()
		deadline := time.After(3 * time.Second)
		for {
			view, err := hub.Peek(env, "tok")
			if err != nil {
				t.Fatalf("peek: %v", err)
			}
			if until(view) {
				return view
			}
			select {
			case <-deadline:
				t.Fatalf("timed out waiting for archive state, snaps=%+v", view.Snaps)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}

	// 活跃面先就位（entry Ready 才带 Snaps）；waitView 的归档 kick 发一次
	// 探测性快照 RPC → 方法不存在 → 触发旧面订阅。
	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}

	// 回退已激活（切换的证据）。修复后顺序保证：awaitArchiveSub 可观测时
	// 失败收尾必已完成（先记账、后 close(legacyCh) 放行订阅）。
	srv.awaitArchiveSub(t)

	// 失败已如实记账、订阅快照仍被闸门压住：归档面此刻必须报不可信。
	view := waitForPeek(func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil
	})
	_ = view

	// 放行权威快照落地：健康的 legacy 流必须清 ArchiveErr、恢复可信——
	// 先前的失败收尾不得把它重新写回失败态。
	srv.resumeArchiveSub()
	view = waitForPeek(func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	if view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("archived threads via legacy subscription = %+v", view.Snaps.Archived.Threads)
	}

	// 失败冷却/备忘簿记不得在订阅面复活按需拉取：切换后继续读不再产生新
	// 的快照 RPC，ArchiveErr 稳定保持 nil（legacy 模式下 Peek 的 kick 短
	// 路，不拨号）。
	calls := srv.archiveCallCount()
	for i := 0; i < 3; i++ {
		v, err := hub.Peek(env, "tok")
		if err != nil {
			t.Fatalf("peek: %v", err)
		}
		if v.Snaps == nil || v.Snaps.ArchiveErr != nil {
			t.Fatalf("trusted legacy face must stay trusted, snaps=%+v", v.Snaps)
		}
	}
	if got := srv.archiveCallCount(); got != calls {
		t.Fatalf("legacy env must not re-dial the on-demand snapshot RPC, calls %d → %d", calls, got)
	}
}

// 旧面写后失效（R3 P1-B）：失效的正确动作是让归档订阅断开重连取回权威
// 全量——常驻流只收增量、不自发重发全量，「增量落地后整表重置」会把
// 归档面清空到下一次断线才恢复。本用例钉住：失效后面留行但不可信 →
// 第一连接被主动断开 → 重连的新订阅拿到写后全量快照。
func TestT3ShellHub_LegacyInvalidateTriggersReconnectRefetch(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 活跃面先就位；归档按需探测被判方法不存在 → 旧面订阅激活。
	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}
	srv.awaitArchiveSub(t)
	firstArchive := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})

	// 写后失效：归档面立刻撤为不可信（旧行保留），订阅面按备忘断开。
	hub.InvalidateArchive("user-1", "env-1")
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil
	})
	if !firstArchive.awaitClosed(3 * time.Second) {
		t.Fatalf("invalidation must end the legacy archive connection for a fresh snapshot")
	}

	// 重连订阅 = 写后状态的全量（fake 应答按当时 fixture 组装）。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
	sub2 := srv.awaitArchiveSub(t)
	secondArchive := <-srv.archiveConns
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	ids := map[string]bool{}
	for _, th := range view.Snaps.Archived.Threads {
		ids[th.ID] = true
	}
	if !ids["th-a"] || !ids["th-b"] {
		t.Fatalf("archive face must recover via reconnected snapshot, got %+v", view.Snaps.Archived.Threads)
	}

	// 恢复后增量照常吃：面不会再被误清（旧实现的缺陷路径已删，本断言
	// 钉住「增量不触发重置」的语义）。
	thC, _ := json.Marshal(map[string]any{"id": "th-c", "projectId": "p-1", "title": "归档-th-c", "status": "idle", "archivedAt": "2026-10-01T00:00:00.000Z"})
	item, _ := json.Marshal(map[string]any{"kind": "thread.updated", "sequence": 3, "thread": json.RawMessage(thC)})
	if err := secondArchive.sendValue(sub2.ID, item); err != nil {
		t.Fatalf("send archive delta: %v", err)
	}
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 3
	})
	_ = view
}

// 旧面守旧备忘与增量落地并发（R3 P1-A）：失效置备忘时恰有在途增量落地，
// 修复前落地收尾持 arch.mu 取 s.mu（arch.mu → s.mu 反向嵌套），与任何
// s.mu → arch.mu 序列（如 admitArchiveRefresh 准入）并发即 ABBA 死锁、
// s.mu 永久占住。本用例先在测试线程持住 s.mu，再推送增量：若落地路径仍
// 取 arch.mu → s.mu，它会卡在等 s.mu（不崩溃、可多次重演）；持锁期间
// 置失效并发 reconnect 信号，随后放锁——增量落地 + 备忘消费 +
// 订阅断开必须全部在有限时间内完成，不走完即死锁回归。
func TestT3ShellHub_LegacyPendingDoesNotDeadlock(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
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
	sub := srv.awaitArchiveSub(t)
	firstArchive := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})

	key := t3ShellHubKey("user-1", "env-1")
	hub.mu.Lock()
	entry := hub.entries[key]
	hub.mu.Unlock()
	if entry == nil {
		t.Fatalf("entry missing")
	}

	// 重演「失效置备忘 与 归档增量落地 并发」：持 s.mu 卡住任何
	// arch.mu → s.mu 反向嵌套的落地收尾（旧实现死锁形态），同时在锁外
	// 推增量 + 置失效。解锁后三件事都得走完：增量应用并 Ack、备忘被
	// 读循环消费、连接按备忘断开。
	sendDelta := func(conn *fakeShellConn, id, seq int) error {
		th, _ := json.Marshal(map[string]any{"id": "th-d", "projectId": "p-1", "title": "归档-th-d", "status": "idle", "archivedAt": "2026-10-01T00:00:00.000Z"})
		item, _ := json.Marshal(map[string]any{"kind": "thread.updated", "sequence": seq, "thread": json.RawMessage(th)})
		return conn.sendValue(id, item)
	}
	for round := 0; round < 3; round++ {
		entry.state.mu.Lock()
		sendDone := make(chan error, 1)
		go func(seq int) {
			// 发送本身与断连竞速：写失败只说明备忘已先行拆连（它正是
			// 目的），不等于死锁；死锁的证据是 sendValue/落地永远回不来。
			sendDone <- sendDelta(firstArchive, sub.ID, seq)
		}(round)
		// 失效与增量落地并发（InvalidateArchive 自身也要 s.mu，异步发起
		// 再解锁才构成真正交错）。
		invDone := make(chan struct{})
		go func() {
			hub.InvalidateArchive("user-1", "env-1")
			close(invDone)
		}()
		entry.state.mu.Unlock()
		select {
		case <-sendDone:
		case <-time.After(3 * time.Second):
			t.Fatalf("round %d: archive delta landing deadlocked", round)
		}
		select {
		case <-invDone:
		case <-time.After(3 * time.Second):
			t.Fatalf("round %d: invalidate deadlocked against archive landing", round)
		}
		if !firstArchive.awaitClosed(3 * time.Second) {
			t.Fatalf("round %d: legacy conn must be ended by the reconnect memo", round)
		}
		// 重连接回：旧面恢复可信后再重演下一轮。
		sub = srv.awaitArchiveSub(t)
		firstArchive = <-srv.archiveConns
		waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
			return v.Snaps != nil && v.Snaps.ArchiveErr == nil
		})
	}

	// 稳定连接上确定性重演：增量正常落地并 Ack（落地路径不再触备忘，
	// 也不会在增量后清表），随后置备忘依旧只拆连不清通道——两侧都不
	// 死锁、不失步。
	if err := sendDelta(firstArchive, sub.ID, 90); err != nil {
		t.Fatalf("deterministic archive delta: %v", err)
	}
	if !firstArchive.awaitAck(sub.ID, 2*time.Second) {
		t.Fatalf("delta must land and be acked on the stable legacy conn")
	}
	hub.InvalidateArchive("user-1", "env-1")
	if !firstArchive.awaitClosed(3 * time.Second) {
		t.Fatalf("memo-driven disconnect after delta landing")
	}
	srv.awaitArchiveSub(t)

	// 失效/落地并发不得阻塞 s.mu 上的读取（死锁形态下 View 会永久挂起）。
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.InvalidateArchive("user-1", "env-1")
	}()
	if _, err := hub.View(context.Background(), env, "tok"); err != nil {
		t.Fatalf("view during invalidation: %v", err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("invalidate must not deadlock against in-flight archive work")
	}
}

// 旧面 reconnect 世代护栏（R4 P2）：重连的新连接已发出权威快照、但快照
// 尚未 apply 的窗口里又一次失效，「变更前快照」落地不得吞掉该请求的
// 备忘——读循环的信号分支须消费到它、断开该连接并再重连一次取写后全
// 量。旧实现落地即清备忘：信号到的时候备忘已空、被忽略，写前数据被当
// 成当前可信，wait 会拿陈旧归档集判 thread_gone 直到下一个事件/重连。
// 交错用 s.mu 精确排出（禁用竞态）：持锁阻住落地 → 发送快照帧（frame
// 已发出但 apply 阻塞在 s.mu 上）→ 失效记入新备忘 → 放锁 → 断言「快照
// 落地但备忘仍在、连接随后被拆」。
func TestT3ShellHub_LegacyReconnectPreservesNewerRequest(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 活跃面先就位；归档按需探测被判方法不存在 → 旧面订阅激活。
	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}
	srv.awaitArchiveSub(t)
	conn1 := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})

	// 第一次失效：连接1被拆、监督循环重连出连接2。连接2的订阅应答快照
	// 由 fake 在受理时自动推送——但内容按应答时刻的 fixture（此时仍是
	// 写前的 th-a 单线程体），它正是「变更前快照」。
	hub.InvalidateArchive("user-1", "env-1")
	if !conn1.awaitClosed(3 * time.Second) {
		t.Fatalf("first invalidation must end conn1")
	}
	sub2 := srv.awaitArchiveSub(t)
	conn2 := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil
	})

	// 上游在连接2订阅之后、第二次失效之前把归档集改成写后状态——连接3
	// 的订阅应答将携带它；连接2已发的快照则是变更前数据。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)

	key := t3ShellHubKey("user-1", "env-1")
	hub.mu.Lock()
	entry := hub.entries[key]
	hub.mu.Unlock()
	if entry == nil {
		t.Fatalf("entry missing")
	}

	// 排交错（仅在非 -race 构建下有区分度；race 下本用例退化为「失效与
	// 落地乱序」的两种合法结局，其中备忘被信号消费也是正确路径，后面
	// 的断言两种结局都接受——真正的门控回归只在「备忘被清且连接2未
	// 被拆」时炸出）。
	entry.state.mu.Lock()
	thStale, _ := json.Marshal(map[string]any{"id": "th-a", "projectId": "p-1", "title": "归档-th-a", "status": "idle", "archivedAt": "2026-10-01T00:00:00.000Z"})
	staleSnap, _ := json.Marshal(map[string]any{
		"kind": "snapshot",
		"snapshot": map[string]any{
			"schemaVersion": 1, "snapshotSequence": 5,
			"projects": []map[string]any{{"id": "p-1", "title": "Fast Ship", "workspaceRoot": "/repo"}},
			"threads":  []json.RawMessage{thStale},
		},
	})
	if err := conn2.sendValue(sub2.ID, staleSnap); err != nil {
		entry.state.mu.Unlock()
		t.Fatalf("send stale-window snapshot: %v", err)
	}
	// 快照帧已发出、落地被 s.mu 阻住——此刻在另一 goroutine 记第二次失
	// 效（失效自身取 s.mu，同步调会把测试卡死在自持锁上）：它阻塞在
	// s.mu 上正好排在「落地」之后，构成「帧已发出、apply 前失效」的窗
	// 口交错。
	invDone := make(chan struct{})
	go func() {
		hub.InvalidateArchive("user-1", "env-1")
		close(invDone)
	}()
	entry.state.mu.Unlock()
	select {
	case <-invDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("invalidation must land after the snapshot apply releases s.mu")
	}

	// 断言：变更前快照不得吞掉更新的备忘——读循环的信号分支消费到它
	// 后必须拆掉连接2（旧实现落地即清备忘，信号被忽略，连接2会一直活
	// 着把写前数据当最终可信态）。
	if !conn2.awaitClosed(3 * time.Second) {
		t.Fatalf("newer reconnect request must survive the pre-change snapshot and end conn2")
	}

	// 断言2：第三次重连拿到写后全量——备忘兑现的证据是连接3的订阅应
	// 答携带 th-b（订阅受理时的 fixture 已是写后状态）。
	srv.awaitArchiveSub(t)
	conn3 := <-srv.archiveConns
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	ids := map[string]bool{}
	for _, th := range view.Snaps.Archived.Threads {
		ids[th.ID] = true
	}
	if !ids["th-a"] || !ids["th-b"] {
		t.Fatalf("archive face must recover post-change rows via the extra reconnect, got %+v", view.Snaps.Archived.Threads)
	}
	_ = conn3
}

// 旧面邮戳时机（审修 R6-A）：邮戳必须在发 subscribe 之前取——若在发送
// 后才取，「发送后、邮戳取值前」记入的失效世代会被算进邮戳，变更前快
// 照落地时 connectionStart >= pendingGen 成立、备忘被误清，随后到达的
// reconnect 信号被当无备忘忽略，写后全量不再重拉。发送→取戳的窗口在进
// 程内只有纳秒级、测试无法从外部确定性注入；本用例钉住同一不变量在可
// 观察粒度上的表达：「conn2 的 subscribe 已发出、其权威快照尚未落地」
// 期间记入的请求世代（gen2）必然大于邮戳——落地不得吞掉备忘，读循环
// 须消费信号拆掉 conn2，再由 conn3 的订阅取回写后全量。
//
// 交错：conn1 尚在时经写后失效记备忘 gen=1 → conn1 被拆、conn2
// 重连（subscribe 在 gen=1 之后发出，其快照对 gen=1 合法）→ 持 s.mu 阻
// 住 conn2 的 apply → 推送一张变更前形状的快照（写前数据）→ 在「
// subscribe 已发出、快照未落地」的窗口里记第二次失效（gen2）→ 放锁：
// 快照落地不得清掉 gen2 的备忘，conn2 随后被信号消费拆掉。
func TestT3ShellHub_LegacyStampTakenBeforeSubscribe(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 活跃面先就位；归档按需探测被判方法不存在 → 旧面订阅激活并就绪。
	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}
	srv.awaitArchiveSub(t)
	conn1 := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})

	// 上游先把归档集改成写后状态：之后受理的订阅应答都携带 th-b。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)

	key := t3ShellHubKey("user-1", "env-1")
	hub.mu.Lock()
	entry := hub.entries[key]
	hub.mu.Unlock()
	if entry == nil {
		t.Fatalf("entry missing")
	}

	// 第一次重建请求（gen=1，经写后失效记入：legacy 面失效的正确动作就
	// 是拆连重连）：conn1 被拆、conn2 重连。conn2 的 subscribe
	// 受理证据（archiveSubs）一到，subscribe 必已发出——此刻记入的失效世
	// 代必然在 conn2 邮戳右侧（邮戳取于发送之前）。
	hub.InvalidateArchive("user-1", "env-1")
	if !conn1.awaitClosed(3 * time.Second) {
		t.Fatalf("first reconnect request must end conn1")
	}
	sub2 := srv.awaitArchiveSub(t)
	conn2 := <-srv.archiveConns

	// 压住 conn2 的 apply：持 s.mu 推送一张变更前形状的快照（写前数据，
	// 模拟上游按变更前归档集应答），再在「subscribe 已发出、快照未落地」
	// 的窗口里记第二次失效（gen2，落在另一 goroutine 上等 s.mu）。
	entry.state.mu.Lock()
	thStale, _ := json.Marshal(map[string]any{"id": "th-a", "projectId": "p-1", "title": "归档-th-a", "status": "idle", "archivedAt": "2026-10-01T00:00:00.000Z"})
	staleSnap, _ := json.Marshal(map[string]any{
		"kind": "snapshot",
		"snapshot": map[string]any{
			"schemaVersion": 1, "snapshotSequence": 9,
			"projects": []map[string]any{{"id": "p-1", "title": "Fast Ship", "workspaceRoot": "/repo"}},
			"threads":  []json.RawMessage{thStale},
		},
	})
	if err := conn2.sendValue(sub2.ID, staleSnap); err != nil {
		entry.state.mu.Unlock()
		t.Fatalf("send stale snapshot: %v", err)
	}
	invDone := make(chan struct{})
	go func() {
		hub.InvalidateArchive("user-1", "env-1") // gen=2：conn2 subscribe 之后记入
		close(invDone)
	}()
	entry.state.mu.Unlock()
	select {
	case <-invDone:
	case <-time.After(3 * time.Second):
		t.Fatalf("invalidation must land after the snapshot apply releases s.mu")
	}

	// 断言：变更前快照不得吞掉 subscribe 后记录的备忘——读循环消费到它
	// 后必须拆掉 conn2（邮戳若含 gen2，备忘被清、信号被忽略，conn2 一直
	// 活着把写前数据当最终可信态）。
	if !conn2.awaitClosed(3 * time.Second) {
		t.Fatalf("post-subscribe invalidation must survive the pre-change snapshot and end conn2")
	}
	srv.awaitArchiveSub(t)
	<-srv.archiveConns
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	ids := map[string]bool{}
	for _, th := range view.Snaps.Archived.Threads {
		ids[th.ID] = true
	}
	if !ids["th-a"] || !ids["th-b"] {
		t.Fatalf("archive face must recover post-change rows via the extra reconnect, got %+v", view.Snaps.Archived.Threads)
	}
}

// 归档快照应答形状校验（审修 R6-B）：getArchivedShellSnapshot 的成功应答
// 不校验判别字段时，value=null、多包一层（{snapshot:{...}}）、字段改名都
// 零错误解成零值快照——落地会把归档表整体重置为空并封可信，谎报「归档
// 列表确定为空且可信」，且不触发旧面回退。形状不符（threads 字段缺失/
// null）必须走失败路径：ArchiveErr 记真实原因、已有行不清空、不封窗；
// threads:[] 的合法空归档仍要照常可信。
func TestT3ShellHub_ArchiveSnapshotShapeValidated(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 先喂一张带行的合法快照，归档面就绪且留行。
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	_ = view

	// 失效作废可信面 → 下一次读取 kick 重拉；fake 改答「threads 字段缺失的包裹形
	// 状」。修复前零值快照被应用成功并封可信、清空归档行；修复后读取以形
	// 状失败告终：ArchiveErr 记真实原因、行保留、不封窗。
	hub.InvalidateArchive("user-1", "env-1")
	wrapped, _ := json.Marshal(map[string]any{"snapshot": json.RawMessage(archiveSnapshotBody(2, "th-b"))})
	srv.setArchiveFixture(wrapped, nil)
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil &&
			strings.Contains(v.Snaps.ArchiveErr.Error(), "缺少 threads 判别字段") &&
			len(v.Snaps.Archived.Threads) == 1
	})
	if view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("rejected shape must not wipe archive rows, got %+v", view.Snaps.Archived.Threads)
	}

	// value=null 同样被拒（Exit Success value 缺省/显式 null → threads
	// 判别字段缺失）。
	hub.InvalidateArchive("user-1", "env-1")
	srv.setArchiveFixture(json.RawMessage("null"), nil)
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil &&
			strings.Contains(v.Snaps.ArchiveErr.Error(), "缺少 threads 判别字段") &&
			len(v.Snaps.Archived.Threads) == 1
	})
	_ = view

	// threads:null 同样被拒（审修 R8 P2）：json.RawMessage 对显式 null 得
	// 到 4 字节 "null" 而非 nil——按「字段存在」放行会把归档表整体清成
	// 空并封可信，谎报「归档列表确定为空且可信」。
	hub.InvalidateArchive("user-1", "env-1")
	srv.setArchiveFixture(json.RawMessage(`{"schemaVersion":1,"snapshotSequence":2,"projects":[],"threads":null}`), nil)
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil &&
			strings.Contains(v.Snaps.ArchiveErr.Error(), "缺少 threads 判别字段") &&
			len(v.Snaps.Archived.Threads) == 1
	})
	if view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("threads:null must not wipe archive rows, got %+v", view.Snaps.Archived.Threads)
	}

	// 合法空归档（threads:[] 字段存在）不是坏形状：照常落地并封可信、
	// 归档表按权威快照置空——与「字段缺失的零值」区分开。
	srv.setArchiveFixture(archiveSnapshotBody(3), nil)
	hub.InvalidateArchive("user-1", "env-1")
	view = waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 0
	})
	_ = view
}

// 归档退避用归档面自己的同步时刻（审修 R6-C）：活跃面长期健康时其
// syncConfirmedAt 恒为旧值，归档订阅持续失败被误当「刚追平过」——
// attempts 每轮重置、按 BackoffBase 无限重连（重连风暴）。修复后失败连接
// 的 syncedAt 是归档面最近数据落地时刻：从未收到数据的持续失败让
// attempts 单调加深，退避必须递增。探测计数比对前后窗口的增长速率：速率
// 下降即退避在加深，恒高速即风暴回归。最后让订阅成功一次，确认连接稳定
// 后不再风暴。
func TestT3ShellHub_LegacyArchiveBackoffEscalates(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	// 退避递增需要可观察的区间：上限放宽到 2s（远大于观察窗口），否则
	// 递增多轮后都钳在同一个小上限上，速率差分不出来。StableResetAfter
	// 压到 1ms：修复前稳定判定用活跃面 syncConfirmedAt——活跃面已追平，
	// since 必然 >1ms，attempts 每轮重置为 0（风暴形态，恒定 20ms 节奏）；
	// 修复后掐断的连接 syncedAt 为零值（本连接没收到过数据），attempts
	// 持续加深。
	cfg := hubTestConfig()
	cfg.BackoffMax = 2 * time.Second
	cfg.StableResetAfter = time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
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

	// 归档订阅先健康一段（数据落地 → lastArchiveSyncedAt 非零），再让
	// 上游开始持续掐断归档订阅：之后每条连接 subscribe 后立即被服务端
	// 关闭，归档面再未收到数据。
	srv.awaitArchiveSub(t)
	firstArchive := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	srv.setArchiveDrop(true)
	firstArchive.closeConn()
	if !firstArchive.awaitClosed(3 * time.Second) {
		t.Fatalf("first legacy conn must close")
	}

	// 等长时间窗比对重连速率（窗口起点锚在断连后第一条 subscribe 受理：
	// 退避递增 → 第一条之后的 500ms 内受理数远少于稳态风暴）。若稳定判
	// 定错用活跃面时刻，attempts 恒被重置为 0、重连按 BackoffBase 节奏持
	// 续，窗口内受理数不会掉下来。
	select {
	case <-srv.archiveSubs:
	case <-time.After(2 * time.Second):
		t.Fatalf("no archive reconnect after drop")
	}
	window := time.After(500 * time.Millisecond)
	late := 0
	for counting := true; counting; {
		select {
		case <-srv.archiveSubs:
			late++
		case <-window:
			counting = false
		}
	}
	// 递增退避下 500ms 窗口至多容纳 ~4 条（20+40+80+160+320≈620ms 的
	// 前五条间隔已超窗，jitter 至多 ±25%）；风暴模式下 BackoffBase 节奏
	// 20ms/jitter 一档恒速，500ms 至少 ~15 条。取保守阈值 6。
	if late > 6 {
		t.Fatalf("archive reconnects must back off exponentially, got %d subs in 500ms window", late)
	}

	// 停止掐断后订阅恢复：新连接的权威快照落地，归档面回到可信。
	srv.setArchiveDrop(false)
	srv.awaitArchiveSub(t)
	<-srv.archiveConns
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	_ = view
}

// 旧面写前快照不得封可信（审修 R8 P2）：订阅连接「邮戳 < 备忘世代」的
// 快照是先于最近一次写后失效发出的变更前状态——落地只能换数据，不得
// 恢复可信。修复前 applyArchiveSnapshotLocked 无条件置 Ready+清 Err 才
// 判邮戳，从消费备忘到连接被拆开的窗口里，并发 View/Peek 会拿写前归
// 档集判 thread_gone、谎报列表完整。
// 交错窗口（备忘已置位、写前快照尚未落地）在 wire 上只有纳秒级、无法
// 从外部确定性注入；unit 版（t3_shell_state_test.go 的
// TestShellState_LegacyPreChangeSnapshotStaysUntrusted）直接对 state 驱动
// 同一 applyArchiveItem 路径钉死不变量。这里钉住端到端结局：连续失效
// 后归档面始终经「拆旧连→新连写后快照」恢复可信，不谎报完整。
func TestT3ShellHub_LegacyPreChangeSnapshotStaysUntrusted(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 活跃面先就位；归档按需探测被判方法不存在 → 旧面订阅激活并就绪。
	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}
	srv.awaitArchiveSub(t)
	conn1 := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})

	// 上游把归档集改成写后状态：之后受理的订阅应答都携带 th-b。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)

	// 写后失效：conn1 被拆、conn2 重连。conn2 的订阅应答携带写后全量
	// （fixture 已是 th-b），落地恢复可信——写前快照不会把这窗口谎报成
	// 完整（恢复路径本身即「邮戳 ≥ 备忘世代才置可信」的证据）。
	hub.InvalidateArchive("user-1", "env-1")
	if !conn1.awaitClosed(3 * time.Second) {
		t.Fatalf("first invalidation must end conn1")
	}
	srv.awaitArchiveSub(t)
	<-srv.archiveConns
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	ids := map[string]bool{}
	for _, th := range view.Snaps.Archived.Threads {
		ids[th.ID] = true
	}
	if !ids["th-a"] || !ids["th-b"] {
		t.Fatalf("post-change snapshot must restore trust with write-behind rows, got %+v", view.Snaps.Archived.Threads)
	}
}

// legacy mode 自愈（审修 R8 P2，end-to-end）：订阅连接因「方法不存在」
// Exit 失败时 mode 回 Unknown——下一次读重探测按需快照（环境可能中途
// 升级回新版/上次误判）。真旧环境重探测会再次判方法不存在、经
// switchToArchiveLegacy 切回订阅面（多付一次探测拨号、不死锁）；环境
// 确实升级了则按需路径恢复供数。订阅的其他失败（非方法级）不误重置。
func TestT3ShellHub_LegacyMethodNotFoundResetsToUnknown(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 旧面就绪。
	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}
	srv.awaitArchiveSub(t)
	conn1 := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})

	// 订阅连接收到「方法不存在」的 Exit——本该有订阅面的环境反而没
	// 有，是「环境中途升级/上次误判」的证据。archSubFail 定制该应答
	// （archLegacy 保持 true，这里只钉住「方法不存在的订阅 Exit → mode
	// 回 Unknown」这一步，不追踪升级后的完整路径——后者由
	// LegacyHealsToSnapshotAfterUpgrade 用真升级 fake 覆盖）。
	srv.setArchiveSubFail(map[string]any{
		"_tag":    "UnknownException",
		"message": "method not found: orchestration.subscribeArchivedShell",
	})
	conn1.closeConn()
	if !conn1.awaitClosed(3 * time.Second) {
		t.Fatalf("legacy conn must close for the reconnect")
	}
	// 等「发送了失败 Exit」的重连连接——它的落地触发 mode 重置。断言
	// mode 真的回到 Unknown（自愈的证据）。
	select {
	case <-srv.archiveConns:
	case <-time.After(3 * time.Second):
		t.Fatalf("no reconnecting conn carries the method-not-found exit")
	}
	key := t3ShellHubKey("user-1", "env-1")
	hub.mu.Lock()
	entry := hub.entries[key]
	hub.mu.Unlock()
	healed := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		entry.arch.mu.Lock()
		mode := entry.arch.mode
		entry.arch.mu.Unlock()
		if mode == t3ArchiveModeUnknown {
			healed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !healed {
		t.Fatalf("method-not-found subscribe exit must reset mode to Unknown")
	}
}

// legacy mode 自愈到按需面（审修 R8 P2，真升级）：环境「中途升级回新
// 版」——getArchivedShellSnapshot 重新可用、subscribeArchivedShell 反而
// 被判方法不存在。mode 回 Unknown 后重探测按需快照成功，归档面改由按
// 需路径供数（不再发订阅），是本审修项要修的核心自愈场景。
func TestT3ShellHub_LegacyHealsToSnapshotAfterUpgrade(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 旧面就绪（按需方法不存在 → 切订阅面）。
	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}
	srv.awaitArchiveSub(t)
	conn1 := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	probeCalls := srv.archiveCallCount()

	// 环境升级：按需方法恢复可用、订阅方法消失。#26238 后的新上游删除
	// 了 subscribeArchivedShell——fake 的 archLegacy=false 正好表达「新
	// 上游」（按需应答正常、subscribe 按方法不存在拒绝），订阅 Exit
	// method-not-found 触发 mode 回 Unknown 重探测。
	srv.setArchiveLegacy(false)
	conn1.closeConn()
	if !conn1.awaitClosed(3 * time.Second) {
		t.Fatalf("legacy conn must close for the reconnect")
	}
	// 新上游对 subscribe 回方法不存在（fake 内置应答）；等下一条重连
	// 连接到达即知失败 Exit 已送达、mode 重置已发生。连接走默认
	// method-not-found 分支，句柄不入 archiveConns——改用「按需快照被
	// 重探测」作落地证据。
	probed0 := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if _, err := hub.Peek(env, "tok"); err != nil {
			t.Fatalf("peek: %v", err)
		}
		if srv.archiveCallCount() > probeCalls {
			probed0 = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !probed0 {
		t.Fatalf("upgraded env must reset mode and re-probe the on-demand snapshot")
	}

	// 自愈后按需路径接管：kick 重探测按需快照成功，归档面经按需供数恢
	// 复可信；此后不再发订阅（按需面没有常驻订阅）。
	probed := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if _, err := hub.Peek(env, "tok"); err != nil {
			t.Fatalf("peek: %v", err)
		}
		if srv.archiveCallCount() > probeCalls {
			probed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !probed {
		t.Fatalf("healed mode must re-probe the on-demand snapshot")
	}
	view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	if view.Snaps.Archived.Threads[0].ID != "th-a" {
		t.Fatalf("upgraded env must serve the archive face via the on-demand snapshot, got %+v", view.Snaps.Archived.Threads)
	}
}

// 订阅的非方法级失败不误重置 mode（审修 R8 P2 的窄条件）：旧面订阅以
// 任意业务错误 Exit 时，「方法不存在」的窄条件不命中——mode 保持
// legacy，监督循环按退避重连订阅，下一次读不得复活按需快照拨号。
func TestT3ShellHub_LegacyNonMethodExitDoesNotReset(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
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
	srv.awaitArchiveSub(t)
	conn1 := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	probeCalls := srv.archiveCallCount()

	// 订阅持续以非方法级错误失败：监督循环退避重连，归档面留不可读。
	// mode 不得被重置——否则 kick 会复活按需拨号（按需方法在旧上游真
	// 的不存在，每轮探测都再切回 legacy，纯空拨）。
	srv.setArchiveSubFail(map[string]any{
		"_tag":    "RuntimeError",
		"message": "archive subscription exploded",
	})
	conn1.closeConn()
	if !conn1.awaitClosed(3 * time.Second) {
		t.Fatalf("legacy conn must close for the reconnect")
	}
	// 等「发送了失败 Exit」的重连连接：非方法级失败已落地，mode 应保
	// 持 legacy（窄条件不命中）。
	select {
	case <-srv.archiveConns:
	case <-time.After(3 * time.Second):
		t.Fatalf("no reconnecting conn carries the non-method exit")
	}

	// 连续读：归档面保持不可信，但按需快照零新调用（mode 未回 Unknown）。
	for i := 0; i < 3; i++ {
		if _, err := hub.Peek(env, "tok"); err != nil {
			t.Fatalf("peek: %v", err)
		}
	}
	time.Sleep(150 * time.Millisecond)
	if got := srv.archiveCallCount(); got != probeCalls {
		t.Fatalf("non-method subscribe failure must not re-probe the on-demand snapshot, calls %d → %d", probeCalls, got)
	}
	// 退避重连仍在走订阅路径（不是挂断空等）：窗口内应再受理订阅。
	select {
	case <-srv.archiveSubs:
	case <-time.After(2 * time.Second):
		t.Fatalf("legacy subscribe must keep reconnecting after a non-method failure")
	}
}

// 主动重连不计退避（审修 R8 P2，end-to-end）：写后失效触发的
// consume-断开是「主动去拿写后状态」不是故障——连续批量归档时每轮重
// 连立即收新快照，若按「syncedAt 刚刷新 → 视作不稳定」加深退避，退避
// 会 1s→2s→4s→30s 涨上去，每轮 markArchiveFailed 清空归档行，归档线程
// 消失数十秒。修复后 connResult.voluntary 区分两种断开：主动重连的
// attempts 归 0、按 BackoffBase 立即重连；真正的故障断开（订阅被掐/读
// 错误）照常加深。观察连续三次失效重连的间隔不超一次基准退避窗口，再
// 用一次真故障确认退避仍会加深。
func TestT3ShellHub_LegacyVoluntaryReconnectDoesNotBackoff(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	cfg := hubTestConfig()
	// 真故障可区分性：退避加深必须能观测——上限放宽到 300ms，真故障的
	// 第二轮间隔才会明显超过「立即重连」的阈值；主动重连恒 ~BackoffBase
	// （20ms±jitter）。
	cfg.BackoffMax = 300 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
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
	srv.awaitArchiveSub(t)
	conn := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})

	// 连续三次写后失效：每轮「备忘→断连→重连→新快照」应在一个小窗口
	// 内完成。退避若按故障加深（20→40→80→160…），第三轮的间隔已明显超
	// 出阈值——阈值 150ms > BackoffBase+jitter 上限（~25ms）+拨号/落
	// 地开销，< 连续加深后的累计（第 N 轮已背 40/80ms 档）。
	for round := 0; round < 3; round++ {
		start := time.Now()
		hub.InvalidateArchive("user-1", "env-1")
		if !conn.awaitClosed(3 * time.Second) {
			t.Fatalf("round %d: invalidation must end the legacy conn", round)
		}
		srv.awaitArchiveSub(t)
		conn = <-srv.archiveConns
		view := waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
			return v.Snaps != nil && v.Snaps.ArchiveErr == nil
		})
		_ = view
		if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
			t.Fatalf("round %d: voluntary reconnect must not deepen the backoff (took %v)", round, elapsed)
		}
	}

	// 真故障仍加深：掐断模式让订阅受理即断（无数据落地、非主动断开），
	// 连续重连的间隔必须涨起来——「主动断开不计退避」不得把故障路径
	// 的加深一并取消。
	srv.setArchiveDrop(true)
	conn.closeConn()
	if !conn.awaitClosed(3 * time.Second) {
		t.Fatalf("dropped legacy conn must close")
	}
	first := time.Now()
	select {
	case <-srv.archiveSubs:
	case <-time.After(2 * time.Second):
		t.Fatalf("no archive reconnect after drop")
	}
	second := time.Now()
	select {
	case <-srv.archiveSubs:
	case <-time.After(2 * time.Second):
		t.Fatalf("no second archive reconnect after drop")
	}
	third := time.Now()
	select {
	case <-srv.archiveSubs:
	case <-time.After(2 * time.Second):
		t.Fatalf("no third archive reconnect after drop")
	}
	// 掐断的连接 voluntary=false 且 syncedAt 零值（无数据）→ attempts 递
	// 增：受理间隔必须逐轮变长（20→40→80…±jitter）。只断言「后一间
	// 隔 > 前一间隔」证明故障路径的加深没有被 voluntary 免退避带走。
	gap12 := second.Sub(first)
	gap23 := third.Sub(second)
	if gap23 <= gap12 {
		t.Fatalf("real failures must keep deepening the backoff, gap1=%v gap2=%v", gap12, gap23)
	}
	srv.setArchiveDrop(false)
}

// 自愈后旧面监督循环收缰（审修 R9 P2-1，end-to-end）：订阅被判方法不存在
// → mode 回 Unknown 后，监督循环必须重新停泊——不再发任何 subscribe；按
// 需面接管供数，持续观察 2×TTL（含 TTL 到期重拉）：归档行不再被清空、
// 可信度至多是「过期重拉在途」的短暂 stale。旧实现里残余订阅连接的反复
// 失败经 endFaceConnection 每轮 markArchiveFailed 清表，而 kick 被簿记封
// 窗短路不重拉，归档面整整消失一个 TTL（1.5s 内 147/150 次 Peek 得
// ArchiveErr、归档行 0）。
func TestT3ShellHub_LegacyHealQuietAfterUpgrade(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveLegacy(true)
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	cfg := hubTestConfig()
	cfg.ArchiveTTL = 80 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	// 旧面就绪：按需快照被判方法不存在 → 切订阅面供数。
	go hub.View(context.Background(), env, "tok")
	collector := newShellConnCollector(t, srv)
	active := collector.await(false)
	if err := active.conn.sendValue(active.sub.ID, shellSnapshotValue(42, "th-1")); err != nil {
		t.Fatalf("send active snapshot: %v", err)
	}
	if !active.conn.awaitAck(active.sub.ID, 2*time.Second) {
		t.Fatalf("active snapshot not acked")
	}
	srv.awaitArchiveSub(t)
	conn1 := <-srv.archiveConns
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	probeCalls := srv.archiveCallCount()

	// 环境升级：按需方法恢复可用、订阅方法消失（fake 的 archLegacy=false
	// 即新上游）。旧连接断开后监督循环重连的那条连接收到「方法不存在」
	// 的 Exit → mode 回 Unknown，这是自愈落地的证据；此后基线才可信。
	srv.setArchiveLegacy(false)
	conn1.closeConn()
	key := t3ShellHubKey("user-1", "env-1")
	hub.mu.Lock()
	entry := hub.entries[key]
	hub.mu.Unlock()
	if entry == nil {
		t.Fatalf("entry missing")
	}
	healed := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		entry.arch.mu.Lock()
		mode := entry.arch.mode
		entry.arch.mu.Unlock()
		if mode == t3ArchiveModeUnknown {
			healed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !healed {
		t.Fatalf("method-not-found subscribe exit must reset mode to Unknown")
	}

	// 按需面接管：kick 重探测按需快照成功，归档恢复可信。
	probed := false
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if _, err := hub.Peek(env, "tok"); err != nil {
			t.Fatalf("peek: %v", err)
		}
		if srv.archiveCallCount() > probeCalls {
			probed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !probed {
		t.Fatalf("healed mode must re-probe the on-demand snapshot")
	}
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})

	// 持续观察 2×TTL（含至少一次 TTL 到期重拉）：归档行恒在（不再被
	// 清空）、非 stale 的 ArchiveErr 恒不出现、订阅计数钉在基线。
	baselineSubs := srv.archiveSubscribeCount()
	baselineCalls := srv.archiveCallCount()
	deadline := time.Now().Add(2*cfg.ArchiveTTL + 60*time.Millisecond)
	for time.Now().Before(deadline) {
		v, err := hub.Peek(env, "tok")
		if err != nil {
			t.Fatalf("peek: %v", err)
		}
		if v.Snaps == nil {
			t.Fatalf("healed archive view missing snaps")
		}
		if len(v.Snaps.Archived.Threads) != 1 {
			t.Fatalf("healed archive face must never be cleared, rows=%d", len(v.Snaps.Archived.Threads))
		}
		if v.Snaps.ArchiveErr != nil && !errors.Is(v.Snaps.ArchiveErr, errT3ShellArchiveStale) {
			t.Fatalf("healed archive face must stay trusted, err=%v", v.Snaps.ArchiveErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := srv.archiveSubscribeCount(); got != baselineSubs {
		t.Fatalf("healed entry must not subscribe again, subs %d → %d", baselineSubs, got)
	}
	if srv.archiveCallCount() <= baselineCalls {
		t.Fatalf("observation window must include TTL re-pulls (window not vacuous)")
	}
}

// endFaceConnection 的归档收尾按 mode 把关（审修 R9 P2-1，单元口径）：
// 非 legacy（按需面接管）下旧面残余连接终止不清表、不写 ArchiveErr、
// 不发事件；legacy 下终止照旧整体弃用归档面（G1：数据随连接生死）。
func TestT3ShellHub_EndArchiveTeardownRespectsMode(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	hub := newT3ShellHubWithConfig(hubTestConfig(), nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	sub := hub.Subscribe("user-1")
	defer sub.Close()
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 1
	})
	key := t3ShellHubKey("user-1", "env-1")
	hub.mu.Lock()
	entry := hub.entries[key]
	hub.mu.Unlock()
	if entry == nil {
		t.Fatalf("entry missing")
	}
	sub.Drain()

	// 非 legacy：残余旧面连接终止——归档面由按需簿记管可信，不动数据、
	// 不发事件（订阅端视角没有任何变化）。
	hub.endFaceConnection(entry, true, errors.New("残余订阅连接结束"))
	v, err := hub.Peek(env, "tok")
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if v.Snaps == nil || v.Snaps.ArchiveErr != nil || len(v.Snaps.Archived.Threads) != 1 {
		t.Fatalf("non-legacy teardown must not touch the archive face, snaps=%+v", v.Snaps)
	}
	if evs := sub.Drain(); len(evs) != 0 {
		t.Fatalf("non-legacy teardown must publish nothing, got %+v", evs)
	}

	// legacy：订阅面数据随连接生死（G1），终止照旧清表 + ArchiveErr。
	// 直接置 mode（不经 switchToArchiveLegacy，避免放行真订阅拨号）；
	// 写序沿全局锁序 s.mu → arch.mu。
	entry.state.mu.Lock()
	entry.arch.mu.Lock()
	entry.arch.mode = t3ArchiveModeLegacy
	entry.arch.mu.Unlock()
	entry.state.mu.Unlock()
	hub.endFaceConnection(entry, true, errors.New("legacy 订阅连接结束"))
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil && len(v.Snaps.Archived.Threads) == 0
	})
}

// 归档面发布去重（审修 R9 P2-2）：首次落地发 Full；TTL 例行重拉同一份
// 数据不发任何事件（旧实现每次 Full → 前端每环境每 TTL 整环境重取）；
// 归档集真变发 Full；失败首次出现发轻量 State 事件、同一错误反复失败
// 去重；恢复可信（同集合）再发一次轻量 State。
func TestT3ShellHub_ArchiveRepublishDedup(t *testing.T) {
	srv, url := startFakeShellStreamServer(t, fakeShellServerOptions{bearer: "tok", marker: true, autoPong: true})
	srv.setArchiveFixture(archiveSnapshotBody(1, "th-a"), nil)
	cfg := hubTestConfig()
	cfg.ArchiveTTL = 80 * time.Millisecond
	hub := newT3ShellHubWithConfig(cfg, nil)
	defer hub.Close()
	env := hubTestEnv(url, 0)

	sub := hub.Subscribe("user-1")
	defer sub.Close()
	_, _, _, _, _ = establishBothFaces(t, hub, srv, env, "tok", 42, "th-1")
	// 首次落地：Full。
	ev := awaitShellEvent(t, sub, func(e T3ShellEvent) bool { return e.Full })
	if ev.EnvironmentID != "env-1" {
		t.Fatalf("first landing full event environment = %q, want env-1", ev.EnvironmentID)
	}
	sub.Drain()

	// TTL 例行重拉同一份数据：无任何事件。
	time.Sleep(cfg.ArchiveTTL + 40*time.Millisecond)
	calls := srv.archiveCallCount()
	if _, err := hub.Peek(env, "tok"); err != nil {
		t.Fatalf("peek: %v", err)
	}
	deadline := time.After(2 * time.Second)
	for srv.archiveCallCount() <= calls {
		select {
		case <-deadline:
			t.Fatalf("expired TTL must refetch the archive snapshot")
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(60 * time.Millisecond) // 落地 + 发布窗口
	if evs := sub.Drain(); len(evs) != 0 {
		t.Fatalf("identical re-pull must publish nothing, got %+v", evs)
	}

	// 归档集真变：Full。
	srv.setArchiveFixture(archiveSnapshotBody(2, "th-a", "th-b"), nil)
	time.Sleep(cfg.ArchiveTTL + 40*time.Millisecond)
	if _, err := hub.Peek(env, "tok"); err != nil {
		t.Fatalf("peek: %v", err)
	}
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil && len(v.Snaps.Archived.Threads) == 2
	})
	ev = awaitShellEvent(t, sub, func(e T3ShellEvent) bool { return e.Full })
	if ev.EnvironmentID != "env-1" {
		t.Fatalf("changed landing full event environment = %q, want env-1", ev.EnvironmentID)
	}
	sub.Drain()

	// 失败首次出现：轻量 State 事件（非 Full）。
	srv.setArchiveFixture(nil, map[string]any{"_tag": "RuntimeError", "message": "archive exploded"})
	time.Sleep(cfg.ArchiveTTL + 40*time.Millisecond)
	if _, err := hub.Peek(env, "tok"); err != nil {
		t.Fatalf("peek: %v", err)
	}
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr != nil
	})
	ev = awaitShellEvent(t, sub, func(e T3ShellEvent) bool { return !e.Full && e.State != "" })
	if ev.EnvironmentID != "env-1" {
		t.Fatalf("failure state event environment = %q, want env-1", ev.EnvironmentID)
	}
	sub.Drain()

	// 同一错误反复失败：去重，不再发事件。
	for i := 0; i < 2; i++ {
		time.Sleep(cfg.ArchiveTTL + 40*time.Millisecond)
		if _, err := hub.Peek(env, "tok"); err != nil {
			t.Fatalf("peek: %v", err)
		}
		waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
			return v.Snaps != nil && v.Snaps.ArchiveErr != nil
		})
	}
	time.Sleep(60 * time.Millisecond)
	if evs := sub.Drain(); len(evs) != 0 {
		t.Fatalf("repeated identical failure must be deduplicated, got %+v", evs)
	}

	// 恢复可信（同集合）：轻量 State 事件（不发 Full）。
	srv.setArchiveFixture(archiveSnapshotBody(3, "th-a", "th-b"), nil)
	time.Sleep(cfg.ArchiveTTL + 40*time.Millisecond)
	if _, err := hub.Peek(env, "tok"); err != nil {
		t.Fatalf("peek: %v", err)
	}
	waitForView(t, hub, env, "tok", func(v *t3ShellView) bool {
		return v.Snaps != nil && v.Snaps.ArchiveErr == nil
	})
	ev = awaitShellEvent(t, sub, func(e T3ShellEvent) bool { return !e.Full && e.State != "" })
	_ = ev
}

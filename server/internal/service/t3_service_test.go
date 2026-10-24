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

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"golang.org/x/net/websocket"
	"gorm.io/gorm"
)

// t3ErrCode 取业务错误码（errors 包没有 Code 助手）。
func t3ErrCode(err error) int {
	var appErr *errs.AppError
	if errors.As(err, &appErr) {
		return appErr.Code
	}
	return -1
}

// fakeT3Environment 在一个 httptest.Server 上同时模拟 T3 的 HTTP 面与 WS RPC 面，
// 信封/方法名/路径都按真实协议（packages/contracts、apps/server/src/ws.ts）。
// INT-116 语义：shell 状态来自 hub 的两条长驻订阅（活跃/归档各一条连接），
// 所以 WS 面是「按 requestId 分派 + 逐 requestId Ack 门控」的统一读循环——
// 同一条连接上要能先 server.getConfig 再挂订阅流，订阅注册后保持 live 等推送；
// 归档数据由 orchestration.subscribeArchivedShell 的流式全量快照提供（真实
// 活跃快照的 archivedThreads 恒为空）；项目表在 HTTP 面提供（GET /api/projects）。
// 线程投影按 wire 形状携带 visibleTurnItems 且裁掉命令输出。
type fakeT3Environment struct {
	t  *testing.T
	mu sync.Mutex

	baseURL         string
	pairingCodes    map[string]bool          // 有效配对码
	validBearers    map[string]bool          // 有效 Bearer
	grantScope      string                   // /oauth/token 回的 scope；默认宽 scope（标准客户端授权）
	snapshot        json.RawMessage          // subscribeShell 的权威活跃快照流项（{"kind":"snapshot",...}）
	archive         json.RawMessage          // 归档订阅的全量快照体（无 kind 包装）
	archiveFail     bool                     // true 时归档订阅回 Exit Failure（G1 降级路径）
	archiveGate     chan struct{}            // 非空时归档订阅先等放行再发快照（制造「归档未就绪」窗口）
	dropAfterActive bool                     // true 时活跃快照发出后立即断连
	projects        json.RawMessage          // GET /api/projects 响应体
	projections     map[string]string        // threadId -> wire 投影 JSON
	blockProjection map[string]chan struct{} // threadId -> 非空时投影请求挂起直到关闭或客户端断开（测预算中途杀掉）
	turnItems       map[string]string        // itemId -> getTurnItem 返回的完整条目 JSON

	// 订阅流句柄：hub 每条连接至多一条流，测试经这两个引用推送增量驱动状态。
	// 多环境共用一个 fake 时以最后注册者为准（测试没有这种用法）。
	activeStream  *fakeT3WSStream
	archiveStream *fakeT3WSStream
	conns         []*websocket.Conn // 已建立的 WS 连接（dropConnections 用）
	nextSeq       int               // 推送增量的序号基线（高于所有 fixture 的 snapshotSequence）

	// 写路径（INT-85）可配置响应
	configValue    json.RawMessage   // server.getConfig 返回值
	settingsValue  json.RawMessage   // server.getSettings 返回值
	vcsStatus      json.RawMessage   // vcs.refreshStatus 返回值
	vcsRefs        json.RawMessage   // vcs.listRefs 返回值（单页）
	vcsRefsPages   []json.RawMessage // 非空时按调用顺序弹出，用于跟页测试
	uploaded       map[string][]byte // attachmentId -> 上传的字节（POST /api/attachments/upload/*）
	assetContent   map[string][]byte // attachmentId -> 下载字节（GET /api/assets/<id>）
	assetMissing   bool              // true 时 assets.createUrl 回 AssetAttachmentNotFoundError
	secretFail     string            // 非空时 secrets.answerRequest 回该 reason 的 SecretRequestError
	secretAnswers  []map[string]any  // secrets.answerRequest 收到的完整 payload（验证转发）
	deletedUploads []string          // attachments.delete 收到的 attachmentId

	gotTokenForms []map[string]string
	gotRPC        []t3RecordedRPC // WS 请求（方法+payload，含订阅）

	tokenGate chan struct{} // 非空时 /oauth/token 阻塞直到关闭（并发交错测试用）

	// onDispatch 非空时在收到 orchestration.dispatchCommand 后同步调用（先于
	// 回执写出）：用于让 send 一类写操作在受理瞬间同步改变快照，复现
	// 「写操作生效与 wait 采基线」的先后竞态。
	onDispatch func(payload json.RawMessage)
}

// fakeT3WSStream 是 fake 上一条已注册的订阅流：写帧经连接级互斥串行化，
// 每推一个 Chunk 都等服务端（被测 hub）的 Ack 才发下一块（协议门控）。
type fakeT3WSStream struct {
	face  string // active | archive
	id    any    // 订阅请求 requestId
	write func(frame any) error

	mu    sync.Mutex
	acked chan struct{} // 最近一个 Chunk 的 Ack 信号
}

// ackLatest 由连接读循环在收到匹配 requestId 的 Ack 时调用。
func (st *fakeT3WSStream) ackLatest() {
	st.mu.Lock()
	if st.acked != nil {
		close(st.acked)
		st.acked = nil
	}
	st.mu.Unlock()
}

// push 推送一个 Chunk 并等待 Ack；上一块未 Ack 前不发下一块。
func (st *fakeT3WSStream) push(values ...json.RawMessage) error {
	st.mu.Lock()
	if err := st.write(t3ChunkFrame(st.id, values)); err != nil {
		st.mu.Unlock()
		return err
	}
	gate := make(chan struct{})
	st.acked = gate
	st.mu.Unlock()
	select {
	case <-gate:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("等待订阅流 Ack 超时")
	}
}

type t3RecordedRPC struct {
	method  string
	payload json.RawMessage
}

func newFakeT3Environment(t *testing.T) *fakeT3Environment {
	return &fakeT3Environment{
		t:            t,
		pairingCodes: map[string]bool{},
		validBearers: map[string]bool{},
		// 官方配对链接缺省授予标准客户端 scope（含 orchestration:operate）。
		grantScope: t3ScopeOrchestrationRead + " " + t3ScopeOrchestrationOperate,
		// 空的权威活跃快照（stream item 形状）与空的归档快照体：未定制 fixture
		// 时 hub 的订阅也能正常就绪，不会把空帧当坏帧。
		snapshot: mustJSON(map[string]any{
			"kind": "snapshot",
			"snapshot": map[string]any{
				"schemaVersion": 1, "snapshotSequence": 0,
				"projects": []any{}, "threads": []any{}, "archivedThreads": []any{},
			},
		}),
		archive:       mustJSON(map[string]any{"schemaVersion": 1, "snapshotSequence": 0, "projects": []any{}, "threads": []any{}}),
		projects:      mustJSON(map[string]any{"projects": []any{}}),
		nextSeq:       1000, // 高于所有 fixture 的 snapshotSequence（增量只前进不回卷）
		projections:   map[string]string{},
		turnItems:     map[string]string{},
		configValue:   mustJSON(map[string]any{"providers": map[string]any{}, "shellResumeCompletionMarker": true}),
		settingsValue: mustJSON(map[string]any{"newWorktreesStartFromOrigin": true}),
		vcsStatus:     mustJSON(map[string]any{"isRepo": true, "refName": "main", "hasWorkingTreeChanges": false, "hasPrimaryRemote": true}),
		vcsRefs: mustJSON(map[string]any{
			"refs":   []any{map[string]any{"name": "main", "isRemote": false, "isDefault": true, "worktreePath": nil}},
			"isRepo": true, "hasPrimaryRemote": true, "nextCursor": nil, "totalCount": 1,
		}),
		uploaded:       map[string][]byte{},
		assetContent:   map[string][]byte{},
		secretAnswers:  []map[string]any{},
		deletedUploads: []string{},
	}
}

func (f *fakeT3Environment) issuePairingCode() string {
	code := "pair-" + uuid.NewString()
	f.mu.Lock()
	f.pairingCodes[code] = true
	f.mu.Unlock()
	return code
}

func (f *fakeT3Environment) pairingURL(base string) string {
	// 官方 t3 pair 输出形态：web origin + /pair 路径 + hash 里的 token。
	return strings.TrimSuffix(base, "/") + "/pair#token=" + f.issuePairingCode()
}

func (f *fakeT3Environment) revokeBearers() {
	f.mu.Lock()
	f.validBearers = map[string]bool{}
	f.mu.Unlock()
}

func (f *fakeT3Environment) recordedRPC() []t3RecordedRPC {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]t3RecordedRPC(nil), f.gotRPC...)
}

// uploadedAttachment 取 POST 到签名上传端点的字节。
func (f *fakeT3Environment) uploadedAttachment() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, body := range f.uploaded {
		return body
	}
	return nil
}

// lastSecretAnswer 取最近一次 secrets.answerRequest 的完整 payload。
func (f *fakeT3Environment) lastSecretAnswer() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.secretAnswers) == 0 {
		return nil
	}
	return f.secretAnswers[len(f.secretAnswers)-1]
}

// deletedAttachmentIDs 取 attachments.delete 收到的 ID 列表。
func (f *fakeT3Environment) deletedAttachmentIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deletedUploads...)
}

func (f *fakeT3Environment) tokenFormCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.gotTokenForms)
}

// ------ 订阅流驱动（推送增量 / 断连 / 计数） ------

// pushActive 在最近注册的活跃订阅流上推送流项并等待 Ack。
func (f *fakeT3Environment) pushActive(t *testing.T, values ...json.RawMessage) {
	t.Helper()
	f.mu.Lock()
	st := f.activeStream
	f.mu.Unlock()
	if st == nil {
		t.Fatalf("no active subscription stream to push on")
	}
	if err := st.push(values...); err != nil {
		t.Fatalf("push active values: %v", err)
	}
}

// nextPushSeq 取下一个增量序号（高于快照序号，只前进不回卷）。
func (f *fakeT3Environment) nextPushSeq() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextSeq++
	return f.nextSeq
}

// pushThreadDelta 推送 thread.updated{location:active} 增量：上游活跃流只产生
// active 位置的增量（ShellStream.ts:292-320），归档/恢复由归档订阅自己驱动。
func (f *fakeT3Environment) pushThreadDelta(t *testing.T, thread map[string]any) {
	t.Helper()
	value := mustJSON(map[string]any{
		"kind": "thread.updated", "sequence": f.nextPushSeq(), "location": "active", "thread": thread,
	})
	f.pushActive(t, value)
}

// pushThreadRemoval 推送 thread.removed{location:active} 增量。
func (f *fakeT3Environment) pushThreadRemoval(t *testing.T, threadID string) {
	t.Helper()
	value := mustJSON(map[string]any{
		"kind": "thread.removed", "sequence": f.nextPushSeq(), "location": "active", "threadId": threadID,
	})
	f.pushActive(t, value)
}

// dropConnections 服务端主动断开全部 WS 连接（模拟吊销会话/进程重启后的
// 连接死亡；hub 侧表现为读失败判死，随后按各自分类重连）。
func (f *fakeT3Environment) dropConnections() {
	f.mu.Lock()
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

// subscribeShellCount 统计记录到的 subscribeShell 请求数（含恢复续订）：
// 热路径复用断言用——多次列表只应发生一次冷启动全量订阅。
func (f *fakeT3Environment) subscribeShellCount() int {
	n := 0
	for _, rpc := range f.recordedRPC() {
		if rpc.method == t3RPCSubscribeShell {
			n++
		}
	}
	return n
}

// awaitPrewarmSubscriptions 等环境的常驻订阅（INT-132：Create 落库即预热）
// 完成两条订阅，返回当前 RPC 记录条数。调用方以它圈出「被测调用」的观察
// 窗口，让「该路径不订阅」的断言不被预热流量污染。
func awaitPrewarmSubscriptions(t *testing.T, f *fakeT3Environment) int {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		shell, archived := false, false
		for _, rpc := range f.recordedRPC() {
			switch rpc.method {
			case t3RPCSubscribeShell:
				shell = true
			case t3RPCSubscribeArchivedShell:
				archived = true
			}
		}
		if shell && archived {
			return len(f.recordedRPC())
		}
		select {
		case <-deadline:
			t.Fatalf("prewarm subscriptions never settled")
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (f *fakeT3Environment) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
			f.handleToken(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/auth/session":
			f.handleSession(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/projects":
			f.handleProjects(w, r)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/orchestration/threads/"):
			f.handleThreadSnapshot(w, r)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/attachments/upload/"):
			f.handleAttachmentUpload(w, r)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/assets/"):
			f.handleAssetDownload(w, r)
		case r.URL.Path == "/ws":
			f.handleWS(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

// handleAttachmentUpload 模拟 T3 的签名上传端点：Content-Length 必须非零，
// 204 接收字节（真实形状：http.ts layerAttachmentUploadRoute）。
func (f *fakeT3Environment) handleAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength <= 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Content-Length must match the upload size."))
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if int64(len(body)) != r.ContentLength {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.uploaded[r.URL.Path] = body
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// handleAssetDownload 模拟签名资产下载端点。
func (f *fakeT3Environment) handleAssetDownload(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	content, ok := f.assetContent[strings.TrimPrefix(r.URL.Path, "/api/assets/")]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(content)
}

func (f *fakeT3Environment) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	formValues := map[string]string{}
	for k := range r.PostForm {
		formValues[k] = r.PostForm.Get(k)
	}
	f.mu.Lock()
	f.gotTokenForms = append(f.gotTokenForms, formValues)
	code := formValues["subject_token"]
	valid := f.pairingCodes[code]
	grantScope := f.grantScope
	if valid {
		delete(f.pairingCodes, code) // 一次性
		f.validBearers["bearer-"+code] = true
	}
	gate := f.tokenGate
	f.mu.Unlock()

	if gate != nil {
		<-gate // 交由测试控制兑换时机，制造 rename/delete 与重配的交错
	}

	if !valid {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"auth_invalid","reason":"invalid_credential","traceId":"t-fake","message":"The environment rejected this client's credentials (invalid_credential)."}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token":      "bearer-" + code,
		"issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
		"token_type":        "Bearer",
		"expires_in":        3600,
		"scope":             grantScope,
	})
}

func (f *fakeT3Environment) handleSession(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	valid := f.validBearers[token]
	f.mu.Unlock()
	if !valid {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"auth_invalid","reason":"invalid_credential","traceId":"t-fake"}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"authenticated": true,
		"auth": map[string]any{
			"policy":            "remote-reachable",
			"bootstrapMethods":  []string{"one-time-token"},
			"sessionMethods":    []string{"bearer-access-token"},
			"sessionCookieName": "t3_session",
		},
		"scopes":        []string{"orchestration:read"},
		"sessionMethod": "bearer-access-token",
	})
}

func (f *fakeT3Environment) handleThreadSnapshot(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	threadID := strings.TrimPrefix(r.URL.Path, "/api/orchestration/threads/")
	f.mu.Lock()
	valid := f.validBearers[token]
	projection, ok := f.projections[threadID]
	block := f.blockProjection[threadID]
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-r.Context().Done():
			return
		}
	}
	if !valid {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"auth_invalid","reason":"invalid_credential","traceId":"t-fake"}`))
		return
	}
	if r.Header.Get("x-t3-orchestration-protocol") != "2" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"invalid_request","reason":"invalid_command","traceId":"t-fake"}`))
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","reason":"thread_not_found","traceId":"t-fake","message":"The environment could not find what this request named (thread_not_found)."}`))
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"snapshotSequence": 1, "projection": json.RawMessage(projection)})
}

// handleProjects 模拟 GET /api/projects（启动/绑定路径的项目表，INT-116）。
func (f *fakeT3Environment) handleProjects(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	valid := f.validBearers[token]
	projects := f.projects
	f.mu.Unlock()
	if !valid {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"auth_invalid","reason":"invalid_credential","traceId":"t-fake"}`))
		return
	}
	_, _ = w.Write(projects)
}

func (f *fakeT3Environment) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("orchestrationProtocol") != "2" {
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"code":"orchestration_protocol_incompatible","message":"Update this client to one that supports orchestration protocol 2.","orchestrationProtocolVersion":2}`))
		return
	}
	websocket.Handler(f.serveWS).ServeHTTP(w, r)
}

// serveWS 是统一读循环：按 requestId 分派请求帧（同一连接可先 getConfig 再挂
// 订阅流），Ack 路由到对应流的门控，订阅注册后保持 live 等推送——hub 的两条
// 长驻连接各持一条流，一次性 RPC（详情补读/写命令）各自开新连接。
func (f *fakeT3Environment) serveWS(conn *websocket.Conn) {
	defer conn.Close()
	token := strings.TrimPrefix(conn.Request().Header.Get("Authorization"), "Bearer ")
	f.mu.Lock()
	valid := f.validBearers[token]
	turnItems := make(map[string]string, len(f.turnItems))
	for id, item := range f.turnItems {
		turnItems[id] = item
	}
	f.conns = append(f.conns, conn)
	f.mu.Unlock()
	// 连接结束只注销本连接注册的全局流引用（其它连接——hub 两条长驻连接、
	// 一次性 RPC 连接——的引用不能被顺手清掉）。
	var myActive, myArchive *fakeT3WSStream
	defer func() {
		f.mu.Lock()
		if myActive != nil && f.activeStream == myActive {
			f.activeStream = nil
		}
		if myArchive != nil && f.archiveStream == myArchive {
			f.archiveStream = nil
		}
		f.mu.Unlock()
	}()

	// 连接级写互斥：订阅流推送（测试 goroutine）与一次性应答（本循环）共用一条连接。
	var writeMu sync.Mutex
	write := func(frame any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return sendT3Frame(conn, frame)
	}
	streams := map[any]*fakeT3WSStream{} // requestId → 订阅流（同一连接至多一条）

	for {
		var data []byte
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := websocket.Message.Receive(conn, &data); err != nil {
			return
		}
		var frame struct {
			Tag       string          `json:"_tag"`
			ID        any             `json:"id"`
			Method    string          `json:"tag"`
			Payload   json.RawMessage `json:"payload"`
			RequestID any             `json:"requestId"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			return
		}
		switch frame.Tag {
		case "Request":
			if !valid {
				_ = write(map[string]any{
					"_tag":      "Exit",
					"requestId": frame.ID,
					"exit": map[string]any{"_tag": "Failure", "cause": []any{map[string]any{
						"_tag": "Fail",
						"error": map[string]any{
							"_tag":    "EnvironmentAuthInvalidError",
							"code":    "auth_invalid",
							"reason":  "invalid_credential",
							"traceId": "t-fake",
						},
					}}},
				})
				return
			}
			f.mu.Lock()
			f.gotRPC = append(f.gotRPC, t3RecordedRPC{method: frame.Method, payload: frame.Payload})
			f.mu.Unlock()
			switch frame.Method {
			case t3RPCSubscribeShell:
				var payload struct {
					AfterSequence *int `json:"afterSequence"`
				}
				_ = json.Unmarshal(frame.Payload, &payload)
				st := &fakeT3WSStream{face: "active", id: frame.ID, write: write}
				streams[frame.ID] = st
				myActive = st
				f.mu.Lock()
				f.activeStream = st
				f.mu.Unlock()
				if payload.AfterSequence == nil {
					// 冷启动：权威全量快照 + synchronized 标记（上游顺序：初始
					// 快照之后、live 之前，ws.ts:1100-1105）。同一 Chunk 内多个
					// value 按序应用、共享一次 Ack。
					f.mu.Lock()
					snapshot := f.snapshot
					f.mu.Unlock()
					values := []json.RawMessage{snapshot, json.RawMessage(`{"kind":"synchronized"}`)}
					if err := write(t3ChunkFrame(frame.ID, values)); err != nil {
						return
					}
					if f.dropAfterActiveFlag() {
						// 快照已送达后断连：覆盖「快照到手后连接死亡不丢活跃面、
						// 自动以 afterSequence 恢复」的路径。只作用于冷启动——
						// 恢复连接不断连，否则每次续订都死循环重连。
						return
					}
				} else {
					// 恢复（afterSequence 续订）：上游回放缺口后发标记；fake 无
					// 缺口可放，直接标记追平进入 live，游标保持不回卷。
					if err := write(t3ChunkFrame(frame.ID, []json.RawMessage{json.RawMessage(`{"kind":"synchronized"}`)})); err != nil {
						return
					}
				}
			case t3RPCSubscribeArchivedShell:
				f.mu.Lock()
				gate := f.archiveGate
				f.mu.Unlock()
				if gate != nil {
					// 归档未就绪窗口：订阅已受理但不发快照，等测试放行。
					select {
					case <-gate:
					case <-time.After(5 * time.Second):
						return
					}
				}
				f.mu.Lock()
				archiveFail := f.archiveFail
				archive := f.archive
				f.mu.Unlock()
				if archiveFail {
					// 订阅请求的 Exit Failure：归档面读取失败（G1 降级路径）。
					_ = write(exitFailureFrame(frame.ID, map[string]any{
						"_tag":    "UnknownException",
						"message": "archived shell snapshot unavailable",
					}))
					continue
				}
				st := &fakeT3WSStream{face: "archive", id: frame.ID, write: write}
				streams[frame.ID] = st
				myArchive = st
				f.mu.Lock()
				f.archiveStream = st
				f.mu.Unlock()
				// 归档订阅先全量快照再增量（ws.ts:1777-1811）；快照体包装成流项。
				item := mustJSON(map[string]any{"kind": "snapshot", "snapshot": json.RawMessage(archive)})
				if err := write(t3ChunkFrame(frame.ID, []json.RawMessage{item})); err != nil {
					return
				}
			case "orchestration.getTurnItem":
				var payload struct {
					ItemID string `json:"itemId"`
				}
				_ = json.Unmarshal(frame.Payload, &payload)
				item, ok := turnItems[payload.ItemID]
				if !ok || item == "" {
					_ = write(exitSuccessFrame(frame.ID, json.RawMessage(`{"item":null}`)))
					continue
				}
				_ = write(exitSuccessFrame(frame.ID, json.RawMessage(`{"item":`+item+`}`)))
			case "orchestration.dispatchCommand":
				f.mu.Lock()
				hook := f.onDispatch
				f.mu.Unlock()
				if hook != nil {
					hook(frame.Payload)
				}
				// 一次性命令受理：真实形状 {"sequence":N}。
				_ = write(exitSuccessFrame(frame.ID, json.RawMessage(`{"sequence":1}`)))
			case "orchestration.launchThread":
				_ = write(exitSuccessFrame(frame.ID, json.RawMessage(`{"threadId":"th-launched","resumed":false}`)))
			case "server.getConfig":
				f.mu.Lock()
				value := f.configValue
				f.mu.Unlock()
				_ = write(exitSuccessFrame(frame.ID, value))
			case "server.getSettings":
				f.mu.Lock()
				value := f.settingsValue
				f.mu.Unlock()
				_ = write(exitSuccessFrame(frame.ID, value))
			case "vcs.refreshStatus":
				f.mu.Lock()
				value := f.vcsStatus
				f.mu.Unlock()
				_ = write(exitSuccessFrame(frame.ID, value))
			case "vcs.listRefs":
				f.mu.Lock()
				value := f.vcsRefs
				if len(f.vcsRefsPages) > 0 {
					value = f.vcsRefsPages[0]
					f.vcsRefsPages = f.vcsRefsPages[1:]
				}
				f.mu.Unlock()
				_ = write(exitSuccessFrame(frame.ID, value))
			case "attachments.createUploadUrl":
				_ = write(exitSuccessFrame(frame.ID, json.RawMessage(
					`{"attachmentId":"att-1","relativeUrl":"/api/attachments/upload/fake-token","expiresAt":9999999999999}`)))
			case "attachments.delete":
				var payload struct {
					AttachmentID string `json:"attachmentId"`
				}
				_ = json.Unmarshal(frame.Payload, &payload)
				f.mu.Lock()
				f.deletedUploads = append(f.deletedUploads, payload.AttachmentID)
				f.mu.Unlock()
				_ = write(exitSuccessFrame(frame.ID, json.RawMessage(`null`)))
			case "assets.createUrl":
				f.mu.Lock()
				missing := f.assetMissing
				f.mu.Unlock()
				if missing {
					_ = write(exitFailureFrame(frame.ID, map[string]any{
						"_tag":    "AssetAttachmentNotFoundError",
						"message": "Attachment was not found.",
					}))
					continue
				}
				_ = write(exitSuccessFrame(frame.ID, json.RawMessage(`{"relativeUrl":"/api/assets/att-1","expiresAt":9999999999999}`)))
			case "secrets.answerRequest":
				var payload map[string]any
				_ = json.Unmarshal(frame.Payload, &payload)
				f.mu.Lock()
				f.secretAnswers = append(f.secretAnswers, payload)
				failReason := f.secretFail
				f.mu.Unlock()
				if failReason != "" {
					_ = write(exitFailureFrame(frame.ID, map[string]any{
						"_tag":    "SecretRequestError",
						"reason":  failReason,
						"message": "This secret request was already answered.",
						"cause":   map[string]any{"echoed": "upstream-defect"},
					}))
					continue
				}
				_ = write(exitSuccessFrame(frame.ID, json.RawMessage(`null`)))
			default:
				_ = write(exitSuccessFrame(frame.ID, json.RawMessage(`null`)))
			}
		case "Ack":
			// 逐 requestId 路由：只唤醒等这块 Ack 的流。
			f.mu.Lock()
			st := streams[frame.RequestID]
			f.mu.Unlock()
			if st != nil {
				st.ackLatest()
			}
		case "Interrupt":
			// 订阅流被中断：注销流，连接保留给同会话后续请求。
			delete(streams, frame.RequestID)
		case "Ping":
			// Effect RPC 应用层心跳：回 Pong（RpcServer.ts:806）。
			_ = write(map[string]any{"_tag": "Pong"})
		}
	}
}

// dropAfterActiveFlag 读取断连开关。
func (f *fakeT3Environment) dropAfterActiveFlag() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dropAfterActive
}

// ------ 测试基建 ------

type t3TestEnv struct {
	db   *gorm.DB
	cfg  *config.Config
	hub  *t3ShellHub
	env  *T3EnvironmentService
	th   *T3ThreadsService
	fake *fakeT3Environment
}

func setupT3ServiceTest(t *testing.T) *t3TestEnv {
	t.Helper()

	fake := newFakeT3Environment(t)
	server := httptest.NewServer(fake.handler())
	t.Cleanup(server.Close)
	fake.baseURL = server.URL

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.T3Environment{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_t3_environments_user_name ON t3_environments(user_id, name)")

	cfg := &config.Config{
		JWT: config.JWTConfig{Secret: "test-secret", ExpireHours: 24},
		Encryption: config.EncryptionConfig{
			Key: "12345678901234567890123456789012",
		},
	}
	repo := repository.NewT3EnvironmentRepository(db)
	hub := NewT3ShellHub(zap.NewNop())
	t.Cleanup(hub.Close)
	th := NewT3ThreadsService(repo, cfg, zap.NewNop(), hub)
	// 测试预算：秒级封顶，坏环境的隔离不需要真等默认预算。
	// ShellFirstSync 给足本地 fake 完成订阅的时间。
	th.budgets = t3RemoteBudgets{Round: 5 * time.Second, PerEnvironment: 2 * time.Second, ThreadDetail: 5 * time.Second, ItemDetail: 5 * time.Second, ShellFirstSync: 2 * time.Second}
	return &t3TestEnv{
		db:   db,
		cfg:  cfg,
		hub:  hub,
		env:  NewT3EnvironmentService(repo, cfg, zap.NewNop(), hub),
		th:   th,
		fake: fake,
	}
}

func (e *t3TestEnv) createUser(t *testing.T) string {
	t.Helper()
	id := uuid.NewString()
	if err := e.db.Create(&model.User{ID: id, Username: "u" + id[:8], Email: id[:8] + "@example.com", PasswordHash: "x", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

// shellThread 造一个活跃 shell 线程（真实字段形状）。
func shellThread(id, title, status string, extra map[string]any) map[string]any {
	thread := map[string]any{
		"id":                 id,
		"projectId":          "p-1",
		"title":              title,
		"providerInstanceId": "claude-pro",
		"modelSelection":     map[string]any{"instanceId": "claude-pro", "model": "claude-sonnet-5-5"},
		"branch":             "t3/threads",
		"status":             status,
		// 真实 ThreadPullRequestLink 形状：投影要求 repository 身份（缺身份的条目
		// 会被当作坏数据跳过，不会进 pull_requests / pull_request_count）。
		"pullRequests": []any{
			map[string]any{"host": "github.com", "repository": "o/r", "number": 1, "url": "https://github.com/o/r/pull/1", "source": "manual"},
			map[string]any{"host": "github.com", "repository": "o/r", "number": 2, "url": "https://github.com/o/r/pull/2", "source": "created"},
		},
		"updatedAt": "2026-10-12T03:00:00.000Z",
	}
	for k, v := range extra {
		thread[k] = v
	}
	return thread
}

// ------ 用例 ------

func TestT3EnvironmentLifecycle(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	// 配对：官方 /pair#token= 形态、一次性码换 Bearer、地址规范化为 origin、凭证加密落库
	created, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "工作站", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Status != T3EnvironmentStatusOK {
		t.Fatalf("expected ok status after pairing, got %v (%s)", created.Status, api.Deref(created.Error))
	}
	if created.Address != e.fake.baseURL+"/" {
		t.Fatalf("address = %q", created.Address)
	}

	stored, err := e.env.repo.FindByIDAndUserID(ctx, created.Id, userID)
	if err != nil {
		t.Fatalf("load env: %v", err)
	}
	if len(stored.BearerTokenEncrypted) == 0 {
		t.Fatalf("expected encrypted bearer")
	}
	plain, err := crypto.Decrypt(stored.BearerTokenEncrypted, []byte(e.cfg.Encryption.Key))
	if err != nil || string(plain) == "" {
		t.Fatalf("decrypt bearer: %v", err)
	}
	if stored.BearerScope != e.fake.grantScope {
		t.Fatalf("scope = %q, want granted %q", stored.BearerScope, e.fake.grantScope)
	}
	if !t3HasOperateScope(stored.BearerScope) {
		t.Fatalf("wide grant must carry operate")
	}
	if strings.Contains(string(stored.BearerTokenEncrypted), "bearer-") {
		t.Fatalf("bearer stored in plaintext")
	}

	// 兑换请求字段按真实协议：不携带 scope（授予由配对链接的授权决定）
	if len(e.fake.gotTokenForms) != 1 {
		t.Fatalf("token exchanges = %d", len(e.fake.gotTokenForms))
	}
	form := e.fake.gotTokenForms[0]
	if _, hasScope := form["scope"]; hasScope {
		t.Fatalf("exchange request must not carry scope, got %q", form["scope"])
	}
	if form["grant_type"] != t3GrantTypeTokenExchange || form["client_label"] != "Fast Ship" {
		t.Fatalf("token form = %+v", form)
	}

	// 重名拒绝
	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "工作站", PairingUrl: e.fake.pairingURL(e.fake.baseURL)}); err == nil || t3ErrCode(err) != errs.ErrT3EnvironmentNameExists.Code {
		t.Fatalf("expected duplicate name conflict, got %v", err)
	}

	// 非法配对链接
	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "坏链接", PairingUrl: "https://no-token.example.com/"}); err == nil || t3ErrCode(err) != errs.ErrPairingURLInvalid.Code {
		t.Fatalf("expected pairing url invalid, got %v", err)
	}

	// 都不传 → 40001
	if _, err := e.env.Update(ctx, userID, created.Id, &UpdateT3EnvironmentRequest{}); err == nil || t3ErrCode(err) != errs.ErrInvalidParams.Code {
		t.Fatalf("expected 40001 for empty update, got %v", err)
	}

	// 改名
	updated, err := e.env.Update(ctx, userID, created.Id, &UpdateT3EnvironmentRequest{Name: strPtr("家里的机器")})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	if updated.Name != "家里的机器" {
		t.Fatalf("name = %q", updated.Name)
	}

	// 凭证吊销后 List 探测 → repair
	e.fake.revokeBearers()
	list, err := e.env.List(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if list[0].Status != T3EnvironmentStatusRepair {
		t.Fatalf("expected repair, got %+v", list[0])
	}

	// 重新配对（repair 修复路径）
	repaired, err := e.env.Update(ctx, userID, created.Id, &UpdateT3EnvironmentRequest{PairingUrl: strPtr(e.fake.pairingURL(e.fake.baseURL))})
	if err != nil {
		t.Fatalf("re-pair: %v", err)
	}
	if repaired.Status != T3EnvironmentStatusOK {
		t.Fatalf("expected ok after re-pair, got %+v", repaired)
	}

	// 删除与隔离
	if err := e.env.Delete(ctx, userID, created.Id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := e.env.Delete(ctx, userID, created.Id); err == nil || t3ErrCode(err) != errs.ErrT3EnvironmentNotFound.Code {
		t.Fatalf("expected not found on repeat delete, got %v", err)
	}

	otherUser := e.createUser(t)
	if _, err := e.env.Update(ctx, otherUser, created.Id, &UpdateT3EnvironmentRequest{Name: strPtr("x")}); err == nil || t3ErrCode(err) != errs.ErrT3EnvironmentNotFound.Code {
		t.Fatalf("expected cross-user not found, got %v", err)
	}
}

// TestT3UpdateInterleaves 覆盖 panel r1 #3 的并发交错：
// 重配/改名互不覆盖、重配/删除不复活记录、唯一冲突映射 40912。
func TestT3UpdateInterleaves(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	// —— 重配期间并发改名：改名不被旧名覆盖，重配产物照常落库 ——
	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "原始名", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Create 完成后再立闸门：只拦住后续重配的兑换请求。
	baseline := e.fake.tokenFormCount()
	e.fake.mu.Lock()
	e.fake.tokenGate = make(chan struct{})
	e.fake.mu.Unlock()
	repairDone := make(chan error, 1)
	go func() {
		_, err := e.env.Update(ctx, userID, env.Id, &UpdateT3EnvironmentRequest{PairingUrl: strPtr(e.fake.pairingURL(e.fake.baseURL))})
		repairDone <- err
	}()
	// 等 token 兑换进入阻塞段后再改名。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && e.fake.tokenFormCount() <= baseline {
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := e.env.Update(ctx, userID, env.Id, &UpdateT3EnvironmentRequest{Name: strPtr("并发新名")}); err != nil {
		t.Fatalf("concurrent rename: %v", err)
	}
	close(e.fake.tokenGate)
	if err := <-repairDone; err != nil {
		t.Fatalf("re-pair failed: %v", err)
	}
	final, err := e.env.repo.FindByIDAndUserID(ctx, env.Id, userID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if final.Name != "并发新名" {
		t.Fatalf("rename lost by re-pair, name = %q", final.Name)
	}
	plain, err := crypto.Decrypt(final.BearerTokenEncrypted, []byte(e.cfg.Encryption.Key))
	if err != nil || !strings.HasPrefix(string(plain), "bearer-pair-") {
		t.Fatalf("expected refreshed bearer after re-pair, got %v %q", err, string(plain))
	}

	// —— 重配期间并发删除：条件 UPDATE 0 行 → 40417，且记录不复活 ——
	baseline = e.fake.tokenFormCount()
	e.fake.mu.Lock()
	e.fake.tokenGate = make(chan struct{})
	e.fake.mu.Unlock()
	rePairDone := make(chan error, 1)
	go func() {
		_, err := e.env.Update(ctx, userID, env.Id, &UpdateT3EnvironmentRequest{PairingUrl: strPtr(e.fake.pairingURL(e.fake.baseURL))})
		rePairDone <- err
	}()
	// 等这次兑换进入阻塞段后再删除。
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && e.fake.tokenFormCount() <= baseline {
		time.Sleep(5 * time.Millisecond)
	}
	if err := e.env.Delete(ctx, userID, env.Id); err != nil {
		t.Fatalf("concurrent delete: %v", err)
	}
	close(e.fake.tokenGate)
	if err := <-rePairDone; err == nil || t3ErrCode(err) != errs.ErrT3EnvironmentNotFound.Code {
		t.Fatalf("expected 40417 for re-pair racing deletion, got %v", err)
	}

	var count int64
	e.db.Model(&model.T3Environment{}).Where("id = ?", env.Id).Count(&count)
	if count != 0 {
		t.Fatalf("deleted environment resurrected by in-flight re-pair")
	}

	// —— 唯一索引兜底：绕过预检直接撞唯一约束，服务层映射 40912 ——
	envA, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "A", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "B", PairingUrl: e.fake.pairingURL(e.fake.baseURL)}); err != nil {
		t.Fatalf("create B: %v", err)
	}
	err = e.env.repo.Update(ctx, envA.Id, userID, &repository.T3EnvironmentUpdate{Name: api.Ptr("B"), UpdatedAt: time.Now().UTC()})
	if err == nil || !isUniqueConstraintError(err) {
		t.Fatalf("expected unique violation on concurrent-style rename, got %v", err)
	}
	if mapped := e.env.updateRepoError(err); t3ErrCode(mapped) != errs.ErrT3EnvironmentNameExists.Code {
		t.Fatalf("expected 40912 mapping, got %v", mapped)
	}
}

// TestT3UpdateCanceledBeforeSave ctx 取消后不落库：重配在兑换完成前取消，
// 凭证与地址保持原值；合并更新（改名+重配）取消时两者都不得落库。
func TestT3UpdateCanceledBeforeSave(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "取消机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before, err := e.env.repo.FindByIDAndUserID(ctx, env.Id, userID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.env.Update(canceled, userID, env.Id, &UpdateT3EnvironmentRequest{PairingUrl: strPtr(e.fake.pairingURL(e.fake.baseURL))}); err == nil {
		t.Fatalf("expected error on canceled re-pair")
	}
	if _, err := e.env.Update(canceled, userID, env.Id, &UpdateT3EnvironmentRequest{
		Name:       strPtr("不该落库的新名"),
		PairingUrl: strPtr(e.fake.pairingURL(e.fake.baseURL)),
	}); err == nil {
		t.Fatalf("expected error on canceled merged update")
	}
	after, err := e.env.repo.FindByIDAndUserID(ctx, env.Id, userID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Name != before.Name {
		t.Fatalf("canceled update must not persist the rename, name = %q", after.Name)
	}
	if string(after.BearerTokenEncrypted) != string(before.BearerTokenEncrypted) || after.Address != before.Address {
		t.Fatalf("canceled re-pair must not persist: before=%q after=%q", before.Address, after.Address)
	}
}

// TestT3UpdateAtomic 覆盖审查 W1：合并更新（name + pairing_url）先完成全部
// 校验与远程兑换、再一次条件 UPDATE 落库——中途失败不得留下部分写入。
func TestT3UpdateAtomic(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "原始名", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 无效 pairing_url：整体失败（40005），改名不得落库
	if _, err := e.env.Update(ctx, userID, env.Id, &UpdateT3EnvironmentRequest{
		Name:       strPtr("不该出现的新名"),
		PairingUrl: strPtr("https://no-token.example.com/"),
	}); err == nil || t3ErrCode(err) != errs.ErrPairingURLInvalid.Code {
		t.Fatalf("expected pairing url invalid, got %v", err)
	}
	after, err := e.env.repo.FindByIDAndUserID(ctx, env.Id, userID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Name != "原始名" {
		t.Fatalf("failed merged update must not persist the rename, name = %q", after.Name)
	}

	// name + 有效 pairing_url：一次落库，名字与凭证同时生效
	beforeRow, err := e.env.repo.FindByIDAndUserID(ctx, env.Id, userID)
	if err != nil {
		t.Fatalf("load before merged update: %v", err)
	}
	merged, err := e.env.Update(ctx, userID, env.Id, &UpdateT3EnvironmentRequest{
		Name:       strPtr("一起改"),
		PairingUrl: strPtr(e.fake.pairingURL(e.fake.baseURL)),
	})
	if err != nil {
		t.Fatalf("merged update: %v", err)
	}
	if merged.Name != "一起改" {
		t.Fatalf("merged update name = %q", merged.Name)
	}
	final, err := e.env.repo.FindByIDAndUserID(ctx, env.Id, userID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if final.Name != "一起改" {
		t.Fatalf("merged update must persist the rename, name = %q", final.Name)
	}
	plain, err := crypto.Decrypt(final.BearerTokenEncrypted, []byte(e.cfg.Encryption.Key))
	if err != nil || !strings.HasPrefix(string(plain), "bearer-pair-") {
		t.Fatalf("merged update must persist the re-paired bearer, got %v", err)
	}
	if !final.UpdatedAt.After(beforeRow.UpdatedAt) {
		t.Fatalf("updated_at must advance on merged update: %v -> %v", beforeRow.UpdatedAt, final.UpdatedAt)
	}
}

// TestT3CreateConcurrentSameName 覆盖审查 N1：两个同名 Create 都过了
// FindByName 预检（tokenGate 把兑换拦在同一时刻），第二个插入撞
// (user_id, name) 唯一索引，映射 40912 而非 50000，库里只留一条记录。
func TestT3CreateConcurrentSameName(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.tokenGate = make(chan struct{})
	e.fake.mu.Unlock()

	const name = "撞名"
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: name, PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
			results <- err
		}()
	}
	// 两个请求都进入兑换阻塞段（即都已通过 FindByName 预检）后再放行。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && e.fake.tokenFormCount() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if e.fake.tokenFormCount() < 2 {
		t.Fatalf("both creates should reach the token exchange, got %d", e.fake.tokenFormCount())
	}
	close(e.fake.tokenGate)

	succeeded, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			succeeded++
		case t3ErrCode(err) == errs.ErrT3EnvironmentNameExists.Code:
			conflicts++
		default:
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if succeeded != 1 || conflicts != 1 {
		t.Fatalf("expected exactly one success and one 40912, got %d success / %d conflict", succeeded, conflicts)
	}
	var count int64
	e.db.Model(&model.T3Environment{}).Where("user_id = ? AND name = ?", userID, name).Count(&count)
	if count != 1 {
		t.Fatalf("expected exactly one row, got %d", count)
	}
}

// TestT3ThreadsAggregationAndErrorIsolation 覆盖 panel r1 #7/#8/#9：
// 归档走独立 RPC、子代理线程排除、坏环境不拖垮健康环境。
// listUntil 反复读列表直到条件满足。列表只等待活跃面就绪，归档面是独立订阅、
// 稍后才到：冷启动的第一次 List 完全可能少几个归档线程、或因为归档未就绪而标
// degraded。需要「两面都热」的断言必须先等条件成立，不能赌第一次调用的时序。
func listUntil(t *testing.T, e *t3TestEnv, userID string, until func(*T3ThreadListResponse) bool) *T3ThreadListResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		result, err := e.th.List(context.Background(), userID)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if until(result) {
			return result
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for list condition, last = %+v", result.Environments)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestT3ThreadsAggregationAndErrorIsolation(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = mustJSON(map[string]any{
		"kind": "snapshot",
		"snapshot": map[string]any{
			"schemaVersion":    1,
			"snapshotSequence": 4,
			"projects":         []any{map[string]any{"id": "p-1", "title": "Fast Ship 主仓库"}},
			"threads": []any{
				shellThread("th-1", "实现只读线程", "running", map[string]any{
					"latestRunStartedAt": "2026-10-12T02:42:00.000Z",
				}),
				// 等待审批 + 已排队新 run：pending 优先于 run 状态 → waiting（GPT10）
				shellThread("th-2", "等待审批", "queued", map[string]any{
					"pendingRuntimeRequest": map[string]any{"id": "rr-1", "kind": "approval", "createdAt": "2026-10-12T04:00:00.000Z"},
					// 用户在 T3 收进历史区：与运行态正交透传
					"settledOverride": "settled",
					"settledAt":       "2026-10-12T05:00:00.000Z",
				}),
				shellThread("th-3", "后台活跃", "completed", map[string]any{
					"activityRunStatus": "running", // shell.status 已完成但后台 run 未停 → running（Grok8）
				}),
				shellThread("th-5", "子代理任务", "running", map[string]any{
					"lineage": map[string]any{"parentThreadId": "th-1", "relationshipToParent": "subagent", "rootThreadId": "th-1"},
				}),
			},
			"archivedThreads": []any{}, // 真实活跃快照归档恒为空
		},
	})
	e.fake.archive = mustJSON(map[string]any{
		"schemaVersion":    1,
		"snapshotSequence": 9,
		"projects":         []any{map[string]any{"id": "p-1", "title": "Fast Ship 主仓库"}},
		"threads": []any{
			shellThread("th-4", "归档线程", "idle", map[string]any{
				"branch":     "old",
				"archivedAt": "2026-10-01T00:00:00.000Z",
				"updatedAt":  "2026-10-01T00:00:00.000Z",
			}),
		},
	})
	e.fake.mu.Unlock()

	healthy, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "健康", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create healthy: %v", err)
	}

	// 第二个环境：配对成功后立刻下线服务端 → List 时不可达
	offlineFake := newFakeT3Environment(t)
	offlineServer := httptest.NewServer(offlineFake.handler())
	offline, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "失联", PairingUrl: offlineFake.pairingURL(offlineServer.URL)})
	if err != nil {
		t.Fatalf("create offline env: %v", err)
	}
	offlineServer.Close()

	// 归档线程（th-4）来自独立的归档订阅，等两面都热再断言。
	result := listUntil(t, e, userID, func(res *T3ThreadListResponse) bool {
		return len(res.Items) == 4 && len(res.Environments) == 2 &&
			res.Environments[0].Status == T3EnvironmentStatusOK
	})
	// th-1 活跃、th-2 waiting、th-3 running、th-4 归档；th-5 子代理排除
	if len(result.Items) != 4 {
		t.Fatalf("expected 4 threads (subagent excluded), got %d: %+v", len(result.Items), result.Items)
	}
	byID := map[string]T3ThreadSummary{}
	for _, item := range result.Items {
		byID[item.Id] = item
	}

	th1 := byID["th-1"]
	if th1.Status != "running" || th1.ProjectName != "Fast Ship 主仓库" || th1.Model != "claude-sonnet-5-5" || th1.Provider != "claude-pro" || th1.Branch != "t3/threads" || th1.PullRequestCount != 2 {
		t.Fatalf("th-1 = %+v", th1)
	}
	if th1.EnvironmentId != healthy.Id || th1.EnvironmentName != "健康" {
		t.Fatalf("th-1 env fields = %+v", th1)
	}
	if got := api.Deref(th1.RunStartedAt); got != "2026-10-12T02:42:00.000Z" {
		t.Fatalf("th-1 run_started_at passthrough, got %q", got)
	}
	if th1.Settled {
		t.Fatalf("th-1 must not be settled")
	}
	if byID["th-2"].Status != "waiting" {
		t.Fatalf("th-2 should be waiting (pendingRuntimeRequest wins over queued run), got %+v", byID["th-2"])
	}
	if !byID["th-2"].Settled {
		t.Fatalf("th-2 should be settled (settledOverride=settled)")
	}
	if byID["th-3"].Status != "running" {
		t.Fatalf("th-3 should be running (activityRunStatus), got %+v", byID["th-3"])
	}
	if byID["th-4"].Status != "archived" {
		t.Fatalf("th-4 = %+v", byID["th-4"])
	}
	if _, ok := byID["th-5"]; ok {
		t.Fatalf("subagent thread th-5 must be excluded from list")
	}

	// 归档读取走的是归档订阅（先全量快照后增量），不再是归档一次性 RPC
	var sawArchiveSubscribe bool
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == t3RPCSubscribeArchivedShell {
			sawArchiveSubscribe = true
		}
	}
	if !sawArchiveSubscribe {
		t.Fatalf("expected orchestration.subscribeArchivedShell stream")
	}
	// 热路径复用：第一次列表只发生一次冷启动全量订阅（活跃一条连接）。
	if got := e.fake.subscribeShellCount(); got != 1 {
		t.Fatalf("expected exactly one cold subscribeShell, got %d", got)
	}

	// 环境数组带状态：健康环境 ok，失联环境 unreachable，单环境失败不阻塞线程返回
	if len(result.Environments) != 2 {
		t.Fatalf("expected 2 environment entries, got %+v", result.Environments)
	}
	statuses := map[string]T3EnvironmentStatus{}
	for _, item := range result.Environments {
		statuses[item.Id] = item.Status
	}
	if statuses[healthy.Id] != T3EnvironmentStatusOK {
		t.Fatalf("healthy env status = %+v", result.Environments)
	}
	if statuses[offline.Id] != T3EnvironmentStatusUnreachable {
		t.Fatalf("offline env status = %+v", result.Environments)
	}

	// 第二次列表：从 hub 内存回答，不发生新的 shell 订阅，内容一致。
	replay, err := e.th.List(ctx, userID)
	if err != nil {
		t.Fatalf("list threads again: %v", err)
	}
	if len(replay.Items) != 4 {
		t.Fatalf("expected same 4 threads on replay, got %+v", replay.Items)
	}
	if got := e.fake.subscribeShellCount(); got != 1 {
		t.Fatalf("hot-path list must not resubscribe, subscribeShell count = %d", got)
	}
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.subscribeShell" {
			var payload map[string]any
			if err := json.Unmarshal(rpc.payload, &payload); err != nil {
				t.Fatalf("subscribe payload: %v", err)
			}
			if _, has := payload["afterSequence"]; has {
				t.Fatalf("cold subscribe must not carry afterSequence: %+v", payload)
			}
		}
	}

	// 凭证吊销的列表语义见 TestT3ThreadsListCredentialRejectedSurfacesRepair
	// （吊销发生在首次订阅之前 → Failed 分类）。entry 已就绪时吊销要等重连
	// 才暴露，陈旧/断连语义见 TestT3ThreadsStaleAfterDisconnectNotTrusted。

	// 不存在的环境
	if _, err := e.th.Detail(ctx, userID, "no-env", "th-1"); err == nil || t3ErrCode(err) != errs.ErrT3EnvironmentNotFound.Code {
		t.Fatalf("expected env not found, got %v", err)
	}
}

// TestT3ThreadsSlowEnvironmentIsolated 慢环境吃满等待预算后，健康环境结果
// 照常返回（坏环境不拖垮健康结果）。慢环境的 hub 首次同步等不到权威快照，
// 按 Syncing 的新鲜度如实降级（正在同步），不再伪装成 unreachable。
func TestT3ThreadsSlowEnvironmentIsolated(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = mustJSON(map[string]any{
		"kind": "snapshot",
		"snapshot": map[string]any{
			"schemaVersion": 1, "snapshotSequence": 1,
			"projects": []any{map[string]any{"id": "p-1", "title": "P"}},
			"threads":  []any{shellThread("th-1", "健康线程", "running", nil)},
		},
	})
	e.fake.mu.Unlock()
	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "健康", PairingUrl: e.fake.pairingURL(e.fake.baseURL)}); err != nil {
		t.Fatalf("create healthy: %v", err)
	}

	// 慢环境：会话探测挂起，直到预算超时也不应答。
	// 缩小环境服务预算，配对后的即时探测也在毫秒级返回。
	e.env.budgets = t3RemoteBudgets{Round: 5 * time.Second, PerEnvironment: 300 * time.Millisecond, ThreadDetail: 5 * time.Second, ItemDetail: 5 * time.Second, ShellFirstSync: 300 * time.Millisecond}
	release := make(chan struct{})
	slowFake := newFakeT3Environment(t)
	slowServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			slowFake.handler().ServeHTTP(w, r)
			return
		}
		<-release
	}))
	t.Cleanup(slowServer.Close)
	defer close(release) // defers 先行：放行挂起的 handler，再由 Cleanup 关闭 server
	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "慢", PairingUrl: slowFake.pairingURL(slowServer.URL)}); err != nil {
		t.Fatalf("create slow env: %v", err)
	}

	e.th.budgets = t3RemoteBudgets{Round: 5 * time.Second, PerEnvironment: 300 * time.Millisecond, ThreadDetail: 5 * time.Second, ItemDetail: 5 * time.Second, ShellFirstSync: 300 * time.Millisecond}
	started := time.Now()
	result, err := e.th.List(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("round budget did not bound the slow environment, elapsed = %v", elapsed)
	}
	if len(result.Items) != 1 || result.Items[0].Id != "th-1" {
		t.Fatalf("healthy environment results must survive, got %+v", result.Items)
	}
	statuses := map[string]T3EnvironmentStatus{}
	msgs := map[string]string{}
	for _, item := range result.Environments {
		statuses[item.Id] = item.Status
		msgs[item.Id] = api.Deref(item.Error)
	}
	slowID := slowEnvID(t, e, userID, "慢")
	if statuses[slowID] != T3EnvironmentStatusDegraded {
		t.Fatalf("slow env should degrade as syncing, got %+v", result.Environments)
	}
	if !strings.Contains(msgs[slowID], "正在同步") {
		t.Fatalf("expected syncing banner, got %q", msgs[slowID])
	}
}

func slowEnvID(t *testing.T, e *t3TestEnv, userID, name string) string {
	t.Helper()
	env, err := e.env.repo.FindByNameAndUserID(context.Background(), name, userID)
	if err != nil {
		t.Fatalf("find slow env: %v", err)
	}
	return env.ID
}

// wireProjection 组装一个真实 wire 形状的线程投影：visibleTurnItems 含继承行，
// 命令输出被裁剪（outputOmitted），turnItems 里的回滚条目不进可见时间线。
func wireProjection(thread map[string]any, rows []map[string]any) string {
	return mustJSONString(map[string]any{
		"thread":           thread,
		"runs":             []any{},
		"turnItems":        []any{map[string]any{"id": "ghost", "type": "user_message", "status": "completed", "text": "已回滚不应出现"}},
		"visibleTurnItems": rows,
	})
}

// appThreadFixture 造投影 thread 字段的真实形状（OrchestrationV2AppThread，
// contracts orchestrationV2.ts:363）：只有线程事实。status/activityRunStatus/
// pendingRuntimeRequest 只属于 ThreadShell（orchestrationV2.ts:1927），
// 测试不得把 shell 字段混进投影——r2 曾因此把详情运行态喂错。
func appThreadFixture(id, title string, extra map[string]any) map[string]any {
	thread := map[string]any{
		"id":                 id,
		"projectId":          "p-1",
		"title":              title,
		"providerInstanceId": "prov",
		"modelSelection":     map[string]any{"instanceId": "prov", "model": "m"},
		"branch":             "b",
		"archivedAt":         nil,
		"updatedAt":          "2026-10-12T00:00:00.000Z",
	}
	for k, v := range extra {
		thread[k] = v
	}
	return thread
}

// shellSnapshotFixture 组装 subscribeShell 首块的活跃快照（真实形状：
// 归档恒空，线程是带运行态的 ThreadShell）。
func shellSnapshotFixture(projects []any, threads []any) json.RawMessage {
	return mustJSON(map[string]any{
		"kind": "snapshot",
		"snapshot": map[string]any{
			"schemaVersion": 1, "snapshotSequence": 1,
			"projects": projects, "threads": threads, "archivedThreads": []any{},
		},
	})
}

func TestT3ThreadDetailNormalization(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	rows := []map[string]any{
		// fork 继承前缀只存在于 visibleTurnItems，来源身份参与条目 ID
		// i0 不带 updatedAt：updated_at 原样透传，上游省略即缺省
		{"position": 0, "visibility": "inherited", "sourceThreadId": "th-src", "sourceItemId": "i0",
			"item": map[string]any{"id": "i0", "type": "user_message", "status": "completed", "text": "继承的前缀消息"}},
		{"position": 1, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i1",
			"item": map[string]any{"id": "i1", "type": "user_message", "status": "completed", "text": "帮我看看这段报错\n第二行", "updatedAt": "2026-10-12T01:00:00.000Z"}},
		{"position": 2, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i2",
			"item": map[string]any{"id": "i2", "type": "reasoning", "status": "completed", "text": "思考过程..."}},
		{"position": 3, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i3",
			"item": map[string]any{"id": "i3", "type": "assistant_message", "status": "completed", "text": "这是修复方案", "streaming": false}},
		// wire 投影：命令输出被裁剪，只有 outputOmitted 标注
		{"position": 4, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i4",
			"item": map[string]any{"id": "i4", "type": "command_execution", "status": "completed", "input": "go test ./...", "outputOmitted": true}},
		{"position": 5, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i5",
			"item": map[string]any{"id": "i5", "type": "file_change", "status": "completed", "fileName": "main.go", "additions": 10, "deletions": 2,
				"changes": []any{map[string]any{"operation": "edit", "path": "main.go"}}}},
		{"position": 6, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i6",
			"item": map[string]any{"id": "i6", "type": "dynamic_tool", "status": "completed", "toolName": "browser_navigate",
				"input": map[string]any{"url": "https://x"}, "outputOmitted": true}},
		{"position": 7, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i7",
			"item": map[string]any{"id": "i7", "type": "subagent", "status": "completed", "prompt": "子任务", "childThreadId": "th-child"}},
		{"position": 8, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i8",
			"item": map[string]any{"id": "i8", "type": "error", "status": "completed", "failure": map[string]any{"class": "provider_error", "message": "额度用尽"}}},
		{"position": 9, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i9",
			"item": map[string]any{"id": "i9", "type": "proposed_plan", "status": "completed", "planId": "pl-1", "markdown": "# 计划", "streaming": false}},
		{"position": 10, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i11",
			"item": map[string]any{"id": "i11", "type": "checkpoint", "status": "completed", "checkpointId": "c1", "scopeId": "s1", "files": []any{}}},
		{"position": 11, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i12",
			"item": map[string]any{"id": "i12", "type": "command_execution", "status": "completed", "input": "curl ...", "outputOmitted": true}},
	}
	e.fake.mu.Lock()
	// 运行态取 WS 快照里的可信 shell：status 已完成但后台 run 未停 → running。
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-9", "详情", "completed", map[string]any{"activityRunStatus": "running"})},
	)
	e.fake.projections["th-9"] = wireProjection(appThreadFixture("th-9", "详情", nil), rows)
	e.fake.mu.Unlock()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "详情机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	detail, err := e.th.Detail(ctx, userID, env.Id, "th-9")
	if err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail.Thread.Id != "th-9" || detail.Thread.Status != "running" {
		t.Fatalf("detail thread = %+v", detail.Thread)
	}
	if detail.Access != T3EnvironmentAccessOperate {
		t.Fatalf("detail access = %v", detail.Access)
	}
	if detail.Thread.ProjectName != "P" || detail.Thread.Title != "详情" || detail.Thread.Model != "claude-sonnet-5-5" || detail.Thread.Provider != "claude-pro" || detail.Thread.Branch != "t3/threads" || detail.Thread.PullRequestCount != 2 {
		t.Fatalf("detail thread metadata must come from trusted shell, got %+v", detail.Thread)
	}
	// 12 行中 checkpoint（结构性事件）丢弃；turnItems 里的 ghost 不在可见时间线
	if len(detail.Items) != 11 {
		t.Fatalf("expected 11 items, got %d: %+v", len(detail.Items), detail.Items)
	}
	types := map[string]T3ThreadItem{}
	var order []string
	for _, item := range detail.Items {
		types[item.Id] = item
		order = append(order, item.Id)
	}
	wantOrder := []string{"th-src::i0", "th-9::i1", "th-9::i2", "th-9::i3", "th-9::i4", "th-9::i5", "th-9::i6", "th-9::i7", "th-9::i8", "th-9::i9", "th-9::i12"}
	if strings.Join(order, ",") != strings.Join(wantOrder, ",") {
		t.Fatalf("visible order = %v", order)
	}
	if types["th-src::i0"].Type != "user_message" || types["th-src::i0"].Text != "继承的前缀消息" {
		t.Fatalf("inherited row = %+v", types["th-src::i0"])
	}
	if !api.Deref(types["th-src::i0"].Inherited) || api.Deref(types["th-src::i0"].SourceThreadId) != "th-src" {
		t.Fatalf("inherited origin = %+v", types["th-src::i0"])
	}
	if api.Deref(types["th-9::i1"].Inherited) {
		t.Fatalf("local row must not be marked inherited")
	}
	// 条目自带版本：updatedAt 原样透传，上游省略即缺省
	if types["th-src::i0"].UpdatedAt != nil {
		t.Fatalf("missing upstream updatedAt must stay absent, got %q", *types["th-src::i0"].UpdatedAt)
	}
	if got := api.Deref(types["th-9::i1"].UpdatedAt); got != "2026-10-12T01:00:00.000Z" {
		t.Fatalf("item updated_at must pass through verbatim, got %q", got)
	}
	if types["th-9::i1"].Text != "帮我看看这段报错\n第二行" {
		t.Fatalf("i1 = %+v", types["th-9::i1"])
	}
	// 命令：text=命令，summary=命令首行，输出经补读（detail_available）
	if types["th-9::i4"].Type != "command_execution" || types["th-9::i4"].Text != "go test ./..." || types["th-9::i4"].Summary != "go test ./..." {
		t.Fatalf("i4 = %+v", types["th-9::i4"])
	}
	if !api.Deref(types["th-9::i4"].DetailAvailable) || !api.Deref(types["th-9::i6"].DetailAvailable) || !api.Deref(types["th-9::i7"].DetailAvailable) {
		t.Fatalf("command/dynamic_tool/subagent must be detail_available: %+v", types)
	}
	// 文件：真实元数据，无 diff 读路径
	if types["th-9::i5"].Type != "file_change" || types["th-9::i5"].Summary != "main.go (+10/-2)" {
		t.Fatalf("i5 = %+v", types["th-9::i5"])
	}
	if !strings.Contains(types["th-9::i5"].Text, "main.go") || !strings.Contains(types["th-9::i5"].Text, "+10/-2") || !strings.Contains(types["th-9::i5"].Text, "edit main.go") {
		t.Fatalf("i5 metadata = %q", types["th-9::i5"].Text)
	}
	if api.Deref(types["th-9::i5"].DetailAvailable) {
		t.Fatalf("file_change has no upstream detail, detail_available must be false")
	}
	if types["th-9::i6"].Type != "dynamic_tool" || types["th-9::i6"].Summary != "browser_navigate" {
		t.Fatalf("i6 = %+v", types["th-9::i6"])
	}
	if types["th-9::i7"].Type != "subagent" || api.Deref(types["th-9::i7"].ChildThreadId) != "th-child" || types["th-9::i7"].Summary != "子任务" {
		t.Fatalf("i7 = %+v", types["th-9::i7"])
	}
	if types["th-9::i8"].Type != "error" || types["th-9::i8"].Text != "额度用尽" || types["th-9::i8"].Summary != "provider_error" {
		t.Fatalf("i8 = %+v", types["th-9::i8"])
	}
	if types["th-9::i9"].Type != "assistant_message" || types["th-9::i9"].Text != "# 计划" {
		t.Fatalf("i9 = %+v", types["th-9::i9"])
	}
	if strings.Contains(types["th-9::i1"].Text, "已回滚") {
		t.Fatalf("rolled-back ghost item must not appear")
	}

	// 上游 404 → 40418
	if _, err := e.th.Detail(ctx, userID, env.Id, "th-missing"); err == nil || t3ErrCode(err) != errs.ErrT3ThreadNotFound.Code {
		t.Fatalf("expected thread not found, got %v", err)
	}

	// 凭证吊销 → 50202 with repair hint
	e.fake.revokeBearers()
	_, err = e.th.Detail(ctx, userID, env.Id, "th-9")
	if err == nil || t3ErrCode(err) != errs.ErrT3Upstream.Code {
		t.Fatalf("expected upstream error after revoke, got %v", err)
	}
}

// TestT3ThreadDetailUsesTrustedShell 覆盖 r3 必修1：详情运行态只来自可信
// WS shell（投影 thread 是 AppThread，没有运行态字段可喂）——
// 活跃后台 run、pending 用户输入、子代理子线程、归档线程，以及 shell 读不到
// 时明确失败而非假 idle。
func TestT3ThreadDetailUsesTrustedShell(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	// 时间线 fixtures：threadId → 可见行（一行即可，运行态断言不依赖条目）
	oneRow := func(sourceThreadID, itemID string) []map[string]any {
		return []map[string]any{
			{"position": 0, "visibility": "local", "sourceThreadId": sourceThreadID, "sourceItemId": itemID,
				"item": map[string]any{"id": itemID, "type": "user_message", "status": "completed", "text": "你好"}},
		}
	}

	e.fake.mu.Lock()
	// 活跃快照：th-run（后台 run 未停）、th-wait（等用户输入）、th-child（子代理）
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{
			shellThread("th-run", "后台跑", "completed", map[string]any{
				"activityRunStatus": "running", // 后台 run 未停 → running
			}),
			shellThread("th-wait", "等输入", "queued", map[string]any{
				"pendingRuntimeRequest": map[string]any{"id": "rr-9", "kind": "user_input", "createdAt": "2026-10-12T05:00:00.000Z"},
			}),
			shellThread("th-child", "子代理任务", "running", map[string]any{
				"lineage": map[string]any{"parentThreadId": "th-run", "relationshipToParent": "subagent", "rootThreadId": "th-run"},
			}),
		},
	)
	// 归档只由独立 RPC 提供（真实活跃快照归档恒空）；th-old 只在归档里。
	e.fake.archive = mustJSON(map[string]any{
		"schemaVersion": 1, "snapshotSequence": 2,
		"projects": []any{map[string]any{"id": "p-1", "title": "P"}},
		"threads": []any{
			shellThread("th-old", "归档线程", "idle", map[string]any{
				"branch":     "old",
				"archivedAt": "2026-10-01T00:00:00.000Z",
				"updatedAt":  "2026-10-01T00:00:00.000Z",
			}),
		},
	})
	e.fake.projections["th-run"] = wireProjection(appThreadFixture("th-run", "后台跑", nil), oneRow("th-run", "r1"))
	e.fake.projections["th-wait"] = wireProjection(appThreadFixture("th-wait", "等输入", nil), oneRow("th-wait", "w1"))
	e.fake.projections["th-child"] = wireProjection(appThreadFixture("th-child", "子代理任务", map[string]any{
		"lineage": map[string]any{"parentThreadId": "th-run", "relationshipToParent": "subagent", "rootThreadId": "th-run"},
	}), oneRow("th-child", "c1"))
	e.fake.projections["th-old"] = wireProjection(appThreadFixture("th-old", "归档线程", map[string]any{
		"archivedAt": "2026-10-01T00:00:00.000Z",
	}), oneRow("th-old", "o1"))
	e.fake.mu.Unlock()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "状态机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 子代理子线程详情可进入（列表才排除 lineage=subagent）
	child, err := e.th.Detail(ctx, userID, env.Id, "th-child")
	if err != nil {
		t.Fatalf("child detail: %v", err)
	}
	if child.Thread.Id != "th-child" || child.Thread.Status != "running" {
		t.Fatalf("child detail thread = %+v", child.Thread)
	}

	// 归档线程详情：只在归档快照里 → archived
	old, err := e.th.Detail(ctx, userID, env.Id, "th-old")
	if err != nil {
		t.Fatalf("archived detail: %v", err)
	}
	if old.Thread.Status != "archived" {
		t.Fatalf("archived thread status = %+v", old.Thread)
	}

	// pending 用户输入压过 queued run → waiting
	wait, err := e.th.Detail(ctx, userID, env.Id, "th-wait")
	if err != nil {
		t.Fatalf("waiting detail: %v", err)
	}
	if wait.Thread.Status != "waiting" {
		t.Fatalf("pending-input thread status = %+v", wait.Thread)
	}

	// 同一线程两次详情：运行中（后台 run）→ 静止（idle），状态跟随 shell 而非
	// 投影；变化由订阅流推送增量驱动（INT-116：不再靠替换 fixture 等新连接）。
	run, err := e.th.Detail(ctx, userID, env.Id, "th-run")
	if err != nil {
		t.Fatalf("running detail: %v", err)
	}
	if run.Thread.Status != "running" {
		t.Fatalf("expected running from activityRunStatus, got %+v", run.Thread)
	}
	e.fake.pushThreadDelta(t, shellThread("th-run", "后台跑", "idle", nil)) // run 停了
	idle, err := e.th.Detail(ctx, userID, env.Id, "th-run")
	if err != nil {
		t.Fatalf("idle detail: %v", err)
	}
	if idle.Thread.Status != "idle" {
		t.Fatalf("expected idle after run finished, got %+v", idle.Thread)
	}

	// shell 读不到（投影在、快照无此线程）→ 明确失败，不假 idle
	e.fake.mu.Lock()
	e.fake.projections["th-ghost"] = wireProjection(appThreadFixture("th-ghost", "幽灵", nil), oneRow("th-ghost", "g1"))
	e.fake.mu.Unlock()
	_, err = e.th.Detail(ctx, userID, env.Id, "th-ghost")
	if err == nil || t3ErrCode(err) != errs.ErrT3Upstream.Code {
		t.Fatalf("expected explicit failure when shell is unreadable, got %v", err)
	}
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || !strings.Contains(appErr.Message, "运行状态不可读") {
		t.Fatalf("expected explicit shell-missing message, got %v", err)
	}
}

// TestT3ThreadItemDetail 覆盖 panel r1 新增共同接口：
// 成员校验 → 从可信行派生 getTurnItem 参数 → 完整正文归一化。
func TestT3ThreadItemDetail(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	// ItemDetail 只读投影行与 getTurnItem，不需要 shell；投影 thread 按
	// 真实形状喂 AppThread（无运行态字段）。
	rows := []map[string]any{
		{"position": 0, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i4",
			"item": map[string]any{"id": "i4", "type": "command_execution", "status": "completed", "input": "go test ./...", "outputOmitted": true}},
		// 继承的命令行：getTurnItem 参数必须取自行内条目的持久身份（来源线程）
		{"position": 1, "visibility": "inherited", "sourceThreadId": "th-src", "sourceItemId": "s-cmd",
			"item": map[string]any{"id": "s-cmd", "threadId": "th-src", "type": "command_execution", "status": "completed", "input": "继承命令", "outputOmitted": true}},
		{"position": 2, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i7",
			"item": map[string]any{"id": "i7", "type": "subagent", "status": "completed", "prompt": "子任务", "childThreadId": "th-child"}},
		{"position": 3, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i1",
			"item": map[string]any{"id": "i1", "type": "user_message", "status": "completed", "text": "普通消息"}},
		{"position": 4, "visibility": "local", "sourceThreadId": "th-9", "sourceItemId": "i5",
			"item": map[string]any{"id": "i5", "type": "file_change", "status": "completed", "fileName": "main.go", "additions": 1, "deletions": 0}},
	}
	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture([]any{}, []any{})
	e.fake.projections["th-9"] = wireProjection(appThreadFixture("th-9", "详情", nil), rows)
	e.fake.turnItems["i4"] = mustJSONString(map[string]any{
		"id": "i4", "threadId": "th-9", "type": "command_execution", "status": "completed",
		"input": "go test ./...", "output": "ok\nPASS", "updatedAt": "2026-10-12T02:00:00.000Z",
	})
	e.fake.turnItems["s-cmd"] = mustJSONString(map[string]any{
		"id": "s-cmd", "threadId": "th-src", "type": "command_execution", "status": "completed",
		"input": "继承命令", "output": "继承输出",
	})
	e.fake.turnItems["i7"] = mustJSONString(map[string]any{
		"id": "i7", "threadId": "th-9", "type": "subagent", "status": "completed",
		"prompt": "子任务", "result": "完整结果\n第二行", "progress": "进度快照", "childThreadId": "th-child",
	})
	e.fake.mu.Unlock()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "条目机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 命令：完整正文含 input+output，detail_available=false，参数按可信行派生
	item, err := e.th.ItemDetail(ctx, userID, env.Id, "th-9", "th-9::i4")
	if err != nil {
		t.Fatalf("item detail i4: %v", err)
	}
	if item.Type != "command_execution" || api.Deref(item.DetailAvailable) {
		t.Fatalf("i4 detail = %+v", item)
	}
	if got := api.Deref(item.UpdatedAt); got != "2026-10-12T02:00:00.000Z" {
		t.Fatalf("full item updated_at must come from getTurnItem, got %q", got)
	}
	if !strings.Contains(item.Text, "go test ./...") || !strings.Contains(item.Text, "--- 输出 ---") || !strings.Contains(item.Text, "ok\nPASS") {
		t.Fatalf("i4 text = %q", item.Text)
	}
	var gotPayload map[string]any
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.getTurnItem" {
			if err := json.Unmarshal(rpc.payload, &gotPayload); err != nil {
				t.Fatalf("payload: %v", err)
			}
		}
	}
	if gotPayload["threadId"] != "th-9" || gotPayload["itemId"] != "i4" {
		t.Fatalf("getTurnItem payload = %+v", gotPayload)
	}

	// 继承命令：threadId 派生自行内条目（th-src），不是详情线程
	item, err = e.th.ItemDetail(ctx, userID, env.Id, "th-9", "th-src::s-cmd")
	if err != nil {
		t.Fatalf("item detail s-cmd: %v", err)
	}
	if !strings.Contains(item.Text, "继承命令") || !strings.Contains(item.Text, "继承输出") {
		t.Fatalf("s-cmd text = %q", item.Text)
	}
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.getTurnItem" && strings.Contains(string(rpc.payload), "s-cmd") {
			var payload map[string]any
			_ = json.Unmarshal(rpc.payload, &payload)
			if payload["threadId"] != "th-src" {
				t.Fatalf("inherited getTurnItem must use row threadId, got %+v", payload)
			}
		}
	}

	// subagent：prompt+result+progress 全量进正文
	item, err = e.th.ItemDetail(ctx, userID, env.Id, "th-9", "th-9::i7")
	if err != nil {
		t.Fatalf("item detail i7: %v", err)
	}
	if api.Deref(item.ChildThreadId) != "th-child" || api.Deref(item.DetailAvailable) {
		t.Fatalf("i7 detail = %+v", item)
	}
	for _, want := range []string{"子任务", "--- 结果 ---", "完整结果\n第二行", "--- 进度 ---", "进度快照"} {
		if !strings.Contains(item.Text, want) {
			t.Fatalf("i7 text missing %q: %q", want, item.Text)
		}
	}

	// 普通消息 / 文件变更：快照即全部，不发起补读
	before := len(e.fake.recordedRPC())
	item, err = e.th.ItemDetail(ctx, userID, env.Id, "th-9", "th-9::i1")
	if err != nil {
		t.Fatalf("item detail i1: %v", err)
	}
	if api.Deref(item.DetailAvailable) || item.Text != "普通消息" {
		t.Fatalf("i1 detail = %+v", item)
	}
	item, err = e.th.ItemDetail(ctx, userID, env.Id, "th-9", "th-9::i5")
	if err != nil {
		t.Fatalf("item detail i5: %v", err)
	}
	if api.Deref(item.DetailAvailable) || !strings.Contains(item.Text, "main.go") {
		t.Fatalf("i5 detail = %+v", item)
	}
	for _, rpc := range e.fake.recordedRPC()[before:] {
		if rpc.method == "orchestration.getTurnItem" {
			t.Fatalf("snapshot-complete item must not trigger getTurnItem")
		}
	}

	// 非成员 → 40419；伪造的来源身份不改变查找
	if _, err := e.th.ItemDetail(ctx, userID, env.Id, "th-9", "th-elsewhere::nope"); err == nil || t3ErrCode(err) != errs.ErrT3ThreadItemNotFound.Code {
		t.Fatalf("expected 40419 for non-member item, got %v", err)
	}
}

// TestT3ThreadsArchiveFailureKeepsActiveThreads 覆盖 G1 列表侧：归档 RPC
// 失败是部分失败——活跃线程照常返回，环境标 degraded 并附「归档线程
// 暂不可读」横幅文案，而不是把健康线程整批丢成空。附带 G2 端到端：
// pendingBackgroundTasks 经 WS 快照解码后参与状态映射。
func TestT3ThreadsArchiveFailureKeepsActiveThreads(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{
			shellThread("th-1", "活跃线程", "running", nil),
			// shell.status 已完成，但后台 subagent 未收尾（command 不占位）：
			// 仍要判 running——列表置顶、详情轮询的依据。
			shellThread("th-2", "后台工作未收尾", "completed", map[string]any{
				"pendingBackgroundTasks": []any{
					map[string]any{"taskId": "bg-1", "kind": "subagent"},
					map[string]any{"taskId": "bg-2", "kind": "command"},
				},
			}),
		},
	)
	e.fake.archiveFail = true
	e.fake.mu.Unlock()

	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "降级机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)}); err != nil {
		t.Fatalf("create: %v", err)
	}

	result, err := e.th.List(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("active threads must survive archive RPC failure, got %+v", result.Items)
	}
	byID := map[string]T3ThreadSummary{}
	for _, item := range result.Items {
		byID[item.Id] = item
	}
	if byID["th-1"].Status != "running" {
		t.Fatalf("th-1 = %+v", byID["th-1"])
	}
	if byID["th-2"].Status != "running" {
		t.Fatalf("pending background subagent must hold completion (running), got %+v", byID["th-2"])
	}
	if len(result.Environments) != 1 {
		t.Fatalf("environments = %+v", result.Environments)
	}
	envStatus := result.Environments[0]
	if envStatus.Status != T3EnvironmentStatusDegraded {
		t.Fatalf("archive failure must mark the environment degraded, got %+v", envStatus)
	}
	if msg := api.Deref(envStatus.Error); !strings.Contains(msg, "归档") {
		t.Fatalf("expected degradation banner text about archive, got %q", msg)
	}
}

// TestT3ThreadDetailArchiveFailure 覆盖 G1 详情侧：活跃命中时归档失败不影响
// 正常详情；活跃没有且归档读取失败时回上游错误（50202）——即便投影 404，
// 也不得借不可信的归档面把线程谎报成 40418。
func TestT3ThreadDetailArchiveFailure(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	oneRow := func(sourceThreadID, itemID string) []map[string]any {
		return []map[string]any{
			{"position": 0, "visibility": "local", "sourceThreadId": sourceThreadID, "sourceItemId": itemID,
				"item": map[string]any{"id": itemID, "type": "user_message", "status": "completed", "text": "你好"}},
		}
	}

	e.fake.mu.Lock()
	// 活跃面只有 th-live；归档面故障（th-gone 已归档/刚从活跃面消失的形态）。
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-live", "活跃线程", "running", nil)},
	)
	e.fake.archiveFail = true
	e.fake.projections["th-live"] = wireProjection(appThreadFixture("th-live", "活跃线程", nil), oneRow("th-live", "l1"))
	// th-gone 无投影：若误把归档失败当「快照齐全」就会顺投影 404 谎报 40418。
	e.fake.mu.Unlock()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "归档故障机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 活跃命中：归档失败不影响正常详情。
	detail, err := e.th.Detail(ctx, userID, env.Id, "th-live")
	if err != nil {
		t.Fatalf("active thread detail must succeed despite archive failure: %v", err)
	}
	if detail.Thread.Id != "th-live" || detail.Thread.Status != "running" {
		t.Fatalf("detail thread = %+v", detail.Thread)
	}
	if len(detail.Items) != 1 {
		t.Fatalf("items = %+v", detail.Items)
	}

	// 活跃没有 + 归档失败：上游错误（50202），不是 40418。
	_, err = e.th.Detail(ctx, userID, env.Id, "th-gone")
	if err == nil || t3ErrCode(err) != errs.ErrT3Upstream.Code {
		t.Fatalf("expected upstream error when archive is unreadable, got %v", err)
	}
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || !strings.Contains(appErr.Message, "归档") {
		t.Fatalf("expected archive-failure message, got %v", err)
	}
}

// TestT3ThreadStatusPendingBackgroundTasks 覆盖 G2 状态映射：根 run 结束后
// 仍在跑的后台工作按 kind 占完成位——command 不占，其余一切 kind（含未知与
// 缺省）都占 → running；判定次序上 archivedAt/pendingRuntimeRequest/
// activityRunStatus 保持优先。用例从 JSON 解码起步，连带覆盖 wire 字段标签。
func TestT3ThreadStatusPendingBackgroundTasks(t *testing.T) {
	decode := func(t *testing.T, shellJSON string) *t3ThreadShell {
		t.Helper()
		var shell t3ThreadShell
		if err := json.Unmarshal([]byte(shellJSON), &shell); err != nil {
			t.Fatalf("decode shell: %v", err)
		}
		return &shell
	}

	// 基线 shell.status=completed（无其它运行态信号）→ 占位与否全由后台工作决定。
	kindCases := []struct {
		name  string
		tasks string
		want  api.T3ThreadSummaryStatus
	}{
		{"subagent 占位", `[{"taskId":"bg-1","kind":"subagent"}]`, api.T3ThreadSummaryStatusRunning},
		{"monitor 占位", `[{"taskId":"bg-1","kind":"monitor"}]`, api.T3ThreadSummaryStatusRunning},
		{"background_task 占位", `[{"taskId":"bg-1","kind":"background_task"}]`, api.T3ThreadSummaryStatusRunning},
		{"未知 kind 占位（上游回退 background_task）", `[{"taskId":"bg-1","kind":"hologram"}]`, api.T3ThreadSummaryStatusRunning},
		{"kind 缺省占位（旧 roster 按 background_task 加载）", `[{"taskId":"bg-1"}]`, api.T3ThreadSummaryStatusRunning},
		{"command 不占位", `[{"taskId":"bg-1","kind":"command"}]`, api.T3ThreadSummaryStatusIdle},
		{"command 与占位混合", `[{"taskId":"b1","kind":"command"},{"taskId":"b2","kind":"monitor"}]`, api.T3ThreadSummaryStatusRunning},
		{"空数组不占位", `[]`, api.T3ThreadSummaryStatusIdle},
	}
	for _, c := range kindCases {
		t.Run(c.name, func(t *testing.T) {
			shell := decode(t, `{"id":"th-1","status":"completed","pendingBackgroundTasks":`+c.tasks+`}`)
			if got := t3ThreadStatus(shell); got != c.want {
				t.Fatalf("tasks %s: status = %v, want %v", c.tasks, got, c.want)
			}
		})
	}

	// 判定次序：既有的更早信号不被占位工作推翻，command 也不压过活动 run。
	precedenceCases := []struct {
		name  string
		shell string
		want  api.T3ThreadSummaryStatus
	}{
		{"archivedAt 最优先", `{"id":"t","status":"completed","archivedAt":"2026-10-01T00:00:00.000Z","pendingBackgroundTasks":[{"taskId":"b","kind":"subagent"}]}`, api.T3ThreadSummaryStatusArchived},
		{"pendingRuntimeRequest 压过占位工作", `{"id":"t","status":"completed","pendingRuntimeRequest":{"id":"rr","kind":"approval","createdAt":"2026-10-12T00:00:00.000Z"},"pendingBackgroundTasks":[{"taskId":"b","kind":"subagent"}]}`, api.T3ThreadSummaryStatusWaiting},
		{"activityRunStatus=waiting 压过占位工作", `{"id":"t","status":"completed","activityRunStatus":"waiting","pendingBackgroundTasks":[{"taskId":"b","kind":"subagent"}]}`, api.T3ThreadSummaryStatusWaiting},
		{"activityRunStatus=running 不被 command 压成 idle", `{"id":"t","status":"completed","activityRunStatus":"running","pendingBackgroundTasks":[{"taskId":"b","kind":"command"}]}`, api.T3ThreadSummaryStatusRunning},
	}
	for _, c := range precedenceCases {
		t.Run(c.name, func(t *testing.T) {
			if got := t3ThreadStatus(decode(t, c.shell)); got != c.want {
				t.Fatalf("status = %v, want %v", got, c.want)
			}
		})
	}
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("marshal fixture: %v", err))
	}
	return data
}

// t3ChunkFrame 组装流式块帧（effect rpc：同一 requestId 的多个 value 共享一次 Ack）。
func t3ChunkFrame(requestID any, values []json.RawMessage) map[string]any {
	return map[string]any{"_tag": "Chunk", "requestId": requestID, "values": values}
}

func mustJSONString(v any) string {
	return string(mustJSON(v))
}

// TestT3ThreadsSnapshotWriteFailureKeepsActive 覆盖：快照完整收下后连接立即
// 被服务端断开（收尾写必失败）——活跃线程不丢（先入内存），hub 以
// afterSequence 自动恢复订阅后回到 Ready，列表继续给出线程。
func TestT3ThreadsSnapshotWriteFailureKeepsActive(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-1", "活跃线程", "running", nil)},
	)
	e.fake.dropAfterActive = true // 快照送达即断连：客户端 Ack/Interrupt 写失败
	e.fake.mu.Unlock()

	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "断连机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// 第一次列表：快照已到手（断连发生在快照之后），线程照常返回。
	result, err := e.th.List(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Id != "th-1" {
		t.Fatalf("active threads must survive post-snapshot drop, got %+v", result.Items)
	}

	// hub 自动恢复：等服务端看到第二次订阅（带 afterSequence 的续订）。
	deadline := time.Now().Add(3 * time.Second)
	for e.fake.subscribeShellCount() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("hub did not resubscribe after the post-snapshot drop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == t3RPCSubscribeShell && strings.Contains(string(rpc.payload), "afterSequence") {
			var payload map[string]any
			if err := json.Unmarshal(rpc.payload, &payload); err != nil {
				t.Fatalf("payload: %v", err)
			}
			if payload["afterSequence"] != float64(1) {
				t.Fatalf("resume afterSequence = %v, want 1", payload["afterSequence"])
			}
		}
	}

	// 恢复后列表照常：内存数据 + 续订都指向同一线程集。
	recovered := waitForEnvStatus(t, func() (T3EnvironmentStatus, []T3ThreadSummary) {
		list, err := e.th.List(ctx, userID)
		if err != nil || len(list.Environments) == 0 {
			return "", nil
		}
		return list.Environments[0].Status, list.Items
	}, T3EnvironmentStatusOK, 3*time.Second)
	if len(recovered) != 1 || recovered[0].Id != "th-1" {
		t.Fatalf("threads must survive the drop across resubscribe, got %+v", recovered)
	}
}

// waitForEnvStatus 轮询直到环境状态达到期望（hub 的恢复/重连是异步的），
// 返回最后一次的线程列表。
func waitForEnvStatus(t *testing.T, read func() (T3EnvironmentStatus, []T3ThreadSummary), want T3EnvironmentStatus, timeout time.Duration) []T3ThreadSummary {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		status, items := read()
		if status == want && items != nil {
			return items
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for env status %v, last = %v (items=%d)", want, status, len(items))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestT3ThreadDetailRevokedCredentialHintsRepair 覆盖审查 r1#1：详情路径与
// 列表同一探测语义——凭证吊销时不再落到 WS 握手失败的一句笼统 50202，
// 而是「请重新配对」的明确指引。
func TestT3ThreadDetailRevokedCredentialHintsRepair(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-1", "活跃线程", "running", nil)},
	)
	e.fake.mu.Unlock()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "吊销机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	e.fake.revokeBearers()
	_, err = e.th.Detail(ctx, userID, env.Id, "th-1")
	if err == nil || t3ErrCode(err) != errs.ErrT3Upstream.Code {
		t.Fatalf("expected upstream error after revoke, got %v", err)
	}
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || !strings.Contains(appErr.Message, "重新配对") {
		t.Fatalf("expected re-pair hint in message, got %v", err)
	}
}

// TestT3ThreadsListCredentialRejectedSurfacesRepair 覆盖 hub 的 Failed 分类：
// 首次订阅前凭证已被吊销 → 探测 repair → View 给 Failed（凭证被拒），列表
// 标 repair 且无线程，而不是笼统 unreachable。
func TestT3ThreadsListCredentialRejectedSurfacesRepair(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-1", "活跃线程", "running", nil)},
	)
	e.fake.mu.Unlock()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "吊销列表机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 先吊销再列表：hub 首连探测 401 → Failed（凭证被拒）。
	e.fake.revokeBearers()
	result, err := e.th.List(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("expected no items when credentials rejected, got %+v", result.Items)
	}
	if len(result.Environments) != 1 || result.Environments[0].Status != T3EnvironmentStatusRepair {
		t.Fatalf("expected repair status, got %+v", result.Environments)
	}
	if msg := api.Deref(result.Environments[0].Error); !strings.Contains(msg, "重新配对") {
		t.Fatalf("expected re-pair hint, got %q", msg)
	}

	// 详情同样给「请重新配对」指引（Failed + 凭证被拒的专用文案）。
	_, err = e.th.Detail(ctx, userID, env.Id, "th-1")
	if err == nil || t3ErrCode(err) != errs.ErrT3Upstream.Code {
		t.Fatalf("expected upstream error, got %v", err)
	}
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || !strings.Contains(appErr.Message, "重新配对") {
		t.Fatalf("expected re-pair hint in detail error, got %v", err)
	}
}

// 凭证在订阅存活期间被吊销：重连探测被拒时视图是 Stale（已经有权威快照），
// 必须回到 repair 并给「请重新配对」——不能长期停在 degraded「正在重连」被
// 反复重试（codex 云端审查 P2）。
func TestT3ThreadsStaleCredentialRejectionSurfacesRepair(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-1", "活跃线程", "running", nil)},
	)
	e.fake.mu.Unlock()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "存活吊销机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// 等热起来：活跃面到得快、归档面可能稍后（归档未就绪时列表是 degraded），
	// 这里要的是「已有快照且健康」这个前置状态。
	listUntil(t, e, userID, func(res *T3ThreadListResponse) bool {
		return len(res.Items) == 1 && res.Environments[0].Status == T3EnvironmentStatusOK
	})

	// 已有快照后吊销凭证并断开连接：重连探测 401 → Stale + 凭证被拒。
	e.fake.revokeBearers()
	e.fake.dropConnections()

	repairDeadline := time.Now().Add(8 * time.Second)
	var status T3EnvironmentStatus
	var msg string
	for time.Now().Before(repairDeadline) {
		listed, listErr := e.th.List(ctx, userID)
		if listErr != nil {
			t.Fatalf("list: %v", listErr)
		}
		status = listed.Environments[0].Status
		msg = api.Deref(listed.Environments[0].Error)
		if status == T3EnvironmentStatusRepair {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if status != T3EnvironmentStatusRepair {
		t.Fatalf("stale credential rejection must map to repair, got %s (%q)", status, msg)
	}
	if !strings.Contains(msg, "重新配对") {
		t.Fatalf("expected re-pair hint, got %q", msg)
	}

	// 详情同样给「请重新配对」，而不是一句笼统的「正在同步」。
	_, err = e.th.Detail(ctx, userID, env.Id, "th-1")
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || !strings.Contains(appErr.Message, "重新配对") {
		t.Fatalf("expected re-pair hint in detail error, got %v", err)
	}
}

// TestT3ThreadsArchiveNotReadyDegrades 覆盖 G1 的「未就绪」半边：归档订阅已
// 受理但全量快照未到（两条连接进度独立）时，活跃线程照常返回、环境标
// degraded「归档线程暂不可读」；放行后归档快照到达，状态收敛为 ok。
func TestT3ThreadsArchiveNotReadyDegrades(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-1", "活跃线程", "running", nil)},
	)
	e.fake.archive = mustJSON(map[string]any{
		"schemaVersion": 1, "snapshotSequence": 2,
		"projects": []any{map[string]any{"id": "p-1", "title": "P"}},
		"threads":  []any{shellThread("th-2", "归档线程", "idle", map[string]any{"archivedAt": "2026-10-01T00:00:00.000Z"})},
	})
	gate := make(chan struct{})
	e.fake.archiveGate = gate
	e.fake.mu.Unlock()

	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "归档慢机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)}); err != nil {
		t.Fatalf("create: %v", err)
	}

	result, err := e.th.List(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(result.Items) != 1 || result.Items[0].Id != "th-1" {
		t.Fatalf("active threads must be served while archive is not ready, got %+v", result.Items)
	}
	if len(result.Environments) != 1 || result.Environments[0].Status != T3EnvironmentStatusDegraded {
		t.Fatalf("archive-not-ready must degrade the environment, got %+v", result.Environments)
	}
	if msg := api.Deref(result.Environments[0].Error); !strings.Contains(msg, "归档") {
		t.Fatalf("expected archive banner, got %q", msg)
	}

	// 放行归档快照 → 状态收敛 ok，归档线程出现。
	close(gate)
	items := waitForEnvStatus(t, func() (T3EnvironmentStatus, []T3ThreadSummary) {
		list, err := e.th.List(ctx, userID)
		if err != nil || len(list.Environments) == 0 {
			return "", nil
		}
		return list.Environments[0].Status, list.Items
	}, T3EnvironmentStatusOK, 3*time.Second)
	if len(items) != 2 {
		t.Fatalf("archived thread must appear after the snapshot arrives, got %+v", items)
	}
}

// TestT3ThreadsStaleAfterDisconnectNotTrusted 覆盖新鲜度契约：连接断开且
// 重连被拒（吊销）后进入 Stale——列表如实降级标注「连接已断开」；详情/Stop
// 不用陈旧快照回答运行态（陈旧 idle 会让 Stop 无事可做，也会把旧数据当可信）。
func TestT3ThreadsStaleAfterDisconnectNotTrusted(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-1", "活跃线程", "running", nil)},
	)
	e.fake.mu.Unlock()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "陈旧机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 先就绪一次（entry 建立、快照到手）。
	result, err := e.th.List(ctx, userID)
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("initial list must be ready, got %+v err=%v", result, err)
	}

	// 吊销凭证并断开连接：重连探测 401 → entry 有数据但连接失败 → Stale。
	e.fake.revokeBearers()
	e.fake.dropConnections()
	deadline := time.Now().Add(3 * time.Second)
	for {
		result, err = e.th.List(ctx, userID)
		if err == nil && len(result.Environments) == 1 && result.Environments[0].Status == T3EnvironmentStatusDegraded &&
			strings.Contains(api.Deref(result.Environments[0].Error), "连接已断开") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected stale degraded banner, last = %+v err=%v", result, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Stale 的内存线程仍如实展示。
	if len(result.Items) != 1 || result.Items[0].Id != "th-1" {
		t.Fatalf("stale list must keep serving in-memory threads, got %+v", result.Items)
	}

	// 详情：陈旧命中不当可信运行态 → 50202「暂时不可读」，绝不返回 running。
	_, err = e.th.Detail(ctx, userID, env.Id, "th-1")
	if err == nil || t3ErrCode(err) != errs.ErrT3Upstream.Code {
		t.Fatalf("expected upstream error for stale hit, got %v", err)
	}
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || !strings.Contains(appErr.Message, "暂时不可读") {
		t.Fatalf("expected unreadable-runtime message, got %v", err)
	}

	// Stop 同一口径：不基于陈旧快照下「无事可做」的结论。
	if _, err := e.th.StopThread(ctx, userID, env.Id, "th-1"); err == nil || t3ErrCode(err) != errs.ErrT3Upstream.Code {
		t.Fatalf("expected upstream error for stale stop, got %v", err)
	}
}

// TestT3ThreadsListReflectsPushDeltas 覆盖增量驱动：列表状态变化由订阅流
// 推送驱动（thread.updated / thread.removed），全程不重连、不重新全量订阅。
func TestT3ThreadsListReflectsPushDeltas(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-1", "增量线程", "running", nil)},
	)
	e.fake.mu.Unlock()

	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "增量机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)}); err != nil {
		t.Fatalf("create: %v", err)
	}

	result, err := e.th.List(ctx, userID)
	if err != nil || len(result.Items) != 1 || result.Items[0].Status != "running" {
		t.Fatalf("initial list = %+v err=%v", result, err)
	}

	// 推送运行态变化 → 列表跟随。
	e.fake.pushThreadDelta(t, shellThread("th-1", "增量线程", "idle", nil))
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, err = e.th.List(ctx, userID)
		if err == nil && len(result.Items) == 1 && result.Items[0].Status == "idle" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("delta did not surface in list, last = %+v err=%v", result, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 推送删除 → 线程从列表消失。
	e.fake.pushThreadRemoval(t, "th-1")
	deadline = time.Now().Add(2 * time.Second)
	for {
		result, err = e.th.List(ctx, userID)
		if err == nil && len(result.Items) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("removal did not surface in list, last = %+v err=%v", result, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 全程只发生一次冷启动全量订阅。
	if got := e.fake.subscribeShellCount(); got != 1 {
		t.Fatalf("push-driven updates must not resubscribe, subscribeShell count = %d", got)
	}
}

// TestT3EnvironmentUpdateAndDeleteDropHubEntry 覆盖环境服务与 hub 的接线：
// 重配（凭证变更）落库后 config_revision 自增、Drop 旧代 entry 并立即用重读
// 出的新行重建常驻订阅（INT-132），floor 记新世代；携带旧世代的迟到请求被拒
// 且不替换新代 entry；删除后 entry 无条件清掉（MaxInt64 哨兵），且任何世代
// 都被拒。
func TestT3EnvironmentUpdateAndDeleteDropHubEntry(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-1", "活跃线程", "running", nil)},
	)
	e.fake.mu.Unlock()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "换证机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// 列表建立 entry。
	if _, err := e.th.List(ctx, userID); err != nil {
		t.Fatalf("list: %v", err)
	}
	e.hub.mu.Lock()
	entries := len(e.hub.entries)
	e.hub.mu.Unlock()
	if entries != 1 {
		t.Fatalf("expected one hub entry after list, got %d", entries)
	}
	beforeRow, err := e.env.repo.FindByIDAndUserID(ctx, env.Id, userID)
	if err != nil {
		t.Fatalf("load env before re-pair: %v", err)
	}

	// 重配（凭证变更）→ config_revision 自增、Drop 旧代后立即以重读出的新行
	// 重建常驻订阅（INT-132）：entry 跟环境走，重配后不落后于新配置；floor
	// 记新世代，迟到请求仍被拒。
	e.fake.revokeBearers()
	if _, err := e.env.Update(ctx, userID, env.Id, &UpdateT3EnvironmentRequest{PairingUrl: strPtr(e.fake.pairingURL(e.fake.baseURL))}); err != nil {
		t.Fatalf("re-pair: %v", err)
	}
	afterRow, err := e.env.repo.FindByIDAndUserID(ctx, env.Id, userID)
	if err != nil {
		t.Fatalf("load env after re-pair: %v", err)
	}
	if afterRow.ConfigRevision != beforeRow.ConfigRevision+1 {
		t.Fatalf("re-pair must bump config_revision exactly once: %d -> %d", beforeRow.ConfigRevision, afterRow.ConfigRevision)
	}
	e.hub.mu.Lock()
	entries, floors := len(e.hub.entries), len(e.hub.floors)
	var entryRevision int64
	for _, en := range e.hub.entries {
		entryRevision = en.configRevision
	}
	e.hub.mu.Unlock()
	if entries != 1 || floors == 0 {
		t.Fatalf("re-pair must rebuild the hub entry and record floor, entries=%d floors=%d", entries, floors)
	}
	if entryRevision != afterRow.ConfigRevision {
		t.Fatalf("rebuilt entry must carry the new generation, entry=%d want=%d", entryRevision, afterRow.ConfigRevision)
	}
	// 携带旧世代（重配前读出的行）的迟到请求被拒，且不替换重建出的新代 entry。
	stale := *beforeRow
	staleToken, err := e.th.bearerToken(&stale)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if _, err := e.th.viewShell(ctx, &stale, staleToken); err == nil || !strings.Contains(err.Error(), "请重试") {
		t.Fatalf("stale revision view after re-pair = %v, want retryable 请重试", err)
	}
	e.hub.mu.Lock()
	entries = len(e.hub.entries)
	e.hub.mu.Unlock()
	if entries != 1 {
		t.Fatalf("stale view must not disturb the rebuilt entry, entries = %d", entries)
	}

	// 新凭证列表照常（entry 以新世代重建）。
	result, err := e.th.List(ctx, userID)
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("list after re-pair = %+v err=%v", result, err)
	}

	// 删除 → Drop（MaxInt64 哨兵：entry 无条件关闭，floor 拒绝任何世代）。
	if err := e.env.Delete(ctx, userID, env.Id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	e.hub.mu.Lock()
	entries = len(e.hub.entries)
	e.hub.mu.Unlock()
	if entries != 0 {
		t.Fatalf("delete must drop the hub entry, entries=%d", entries)
	}
	// 删除后任何世代都被拒：即使带着删除前最后读出的行（最新 revision）也不得
	// 复活订阅。
	last := *afterRow
	lastToken, err := e.th.bearerToken(&last)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if _, err := e.th.viewShell(ctx, &last, lastToken); err == nil || !strings.Contains(err.Error(), "请重试") {
		t.Fatalf("view after delete = %v, want retryable 请重试", err)
	}
}

// F5：版本守卫拒绝（errT3ShellConfigStale）不再内部重读环境重试——统一翻译
// 成「环境配置已更新，请重试」的可重试错误，且不得借重读把 entry 复活
// （重读会让同一操作拼成「新配置 shell + 旧配置投影/写」的跨环境错配）。
func TestT3ThreadsStaleConfigViewIsRetryable(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-1", "活跃线程", "running", nil)},
	)
	e.fake.mu.Unlock()

	env, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "配置变更机", PairingUrl: e.fake.pairingURL(e.fake.baseURL)})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := e.th.List(ctx, userID); err != nil {
		t.Fatalf("list: %v", err)
	}

	// 构造「配置已在别处被替换或删除（hub trail 已 Drop 到 tombstone）」的世界，
	// 迟到请求仍拿着 tombstone 之前的行与旧凭证。改名随后落库：它不递增
	// config_revision，改变不了任何请求携带的世代。
	row, err := e.env.repo.FindByIDAndUserID(ctx, env.Id, userID)
	if err != nil {
		t.Fatalf("load env: %v", err)
	}
	e.hub.Drop(userID, env.Id, row.ConfigRevision+1)
	if err := e.env.repo.Update(ctx, env.Id, userID, &repository.T3EnvironmentUpdate{
		Name:      api.Ptr("配置变更机-v2"),
		UpdatedAt: row.UpdatedAt.Add(time.Second),
	}); err != nil {
		t.Fatalf("rename env: %v", err)
	}

	stale := *row
	token, err := e.th.bearerToken(&stale)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	if _, err := e.th.viewShell(ctx, &stale, token); err == nil || !strings.Contains(err.Error(), "请重试") {
		t.Fatalf("stale view error = %v, want retryable 请重试", err)
	}
	// 内部重读会拿到与 tombstone 之前相同的世代（改名不递增）并复活 entry——
	// 新语义必须保持拒绝。
	e.hub.mu.Lock()
	entries := len(e.hub.entries)
	e.hub.mu.Unlock()
	if entries != 0 {
		t.Fatalf("stale view must not resurrect the entry via internal re-read, entries = %d", entries)
	}
}

// TestT3WSAuthInvalidErrorIsCredentialRejected 覆盖审查 r1#1 的第二条修法：
// RPC 层的 EnvironmentAuthInvalidError（无 message 的旧形态）与
// EnvironmentAuthorizationError 一样归成凭证失效。
func TestT3WSAuthInvalidErrorIsCredentialRejected(t *testing.T) {
	frame := rpcFrame{
		Exit: &rpcExit{
			Tag: "Failure",
			Cause: []rpcFailureCause{{
				Tag:   "Fail",
				Error: json.RawMessage(`{"_tag":"EnvironmentAuthInvalidError"}`),
			}},
		},
	}
	if _, err := frame.exitValue(); !errors.Is(err, errT3CredentialRejected) {
		t.Fatalf("EnvironmentAuthInvalidError must classify as credential rejected, got %v", err)
	}
}

// 环境增删改本身不产生线程级增量：必须显式发布用户级全局刷新，否则去掉
// 轮询后别的标签页会一直显示已删除/已新增的环境（Codex review P2）。
func TestT3EnvironmentCRUDPublishesUserRefresh(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	sub := e.hub.Subscribe(userID)
	defer sub.Close()

	isGlobal := func(ev T3ShellEvent) bool {
		return ev.Full && ev.EnvironmentID == ""
	}

	envID := pairWritable(t, e, userID, "推送机")
	awaitShellEvent(t, sub, isGlobal)

	renamed := "推送机（改名）"
	if _, err := e.env.Update(context.Background(), userID, envID, &UpdateT3EnvironmentRequest{Name: &renamed}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	awaitShellEvent(t, sub, isGlobal)

	if err := e.env.Delete(context.Background(), userID, envID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	awaitShellEvent(t, sub, isGlobal)
}

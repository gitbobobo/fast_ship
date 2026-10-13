package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// 真实语义：活跃 shell 快照的 archivedThreads 恒为空，归档线程只由
// orchestration.getArchivedShellSnapshot 一次性 RPC 提供；线程投影按 wire
// 形状携带 visibleTurnItems 且裁掉命令输出。
type fakeT3Environment struct {
	t  *testing.T
	mu sync.Mutex

	baseURL      string
	pairingCodes map[string]bool   // 有效配对码
	validBearers map[string]bool   // 有效 Bearer
	grantScope   string            // /oauth/token 回的 scope；默认 orchestration:read
	snapshot     json.RawMessage   // subscribeShell 首块 value（活跃快照，归档为空）
	archive      json.RawMessage   // getArchivedShellSnapshot 一次性返回值
	archiveFail  bool              // true 时归档 RPC 回 Exit Failure（G1 降级路径）
	projections  map[string]string // threadId -> wire 投影 JSON
	turnItems    map[string]string // itemId -> getTurnItem 返回的完整条目 JSON

	gotTokenForms []map[string]string
	gotRPC        []t3RecordedRPC // WS 一次性请求（方法+payload）

	tokenGate chan struct{} // 非空时 /oauth/token 阻塞直到关闭（并发交错测试用）
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
		grantScope:   t3ScopeOrchestrationRead,
		projections:  map[string]string{},
		turnItems:    map[string]string{},
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

func (f *fakeT3Environment) tokenFormCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.gotTokenForms)
}

func (f *fakeT3Environment) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
			f.handleToken(w, r)
		case r.Method == http.MethodGet && r.URL.Path == "/api/auth/session":
			f.handleSession(w, r)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/orchestration/threads/"):
			f.handleThreadSnapshot(w, r)
		case r.URL.Path == "/ws":
			f.handleWS(w, r)
		default:
			http.NotFound(w, r)
		}
	})
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
	f.mu.Lock()
	valid := f.validBearers[token]
	projection, ok := f.projections[strings.TrimPrefix(r.URL.Path, "/api/orchestration/threads/")]
	f.mu.Unlock()
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

func (f *fakeT3Environment) handleWS(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("orchestrationProtocol") != "2" {
		w.WriteHeader(http.StatusUpgradeRequired)
		_, _ = w.Write([]byte(`{"code":"orchestration_protocol_incompatible","message":"Update this client to one that supports orchestration protocol 2.","orchestrationProtocolVersion":2}`))
		return
	}
	websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		token := strings.TrimPrefix(conn.Request().Header.Get("Authorization"), "Bearer ")
		f.mu.Lock()
		valid := f.validBearers[token]
		snapshot := f.snapshot
		archive := f.archive
		archiveFail := f.archiveFail
		turnItems := make(map[string]string, len(f.turnItems))
		for id, item := range f.turnItems {
			turnItems[id] = item
		}
		f.mu.Unlock()
		for {
			var data []byte
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if err := websocket.Message.Receive(conn, &data); err != nil {
				return
			}
			var frame struct {
				Tag     string          `json:"_tag"`
				ID      any             `json:"id"`
				Method  string          `json:"tag"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(data, &frame); err != nil {
				return
			}
			switch frame.Tag {
			case "Request":
				if !valid {
					_ = sendT3Frame(conn, map[string]any{
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
				case "orchestration.subscribeShell":
					_ = sendT3Frame(conn, map[string]any{
						"_tag":      "Chunk",
						"requestId": frame.ID,
						"values":    []json.RawMessage{snapshot},
					})
					// 首块无需 Ack 即可发出；Interrupt 只结束订阅流，
					// 连接保留给同一会话上的后续一次性 RPC（真实语义）。
					f.waitStreamEnd(conn)
				case "orchestration.getArchivedShellSnapshot":
					if archiveFail {
						// 一次性 RPC 的 Exit Failure：归档面读取失败（G1 降级路径）。
						// 连接保留，与真实失败语义一致。
						_ = sendT3Frame(conn, exitFailureFrame(frame.ID, map[string]any{
							"_tag":    "UnknownException",
							"message": "archived shell snapshot unavailable",
						}))
						continue
					}
					_ = sendT3Frame(conn, exitSuccessFrame(frame.ID, archive))
				case "orchestration.getTurnItem":
					var payload struct {
						ItemID string `json:"itemId"`
					}
					_ = json.Unmarshal(frame.Payload, &payload)
					item, ok := turnItems[payload.ItemID]
					if !ok || item == "" {
						_ = sendT3Frame(conn, exitSuccessFrame(frame.ID, json.RawMessage(`{"item":null}`)))
						continue
					}
					_ = sendT3Frame(conn, exitSuccessFrame(frame.ID, json.RawMessage(`{"item":`+item+`}`)))
				default:
					_ = sendT3Frame(conn, exitSuccessFrame(frame.ID, json.RawMessage(`null`)))
				}
			case "Ack":
			case "Interrupt":
				return
			}
		}
	}).ServeHTTP(w, r)
}

// waitStreamEnd 吃掉订阅请求的 Ack/Interrupt 帧直到流被中断；
// 不关闭连接——同一 WS 会话还要服务后续一次性 RPC。
func (f *fakeT3Environment) waitStreamEnd(conn *websocket.Conn) {
	for {
		var data []byte
		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := websocket.Message.Receive(conn, &data); err != nil {
			return // 连接关闭视作结束
		}
		var frame struct {
			Tag string `json:"_tag"`
		}
		if err := json.Unmarshal(data, &frame); err != nil {
			return
		}
		if frame.Tag == "Interrupt" {
			return
		}
	}
}

// ------ 测试基建 ------

type t3TestEnv struct {
	db   *gorm.DB
	cfg  *config.Config
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
	th := NewT3ThreadsService(repo, cfg, zap.NewNop())
	// 测试预算：毫秒级，坏环境的隔离不需要真等秒级超时。
	th.budgets = t3RemoteBudgets{Round: 5 * time.Second, PerEnvironment: 2 * time.Second, ThreadDetail: 5 * time.Second, ItemDetail: 5 * time.Second}
	return &t3TestEnv{
		db:   db,
		cfg:  cfg,
		env:  NewT3EnvironmentService(repo, cfg, zap.NewNop()),
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
		"pullRequests":       []any{map[string]any{"number": 1}, map[string]any{"number": 2}},
		"updatedAt":          "2026-10-12T03:00:00.000Z",
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
	if stored.BearerScope != t3ScopeOrchestrationRead {
		t.Fatalf("scope = %q", stored.BearerScope)
	}
	if strings.Contains(string(stored.BearerTokenEncrypted), "bearer-") {
		t.Fatalf("bearer stored in plaintext")
	}

	// 兑换请求字段按真实协议
	if len(e.fake.gotTokenForms) != 1 {
		t.Fatalf("token exchanges = %d", len(e.fake.gotTokenForms))
	}
	form := e.fake.gotTokenForms[0]
	if form["grant_type"] != t3GrantTypeTokenExchange || form["scope"] != t3ScopeOrchestrationRead || form["client_label"] != "Fast Ship" {
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
				shellThread("th-1", "实现只读线程", "running", nil),
				// 等待审批 + 已排队新 run：pending 优先于 run 状态 → waiting（GPT10）
				shellThread("th-2", "等待审批", "queued", map[string]any{
					"pendingRuntimeRequest": map[string]any{"id": "rr-1", "kind": "approval", "createdAt": "2026-10-12T04:00:00.000Z"},
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

	result, err := e.th.List(ctx, userID)
	if err != nil {
		t.Fatalf("list threads: %v", err)
	}
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
	if byID["th-2"].Status != "waiting" {
		t.Fatalf("th-2 should be waiting (pendingRuntimeRequest wins over queued run), got %+v", byID["th-2"])
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

	// 归档读取走的是独立 RPC（活跃快照里归档为空）
	var sawArchiveRPC bool
	for _, rpc := range e.fake.recordedRPC() {
		if rpc.method == "orchestration.getArchivedShellSnapshot" {
			sawArchiveRPC = true
		}
	}
	if !sawArchiveRPC {
		t.Fatalf("expected orchestration.getArchivedShellSnapshot one-shot RPC")
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

	// 凭证吊销 → 该环境 repair，错误保留在 environments，items 清空
	e.fake.revokeBearers()
	result, err = e.th.List(ctx, userID)
	if err != nil {
		t.Fatalf("list threads after revoke: %v", err)
	}
	if len(result.Items) != 0 {
		t.Fatalf("expected no items after revoke, got %d", len(result.Items))
	}
	revokedStatuses := map[string]T3EnvironmentStatus{}
	for _, item := range result.Environments {
		revokedStatuses[item.Id] = item.Status
	}
	if revokedStatuses[healthy.Id] != T3EnvironmentStatusRepair {
		t.Fatalf("expected repair for healthy env after revoke, got %+v", result.Environments)
	}

	// 不存在的环境
	if _, err := e.th.Detail(ctx, userID, "no-env", "th-1"); err == nil || t3ErrCode(err) != errs.ErrT3EnvironmentNotFound.Code {
		t.Fatalf("expected env not found, got %v", err)
	}
}

// TestT3ThreadsSlowEnvironmentIsolated 慢环境吃满单环境预算后，
// 健康环境结果照常返回（panel r1 #9：坏环境不拖垮健康结果）。
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
	e.env.budgets = t3RemoteBudgets{Round: 5 * time.Second, PerEnvironment: 300 * time.Millisecond, ThreadDetail: 5 * time.Second, ItemDetail: 5 * time.Second}
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

	e.th.budgets = t3RemoteBudgets{Round: 5 * time.Second, PerEnvironment: 300 * time.Millisecond, ThreadDetail: 5 * time.Second, ItemDetail: 5 * time.Second}
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
	for _, item := range result.Environments {
		statuses[item.Id] = item.Status
	}
	if statuses[slowEnvID(t, e, userID, "慢")] != T3EnvironmentStatusUnreachable {
		t.Fatalf("slow env should be unreachable, got %+v", result.Environments)
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

	// 同一线程两次详情：运行中（后台 run）→ 静止（idle），状态跟随 shell 而非投影
	run, err := e.th.Detail(ctx, userID, env.Id, "th-run")
	if err != nil {
		t.Fatalf("running detail: %v", err)
	}
	if run.Thread.Status != "running" {
		t.Fatalf("expected running from activityRunStatus, got %+v", run.Thread)
	}
	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture(
		[]any{map[string]any{"id": "p-1", "title": "P"}},
		[]any{shellThread("th-run", "后台跑", "idle", nil)}, // run 停了
	)
	e.fake.mu.Unlock()
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
// 失败是部分失败——活跃线程照常返回，环境标 unreachable 并附「归档线程
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
	if envStatus.Status != T3EnvironmentStatusUnreachable {
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
		{"subagent 占位", `[{"taskId":"bg-1","kind":"subagent"}]`, api.Running},
		{"monitor 占位", `[{"taskId":"bg-1","kind":"monitor"}]`, api.Running},
		{"background_task 占位", `[{"taskId":"bg-1","kind":"background_task"}]`, api.Running},
		{"未知 kind 占位（上游回退 background_task）", `[{"taskId":"bg-1","kind":"hologram"}]`, api.Running},
		{"kind 缺省占位（旧 roster 按 background_task 加载）", `[{"taskId":"bg-1"}]`, api.Running},
		{"command 不占位", `[{"taskId":"bg-1","kind":"command"}]`, api.Idle},
		{"command 与占位混合", `[{"taskId":"b1","kind":"command"},{"taskId":"b2","kind":"monitor"}]`, api.Running},
		{"空数组不占位", `[]`, api.Idle},
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
		{"archivedAt 最优先", `{"id":"t","status":"completed","archivedAt":"2026-10-01T00:00:00.000Z","pendingBackgroundTasks":[{"taskId":"b","kind":"subagent"}]}`, api.Archived},
		{"pendingRuntimeRequest 压过占位工作", `{"id":"t","status":"completed","pendingRuntimeRequest":{"id":"rr","kind":"approval","createdAt":"2026-10-12T00:00:00.000Z"},"pendingBackgroundTasks":[{"taskId":"b","kind":"subagent"}]}`, api.Waiting},
		{"activityRunStatus=waiting 压过占位工作", `{"id":"t","status":"completed","activityRunStatus":"waiting","pendingBackgroundTasks":[{"taskId":"b","kind":"subagent"}]}`, api.Waiting},
		{"activityRunStatus=running 不被 command 压成 idle", `{"id":"t","status":"completed","activityRunStatus":"running","pendingBackgroundTasks":[{"taskId":"b","kind":"command"}]}`, api.Running},
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

func mustJSONString(v any) string {
	return string(mustJSON(v))
}

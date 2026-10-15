package middleware

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/godbobo/fast_ship/server/internal/pkg/selflog"
)

// requestLogEnv 是请求日志中间件的测试台架：真实独立日志库 + gin 引擎。
type requestLogEnv struct {
	router   *gin.Engine
	store    *selflog.Store
	pipeline *selflog.Pipeline
}

func newRequestLogEnv(t *testing.T) *requestLogEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	store, err := selflog.OpenStore(filepath.Join(t.TempDir(), "self_logs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	pipeline, err := selflog.Start(selflog.Options{
		Sink:          store,
		Stdout:        slog.NewTextHandler(io.Discard, nil),
		FlushInterval: 5 * time.Millisecond,
		MinLevel:      slog.LevelDebug,
	})
	if err != nil {
		t.Fatalf("start pipeline: %v", err)
	}
	t.Cleanup(pipeline.Close)

	router := gin.New()
	router.Use(RequestLog(slog.New(pipeline.Handler())))
	return &requestLogEnv{router: router, store: store, pipeline: pipeline}
}

func (e *requestLogEnv) do(t *testing.T, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// traceByRequestID 关闭管道（触发冲刷）后按请求 ID 查链路。
func (e *requestLogEnv) traceByRequestID(t *testing.T, requestID string) []selflog.Entry {
	t.Helper()
	e.pipeline.Close()
	page, err := e.store.Query(context.Background(), selflog.Filter{RequestID: requestID}, "", 100, true)
	if err != nil {
		t.Fatalf("query trace: %v", err)
	}
	return page.Items
}

func TestRequestLogAssignsAndEchoesRequestID(t *testing.T) {
	env := newRequestLogEnv(t)
	env.router.GET("/api/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	w := env.do(t, http.MethodGet, "/api/ping", nil)
	if got := w.Header().Get(RequestIDHeader); got == "" {
		t.Fatalf("响应缺少 X-Request-Id")
	}
}

func TestRequestLogHonorsIncomingRequestID(t *testing.T) {
	env := newRequestLogEnv(t)
	env.router.GET("/api/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"request_id": RequestIDOf(c)})
	})

	w := env.do(t, http.MethodGet, "/api/ping", map[string]string{RequestIDHeader: "my-trace-id"})
	if got := w.Header().Get(RequestIDHeader); got != "my-trace-id" {
		t.Fatalf("X-Request-Id = %q, want 透传 my-trace-id", got)
	}
	entries := env.traceByRequestID(t, "my-trace-id")
	if len(entries) == 0 {
		t.Fatalf("调用方自带请求 ID 未贯穿到日志")
	}
}

func TestRequestLogRecordsRouteStatusDuration(t *testing.T) {
	env := newRequestLogEnv(t)
	env.router.GET("/api/things/:id", func(c *gin.Context) {
		status := http.StatusOK
		if c.Query("fail") == "1" {
			status = http.StatusBadGateway
		}
		c.JSON(status, gin.H{"ok": true})
	})

	env.do(t, http.MethodGet, "/api/things/abc", map[string]string{RequestIDHeader: "req-200"})
	w := env.do(t, http.MethodGet, "/api/things/xyz?fail=1", map[string]string{RequestIDHeader: "req-502"})
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d", w.Code)
	}

	entries := env.traceByRequestID(t, "req-200")
	if len(entries) != 1 || entries[0].Level != selflog.LevelInfo {
		t.Fatalf("200 请求应只落一条 INFO: %+v", entries)
	}

	entries = env.traceByRequestID(t, "req-502")
	var done *selflog.Entry
	for i := range entries {
		if entries[i].Message == "请求完成" {
			done = &entries[i]
		}
	}
	if done == nil {
		t.Fatalf("缺少请求完成日志: %+v", entries)
	}
	if done.Level != selflog.LevelError {
		t.Fatalf("5xx 级别 = %s, want ERROR", done.Level)
	}
	if done.Extra == "" || !strings.Contains(done.Extra, `"route":"/api/things/:id"`) {
		t.Fatalf("路由模板缺失: %s", done.Extra)
	}
	if !strings.Contains(done.Extra, `"status":502`) || !strings.Contains(done.Extra, "duration_ms") {
		t.Fatalf("状态码/耗时缺失: %s", done.Extra)
	}
	if !strings.Contains(done.Extra, "stack") {
		t.Fatalf("5xx 应附堆栈: %s", done.Extra)
	}
}

func TestRequestLogPanicReturns500AndLogsStack(t *testing.T) {
	env := newRequestLogEnv(t)
	env.router.GET("/api/boom", func(c *gin.Context) {
		panic("boom in handler")
	})

	w := env.do(t, http.MethodGet, "/api/boom", map[string]string{RequestIDHeader: "req-panic"})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("panic 后 status = %d, want 500", w.Code)
	}

	// 验收场景：仅凭响应头请求 ID 可查到完整链路（panic 条目 + 堆栈 + 完成条目）。
	requestID := w.Header().Get(RequestIDHeader)
	entries := env.traceByRequestID(t, requestID)
	var panicEntry, doneEntry *selflog.Entry
	for i := range entries {
		switch entries[i].Message {
		case "请求处理 panic":
			panicEntry = &entries[i]
		case "请求完成":
			doneEntry = &entries[i]
		}
	}
	if panicEntry == nil || doneEntry == nil {
		t.Fatalf("panic 链路缺失: %+v", entries)
	}
	if !strings.Contains(panicEntry.Extra, "boom in handler") || !strings.Contains(panicEntry.Extra, "stack") {
		t.Fatalf("panic 条目缺堆栈: %s", panicEntry.Extra)
	}
	if !strings.Contains(panicEntry.Extra, "request_log_test.go") {
		t.Fatalf("堆栈应指向 panic 源: %s", panicEntry.Extra)
	}
	if doneEntry.Level != selflog.LevelError {
		t.Fatalf("panic 请求完成级别 = %s, want ERROR", doneEntry.Level)
	}
}

func TestRequestLogDoesNotRecordAuthorizationOrBody(t *testing.T) {
	env := newRequestLogEnv(t)
	env.router.POST("/api/echo", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/echo", strings.NewReader(
		`{"token":fakeSecret("fsk_", "SuperSecretTokenValue99"),"password":"hunter2-plain"}`))
	req.Header.Set(RequestIDHeader, "req-body")
	req.Header.Set("Authorization", "Bearer "+fakeSecret("fsk_", "SuperSecretTokenValue99")+"")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	entries := env.traceByRequestID(t, "req-body")
	if len(entries) == 0 {
		t.Fatalf("应至少有一条请求完成日志")
	}
	var dump strings.Builder
	for _, e := range entries {
		dump.WriteString(e.Message)
		dump.WriteString(e.Extra)
		dump.WriteString(e.RequestID)
	}
	for _, secret := range []string{fakeSecret("fsk_", "SuperSecretTokenValue99"), "hunter2-plain", "Authorization"} {
		if strings.Contains(dump.String(), secret) {
			t.Fatalf("请求日志泄漏 %q:\n%s", secret, dump.String())
		}
	}
}

func TestRequestLogRejectsSecretShapedRequestID(t *testing.T) {
	env := newRequestLogEnv(t)
	env.router.GET("/api/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	secret := fakeSecret("fsk_", "AbCdEfGh12345678")
	w := env.do(t, http.MethodGet, "/api/ping", map[string]string{RequestIDHeader: secret})
	got := w.Header().Get(RequestIDHeader)
	if got == "" || got == secret || strings.Contains(got, "fsk_") {
		t.Fatalf("敏感形状的 X-Request-Id 应被丢弃并改生成, got %q", got)
	}
	env.pipeline.Close()
	page, err := env.store.Query(context.Background(), selflog.Filter{}, "", 20, false)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for _, e := range page.Items {
		dump := e.Message + e.Extra + e.RequestID
		if strings.Contains(dump, secret) {
			t.Fatalf("外部可控 request_id 明文入库: %s", dump)
		}
	}
}

func TestRequestLogSkipsStaticSuccess(t *testing.T) {
	env := newRequestLogEnv(t)
	env.router.GET("/assets/app.js", func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	env.router.GET("/api/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })

	env.do(t, http.MethodGet, "/assets/app.js", map[string]string{RequestIDHeader: "req-static"})
	env.do(t, http.MethodGet, "/api/ping", map[string]string{RequestIDHeader: "req-api"})

	if got := len(env.traceByRequestID(t, "req-static")); got != 0 {
		t.Fatalf("静态 2xx 不应入库, got %d", got)
	}
	entries := env.traceByRequestID(t, "req-api")
	if len(entries) == 0 {
		t.Fatalf("API 2xx 应入库")
	}
}

func TestRequestLogVisibilityBindsUserAfterAuth(t *testing.T) {
	env := newRequestLogEnv(t)
	env.router.Use(func(c *gin.Context) {
		if uid := c.GetHeader("X-Test-User"); uid != "" {
			c.Set(ContextKeyUserID, uid)
			c.Set(ContextKeyAuthType, AuthTypeJWT)
			BindRequestUser(c)
		}
		c.Next()
	})
	env.router.GET("/api/secret", func(c *gin.Context) {
		LoggerFrom(c).Info("业务动作", slog.String("issue_id", "iss-vis"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	env.router.GET("/api/boom", func(c *gin.Context) {
		panic("boom-vis")
	})

	env.do(t, http.MethodGet, "/api/secret", map[string]string{
		RequestIDHeader: "req-alice",
		"X-Test-User":   "user-alice",
	})
	env.do(t, http.MethodGet, "/api/secret", map[string]string{
		RequestIDHeader: "req-bob",
		"X-Test-User":   "user-bob",
	})
	w := env.do(t, http.MethodGet, "/api/boom", map[string]string{
		RequestIDHeader: "req-alice-panic",
		"X-Test-User":   "user-alice",
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d", w.Code)
	}

	slog.New(env.pipeline.Handler()).Info("服务启动", slog.String("module", "app"))
	env.pipeline.Close()

	alice, err := env.store.Query(context.Background(), selflog.Filter{VisibleUserID: "user-alice"}, "", 50, true)
	if err != nil {
		t.Fatalf("query alice: %v", err)
	}
	bob, err := env.store.Query(context.Background(), selflog.Filter{VisibleUserID: "user-bob"}, "", 50, true)
	if err != nil {
		t.Fatalf("query bob: %v", err)
	}

	aliceHasBiz, aliceHasBob, aliceHasStartup, aliceHasPanic := false, false, false, false
	for _, e := range alice.Items {
		if e.UserID == "user-bob" || e.RequestID == "req-bob" || e.Message == "业务动作" && e.UserID != "user-alice" {
			aliceHasBob = true
		}
		if e.Message == "业务动作" && e.UserID == "user-alice" && e.RequestID == "req-alice" {
			aliceHasBiz = true
		}
		if e.Message == "服务启动" && e.UserID == "" {
			aliceHasStartup = true
		}
		if e.Message == "请求处理 panic" && e.UserID == "user-alice" {
			aliceHasPanic = true
		}
	}
	if !aliceHasBiz {
		t.Fatalf("alice 应看到自己真实写入路径的业务日志: %+v", alice.Items)
	}
	if aliceHasBob {
		t.Fatalf("alice 不应看到 bob 的无归属/错归属日志")
	}
	if !aliceHasStartup {
		t.Fatalf("alice 应仍能看到进程级启动日志")
	}
	if !aliceHasPanic {
		t.Fatalf("panic 路径应带 user_id: %+v", alice.Items)
	}

	bobHasAlice := false
	bobHasBiz := false
	for _, e := range bob.Items {
		if e.UserID == "user-alice" || e.RequestID == "req-alice" {
			bobHasAlice = true
		}
		if e.Message == "业务动作" && e.UserID == "user-bob" {
			bobHasBiz = true
		}
	}
	if bobHasAlice {
		t.Fatalf("bob 不应看到 alice 的日志")
	}
	if !bobHasBiz {
		t.Fatalf("bob 应看到自己的业务日志")
	}
}

func TestRequestLogHandlerScopeLoggerCarriesRequestID(t *testing.T) {
	env := newRequestLogEnv(t)
	env.router.GET("/api/things/:id", func(c *gin.Context) {
		LoggerFrom(c).Warn("读取协作区失败，详情降级",
			slog.String("issue_id", "iss-1"),
			slog.String("error", "collab unavailable"))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	env.do(t, http.MethodGet, "/api/things/abc", map[string]string{RequestIDHeader: "req-handler"})
	entries := env.traceByRequestID(t, "req-handler")
	var degraded *selflog.Entry
	for i := range entries {
		if entries[i].Message == "读取协作区失败，详情降级" {
			degraded = &entries[i]
		}
	}
	if degraded == nil {
		t.Fatalf("handler 级日志未入库: %+v", entries)
	}
	if degraded.Module != "http" || degraded.IssueID != "iss-1" {
		t.Fatalf("handler 日志字段缺失: %+v", degraded)
	}
}

// fakeSecret 运行时拼接出密钥形状的假值；脱敏断言依赖形状，
// 但字面量直接写进源码会被密钥扫描工具误杀，故运行时拼接。
func fakeSecret(prefix, suffix string) string {
	return prefix + suffix
}

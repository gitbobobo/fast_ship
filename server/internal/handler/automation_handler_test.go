package handler

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 自动化端点鉴权定向测试（INT-90）：JWT 管理端点的跨用户隔离与 API Key 拒绝、
// Agent 端点的运行令牌语义（fsk_ 40304、无效 40106、消费后读取失效）。

type automationHandlerEnv struct {
	db     *gorm.DB
	router *gin.Engine
	agent  *service.AutomationAgentService
	tokens *repository.AutomationRunTokenRepository
	runs   *repository.AutomationRunRepository
	cfg    *config.Config
}

func setupAutomationHandlerEnv(t *testing.T) *automationHandlerEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.ApiKey{}, &model.T3Environment{},
		&model.JWTBlacklist{}, &model.RefreshToken{},
		&model.Automation{}, &model.AutomationRun{}, &model.AutomationRunToken{}, &model.Notification{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_automation_trigger ON automation_runs(automation_id, trigger_key)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_active_once ON automation_runs(automation_id) WHERE finished_at IS NULL OR post_state = 'pending'")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_user_dedup ON notifications(user_id, dedup_key)")

	cfg := &config.Config{
		JWT:        config.JWTConfig{Secret: "test-secret", ExpireHours: 24},
		Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"},
		Server:     config.ServerConfig{PublicURL: "https://fs.example.com"},
	}
	logger := zap.NewNop()

	autos := repository.NewAutomationRepository(db)
	runs := repository.NewAutomationRunRepository(db)
	tokens := repository.NewAutomationRunTokenRepository(db)
	sink := service.NewNotificationSink(db)
	agent := service.NewAutomationAgentService(db, runs, tokens, nil, sink, logger)
	automationHandler := NewAutomationHandler(service.NewAutomationService(db, autos, runs,
		repository.NewT3EnvironmentRepository(db), repository.NewProjectRepository(db),
		repository.NewIssueRepository(db), sink, cfg, logger))
	notificationHandler := NewNotificationHandler(service.NewNotificationService(repository.NewNotificationRepository(db)))
	agentHandler := NewAutomationAgentHandler(agent)
	authService := service.NewAuthService(repository.NewUserRepository(db), repository.NewJWTBlacklistRepository(db), repository.NewRefreshTokenRepository(db), cfg)
	apiKeyRepo := repository.NewApiKeyRepository(db)
	_ = apiKeyRepo

	r := gin.New()
	r.GET("/api/automations", middleware.RequireJWT(cfg, authService), automationHandler.List)
	r.POST("/api/automations", middleware.RequireJWT(cfg, authService), automationHandler.Create)
	r.GET("/api/automations/:id", middleware.RequireJWT(cfg, authService), automationHandler.Get)
	r.DELETE("/api/automations/:id", middleware.RequireJWT(cfg, authService), automationHandler.Delete)
	r.GET("/api/notifications", middleware.RequireJWT(cfg, authService), notificationHandler.List)
	r.POST("/api/automation-agent/report", middleware.RequireRunToken(agent, false), agentHandler.Report)
	r.GET("/api/automation-agent/threads", middleware.RequireRunToken(agent, true), agentHandler.ListThreads)
	r.POST("/api/automation-agent/notifications", middleware.RequireRunToken(agent, true), agentHandler.PublishNotification)
	return &automationHandlerEnv{db: db, router: r, agent: agent, tokens: tokens, runs: runs, cfg: cfg}
}

func (e *automationHandlerEnv) createUser(t *testing.T) *model.User {
	t.Helper()
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@t", PasswordHash: "x"}
	if err := e.db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

// mintJWT 按生产签名口径签一个测试 JWT。
func (e *automationHandlerEnv) mintJWT(userID string) string {
	claims := jwt.MapClaims{
		"sub":      userID,
		"username": "tester",
		"jti":      uuid.NewString(),
		"iat":      time.Now().Unix(),
		"exp":      time.Now().Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(e.cfg.JWT.Secret))
	if err != nil {
		panic(err)
	}
	return signed
}

func (e *automationHandlerEnv) createApiKey(t *testing.T, userID string) string {
	t.Helper()
	raw := uuid.NewString()
	if err := e.db.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    userID,
		Name:      "ci",
		KeyHash:   service.HashApiKey(raw),
		KeyPrefix: raw[:8],
		CreatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("create api key: %v", err)
	}
	return service.FormatApiKey(raw)
}

func (e *automationHandlerEnv) do(t *testing.T, method, path, authorization string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", "Bearer "+authorization)
	}
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

func (e *automationHandlerEnv) seedAutomation(t *testing.T, userID string) *model.Automation {
	t.Helper()
	now := time.Now().UTC()
	env := &model.T3Environment{
		ID: uuid.NewString(), UserID: userID, Name: "env-" + uuid.NewString()[:8],
		Address: "https://t3.example.com", BearerTokenEncrypted: []byte("x"),
		BearerExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := e.db.Create(env).Error; err != nil {
		t.Fatalf("create env: %v", err)
	}
	a := &model.Automation{
		ID: uuid.NewString(), UserID: userID, Name: "auto", Enabled: true,
		CreatedAt: now, UpdatedAt: now, EnvironmentID: env.ID, T3ProjectID: "p",
		TriggerType: model.AutomationTriggerIssueCreated, PromptTemplate: "go",
		RuntimeMode: "full-access", TimeoutSeconds: 3600,
		PostAction: model.AutomationPostNone,
	}
	if err := e.db.Create(a).Error; err != nil {
		t.Fatalf("create automation: %v", err)
	}
	return a
}

func (e *automationHandlerEnv) seedRunWithToken(t *testing.T, a *model.Automation) (*model.AutomationRun, string) {
	t.Helper()
	now := time.Now().UTC()
	run := &model.AutomationRun{
		ID: uuid.NewString(), UserID: a.UserID, AutomationID: a.ID,
		TriggerType: a.TriggerType, TriggerKey: "event:" + uuid.NewString(),
		TriggeredAt: now, ConfigSnapshotJSON: `{"prompt_template":"go","runtime_mode":"full-access","post_action":"none"}`,
		EnvironmentID: a.EnvironmentID, T3ProjectID: a.T3ProjectID,
		ThreadID: uuid.NewString(), LaunchCommandID: uuid.NewString(),
		InitialMessageID: uuid.NewString(), Status: model.AutomationRunDispatched,
		DeadlineAt: now.Add(time.Hour), PostState: model.AutomationPostStateNone,
	}
	if err := e.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	raw := mintRunToken()
	if err := e.db.Create(&model.AutomationRunToken{
		RunID: run.ID, TokenHash: hashRunTokenForTest(raw),
		CreatedAt: now, ExpiresAt: run.DeadlineAt.Add(5 * time.Minute),
	}).Error; err != nil {
		t.Fatalf("save token: %v", err)
	}
	return run, raw
}

// mintRunToken/hashRunTokenForTest 与 service 派发口径一致：fsr_ + 32 字节
// base64url，SHA-256 存哈希。
func mintRunToken() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return "fsr_" + base64.RawURLEncoding.EncodeToString(buf)
}

func hashRunTokenForTest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func decodeAutomationEnvelope(t *testing.T, rec *httptest.ResponseRecorder) (int, string) {
	t.Helper()
	var envelope struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, rec.Body.String())
	}
	return envelope.Code, envelope.Message
}

func TestAutomationEndpoints_RejectAPIKey(t *testing.T) {
	env := setupAutomationHandlerEnv(t)
	user := env.createUser(t)
	apiKey := env.createApiKey(t, user.ID)

	rec := env.do(t, http.MethodGet, "/api/automations", apiKey, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("api key on automations list = %d, want 403", rec.Code)
	}
	if code, _ := decodeAutomationEnvelope(t, rec); code != 40301 {
		t.Fatalf("code = %d, want 40301", code)
	}
}

func TestAutomationEndpoints_CrossUserIs404(t *testing.T) {
	env := setupAutomationHandlerEnv(t)
	owner := env.createUser(t)
	other := env.createUser(t)
	a := env.seedAutomation(t, owner.ID)

	// 其它用户的 JWT 读/删自动化：404（跨用户与不存在同语义）。
	rec := env.do(t, http.MethodGet, "/api/automations/"+a.ID, env.mintJWT(other.ID), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user get = %d, want 404", rec.Code)
	}
	if code, _ := decodeAutomationEnvelope(t, rec); code != 40421 {
		t.Fatalf("code = %d, want 40421", code)
	}
	rec = env.do(t, http.MethodDelete, "/api/automations/"+a.ID, env.mintJWT(other.ID), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user delete = %d, want 404", rec.Code)
	}

	// 所属用户可见。
	rec = env.do(t, http.MethodGet, "/api/automations/"+a.ID, env.mintJWT(owner.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner get = %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAgentEndpoints_RunTokenSemantics(t *testing.T) {
	env := setupAutomationHandlerEnv(t)
	owner := env.createUser(t)
	other := env.createUser(t)
	_ = env.seedAutomation(t, owner.ID)
	a := env.seedAutomation(t, owner.ID)
	run, token := env.seedRunWithToken(t, a)
	apiKey := env.createApiKey(t, owner.ID)

	// fsk_ API Key 打 Agent 端点：40304（不获得任何 Agent 能力）。
	rec := env.do(t, http.MethodGet, "/api/automation-agent/threads", apiKey, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("api key on agent threads = %d, want 403", rec.Code)
	}
	if code, _ := decodeAutomationEnvelope(t, rec); code != 40304 {
		t.Fatalf("code = %d, want 40304", code)
	}

	// 无令牌/垃圾令牌：40106。
	rec = env.do(t, http.MethodGet, "/api/automation-agent/threads", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token = %d, want 401", rec.Code)
	}
	rec = env.do(t, http.MethodGet, "/api/automation-agent/threads", "not-a-token", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("garbage token = %d, want 401", rec.Code)
	}
	if code, _ := decodeAutomationEnvelope(t, rec); code != 40106 {
		t.Fatalf("code = %d, want 40106", code)
	}

	// 令牌有效但 phase 非法：40001。
	rec = env.do(t, http.MethodPost, "/api/automation-agent/report", token, []byte(`{"phase":"sideways"}`))
	if code, _ := decodeAutomationEnvelope(t, rec); code != 40001 {
		t.Fatalf("bad phase code = %d body=%s, want 40001", code, rec.Body.String())
	}

	// started → finished → 读取端点失效。
	rec = env.do(t, http.MethodPost, "/api/automation-agent/report", token, []byte(`{"phase":"started"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("started = %d body=%s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "/api/automation-agent/report", token, []byte(`{"phase":"finished","summary":"done"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("finished = %d body=%s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodGet, "/api/automation-agent/threads", token, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("threads after consume = %d, want 401", rec.Code)
	}

	// 完全相同结束回报可重放；不同负载 40917。
	rec = env.do(t, http.MethodPost, "/api/automation-agent/report", token, []byte(`{"phase":"finished","summary":"done"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("identical replay = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	rec = env.do(t, http.MethodPost, "/api/automation-agent/report", token, []byte(`{"phase":"finished","summary":"改口"}`))
	if code, _ := decodeAutomationEnvelope(t, rec); code != 40917 {
		t.Fatalf("different replay code = %d, want 40917", code)
	}

	_ = run
	_ = other
}

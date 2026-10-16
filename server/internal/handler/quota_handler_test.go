package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/quota"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type quotaHandlerEnv struct {
	db         *gorm.DB
	router     *gin.Engine
	cfg        *config.Config
	auth       *service.AuthService
	apiKeyRepo *repository.ApiKeyRepository
}

// setupQuotaHandlerEnv 建一个只含 quota 路由的测试环境，registry 换成假供应商，
// 不触网。
func setupQuotaHandlerEnv(t *testing.T, fetch quota.Fetcher) *quotaHandlerEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.ApiKey{}, &model.JWTBlacklist{}, &model.RefreshToken{}, &model.QuotaAccount{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := &config.Config{
		JWT:        config.JWTConfig{Secret: "test-secret", ExpireHours: 24},
		Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"},
	}
	authService := service.NewAuthService(repository.NewUserRepository(db), repository.NewJWTBlacklistRepository(db), repository.NewRefreshTokenRepository(db), cfg, nil)
	apiKeyRepo := repository.NewApiKeyRepository(db)

	if fetch == nil {
		fetch = func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
			return quota.Snapshot{
				PlanName: "Pro",
				Windows: []quota.Window{
					{Kind: quota.WindowFiveHour, Label: "5 小时", UsedPercent: quota.PercentPtr(38.0), Unit: quota.UnitPercent},
				},
				FetchedAt: time.Now().UTC(),
			}, nil
		}
	}
	providers := []quota.Provider{{
		ID: "fake", Name: "Fake", PlanHint: "Pro",
		DefaultBaseURL: "https://fake.example.com",
		CredentialFields: []quota.CredentialField{
			{Key: "api_key", Label: "API Key", Secret: true, Required: true},
		},
		Instructions: []string{"复制 API key"},
		Fetch:        fetch,
	}}
	quotaService := service.NewQuotaService(repository.NewQuotaAccountRepository(db), cfg, zap.NewNop(),
		func() []quota.Provider { return providers })
	h := NewQuotaHandler(quotaService)

	r := gin.New()
	quotaJWT := r.Group("/api/quota", middleware.RequireJWT(cfg, authService))
	quotaJWT.GET("/providers", h.ListProviders)
	quotaJWT.POST("/accounts", h.CreateAccount)
	quotaJWT.PUT("/accounts/:id", h.UpdateAccount)
	quotaJWT.DELETE("/accounts/:id", h.DeleteAccount)
	quotaJWT.POST("/accounts/:id/refresh", h.RefreshAccount)
	quotaJWT.POST("/test", h.Test)
	r.GET("/api/quota/accounts", middleware.RequireAuth(cfg, apiKeyRepo, authService), h.ListAccounts)

	return &quotaHandlerEnv{db: db, router: r, cfg: cfg, auth: authService, apiKeyRepo: apiKeyRepo}
}

func (e *quotaHandlerEnv) createUser(t *testing.T) *model.User {
	t.Helper()
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@t", PasswordHash: "x"}
	if err := e.db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func (e *quotaHandlerEnv) mintJWT(userID string) string {
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

func (e *quotaHandlerEnv) createApiKey(t *testing.T, userID string) string {
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

func (e *quotaHandlerEnv) do(t *testing.T, method, path, authHeader string, body any) (*httptest.ResponseRecorder, apiEnvelope) {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", "Bearer "+authHeader)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var env apiEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, w.Body.String())
	}
	return w, env
}

// 供应商列表：JWT 返回元数据；API Key 40301。
func TestQuotaHandler_ListProviders(t *testing.T) {
	env := setupQuotaHandlerEnv(t, nil)
	user := env.createUser(t)

	w, envResp := env.do(t, http.MethodGet, "/api/quota/providers", env.mintJWT(user.ID), nil)
	if w.Code != 200 {
		t.Fatalf("providers = %d %s", w.Code, w.Body.String())
	}
	var providers []map[string]any
	if err := json.Unmarshal(envResp.Data, &providers); err != nil || len(providers) != 1 {
		t.Fatalf("providers decode: %v", err)
	}
	if providers[0]["id"] != "fake" || providers[0]["name"] != "Fake" {
		t.Fatalf("unexpected provider meta: %+v", providers[0])
	}
	fields := providers[0]["credential_fields"].([]any)
	if len(fields) != 1 || fields[0].(map[string]any)["key"] != "api_key" {
		t.Fatalf("unexpected credential_fields: %+v", fields)
	}

	w, _ = env.do(t, http.MethodGet, "/api/quota/providers", env.createApiKey(t, user.ID), nil)
	if w.Code != 403 {
		t.Fatalf("api key providers = %d, want 403", w.Code)
	}
}

// 创建：成功（含同步抓取）；40030 未知供应商；40031 缺凭据；40001 坏请求体。
func TestQuotaHandler_CreateAccount(t *testing.T) {
	env := setupQuotaHandlerEnv(t, nil)
	user := env.createUser(t)
	jwtTok := env.mintJWT(user.ID)

	w, envResp := env.do(t, http.MethodPost, "/api/quota/accounts", jwtTok, map[string]any{
		"provider": "fake", "label": "我的 Pro", "credentials": map[string]string{"api_key": "fk-secret-ab12"},
	})
	if w.Code != 200 {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var account map[string]any
	if err := json.Unmarshal(envResp.Data, &account); err != nil {
		t.Fatalf("decode account: %v", err)
	}
	if account["status"] != "ok" || account["credential_hint"] != "…ab12" || account["plan_name"] != "Pro" {
		t.Fatalf("unexpected account: %+v", account)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("fk-secret-ab12")) {
		t.Fatalf("response leaks plaintext: %s", w.Body.String())
	}

	_, envResp = env.do(t, http.MethodPost, "/api/quota/accounts", jwtTok, map[string]any{
		"provider": "nope", "label": "x", "credentials": map[string]string{"api_key": "k"},
	})
	if envResp.Code != 40030 {
		t.Fatalf("unknown provider code = %d, want 40030", envResp.Code)
	}
	_, envResp = env.do(t, http.MethodPost, "/api/quota/accounts", jwtTok, map[string]any{
		"provider": "fake", "label": "x", "credentials": map[string]string{},
	})
	if envResp.Code != 40031 {
		t.Fatalf("missing creds code = %d, want 40031", envResp.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/quota/accounts", bytes.NewReader([]byte("{bad")))
	req.Header.Set("Authorization", "Bearer "+jwtTok)
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("bad json = %d, want 400", w.Code)
	}
}

// API Key 只读：GET /accounts 放行且不含凭据；其余端点一律 40301。
func TestQuotaHandler_ApiKeyReadOnly(t *testing.T) {
	env := setupQuotaHandlerEnv(t, nil)
	user := env.createUser(t)
	jwtTok := env.mintJWT(user.ID)
	apiKey := env.createApiKey(t, user.ID)

	w, envResp := env.do(t, http.MethodPost, "/api/quota/accounts", jwtTok, map[string]any{
		"provider": "fake", "label": "x", "credentials": map[string]string{"api_key": "fk-1234"},
	})
	if w.Code != 200 {
		t.Fatalf("create = %d", w.Code)
	}
	var account map[string]any
	_ = json.Unmarshal(envResp.Data, &account)
	accountID := account["id"].(string)

	w, envResp = env.do(t, http.MethodGet, "/api/quota/accounts", apiKey, nil)
	if w.Code != 200 {
		t.Fatalf("api key list = %d %s", w.Code, w.Body.String())
	}
	var accounts []map[string]any
	if err := json.Unmarshal(envResp.Data, &accounts); err != nil || len(accounts) != 1 {
		t.Fatalf("api key list decode: %+v err=%v", envResp.Data, err)
	}
	if bytes.Contains(w.Body.Bytes(), []byte("fk-1234")) {
		t.Fatalf("api key response leaks credentials: %s", w.Body.String())
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/quota/providers"},
		{http.MethodPost, "/api/quota/accounts"},
		{http.MethodPut, "/api/quota/accounts/" + accountID},
		{http.MethodDelete, "/api/quota/accounts/" + accountID},
		{http.MethodPost, "/api/quota/accounts/" + accountID + "/refresh"},
		{http.MethodPost, "/api/quota/test"},
	} {
		var body any
		if tc.method == http.MethodPost && tc.path == "/api/quota/accounts" {
			body = map[string]any{"provider": "fake", "label": "x", "credentials": map[string]string{"api_key": "k"}}
		}
		w, envResp = env.do(t, tc.method, tc.path, apiKey, body)
		if w.Code != 403 || envResp.Code != 40301 {
			t.Fatalf("%s %s with api key = %d code %d, want 403/40301", tc.method, tc.path, w.Code, envResp.Code)
		}
	}
}

// 越权：他人账号改/删/刷新一律 40430；不存在的账号同 40430。
func TestQuotaHandler_ForeignAccount(t *testing.T) {
	env := setupQuotaHandlerEnv(t, nil)
	owner := env.createUser(t)
	stranger := env.createUser(t)
	ownerJWT := env.mintJWT(owner.ID)
	strangerJWT := env.mintJWT(stranger.ID)

	w, envResp := env.do(t, http.MethodPost, "/api/quota/accounts", ownerJWT, map[string]any{
		"provider": "fake", "label": "owner", "credentials": map[string]string{"api_key": "k-1"},
	})
	if w.Code != 200 {
		t.Fatalf("create = %d", w.Code)
	}
	var account map[string]any
	_ = json.Unmarshal(envResp.Data, &account)
	accountID := account["id"].(string)

	// 陌生人列表看不到他人账号。
	_, envResp = env.do(t, http.MethodGet, "/api/quota/accounts", strangerJWT, nil)
	var list []map[string]any
	_ = json.Unmarshal(envResp.Data, &list)
	if len(list) != 0 {
		t.Fatalf("stranger sees foreign accounts: %+v", list)
	}

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPut, "/api/quota/accounts/" + accountID, map[string]any{"label": "hijack"}},
		{http.MethodDelete, "/api/quota/accounts/" + accountID, nil},
		{http.MethodPost, "/api/quota/accounts/" + accountID + "/refresh", nil},
	} {
		w, envResp = env.do(t, tc.method, tc.path, strangerJWT, tc.body)
		if envResp.Code != 40430 {
			t.Fatalf("%s on foreign = %d (http %d), want 40430", tc.method+" "+tc.path, envResp.Code, w.Code)
		}
	}

	_, envResp = env.do(t, http.MethodDelete, "/api/quota/accounts/nonexistent", ownerJWT, nil)
	if envResp.Code != 40430 {
		t.Fatalf("delete nonexistent code = %d, want 40430", envResp.Code)
	}
}

// Update：改标签不触发抓取；替换凭据重抓并换尾号。
func TestQuotaHandler_UpdateAccount(t *testing.T) {
	env := setupQuotaHandlerEnv(t, nil)
	user := env.createUser(t)
	jwtTok := env.mintJWT(user.ID)

	w, envResp := env.do(t, http.MethodPost, "/api/quota/accounts", jwtTok, map[string]any{
		"provider": "fake", "label": "old", "credentials": map[string]string{"api_key": "key-v1-a1"},
	})
	if w.Code != 200 {
		t.Fatalf("create = %d", w.Code)
	}
	var account map[string]any
	_ = json.Unmarshal(envResp.Data, &account)
	accountID := account["id"].(string)

	w, envResp = env.do(t, http.MethodPut, "/api/quota/accounts/"+accountID, jwtTok, map[string]any{"label": "renamed"})
	if w.Code != 200 {
		t.Fatalf("rename = %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(envResp.Data, &account)
	if account["label"] != "renamed" {
		t.Fatalf("label not updated: %+v", account)
	}

	w, envResp = env.do(t, http.MethodPut, "/api/quota/accounts/"+accountID, jwtTok, map[string]any{
		"credentials": map[string]string{"api_key": "key-v2-zz99"},
	})
	if w.Code != 200 {
		t.Fatalf("update creds = %d %s", w.Code, w.Body.String())
	}
	_ = json.Unmarshal(envResp.Data, &account)
	if account["credential_hint"] != "…zz99" {
		t.Fatalf("hint not updated: %+v", account)
	}
}

// Refresh：成功返回账号；冷却内 42900 + HTTP 429。
func TestQuotaHandler_RefreshAccount(t *testing.T) {
	env := setupQuotaHandlerEnv(t, nil)
	user := env.createUser(t)
	jwtTok := env.mintJWT(user.ID)

	w, envResp := env.do(t, http.MethodPost, "/api/quota/accounts", jwtTok, map[string]any{
		"provider": "fake", "label": "x", "credentials": map[string]string{"api_key": "k-1"},
	})
	if w.Code != 200 {
		t.Fatalf("create = %d", w.Code)
	}
	var account map[string]any
	_ = json.Unmarshal(envResp.Data, &account)
	accountID := account["id"].(string)

	// 创建时刚抓过 → 立即刷新命中冷却 42900（HTTP 429）。
	w, envResp = env.do(t, http.MethodPost, "/api/quota/accounts/"+accountID+"/refresh", jwtTok, nil)
	if w.Code != 429 || envResp.Code != 42900 {
		t.Fatalf("refresh cooldown = %d code %d, want 429/42900", w.Code, envResp.Code)
	}
}

// Test：探测成功与失败都不落库；供应商未知 40030。
func TestQuotaHandler_Test(t *testing.T) {
	fail := false
	env := setupQuotaHandlerEnv(t, func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		if fail {
			return quota.Snapshot{}, quota.ProviderError("接口 500", 500)
		}
		return quota.Snapshot{PlanName: "Pro", FetchedAt: time.Now().UTC()}, nil
	})
	user := env.createUser(t)
	jwtTok := env.mintJWT(user.ID)

	w, envResp := env.do(t, http.MethodPost, "/api/quota/test", jwtTok, map[string]any{
		"provider": "fake", "credentials": map[string]string{"api_key": "k"},
	})
	if w.Code != 200 {
		t.Fatalf("test = %d %s", w.Code, w.Body.String())
	}
	var probe map[string]any
	_ = json.Unmarshal(envResp.Data, &probe)
	if probe["status"] != "ok" || probe["plan_name"] != "Pro" {
		t.Fatalf("unexpected probe: %+v", probe)
	}

	fail = true
	w, envResp = env.do(t, http.MethodPost, "/api/quota/test", jwtTok, map[string]any{
		"provider": "fake", "credentials": map[string]string{"api_key": "k"},
	})
	if w.Code != 200 {
		t.Fatalf("failed probe must still be 200, got %d", w.Code)
	}
	_ = json.Unmarshal(envResp.Data, &probe)
	if probe["status"] != "provider_error" || probe["error"] != "接口 500" {
		t.Fatalf("unexpected failed probe: %+v", probe)
	}

	_, envResp = env.do(t, http.MethodPost, "/api/quota/test", jwtTok, map[string]any{
		"provider": "nope", "credentials": map[string]string{"api_key": "k"},
	})
	if envResp.Code != 40030 {
		t.Fatalf("test unknown provider code = %d, want 40030", envResp.Code)
	}
}

// B3：label 校验落地——超长被 binding（openapi maxLength → max=100）拦下；
// 纯空白/空串漏过 binding，由 service 兜底 → 40001。两条路径都回 400/40001。
func TestQuotaHandler_LabelValidation(t *testing.T) {
	env := setupQuotaHandlerEnv(t, nil)
	user := env.createUser(t)
	jwtTok := env.mintJWT(user.ID)

	w, envResp := env.do(t, http.MethodPost, "/api/quota/accounts", jwtTok, map[string]any{
		"provider": "fake", "label": "   ", "credentials": map[string]string{"api_key": "k"},
	})
	if w.Code != 400 || envResp.Code != 40001 {
		t.Fatalf("blank label create = %d code %d, want 400/40001", w.Code, envResp.Code)
	}

	w, envResp = env.do(t, http.MethodPost, "/api/quota/accounts", jwtTok, map[string]any{
		"provider": "fake", "label": "mine", "credentials": map[string]string{"api_key": "k"},
	})
	if w.Code != 200 {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	var account map[string]any
	_ = json.Unmarshal(envResp.Data, &account)
	accountID := account["id"].(string)

	w, envResp = env.do(t, http.MethodPut, "/api/quota/accounts/"+accountID, jwtTok, map[string]any{
		"label": strings.Repeat("x", 101),
	})
	if w.Code != 400 || envResp.Code != 40001 {
		t.Fatalf("overlong label = %d code %d, want 400/40001 (binding max=100)", w.Code, envResp.Code)
	}
	for _, label := range []string{"", "   "} {
		w, envResp = env.do(t, http.MethodPut, "/api/quota/accounts/"+accountID, jwtTok, map[string]any{"label": label})
		if w.Code != 400 || envResp.Code != 40001 {
			t.Fatalf("label %q = %d code %d, want 400/40001", label, w.Code, envResp.Code)
		}
	}
}

// B4：金额与百分比按 float64 原样往返——降 float32 时 19.987654321 会序列化成
// 19.987654、33.333333 会变成 33.333332（10.1 这类短值两种类型输出相同，不能用）。
func TestQuotaHandler_BalancePrecision(t *testing.T) {
	env := setupQuotaHandlerEnv(t, func(context.Context, *http.Client, string, quota.Credentials) (quota.Snapshot, error) {
		return quota.Snapshot{
			PlanName: "Pro",
			Windows: []quota.Window{
				{Kind: quota.WindowFiveHour, Label: "5 小时", UsedPercent: quota.PercentPtr(33.333333), Unit: quota.UnitPercent},
			},
			Balance:   &quota.Balance{Label: "剩余额度", Amount: 19.987654321, Currency: "USD"},
			FetchedAt: time.Now().UTC(),
		}, nil
	})
	user := env.createUser(t)
	jwtTok := env.mintJWT(user.ID)

	w, _ := env.do(t, http.MethodPost, "/api/quota/accounts", jwtTok, map[string]any{
		"provider": "fake", "label": "x", "credentials": map[string]string{"api_key": "k"},
	})
	if w.Code != 200 {
		t.Fatalf("create = %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"amount":19.987654321`) || !strings.Contains(body, `"used_percent":33.333333`) {
		t.Fatalf("float64 values must round-trip without float32 degradation: %s", body)
	}
}

// 未认证：无 Authorization 一律 401。
func TestQuotaHandler_Unauthenticated(t *testing.T) {
	env := setupQuotaHandlerEnv(t, nil)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/quota/providers"},
		{http.MethodGet, "/api/quota/accounts"},
		{http.MethodPost, "/api/quota/test"},
	} {
		w, _ := env.do(t, tc.method, tc.path, "", nil)
		if w.Code != 401 {
			t.Fatalf("%s %s unauthenticated = %d, want 401", tc.method, tc.path, w.Code)
		}
	}
}

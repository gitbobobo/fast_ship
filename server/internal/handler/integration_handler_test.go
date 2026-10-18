package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 集成端点（INT-119）定向测试：40001/40006 文案、响应投影不含密钥明文、
// 显式 null 等于省略（保留语义）。

type integrationHandlerEnv struct {
	router *gin.Engine
	svc    *service.IntegrationService
	db     *gorm.DB
	userID string
}

func setupIntegrationHandlerEnv(t *testing.T) *integrationHandlerEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Integration{}, &model.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := &config.Config{
		Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"},
		Server:     config.ServerConfig{PublicURL: "https://fs.example.com"},
	}
	svc := service.NewIntegrationService(repository.NewIntegrationRepository(db), cfg, zap.NewNop())
	svc.SetVerify(func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection {
		return githubapp.Detection{Status: githubapp.DetectionOK, Detail: "已连接 GitHub App：fast-ship"}
	})

	h := NewIntegrationHandler(svc)
	userID := "u-" + uuid.NewString()[:8]
	r := gin.New()
	// 测试环境没有 JWT 中间件：注入固定用户模拟 RequireJWT 之后的上下文。
	r.Use(func(c *gin.Context) { c.Set(middleware.ContextKeyUserID, userID) })
	r.GET("/api/integrations/github-app", h.Get)
	r.PUT("/api/integrations/github-app", h.Update)
	r.POST("/api/integrations/github-app/test", h.Test)
	return &integrationHandlerEnv{router: r, svc: svc, db: db, userID: userID}
}

func (e *integrationHandlerEnv) do(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

type integrationEnvelope struct {
	Code    int                      `json:"code"`
	Message string                   `json:"message"`
	Data    api.GitHubAppIntegration `json:"data"`
}

func TestIntegrationHandler_UpdateValidation(t *testing.T) {
	env := setupIntegrationHandlerEnv(t)

	w := env.do(t, http.MethodPut, "/api/integrations/github-app", []byte(`{"app_id":"12ab"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric code = %d", w.Code)
	}
	var env1 integrationEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env1)
	if env1.Code != 40001 || env1.Message != "App ID 必须是数字" {
		t.Fatalf("non-numeric = [%d %s]", env1.Code, env1.Message)
	}

	// 首次配置（无私钥来源）省略私钥。
	w = env.do(t, http.MethodPut, "/api/integrations/github-app", []byte(`{"app_id":"123"}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("first save code = %d", w.Code)
	}
	var env2 integrationEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env2)
	if env2.Code != 40001 || env2.Message != "首次配置必须提供 App 私钥" {
		t.Fatalf("first save = [%d %s]", env2.Code, env2.Message)
	}

	// 私钥不是 RSA PEM。
	body := []byte(`{"app_id":"123","private_key_pem":"garbage"}`)
	w = env.do(t, http.MethodPut, "/api/integrations/github-app", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad pem code = %d", w.Code)
	}
	var env3 integrationEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env3)
	if env3.Code != 40001 || env3.Message != "私钥不是有效的 RSA PEM" {
		t.Fatalf("bad pem = [%d %s]", env3.Code, env3.Message)
	}
}

func TestIntegrationHandler_UpdateNoSecretLeak(t *testing.T) {
	env := setupIntegrationHandlerEnv(t)

	pem, err := githubapp.GenerateAppKeyPEM()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	req := map[string]string{
		"app_id":          "123456",
		"private_key_pem": string(pem),
		"webhook_secret":  "leak-check-secret",
	}
	raw, _ := json.Marshal(req)
	w := env.do(t, http.MethodPut, "/api/integrations/github-app", raw)
	if w.Code != http.StatusOK {
		t.Fatalf("update code = %d body=%s", w.Code, w.Body.String())
	}

	body := w.Body.String()
	if bytes.Contains([]byte(body), []byte("PRIVATE KEY")) || bytes.Contains([]byte(body), []byte("leak-check-secret")) {
		t.Fatal("secret material leaked into response body")
	}

	var out integrationEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	d := out.Data
	if !d.PrivateKeySet || !d.WebhookSecretSet || d.Source != api.GitHubAppIntegrationSourceDatabase {
		t.Fatalf("projection = set(%v,%v) source=%s", d.PrivateKeySet, d.WebhookSecretSet, d.Source)
	}
	if d.Provider != api.GithubApp || d.AppId != "123456" || !d.Configured {
		t.Fatalf("head = %s %s %v", d.Provider, d.AppId, d.Configured)
	}
	if d.WebhookUrl != "https://fs.example.com/api/github/webhooks" {
		t.Fatalf("webhook_url = %s", d.WebhookUrl)
	}
	if d.DetectionStatus != api.GitHubAppIntegrationDetectionStatusOk || d.DetectedAt == nil || d.UpdatedAt == nil {
		t.Fatalf("detection = %s %v %v", d.DetectionStatus, d.DetectedAt, d.UpdatedAt)
	}
}

func TestIntegrationHandler_TestUnconfigured(t *testing.T) {
	env := setupIntegrationHandlerEnv(t)

	w := env.do(t, http.MethodPost, "/api/integrations/github-app/test", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("test code = %d", w.Code)
	}
	var out integrationEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Code != 40006 || out.Message != "尚未配置 GitHub App" {
		t.Fatalf("test = [%d %s]", out.Code, out.Message)
	}
}

func TestIntegrationHandler_GetProjection(t *testing.T) {
	env := setupIntegrationHandlerEnv(t)

	// 未配置：source=none、webhook_url 为空串（PublicURL 有值但没有身份来源也
	// 不给半截 URL——集成页按 configured 渲染）。
	w := env.do(t, http.MethodGet, "/api/integrations/github-app", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get code = %d", w.Code)
	}
	var out integrationEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out.Data.Source != api.GitHubAppIntegrationSourceNone || out.Data.Configured {
		t.Fatalf("none projection = %s %v", out.Data.Source, out.Data.Configured)
	}

	// 保存后 GET：齐全投影。
	pem, _ := githubapp.GenerateAppKeyPEM()
	raw, _ := json.Marshal(map[string]string{"app_id": "42", "private_key_pem": string(pem)})
	if w := env.do(t, http.MethodPut, "/api/integrations/github-app", raw); w.Code != http.StatusOK {
		t.Fatalf("update code = %d", w.Code)
	}
	w = env.do(t, http.MethodGet, "/api/integrations/github-app", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("get after save code = %d", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Data.Source != api.GitHubAppIntegrationSourceDatabase || out.Data.AppId != "42" ||
		out.Data.WebhookUrl != "https://fs.example.com/api/github/webhooks" || out.Data.UpdatedAt == nil {
		t.Fatalf("db projection = %s %s %q %v", out.Data.Source, out.Data.AppId, out.Data.WebhookUrl, out.Data.UpdatedAt)
	}
}

// TestIntegrationHandler_ExplicitNullEqualsOmitted 显式 JSON null 与省略字段
// 等价：保留已存私钥与密钥（首次保存时收编 config 的现值）。
func TestIntegrationHandler_ExplicitNullEqualsOmitted(t *testing.T) {
	env := setupIntegrationHandlerEnv(t)

	pem, _ := githubapp.GenerateAppKeyPEM()
	// 首次保存显式 null：等价省略（本环境无 config 兜底 → 40001 拒绝）。
	w := env.do(t, http.MethodPut, "/api/integrations/github-app",
		[]byte(`{"app_id":"42","private_key_pem":null,"webhook_secret":null}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("first save nulls = %d %s", w.Code, w.Body.String())
	}

	// 正常首次保存后再 PUT null：保留全部。
	raw, _ := json.Marshal(map[string]string{
		"app_id": "42", "private_key_pem": string(pem), "webhook_secret": "sec-keep",
	})
	if w := env.do(t, http.MethodPut, "/api/integrations/github-app", raw); w.Code != http.StatusOK {
		t.Fatalf("first save = %d %s", w.Code, w.Body.String())
	}
	w = env.do(t, http.MethodPut, "/api/integrations/github-app",
		[]byte(`{"app_id":"42","private_key_pem":null,"webhook_secret":null}`))
	if w.Code != http.StatusOK {
		t.Fatalf("null put = %d %s", w.Code, w.Body.String())
	}
	var out integrationEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !out.Data.PrivateKeySet || !out.Data.WebhookSecretSet {
		t.Fatalf("nulls must retain: set(%v,%v)", out.Data.PrivateKeySet, out.Data.WebhookSecretSet)
	}
}

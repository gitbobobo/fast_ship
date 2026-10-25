package handler

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GitHub webhook 端点（INT-93；INT-129 起部署级单密钥）定向测试：
// 503/401/202 语义、部署设置的密钥优先级、投递幂等。

type webhookHandlerEnv struct {
	db     *gorm.DB
	router *gin.Engine
	fake   *githubapp.Fake
	cfg    *config.Config
	// integrationSvc 部署级集成服务：setupWebhookEnv 总是创建，数据库来源的
	// 验签场景把它接成 webhook 的密钥来源。
	integrationSvc *service.IntegrationService
}

func setupWebhookHandlerEnv(t *testing.T, secret string) *webhookHandlerEnv {
	return setupWebhookEnv(t, StaticWebhookSecret(secret))
}

// setupDeploymentWebhookEnv 验签密钥走真实集成服务：部署设置行 > config 兜底
// （INT-129 生产形态）。
func setupDeploymentWebhookEnv(t *testing.T) *webhookHandlerEnv {
	return setupWebhookEnv(t, nil)
}

func setupWebhookEnv(t *testing.T, secrets WebhookSecretSource) *webhookHandlerEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Project{}, &model.Notification{},
		// 项目写路径的集成存在性条件引用 forge_integrations（生产恒有）。
		&model.ForgeIntegration{},
		&model.GitHubWebhookDelivery{},
		&model.ProjectReviewSettings{}, &model.ReviewRun{}, &model.ReviewRunToken{},
		&model.ReviewPullRequest{}, &model.ReviewAsset{}, &model.ReviewDebounce{},
		&model.ReviewSeenSHA{},
		&model.DeploymentSettings{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_user_dedup ON notifications(user_id, dedup_key)")

	cfg := &config.Config{
		JWT:        config.JWTConfig{Secret: "test-secret", ExpireHours: 24},
		Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"},
		Server:     config.ServerConfig{PublicURL: "https://fs.example.com"},
	}
	logger := zap.NewNop()
	fake := githubapp.NewFake()
	sink := service.NewNotificationSink(db)
	review := service.NewReviewService(db, repository.NewProjectRepository(db),
		repository.NewGitHubWebhookDeliveryRepository(db), sink,
		githubapp.StaticClients{Client: fake}, nil, nil, nil, nil, nil,
		repository.NewForgeIntegrationRepository(db), cfg, logger)

	integrationSvc := service.NewIntegrationService(repository.NewDeploymentSettingsRepository(db), cfg, logger)
	integrationSvc.SetVerify(func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection {
		return githubapp.Detection{Status: githubapp.DetectionOK, Detail: "已连接 GitHub App：test"}
	})
	if secrets == nil {
		secrets = integrationSvc
	}

	webhookHandler := NewGitHubWebhookHandler(review, secrets, fake)

	r := gin.New()
	r.POST("/api/github/webhooks", webhookHandler.Handle)
	return &webhookHandlerEnv{db: db, router: r, fake: fake, cfg: cfg, integrationSvc: integrationSvc}
}

// postWebhook 签名并投递 payload。
func (e *webhookHandlerEnv) postWebhook(t *testing.T, delivery, event string, body []byte, signature string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/github/webhooks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// saveDeploymentIntegration 以真实集成服务保存部署级 GitHub App 配置（检测已 stub）。
func (e *webhookHandlerEnv) saveDeploymentIntegration(t *testing.T, appID, secret string) {
	t.Helper()
	pem, err := githubapp.GenerateAppKeyPEM()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if _, err := e.integrationSvc.Update(context.Background(), &api.UpdateGitHubAppDeploymentRequest{
		AppId: appID, PrivateKeyPem: api.Ptr(string(pem)), WebhookSecret: api.Ptr(secret),
	}); err != nil {
		t.Fatalf("save deployment integration: %v", err)
	}
}

// prWebhookPayload 官方 pull_request 事件形状的最小 payload（真实投递形态）。
func prWebhookPayload(action, repo string, number int, sha string) []byte {
	owner, name, _ := strings.Cut(repo, "/")
	return []byte(fmt.Sprintf(`{
		"action": %q, "number": %d,
		"pull_request": {
			"draft": false,
			"html_url": "https://github.com/%s/pull/%d",
			"title": "T",
			"head": {"sha": %q, "ref": "feature", "repo": {"fork": false, "full_name": %q}},
			"base": {"ref": "main", "repo": {"full_name": %q}}
		},
		"repository": {"full_name": %q, "owner": {"login": %q}, "name": %q},
		"installation": {"id": 7}
	}`, action, number, repo, number, sha, repo, repo, repo, owner, name))
}

func (e *webhookHandlerEnv) deliveryCount(t *testing.T, delivery string) int64 {
	t.Helper()
	var n int64
	if err := e.db.Model(&model.GitHubWebhookDelivery{}).Where("delivery_id = ?", delivery).Count(&n).Error; err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	return n
}

// TestWebhookHandler_DeploymentSecret 部署级单密钥验签：数据库行的密钥生效；
// 签名对不上 401；行内密钥为空 503（拒绝接受请求，而不是 401）。
func TestWebhookHandler_DeploymentSecret(t *testing.T) {
	env := setupDeploymentWebhookEnv(t)
	env.saveDeploymentIntegration(t, "111", "sec-a")

	body := []byte(`{"action":"opened"}`)
	// 行内密钥验签通过（GitHub App ID 头对本部署的验签没有意义，带了也不看）。
	w := env.postWebhook(t, "d-u1", "pull_request", body, githubapp.SignSignature("sec-a", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("row secret = %d %s", w.Code, w.Body.String())
	}
	// 错误密钥 401。
	w = env.postWebhook(t, "d-u2", "pull_request", body, githubapp.SignSignature("nope", body))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Webhook 签名无效或缺失") {
		t.Fatalf("wrong secret = %d %s", w.Code, w.Body.String())
	}

	// 换成空密钥：保存后立即生效 → 503（没有可验签的身份）。
	env.saveDeploymentIntegration(t, "111", "")
	w = env.postWebhook(t, "d-u3", "pull_request", body, githubapp.SignSignature("sec-a", body))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("cleared secret = %d %s, want 503", w.Code, w.Body.String())
	}
}

// TestWebhookHandler_ConfigFallbackSecret 部署设置行没有 App 身份时回落 config
// 的 webhook 密钥（INT-93 以来仅配 webhook 的旧部署仍能收投递）。
func TestWebhookHandler_ConfigFallbackSecret(t *testing.T) {
	env := setupDeploymentWebhookEnv(t)
	env.cfg.GitHubApp.WebhookSecret = "legacy-secret"

	body := prWebhookPayload("opened", "octocat/hw", 1, "sha-1")
	w := env.postWebhook(t, "d-l1", "pull_request", body, githubapp.SignSignature("legacy-secret", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("legacy secret = %d %s", w.Code, w.Body.String())
	}
	// 密钥不对仍然 401。
	w = env.postWebhook(t, "d-l2", "pull_request", body, githubapp.SignSignature("other", body))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("legacy wrong secret = %d, want 401", w.Code)
	}
	// config 密钥清空后：503，不是 401——没有可验签的身份，拒绝接受请求。
	env.cfg.GitHubApp.WebhookSecret = ""
	w = env.postWebhook(t, "d-l3", "pull_request", body, githubapp.SignSignature("legacy-secret", body))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("cleared secret = %d %s, want 503", w.Code, w.Body.String())
	}
}

// TestWebhookHandler_CorruptedCipherRejected 部署行密文损坏：webhook 原样映射
// 50000（不静默回落 config，否则投递会被无声吞掉）。
func TestWebhookHandler_CorruptedCipherRejected(t *testing.T) {
	env := setupDeploymentWebhookEnv(t)
	env.saveDeploymentIntegration(t, "111", "sec-a")
	env.cfg.GitHubApp.WebhookSecret = "cfg-secret"

	if err := env.db.Model(&model.DeploymentSettings{}).
		Where("app_id = ?", "111").
		Update("config_encrypted", []byte("corrupted-ciphertext")).Error; err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	body := []byte(`{"action":"opened"}`)
	w := env.postWebhook(t, "d-f1", "pull_request", body, githubapp.SignSignature("sec-a", body))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "50000") {
		t.Fatalf("corrupt row = %d %s, want 500/50000", w.Code, w.Body.String())
	}
}

// TestWebhookHandler_StaticSecretTrimmed 静态密钥来源（config/fake 路径）已
// trim：trim 后的密钥能验签，原串不能。
func TestWebhookHandler_StaticSecretTrimmed(t *testing.T) {
	secret, err := StaticWebhookSecret("  whsec  ").WebhookSecret(context.Background())
	if err != nil || secret != "whsec" {
		t.Fatalf("static secret = %q %v, want trimmed whsec", secret, err)
	}

	env := setupWebhookHandlerEnv(t, "  whsec  ")
	body := []byte(`{"action":"opened"}`)
	w := env.postWebhook(t, "d-t1", "pull_request", body, githubapp.SignSignature("whsec", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("trimmed secret = %d %s", w.Code, w.Body.String())
	}
	w = env.postWebhook(t, "d-t2", "pull_request", body, githubapp.SignSignature("  whsec  ", body))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("untrimmed secret = %d, want 401", w.Code)
	}
}

// TestWebhookHandler_SignatureEnforcement 503（secret 空）与 401（签名错）。
func TestWebhookHandler_SignatureEnforcement(t *testing.T) {
	noSecret := setupWebhookHandlerEnv(t, "")
	body := []byte(`{"action":"opened"}`)
	w := noSecret.postWebhook(t, "d-1", "pull_request", body, "sha256=anything")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "50300") {
		t.Fatalf("empty secret = %d %s, want 503/50300", w.Code, w.Body.String())
	}

	env := setupWebhookHandlerEnv(t, "whsec")
	// 错误签名 401（40107）。
	w = env.postWebhook(t, "d-2", "pull_request", body, "sha256=deadbeef")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "40107") {
		t.Fatalf("bad signature = %d %s, want 401/40107", w.Code, w.Body.String())
	}
	// 缺头 401。
	w = env.postWebhook(t, "d-3", "pull_request", body, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature code = %d, want 401", w.Code)
	}
	// 正确签名 202。
	w = env.postWebhook(t, "d-4", "pull_request", body, githubapp.SignSignature("whsec", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("good signature code = %d, want 202", w.Code)
	}
}

// TestWebhookHandler_DeliveryIdempotent 同一 delivery_id 重复投递：两次都 202，
// 但只记一行，不重复处理。
func TestWebhookHandler_DeliveryIdempotent(t *testing.T) {
	env := setupWebhookHandlerEnv(t, "whsec")
	body := prWebhookPayload("opened", "octocat/hw", 1, "s1")

	w := env.postWebhook(t, "d-idem", "pull_request", body, githubapp.SignSignature("whsec", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if n := env.deliveryCount(t, "d-idem"); n != 1 {
		t.Fatalf("deliveries = %d, want 1", n)
	}

	w = env.postWebhook(t, "d-idem", "pull_request", body, githubapp.SignSignature("whsec", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("dup code = %d, want 202", w.Code)
	}
	if n := env.deliveryCount(t, "d-idem"); n != 1 {
		t.Fatalf("deliveries after dup = %d, want 1", n)
	}
}

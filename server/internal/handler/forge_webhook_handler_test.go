package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// Forgejo webhook 端点（INT-167 任务 3）定向测试：404/503/401/202 语义、
// 纯 hex 无前缀验签（带 sha256= 前缀一律拒收）、验签失败零写入、投递幂等、
// synchronized 触发开轮前置。

type forgeWebhookEnv struct {
	db     *gorm.DB
	router *gin.Engine
	cfg    *config.Config
}

func setupForgeWebhookEnv(t *testing.T) *forgeWebhookEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Project{}, &model.Notification{},
		&model.ForgeIntegration{},
		&model.GitHubWebhookDelivery{},
		&model.ProjectReviewSettings{}, &model.ReviewRun{}, &model.ReviewRunToken{},
		&model.ReviewPullRequest{}, &model.ReviewAsset{}, &model.ReviewDebounce{},
		&model.ReviewSeenSHA{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := &config.Config{
		JWT:        config.JWTConfig{Secret: "test-secret", ExpireHours: 24},
		Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"},
		Server:     config.ServerConfig{PublicURL: "https://fs.example.com"},
	}
	logger := zap.NewNop()
	review := service.NewReviewService(db, repository.NewProjectRepository(db),
		repository.NewGitHubWebhookDeliveryRepository(db), service.NewNotificationSink(db),
		githubapp.StaticClients{Client: githubapp.NewFake()}, nil, nil, nil, nil, nil,
		repository.NewForgeIntegrationRepository(db), cfg, logger)
	forgeSource := service.NewForgeClientSource(repository.NewForgeIntegrationRepository(db), cfg, logger)
	forgeHandler := NewForgeWebhookHandler(review, forgeSource)

	r := gin.New()
	r.POST("/api/forge/:id/webhooks", forgeHandler.Handle)
	return &forgeWebhookEnv{db: db, router: r, cfg: cfg}
}

// seedForgeIntegration 落一条 forgejo 集成行：webhook secret 按 cfg 密钥加密
// （secret 为空串 = 行存在但未配置）。
func (e *forgeWebhookEnv) seedForgeIntegration(t *testing.T, secret string) *model.ForgeIntegration {
	t.Helper()
	row := &model.ForgeIntegration{
		ID:         uuid.NewString(),
		Kind:       model.ForgeIntegrationKindForgejo,
		Name:       "forge",
		BaseURL:    "https://forge.example.com",
		APIBaseURL: "https://forge.example.com/api/v1",
		HostKey:    "forge.example.com:443",
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if secret != "" {
		enc, err := crypto.Encrypt([]byte(secret), []byte(e.cfg.Encryption.Key))
		if err != nil {
			t.Fatalf("encrypt secret: %v", err)
		}
		row.WebhookSecretEncrypted = enc
	}
	if err := e.db.Create(row).Error; err != nil {
		t.Fatalf("seed integration: %v", err)
	}
	return row
}

// postForgeWebhook 投递 payload；signature 为空串 = 不带签名头。
func (e *forgeWebhookEnv) postForgeWebhook(t *testing.T, integrationID, delivery, event string, body []byte, signature string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/forge/"+integrationID+"/webhooks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forgejo-Event", event)
	req.Header.Set("X-Forgejo-Delivery", delivery)
	if signature != "" {
		req.Header.Set("X-Forgejo-Signature", signature)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

func (e *forgeWebhookEnv) forgeDeliveryCount(t *testing.T, delivery string) int64 {
	t.Helper()
	var n int64
	if err := e.db.Model(&model.GitHubWebhookDelivery{}).Where("delivery_id = ?", delivery).Count(&n).Error; err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	return n
}

func forgejoSyncPayload() []byte {
	return []byte(`{
		"action": "synchronized", "number": 4,
		"pull_request": {
			"draft": false,
			"html_url": "https://forge.example.com/acme/app/pulls/4",
			"title": "T",
			"head": {"sha": "sha-fw-1", "ref": "feat", "repo": {"fork": false, "full_name": "acme/app"}},
			"base": {"ref": "main", "repo": {"full_name": "acme/app"}}
		},
		"repository": {"full_name": "acme/app"}
	}`)
}

// TestForgeWebhookHandler_SignatureMatrix 验签矩阵：合法 hex → 202 且落账；
// 错误密钥/缺头/带 sha256= 前缀 → 401 且零写入；未知集成 404；行未配密钥 503。
func TestForgeWebhookHandler_SignatureMatrix(t *testing.T) {
	env := setupForgeWebhookEnv(t)
	row := env.seedForgeIntegration(t, "whsec-forge")
	body := []byte(`{"action":"opened"}`)

	// 合法签名（纯 hex，大小写不敏感）→ 202 + delivery 落账。
	sig := forge.SignForgejoSignature("whsec-forge", body)
	w := env.postForgeWebhook(t, row.ID, "fw-1", "pull_request", body, strings.ToUpper(sig))
	if w.Code != http.StatusAccepted {
		t.Fatalf("valid = %d %s", w.Code, w.Body.String())
	}
	if n := env.forgeDeliveryCount(t, "fw-1"); n != 1 {
		t.Fatalf("deliveries = %d, want 1", n)
	}

	// 错误密钥 → 401/40107，零写入。
	w = env.postForgeWebhook(t, row.ID, "fw-2", "pull_request", body, forge.SignForgejoSignature("nope", body))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "40107") {
		t.Fatalf("wrong secret = %d %s", w.Code, w.Body.String())
	}
	// 缺签名头 → 401。
	w = env.postForgeWebhook(t, row.ID, "fw-3", "pull_request", body, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("missing signature = %d", w.Code)
	}
	// GitHub 形态的 sha256= 前缀 → 一律无效（Forgejo 只发纯 hex）。
	prefixed := "sha256=" + forge.SignForgejoSignature("whsec-forge", body)
	w = env.postForgeWebhook(t, row.ID, "fw-4", "pull_request", body, prefixed)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("prefixed signature = %d, want 401", w.Code)
	}
	// 验签失败路径零写入（含 delivery 幂等账）。
	var n int64
	env.db.Model(&model.GitHubWebhookDelivery{}).Where("delivery_id IN ?", []string{"fw-2", "fw-3", "fw-4"}).Count(&n)
	if n != 0 {
		t.Fatalf("rejected writes = %d, want 0", n)
	}

	// 未知集成 → 404。
	w = env.postForgeWebhook(t, "no-such-integration", "fw-5", "pull_request", body, sig)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown integration = %d, want 404", w.Code)
	}
	// 行存在但密钥未配置 → 503（拒收，没有可验签的身份）。
	bare := env.seedForgeIntegration(t, "")
	w = env.postForgeWebhook(t, bare.ID, "fw-6", "pull_request", body, sig)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "50300") {
		t.Fatalf("no secret = %d %s, want 503/50300", w.Code, w.Body.String())
	}
	// 拒收路径同样零写入。
	if n := env.forgeDeliveryCount(t, "fw-6"); n != 0 {
		t.Fatalf("503 writes = %d, want 0", n)
	}
}

// TestForgeWebhookHandler_GiteaHeaderFallback 旧 Gitea（<1.19）只发 X-Gitea-*
// 三个头：缺省回退受理，与 Forgejo 头同权（投递幂等账也记上）。
func TestForgeWebhookHandler_GiteaHeaderFallback(t *testing.T) {
	env := setupForgeWebhookEnv(t)
	row := env.seedForgeIntegration(t, "whsec-forge")
	body := []byte(`{"action":"opened"}`)

	req := httptest.NewRequest(http.MethodPost, "/api/forge/"+row.ID+"/webhooks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "pull_request")
	req.Header.Set("X-Gitea-Delivery", "fw-gitea")
	req.Header.Set("X-Gitea-Signature", forge.SignForgejoSignature("whsec-forge", body))
	w := httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("gitea headers = %d %s, want 202", w.Code, w.Body.String())
	}
	if n := env.forgeDeliveryCount(t, "fw-gitea"); n != 1 {
		t.Fatalf("deliveries = %d, want 1", n)
	}

	// 两套头并存时 Forgejo 头优先（此处验证 Gitea 头的错误签名不抢跑——
	// Forgejo 签名合法仍受理）。
	req = httptest.NewRequest(http.MethodPost, "/api/forge/"+row.ID+"/webhooks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forgejo-Event", "pull_request")
	req.Header.Set("X-Forgejo-Delivery", "fw-both")
	req.Header.Set("X-Forgejo-Signature", forge.SignForgejoSignature("whsec-forge", body))
	req.Header.Set("X-Gitea-Signature", "deadbeef")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("forgejo precedence = %d, want 202", w.Code)
	}
	if n := env.forgeDeliveryCount(t, "fw-both"); n != 1 {
		t.Fatalf("deliveries = %d, want 1", n)
	}
}

// TestForgeWebhookHandler_CorruptedCipherRejected 集成行密文损坏 → 500/50000
// 原样映射（不静默放行也不吞投递）。
func TestForgeWebhookHandler_CorruptedCipherRejected(t *testing.T) {
	env := setupForgeWebhookEnv(t)
	row := env.seedForgeIntegration(t, "whsec-forge")
	if err := env.db.Model(&model.ForgeIntegration{}).Where("id = ?", row.ID).
		Update("webhook_secret_encrypted", []byte("corrupted-ciphertext")).Error; err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	body := []byte(`{"action":"opened"}`)
	w := env.postForgeWebhook(t, row.ID, "fw-f1", "pull_request", body, forge.SignForgejoSignature("whsec-forge", body))
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "50000") {
		t.Fatalf("corrupt row = %d %s, want 500/50000", w.Code, w.Body.String())
	}
}

// TestForgeWebhookHandler_DeliveryIdempotent 同一 delivery 重放：两次都 202，
// 只记一行。
func TestForgeWebhookHandler_DeliveryIdempotent(t *testing.T) {
	env := setupForgeWebhookEnv(t)
	row := env.seedForgeIntegration(t, "whsec-forge")
	body := forgejoSyncPayload()

	w := env.postForgeWebhook(t, row.ID, "fw-idem", "pull_request", body, forge.SignForgejoSignature("whsec-forge", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("first = %d %s", w.Code, w.Body.String())
	}
	w = env.postForgeWebhook(t, row.ID, "fw-idem", "pull_request", body, forge.SignForgejoSignature("whsec-forge", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("replay = %d", w.Code)
	}
	if n := env.forgeDeliveryCount(t, "fw-idem"); n != 1 {
		t.Fatalf("deliveries = %d, want 1", n)
	}
}

// TestForgeWebhookHandler_SynchronizedAcceptsPR 合法 synchronized 投递经完整
// 链路：delivery + seen + 防抖（动作归一 synchronize）落账，键按集成 id 分区。
func TestForgeWebhookHandler_SynchronizedAcceptsPR(t *testing.T) {
	env := setupForgeWebhookEnv(t)
	row := env.seedForgeIntegration(t, "whsec-forge")

	user := &model.User{Username: "fw-user", Email: "fw@example.com", PasswordHash: "x"}
	if err := env.db.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	project := &model.Project{UserID: user.ID, Name: "app", GithubOwner: "acme", GithubRepo: "app", CodeIntegrationID: row.ID}
	if err := env.db.Create(project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	now := time.Now().UTC()
	if err := env.db.Create(&model.ProjectReviewSettings{
		ProjectID: project.ID, UserID: user.ID,
		RepoFullName: "acme/app", IntegrationID: row.ID, Enabled: true,
		TriggerOpened: true, TriggerSynchronize: true,
		CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	body := forgejoSyncPayload()
	w := env.postForgeWebhook(t, row.ID, "fw-sync", "pull_request", body, forge.SignForgejoSignature("whsec-forge", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("accepted = %d %s", w.Code, w.Body.String())
	}
	var debounce model.ReviewDebounce
	if err := env.db.Where("project_id = ? AND integration_id = ?", project.ID, row.ID).
		First(&debounce).Error; err != nil {
		t.Fatalf("debounce: %v", err)
	}
	if debounce.HeadSHA != "sha-fw-1" || debounce.Action != "synchronize" {
		t.Fatalf("debounce = %s/%s, want sha-fw-1/synchronize", debounce.HeadSHA, debounce.Action)
	}
	var seen int64
	env.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND head_sha = ?", project.ID, "sha-fw-1").Count(&seen)
	if seen != 1 {
		t.Fatalf("seen rows = %d, want 1", seen)
	}
}

// TestForgeWebhookHandler_PullRequestSyncEvent 独立订阅项 pull_request_sync
// 与 pull_request 同形受理（payload 共享、action=synchronized 归一）——旧
// Gitea/自定义投递会把细型放进 X-Forgejo-Event 头，受理面两个名字都认。
func TestForgeWebhookHandler_PullRequestSyncEvent(t *testing.T) {
	env := setupForgeWebhookEnv(t)
	row := env.seedForgeIntegration(t, "whsec-forge")

	user := &model.User{Username: "fw-u2", Email: "fw2@example.com", PasswordHash: "x"}
	if err := env.db.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	project := &model.Project{UserID: user.ID, Name: "app", GithubOwner: "acme", GithubRepo: "app", CodeIntegrationID: row.ID}
	if err := env.db.Create(project).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	now := time.Now().UTC()
	if err := env.db.Create(&model.ProjectReviewSettings{
		ProjectID: project.ID, UserID: user.ID,
		RepoFullName: "acme/app", IntegrationID: row.ID, Enabled: true,
		TriggerOpened: true, TriggerSynchronize: true,
		CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	body := forgejoSyncPayload()
	w := env.postForgeWebhook(t, row.ID, "fw-prs", "pull_request_sync", body, forge.SignForgejoSignature("whsec-forge", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("pull_request_sync 投递未受理 = %d %s", w.Code, w.Body.String())
	}
	var debounce model.ReviewDebounce
	if err := env.db.Where("project_id = ? AND integration_id = ?", project.ID, row.ID).
		First(&debounce).Error; err != nil {
		t.Fatalf("debounce: %v", err)
	}
	if debounce.Action != "synchronize" {
		t.Fatalf("action = %s, want synchronize", debounce.Action)
	}
}

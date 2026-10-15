package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GitHub webhook 与 PR 代发端点（INT-93）定向测试：503/401/202 语义、幂等、
// 运行令牌边界（结束后 40106、fsk_ 40304、非 PR 运行 40920）。

type webhookHandlerEnv struct {
	db     *gorm.DB
	router *gin.Engine
	fake   *githubapp.Fake
	agent  *service.AutomationAgentService
	runs   *repository.AutomationRunRepository
	tokens *repository.AutomationRunTokenRepository
	cfg    *config.Config
}

func setupWebhookHandlerEnv(t *testing.T, secret string) *webhookHandlerEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Project{}, &model.ApiKey{}, &model.T3Environment{},
		&model.JWTBlacklist{}, &model.RefreshToken{},
		&model.Automation{}, &model.AutomationRun{}, &model.AutomationRunToken{}, &model.Notification{},
		&model.GitHubWebhookDelivery{}, &model.GitHubPRDebounce{}, &model.GitHubPRReview{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_automation_trigger ON automation_runs(automation_id, trigger_key)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_user_dedup ON notifications(user_id, dedup_key)")

	cfg := &config.Config{
		JWT:        config.JWTConfig{Secret: "test-secret", ExpireHours: 24},
		Encryption: config.EncryptionConfig{Key: "12345678901234567890123456789012"},
		Server:     config.ServerConfig{PublicURL: "https://fs.example.com"},
	}
	logger := zap.NewNop()
	fake := githubapp.NewFake()
	autos := repository.NewAutomationRepository(db)
	runs := repository.NewAutomationRunRepository(db)
	tokens := repository.NewAutomationRunTokenRepository(db)
	sink := service.NewNotificationSink(db)
	prReview := service.NewPRReviewService(db, autos, runs, repository.NewProjectRepository(db),
		repository.NewGitHubWebhookDeliveryRepository(db),
		repository.NewGitHubPRDebounceRepository(db),
		repository.NewGitHubPRReviewRepository(db),
		fake, nil, sink, cfg, logger)
	agent := service.NewAutomationAgentService(db, runs, tokens, nil, sink, logger)
	agent.SetPRGitHub(fake)

	webhookHandler := NewGitHubWebhookHandler(prReview, secret, fake)
	agentHandler := NewAutomationAgentHandler(agent)

	r := gin.New()
	r.POST("/api/github/webhooks", webhookHandler.Handle)
	r.GET("/api/automation-agent/pull-request", middleware.RequireRunToken(agent, true), agentHandler.GetPullRequest)
	r.GET("/api/automation-agent/pull-request/diff", middleware.RequireRunToken(agent, true), agentHandler.GetPullRequestDiff)
	r.POST("/api/automation-agent/pull-request/review", middleware.RequireRunToken(agent, true), agentHandler.SubmitPullRequestReview)
	r.POST("/api/automation-agent/pull-request/check", middleware.RequireRunToken(agent, true), agentHandler.UpdatePullRequestCheck)
	return &webhookHandlerEnv{db: db, router: r, fake: fake, agent: agent, runs: runs, tokens: tokens, cfg: cfg}
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

// TestWebhookHandler_SignatureEnforcement 503（secret 空）与 401（签名错）。
func TestWebhookHandler_SignatureEnforcement(t *testing.T) {
	noSecret := setupWebhookHandlerEnv(t, "")
	body := []byte(`{"action":"opened"}`)
	w := noSecret.postWebhook(t, "d-1", "pull_request", body, "sha256=anything")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty secret code = %d, want 503", w.Code)
	}

	env := setupWebhookHandlerEnv(t, "whsec")
	// 错误签名 401。
	w = env.postWebhook(t, "d-2", "pull_request", body, "sha256=deadbeef")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature code = %d, want 401", w.Code)
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

// TestWebhookHandler_OpenRoundEndToEnd debounce=0 的规则在请求事务里开轮。
func TestWebhookHandler_OpenRoundEndToEnd(t *testing.T) {
	env := setupWebhookHandlerEnv(t, "whsec")

	// 用户 + 关联项目 + 规则（debounce=0，双模型）。
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@t", PasswordHash: "x"}
	if err := env.db.Create(user).Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	project := &model.Project{ID: uuid.NewString(), UserID: user.ID, Name: "p", GithubOwner: "octocat", GithubRepo: "hw", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := env.db.Create(project).Error; err != nil {
		t.Fatalf("project: %v", err)
	}
	now := time.Now().UTC()
	envRow := &model.T3Environment{ID: uuid.NewString(), UserID: user.ID, Name: "env", Address: "https://t3.example.com", BearerTokenEncrypted: []byte("x"), BearerExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
	if err := env.db.Create(envRow).Error; err != nil {
		t.Fatalf("env: %v", err)
	}
	selsJSON, _ := json.Marshal([]map[string]string{
		{"instance_id": "inst-a", "model": "claude"},
		{"instance_id": "inst-b", "model": "glm"},
	})
	rule := &model.Automation{
		ID: uuid.NewString(), UserID: user.ID, Name: "PR 评审", Enabled: true,
		CreatedAt: now, UpdatedAt: now, EnvironmentID: envRow.ID, T3ProjectID: "t3-proj",
		TriggerType:         model.AutomationTriggerGitHubPROpened,
		PromptTemplate:      "评审 {{repo}} #{{pr_number}} @ {{head_sha}} {{pr_title}} {{pr_url}}",
		ModelSelectionsJSON: string(selsJSON),
		DebounceSeconds:     0,
		RuntimeMode:         "full-access", TimeoutSeconds: 3600,
	}
	if err := env.db.Create(rule).Error; err != nil {
		t.Fatalf("rule: %v", err)
	}

	payload := []byte(`{"action":"opened","number":1,"pull_request":{"draft":false,"title":"T","head":{"sha":"s1","ref":"f","repo":{"fork":false,"full_name":"octocat/hw"}},"base":{"ref":"main","repo":{"full_name":"octocat/hw"}}},"repository":{"full_name":"octocat/hw","owner":{"login":"octocat"},"name":"hw"},"installation":{"id":7}}`)
	w := env.postWebhook(t, "d-e2e", "pull_request", payload, githubapp.SignSignature("whsec", payload))
	if w.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202; body=%s", w.Code, w.Body.String())
	}

	var runs []model.AutomationRun
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").Find(&runs).Error; err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	// 幂等：同 delivery 再投不重复开轮。
	w = env.postWebhook(t, "d-e2e", "pull_request", payload, githubapp.SignSignature("whsec", payload))
	if w.Code != http.StatusAccepted {
		t.Fatalf("dup code = %d, want 202", w.Code)
	}
	var n int64
	env.db.Model(&model.AutomationRun{}).Where("repo_full_name = ?", "octocat/hw").Count(&n)
	if n != 2 {
		t.Fatalf("runs after dup = %d, want 2", n)
	}

	pr, err := env.fake.GetPullRequest(context.Background(), 7, "octocat/hw", 1)
	if err != nil {
		t.Fatalf("fake PR not seeded from webhook: %v", err)
	}
	if pr.HeadSHA != "s1" || pr.Title != "T" {
		t.Fatalf("fake PR = %+v, want title=T sha=s1", pr)
	}
}

// seedPRRunWithToken 造一个进行中的 PR 运行并存明文令牌（返回令牌）。
func (e *webhookHandlerEnv) seedPRRunWithToken(t *testing.T, userID string, triggerType model.AutomationTriggerType, status model.AutomationRunStatus) (string, *model.AutomationRun) {
	t.Helper()
	now := time.Now().UTC()
	// 运行行有 automation_id 外键，规则又有 environment 外键：逐一落真行。
	envRow := &model.T3Environment{
		ID: uuid.NewString(), UserID: userID, Name: "env-" + uuid.NewString()[:8],
		Address: "https://t3.example.com", BearerTokenEncrypted: []byte("x"),
		BearerExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := e.db.Create(envRow).Error; err != nil {
		t.Fatalf("seed env: %v", err)
	}
	hostRule := &model.Automation{
		ID: uuid.NewString(), UserID: userID, Name: "host", Enabled: true,
		CreatedAt: now, UpdatedAt: now, EnvironmentID: envRow.ID, T3ProjectID: "p",
		TriggerType: triggerType, PromptTemplate: "x", RuntimeMode: "full-access", TimeoutSeconds: 3600,
	}
	if err := e.db.Create(hostRule).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	run := &model.AutomationRun{
		ID: uuid.NewString(), UserID: userID, AutomationID: hostRule.ID,
		TriggerType: triggerType, TriggerKey: "pr:octocat/hw:1:s1:inst-a:claude",
		TriggerContextJSON: "{}", TriggeredAt: now,
		ConfigSnapshotJSON: "{}",
		EnvironmentID:      envRow.ID, T3ProjectID: "p", ThreadID: uuid.NewString(),
		LaunchCommandID: uuid.NewString(), InitialMessageID: uuid.NewString(),
		Status: status, DeadlineAt: now.Add(time.Hour),
		RepoFullName: "octocat/hw", PRNumber: 1, HeadSHA: "s1", RoundNumber: 1,
		InstallationID: 7, ModelInstanceID: "inst-a", ModelName: "claude",
	}
	if err := e.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	token := "fsr_" + uuid.NewString()
	sum := sha256.Sum256([]byte(token))
	if err := e.db.Create(&model.AutomationRunToken{
		RunID: run.ID, TokenHash: hex.EncodeToString(sum[:]),
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	return token, run
}

func (e *webhookHandlerEnv) getWithToken(path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// TestAgentPR_TokenLifecycle 进行中的 PR 运行可读元数据/diff；结束后令牌 40106；
// fsk_ API Key 40304；非 PR 运行 40920。
func TestAgentPR_TokenLifecycle(t *testing.T) {
	env := setupWebhookHandlerEnv(t, "whsec")
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@t", PasswordHash: "x"}
	if err := env.db.Create(user).Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	env.fake.SetPR("octocat/hw", 1, &githubapp.PullRequest{Title: "T", HTMLURL: "https://github.com/octocat/hw/pull/1", HeadSHA: "s1"})

	token, run := env.seedPRRunWithToken(t, user.ID, model.AutomationTriggerGitHubPROpened, model.AutomationRunDispatched)

	// 进行中：读取 OK。
	w := env.getWithToken("/api/automation-agent/pull-request", token)
	if w.Code != http.StatusOK {
		t.Fatalf("get PR code = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var detail struct {
		Data struct {
			RepoFullName string `json:"repo_full_name"`
			HeadSha      string `json:"head_sha"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || detail.Data.RepoFullName != "octocat/hw" || detail.Data.HeadSha != "s1" {
		t.Fatalf("detail = %s (err=%v)", w.Body.String(), err)
	}
	w = env.getWithToken("/api/automation-agent/pull-request/diff", token)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "diff --git") {
		t.Fatalf("diff = %d %s", w.Code, w.Body.String())
	}

	// fsk_ API Key 明确 40304。
	req := httptest.NewRequest(http.MethodGet, "/api/automation-agent/pull-request", nil)
	req.Header.Set("Authorization", "Bearer fsk_dummy")
	w = httptest.NewRecorder()
	env.router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "40304") {
		t.Fatalf("fsk_ = %d %s, want 403/40304", w.Code, w.Body.String())
	}

	// 结束回报消费令牌（等价于 finished 被受理）：之后一切 PR 端点 40106。
	if err := env.db.Model(&model.AutomationRunToken{}).Where("run_id = ?", run.ID).
		Update("consumed_at", time.Now().UTC()).Error; err != nil {
		t.Fatalf("consume: %v", err)
	}
	w = env.getWithToken("/api/automation-agent/pull-request", token)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "40106") {
		t.Fatalf("after finish = %d %s, want 401/40106", w.Code, w.Body.String())
	}
}

// TestAgentPR_NonPRRunRejected cron 运行的令牌访问 PR 端点 → 40920。
func TestAgentPR_NonPRRunRejected(t *testing.T) {
	env := setupWebhookHandlerEnv(t, "whsec")
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@t", PasswordHash: "x"}
	if err := env.db.Create(user).Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	token, _ := env.seedPRRunWithToken(t, user.ID, model.AutomationTriggerCron, model.AutomationRunDispatched)

	w := env.getWithToken("/api/automation-agent/pull-request", token)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "40920") {
		t.Fatalf("non-PR run = %d %s, want 409/40920", w.Code, w.Body.String())
	}
}

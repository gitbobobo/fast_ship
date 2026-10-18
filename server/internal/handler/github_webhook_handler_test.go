package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/middleware"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GitHub webhook 与 PR 代发端点（INT-93/INT-119）定向测试：503/401/202 语义、
// 按 App ID 头定位用户密钥、幂等、运行令牌边界（结束后 40106、fsk_ 40304、
// 非 PR 运行 40920）。

type webhookHandlerEnv struct {
	db     *gorm.DB
	router *gin.Engine
	fake   *githubapp.Fake
	agent  *service.AutomationAgentService
	runs   *repository.AutomationRunRepository
	tokens *repository.AutomationRunTokenRepository
	cfg    *config.Config
	// integrationSvc 用户级集成服务：setupWebhookEnv 总是创建，用户级验签
	// 场景把它接成 webhook 的密钥来源。
	integrationSvc *service.IntegrationService
}

func setupWebhookHandlerEnv(t *testing.T, secret string) *webhookHandlerEnv {
	return setupWebhookEnv(t, StaticWebhookSecret(secret))
}

// setupUserScopedWebhookEnv 验签密钥走真实集成服务：按 X-GitHub-Hook-
// Installation-Target-ID 头定位到用户行（INT-119 生产形态）。
func setupUserScopedWebhookEnv(t *testing.T) *webhookHandlerEnv {
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
		&model.User{}, &model.Project{}, &model.ApiKey{}, &model.T3Environment{},
		&model.JWTBlacklist{}, &model.RefreshToken{},
		&model.Automation{}, &model.AutomationTrigger{}, &model.AutomationRun{}, &model.AutomationRunToken{}, &model.Notification{},
		&model.GitHubWebhookDelivery{}, &model.GitHubPRDebounce{}, &model.GitHubPRReview{}, &model.GitHubPRSeenSHA{},
		&model.Integration{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_trigger_key ON automation_runs(trigger_id, trigger_key)")
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
		githubapp.StaticClients{Client: fake}, nil, sink, cfg, logger)
	agent := service.NewAutomationAgentService(db, runs, tokens, nil, sink, logger)
	agent.SetPRGitHub(githubapp.StaticClients{Client: fake})
	agent.SetPRReview(prReview)

	integrationSvc := service.NewIntegrationService(repository.NewIntegrationRepository(db), cfg, logger)
	integrationSvc.SetVerify(func(ctx context.Context, appID string, keyPEM []byte, apiBaseURL string) githubapp.Detection {
		return githubapp.Detection{Status: githubapp.DetectionOK, Detail: "已连接 GitHub App：test"}
	})
	if secrets == nil {
		secrets = integrationSvc
	}

	webhookHandler := NewGitHubWebhookHandler(prReview, secrets, fake)
	agentHandler := NewAutomationAgentHandler(agent)

	r := gin.New()
	r.POST("/api/github/webhooks", webhookHandler.Handle)
	r.GET("/api/automation-agent/pull-request", middleware.RequireRunToken(agent, true), agentHandler.GetPullRequest)
	r.GET("/api/automation-agent/pull-request/diff", middleware.RequireRunToken(agent, true), agentHandler.GetPullRequestDiff)
	r.POST("/api/automation-agent/pull-request/review", middleware.RequireRunToken(agent, true), agentHandler.SubmitPullRequestReview)
	r.POST("/api/automation-agent/pull-request/check", middleware.RequireRunToken(agent, true), agentHandler.UpdatePullRequestCheck)
	return &webhookHandlerEnv{db: db, router: r, fake: fake, agent: agent, runs: runs, tokens: tokens, cfg: cfg, integrationSvc: integrationSvc}
}

// postWebhook 签名并投递 payload。
func (e *webhookHandlerEnv) postWebhook(t *testing.T, delivery, event string, body []byte, signature string) *httptest.ResponseRecorder {
	return e.postWebhookWithAppID(t, delivery, event, body, signature, "")
}

// postWebhookWithAppID 带可选 X-GitHub-Hook-Installation-Target-ID 头投递。
func (e *webhookHandlerEnv) postWebhookWithAppID(t *testing.T, delivery, event string, body []byte, signature, appID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/github/webhooks", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	if appID != "" {
		req.Header.Set(headerGitHubAppID, appID)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// createWebhookUser 建一个测试用户。
func createWebhookUser(t *testing.T, db *gorm.DB) *model.User {
	t.Helper()
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@t", PasswordHash: "x"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	return user
}

// saveUserIntegration 以真实集成服务保存某用户的 GitHub App 配置（检测已 stub）。
func (e *webhookHandlerEnv) saveUserIntegration(t *testing.T, userID, appID, secret string) {
	t.Helper()
	pem, err := githubapp.GenerateAppKeyPEM()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if _, err := e.integrationSvc.Update(context.Background(), userID, &api.UpdateGitHubAppIntegrationRequest{
		AppId: appID, PrivateKeyPem: api.Ptr(string(pem)), WebhookSecret: api.Ptr(secret),
	}); err != nil {
		t.Fatalf("save integration: %v", err)
	}
}

// createWebhookPRSite 给用户建关联项目 + T3 环境 + debounce=0 的 opened 与
// synchronize 双模型规则各一条：opened/synchronize 投递都能在请求事务里开轮。
func (e *webhookHandlerEnv) createWebhookPRSite(t *testing.T, userID, owner, repo string) {
	t.Helper()
	now := time.Now().UTC()
	project := &model.Project{
		ID: uuid.NewString(), UserID: userID, Name: "p-" + uuid.NewString()[:8],
		GithubOwner: owner, GithubRepo: repo, PublicGitHubTokenID: "tok-pr-review",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.db.Create(project).Error; err != nil {
		t.Fatalf("project: %v", err)
	}
	envRow := &model.T3Environment{
		ID: uuid.NewString(), UserID: userID, Name: "env-" + uuid.NewString()[:8],
		Address: "https://t3.example.com", BearerTokenEncrypted: []byte("x"),
		BearerExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := e.db.Create(envRow).Error; err != nil {
		t.Fatalf("env: %v", err)
	}
	selsJSON, _ := json.Marshal([]map[string]string{
		{"instance_id": "inst-a", "model": "claude"},
		{"instance_id": "inst-b", "model": "glm"},
	})
	for _, trigger := range []model.AutomationTriggerType{
		model.AutomationTriggerGitHubPROpened, model.AutomationTriggerGitHubPRSynchronize,
	} {
		rule := &model.Automation{
			ID: uuid.NewString(), UserID: userID, Name: "PR 评审 " + string(trigger), Enabled: true,
			CreatedAt: now, UpdatedAt: now, EnvironmentID: envRow.ID, T3ProjectID: "t3-proj",
			PromptTemplate: "评审 {{repo}} #{{pr_number}}",
			RuntimeMode:    "full-access", TimeoutSeconds: 3600,
		}
		if err := e.db.Create(rule).Error; err != nil {
			t.Fatalf("rule: %v", err)
		}
		ruleTrigger := &model.AutomationTrigger{
			ID: uuid.NewString(), AutomationID: rule.ID,
			TriggerType:         trigger,
			ModelSelectionsJSON: string(selsJSON),
			DebounceSeconds:     0,
			CreatedAt:           now, UpdatedAt: now,
		}
		if err := e.db.Create(ruleTrigger).Error; err != nil {
			t.Fatalf("rule trigger: %v", err)
		}
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

// countRunsWhere 统计满足条件的运行行数。
func countRunsWhere(t *testing.T, db *gorm.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.AutomationRun{}).Where(query, args...).Count(&n).Error; err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return n
}

// TestWebhookHandler_UserScopedSecretLocation 身份归属只认验签（INT-119）：
// App ID 头只是候选排序提示——头是任意值（含仓库 ID 形态）或不带都不影响
// 验签归属；各用户行、config 同名身份的密钥各自验各自的；签名对不上仍然 401。
func TestWebhookHandler_UserScopedSecretLocation(t *testing.T) {
	env := setupUserScopedWebhookEnv(t)
	alice := createWebhookUser(t, env.db)
	bob := createWebhookUser(t, env.db)
	env.saveUserIntegration(t, alice.ID, "111", "sec-a")
	env.saveUserIntegration(t, bob.ID, "222", "sec-b")

	body := []byte(`{"action":"opened"}`)
	// 各自的密钥都能验过（带头值是否等于自己的 App ID 无关紧要）。
	w := env.postWebhookWithAppID(t, "d-u1", "pull_request", body, githubapp.SignSignature("sec-a", body), "111")
	if w.Code != http.StatusAccepted {
		t.Fatalf("alice code = %d body=%s", w.Code, w.Body.String())
	}
	w = env.postWebhookWithAppID(t, "d-u2", "pull_request", body, githubapp.SignSignature("sec-b", body), "222")
	if w.Code != http.StatusAccepted {
		t.Fatalf("bob code = %d", w.Code)
	}
	// 头是任意值（GitHub 文档未保证该头等于 App ID，仓库级投递就是仓库 ID）：
	// 签名正确照样验签通过。
	w = env.postWebhookWithAppID(t, "d-u3", "pull_request", body, githubapp.SignSignature("sec-a", body), "501337")
	if w.Code != http.StatusAccepted {
		t.Fatalf("repo-id-like header = %d %s", w.Code, w.Body.String())
	}
	// 无头（simulate 脚本这类调用）：sec-a 只命中 alice 的候选 → 归属 alice。
	w = env.postWebhook(t, "d-u4", "pull_request", body, githubapp.SignSignature("sec-a", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("no header = %d %s", w.Code, w.Body.String())
	}
	// 第三把密钥谁也不匹配 → 401。
	w = env.postWebhook(t, "d-u5", "pull_request", body, githubapp.SignSignature("nope", body))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "Webhook 签名无效或缺失") {
		t.Fatalf("unknown secret = %d %s", w.Code, w.Body.String())
	}

	// 真实 payload：alice 的项目+规则按她的身份匹配开轮。
	env.createWebhookPRSite(t, alice.ID, "octocat", "hw")
	p1 := prWebhookPayload("opened", "octocat/hw", 1, "sha-1")
	w = env.postWebhookWithAppID(t, "d-r1", "pull_request", p1, githubapp.SignSignature("sec-a", p1), "111")
	if w.Code != http.StatusAccepted {
		t.Fatalf("alice real payload = %d %s", w.Code, w.Body.String())
	}
	if n := countRunsWhere(t, env.db, "user_id = ? AND head_sha = ?", alice.ID, "sha-1"); n != 2 {
		t.Fatalf("alice runs = %d, want 2 (rule matched under her identity)", n)
	}

	// bob 的身份投递同一仓库的新 SHA：不能取消 alice 的在途。
	env.saveUserIntegration(t, bob.ID, "222", "sec-b")
	p2 := prWebhookPayload("synchronize", "octocat/hw", 1, "sha-2")
	w = env.postWebhookWithAppID(t, "d-r2", "pull_request", p2, githubapp.SignSignature("sec-b", p2), "222")
	if w.Code != http.StatusAccepted {
		t.Fatalf("bob cross-identity sync = %d %s", w.Code, w.Body.String())
	}
	if n := countRunsWhere(t, env.db, "user_id = ? AND head_sha = ? AND status <> ?",
		alice.ID, "sha-1", model.AutomationRunCancelled); n != 2 {
		t.Fatalf("alice live runs after bob delivery = %d, want 2 (cancel scoped to identity)", n)
	}
	if n := countRunsWhere(t, env.db, "1=1"); n != 2 {
		t.Fatalf("total runs after bob delivery = %d, want 2 (no round for bob)", n)
	}

	// alice 自己的新 SHA 才会取消她的旧轮并开新一轮。
	w = env.postWebhookWithAppID(t, "d-r3", "pull_request", p2, githubapp.SignSignature("sec-a", p2), "111")
	if w.Code != http.StatusAccepted {
		t.Fatalf("alice own sync = %d %s", w.Code, w.Body.String())
	}
	if n := countRunsWhere(t, env.db, "user_id = ? AND head_sha = ? AND status = ?",
		alice.ID, "sha-1", model.AutomationRunCancelled); n != 2 {
		t.Fatalf("alice cancelled old runs = %d, want 2", n)
	}
	if n := countRunsWhere(t, env.db, "user_id = ? AND head_sha = ? AND status <> ?",
		alice.ID, "sha-2", model.AutomationRunCancelled); n != 2 {
		t.Fatalf("alice new round = %d, want 2", n)
	}
}

// TestWebhookHandler_ForgedDeliveryStaysInOwnIdentity 伪造投递不越权：alice 的
// App/密钥签一个指向 bob 项目仓库的 payload，验签通过也只落在 alice 身份上——
// 不取消 bob 的在途运行、不为 bob 开轮、bob 的评审账不变。
func TestWebhookHandler_ForgedDeliveryStaysInOwnIdentity(t *testing.T) {
	env := setupUserScopedWebhookEnv(t)
	alice := createWebhookUser(t, env.db)
	bob := createWebhookUser(t, env.db)
	env.saveUserIntegration(t, alice.ID, "111", "sec-a")
	env.saveUserIntegration(t, bob.ID, "222", "sec-b")
	env.createWebhookPRSite(t, bob.ID, "octocat", "hw")

	// bob 自己身份的正常投递：开一轮（2 个模型）。
	p1 := prWebhookPayload("opened", "octocat/hw", 1, "sha-1")
	w := env.postWebhookWithAppID(t, "d-b1", "pull_request", p1, githubapp.SignSignature("sec-b", p1), "222")
	if w.Code != http.StatusAccepted {
		t.Fatalf("bob round = %d %s", w.Code, w.Body.String())
	}
	if n := countRunsWhere(t, env.db, "user_id = ? AND head_sha = ?", bob.ID, "sha-1"); n != 2 {
		t.Fatalf("bob runs = %d, want 2", n)
	}

	// 伪造：alice 的 App ID + alice 的密钥，payload 指向 bob 的仓库、新 SHA。
	forged := prWebhookPayload("synchronize", "octocat/hw", 1, "sha-2")
	w = env.postWebhookWithAppID(t, "d-forge", "pull_request", forged, githubapp.SignSignature("sec-a", forged), "111")
	if w.Code != http.StatusAccepted {
		t.Fatalf("forged delivery = %d %s", w.Code, w.Body.String())
	}
	// bob 的在途不被取消、没有新运行、评审账只有 bob 一行且未动。
	if n := countRunsWhere(t, env.db, "user_id = ? AND head_sha = ? AND status <> ?",
		bob.ID, "sha-1", model.AutomationRunCancelled); n != 2 {
		t.Fatalf("bob live runs after forged delivery = %d, want 2", n)
	}
	if n := countRunsWhere(t, env.db, "1=1"); n != 2 {
		t.Fatalf("total runs = %d, want 2", n)
	}
	var revs []model.GitHubPRReview
	if err := env.db.Find(&revs).Error; err != nil {
		t.Fatalf("reviews: %v", err)
	}
	if len(revs) != 1 || revs[0].AppUserID != bob.ID || revs[0].HeadSHA != "sha-1" {
		t.Fatalf("review rows = %+v, want single bob row at sha-1", revs)
	}
}

// TestWebhookHandler_DuplicateAppIDRejected 同一 App ID 全库唯一：第二个用户
// 保存被 409 拒绝、库里仍只有第一用户的行，投递候选也只剩该用户——「同 App ID
// 多行互相遮蔽、创建顺序靠后者永远收不到投递」被挡在保存时。
func TestWebhookHandler_DuplicateAppIDRejected(t *testing.T) {
	env := setupUserScopedWebhookEnv(t)
	alice := createWebhookUser(t, env.db)
	bob := createWebhookUser(t, env.db)
	env.saveUserIntegration(t, alice.ID, "555", "sec-alice")

	// bob 保存同一 App ID → 40929（service 层断言，保存被整体拒绝）。
	pem, err := githubapp.GenerateAppKeyPEM()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	_, err = env.integrationSvc.Update(context.Background(), bob.ID, &api.UpdateGitHubAppIntegrationRequest{
		AppId: "555", PrivateKeyPem: api.Ptr(string(pem)), WebhookSecret: api.Ptr("sec-bob"),
	})
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrIntegrationAppIDTaken.Code {
		t.Fatalf("bob duplicate save = %v, want [%d]", err, errs.ErrIntegrationAppIDTaken.Code)
	}
	var rows []model.Integration
	if err := env.db.Find(&rows).Error; err != nil || len(rows) != 1 || rows[0].UserID != alice.ID {
		t.Fatalf("rows = %+v %v, want single alice row", rows, err)
	}

	// 候选只剩 alice 一份（App ID 头 555 不会被别人遮蔽）。
	cands, err := env.integrationSvc.WebhookCandidates(context.Background(), "555")
	if err != nil || len(cands) != 1 || cands[0].AppUserID != alice.ID || cands[0].Secret != "sec-alice" {
		t.Fatalf("candidates = %v %v, want single alice candidate", cands, err)
	}
}

// TestWebhookHandler_SameRepoTwoIdentitiesIsolated 同仓库两个身份的投递各自
// 归各自的账：bob 先开轮（含在途运行、已签发令牌、待触发防抖），alice 也关联
// 同仓库后推新 SHA——只有 alice 自己的旧轮被取消、新轮开启；bob 的运行、令牌、
// 防抖与评审账完全不变。
func TestWebhookHandler_SameRepoTwoIdentitiesIsolated(t *testing.T) {
	env := setupUserScopedWebhookEnv(t)
	alice := createWebhookUser(t, env.db)
	bob := createWebhookUser(t, env.db)
	env.saveUserIntegration(t, alice.ID, "111", "sec-a")
	env.saveUserIntegration(t, bob.ID, "222", "sec-b")
	env.createWebhookPRSite(t, alice.ID, "octocat", "hw")
	env.createWebhookPRSite(t, bob.ID, "octocat", "hw")

	// bob 以自己身份开轮（2 个模型）。
	pb := prWebhookPayload("opened", "octocat/hw", 1, "sha-1")
	w := env.postWebhookWithAppID(t, "d-b1", "pull_request", pb, githubapp.SignSignature("sec-b", pb), "222")
	if w.Code != http.StatusAccepted {
		t.Fatalf("bob round = %d %s", w.Code, w.Body.String())
	}
	var bobRuns []model.AutomationRun
	if err := env.db.Where("user_id = ?", bob.ID).Find(&bobRuns).Error; err != nil || len(bobRuns) != 2 {
		t.Fatalf("bob runs = %d %v, want 2", len(bobRuns), err)
	}
	// 给 bob 布置一张已签发令牌与一条待触发防抖（他的另一个 SHA 窗口）。
	now := time.Now().UTC()
	if err := env.db.Create(&model.AutomationRunToken{
		RunID: bobRuns[0].ID, TokenHash: "hash-bob", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}
	var bobSyncTrigger model.AutomationTrigger
	if err := env.db.Joins("JOIN automations ON automations.id = automation_triggers.automation_id").
		Where("automations.user_id = ? AND automation_triggers.trigger_type = ?",
			bob.ID, model.AutomationTriggerGitHubPRSynchronize).
		First(&bobSyncTrigger).Error; err != nil {
		t.Fatalf("bob synchronize trigger: %v", err)
	}
	if err := env.db.Create(&model.GitHubPRDebounce{
		TriggerID: bobSyncTrigger.ID, RepoFullName: "octocat/hw", PRNumber: 1,
		AppUserID: bob.ID, UserID: bob.ID, ProjectID: "p", InstallationID: 7,
		Action: "synchronize", HeadSHA: "sha-1", FireAt: now.Add(time.Hour), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed debounce: %v", err)
	}

	// alice 也投递同一仓库的新 SHA：她的旧轮取消、新轮开启。
	pa1 := prWebhookPayload("opened", "octocat/hw", 1, "sha-1")
	w = env.postWebhookWithAppID(t, "d-a1", "pull_request", pa1, githubapp.SignSignature("sec-a", pa1), "111")
	if w.Code != http.StatusAccepted {
		t.Fatalf("alice round 1 = %d %s", w.Code, w.Body.String())
	}
	pa2 := prWebhookPayload("synchronize", "octocat/hw", 1, "sha-2")
	w = env.postWebhookWithAppID(t, "d-a2", "pull_request", pa2, githubapp.SignSignature("sec-a", pa2), "111")
	if w.Code != http.StatusAccepted {
		t.Fatalf("alice round 2 = %d %s", w.Code, w.Body.String())
	}
	if n := countRunsWhere(t, env.db, "user_id = ? AND head_sha = ? AND status = ?",
		alice.ID, "sha-1", model.AutomationRunCancelled); n != 2 {
		t.Fatalf("alice cancelled old runs = %d, want 2", n)
	}
	if n := countRunsWhere(t, env.db, "user_id = ? AND head_sha = ? AND status <> ?",
		alice.ID, "sha-2", model.AutomationRunCancelled); n != 2 {
		t.Fatalf("alice new round = %d, want 2", n)
	}

	// bob 的运行、令牌、防抖、评审账完全不变。
	for _, run := range bobRuns {
		var fresh model.AutomationRun
		if err := env.db.Where("id = ?", run.ID).First(&fresh).Error; err != nil {
			t.Fatalf("reload bob run: %v", err)
		}
		if fresh.Status == model.AutomationRunCancelled {
			t.Fatalf("bob run %s cancelled by alice's delivery", run.ID)
		}
	}
	var token model.AutomationRunToken
	if err := env.db.Where("run_id = ?", bobRuns[0].ID).First(&token).Error; err != nil {
		t.Fatalf("reload token: %v", err)
	}
	if token.ConsumedAt != nil {
		t.Fatal("bob token must stay unconsumed")
	}
	var d model.GitHubPRDebounce
	if err := env.db.Where("app_user_id = ?", bob.ID).First(&d).Error; err != nil {
		t.Fatalf("reload debounce: %v", err)
	}
	if d.HeadSHA != "sha-1" {
		t.Fatalf("bob debounce sha = %s, want sha-1", d.HeadSHA)
	}
	var bobRev model.GitHubPRReview
	if err := env.db.Where("app_user_id = ?", bob.ID).First(&bobRev).Error; err != nil {
		t.Fatalf("reload bob review: %v", err)
	}
	if bobRev.HeadSHA != "sha-1" {
		t.Fatalf("bob review sha = %s, want sha-1", bobRev.HeadSHA)
	}
	// alice 的评审账独立成行（app_user_id=alice，已推进到新 SHA）。
	var aliceRev model.GitHubPRReview
	if err := env.db.Where("app_user_id = ?", alice.ID).First(&aliceRev).Error; err != nil {
		t.Fatalf("reload alice review: %v", err)
	}
	if aliceRev.HeadSHA != "sha-2" {
		t.Fatalf("alice review sha = %s, want sha-2", aliceRev.HeadSHA)
	}
}

// TestWebhookHandler_ConfigAndUserShareAppID 用户行与 config 配同一 App ID：
// 用户行不遮蔽 config 候选，config 密钥仍能验签且身份为空串（部署身份）。
func TestWebhookHandler_ConfigAndUserShareAppID(t *testing.T) {
	env := setupUserScopedWebhookEnv(t)
	alice := createWebhookUser(t, env.db)
	env.saveUserIntegration(t, alice.ID, "777", "sec-alice")
	env.cfg.GitHubApp.AppID = "777"
	env.cfg.GitHubApp.WebhookSecret = "cfg-secret"

	cands, err := env.integrationSvc.WebhookCandidates(context.Background(), "777")
	if err != nil || len(cands) != 2 {
		t.Fatalf("candidates = %v %v, want user row + config", cands, err)
	}
	if cands[0].AppUserID != alice.ID || cands[0].Secret != "sec-alice" ||
		cands[1].AppUserID != "" || cands[1].Secret != "cfg-secret" {
		t.Fatalf("candidates = %+v, want [alice row, config]", cands)
	}

	body := []byte(`{"action":"opened"}`)
	// config 密钥不被用户行遮蔽。
	w := env.postWebhookWithAppID(t, "d-c1", "pull_request", body, githubapp.SignSignature("cfg-secret", body), "777")
	if w.Code != http.StatusAccepted {
		t.Fatalf("config secret = %d %s", w.Code, w.Body.String())
	}
	// 用户行密钥同样可用。
	w = env.postWebhookWithAppID(t, "d-c2", "pull_request", body, githubapp.SignSignature("sec-alice", body), "777")
	if w.Code != http.StatusAccepted {
		t.Fatalf("user secret = %d %s", w.Code, w.Body.String())
	}
	// 第三把密钥 401。
	w = env.postWebhookWithAppID(t, "d-c3", "pull_request", body, githubapp.SignSignature("nope", body), "777")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong secret = %d, want 401", w.Code)
	}
}

// TestWebhookHandler_LegacyConfigOnlySecretWithHeader 仅配 config webhook 密钥
// 的旧部署（INT-93，无 App 身份）：真实带头投递也能验签，不能 401。
func TestWebhookHandler_LegacyConfigOnlySecretWithHeader(t *testing.T) {
	env := setupUserScopedWebhookEnv(t)
	env.cfg.GitHubApp.WebhookSecret = "legacy-secret"

	body := prWebhookPayload("opened", "octocat/hw", 1, "sha-1")
	// GitHub App 真实投递必带的 App ID 头（部署 config 没配身份也照收）。
	w := env.postWebhookWithAppID(t, "d-l1", "pull_request", body, githubapp.SignSignature("legacy-secret", body), "999888")
	if w.Code != http.StatusAccepted {
		t.Fatalf("legacy header delivery = %d %s", w.Code, w.Body.String())
	}
	// 密钥不对仍然 401。
	w = env.postWebhookWithAppID(t, "d-l2", "pull_request", body, githubapp.SignSignature("other", body), "999888")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("legacy wrong secret = %d, want 401", w.Code)
	}
	// config 密钥真的清空后：候选为空（全部候选密钥为空）→ 503，不是 401
	// ——没有可验签的身份，拒绝接受请求。
	env.cfg.GitHubApp.WebhookSecret = ""
	w = env.postWebhookWithAppID(t, "d-l3", "pull_request", body, githubapp.SignSignature("legacy-secret", body), "999888")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("cleared secret = %d %s, want 503", w.Code, w.Body.String())
	}
}

// TestWebhookHandler_SameSecretTwoIdentitiesRejected 两个身份配了相同的
// webhook 密钥：一次验签命中多份候选，无法安全归属（投递可能来自其中任何
// 一份），拒绝而不是取第一个命中。
func TestWebhookHandler_SameSecretTwoIdentitiesRejected(t *testing.T) {
	env := setupUserScopedWebhookEnv(t)
	alice := createWebhookUser(t, env.db)
	bob := createWebhookUser(t, env.db)
	env.saveUserIntegration(t, alice.ID, "111", "same-secret")
	env.saveUserIntegration(t, bob.ID, "222", "same-secret")

	body := []byte(`{"action":"opened"}`)
	for _, appID := range []string{"111", "222", "501337", ""} {
		var w *httptest.ResponseRecorder
		if appID == "" {
			w = env.postWebhook(t, "d-s-"+appID+"x", "pull_request", body, githubapp.SignSignature("same-secret", body))
		} else {
			w = env.postWebhookWithAppID(t, "d-s-"+appID, "pull_request", body, githubapp.SignSignature("same-secret", body), appID)
		}
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "无法确定集成身份") {
			t.Fatalf("same-secret header=%q = %d %s, want 401 无法确定集成身份", appID, w.Code, w.Body.String())
		}
	}

	// 恢复互不相同：立刻又能各自归属（错误是候选集形状问题，不是请求问题）。
	env.saveUserIntegration(t, bob.ID, "222", "sec-b")
	w := env.postWebhookWithAppID(t, "d-s-fix", "pull_request", body, githubapp.SignSignature("sec-b", body), "222")
	if w.Code != http.StatusAccepted {
		t.Fatalf("after dedupe = %d %s", w.Code, w.Body.String())
	}
}

// TestWebhookHandler_StaticSecretTrimmed 静态密钥来源（config/fake 路径）返回
// 单候选且已 trim：trim 后的密钥能验签，原串不能。
func TestWebhookHandler_StaticSecretTrimmed(t *testing.T) {
	src := StaticWebhookSecret("  whsec  ")
	cands, err := src.WebhookCandidates(context.Background(), "")
	if err != nil || len(cands) != 1 || cands[0].AppUserID != "" || cands[0].Secret != "whsec" {
		t.Fatalf("static candidates = %v %v, want trimmed config candidate", cands, err)
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

// TestWebhookHandler_UserScopedSecretFailures 行内密钥为空 → 503；行密文损坏
// （解密失败）→ 500/50000。
func TestWebhookHandler_UserScopedSecretFailures(t *testing.T) {
	env := setupUserScopedWebhookEnv(t)
	user := createWebhookUser(t, env.db)
	env.saveUserIntegration(t, user.ID, "111", "")

	body := []byte(`{"action":"opened"}`)
	w := env.postWebhookWithAppID(t, "d-f1", "pull_request", body, "sha256=whatever", "111")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty row secret = %d, want 503", w.Code)
	}

	if err := env.db.Model(&model.Integration{}).Where("app_id = ?", "111").
		Update("config_encrypted", []byte("corrupted-ciphertext")).Error; err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	w = env.postWebhookWithAppID(t, "d-f2", "pull_request", body, "sha256=whatever", "111")
	if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "50000") {
		t.Fatalf("corrupt row = %d %s, want 500/50000", w.Code, w.Body.String())
	}
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
	project := &model.Project{ID: uuid.NewString(), UserID: user.ID, Name: "p", GithubOwner: "octocat", GithubRepo: "hw", PublicGitHubTokenID: "tok-pr-review", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
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
		PromptTemplate: "评审 {{repo}} #{{pr_number}} @ {{head_sha}} {{pr_title}} {{pr_url}}",
		RuntimeMode:    "full-access", TimeoutSeconds: 3600,
	}
	if err := env.db.Create(rule).Error; err != nil {
		t.Fatalf("rule: %v", err)
	}
	ruleTrigger := &model.AutomationTrigger{
		ID: uuid.NewString(), AutomationID: rule.ID,
		TriggerType:         model.AutomationTriggerGitHubPROpened,
		ModelSelectionsJSON: string(selsJSON),
		DebounceSeconds:     0,
		CreatedAt:           now, UpdatedAt: now,
	}
	if err := env.db.Create(ruleTrigger).Error; err != nil {
		t.Fatalf("rule trigger: %v", err)
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
		PromptTemplate: "x", RuntimeMode: "full-access", TimeoutSeconds: 3600,
	}
	if err := e.db.Create(hostRule).Error; err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	hostTrigger := &model.AutomationTrigger{
		ID: uuid.NewString(), AutomationID: hostRule.ID,
		TriggerType: triggerType, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.db.Create(hostTrigger).Error; err != nil {
		t.Fatalf("seed trigger: %v", err)
	}
	run := &model.AutomationRun{
		ID: uuid.NewString(), UserID: userID, AutomationID: hostRule.ID,
		TriggerID:   hostTrigger.ID,
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

func (e *webhookHandlerEnv) postJSONWithToken(path, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
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
	env.fake.SetPR("octocat/hw", 1, &githubapi.PullRequest{Title: "T", HTMLURL: "https://github.com/octocat/hw/pull/1", HeadSHA: "s1"})

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

func (e *webhookHandlerEnv) seedCheckForRun(t *testing.T, run *model.AutomationRun) int64 {
	t.Helper()
	checkID, err := e.fake.CreateCheckRun(context.Background(), run.InstallationID, run.RepoFullName, "Fast Ship review (claude)", run.HeadSHA, run.ID)
	if err != nil {
		t.Fatalf("create check: %v", err)
	}
	if err := e.db.Model(&model.AutomationRun{}).Where("id = ?", run.ID).Update("check_run_id", checkID).Error; err != nil {
		t.Fatalf("bind check: %v", err)
	}
	return checkID
}

// TestAgentPR_UpdateCheckRejectsCancelledRun 取消后即使令牌未消费也不再把 check
// 打回 in_progress。
func TestAgentPR_UpdateCheckRejectsCancelledRun(t *testing.T) {
	env := setupWebhookHandlerEnv(t, "whsec")
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@t", PasswordHash: "x"}
	if err := env.db.Create(user).Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	token, run := env.seedPRRunWithToken(t, user.ID, model.AutomationTriggerGitHubPROpened, model.AutomationRunDispatched)
	env.seedCheckForRun(t, run)

	w := env.postJSONWithToken("/api/automation-agent/pull-request/check", token, []byte(`{"title":"评审中","summary":"s"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("live check = %d %s", w.Code, w.Body.String())
	}
	before := env.fake.CallCounts()["UpdateCheckRun"]

	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", run.ID).
		Updates(map[string]any{
			"status":      model.AutomationRunCancelled,
			"finished_at": time.Now().UTC(),
			"error_code":  "superseded",
		}).Error; err != nil {
		t.Fatalf("cancel: %v", err)
	}
	w = env.postJSONWithToken("/api/automation-agent/pull-request/check", token, []byte(`{"title":"仍在写","summary":"s"}`))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "40106") {
		t.Fatalf("cancelled check = %d %s, want 401/40106", w.Code, w.Body.String())
	}
	if after := env.fake.CallCounts()["UpdateCheckRun"]; after != before {
		t.Fatalf("UpdateCheckRun after cancel = %d, want %d", after, before)
	}
}

type cancelAfterCheckUpdate struct {
	githubapi.GitHubClient
	after func()
}

func (c *cancelAfterCheckUpdate) UpdateCheckRun(ctx context.Context, installationID int64, repoFullName string, checkRunID int64, in githubapi.CheckUpdate) error {
	err := c.GitHubClient.UpdateCheckRun(ctx, installationID, repoFullName, checkRunID, in)
	if c.after != nil {
		c.after()
	}
	return err
}

// TestAgentPR_UpdateCheckMarksPendingIfCancelledDuringWrite PATCH 之后才变成终态
// 时置 sync_pending，让同步覆盖 in_progress。
func TestAgentPR_UpdateCheckMarksPendingIfCancelledDuringWrite(t *testing.T) {
	env := setupWebhookHandlerEnv(t, "whsec")
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@t", PasswordHash: "x"}
	if err := env.db.Create(user).Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	token, run := env.seedPRRunWithToken(t, user.ID, model.AutomationTriggerGitHubPROpened, model.AutomationRunDispatched)
	env.seedCheckForRun(t, run)
	now := time.Now().UTC()
	if err := env.db.Create(&model.GitHubPRReview{
		RepoFullName: run.RepoFullName, PRNumber: run.PRNumber, InstallationID: run.InstallationID,
		RoundNumber: 1, HeadSHA: run.HeadSHA, SyncPending: false, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("review: %v", err)
	}

	env.agent.SetPRGitHub(githubapp.StaticClients{Client: &cancelAfterCheckUpdate{
		GitHubClient: env.fake,
		after: func() {
			_ = env.db.Model(&model.AutomationRun{}).Where("id = ?", run.ID).
				Updates(map[string]any{
					"status":      model.AutomationRunCancelled,
					"finished_at": time.Now().UTC(),
					"error_code":  "superseded",
				}).Error
		},
	}})

	w := env.postJSONWithToken("/api/automation-agent/pull-request/check", token, []byte(`{"title":"评审中","summary":"s"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("check = %d %s", w.Code, w.Body.String())
	}
	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ? AND pr_number = ?", run.RepoFullName, run.PRNumber).First(&rev).Error; err != nil {
		t.Fatalf("reload review: %v", err)
	}
	if !rev.SyncPending {
		t.Fatal("expected sync_pending after cancel-during-write")
	}
}

// TestAgentPR_SubmitReviewRejectsCancelledRun 取消后不再提交 superseded SHA 的 review。
func TestAgentPR_SubmitReviewRejectsCancelledRun(t *testing.T) {
	env := setupWebhookHandlerEnv(t, "whsec")
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@t", PasswordHash: "x"}
	if err := env.db.Create(user).Error; err != nil {
		t.Fatalf("user: %v", err)
	}
	token, run := env.seedPRRunWithToken(t, user.ID, model.AutomationTriggerGitHubPROpened, model.AutomationRunDispatched)

	w := env.postJSONWithToken("/api/automation-agent/pull-request/review", token, []byte(`{"event":"COMMENT","body":"ok"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("live review = %d %s", w.Code, w.Body.String())
	}
	before := env.fake.CallCounts()["SubmitReview"]

	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", run.ID).
		Updates(map[string]any{
			"status":      model.AutomationRunCancelled,
			"finished_at": time.Now().UTC(),
			"error_code":  "superseded",
		}).Error; err != nil {
		t.Fatalf("cancel: %v", err)
	}
	w = env.postJSONWithToken("/api/automation-agent/pull-request/review", token, []byte(`{"event":"REQUEST_CHANGES","body":"stale"}`))
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "40106") {
		t.Fatalf("cancelled review = %d %s, want 401/40106", w.Code, w.Body.String())
	}
	if after := env.fake.CallCounts()["SubmitReview"]; after != before {
		t.Fatalf("SubmitReview after cancel = %d, want %d", after, before)
	}
}

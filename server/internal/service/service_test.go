package service

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/crypto"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/glebarez/sqlite"
)

type testServices struct {
	db                  *gorm.DB
	storage             storage.Storage
	cfg                 *config.Config
	userAISettingRepo   *repository.UserAISettingRepository
	userIssuePromptRepo *repository.UserIssuePromptSettingRepository
	projectRepo         *repository.ProjectRepository
	tokenRepo           *repository.UserGitHubTokenRepository
	versionRepo         *repository.VersionRepository
	issueRepo           *repository.IssueRepository
	gitHubMetaRepo      *repository.IssueGitHubMetaRepository
	commentRepo         *repository.IssueCommentRepository
	timelineRepo        *repository.IssueTimelineRepository
	internalMetaRepo    *repository.IssueInternalMetaRepository
	shipHookRepo        *repository.IssueShipHookRepository
	shipHookService     *IssueShipHookService
	checklistRepo       *repository.IssueChecklistRepository
	syncStateRepo       *repository.IssueSyncStateRepository
	issueAssetRepo      *repository.IssueAssetRepository
	issueDraftAssetRepo *repository.IssueDraftAssetRepository
	issueAttachmentRepo *repository.IssueAttachmentRepository
	artifactRepo        *repository.ArtifactRepository
	screenshotRepo      *repository.ScreenshotRepository
	annotationRepo      *repository.ScreenshotAnnotationRepository
	collabRepo          *repository.IssueCollabRepository
	readStateRepo       *repository.IssueReadStateRepository
	recRepo             *repository.IssueRecommendationRepository
	issueService        *IssueService
	attachmentService   *IssueAttachmentService
	collabService       *IssueCollabService
	recService          *IssueRecommendationService
	aiService           *AIService
	issuePromptService  *IssuePromptService
	versionService      *VersionService
	artifactService     *ArtifactService
	screenshotService   *ScreenshotService
	annotationService   *ScreenshotAnnotationService
	shipService         *ShipService
}

func setupTestServices(t *testing.T) *testServices {
	t.Helper()

	// 与生产 DSN 一致携带 busy_timeout，否则共享内存库上并发事务撞锁会立刻报
	// database is locked，而不是像生产一样等待持锁事务提交（并发用例会偶发失败）。
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	cleanupTestDB(t, db)

	if err := db.AutoMigrate(
		&model.User{},
		&model.UserGitHubToken{},
		&model.UserAISetting{},
		&model.UserIssuePromptSetting{},
		&model.ApiKey{},
		&model.Project{},
		&model.Version{},
		&model.Issue{},
		&model.IssueGitHubMeta{},
		&model.IssueComment{},
		&model.IssueTimelineEvent{},
		&model.IssueInternalMeta{},
		&model.IssueShipHook{},
		&model.IssueChecklistItem{},
		&model.IssueSyncState{},
		&model.IssueReadState{},
		&model.IssueAsset{},
		&model.IssueDraftAsset{},
		&model.IssueAttachment{},
		&model.Artifact{},
		&model.JWTBlacklist{},
		&model.GitHubRepoLabel{},
		&model.IssueCollabDocument{},
		&model.IssuePullRequest{},
		&model.IssueRecommendation{},
		&model.RecommendationDependency{},
		&model.ScreenshotScreen{},
		&model.ScreenshotAnnotation{},
		&model.ScreenshotPendingDelete{},
		&model.T3Environment{},
		&model.ProjectT3Binding{},
		&model.Notification{},
		&model.GitHubWebhookDelivery{},
		&model.GitHubWebhookRedelivery{},
		// 代码仓集成（INT-166）：项目绑定与链接兜底测试要用。
		&model.ForgeIntegration{},
		&model.ProjectReviewSettings{},
		&model.ReviewRun{},
		&model.ReviewRunToken{},
		&model.ReviewPullRequest{},
		&model.ReviewAsset{},
		&model.ReviewDebounce{},
		&model.ReviewSeenSHA{},
		&model.LegacyAutomationThread{},
		&model.QuotaAccount{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	// INT-209：q 搜索依赖 issues_fts 派生索引，由启动接线补齐；service 测试
	// 绕过 main.go，这里与生产顺序一致（AutoMigrate 之后）补齐。
	if err := repository.EnsureIssueFTSSchema(db); err != nil {
		t.Fatalf("ensure issue fts: %v", err)
	}

	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_name ON projects(user_id, name)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_versions_project_version ON versions(project_id, version_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_sequence ON issues(project_id, sequence_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_project_github_issue ON issue_github_meta(project_id, github_issue_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_issue_id ON issue_github_meta(issue_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_comments_issue_github_comment ON issue_comments(issue_id, github_comment_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_comments_issue_idempotency_key ON issue_comments(issue_id, idempotency_key) WHERE idempotency_key <> ''")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_timeline_issue_event_key ON issue_timeline_events(issue_id, event_key)")
	db.Exec("DROP INDEX IF EXISTS idx_issue_pull_requests_issue_pr")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_pull_requests_issue_pr_v2 ON issue_pull_requests(issue_id, provider, host_key, repo_full_name, number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_screenshot_screens_variant ON screenshot_screens(project_id, screen_key, COALESCE(platform,''),COALESCE(locale,''),COALESCE(theme,''))")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_t3_envs_user_name ON t3_environments(user_id, name)")

	tempDir := t.TempDir()
	fileStorage := storage.NewLocalStorage(filepath.Join(tempDir, "uploads"))

	userRepo := repository.NewUserRepository(db)
	userAISettingRepo := repository.NewUserAISettingRepository(db)
	userIssuePromptRepo := repository.NewUserIssuePromptSettingRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	tokenRepo := repository.NewUserGitHubTokenRepository(db)
	versionRepo := repository.NewVersionRepository(db)
	issueRepo := repository.NewIssueRepository(db)
	gitHubMetaRepo := repository.NewIssueGitHubMetaRepository(db)
	commentRepo := repository.NewIssueCommentRepository(db)
	timelineRepo := repository.NewIssueTimelineRepository(db)
	internalMetaRepo := repository.NewIssueInternalMetaRepository(db)
	shipHookRepo := repository.NewIssueShipHookRepository(db)
	shipHookService := NewIssueShipHookService(issueRepo, projectRepo, shipHookRepo)
	checklistRepo := repository.NewIssueChecklistRepository(db)
	syncStateRepo := repository.NewIssueSyncStateRepository(db)
	issueAssetRepo := repository.NewIssueAssetRepository(db)
	issueDraftAssetRepo := repository.NewIssueDraftAssetRepository(db)
	issueAttachmentRepo := repository.NewIssueAttachmentRepository(db)
	artifactRepo := repository.NewArtifactRepository(db)
	screenshotRepo := repository.NewScreenshotRepository(db)
	annotationRepo := repository.NewScreenshotAnnotationRepository(db)
	collabRepo := repository.NewIssueCollabRepository(db)
	readStateRepo := repository.NewIssueReadStateRepository(db)
	recRepo := repository.NewIssueRecommendationRepository(db)
	t3EnvironmentRepo := repository.NewT3EnvironmentRepository(db)

	cfg := &config.Config{
		Encryption: config.EncryptionConfig{
			Key: "12345678901234567890123456789012",
		},
	}

	svc := &testServices{
		db:                  db,
		storage:             fileStorage,
		cfg:                 cfg,
		userAISettingRepo:   userAISettingRepo,
		userIssuePromptRepo: userIssuePromptRepo,
		projectRepo:         projectRepo,
		tokenRepo:           tokenRepo,
		versionRepo:         versionRepo,
		issueRepo:           issueRepo,
		gitHubMetaRepo:      gitHubMetaRepo,
		commentRepo:         commentRepo,
		timelineRepo:        timelineRepo,
		internalMetaRepo:    internalMetaRepo,
		shipHookRepo:        shipHookRepo,
		shipHookService:     shipHookService,
		checklistRepo:       checklistRepo,
		syncStateRepo:       syncStateRepo,
		issueAssetRepo:      issueAssetRepo,
		issueDraftAssetRepo: issueDraftAssetRepo,
		issueAttachmentRepo: issueAttachmentRepo,
		artifactRepo:        artifactRepo,
		screenshotRepo:      screenshotRepo,
		annotationRepo:      annotationRepo,
		collabRepo:          collabRepo,
		readStateRepo:       readStateRepo,
		recRepo:             recRepo,
		issueService:        NewIssueService(issueRepo, gitHubMetaRepo, commentRepo, timelineRepo, internalMetaRepo, shipHookService, checklistRepo, syncStateRepo, issueAssetRepo, issueDraftAssetRepo, projectRepo, tokenRepo, userRepo, repository.NewGitHubRepoLabelRepository(db), readStateRepo, recRepo, repository.NewIssuePullRequestRepository(db), issueAttachmentRepo, fileStorage, cfg, zap.NewNop(), annotationRepo, repository.NewForgeIntegrationRepository(db), NewForgeClientSource(repository.NewForgeIntegrationRepository(db), cfg, zap.NewNop())),
		attachmentService:   NewIssueAttachmentService(issueAttachmentRepo, issueRepo, projectRepo, fileStorage, cfg, zap.NewNop()),
		collabService:       NewIssueCollabService(collabRepo, issueRepo, projectRepo, userRepo),
		recService:          NewIssueRecommendationService(recRepo, issueRepo, internalMetaRepo, projectRepo),
		aiService:           NewAIService(userAISettingRepo, issueRepo, commentRepo, projectRepo, cfg, zap.NewNop()),
		issuePromptService:  NewIssuePromptService(userIssuePromptRepo, t3EnvironmentRepo),
		versionService:      NewVersionService(versionRepo, projectRepo, tokenRepo, fileStorage, cfg, zap.NewNop()),
		artifactService:     NewArtifactService(artifactRepo, versionRepo, projectRepo, fileStorage),
		screenshotService:   NewScreenshotService(screenshotRepo, projectRepo, fileStorage, cfg),
		annotationService:   NewScreenshotAnnotationService(annotationRepo, screenshotRepo, issueRepo, projectRepo, fileStorage),
	}
	svc.shipService = NewShipService(versionRepo, projectRepo, tokenRepo, artifactRepo, issueRepo, shipHookRepo, svc.issueService, fileStorage, cfg, zap.NewNop())

	return svc
}

// cleanupTestDB 注册 t.Cleanup 关闭 sql.DB 句柄：共享缓存内存 SQLite 的
// connectionOpener 在句柄关闭前常驻，上千个用例累积的 goroutine 与内存页
// 曾把全量 -race 拖过门禁超时（INT-205）。各测试装配点在 gorm.Open 后立即
// 注册；t.Cleanup 的 LIFO 顺序保证后注册的服务层停止逻辑（如 BotService
// Stop）先跑，后台组件全部退出后才关池。
func cleanupTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			sqlDB.Close() //nolint:errcheck // 测试收尾的连接池关闭，失败无从上报
		}
	})
}

// autoTokenRef 是 createTestProject 里两个引用列的哨兵值：opts 未显式指定引用时，
// 自动创建一条明文 "gh-token" 的 Token 库记录并引用到两个用途——对应旧实现里
// 项目自带可用 Access Token 的默认行为；显式置 "" 表示不引用任何 Token。
const autoTokenRef = "auto-token"

// testEncryptionKey 与 setupTestServices 的 cfg.Encryption.Key 保持一致，
// createTestProject 无 cfg 入参时用它加密默认 Token 行。
const testEncryptionKey = "12345678901234567890123456789012"

func createTestProject(t *testing.T, db *gorm.DB, userID string, opts ...func(*model.Project)) *model.Project {
	t.Helper()

	project := &model.Project{
		ID:                  uuid.New().String(),
		UserID:              userID,
		Name:                "proj-" + uuid.NewString(),
		Description:         "test project",
		GithubOwner:         "owner",
		GithubRepo:          "repo",
		PublicGitHubTokenID: autoTokenRef,
		CodeGitHubTokenID:   autoTokenRef,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}

	for _, opt := range opts {
		opt(project)
	}

	if project.PublicGitHubTokenID == autoTokenRef || project.CodeGitHubTokenID == autoTokenRef {
		token := createTestToken(t, db, testEncryptionKey, userID, "gh-token")
		if project.PublicGitHubTokenID == autoTokenRef {
			project.PublicGitHubTokenID = token.ID
		}
		if project.CodeGitHubTokenID == autoTokenRef {
			project.CodeGitHubTokenID = token.ID
		}
	}

	if err := db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	return project
}

// createTestToken 建一条 Token 库记录（明文加密存储），返回该行供测试断言。
// 与产品语义一致：同一用户明文指纹重复时复用已有记录，不撞唯一索引。
func createTestToken(t *testing.T, db *gorm.DB, encryptionKey, userID, plaintext string) *model.UserGitHubToken {
	t.Helper()

	fingerprint := githubTokenFingerprint(plaintext)
	var existing model.UserGitHubToken
	err := db.Where("user_id = ? AND token_fingerprint = ?", userID, fingerprint).First(&existing).Error
	if err == nil {
		return &existing
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("lookup github token: %v", err)
	}

	encrypted, err := crypto.Encrypt([]byte(plaintext), []byte(encryptionKey))
	if err != nil {
		t.Fatalf("encrypt github token: %v", err)
	}
	token := &model.UserGitHubToken{
		ID:               uuid.New().String(),
		UserID:           userID,
		Title:            "token-" + plaintext,
		TokenEncrypted:   encrypted,
		TokenFingerprint: fingerprint,
		TokenPrefix:      githubTokenPrefix(plaintext),
		DetectionStatus:  model.TokenDetectionUnchecked,
		DetectionType:    model.TokenTypeUnknown,
		DetectionScopes:  "[]",
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("create github token: %v", err)
	}
	return token
}

// createCorruptTestToken 建一条密文损坏的 Token 库记录，用于解密失败路径。
func createCorruptTestToken(t *testing.T, db *gorm.DB, userID string) *model.UserGitHubToken {
	t.Helper()

	token := &model.UserGitHubToken{
		ID:               uuid.New().String(),
		UserID:           userID,
		Title:            "corrupt-token",
		TokenEncrypted:   []byte("corrupt-ciphertext"),
		TokenFingerprint: "fp-" + uuid.NewString(),
		TokenPrefix:      "corrupt-",
		DetectionStatus:  model.TokenDetectionUnchecked,
		DetectionType:    model.TokenTypeUnknown,
		DetectionScopes:  "[]",
		CreatedAt:        time.Now(),
		UpdatedAt:        time.Now(),
	}
	if err := db.Create(token).Error; err != nil {
		t.Fatalf("create corrupt github token: %v", err)
	}
	return token
}

func encryptTestToken(t *testing.T, cfg *config.Config, plaintext string) []byte {
	t.Helper()

	encrypted, err := crypto.Encrypt([]byte(plaintext), []byte(cfg.Encryption.Key))
	if err != nil {
		t.Fatalf("encrypt github token: %v", err)
	}
	return encrypted
}

func createTestUser(t *testing.T, db *gorm.DB, userID string) *model.User {
	t.Helper()

	user := &model.User{
		ID:           userID,
		Username:     "user_" + userID,
		Email:        userID + "@example.com",
		PasswordHash: "hashed",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return user
}

func createTestVersion(t *testing.T, db *gorm.DB, projectID string, opts ...func(*model.Version)) *model.Version {
	t.Helper()

	version := &model.Version{
		ID:              uuid.New().String(),
		ProjectID:       projectID,
		VersionNumber:   "v1.0.0",
		Status:          model.VersionStatusPending,
		ReleaseNotes:    "notes",
		TargetCommitish: "main",
		CreatedAt:       time.Now(),
	}

	for _, opt := range opts {
		opt(version)
	}

	if err := db.Create(version).Error; err != nil {
		t.Fatalf("create version: %v", err)
	}

	return version
}

func createTestIssue(t *testing.T, db *gorm.DB, projectID string, opts ...func(*model.Issue)) *model.Issue {
	t.Helper()

	now := time.Now().UTC()
	issue := &model.Issue{
		ID:             uuid.New().String(),
		ProjectID:      projectID,
		Source:         model.IssueSourceGitHub,
		SequenceNumber: 1,
		State:          model.IssueStateOpen,
		Title:          "Crash on launch",
		Body:           "App crashes",
		AuthorLogin:    "alice",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-1 * time.Hour),
	}

	for _, opt := range opts {
		opt(issue)
	}

	if err := db.Create(issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}
	meta := &model.IssueGitHubMeta{
		IssueID:       issue.ID,
		ProjectID:     projectID,
		GitHubIssueID: 1001,
		GitHubNodeID:  "I_kw_test",
		Number:        42,
		HTMLURL:       "https://github.com/owner/repo/issues/42",
		CommentsCount: 0,
		SyncedAt:      now,
	}
	if err := db.Create(meta).Error; err != nil {
		t.Fatalf("create issue github meta: %v", err)
	}

	return issue
}

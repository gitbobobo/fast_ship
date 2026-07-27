package service

import (
	"os"
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
	versionRepo         *repository.VersionRepository
	issueRepo           *repository.IssueRepository
	gitHubMetaRepo      *repository.IssueGitHubMetaRepository
	commentRepo         *repository.IssueCommentRepository
	timelineRepo        *repository.IssueTimelineRepository
	internalMetaRepo    *repository.IssueInternalMetaRepository
	checklistRepo       *repository.IssueChecklistRepository
	syncStateRepo       *repository.IssueSyncStateRepository
	issueAssetRepo      *repository.IssueAssetRepository
	issueDraftAssetRepo *repository.IssueDraftAssetRepository
	artifactRepo        *repository.ArtifactRepository
	collabRepo          *repository.IssueCollabRepository
	issueService        *IssueService
	collabService       *IssueCollabService
	aiService           *AIService
	issuePromptService  *IssuePromptService
	versionService      *VersionService
	artifactService     *ArtifactService
	shipService         *ShipService
}

func setupTestServices(t *testing.T) *testServices {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(
		&model.User{},
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
		&model.IssueChecklistItem{},
		&model.IssueSyncState{},
		&model.IssueAsset{},
		&model.IssueDraftAsset{},
		&model.Artifact{},
		&model.JWTBlacklist{},
		&model.GitHubRepoLabel{},
		&model.IssueCollabSuggestion{},
		&model.IssueCollabPlan{},
		&model.IssueCollabReview{},
		&model.IssueCollabSummary{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_name ON projects(user_id, name)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_versions_project_version ON versions(project_id, version_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_sequence ON issues(project_id, sequence_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_project_github_issue ON issue_github_meta(project_id, github_issue_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_github_meta_issue_id ON issue_github_meta(issue_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_comments_issue_github_comment ON issue_comments(issue_id, github_comment_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_timeline_issue_event_key ON issue_timeline_events(issue_id, event_key)")

	tempDir := t.TempDir()
	fileStorage := storage.NewLocalStorage(filepath.Join(tempDir, "uploads"))

	userRepo := repository.NewUserRepository(db)
	userAISettingRepo := repository.NewUserAISettingRepository(db)
	userIssuePromptRepo := repository.NewUserIssuePromptSettingRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	versionRepo := repository.NewVersionRepository(db)
	issueRepo := repository.NewIssueRepository(db)
	gitHubMetaRepo := repository.NewIssueGitHubMetaRepository(db)
	commentRepo := repository.NewIssueCommentRepository(db)
	timelineRepo := repository.NewIssueTimelineRepository(db)
	internalMetaRepo := repository.NewIssueInternalMetaRepository(db)
	checklistRepo := repository.NewIssueChecklistRepository(db)
	syncStateRepo := repository.NewIssueSyncStateRepository(db)
	issueAssetRepo := repository.NewIssueAssetRepository(db)
	issueDraftAssetRepo := repository.NewIssueDraftAssetRepository(db)
	artifactRepo := repository.NewArtifactRepository(db)
	collabRepo := repository.NewIssueCollabRepository(db)

	cfg := &config.Config{
		Encryption: config.EncryptionConfig{
			Key: "12345678901234567890123456789012",
		},
	}

	return &testServices{
		db:                  db,
		storage:             fileStorage,
		cfg:                 cfg,
		userAISettingRepo:   userAISettingRepo,
		userIssuePromptRepo: userIssuePromptRepo,
		projectRepo:         projectRepo,
		versionRepo:         versionRepo,
		issueRepo:           issueRepo,
		gitHubMetaRepo:      gitHubMetaRepo,
		commentRepo:         commentRepo,
		timelineRepo:        timelineRepo,
		internalMetaRepo:    internalMetaRepo,
		checklistRepo:       checklistRepo,
		syncStateRepo:       syncStateRepo,
		issueAssetRepo:      issueAssetRepo,
		issueDraftAssetRepo: issueDraftAssetRepo,
		artifactRepo:        artifactRepo,
		collabRepo:          collabRepo,
		issueService:        NewIssueService(issueRepo, gitHubMetaRepo, commentRepo, timelineRepo, internalMetaRepo, checklistRepo, syncStateRepo, issueAssetRepo, issueDraftAssetRepo, projectRepo, userRepo, repository.NewGitHubRepoLabelRepository(db), fileStorage, cfg, zap.NewNop()),
		collabService:       NewIssueCollabService(collabRepo, issueRepo, projectRepo, userRepo),
		aiService:           NewAIService(userAISettingRepo, issueRepo, commentRepo, projectRepo, cfg, zap.NewNop()),
		issuePromptService:  NewIssuePromptService(userIssuePromptRepo),
		versionService:      NewVersionService(versionRepo, projectRepo, fileStorage, cfg),
		artifactService:     NewArtifactService(artifactRepo, versionRepo, projectRepo, fileStorage),
		shipService:         NewShipService(versionRepo, projectRepo, artifactRepo, fileStorage, cfg, zap.NewNop()),
	}
}

func createTestProject(t *testing.T, db *gorm.DB, userID string, opts ...func(*model.Project)) *model.Project {
	t.Helper()

	project := &model.Project{
		ID:                   uuid.New().String(),
		UserID:               userID,
		Name:                 "proj-" + uuid.NewString(),
		Description:          "test project",
		GithubOwner:          "owner",
		GithubRepo:           "repo",
		GithubTokenEncrypted: []byte("token"),
		CreatedAt:            time.Now(),
		UpdatedAt:            time.Now(),
	}

	for _, opt := range opts {
		opt(project)
	}

	if err := db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	return project
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

func artifactFileExists(t *testing.T, baseDir, relPath string) bool {
	t.Helper()

	_, err := os.Stat(filepath.Join(baseDir, relPath))
	return err == nil
}

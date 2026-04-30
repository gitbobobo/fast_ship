package handler

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type handlerTestEnv struct {
	db              *gorm.DB
	authHandler     *AuthHandler
	aiHandler       *AIHandler
	versionHandler  *VersionHandler
	issueHandler    *IssueHandler
	artifactHandler *ArtifactHandler
}

type apiEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func setupHandlerTestEnv(t *testing.T) *handlerTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(
		&model.User{},
		&model.UserAISetting{},
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

	cfg := &config.Config{
		JWT: config.JWTConfig{
			Secret:      "test-secret",
			ExpireHours: 24,
		},
		Encryption: config.EncryptionConfig{
			Key: "12345678901234567890123456789012",
		},
	}

	fileStorage := storage.NewLocalStorage(filepath.Join(t.TempDir(), "uploads"))

	userRepo := repository.NewUserRepository(db)
	userAISettingRepo := repository.NewUserAISettingRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	versionRepo := repository.NewVersionRepository(db)
	issueRepo := repository.NewIssueRepository(db)
	issueGitHubMetaRepo := repository.NewIssueGitHubMetaRepository(db)
	issueCommentRepo := repository.NewIssueCommentRepository(db)
	issueTimelineRepo := repository.NewIssueTimelineRepository(db)
	issueInternalMetaRepo := repository.NewIssueInternalMetaRepository(db)
	issueChecklistRepo := repository.NewIssueChecklistRepository(db)
	issueSyncStateRepo := repository.NewIssueSyncStateRepository(db)
	issueAssetRepo := repository.NewIssueAssetRepository(db)
	issueDraftAssetRepo := repository.NewIssueDraftAssetRepository(db)
	artifactRepo := repository.NewArtifactRepository(db)
	jwtBlacklistRepo := repository.NewJWTBlacklistRepository(db)

	authService := service.NewAuthService(userRepo, jwtBlacklistRepo, cfg)
	aiService := service.NewAIService(userAISettingRepo, issueRepo, issueCommentRepo, projectRepo, cfg)
	versionService := service.NewVersionService(versionRepo, projectRepo, fileStorage, cfg)
	issueService := service.NewIssueService(issueRepo, issueGitHubMetaRepo, issueCommentRepo, issueTimelineRepo, issueInternalMetaRepo, issueChecklistRepo, issueSyncStateRepo, issueAssetRepo, issueDraftAssetRepo, projectRepo, userRepo, fileStorage, cfg, zap.NewNop())
	artifactService := service.NewArtifactService(artifactRepo, versionRepo, projectRepo, fileStorage)
	shipService := service.NewShipService(versionRepo, projectRepo, artifactRepo, fileStorage, cfg, zap.NewNop())

	return &handlerTestEnv{
		db:              db,
		authHandler:     NewAuthHandler(authService),
		aiHandler:       NewAIHandler(aiService),
		versionHandler:  NewVersionHandler(versionService, shipService),
		issueHandler:    NewIssueHandler(issueService),
		artifactHandler: NewArtifactHandler(artifactService),
	}
}

func createHandlerTestUser(t *testing.T, db *gorm.DB, id string) *model.User {
	t.Helper()

	user := &model.User{
		ID:           id,
		Username:     "user_" + id,
		Email:        id + "@example.com",
		PasswordHash: "hashed",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	return user
}

func createHandlerTestProject(t *testing.T, db *gorm.DB, userID string, opts ...func(*model.Project)) *model.Project {
	t.Helper()

	project := &model.Project{
		ID:                   uuid.NewString(),
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

func createHandlerTestVersion(t *testing.T, db *gorm.DB, projectID string, opts ...func(*model.Version)) *model.Version {
	t.Helper()

	version := &model.Version{
		ID:              uuid.NewString(),
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

func createHandlerTestIssue(t *testing.T, db *gorm.DB, projectID string, opts ...func(*model.Issue)) *model.Issue {
	t.Helper()

	now := time.Now().UTC()
	issue := &model.Issue{
		ID:             uuid.NewString(),
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
		GitHubNodeID:  "I_kw_handler",
		Number:        42,
		HTMLURL:       "https://github.com/owner/repo/issues/42",
		SyncedAt:      now,
	}
	if err := db.Create(meta).Error; err != nil {
		t.Fatalf("create issue github meta: %v", err)
	}

	return issue
}

func newJSONContext(method, target string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(method, target, bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	return ctx, rec
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder, target any) apiEnvelope {
	t.Helper()

	var envelope apiEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}

	if target != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, target); err != nil {
			t.Fatalf("decode data: %v", err)
		}
	}

	return envelope
}

func newMultipartUploadRequest(t *testing.T, target, fieldName, fileName string, content []byte, extra map[string]string) (*http.Request, string) {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}

	if _, err := part.Write(content); err != nil {
		t.Fatalf("write form file: %v", err)
	}

	for key, value := range extra {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatalf("write field: %v", err)
		}
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, body.String()
}

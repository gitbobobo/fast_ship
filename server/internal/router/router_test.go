package router

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/handler"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/godbobo/fast_ship/server/internal/service"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type routerTestEnv struct {
	router     *gin.Engine
	db         *gorm.DB
	apiKeyRepo *repository.ApiKeyRepository
}

type routerEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func TestRouterAuthRegisterLoginAndMe(t *testing.T) {
	env := setupRouterTestEnv(t)

	registerBody := []byte(`{"username":"tester","email":"tester@example.com","password":"Password123"}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader(registerBody))
	registerReq.Header.Set("Content-Type", "application/json")

	registerRec := httptest.NewRecorder()
	env.router.ServeHTTP(registerRec, registerReq)

	if registerRec.Code != http.StatusOK {
		t.Fatalf("expected register 200, got %d: %s", registerRec.Code, registerRec.Body.String())
	}

	var registerResp struct {
		Token string `json:"token"`
		User  struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Email    string `json:"email"`
		} `json:"user"`
	}
	decodeRouterEnvelope(t, registerRec, &registerResp)
	if registerResp.Token == "" {
		t.Fatalf("expected token in register response")
	}

	loginBody := []byte(`{"login":"tester@example.com","password":"Password123"}`)
	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")

	loginRec := httptest.NewRecorder()
	env.router.ServeHTTP(loginRec, loginReq)

	if loginRec.Code != http.StatusOK {
		t.Fatalf("expected login 200, got %d: %s", loginRec.Code, loginRec.Body.String())
	}

	var loginResp struct {
		Token string `json:"token"`
		User  struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"user"`
	}
	decodeRouterEnvelope(t, loginRec, &loginResp)
	if loginResp.Token == "" {
		t.Fatalf("expected token in login response")
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+loginResp.Token)
	meRec := httptest.NewRecorder()
	env.router.ServeHTTP(meRec, meReq)

	if meRec.Code != http.StatusOK {
		t.Fatalf("expected me 200, got %d: %s", meRec.Code, meRec.Body.String())
	}

	var meResp struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	decodeRouterEnvelope(t, meRec, &meResp)
	if meResp.ID != registerResp.User.ID || meResp.Username != "tester" {
		t.Fatalf("unexpected /me response: %+v", meResp)
	}
}

func TestRouterShipCheckRequiresJWT(t *testing.T) {
	env := setupRouterTestEnv(t)
	auth := registerAndLoginRouterUser(t, env.router, "shiptester", "shiptester@example.com", "Password123")
	project := createRouterTestProject(t, env.db, auth.UserID, func(p *model.Project) {
		p.GithubOwner = ""
		p.GithubRepo = ""
		p.GithubTokenEncrypted = []byte{}
	})
	version := createRouterTestVersion(t, env.db, project.ID, func(v *model.Version) {
		v.ReleaseNotes = ""
		v.TargetCommitish = ""
	})

	rawKey := "RAWAPIKEY1234567890"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    auth.UserID,
		Name:      "CI-Android",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	apiKeyReq := httptest.NewRequest(http.MethodGet, "/api/versions/"+version.ID+"/ship-check", nil)
	apiKeyReq.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))
	apiKeyRec := httptest.NewRecorder()
	env.router.ServeHTTP(apiKeyRec, apiKeyReq)

	if apiKeyRec.Code != http.StatusForbidden {
		t.Fatalf("expected API key request 403, got %d: %s", apiKeyRec.Code, apiKeyRec.Body.String())
	}

	jwtReq := httptest.NewRequest(http.MethodGet, "/api/versions/"+version.ID+"/ship-check", nil)
	jwtReq.Header.Set("Authorization", "Bearer "+auth.Token)
	jwtRec := httptest.NewRecorder()
	env.router.ServeHTTP(jwtRec, jwtReq)

	if jwtRec.Code != http.StatusOK {
		t.Fatalf("expected JWT request 200, got %d: %s", jwtRec.Code, jwtRec.Body.String())
	}

	var resp struct {
		CanShip bool `json:"can_ship"`
		Items   []struct {
			Key string `json:"key"`
			OK  bool   `json:"ok"`
		} `json:"items"`
	}
	decodeRouterEnvelope(t, jwtRec, &resp)
	if resp.CanShip {
		t.Fatalf("expected can_ship=false")
	}
	if len(resp.Items) != 4 {
		t.Fatalf("expected 4 check items, got %d", len(resp.Items))
	}
}

func TestRouterArtifactUploadAndDownloadWithAPIKey(t *testing.T) {
	env := setupRouterTestEnv(t)
	user := createRouterTestUser(t, env.db, "user-2", "builder", "builder@example.com")
	project := createRouterTestProject(t, env.db, user.ID)
	version := createRouterTestVersion(t, env.db, project.ID)

	rawKey := "RAWAPIKEY0987654321"
	if err := env.apiKeyRepo.Create(&model.ApiKey{
		ID:        uuid.NewString(),
		UserID:    user.ID,
		Name:      "CI-Upload",
		KeyPrefix: rawKey[:8],
		KeyHash:   service.HashApiKey(rawKey),
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	uploadReq := newRouterMultipartRequest(t, "/api/versions/"+version.ID+"/artifacts", "file", "app.apk", []byte("artifact-binary"), map[string]string{
		"platform": "android",
	})
	uploadReq.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))

	uploadRec := httptest.NewRecorder()
	env.router.ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusOK {
		t.Fatalf("expected upload 200, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	var artifact struct {
		ID         string `json:"id"`
		UploadedBy string `json:"uploaded_by"`
		Platform   string `json:"platform"`
	}
	decodeRouterEnvelope(t, uploadRec, &artifact)
	if artifact.UploadedBy != "API Key: CI-Upload" {
		t.Fatalf("expected uploaded_by from API key, got %q", artifact.UploadedBy)
	}

	downloadReq := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/download", nil)
	downloadReq.Header.Set("Authorization", "Bearer "+service.FormatApiKey(rawKey))
	downloadRec := httptest.NewRecorder()
	env.router.ServeHTTP(downloadRec, downloadReq)

	if downloadRec.Code != http.StatusOK {
		t.Fatalf("expected download 200, got %d: %s", downloadRec.Code, downloadRec.Body.String())
	}
	if downloadRec.Body.String() != "artifact-binary" {
		t.Fatalf("unexpected download body %q", downloadRec.Body.String())
	}
	if disposition := downloadRec.Header().Get("Content-Disposition"); !strings.Contains(disposition, "app.apk") {
		t.Fatalf("expected filename in content disposition, got %q", disposition)
	}
}

func setupRouterTestEnv(t *testing.T) *routerTestEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}

	if err := db.AutoMigrate(
		&model.User{},
		&model.ApiKey{},
		&model.Project{},
		&model.Version{},
		&model.Issue{},
		&model.IssueComment{},
		&model.IssueTimelineEvent{},
		&model.IssueInternalMeta{},
		&model.IssueSyncState{},
		&model.Artifact{},
		&model.JWTBlacklist{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_name ON projects(user_id, name)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_versions_project_version ON versions(project_id, version_number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_number ON issues(project_id, number)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issues_project_github_issue ON issues(project_id, github_issue_id)")
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
	apiKeyRepo := repository.NewApiKeyRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	versionRepo := repository.NewVersionRepository(db)
	issueRepo := repository.NewIssueRepository(db)
	issueCommentRepo := repository.NewIssueCommentRepository(db)
	issueTimelineRepo := repository.NewIssueTimelineRepository(db)
	issueInternalMetaRepo := repository.NewIssueInternalMetaRepository(db)
	issueSyncStateRepo := repository.NewIssueSyncStateRepository(db)
	artifactRepo := repository.NewArtifactRepository(db)
	jwtBlacklistRepo := repository.NewJWTBlacklistRepository(db)

	authService := service.NewAuthService(userRepo, jwtBlacklistRepo, cfg)
	apiKeyService := service.NewApiKeyService(apiKeyRepo)
	projectService := service.NewProjectService(projectRepo, versionRepo, issueSyncStateRepo, fileStorage, cfg)
	versionService := service.NewVersionService(versionRepo, projectRepo, fileStorage)
	issueService := service.NewIssueService(issueRepo, issueCommentRepo, issueTimelineRepo, issueInternalMetaRepo, issueSyncStateRepo, projectRepo, cfg, zap.NewNop())
	artifactService := service.NewArtifactService(artifactRepo, versionRepo, projectRepo, fileStorage)
	shipService := service.NewShipService(versionRepo, projectRepo, artifactRepo, fileStorage, cfg, zap.NewNop())

	authHandler := handler.NewAuthHandler(authService)
	apiKeyHandler := handler.NewApiKeyHandler(apiKeyService)
	projectHandler := handler.NewProjectHandler(projectService)
	versionHandler := handler.NewVersionHandler(versionService, shipService)
	issueHandler := handler.NewIssueHandler(issueService)
	artifactHandler := handler.NewArtifactHandler(artifactService)

	r := gin.New()
	Setup(r, cfg, authHandler, apiKeyHandler, projectHandler, versionHandler, issueHandler, artifactHandler, authService, apiKeyRepo)

	return &routerTestEnv{
		router:     r,
		db:         db,
		apiKeyRepo: apiKeyRepo,
	}
}

func createRouterTestUser(t *testing.T, db *gorm.DB, id, username, email string) *model.User {
	t.Helper()
	user := &model.User{
		ID:           id,
		Username:     username,
		Email:        email,
		PasswordHash: "hashed",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user
}

func createRouterTestProject(t *testing.T, db *gorm.DB, userID string, opts ...func(*model.Project)) *model.Project {
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

func createRouterTestVersion(t *testing.T, db *gorm.DB, projectID string, opts ...func(*model.Version)) *model.Version {
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

func decodeRouterEnvelope(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	var env routerEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if env.Code != 0 {
		t.Fatalf("expected success envelope, got code=%d body=%s", env.Code, rec.Body.String())
	}
	if target != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, target); err != nil {
			t.Fatalf("decode envelope data: %v", err)
		}
	}
}

type routerAuthResult struct {
	UserID string
	Token  string
}

func registerAndLoginRouterUser(t *testing.T, r *gin.Engine, username, email, password string) routerAuthResult {
	t.Helper()

	registerBody := []byte(`{"username":"` + username + `","email":"` + email + `","password":"` + password + `"}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/auth/register", bytes.NewReader(registerBody))
	registerReq.Header.Set("Content-Type", "application/json")
	registerRec := httptest.NewRecorder()
	r.ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusOK {
		t.Fatalf("register failed: %d %s", registerRec.Code, registerRec.Body.String())
	}

	var registerResp struct {
		Token string `json:"token"`
		User  struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	decodeRouterEnvelope(t, registerRec, &registerResp)

	loginBody := []byte(`{"login":"` + email + `","password":"` + password + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	decodeRouterEnvelope(t, rec, &resp)
	return routerAuthResult{
		UserID: registerResp.User.ID,
		Token:  resp.Token,
	}
}

func newRouterMultipartRequest(t *testing.T, target, fieldName, fileName string, content []byte, extra map[string]string) *http.Request {
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
		t.Fatalf("close multipart: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, target, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

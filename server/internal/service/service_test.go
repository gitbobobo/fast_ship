package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/storage"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/glebarez/sqlite"
)

type testServices struct {
	db              *gorm.DB
	storage         storage.Storage
	projectRepo     *repository.ProjectRepository
	versionRepo     *repository.VersionRepository
	artifactRepo    *repository.ArtifactRepository
	versionService  *VersionService
	artifactService *ArtifactService
	shipService     *ShipService
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
		&model.ApiKey{},
		&model.Project{},
		&model.Version{},
		&model.Artifact{},
		&model.JWTBlacklist{},
	); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}

	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_user_name ON projects(user_id, name)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_versions_project_version ON versions(project_id, version_number)")

	tempDir := t.TempDir()
	fileStorage := storage.NewLocalStorage(filepath.Join(tempDir, "uploads"))

	projectRepo := repository.NewProjectRepository(db)
	versionRepo := repository.NewVersionRepository(db)
	artifactRepo := repository.NewArtifactRepository(db)

	cfg := &config.Config{
		Encryption: config.EncryptionConfig{
			Key: "12345678901234567890123456789012",
		},
	}

	return &testServices{
		db:              db,
		storage:         fileStorage,
		projectRepo:     projectRepo,
		versionRepo:     versionRepo,
		artifactRepo:    artifactRepo,
		versionService:  NewVersionService(versionRepo, projectRepo, fileStorage),
		artifactService: NewArtifactService(artifactRepo, versionRepo, projectRepo, fileStorage),
		shipService:     NewShipService(versionRepo, projectRepo, artifactRepo, fileStorage, cfg, zap.NewNop()),
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

func artifactFileExists(t *testing.T, baseDir, relPath string) bool {
	t.Helper()

	_, err := os.Stat(filepath.Join(baseDir, relPath))
	return err == nil
}

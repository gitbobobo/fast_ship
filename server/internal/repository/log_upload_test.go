package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupLogRepoTest(t *testing.T) (*LogRepository, string) {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.LogBatch{}, &model.LogEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_log_batches_project_run ON log_batches(project_id, run_id)")

	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now()
	if err := db.Create(&model.User{ID: userID, Username: "u", Email: "u@example.com", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.Project{ID: projectID, UserID: userID, Name: "p", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	return NewLogRepository(db), projectID
}

func TestUploadBatchTx_UniqueConflictUsesExistingWithoutOverwritingDescription(t *testing.T) {
	repo, projectID := setupLogRepoTest(t)
	ts := time.Now().UTC()

	first, err := repo.UploadBatchTx(projectID, "run-conflict", "smux", "keep-me", nil, []model.LogEntry{
		{Timestamp: ts, Level: "info", Message: "a", CreatedAt: ts},
	})
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}

	second, err := repo.UploadBatchTx(projectID, "run-conflict", "smux", "overwrite?", nil, []model.LogEntry{
		{Timestamp: ts.Add(time.Second), Level: "info", Message: "b", CreatedAt: ts},
	})
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same batch id, got %s vs %s", second.ID, first.ID)
	}
	if second.Description != "keep-me" {
		t.Fatalf("description overwritten: %q", second.Description)
	}
	if second.EntryCount != 2 {
		t.Fatalf("expected 2 entries, got %d", second.EntryCount)
	}
}

package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupLogRepoTest(t *testing.T) (*LogRepository, *gorm.DB, string) {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.LogRun{}, &model.LogRunChunk{}, &model.LogEntry{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE INDEX IF NOT EXISTS idx_log_entries_run_timestamp ON log_entries(log_run_id, timestamp ASC)")

	userID := uuid.NewString()
	projectID := uuid.NewString()
	now := time.Now()
	if err := db.Create(&model.User{ID: userID, Username: "u", Email: "u@example.com", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.Project{ID: projectID, UserID: userID, Name: "p", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	return NewLogRepository(db), db, projectID
}

func TestUploadRunTx_UniqueConflictUsesExistingWithoutOverwritingDescription(t *testing.T) {
	repo, _, projectID := setupLogRepoTest(t)
	ts := time.Now().UTC()

	first, err := repo.UploadRunTx(projectID, "run-conflict", "chunk-1", "smux", "keep-me", nil, []model.LogEntry{
		{Timestamp: ts, Level: "info", Message: "a", CreatedAt: ts},
	})
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}

	second, err := repo.UploadRunTx(projectID, "run-conflict", "chunk-2", "smux", "overwrite?", nil, []model.LogEntry{
		{Timestamp: ts.Add(time.Second), Level: "info", Message: "b", CreatedAt: ts},
	})
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if second.Run.ID != first.Run.ID {
		t.Fatalf("expected same run id, got %s vs %s", second.Run.ID, first.Run.ID)
	}
	if second.Run.Description != "keep-me" {
		t.Fatalf("description overwritten: %q", second.Run.Description)
	}
	if second.Run.EntryCount != 2 {
		t.Fatalf("expected 2 entries, got %d", second.Run.EntryCount)
	}
}

func TestUploadRunTx_DuplicateChunk(t *testing.T) {
	repo, db, projectID := setupLogRepoTest(t)
	ts := time.Now().UTC()

	first, err := repo.UploadRunTx(projectID, "run-dup", "chunk-a", "", "", nil, []model.LogEntry{
		{Timestamp: ts, Level: "info", Message: "a", CreatedAt: ts},
	})
	if err != nil {
		t.Fatalf("first upload: %v", err)
	}

	dup, err := repo.UploadRunTx(projectID, "run-dup", "chunk-a", "", "", nil, []model.LogEntry{
		{Timestamp: ts.Add(time.Second), Level: "info", Message: "b", CreatedAt: ts},
	})
	if err != nil {
		t.Fatalf("duplicate upload: %v", err)
	}
	if !dup.Duplicate || dup.AcceptedCount != 0 {
		t.Fatalf("expected duplicate chunk, got duplicate=%v accepted=%d", dup.Duplicate, dup.AcceptedCount)
	}
	if dup.Run.EntryCount != first.Run.EntryCount {
		t.Fatalf("entry count changed: before=%d after=%d", first.Run.EntryCount, dup.Run.EntryCount)
	}

	var entryCount int64
	if err := db.Model(&model.LogEntry{}).Count(&entryCount).Error; err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if entryCount != 1 {
		t.Fatalf("expected 1 entry row, got %d", entryCount)
	}
}

func TestUploadRunTx_EntryLimitRollback(t *testing.T) {
	repo, db, projectID := setupLogRepoTest(t)
	ts := time.Now().UTC()

	if _, err := repo.UploadRunTx(projectID, "run-limit", "chunk-1", "", "", nil, []model.LogEntry{
		{Timestamp: ts, Level: "info", Message: "seed", CreatedAt: ts},
	}); err != nil {
		t.Fatalf("seed upload: %v", err)
	}

	if err := db.Model(&model.LogRun{}).Where("run_id = ?", "run-limit").
		Update("entry_count", MaxLogEntriesPerRun).Error; err != nil {
		t.Fatalf("set entry_count: %v", err)
	}

	_, err := repo.UploadRunTx(projectID, "run-limit", "chunk-2", "", "", nil, []model.LogEntry{
		{Timestamp: ts.Add(time.Second), Level: "info", Message: "overflow", CreatedAt: ts},
	})
	if err != ErrLogRunEntryLimitExceeded {
		t.Fatalf("expected entry limit error, got %v", err)
	}

	var run model.LogRun
	if err := db.Where("run_id = ?", "run-limit").First(&run).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.EntryCount != MaxLogEntriesPerRun {
		t.Fatalf("entry_count should stay %d, got %d", MaxLogEntriesPerRun, run.EntryCount)
	}

	var chunkCount int64
	if err := db.Model(&model.LogRunChunk{}).Where("chunk_id = ?", "chunk-2").Count(&chunkCount).Error; err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if chunkCount != 0 {
		t.Fatalf("overflow chunk should be rolled back, got %d rows", chunkCount)
	}
}

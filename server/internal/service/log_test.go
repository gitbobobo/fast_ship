package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupLogServiceTest(t *testing.T) (*LogService, *gorm.DB, string, string) {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
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
	if err := db.Create(&model.User{ID: userID, Username: "loguser", Email: "log@example.com", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.Create(&model.Project{ID: projectID, UserID: userID, Name: "logproj", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}

	logRepo := repository.NewLogRepository(db)
	projectRepo := repository.NewProjectRepository(db)
	return NewLogService(logRepo, projectRepo), db, userID, projectID
}

func TestLogService_UploadAndList(t *testing.T) {
	svc, _, userID, projectID := setupLogServiceTest(t)
	keyID := uuid.NewString()

	ts := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	res, err := svc.UploadLogs(projectID, userID, &keyID, &UploadLogsRequest{
		RunID:  "run-1",
		Source: "smux",
		Entries: []LogEntryInput{
			{Timestamp: ts, Level: "info", Source: "phase-1", Message: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if res.AcceptedCount != 1 || res.EntryCount != 1 {
		t.Fatalf("unexpected upload result: %+v", res)
	}

	res2, err := svc.UploadLogs(projectID, userID, &keyID, &UploadLogsRequest{
		RunID:  "run-1",
		Source: "smux",
		Entries: []LogEntryInput{
			{Timestamp: ts.Add(time.Minute), Level: "error", Message: "fail"},
		},
	})
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if res2.EntryCount != 2 {
		t.Fatalf("expected merged count 2, got %d", res2.EntryCount)
	}

	items, total, err := svc.ListEntries(projectID, userID, ListLogEntriesRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2 entries, got total=%d len=%d", total, len(items))
	}
}

func TestLogService_InvalidLevelRejected(t *testing.T) {
	svc, _, userID, projectID := setupLogServiceTest(t)

	_, err := svc.UploadLogs(projectID, userID, nil, &UploadLogsRequest{
		RunID: "run-1",
		Entries: []LogEntryInput{
			{Timestamp: time.Now(), Level: "trace", Message: "x"},
		},
	})
	if err != errs.ErrInvalidParams {
		t.Fatalf("expected invalid params, got %v", err)
	}
}

func TestLogService_CrossUserDenied(t *testing.T) {
	svc, db, userID, projectID := setupLogServiceTest(t)
	otherID := uuid.NewString()
	now := time.Now()
	if err := db.Create(&model.User{ID: otherID, Username: "other", Email: "other@example.com", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	_, _, err := svc.ListEntries(projectID, otherID, ListLogEntriesRequest{Page: 1, PageSize: 10})
	if err != errs.ErrProjectNotFound {
		t.Fatalf("expected project not found, got %v", err)
	}

	_, err = svc.UploadLogs(projectID, userID, nil, &UploadLogsRequest{
		RunID: "run-x",
		Entries: []LogEntryInput{
			{Timestamp: time.Now(), Level: "info", Message: "owned"},
		},
	})
	if err != nil {
		t.Fatalf("upload for owner: %v", err)
	}
}

func TestLogService_DeleteBatch(t *testing.T) {
	svc, _, userID, projectID := setupLogServiceTest(t)

	res, err := svc.UploadLogs(projectID, userID, nil, &UploadLogsRequest{
		RunID: "run-del",
		Entries: []LogEntryInput{
			{Timestamp: time.Now(), Level: "info", Message: "to delete"},
		},
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	if err := svc.DeleteBatch(res.ID, userID); err != nil {
		t.Fatalf("delete batch: %v", err)
	}

	_, total, err := svc.ListEntries(projectID, userID, ListLogEntriesRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if total != 0 {
		t.Fatalf("expected 0 entries after delete, got %d", total)
	}
}

func TestLogService_DeleteBatch_CrossUserDenied(t *testing.T) {
	svc, db, userID, projectID := setupLogServiceTest(t)
	otherID := uuid.NewString()
	now := time.Now()
	if err := db.Create(&model.User{ID: otherID, Username: "other2", Email: "other2@example.com", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	res, err := svc.UploadLogs(projectID, userID, nil, &UploadLogsRequest{
		RunID: "run-del-x",
		Entries: []LogEntryInput{
			{Timestamp: time.Now(), Level: "info", Message: "protected"},
		},
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	if err := svc.DeleteBatch(res.ID, otherID); err != errs.ErrProjectNotFound {
		t.Fatalf("expected project not found, got %v", err)
	}
}

func TestLogService_InvalidSourceRejected(t *testing.T) {
	svc, _, userID, projectID := setupLogServiceTest(t)
	longSource := string(make([]byte, maxLogSourceBytes+1))

	_, err := svc.UploadLogs(projectID, userID, nil, &UploadLogsRequest{
		RunID:  "run-1",
		Source: longSource,
		Entries: []LogEntryInput{
			{Timestamp: time.Now(), Level: "info", Message: "x"},
		},
	})
	if err != errs.ErrInvalidParams {
		t.Fatalf("expected invalid params for batch source, got %v", err)
	}
}

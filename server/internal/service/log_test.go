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

	if err := svc.DeleteBatch(res.ID, otherID); err != errs.ErrLogBatchNotFound {
		t.Fatalf("expected log batch not found, got %v", err)
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

func TestLogService_DescriptionCreateAndPreserve(t *testing.T) {
	svc, _, userID, projectID := setupLogServiceTest(t)
	ts := time.Now().UTC()

	res, err := svc.UploadLogs(projectID, userID, nil, &UploadLogsRequest{
		RunID:       "run-desc",
		Description: "first note",
		Entries: []LogEntryInput{
			{Timestamp: ts, Level: "info", Message: "a"},
		},
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if res.Description != "first note" {
		t.Fatalf("expected description first note, got %q", res.Description)
	}

	res2, err := svc.UploadLogs(projectID, userID, nil, &UploadLogsRequest{
		RunID:       "run-desc",
		Description: "should not overwrite",
		Entries: []LogEntryInput{
			{Timestamp: ts.Add(time.Second), Level: "info", Message: "b"},
		},
	})
	if err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if res2.EntryCount != 2 {
		t.Fatalf("expected entry count 2, got %d", res2.EntryCount)
	}
	if res2.Description != "first note" {
		t.Fatalf("description overwritten: %q", res2.Description)
	}

	batch, err := svc.GetBatch(res.ID, userID)
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if batch.Description != "first note" {
		t.Fatalf("get batch description: %q", batch.Description)
	}

	items, total, err := svc.ListBatches(projectID, userID, ListLogBatchesRequest{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("list batches: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].Description != "first note" {
		t.Fatalf("list batches unexpected: total=%d items=%+v", total, items)
	}
}

func TestLogService_DescriptionTooLongRejected(t *testing.T) {
	svc, _, userID, projectID := setupLogServiceTest(t)
	longDesc := string(make([]byte, maxLogDescriptionBytes+1))

	_, err := svc.UploadLogs(projectID, userID, nil, &UploadLogsRequest{
		RunID:       "run-long-desc",
		Description: longDesc,
		Entries: []LogEntryInput{
			{Timestamp: time.Now(), Level: "info", Message: "x"},
		},
	})
	if err != errs.ErrInvalidParams {
		t.Fatalf("expected invalid params, got %v", err)
	}
}

func TestLogService_GetBatch_NotFoundAndCrossUser(t *testing.T) {
	svc, db, userID, projectID := setupLogServiceTest(t)
	otherID := uuid.NewString()
	now := time.Now()
	if err := db.Create(&model.User{ID: otherID, Username: "other3", Email: "other3@example.com", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create other user: %v", err)
	}

	if _, err := svc.GetBatch(uuid.NewString(), userID); err != errs.ErrLogBatchNotFound {
		t.Fatalf("expected not found, got %v", err)
	}

	res, err := svc.UploadLogs(projectID, userID, nil, &UploadLogsRequest{
		RunID: "run-get-x",
		Entries: []LogEntryInput{
			{Timestamp: time.Now(), Level: "info", Message: "x"},
		},
	})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	if _, err := svc.GetBatch(res.ID, otherID); err != errs.ErrLogBatchNotFound {
		t.Fatalf("expected log batch not found for cross user, got %v", err)
	}
}

func TestLogService_DescriptionConcurrentCreate(t *testing.T) {
	svc, _, userID, projectID := setupLogServiceTest(t)
	ts := time.Now().UTC()

	type result struct {
		res *UploadLogsResult
		err error
	}
	ch := make(chan result, 2)
	for i, desc := range []string{"desc-a", "desc-b"} {
		go func(i int, desc string) {
			res, err := svc.UploadLogs(projectID, userID, nil, &UploadLogsRequest{
				RunID:       "run-concurrent",
				Description: desc,
				Entries: []LogEntryInput{
					{Timestamp: ts.Add(time.Duration(i) * time.Second), Level: "info", Message: desc},
				},
			})
			ch <- result{res: res, err: err}
		}(i, desc)
	}

	var results []result
	for i := 0; i < 2; i++ {
		results = append(results, <-ch)
	}
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("concurrent upload failed: %v", r.err)
		}
	}

	batch, err := svc.GetBatch(results[0].res.ID, userID)
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if batch.Description != "desc-a" && batch.Description != "desc-b" {
		t.Fatalf("unexpected description %q", batch.Description)
	}
	if results[0].res.Description != batch.Description || results[1].res.Description != batch.Description {
		t.Fatalf("response descriptions drifted: %q %q final=%q",
			results[0].res.Description, results[1].res.Description, batch.Description)
	}
	if batch.EntryCount != 2 {
		t.Fatalf("expected 2 entries after concurrent upload, got %d", batch.EntryCount)
	}
}


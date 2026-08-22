package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupIssueShipHookTestDB(t *testing.T) (*gorm.DB, *IssueShipHookRepository) {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssueShipHook{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, NewIssueShipHookRepository(db)
}

func createIssueShipHookTestIssue(t *testing.T, db *gorm.DB, projectID string) *model.Issue {
	t.Helper()

	now := time.Now().UTC()
	issue := &model.Issue{
		ID:             uuid.NewString(),
		ProjectID:      projectID,
		Source:         model.IssueSourceInternal,
		SequenceNumber: 1,
		State:          model.IssueStateOpen,
		Title:          "hook target",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}
	return issue
}

func TestIssueShipHookRepository_ClaimAndCompleteDoesNotOverwriteReschedule(t *testing.T) {
	db, repo := setupIssueShipHookTestDB(t)
	now := time.Now().UTC()
	projectID := uuid.NewString()
	userID := uuid.NewString()
	if err := db.Create(&model.User{ID: userID, Username: "u", Email: userID + "@x", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&model.Project{ID: projectID, UserID: userID, Name: "p", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	issue := createIssueShipHookTestIssue(t, db, projectID)
	seed := &model.IssueShipHook{
		IssueID: issue.ID, ProjectID: issue.ProjectID, Status: model.IssueShipHookStatusPending,
		CommentEnabled: true, CommentBody: "old", CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(seed).Error; err != nil {
		t.Fatalf("seed hook: %v", err)
	}

	claimed, err := repo.ClaimPendingByProjectID(issue.ProjectID, "version-a", "1.0.0", "url-a", now.Add(time.Minute), now)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim hook: %v, %+v", err, claimed)
	}
	old := claimed[0]
	old.FiredVersionID = "old-version"
	old.FiredAt = ptrTime(now.Add(2 * time.Minute))

	// A user reschedules while the worker is performing its external action.
	if err := repo.Upsert(&model.IssueShipHook{
		IssueID: issue.ID, ProjectID: issue.ProjectID, Status: model.IssueShipHookStatusPending,
		WorkflowEnabled: true, WorkflowStatus: model.IssueWorkflowStatusDone,
		UpdatedAt: now.Add(3 * time.Minute), CreatedAt: now,
	}); err != nil {
		t.Fatalf("reschedule hook: %v", err)
	}
	if err := repo.CompleteExecution(&old, now.Add(4*time.Minute)); err != ErrStaleIssueShipHook {
		t.Fatalf("expected stale completion error, got %v", err)
	}
	stored, err := repo.GetByIssueID(issue.ID)
	if err != nil {
		t.Fatalf("load rescheduled hook: %v", err)
	}
	if stored.Status != model.IssueShipHookStatusPending || !stored.WorkflowEnabled || stored.FiredVersionID != "" {
		t.Fatalf("stale worker overwrote rescheduled hook: %+v", stored)
	}
}

func TestIssueShipHookRepository_ClaimReclaimsAbandonedRunning(t *testing.T) {
	db, repo := setupIssueShipHookTestDB(t)
	now := time.Now().UTC()
	projectID := uuid.NewString()
	userID := uuid.NewString()
	if err := db.Create(&model.User{ID: userID, Username: "u", Email: userID + "@x", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&model.Project{ID: projectID, UserID: userID, Name: "p", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	issue := createIssueShipHookTestIssue(t, db, projectID)
	if err := db.Create(&model.IssueShipHook{
		IssueID: issue.ID, ProjectID: issue.ProjectID, Status: model.IssueShipHookStatusRunning,
		ExecutionToken: "dead-worker", FiredVersionID: "version-a", FiredVersionNumber: "1.0.0", FiredReleaseURL: "url-a",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed abandoned hook: %v", err)
	}
	claimed, err := repo.ClaimPendingByProjectID(issue.ProjectID, "version-a", "1.0.0", "url-a", now, now.Add(-5*time.Minute))
	if err != nil || len(claimed) != 1 || claimed[0].ExecutionToken != "dead-worker" {
		t.Fatalf("expected abandoned hook to be reclaimed with stable token, err=%v claimed=%+v", err, claimed)
	}
	second, err := repo.ClaimPendingByProjectID(issue.ProjectID, "version-b", "2.0.0", "url-b", now.Add(time.Minute), now.Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("running hook was claimed twice: %+v", second)
	}
	if claimed[0].FiredVersionID != "version-a" || claimed[0].FiredVersionNumber != "1.0.0" {
		t.Fatalf("reclaim changed original version identity: %+v", claimed[0])
	}
}

func TestIssueShipHookRepository_LeaseFencesOldWorker(t *testing.T) {
	db, repo := setupIssueShipHookTestDB(t)
	now := time.Now().UTC()
	projectID := uuid.NewString()
	userID := uuid.NewString()
	if err := db.Create(&model.User{ID: userID, Username: "u", Email: userID + "@x", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&model.Project{ID: projectID, UserID: userID, Name: "p", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	issue := createIssueShipHookTestIssue(t, db, projectID)
	if err := db.Create(&model.IssueShipHook{IssueID: issue.ID, ProjectID: projectID, Status: model.IssueShipHookStatusPending, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed hook: %v", err)
	}
	a, err := repo.ClaimPendingByProjectID(projectID, "version-a", "1.0.0", "url-a", now, now.Add(-time.Minute))
	if err != nil || len(a) != 1 {
		t.Fatalf("claim A: %v %+v", err, a)
	}
	old := a[0]
	b, err := repo.ClaimPendingByProjectID(projectID, "version-b", "2.0.0", "url-b", now.Add(31*time.Minute), now.Add(30*time.Minute))
	if err != nil || len(b) != 1 {
		t.Fatalf("claim B: %v %+v", err, b)
	}
	if old.ExecutionToken != b[0].ExecutionToken || old.LeaseToken == b[0].LeaseToken {
		t.Fatalf("expected stable execution id and rotated lease: old=%+v new=%+v", old, b[0])
	}
	if err := repo.CompleteExecution(&old, now.Add(32*time.Minute)); err != ErrStaleIssueShipHook {
		t.Fatalf("old worker completed after fencing: %v", err)
	}
	if err := repo.CompleteExecution(&b[0], now.Add(32*time.Minute)); err != nil {
		t.Fatalf("new worker completion: %v", err)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

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

func TestIssueShipHookRepository_ConsumePendingByProjectID(t *testing.T) {
	db, repo := setupIssueShipHookTestDB(t)

	userID := uuid.NewString()
	projectA := uuid.NewString()
	projectB := uuid.NewString()
	now := time.Now().UTC()

	if err := db.Create(&model.User{
		ID: userID, Username: "u", Email: "u@example.com", PasswordHash: "x", CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	for _, projectID := range []string{projectA, projectB} {
		if err := db.Create(&model.Project{
			ID: projectID, UserID: userID, Name: "p-" + projectID, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("create project: %v", err)
		}
	}

	issueA1 := createIssueShipHookTestIssue(t, db, projectA)
	issueA2 := createIssueShipHookTestIssue(t, db, projectA)
	issueB1 := createIssueShipHookTestIssue(t, db, projectB)

	seedPending := func(issueID, projectID string) {
		t.Helper()
		if err := db.Create(&model.IssueShipHook{
			IssueID:         issueID,
			ProjectID:       projectID,
			Status:          model.IssueShipHookStatusPending,
			WorkflowEnabled: true,
			WorkflowStatus:  model.IssueWorkflowStatusDone,
			CreatedByUserID: userID,
			UpdatedByUserID: userID,
			CreatedAt:       now,
			UpdatedAt:       now,
		}).Error; err != nil {
			t.Fatalf("seed pending hook: %v", err)
		}
	}
	seedPending(issueA1.ID, projectA)
	seedPending(issueA2.ID, projectA)
	seedPending(issueB1.ID, projectB)

	firedAt := now.Add(time.Minute)
	consumed, err := repo.ConsumePendingByProjectID(projectA, "ver-1", "1.0.0", "https://example.com/1.0.0", firedAt)
	if err != nil {
		t.Fatalf("consume pending: %v", err)
	}
	if len(consumed) != 2 {
		t.Fatalf("expected 2 consumed hooks, got %d", len(consumed))
	}
	for _, hook := range consumed {
		if hook.Status != model.IssueShipHookStatusFired || hook.FiredVersionNumber != "1.0.0" {
			t.Fatalf("unexpected consumed hook: %+v", hook)
		}
	}

	otherPending, err := repo.ListPendingByProjectID(projectB)
	if err != nil {
		t.Fatalf("list project B pending: %v", err)
	}
	if len(otherPending) != 1 {
		t.Fatalf("expected project B pending untouched, got %d", len(otherPending))
	}

	second, err := repo.ConsumePendingByProjectID(projectA, "ver-2", "2.0.0", "https://example.com/2.0.0", firedAt)
	if err != nil {
		t.Fatalf("second consume: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("expected empty second consume, got %d", len(second))
	}
}

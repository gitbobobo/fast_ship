package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// INT-185：BatchCloseInternalTx 只命中 state=open 的内部行，RowsAffected 区分
// 命中与未命中；已非 open 的行（并发方提前关闭）必须留在未命中集合里，行本身
// 不能被二次改写。

func setupBatchCloseInternalTxTestDB(t *testing.T) (*IssueRepository, *gorm.DB, string) {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	now := time.Now().UTC()
	userID := uuid.NewString()
	if err := db.Create(&model.User{ID: userID, Username: "u-" + userID, Email: userID + "@x", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	projectID := uuid.NewString()
	if err := db.Create(&model.Project{ID: projectID, UserID: userID, Name: "p-" + projectID, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return &IssueRepository{db: db}, db, projectID
}

func seedInternalIssue(t *testing.T, db *gorm.DB, projectID string, seq int, state model.IssueState) model.Issue {
	t.Helper()
	now := time.Now().UTC()
	issue := model.Issue{
		ID:             uuid.NewString(),
		ProjectID:      projectID,
		Source:         model.IssueSourceInternal,
		SequenceNumber: seq,
		State:          state,
		Title:          "issue",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(&issue).Error; err != nil {
		t.Fatalf("seed issue %d: %v", seq, err)
	}
	return issue
}

func TestIssueRepositoryBatchCloseInternalTx_OnlyHitsOpenRows(t *testing.T) {
	repo, db, projectID := setupBatchCloseInternalTxTestDB(t)
	open1 := seedInternalIssue(t, db, projectID, 1, model.IssueStateOpen)
	open2 := seedInternalIssue(t, db, projectID, 2, model.IssueStateOpen)
	closedElsewhere := seedInternalIssue(t, db, projectID, 3, model.IssueStateClosed)

	closedAt := time.Now().UTC().Truncate(time.Second)
	closedIDs, err := repo.BatchCloseInternalTx(db, []string{open1.ID, open2.ID, closedElsewhere.ID}, closedAt)
	if err != nil {
		t.Fatalf("batch close internal tx: %v", err)
	}
	if len(closedIDs) != 2 {
		t.Fatalf("expected 2 closed ids, got %v", closedIDs)
	}
	hit := map[string]bool{}
	for _, id := range closedIDs {
		hit[id] = true
	}
	if !hit[open1.ID] || !hit[open2.ID] {
		t.Fatalf("open rows must be closed, got %v", closedIDs)
	}
	if hit[closedElsewhere.ID] {
		t.Fatalf("non-open row must miss, got %v", closedIDs)
	}

	var got, untouched model.Issue
	if err := db.Where("id = ?", open1.ID).First(&got).Error; err != nil {
		t.Fatalf("reload open1: %v", err)
	}
	if got.State != model.IssueStateClosed || got.StateReason != "completed" || got.ClosedAt == nil || !got.ClosedAt.Equal(closedAt) {
		t.Fatalf("expected open1 closed/completed at %v, got %+v", closedAt, got)
	}
	if err := db.Where("id = ?", closedElsewhere.ID).First(&untouched).Error; err != nil {
		t.Fatalf("reload closedElsewhere: %v", err)
	}
	if untouched.ClosedAt != nil {
		t.Fatalf("non-open row must stay untouched, got %+v", untouched)
	}

	// 全部已 closed：再跑一遍必须全部未命中，且不发成功写入。
	again, err := repo.BatchCloseInternalTx(db, []string{open1.ID, open2.ID}, closedAt)
	if err != nil {
		t.Fatalf("second batch close: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected no hits on already-closed rows, got %v", again)
	}
}

func TestIssueRepositoryBatchCloseInternalTx_EmptyIDs(t *testing.T) {
	repo, db, _ := setupBatchCloseInternalTxTestDB(t)
	closedIDs, err := repo.BatchCloseInternalTx(db, nil, time.Now().UTC())
	if err != nil {
		t.Fatalf("empty ids: %v", err)
	}
	if len(closedIDs) != 0 {
		t.Fatalf("expected no ids, got %v", closedIDs)
	}
}

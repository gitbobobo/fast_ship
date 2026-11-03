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
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssueInternalMeta{}); err != nil {
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

func seedInternalIssue(t *testing.T, db *gorm.DB, projectID string, seq int, state model.IssueState, workflow model.IssueWorkflowStatus) model.Issue {
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
	// workflow_status 在 issue_internal_meta 表（关闭事务的 WHERE 经子查询
	// 关联它），种子必须同步建行。
	meta := model.IssueInternalMeta{IssueID: issue.ID, WorkflowStatus: workflow}
	if err := db.Create(&meta).Error; err != nil {
		t.Fatalf("seed issue %d meta: %v", seq, err)
	}
	return issue
}

func TestIssueRepositoryBatchCloseInternalTx_OnlyHitsOpenRows(t *testing.T) {
	repo, db, projectID := setupBatchCloseInternalTxTestDB(t)
	open1 := seedInternalIssue(t, db, projectID, 1, model.IssueStateOpen, model.IssueWorkflowStatusDone)
	open2 := seedInternalIssue(t, db, projectID, 2, model.IssueStateOpen, model.IssueWorkflowStatusDone)
	closedElsewhere := seedInternalIssue(t, db, projectID, 3, model.IssueStateClosed, model.IssueWorkflowStatusDone)
	// 查询后、事务前被看板拖回开发中：state 仍 open，但 workflow 已不是 done，
	// 关闭事务不得命中它（INT-185 R6）。
	draggedBack := seedInternalIssue(t, db, projectID, 4, model.IssueStateOpen, model.IssueWorkflowStatusInProgress)

	closedAt := time.Now().UTC().Truncate(time.Second)
	closedIDs, err := repo.BatchCloseInternalTx(db, []string{open1.ID, open2.ID, closedElsewhere.ID, draggedBack.ID}, closedAt)
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
	if hit[closedElsewhere.ID] || hit[draggedBack.ID] {
		t.Fatalf("non-open / non-done rows must miss, got %v", closedIDs)
	}

	var got, untouched, draggedIssue model.Issue
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
	if err := db.Where("id = ?", draggedBack.ID).First(&draggedIssue).Error; err != nil {
		t.Fatalf("reload draggedBack: %v", err)
	}
	var draggedMeta model.IssueInternalMeta
	if err := db.Where("issue_id = ?", draggedBack.ID).First(&draggedMeta).Error; err != nil {
		t.Fatalf("reload draggedBack meta: %v", err)
	}
	// 目标必须用全新变量：复用已填充结构体时 GORM 会把旧主键追加进 WHERE
	// （R5 同类教训）。
	if draggedIssue.State != model.IssueStateOpen || draggedMeta.WorkflowStatus != model.IssueWorkflowStatusInProgress {
		t.Fatalf("dragged-back row must stay open/in_progress, got issue=%+v meta=%+v", draggedIssue, draggedMeta)
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

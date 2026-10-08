package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupIssueSyncTestDB(t *testing.T) (*gorm.DB, *IssueCommentRepository, *IssueTimelineRepository) {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssueComment{}, &model.IssueTimelineEvent{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_comments_issue_github_comment ON issue_comments(issue_id, github_comment_id)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_timeline_issue_event_key ON issue_timeline_events(issue_id, event_key)")
	return db, NewIssueCommentRepository(db), NewIssueTimelineRepository(db)
}

func seedSyncTestComment(t *testing.T, db *gorm.DB, issueID string, gitHubCommentID int64, source model.IssueSource, body string) *model.IssueComment {
	t.Helper()

	now := time.Now().UTC()
	comment := &model.IssueComment{
		ID:              uuid.NewString(),
		IssueID:         issueID,
		Source:          source,
		GitHubCommentID: gitHubCommentID,
		Body:            body,
		GitHubCreatedAt: now,
		GitHubUpdatedAt: now,
	}
	if err := db.Create(comment).Error; err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	return comment
}

func newSyncTestComment(issueID string, gitHubCommentID int64, body string) *model.IssueComment {
	now := time.Now().UTC()
	return &model.IssueComment{
		ID:              uuid.NewString(),
		IssueID:         issueID,
		Source:          model.IssueSourceGitHub,
		GitHubCommentID: gitHubCommentID,
		Body:            body,
		GitHubCreatedAt: now,
		GitHubUpdatedAt: now,
	}
}

func storedSyncTestComments(t *testing.T, db *gorm.DB, issueID string) map[int64]model.IssueComment {
	t.Helper()

	var comments []model.IssueComment
	if err := db.Where("issue_id = ?", issueID).Find(&comments).Error; err != nil {
		t.Fatalf("list comments: %v", err)
	}
	result := make(map[int64]model.IssueComment, len(comments))
	for _, c := range comments {
		result[c.GitHubCommentID] = c
	}
	return result
}

func seedSyncTestEvent(t *testing.T, db *gorm.DB, issueID, eventKey, eventType string) *model.IssueTimelineEvent {
	t.Helper()

	event := &model.IssueTimelineEvent{
		ID:              uuid.NewString(),
		IssueID:         issueID,
		EventKey:        eventKey,
		EventType:       eventType,
		GitHubCreatedAt: time.Now().UTC(),
	}
	if err := db.Create(event).Error; err != nil {
		t.Fatalf("seed timeline event: %v", err)
	}
	return event
}

func newSyncTestEvent(issueID, eventKey, eventType string) *model.IssueTimelineEvent {
	return &model.IssueTimelineEvent{
		ID:              uuid.NewString(),
		IssueID:         issueID,
		EventKey:        eventKey,
		EventType:       eventType,
		GitHubCreatedAt: time.Now().UTC(),
	}
}

func storedSyncTestEvents(t *testing.T, db *gorm.DB, issueID string) map[string]model.IssueTimelineEvent {
	t.Helper()

	var events []model.IssueTimelineEvent
	if err := db.Where("issue_id = ?", issueID).Find(&events).Error; err != nil {
		t.Fatalf("list timeline events: %v", err)
	}
	result := make(map[string]model.IssueTimelineEvent, len(events))
	for _, e := range events {
		result[e.EventKey] = e
	}
	return result
}

// ReplaceSynced 单事务替换评论镜像：新行插入、既有远端行按唯一键刷新且保留主键、
// 远端已删的行清掉；source='internal' 的本地评论（负数合成 ID）永远不在清理范围。
func TestIssueCommentReplaceSynced_ReplacesMirrorKeepsInternal(t *testing.T) {
	db, comments, _ := setupIssueSyncTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	stale := seedSyncTestComment(t, db, issue.ID, 1, model.IssueSourceGitHub, "stale")
	kept := seedSyncTestComment(t, db, issue.ID, 2, model.IssueSourceGitHub, "old body")
	internal := seedSyncTestComment(t, db, issue.ID, -1, model.IssueSourceInternal, "local note")

	err := comments.ReplaceSynced(issue.ID, []*model.IssueComment{
		newSyncTestComment(issue.ID, 2, "new body"),
		newSyncTestComment(issue.ID, 3, "fresh"),
	})
	if err != nil {
		t.Fatalf("replace synced comments: %v", err)
	}

	stored := storedSyncTestComments(t, db, issue.ID)
	if len(stored) != 3 {
		t.Fatalf("expected 3 comments, got %+v", stored)
	}
	if _, ok := stored[stale.GitHubCommentID]; ok {
		t.Fatalf("expected stale github comment %d deleted", stale.GitHubCommentID)
	}
	if got := stored[2]; got.ID != kept.ID || got.Body != "new body" {
		t.Fatalf("expected comment 2 refreshed in place, got %+v", got)
	}
	if got := stored[3]; got.Body != "fresh" {
		t.Fatalf("expected comment 3 inserted, got %+v", got)
	}
	if got := stored[-1]; got.ID != internal.ID || got.Source != model.IssueSourceInternal {
		t.Fatalf("expected internal comment preserved, got %+v", got)
	}
}

// 远端空集合：清掉该 Issue 全部 GitHub 镜像行，internal 评论仍保留。
func TestIssueCommentReplaceSynced_EmptySetCleansMirrorOnly(t *testing.T) {
	db, comments, _ := setupIssueSyncTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	seedSyncTestComment(t, db, issue.ID, 1, model.IssueSourceGitHub, "mirror")
	internal := seedSyncTestComment(t, db, issue.ID, -1, model.IssueSourceInternal, "local note")

	if err := comments.ReplaceSynced(issue.ID, nil); err != nil {
		t.Fatalf("replace synced comments: %v", err)
	}

	stored := storedSyncTestComments(t, db, issue.ID)
	if len(stored) != 1 || stored[-1].ID != internal.ID {
		t.Fatalf("expected only internal comment kept, got %+v", stored)
	}
}

// upsert 中途失败（外键违例）：事务整体回滚——已 upsert 的行不落库，既有镜像行不变也不删。
func TestIssueCommentReplaceSynced_RollsBackOnUpsertFailure(t *testing.T) {
	db, comments, _ := setupIssueSyncTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	stale := seedSyncTestComment(t, db, issue.ID, 1, model.IssueSourceGitHub, "stale")

	err := comments.ReplaceSynced(issue.ID, []*model.IssueComment{
		newSyncTestComment(issue.ID, 2, "valid"),
		newSyncTestComment("ghost-issue", 3, "fk violation"),
	})
	if err == nil {
		t.Fatal("expected upsert failure, got nil")
	}

	stored := storedSyncTestComments(t, db, issue.ID)
	if len(stored) != 1 || stored[1].ID != stale.ID {
		t.Fatalf("expected collection unchanged after rollback, got %+v", stored)
	}
}

// 清理失败（DELETE 触发器注入）：事务整体回滚——upsert 的行不提交，镜像行不删。
func TestIssueCommentReplaceSynced_RollsBackOnDeleteFailure(t *testing.T) {
	db, comments, _ := setupIssueSyncTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	stale := seedSyncTestComment(t, db, issue.ID, 1, model.IssueSourceGitHub, "stale")

	if err := db.Exec("CREATE TRIGGER fail_comment_delete BEFORE DELETE ON issue_comments BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END").Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS fail_comment_delete")
	})

	err := comments.ReplaceSynced(issue.ID, []*model.IssueComment{
		newSyncTestComment(issue.ID, 2, "valid"),
	})
	if err == nil {
		t.Fatal("expected delete failure, got nil")
	}

	stored := storedSyncTestComments(t, db, issue.ID)
	if len(stored) != 1 || stored[1].ID != stale.ID {
		t.Fatalf("expected collection unchanged after rollback, got %+v", stored)
	}
}

// 时间线 ReplaceSynced 与评论同构：按 (issue_id, event_key) upsert + 清理缺失。
func TestIssueTimelineReplaceSynced_ReplacesMirror(t *testing.T) {
	db, _, timeline := setupIssueSyncTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	seedSyncTestEvent(t, db, issue.ID, "gh:1", "stale")
	kept := seedSyncTestEvent(t, db, issue.ID, "gh:2", "labeled")

	err := timeline.ReplaceSynced(issue.ID, []*model.IssueTimelineEvent{
		newSyncTestEvent(issue.ID, "gh:2", "closed"),
		newSyncTestEvent(issue.ID, "gh:3", "reopened"),
	})
	if err != nil {
		t.Fatalf("replace synced timeline: %v", err)
	}

	stored := storedSyncTestEvents(t, db, issue.ID)
	if len(stored) != 2 {
		t.Fatalf("expected 2 events, got %+v", stored)
	}
	if got := stored["gh:2"]; got.ID != kept.ID || got.EventType != "closed" {
		t.Fatalf("expected event gh:2 refreshed in place, got %+v", got)
	}
	if got := stored["gh:3"]; got.EventType != "reopened" {
		t.Fatalf("expected event gh:3 inserted, got %+v", got)
	}
}

// upsert 中途失败（外键违例）：时间线事务同样整体回滚。
func TestIssueTimelineReplaceSynced_RollsBackOnUpsertFailure(t *testing.T) {
	db, _, timeline := setupIssueSyncTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	stale := seedSyncTestEvent(t, db, issue.ID, "gh:1", "stale")

	err := timeline.ReplaceSynced(issue.ID, []*model.IssueTimelineEvent{
		newSyncTestEvent(issue.ID, "gh:2", "labeled"),
		newSyncTestEvent("ghost-issue", "gh:3", "closed"),
	})
	if err == nil {
		t.Fatal("expected upsert failure, got nil")
	}

	stored := storedSyncTestEvents(t, db, issue.ID)
	if len(stored) != 1 || stored["gh:1"].ID != stale.ID {
		t.Fatalf("expected collection unchanged after rollback, got %+v", stored)
	}
}

// 清理失败（DELETE 触发器注入）：时间线事务同样整体回滚。
func TestIssueTimelineReplaceSynced_RollsBackOnDeleteFailure(t *testing.T) {
	db, _, timeline := setupIssueSyncTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	stale := seedSyncTestEvent(t, db, issue.ID, "gh:1", "stale")

	if err := db.Exec("CREATE TRIGGER fail_event_delete BEFORE DELETE ON issue_timeline_events BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END").Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	t.Cleanup(func() {
		db.Exec("DROP TRIGGER IF EXISTS fail_event_delete")
	})

	err := timeline.ReplaceSynced(issue.ID, []*model.IssueTimelineEvent{
		newSyncTestEvent(issue.ID, "gh:2", "labeled"),
	})
	if err == nil {
		t.Fatal("expected delete failure, got nil")
	}

	stored := storedSyncTestEvents(t, db, issue.ID)
	if len(stored) != 1 || stored["gh:1"].ID != stale.ID {
		t.Fatalf("expected collection unchanged after rollback, got %+v", stored)
	}
}

package service

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func createReadTestComment(t *testing.T, db *gorm.DB, issueID string, githubCommentID int64, body string, createdAt time.Time) *model.IssueComment {
	t.Helper()

	comment := &model.IssueComment{
		ID:              fmt.Sprintf("comment-%s-%d", issueID[:8], githubCommentID),
		IssueID:         issueID,
		Source:          model.IssueSourceGitHub,
		GitHubCommentID: githubCommentID,
		Body:            body,
		AuthorLogin:     "bob",
		GitHubCreatedAt: createdAt,
		GitHubUpdatedAt: createdAt,
	}
	if err := db.Create(comment).Error; err != nil {
		t.Fatalf("create comment: %v", err)
	}
	return comment
}

func TestListCountsUnreadGitHubComments(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	base := time.Now().UTC().Add(-24 * time.Hour)

	githubIssue := createTestIssue(t, svc.db, project.ID)
	createReadTestComment(t, svc.db, githubIssue.ID, 1, "最早的一条", base)
	createReadTestComment(t, svc.db, githubIssue.ID, 2, model.FastShipHookCommentMarker+"ship-1 -->", base.Add(time.Hour))
	createReadTestComment(t, svc.db, githubIssue.ID, 3, "最新的一条", base.Add(2*time.Hour))

	// 内部 Issue 没有 GitHub 元数据，不走 createTestIssue
	internalIssue := &model.Issue{
		ID:             uuid.NewString(),
		ProjectID:      project.ID,
		Source:         model.IssueSourceInternal,
		SequenceNumber: 2,
		State:          model.IssueStateOpen,
		Title:          "站内问题",
		AuthorUserID:   "user-1",
		CreatedAt:      base,
		UpdatedAt:      base,
	}
	if err := svc.db.Create(internalIssue).Error; err != nil {
		t.Fatalf("create internal issue: %v", err)
	}
	createReadTestComment(t, svc.db, internalIssue.ID, -1, "站内评论", base)

	items, _, err := svc.issueService.List(project.ID, "user-1", IssueListFilters{}, 1, 20)
	if err != nil {
		t.Fatalf("list issues: %v", err)
	}

	unread := make(map[string]int, len(items))
	for _, item := range items {
		unread[item.ID] = item.UnreadCommentsCount
	}
	if unread[githubIssue.ID] != 2 {
		t.Fatalf("github issue unread = %d, want 2（发货钩子评论不计入）", unread[githubIssue.ID])
	}
	if unread[internalIssue.ID] != 0 {
		t.Fatalf("internal issue unread = %d, want 0（内部 Issue 不做未读）", unread[internalIssue.ID])
	}
}

func TestMarkIssueReadConsumesUnreadUntilNewCommentSyncs(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	base := time.Now().UTC().Add(-24 * time.Hour)

	issue := createTestIssue(t, svc.db, project.ID)
	createReadTestComment(t, svc.db, issue.ID, 1, "早的一条", base)
	createReadTestComment(t, svc.db, issue.ID, 2, "晚的一条", base.Add(time.Hour))

	if err := svc.issueService.MarkIssueRead(issue.ID, "user-1"); err != nil {
		t.Fatalf("mark issue read: %v", err)
	}
	if counts, err := svc.issueService.unreadCountsByIssueIDs("user-1", []model.Issue{makeGithubIssueRef(issue)}); err != nil {
		t.Fatalf("unread counts: %v", err)
	} else if counts[issue.ID] != 0 {
		t.Fatalf("unread after mark-read = %d, want 0", counts[issue.ID])
	}

	// 之后同步进来一条更新的评论 → 重新算未读
	createReadTestComment(t, svc.db, issue.ID, 3, "刚同步进来的一条", base.Add(2*time.Hour))
	if counts, err := svc.issueService.unreadCountsByIssueIDs("user-1", []model.Issue{makeGithubIssueRef(issue)}); err != nil {
		t.Fatalf("unread counts: %v", err)
	} else if counts[issue.ID] != 1 {
		t.Fatalf("unread after new comment = %d, want 1", counts[issue.ID])
	}
}

func TestMarkIssueReadDoesNotMoveWatermarkBack(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")
	base := time.Now().UTC().Add(-24 * time.Hour)

	issue := createTestIssue(t, svc.db, project.ID)
	createReadTestComment(t, svc.db, issue.ID, 1, "唯一的一条", base)

	if err := svc.issueService.MarkIssueRead(issue.ID, "user-1"); err != nil {
		t.Fatalf("mark issue read: %v", err)
	}

	// 用户随后发评论把水位推得更远；再打开详情页时本地最新评论仍是 base，
	// 水位不能被拉回去
	future := base.Add(time.Hour)
	if err := svc.readStateRepo.Advance("user-1", issue.ID, future); err != nil {
		t.Fatalf("advance watermark: %v", err)
	}
	if err := svc.issueService.MarkIssueRead(issue.ID, "user-1"); err != nil {
		t.Fatalf("mark issue read again: %v", err)
	}
	state, err := svc.readStateRepo.Get("user-1", issue.ID)
	if err != nil {
		t.Fatalf("get read state: %v", err)
	}
	if !state.LastReadAt.Equal(future) {
		t.Fatalf("last_read_at = %v, want %v（水位不允许回拨）", state.LastReadAt, future)
	}
}

func TestMarkIssueReadInternalIssueKeepsNoState(t *testing.T) {
	svc := setupTestServices(t)
	createTestUser(t, svc.db, "user-1")
	project := createTestProject(t, svc.db, "user-1")

	issue := createTestIssue(t, svc.db, project.ID, func(item *model.Issue) {
		item.Source = model.IssueSourceInternal
		item.SequenceNumber = 2
		item.AuthorUserID = "user-1"
	})

	if err := svc.issueService.MarkIssueRead(issue.ID, "user-1"); err != nil {
		t.Fatalf("mark internal issue read: %v", err)
	}
	if _, err := svc.readStateRepo.Get("user-1", issue.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("get read state error = %v, want ErrRecordNotFound（内部 Issue 不写水位）", err)
	}
}

func TestMarkIssueReadUnknownIssue(t *testing.T) {
	svc := setupTestServices(t)
	err := svc.issueService.MarkIssueRead("missing-issue", "user-1")
	if !errors.Is(err, errs.ErrIssueNotFound) {
		t.Fatalf("mark missing issue read error = %v, want ErrIssueNotFound", err)
	}
}

func makeGithubIssueRef(issue *model.Issue) model.Issue {
	return model.Issue{ID: issue.ID, Source: model.IssueSourceGitHub}
}

package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupIssueReadStateTestDB(t *testing.T) (*gorm.DB, *IssueReadStateRepository) {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssueComment{}, &model.IssueReadState{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, NewIssueReadStateRepository(db)
}

// 历史数据里时间戳的『T』分隔与空格分隔可能并存，未读比较必须与格式无关
func TestCountUnreadIgnoresTimestampFormat(t *testing.T) {
	db, repo := setupIssueReadStateTestDB(t)

	user := &model.User{ID: "user-1", Username: "u", Email: "u@example.com", PasswordHash: "x"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	project := &model.Project{ID: "proj-1", UserID: user.ID, Name: "p", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	issue := &model.Issue{ID: "issue-1", ProjectID: project.ID, Source: model.IssueSourceGitHub, State: model.IssueStateOpen, Title: "t", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}

	// 与 GORM 默认落库格式不同的『T』分隔写法
	if err := db.Exec(`
		INSERT INTO issue_comments (id, issue_id, source, github_comment_id, body, created_at, updated_at)
		VALUES ('c1', 'issue-1', 'github', 1, '评论', '2026-09-03T10:00:00+00:00', '2026-09-03T10:00:00+00:00')
	`).Error; err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	// 水位恰好对齐该评论，同刻不算未读
	if err := repo.Advance(user.ID, issue.ID, time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("advance: %v", err)
	}
	counts, err := repo.CountUnreadByIssueIDs(user.ID, []string{issue.ID})
	if err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if counts[issue.ID] != 0 {
		t.Fatalf("unread = %d, want 0（同刻评论不能算未读）", counts[issue.ID])
	}

	// 水位早于评论 → 未读 1
	if err := db.Exec("UPDATE issue_read_states SET last_read_at = '2026-09-03 09:00:00+00:00'").Error; err != nil {
		t.Fatalf("rewind watermark: %v", err)
	}
	counts, err = repo.CountUnreadByIssueIDs(user.ID, []string{issue.ID})
	if err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if counts[issue.ID] != 1 {
		t.Fatalf("unread = %d, want 1", counts[issue.ID])
	}
}

func TestCountUnreadExcludesHookComments(t *testing.T) {
	db, repo := setupIssueReadStateTestDB(t)

	user := &model.User{ID: "user-1", Username: "u", Email: "u@example.com", PasswordHash: "x"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	project := &model.Project{ID: "proj-1", UserID: user.ID, Name: "p", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	issue := &model.Issue{ID: "issue-1", ProjectID: project.ID, Source: model.IssueSourceGitHub, State: model.IssueStateOpen, Title: "t", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}

	seed := func(id string, ghID int64, body string, at string) {
		if err := db.Exec(
			"INSERT INTO issue_comments (id, issue_id, source, github_comment_id, body, created_at, updated_at) VALUES (?, 'issue-1', 'github', ?, ?, ?, ?)",
			id, ghID, body, at, at,
		).Error; err != nil {
			t.Fatalf("seed comment %s: %v", id, err)
		}
	}
	seed("c1", 1, "普通评论", "2026-09-03T10:00:00+00:00")
	seed("c2", 2, model.FastShipHookCommentMarker+"ship-1 -->", "2026-09-03T11:00:00+00:00")

	counts, err := repo.CountUnreadByIssueIDs(user.ID, []string{issue.ID})
	if err != nil {
		t.Fatalf("count unread: %v", err)
	}
	if counts[issue.ID] != 1 {
		t.Fatalf("unread = %d, want 1（发货钩子留言不计入）", counts[issue.ID])
	}
}

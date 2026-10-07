package repository

import (
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

func setupIssuePullRequestTestDB(t *testing.T) (*gorm.DB, *IssuePullRequestRepository) {
	t.Helper()

	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssuePullRequest{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_pull_requests_issue_pr ON issue_pull_requests(issue_id, provider, repo_full_name, number)")
	return db, NewIssuePullRequestRepository(db)
}

func createIssuePullRequestTestIssue(t *testing.T, db *gorm.DB, projectID string) *model.Issue {
	t.Helper()

	now := time.Now().UTC()
	userID := uuid.NewString()
	if err := db.Create(&model.User{ID: userID, Username: "u-" + userID, Email: userID + "@x", PasswordHash: "x", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&model.Project{ID: projectID, UserID: userID, Name: "p-" + projectID, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("seed project: %v", err)
	}

	issue := &model.Issue{
		ID:             uuid.NewString(),
		ProjectID:      projectID,
		Source:         model.IssueSourceInternal,
		SequenceNumber: 1,
		State:          model.IssueStateOpen,
		Title:          "link target",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Create(issue).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}
	return issue
}

func seedIssuePullRequest(t *testing.T, db *gorm.DB, issueID string, number int, origin model.IssuePullRequestLinkOrigin) *model.IssuePullRequest {
	t.Helper()

	now := time.Now().UTC()
	link := &model.IssuePullRequest{
		ID:           uuid.NewString(),
		IssueID:      issueID,
		ProjectID:    "project-1",
		Provider:     model.IssuePullRequestProviderGitHub,
		RepoFullName: "owner/repo",
		Number:       number,
		State:        model.IssuePullRequestStateOpen,
		LinkOrigin:   origin,
		SyncedAt:     now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.Create(link).Error; err != nil {
		t.Fatalf("seed link: %v", err)
	}
	return link
}

func listLinkIDs(t *testing.T, repo *IssuePullRequestRepository, issueID string) map[string]bool {
	t.Helper()

	links, err := repo.ListByIssueID(issueID)
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	ids := make(map[string]bool, len(links))
	for _, link := range links {
		ids[link.ID] = true
	}
	return ids
}

// DeleteMissingSynced 只作用于 synced 行：manual 关联永不被同步清理删掉。
func TestIssuePullRequestDeleteMissingSynced_NeverTouchesManual(t *testing.T) {
	db, repo := setupIssuePullRequestTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	manual := seedIssuePullRequest(t, db, issue.ID, 1, model.IssuePullRequestLinkOriginManual)
	synced := seedIssuePullRequest(t, db, issue.ID, 2, model.IssuePullRequestLinkOriginSynced)

	if err := repo.DeleteMissingSynced(issue.ID, nil); err != nil {
		t.Fatalf("delete missing synced: %v", err)
	}

	remaining := listLinkIDs(t, repo, issue.ID)
	if !remaining[manual.ID] {
		t.Fatalf("manual link %s was wrongly deleted", manual.ID)
	}
	if remaining[synced.ID] {
		t.Fatalf("synced link %s should have been deleted", synced.ID)
	}
}

// keepKeys 保留本轮同步仍存在的 synced 行，其余的删掉。
func TestIssuePullRequestDeleteMissingSynced_KeepsListedKeys(t *testing.T) {
	db, repo := setupIssuePullRequestTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	keep := seedIssuePullRequest(t, db, issue.ID, 1, model.IssuePullRequestLinkOriginSynced)
	stale := seedIssuePullRequest(t, db, issue.ID, 2, model.IssuePullRequestLinkOriginSynced)
	manual := seedIssuePullRequest(t, db, issue.ID, 3, model.IssuePullRequestLinkOriginManual)

	if err := repo.DeleteMissingSynced(issue.ID, []string{keep.SyncKey()}); err != nil {
		t.Fatalf("delete missing synced: %v", err)
	}

	remaining := listLinkIDs(t, repo, issue.ID)
	if !remaining[keep.ID] {
		t.Fatalf("kept synced link %s was wrongly deleted", keep.ID)
	}
	if remaining[stale.ID] {
		t.Fatalf("stale synced link %s should have been deleted", stale.ID)
	}
	if !remaining[manual.ID] {
		t.Fatalf("manual link %s was wrongly deleted", manual.ID)
	}
}

// 唯一键 (issue_id, provider, repo_full_name, number) 生效：同 PR 不能插两行。
func TestIssuePullRequestUniqueKeyEnforced(t *testing.T) {
	db, repo := setupIssuePullRequestTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	seedIssuePullRequest(t, db, issue.ID, 7, model.IssuePullRequestLinkOriginManual)
	dup := seedIssuePullRequest(t, db, issue.ID, 8, model.IssuePullRequestLinkOriginManual)
	dup.Number = 7
	if err := repo.Create(dup); err == nil {
		t.Fatal("expected unique constraint violation, got nil")
	}
}

// issue_pull_requests 的外键必须真的带 ON DELETE CASCADE——has-many 与 belongs-to
// 双侧声明时 GORM 取 has-many 的 constraint，缺了就会在删 Issue/项目时撞 NO ACTION。
func TestIssuePullRequestCascadeInDDLAndDelete(t *testing.T) {
	db, _ := setupIssuePullRequestTestDB(t)

	var ddl string
	db.Raw("SELECT sql FROM sqlite_master WHERE name = 'issue_pull_requests'").Scan(&ddl)
	if !strings.Contains(ddl, "ON DELETE CASCADE") {
		t.Fatalf("issue_pull_requests FK missing ON DELETE CASCADE: %s", ddl)
	}
	type fkRow struct {
		OnDelete string `gorm:"column:on_delete"`
	}
	var fks []fkRow
	db.Raw("PRAGMA foreign_key_list(issue_pull_requests)").Scan(&fks)
	foundCascade := false
	for _, fk := range fks {
		if fk.OnDelete == "CASCADE" {
			foundCascade = true
		}
	}
	if !foundCascade {
		t.Fatalf("PRAGMA foreign_key_list shows no CASCADE: %+v", fks)
	}

	issue := createIssuePullRequestTestIssue(t, db, "project-1")
	seedIssuePullRequest(t, db, issue.ID, 1, model.IssuePullRequestLinkOriginManual)
	seedIssuePullRequest(t, db, issue.ID, 2, model.IssuePullRequestLinkOriginSynced)

	if err := db.Delete(&model.Issue{}, "id = ?", issue.ID).Error; err != nil {
		t.Fatalf("delete issue: %v", err)
	}
	var count int64
	if err := db.Model(&model.IssuePullRequest{}).Where("issue_id = ?", issue.ID).Count(&count).Error; err != nil {
		t.Fatalf("count links: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected links cascaded with issue, got %d", count)
	}
}

// 同一唯一键重复 Upsert：不产生第二行，同步字段被刷新；link_origin 只升不降。
func TestIssuePullRequestUpsert_ConflictKeepsSingleRow(t *testing.T) {
	db, repo := setupIssuePullRequestTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	first := seedIssuePullRequest(t, db, issue.ID, 7, model.IssuePullRequestLinkOriginManual)

	now := time.Now().UTC()
	second := &model.IssuePullRequest{
		ID:           uuid.NewString(),
		IssueID:      issue.ID,
		ProjectID:    issue.ProjectID,
		Provider:     model.IssuePullRequestProviderGitHub,
		RepoFullName: "owner/repo",
		Number:       7,
		Title:        "updated title",
		State:        model.IssuePullRequestStateMerged,
		LinkOrigin:   model.IssuePullRequestLinkOriginManual,
		SyncedAt:     now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repo.Upsert(second); err != nil {
		t.Fatalf("upsert duplicate: %v", err)
	}

	links, err := repo.ListByIssueID(issue.ID)
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("expected single row after conflicting upsert, got %d", len(links))
	}
	stored := links[0]
	if stored.ID != first.ID {
		t.Fatalf("expected existing row id %q to survive, got %q", first.ID, stored.ID)
	}
	if stored.Title != "updated title" || stored.State != model.IssuePullRequestStateMerged {
		t.Fatalf("expected synced fields refreshed, got %+v", stored)
	}
	if stored.LinkOrigin != model.IssuePullRequestLinkOriginManual {
		t.Fatalf("expected link_origin preserved, got %q", stored.LinkOrigin)
	}

	// link_origin 只升不降：synced 写入撞到 manual 行时不得降级，
	// 否则该行会被 DeleteMissingSynced 当成同步投影残留清掉。
	third := &model.IssuePullRequest{
		ID:           uuid.NewString(),
		IssueID:      issue.ID,
		ProjectID:    issue.ProjectID,
		Provider:     model.IssuePullRequestProviderGitHub,
		RepoFullName: "owner/repo",
		Number:       7,
		Title:        "synced write",
		State:        model.IssuePullRequestStateOpen,
		LinkOrigin:   model.IssuePullRequestLinkOriginSynced,
		SyncedAt:     now,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := repo.Upsert(third); err != nil {
		t.Fatalf("upsert synced over manual: %v", err)
	}
	stored2, err := repo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, "owner/repo", 7)
	if err != nil {
		t.Fatalf("find by unique: %v", err)
	}
	if stored2.LinkOrigin != model.IssuePullRequestLinkOriginManual {
		t.Fatalf("manual row must not be downgraded to %q", stored2.LinkOrigin)
	}
	if stored2.Title != "synced write" || stored2.State != model.IssuePullRequestStateOpen {
		t.Fatalf("expected synced fields refreshed by synced upsert, got %+v", stored2)
	}
}

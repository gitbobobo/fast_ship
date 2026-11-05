package repository

import (
	"strconv"
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
	if err := db.AutoMigrate(&model.User{}, &model.Project{}, &model.Issue{}, &model.IssuePullRequest{}, &model.ForgeIntegration{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("DROP INDEX IF EXISTS idx_issue_pull_requests_issue_pr")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_issue_pull_requests_issue_pr_v2 ON issue_pull_requests(issue_id, provider, host_key, repo_full_name, number)")
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

// 以显式时间戳建行，供确定性交错用例控制快照新旧。
func seedIssuePullRequestAt(t *testing.T, db *gorm.DB, issueID string, number int, origin model.IssuePullRequestLinkOrigin, at time.Time, title string, state model.IssuePullRequestState) *model.IssuePullRequest {
	t.Helper()

	link := &model.IssuePullRequest{
		ID:           uuid.NewString(),
		IssueID:      issueID,
		ProjectID:    "project-1",
		Provider:     model.IssuePullRequestProviderGitHub,
		RepoFullName: "owner/repo",
		Number:       number,
		Title:        title,
		HTMLURL:      "https://github.com/owner/repo/pull/" + strconv.Itoa(number),
		State:        state,
		LinkOrigin:   origin,
		SyncedAt:     at,
		CreatedAt:    at,
		UpdatedAt:    at,
	}
	if err := db.Create(link).Error; err != nil {
		t.Fatalf("seed link at %v: %v", at, err)
	}
	return link
}

// INT-188 确定性交错（sync 先写新、attach 后到旧）：sync 先落较新快照，
// attach 带着更旧的快照到达时，快照列不得回拨；link_origin 升级与
// forge_integration_id 换绑是身份语义，不受守卫拦限；空集成快照不覆盖。
func TestIssuePullRequestUpsert_SyncedNewerSurvivesStaleAttach(t *testing.T) {
	db, repo := setupIssuePullRequestTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	t0 := time.Now().UTC().Add(-2 * time.Hour)
	attachFetchedAt := t0.Add(time.Hour)   // attach 快照时刻（旧）
	syncFetchedAt := t0.Add(2 * time.Hour) // sync 快照时刻（新）

	seedIssuePullRequestAt(t, db, issue.ID, 7, model.IssuePullRequestLinkOriginSynced, t0, "initial", model.IssuePullRequestStateOpen)

	// sync 用 SaveSyncedFields 写入较新快照（merged）。
	mergedAt := syncFetchedAt.Add(-30 * time.Minute)
	synced := &model.IssuePullRequest{
		ID: "", IssueID: issue.ID, Provider: model.IssuePullRequestProviderGitHub,
		RepoFullName: "owner/repo", Number: 7,
		HTMLURL: "https://github.com/owner/repo/pull/7/new", Title: "newer-from-sync",
		State: model.IssuePullRequestStateMerged, MergedAt: &mergedAt,
		AuthorLogin: "sync-author", SyncedAt: syncFetchedAt, UpdatedAt: syncFetchedAt,
	}
	stored, err := repo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, "", "owner/repo", 7)
	if err != nil {
		t.Fatalf("find seeded row: %v", err)
	}
	synced.ID = stored.ID
	if err := repo.SaveSyncedFields(synced); err != nil {
		t.Fatalf("sync write: %v", err)
	}

	// attach 带更旧的快照到达：快照列必须被守卫拦下。
	staleAttach := &model.IssuePullRequest{
		ID: uuid.NewString(), IssueID: issue.ID, ProjectID: issue.ProjectID,
		Provider: model.IssuePullRequestProviderGitHub, RepoFullName: "owner/repo", Number: 7,
		HTMLURL: "https://github.com/owner/repo/pull/7", Title: "stale-from-attach",
		State: model.IssuePullRequestStateOpen, AuthorLogin: "attach-author",
		LinkOrigin: model.IssuePullRequestLinkOriginManual, ForgeIntegrationID: "int-new",
		SyncedAt: attachFetchedAt, CreatedAt: attachFetchedAt, UpdatedAt: attachFetchedAt,
	}
	if err := repo.Upsert(staleAttach); err != nil {
		t.Fatalf("stale attach upsert: %v", err)
	}

	row, err := repo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, "", "owner/repo", 7)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.Title != "newer-from-sync" || row.State != model.IssuePullRequestStateMerged || row.MergedAt == nil {
		t.Fatalf("stale attach must not overwrite newer snapshot, got %+v", row)
	}
	if row.HTMLURL != "https://github.com/owner/repo/pull/7/new" || row.AuthorLogin != "sync-author" {
		t.Fatalf("snapshot columns must stay at sync values, got %+v", row)
	}
	if !row.SyncedAt.Equal(syncFetchedAt) || !row.UpdatedAt.Equal(syncFetchedAt) {
		t.Fatalf("synced_at/updated_at must not roll back to stale attach time, got %v/%v", row.SyncedAt, row.UpdatedAt)
	}
	// 身份列不受守卫：manual 升级与集成换绑生效。
	if row.LinkOrigin != model.IssuePullRequestLinkOriginManual {
		t.Fatalf("manual upgrade must not be blocked by snapshot guard, got %q", row.LinkOrigin)
	}
	if row.ForgeIntegrationID != "int-new" {
		t.Fatalf("integration rebind must not be blocked by snapshot guard, got %q", row.ForgeIntegrationID)
	}

	// 空集成快照的调用方不得把已换绑的快照抹掉（既有语义在守卫下依旧成立）。
	emptySnap := *staleAttach
	emptySnap.ForgeIntegrationID = ""
	if err := repo.Upsert(&emptySnap); err != nil {
		t.Fatalf("empty-snapshot upsert: %v", err)
	}
	row2, err := repo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, "", "owner/repo", 7)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row2.ForgeIntegrationID != "int-new" {
		t.Fatalf("empty integration snapshot must not wipe existing value, got %q", row2.ForgeIntegrationID)
	}
	if row2.Title != "newer-from-sync" {
		t.Fatalf("empty-snapshot upsert must stay guarded, got %+v", row2)
	}
}

// INT-188 反向交错（attach 先写旧、sync 后到新）：attach 的旧快照先落库，
// sync 带更新快照到达时照常胜出——时间守卫只拦旧压新，不拦新压旧。
func TestIssuePullRequestUpsert_StaleAttachFirstSyncNewerWins(t *testing.T) {
	db, repo := setupIssuePullRequestTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	t0 := time.Now().UTC().Add(-2 * time.Hour)
	attachFetchedAt := t0.Add(time.Hour)
	syncFetchedAt := t0.Add(2 * time.Hour)

	seedIssuePullRequestAt(t, db, issue.ID, 7, model.IssuePullRequestLinkOriginManual, t0, "initial", model.IssuePullRequestStateOpen)

	attachSnap := &model.IssuePullRequest{
		ID: uuid.NewString(), IssueID: issue.ID, ProjectID: issue.ProjectID,
		Provider: model.IssuePullRequestProviderGitHub, RepoFullName: "owner/repo", Number: 7,
		Title: "attach-t1", State: model.IssuePullRequestStateOpen,
		LinkOrigin: model.IssuePullRequestLinkOriginManual,
		SyncedAt:   attachFetchedAt, CreatedAt: attachFetchedAt, UpdatedAt: attachFetchedAt,
	}
	if err := repo.Upsert(attachSnap); err != nil {
		t.Fatalf("attach upsert: %v", err)
	}

	syncSnap := &model.IssuePullRequest{
		ID: "", IssueID: issue.ID, Provider: model.IssuePullRequestProviderGitHub,
		RepoFullName: "owner/repo", Number: 7,
		Title: "sync-t2", State: model.IssuePullRequestStateClosed,
		AuthorLogin: "sync-author", SyncedAt: syncFetchedAt, UpdatedAt: syncFetchedAt,
	}
	stored, err := repo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, "", "owner/repo", 7)
	if err != nil {
		t.Fatalf("find row: %v", err)
	}
	syncSnap.ID = stored.ID
	if err := repo.SaveSyncedFields(syncSnap); err != nil {
		t.Fatalf("sync write: %v", err)
	}

	row, err := repo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, "", "owner/repo", 7)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.Title != "sync-t2" || row.State != model.IssuePullRequestStateClosed {
		t.Fatalf("newer sync snapshot must win over earlier attach write, got %+v", row)
	}
	if !row.SyncedAt.Equal(syncFetchedAt) {
		t.Fatalf("synced_at must advance to sync snapshot time, got %v", row.SyncedAt)
	}
	if row.LinkOrigin != model.IssuePullRequestLinkOriginManual {
		t.Fatalf("manual origin must survive sync write, got %q", row.LinkOrigin)
	}
}

// 写入端 UTC 归一化（INT-188 审查 P2）：调用方传入非 UTC 时区的 time.Time
// 时，落库前强制归一——否则 SQLite 文本字典序会按墙钟而非真实时刻比较，
// 西边时区的新快照会被守卫误判为旧快照。
func TestIssuePullRequestUpsert_NonUTCTimeNormalizedForGuard(t *testing.T) {
	db, repo := setupIssuePullRequestTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	t0 := time.Now().UTC().Add(-time.Hour)
	utcMinus8 := time.FixedZone("UTC-8", -8*3600)
	seedIssuePullRequestAt(t, db, issue.ID, 7, model.IssuePullRequestLinkOriginManual, t0, "initial", model.IssuePullRequestStateOpen)

	// 比库内新 1 小时的真实时刻，但以 UTC-8 墙钟表达（文本序会落到 t0 之前）：
	// 未归一时守卫会把它误判为旧快照并拦下；归一后必须正常刷新。
	newerInstant := t0.Add(time.Hour)
	attachSnap := &model.IssuePullRequest{
		ID: uuid.NewString(), IssueID: issue.ID, ProjectID: issue.ProjectID,
		Provider: model.IssuePullRequestProviderGitHub, RepoFullName: "owner/repo", Number: 7,
		Title: "newer-non-utc", State: model.IssuePullRequestStateOpen,
		LinkOrigin: model.IssuePullRequestLinkOriginManual,
		SyncedAt:   newerInstant.In(utcMinus8), CreatedAt: newerInstant.In(utcMinus8), UpdatedAt: newerInstant.In(utcMinus8),
	}
	if err := repo.Upsert(attachSnap); err != nil {
		t.Fatalf("non-utc upsert: %v", err)
	}
	row, err := repo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, "", "owner/repo", 7)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.Title != "newer-non-utc" {
		t.Fatalf("non-UTC newer snapshot must refresh after normalization, got %+v", row)
	}
	if !row.SyncedAt.Equal(newerInstant) {
		t.Fatalf("synced_at must advance to the real instant, got %v want %v", row.SyncedAt, newerInstant)
	}
	// 读回的时区必须是 UTC：若写入端未归一，存储文本会带源时区偏移，读回后
	// location 就不是 UTC（文本字典序比较的时序一致依赖这一格式）。
	if row.SyncedAt.Location() != time.UTC {
		t.Fatalf("stored synced_at must be UTC after normalization, got location %v (%v)", row.SyncedAt.Location(), row.SyncedAt)
	}

	// SaveSyncedFields 同口径：更新的真实时刻以 UTC-8 表达，归一后守卫放行。
	syncSnap := &model.IssuePullRequest{
		ID: row.ID, IssueID: issue.ID, Provider: model.IssuePullRequestProviderGitHub,
		RepoFullName: "owner/repo", Number: 7,
		Title: "newer-non-utc-sync", State: model.IssuePullRequestStateClosed,
		SyncedAt: newerInstant.Add(time.Hour).In(utcMinus8), UpdatedAt: newerInstant.Add(time.Hour).In(utcMinus8),
	}
	if err := repo.SaveSyncedFields(syncSnap); err != nil {
		t.Fatalf("non-utc sync write: %v", err)
	}
	row2, err := repo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, "", "owner/repo", 7)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row2.Title != "newer-non-utc-sync" || row2.State != model.IssuePullRequestStateClosed {
		t.Fatalf("non-UTC newer sync write must apply after normalization, got %+v", row2)
	}
	if !row2.SyncedAt.Equal(newerInstant.Add(time.Hour)) {
		t.Fatalf("synced_at must advance to the sync instant, got %v", row2.SyncedAt)
	}
}

// 同时刻快照是同批等价新鲜度：重复 attach 幂等刷新不被守卫误拦。
func TestIssuePullRequestUpsert_EqualSyncedAtRefreshes(t *testing.T) {
	db, repo := setupIssuePullRequestTestDB(t)
	issue := createIssuePullRequestTestIssue(t, db, "project-1")

	at := time.Now().UTC().Add(-time.Hour)
	seedIssuePullRequestAt(t, db, issue.ID, 7, model.IssuePullRequestLinkOriginManual, at, "first", model.IssuePullRequestStateOpen)

	sameTime := &model.IssuePullRequest{
		ID: uuid.NewString(), IssueID: issue.ID, ProjectID: issue.ProjectID,
		Provider: model.IssuePullRequestProviderGitHub, RepoFullName: "owner/repo", Number: 7,
		Title: "second", State: model.IssuePullRequestStateOpen,
		LinkOrigin: model.IssuePullRequestLinkOriginManual,
		SyncedAt:   at, CreatedAt: at, UpdatedAt: at,
	}
	if err := repo.Upsert(sameTime); err != nil {
		t.Fatalf("equal-time upsert: %v", err)
	}

	row, err := repo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, "", "owner/repo", 7)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if row.Title != "second" {
		t.Fatalf("equal synced_at is same vintage and must refresh, got %+v", row)
	}
	if !row.SyncedAt.Equal(at) {
		t.Fatalf("synced_at must stay at shared vintage, got %v", row.SyncedAt)
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
	stored2, err := repo.FindByUnique(issue.ID, model.IssuePullRequestProviderGitHub, "", "owner/repo", 7)
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

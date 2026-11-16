package repository

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godbobo/fast_ship/server/internal/model"
)

func newDashboardTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Project{}, &model.Issue{}, &model.IssueInternalMeta{},
		&model.IssueRecommendation{}, &model.ReviewRun{}, &model.ReviewPullRequest{},
		&model.Version{}, &model.QuotaAccount{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func dashboardTestUser(t *testing.T, db *gorm.DB) string {
	t.Helper()
	id := uuid.NewString()
	if err := db.Create(&model.User{ID: id, Username: "u" + id[:8], Email: id[:8] + "@example.com", PasswordHash: "x", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func dashboardTestProject(t *testing.T, db *gorm.DB, userID, name string, createdAt time.Time, shelved bool) string {
	t.Helper()
	p := &model.Project{ID: uuid.NewString(), UserID: userID, Name: name, CreatedAt: createdAt, UpdatedAt: createdAt}
	if shelved {
		now := time.Now().UTC()
		until := now.Add(24 * time.Hour)
		p.ShelvedAt = &now
		p.ShelvedUntil = &until
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p.ID
}

func dashboardTestIssue(t *testing.T, db *gorm.DB, projectID, title string) string {
	t.Helper()
	i := &model.Issue{ID: uuid.NewString(), ProjectID: projectID, Title: title, State: model.IssueStateOpen, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.Create(i).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}
	return i.ID
}

// GetOpenIssueCounts：按 open 数降序、同数按项目创建时间倒序（INT-235）。
func TestDashboardOpenIssueCountsOrdering(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	repo := NewDashboardRepository(db)

	old := time.Now().Add(-72 * time.Hour)
	p1 := dashboardTestProject(t, db, userID, "新项目", old.Add(48*time.Hour), false) // 0 open
	p2 := dashboardTestProject(t, db, userID, "老项目", old, false)                   // 2 open
	p3 := dashboardTestProject(t, db, userID, "中项目", old.Add(24*time.Hour), false) // 1 open

	for i := 0; i < 2; i++ {
		iid := dashboardTestIssue(t, db, p2, "issue")
		_ = iid
	}
	dashboardTestIssue(t, db, p3, "issue")

	rows, err := repo.GetOpenIssueCounts(userID)
	if err != nil {
		t.Fatalf("GetOpenIssueCounts: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0].ProjectID != p2 || rows[0].OpenIssueCount != 2 {
		t.Fatalf("expected p2 first with 2 open, got %+v", rows[0])
	}
	if rows[1].ProjectID != p3 || rows[1].OpenIssueCount != 1 {
		t.Fatalf("expected p3 second, got %+v", rows[1])
	}
	if rows[2].ProjectID != p1 || rows[2].OpenIssueCount != 0 {
		t.Fatalf("expected p1 last (0 open), got %+v", rows[2])
	}
}

// ListFailedReviews：只算与 review_pull_requests 同轮次、近 14 天、
// failed/timed_out 的行；搁置项目排除；非本用户排除。
func TestDashboardListFailedReviews(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	other := dashboardTestUser(t, db)
	repo := NewDashboardRepository(db)

	pid := dashboardTestProject(t, db, userID, "项目A", time.Now(), false)
	shelved := dashboardTestProject(t, db, userID, "搁置项目", time.Now(), true)
	otherPid := dashboardTestProject(t, db, other, "他人项目", time.Now(), false)

	now := time.Now().UTC()
	mkRunIn := func(projectID, repoFullName, status string, round int, finished time.Time, title string) {
		r := &model.ReviewRun{
			ID: uuid.NewString(), UserID: userID, ProjectID: projectID, Kind: model.ReviewKindCode,
			RepoFullName: repoFullName, PRNumber: 7, HeadSHA: "sha", RoundNumber: round,
			IntegrationID: "int-1", EnvironmentID: "env", InstanceID: "i", Model: "m",
			ThreadID: "t", LaunchCommandID: "c", InitialMessageID: "m1",
			Status: model.ReviewRunStatus(status), DeadlineAt: now,
			FinishedAt: &finished, PRTitle: title,
			CreatedAt: now, UpdatedAt: now,
		}
		if projectID == otherPid {
			r.UserID = other
		}
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
	mkRun := func(projectID, status string, round int, finished time.Time, title string) {
		mkRunIn(projectID, "o/r", status, round, finished, title)
	}
	mkPR := func(projectID string, round int) {
		rp := &model.ReviewPullRequest{
			ProjectID: projectID, RepoFullName: "o/r", PRNumber: 7, IntegrationID: "int-1",
			UserID: userID, RoundNumber: round, HeadSHA: "sha", UpdatedAt: now,
		}
		if err := db.Create(rp).Error; err != nil {
			t.Fatalf("create rp: %v", err)
		}
	}

	// 命中：当前轮次 failed，14 天内
	mkPR(pid, 2)
	mkRun(pid, "failed", 2, now.Add(-time.Hour), "修复登录页")
	// 不命中：旧轮次 failed（rp.round_number=2，run.round_number=1）
	mkRun(pid, "failed", 1, now.Add(-2*time.Hour), "旧轮次")
	// 不命中：completed
	mkRun(pid, "completed", 2, now.Add(-time.Hour), "成功")
	// 不命中：超过 14 天
	mkRun(pid, "timed_out", 2, now.Add(-16*24*time.Hour), "太旧")
	// 不命中：另一仓库同 pr_number 同轮次（JOIN 必须带 repo_full_name/integration_id）
	mkRunIn(pid, "o/other", "failed", 2, now.Add(-time.Hour), "隔壁仓库")
	// 不命中：轮次相同但 head 已推进（advancePRHeadTx 只推 head 不推轮次）
	mkRunInSHA := func(headSHA string) {
		r := &model.ReviewRun{
			ID: uuid.NewString(), UserID: userID, ProjectID: pid, Kind: model.ReviewKindCode,
			RepoFullName: "o/r", PRNumber: 7, HeadSHA: headSHA, RoundNumber: 2,
			IntegrationID: "int-1", EnvironmentID: "env", InstanceID: "i", Model: "m",
			ThreadID: "t", LaunchCommandID: "c", InitialMessageID: "m1",
			Status: model.ReviewRunFailed, DeadlineAt: now,
			FinishedAt: ptrTime(now.Add(-time.Hour)), PRTitle: "旧 head",
			CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
	mkRunInSHA("stale-sha")
	// 去重：同一 PR 同轮次的另一条失败 run（不同模型）不重复计数
	dupRun := &model.ReviewRun{
		ID: uuid.NewString(), UserID: userID, ProjectID: pid, Kind: model.ReviewKindCode,
		RepoFullName: "o/r", PRNumber: 7, HeadSHA: "sha", RoundNumber: 2,
		IntegrationID: "int-1", EnvironmentID: "env", InstanceID: "i2", Model: "m2",
		ThreadID: "t", LaunchCommandID: "c", InitialMessageID: "m1",
		Status: model.ReviewRunFailed, DeadlineAt: now,
		FinishedAt: ptrTime(now.Add(-30 * time.Minute)), PRTitle: "修复登录页",
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(dupRun).Error; err != nil {
		t.Fatalf("create dup run: %v", err)
	}
	// 不命中：搁置项目
	mkPR(shelved, 1)
	mkRun(shelved, "failed", 1, now.Add(-time.Hour), "搁置")
	// 不命中：他人
	mkPR(otherPid, 1)
	mkRun(otherPid, "failed", 1, now.Add(-time.Hour), "他人")

	total, items, err := repo.ListFailedReviews(userID, now.Add(-14*24*time.Hour), 5)
	if err != nil {
		t.Fatalf("ListFailedReviews: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected 1 hit, got total=%d items=%+v", total, items)
	}
	if items[0].PRTitle != "修复登录页" || items[0].ProjectName != "项目A" || items[0].PRNumber != 7 {
		t.Fatalf("unexpected item: %+v", items[0])
	}
}

// ListActiveRecommendations：deferred 排除、搁置排除、priority→updated_at 排序。
func TestDashboardListActiveRecommendations(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	repo := NewDashboardRepository(db)

	pid := dashboardTestProject(t, db, userID, "项目A", time.Now(), false)
	shelved := dashboardTestProject(t, db, userID, "搁置", time.Now(), true)

	mkRec := func(projectID, title, priority string, deferred bool, updatedAt time.Time) {
		iid := dashboardTestIssue(t, db, projectID, title)
		rec := &model.IssueRecommendation{
			IssueID: iid, ProjectID: projectID, Priority: model.IssueRecommendationPriority(priority),
			CreatedAt: updatedAt, UpdatedAt: updatedAt,
		}
		if deferred {
			rec.DeferredAt = &updatedAt
		}
		if err := db.Create(rec).Error; err != nil {
			t.Fatalf("create rec: %v", err)
		}
	}

	now := time.Now().UTC()
	mkRec(pid, "低优先", "low", false, now)
	mkRec(pid, "高优先旧", "high", false, now.Add(-2*time.Hour))
	mkRec(pid, "高优先新", "high", false, now.Add(-time.Hour))
	mkRec(pid, "已延后", "high", true, now)
	mkRec(shelved, "搁置项目推荐", "high", false, now)

	total, items, err := repo.ListActiveRecommendations(userID, 5)
	if err != nil {
		t.Fatalf("ListActiveRecommendations: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("expected 3 active recs, got total=%d items=%+v", total, items)
	}
	if items[0].IssueTitle != "高优先新" || items[1].IssueTitle != "高优先旧" || items[2].IssueTitle != "低优先" {
		t.Fatalf("bad ordering: %+v", items)
	}
	if items[0].ProjectName != "项目A" {
		t.Fatalf("missing project name: %+v", items[0])
	}
}

// ListFailedShips：pending+ship_status=failed（发货失败）或 hooks=failed/
// incomplete（已发货钩子失败/未齐，生产上 hooks 只在发货后写入，所以后者
// 恒为 shipped 行）；健康的行与搁置排除。
func TestDashboardListFailedShips(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	repo := NewDashboardRepository(db)

	pid := dashboardTestProject(t, db, userID, "项目A", time.Now(), false)
	shelved := dashboardTestProject(t, db, userID, "搁置", time.Now(), true)

	mkVersion := func(projectID, num string, status model.VersionStatus, ship model.ShipStatus, hooks string) {
		v := &model.Version{
			ID: uuid.NewString(), ProjectID: projectID, VersionNumber: num,
			Status: status, ShipStatus: ship, ShipHooksStatus: hooks,
			CreatedAt: time.Now(),
		}
		if err := db.Create(v).Error; err != nil {
			t.Fatalf("create version: %v", err)
		}
	}

	mkVersion(pid, "v1.0.0", model.VersionStatusPending, model.ShipStatusFailed, "pending")       // 发货失败
	mkVersion(pid, "v1.1.0", model.VersionStatusShipped, model.ShipStatusCompleted, "incomplete") // 已发货但钩子未齐
	mkVersion(pid, "v1.2.0", model.VersionStatusPending, model.ShipStatusCompleted, "completed")  // 健康，不出现
	mkVersion(pid, "v1.3.0", model.VersionStatusShipped, model.ShipStatusCompleted, "failed")     // 已发货钩子失败
	mkVersion(shelved, "v9.9.9", model.VersionStatusPending, model.ShipStatusFailed, "pending")

	total, items, err := repo.ListFailedShips(userID, 5)
	if err != nil {
		t.Fatalf("ListFailedShips: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("expected 3 failed ships, got total=%d items=%+v", total, items)
	}
}

// ListAttentionQuotaAccounts：只有 credential_invalid / provider_error 命中。
func TestDashboardListAttentionQuotaAccounts(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	repo := NewDashboardRepository(db)

	mk := func(label, status string) {
		a := &model.QuotaAccount{
			ID: uuid.NewString(), UserID: userID, Provider: "claude", Label: label,
			CredentialsEncrypted: []byte("x"), CredentialHint: "1234", PlanName: "pro",
			Status: status, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}
		if err := db.Create(a).Error; err != nil {
			t.Fatalf("create quota account: %v", err)
		}
	}

	mk("失效账号", model.QuotaStatusCredentialInvalid)
	mk("报错账号", model.QuotaStatusProviderError)
	mk("健康账号", model.QuotaStatusOK)
	mk("待抓取", model.QuotaStatusPending)

	total, items, err := repo.ListAttentionQuotaAccounts(userID, 5)
	if err != nil {
		t.Fatalf("ListAttentionQuotaAccounts: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2 attention accounts, got total=%d items=%+v", total, items)
	}
}

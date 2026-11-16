package repository

import (
	"fmt"
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
		&model.Version{}, &model.QuotaAccount{}, &model.IssuePullRequest{},
		&model.BotScheduleTask{}, &model.Bot{},
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

// ListFailedReviews 按 PR 去重的 items 必须取满 limit：一个 PR 同轮次可有多条
// run（code/visual × 多模型），若先 Limit 再去重会让条目数少于角标计数。
func TestDashboardListFailedReviews_FillsLimitAcrossDupRuns(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	repo := NewDashboardRepository(db)
	pid := dashboardTestProject(t, db, userID, "项目A", time.Now(), false)

	now := time.Now().UTC()
	const prs = 7
	const runsPerPR = 9 // 超过旧实现 limit*8 窗口下单个 PR 能塞满的行数
	for pr := 1; pr <= prs; pr++ {
		if err := db.Create(&model.ReviewPullRequest{
			ProjectID: pid, RepoFullName: "o/r", PRNumber: pr, IntegrationID: "int-1",
			UserID: userID, RoundNumber: 1, HeadSHA: "sha", UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("create rp: %v", err)
		}
		for k := 0; k < runsPerPR; k++ {
			fin := now.Add(-time.Duration(pr*100+k) * time.Minute)
			if err := db.Create(&model.ReviewRun{
				ID: uuid.NewString(), UserID: userID, ProjectID: pid, Kind: model.ReviewKindCode,
				RepoFullName: "o/r", PRNumber: pr, HeadSHA: "sha", RoundNumber: 1,
				IntegrationID: "int-1", EnvironmentID: "env", InstanceID: fmt.Sprintf("i%d-%d", pr, k),
				Model: "m", ThreadID: "t", LaunchCommandID: "c", InitialMessageID: "m1",
				Status: model.ReviewRunFailed, DeadlineAt: now, FinishedAt: &fin,
				PRTitle: fmt.Sprintf("PR%d", pr), CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("create run: %v", err)
			}
		}
	}

	total, items, err := repo.ListFailedReviews(userID, now.Add(-14*24*time.Hour), 5)
	if err != nil {
		t.Fatalf("ListFailedReviews: %v", err)
	}
	if total != prs {
		t.Fatalf("expected total=%d, got %d", prs, total)
	}
	if len(items) != 5 {
		t.Fatalf("expected 5 deduped items, got %d", len(items))
	}
	seen := map[int]bool{}
	for _, it := range items {
		if seen[it.PRNumber] {
			t.Fatalf("duplicate PR %d in items", it.PRNumber)
		}
		seen[it.PRNumber] = true
	}
	if items[0].PRNumber != 1 {
		t.Fatalf("expected newest PR first (pr 1), got %d", items[0].PRNumber)
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

// ListPendingVersions：status=pending 按 created_at 升序，count 为全量；搁置
// 项目与非本用户排除；shipped 不混入。
func TestDashboardListPendingVersions(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	other := dashboardTestUser(t, db)
	now := time.Now().UTC()

	mkProject := func(name, owner string, shelved bool) string {
		return dashboardTestProject(t, db, owner, name, now, shelved)
	}
	pid := mkProject("项目A", userID, false)
	shelved := mkProject("搁置项目", userID, true)
	otherPid := mkProject("他人项目", other, false)

	mkVersion := func(projectID, number string, status model.VersionStatus, created time.Time) {
		if err := db.Create(&model.Version{
			ID: uuid.NewString(), ProjectID: projectID, VersionNumber: number,
			Status: status, CreatedAt: created,
		}).Error; err != nil {
			t.Fatalf("create version: %v", err)
		}
	}
	mkVersion(pid, "v3.0.0", model.VersionStatusPending, now.Add(-time.Hour))
	mkVersion(pid, "v2.0.0", model.VersionStatusPending, now.Add(-3*time.Hour))
	mkVersion(pid, "v1.0.0", model.VersionStatusShipped, now.Add(-2*time.Hour))
	mkVersion(shelved, "v8.8.8", model.VersionStatusPending, now.Add(-4*time.Hour))
	mkVersion(otherPid, "v7.7.7", model.VersionStatusPending, now.Add(-5*time.Hour))

	repo := NewDashboardRepository(db)
	total, items, err := repo.ListPendingVersions(userID, 3)
	if err != nil {
		t.Fatalf("ListPendingVersions: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("total=%d items=%d, want 2/2", total, len(items))
	}
	if items[0].VersionNumber != "v2.0.0" || items[1].VersionNumber != "v3.0.0" {
		t.Fatalf("must be created_at ascending: %+v", items)
	}
}

// ListRecentReleases：按 shipped_at 降序取前 N，**不加时间窗口**——40 天前的
// 发版也在列表里（窗口会因文本存储的时间写法错位漏行）。
func TestDashboardListRecentReleases(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	now := time.Now().UTC()
	pid := dashboardTestProject(t, db, userID, "项目A", now, false)

	mkShipped := func(number string, shipped time.Time) {
		if err := db.Create(&model.Version{
			ID: uuid.NewString(), ProjectID: pid, VersionNumber: number,
			Status: model.VersionStatusShipped, CreatedAt: shipped.Add(-time.Hour), ShippedAt: &shipped,
		}).Error; err != nil {
			t.Fatalf("create version: %v", err)
		}
	}
	mkShipped("v1.0.0", now.Add(-24*time.Hour))
	mkShipped("v1.1.0", now.Add(-40*24*time.Hour))
	// pending 与无 shipped_at 的行不进列表
	if err := db.Create(&model.Version{
		ID: uuid.NewString(), ProjectID: pid, VersionNumber: "v2.0.0",
		Status: model.VersionStatusPending, CreatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create version: %v", err)
	}

	repo := NewDashboardRepository(db)
	items, err := repo.ListRecentReleases(userID, 5)
	if err != nil {
		t.Fatalf("ListRecentReleases: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items=%d, want 2", len(items))
	}
	if items[0].VersionNumber != "v1.0.0" || items[1].VersionNumber != "v1.1.0" {
		t.Fatalf("must be shipped_at descending (no window): %+v", items)
	}
}

// ListRecentReleases 混合时区排序回归：字典序会把带偏移的历史行排错
//（"2026-05-21T02:00:00+08:00" > "2026-05-20T20:00:00Z"，但真实时间前者更早），
// 必须在 Go 侧解析后按真实时间倒序；解析失败的行垫底不丢弃。
func TestDashboardListRecentReleasesMixedTimezones(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	now := time.Now().UTC()
	pid := dashboardTestProject(t, db, userID, "项目A", now, false)

	// 直接写历史文本形态，绕开驱动的时间转换。
	mkRawShipped := func(number, rawShipped string) string {
		id := uuid.NewString()
		if err := db.Create(&model.Version{
			ID: id, ProjectID: pid, VersionNumber: number,
			Status: model.VersionStatusShipped, CreatedAt: now,
		}).Error; err != nil {
			t.Fatalf("create version: %v", err)
		}
		if err := db.Exec("UPDATE versions SET shipped_at = ? WHERE id = ?", rawShipped, id).Error; err != nil {
			t.Fatalf("write raw shipped_at: %v", err)
		}
		return id
	}
	// 字典序最大的行真实时间最晚：+08:00 偏移行（= 2026-05-20T18:00:00Z）
	mkRawShipped("v1.0.0", "2026-05-21T02:00:00+08:00")
	mkRawShipped("v1.1.0", "2026-05-20T20:00:00Z")
	mkRawShipped("v1.2.0", "不是时间")

	repo := NewDashboardRepository(db)
	items, err := repo.ListRecentReleases(userID, 5)
	if err != nil {
		t.Fatalf("ListRecentReleases: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("items=%d, want 3（解析失败的行不丢弃）", len(items))
	}
	if items[0].VersionNumber != "v1.1.0" || items[1].VersionNumber != "v1.0.0" || items[2].VersionNumber != "v1.2.0" {
		t.Fatalf("must be real-time descending with unparseable last: %+v", items)
	}
	// 偏移行解析后归一到真实时刻（服务层会 .UTC() 输出）
	if items[1].ShippedAt == nil || !items[1].ShippedAt.Equal(time.Date(2026, 5, 20, 18, 0, 0, 0, time.UTC)) {
		t.Fatalf("+08:00 row must parse to its instant: %+v", items[1].ShippedAt)
	}
	if items[2].ShippedAt != nil {
		t.Fatalf("unparseable row must keep ShippedAt nil: %+v", items[2].ShippedAt)
	}

	top2, err := repo.ListRecentReleases(userID, 2)
	if err != nil {
		t.Fatalf("ListRecentReleases limit: %v", err)
	}
	if len(top2) != 2 || top2[0].VersionNumber != "v1.1.0" || top2[1].VersionNumber != "v1.0.0" {
		t.Fatalf("top-2 must cut the unparseable row: %+v", top2)
	}
}

// CountUnmergedPRIssues：开启且至少关联一个非 merged PR（closed 未合并也算），
// 按 issue 去重、搁置项目排除、非本用户排除。
func TestDashboardCountUnmergedPRIssues(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	other := dashboardTestUser(t, db)
	now := time.Now().UTC()
	pid := dashboardTestProject(t, db, userID, "项目A", now, false)
	shelved := dashboardTestProject(t, db, userID, "搁置项目", now, true)
	otherPid := dashboardTestProject(t, db, other, "他人项目", now, false)

	mkIssueWithPR := func(projectID string, state model.IssueState, prStates ...model.IssuePullRequestState) {
		iid := dashboardTestIssue(t, db, projectID, "i")
		if err := db.Model(&model.Issue{}).Where("id = ?", iid).Update("state", state).Error; err != nil {
			t.Fatalf("update issue: %v", err)
		}
		for i, ps := range prStates {
			if err := db.Create(&model.IssuePullRequest{
				ID: uuid.NewString(), IssueID: iid, ProjectID: projectID, RepoFullName: "o/r",
				Number: i + 1, State: ps, SyncedAt: now, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatalf("create pr link: %v", err)
			}
		}
	}
	mkIssueWithPR(pid, model.IssueStateOpen, model.IssuePullRequestStateOpen, model.IssuePullRequestStateClosed)
	mkIssueWithPR(pid, model.IssueStateOpen, model.IssuePullRequestStateMerged)
	mkIssueWithPR(pid, model.IssueStateClosed, model.IssuePullRequestStateOpen)
	mkIssueWithPR(shelved, model.IssueStateOpen, model.IssuePullRequestStateOpen)
	mkIssueWithPR(otherPid, model.IssueStateOpen, model.IssuePullRequestStateOpen)

	repo := NewDashboardRepository(db)
	total, err := repo.CountUnmergedPRIssues(userID)
	if err != nil {
		t.Fatalf("CountUnmergedPRIssues: %v", err)
	}
	if total != 1 {
		t.Fatalf("total=%d, want 1", total)
	}
}

// GetWorkflowBuckets：空串/NULL meta 归 todo，非空非 done 归 in_progress；
// 恒等式 todo+in_progress == ΣGetOpenIssueCounts。
func TestDashboardWorkflowBucketsIdentity(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	now := time.Now().UTC()
	pid := dashboardTestProject(t, db, userID, "项目A", now, false)
	shelved := dashboardTestProject(t, db, userID, "搁置项目", now, true)

	mkOpenMeta := func(projectID string, status model.IssueWorkflowStatus) {
		iid := dashboardTestIssue(t, db, projectID, "i")
		if status == "" {
			return // 无 meta 行 = NULL JOIN
		}
		if err := db.Create(&model.IssueInternalMeta{IssueID: iid, WorkflowStatus: status, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatalf("create meta: %v", err)
		}
	}
	mkOpenMeta(pid, "") // 无 meta 行
	iid := dashboardTestIssue(t, db, pid, "i")
	if err := db.Create(&model.IssueInternalMeta{IssueID: iid, WorkflowStatus: "", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create meta: %v", err)
	}
	mkOpenMeta(pid, model.IssueWorkflowStatusTodo)
	mkOpenMeta(pid, model.IssueWorkflowStatusInProgress)
	mkOpenMeta(pid, model.IssueWorkflowStatusDone) // 排除
	mkOpenMeta(shelved, model.IssueWorkflowStatusInProgress)

	repo := NewDashboardRepository(db)
	buckets, err := repo.GetWorkflowBuckets(userID)
	if err != nil {
		t.Fatalf("GetWorkflowBuckets: %v", err)
	}
	if buckets.TodoCount != 3 || buckets.InProgressCount != 1 {
		t.Fatalf("buckets=%+v, want todo=3 in_progress=1", buckets)
	}

	openCounts, err := repo.GetOpenIssueCounts(userID)
	if err != nil {
		t.Fatalf("GetOpenIssueCounts: %v", err)
	}
	sum := 0
	for _, row := range openCounts {
		sum += row.OpenIssueCount
	}
	if buckets.TodoCount+buckets.InProgressCount != sum {
		t.Fatalf("identity broken: %d+%d != %d", buckets.TodoCount, buckets.InProgressCount, sum)
	}
}

// GetDailyCreatedCounts：只算 source=internal；窗口与 daily_resolved 同参。
func TestDashboardDailyCreatedCountsInternalOnly(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	now := time.Now().UTC()
	pid := dashboardTestProject(t, db, userID, "项目A", now, false)

	today := now.Truncate(24 * time.Hour)
	mkIssue := func(source model.IssueSource, created time.Time) {
		if err := db.Create(&model.Issue{ID: uuid.NewString(), ProjectID: pid, Title: "i", State: model.IssueStateOpen, Source: source, CreatedAt: created, UpdatedAt: created}).Error; err != nil {
			t.Fatalf("create issue: %v", err)
		}
	}
	mkIssue(model.IssueSourceInternal, today)
	mkIssue(model.IssueSourceInternal, today.Add(-24*time.Hour))
	mkIssue(model.IssueSourceGitHub, today)                         // 排除
	mkIssue(model.IssueSourceInternal, today.Add(-31*24*time.Hour)) // 窗口外

	repo := NewDashboardRepository(db)
	start := today.AddDate(0, 0, -29)
	end := today.AddDate(0, 0, 1)
	rows, err := repo.GetDailyCreatedCounts(userID, start, end)
	if err != nil {
		t.Fatalf("GetDailyCreatedCounts: %v", err)
	}
	total := 0
	for _, row := range rows {
		total += row.CreatedCount
	}
	if total != 2 {
		t.Fatalf("created total=%d, want 2（github 与窗口外排除）", total)
	}
}

// GetCycleTimeSamples：窗口内 completed 且有 started_at；无 started_at 排除。
func TestDashboardCycleTimeSamples(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	now := time.Now().UTC()
	pid := dashboardTestProject(t, db, userID, "项目A", now, false)

	today := now.Truncate(24 * time.Hour)
	mkCompleted := func(started *time.Time, completed *time.Time) {
		iid := dashboardTestIssue(t, db, pid, "i")
		meta := &model.IssueInternalMeta{IssueID: iid, WorkflowStatus: model.IssueWorkflowStatusDone, CreatedAt: now, UpdatedAt: now, StartedAt: started, CompletedAt: completed}
		if err := db.Create(meta).Error; err != nil {
			t.Fatalf("create meta: %v", err)
		}
	}
	mkCompleted(ptrTime(today.Add(-48*time.Hour)), ptrTime(today.Add(-24*time.Hour)))
	mkCompleted(nil, ptrTime(today.Add(-time.Hour)))                                        // 无 started_at 排除
	mkCompleted(ptrTime(today.Add(-40*24*time.Hour)), ptrTime(today.Add(-39*24*time.Hour))) // 窗口外

	repo := NewDashboardRepository(db)
	rows, err := repo.GetCycleTimeSamples(userID, today.AddDate(0, 0, -29), today.AddDate(0, 0, 1))
	if err != nil {
		t.Fatalf("GetCycleTimeSamples: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("samples=%d, want 1", len(rows))
	}
	if rows[0].ProjectID != pid || !rows[0].CompletedAt.After(rows[0].StartedAt) {
		t.Fatalf("bad sample: %+v", rows[0])
	}
}

// GetBacklogAge：三档之和 == 开启未 done 总数（与 GetOpenIssueCounts 同口径）；
// 边界由 Go 传参。
func TestDashboardBacklogAgeIdentity(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	now := time.Now().UTC()
	pid := dashboardTestProject(t, db, userID, "项目A", now, false)

	mkOpen := func(created time.Time) {
		if err := db.Create(&model.Issue{ID: uuid.NewString(), ProjectID: pid, Title: "i", State: model.IssueStateOpen, CreatedAt: created, UpdatedAt: created}).Error; err != nil {
			t.Fatalf("create issue: %v", err)
		}
	}
	mkOpen(now.Add(-24 * time.Hour))
	mkOpen(now.Add(-10 * 24 * time.Hour))
	mkOpen(now.Add(-40 * 24 * time.Hour))
	// workflow done：不进任何档
	done := dashboardTestIssue(t, db, pid, "i")
	if err := db.Create(&model.IssueInternalMeta{IssueID: done, WorkflowStatus: model.IssueWorkflowStatusDone, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatalf("create meta: %v", err)
	}

	repo := NewDashboardRepository(db)
	age, err := repo.GetBacklogAge(userID, now.Add(-7*24*time.Hour), now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("GetBacklogAge: %v", err)
	}
	if age.Under7d != 1 || age.D7To30d != 1 || age.Over30d != 1 {
		t.Fatalf("age=%+v, want 1/1/1", age)
	}
	openCounts, err := repo.GetOpenIssueCounts(userID)
	if err != nil {
		t.Fatalf("GetOpenIssueCounts: %v", err)
	}
	sum := 0
	for _, row := range openCounts {
		sum += row.OpenIssueCount
	}
	if age.Under7d+age.D7To30d+age.Over30d != sum {
		t.Fatalf("identity broken: %d != %d", age.Under7d+age.D7To30d+age.Over30d, sum)
	}
}

// GetReviewOutcomes：当前轮次+当前 head 全键分组、max finished_at 与在途 run
// 计数；搁置项目排除；非本用户排除。
func TestDashboardGetReviewOutcomes(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	now := time.Now().UTC()
	pid := dashboardTestProject(t, db, userID, "项目A", now, false)
	shelved := dashboardTestProject(t, db, userID, "搁置项目", now, true)

	mkPR := func(projectID string, prNumber, round int, codeStatus string) {
		if err := db.Create(&model.ReviewPullRequest{
			ProjectID: projectID, RepoFullName: "o/r", PRNumber: prNumber, IntegrationID: "int-1",
			UserID: userID, RoundNumber: round, HeadSHA: "sha", CodeStatus: codeStatus, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("create rp: %v", err)
		}
	}
	mkRunAt := func(projectID, status string, round int, finished *time.Time, prNumber int) {
		r := &model.ReviewRun{
			ID: uuid.NewString(), UserID: userID, ProjectID: projectID, Kind: model.ReviewKindCode,
			RepoFullName: "o/r", PRNumber: prNumber, HeadSHA: "sha", RoundNumber: round,
			IntegrationID: "int-1", EnvironmentID: "env", InstanceID: "i", Model: "m",
			ThreadID: "t", LaunchCommandID: "c", InitialMessageID: "m1",
			Status: model.ReviewRunStatus(status), DeadlineAt: now,
			FinishedAt: finished, CreatedAt: now, UpdatedAt: now,
		}
		if err := db.Create(r).Error; err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
	mkRun := func(projectID, status string, round int, finished *time.Time) {
		mkRunAt(projectID, status, round, finished, 7)
	}

	// PR#7：当前轮次 success（终态）+ 更早轮次不参与（rp.round=2）
	mkPR(pid, 7, 2, "success")
	mkRun(pid, "completed", 2, ptrTime(now.Add(-2*time.Hour)))
	mkRun(pid, "failed", 1, ptrTime(now.Add(-3*time.Hour)))
	// PR#8：pending 且有在途 run
	mkPR(pid, 8, 1, "pending")
	mkRunAt(pid, "running", 1, nil, 8)
	// 搁置项目：不进结果
	mkPR(shelved, 7, 1, "failure")
	mkRun(shelved, "completed", 1, ptrTime(now.Add(-time.Hour)))

	// 他人项目里的同 PR：r.user_id 过滤已排除，p.user_id 是第二道防线
	//（review_runs.user_id 与 projects.user_id 必须同向，防历史脏数据泄漏）。
	other := dashboardTestUser(t, db)
	otherPID := dashboardTestProject(t, db, other, "他人项目", now, false)
	if err := db.Create(&model.ReviewPullRequest{
		ProjectID: otherPID, RepoFullName: "o/r", PRNumber: 7, IntegrationID: "int-1",
		UserID: other, RoundNumber: 1, HeadSHA: "sha", CodeStatus: "success", UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create other rp: %v", err)
	}
	if err := db.Create(&model.ReviewRun{
		ID: uuid.NewString(), UserID: other, ProjectID: otherPID, Kind: model.ReviewKindCode,
		RepoFullName: "o/r", PRNumber: 7, HeadSHA: "sha", RoundNumber: 1,
		IntegrationID: "int-1", EnvironmentID: "env", InstanceID: "i", Model: "m",
		ThreadID: "t", LaunchCommandID: "c", InitialMessageID: "m1",
		Status: model.ReviewRunCompleted, DeadlineAt: now,
		FinishedAt: ptrTime(now.Add(-time.Hour)), CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("create other run: %v", err)
	}

	repo := NewDashboardRepository(db)
	rows, err := repo.GetReviewOutcomes(userID, now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatalf("GetReviewOutcomes: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d, want 2（搁置排除、旧轮次被全键连接过滤）", len(rows))
	}
	byPR := map[int]DashboardReviewPRRow{}
	for _, row := range rows {
		byPR[row.PRNumber] = row
	}
	p7 := byPR[7]
	if p7.CodeStatus != "success" || p7.ActiveRuns != 0 {
		t.Fatalf("PR7 row: %+v", p7)
	}
	p8 := byPR[8]
	if p8.ActiveRuns != 1 || p8.CodeStatus != "pending" {
		t.Fatalf("PR8 row must carry active run: %+v", p8)
	}
}

// GetNextScheduleTask：最近一次将触发的已启用任务；停用/无 next_run_at/他人排除。
func TestDashboardGetNextScheduleTask(t *testing.T) {
	db := newDashboardTestDB(t)
	userID := dashboardTestUser(t, db)
	other := dashboardTestUser(t, db)
	now := time.Now().UTC()

	mkBot := func(owner, name string) string {
		id := uuid.NewString()
		if err := db.Create(&model.Bot{ID: id, UserID: owner, Name: name, PersonaPrompt: "p", Model: "m", ProviderID: "pv", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatalf("create bot: %v", err)
		}
		return id
	}
	botID := mkBot(userID, "值夜机器人")
	otherBot := mkBot(other, "他人机器人")

	mkTask := func(botID, name string, enabled bool, next *time.Time) {
		id := uuid.NewString()
		if err := db.Create(&model.BotScheduleTask{
			ID: id, BotID: botID, Name: name, CronExpression: "* * * * *",
			Enabled: enabled, NextRunAt: next, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("create task: %v", err)
		}
		if !enabled {
			// Enabled 有 default:true：零值 false 会被默认值覆盖，显式回写
			if err := db.Model(&model.BotScheduleTask{}).Where("id = ?", id).Update("enabled", false).Error; err != nil {
				t.Fatalf("disable task: %v", err)
			}
		}
	}
	mkTask(botID, "晚报", true, ptrTime(now.Add(2*time.Hour)))
	mkTask(botID, "早报", true, ptrTime(now.Add(1*time.Hour))) // 更早，应中选
	mkTask(botID, "停用", false, ptrTime(now.Add(30*time.Minute)))
	mkTask(botID, "无下次", true, nil)
	mkTask(otherBot, "他人任务", true, ptrTime(now.Add(10*time.Minute)))

	repo := NewDashboardRepository(db)
	task, err := repo.GetNextScheduleTask(userID)
	if err != nil {
		t.Fatalf("GetNextScheduleTask: %v", err)
	}
	if task == nil || task.TaskName != "早报" || task.BotName != "值夜机器人" {
		t.Fatalf("task=%+v, want 早报/值夜机器人", task)
	}

	// 没有任何任务的用户 → nil
	loner := dashboardTestUser(t, db)
	if task, err := repo.GetNextScheduleTask(loner); err != nil || task != nil {
		t.Fatalf("empty user: task=%+v err=%v, want nil/nil", task, err)
	}
}

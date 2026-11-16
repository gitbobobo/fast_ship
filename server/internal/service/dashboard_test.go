package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/repository"
)

// fakeT3WaitingSource 窄接口假实现：按预设返回结果/错误。
type fakeT3WaitingSource struct {
	result *T3WaitingResult
	err    error
}

func (f *fakeT3WaitingSource) WaitingThreads(context.Context, string) (*T3WaitingResult, error) {
	return f.result, f.err
}

type fakeUnreadCounter struct {
	count int64
	err   error
}

func (f *fakeUnreadCounter) UnreadCount(context.Context, string) (int64, error) {
	return f.count, f.err
}

// fakeQuotaLister 额度快照假实现。
type fakeQuotaLister struct {
	accounts []api.QuotaAccount
	err      error
}

func (f *fakeQuotaLister) List(string) ([]api.QuotaAccount, error) {
	return f.accounts, f.err
}

// fakeBotUsageSource 机器人用量假实现。
type fakeBotUsageSource struct {
	overview *api.BotUsageOverview
	err      error
}

func (f *fakeBotUsageSource) GetBotUsageOverview(string, string, string) (*api.BotUsageOverview, error) {
	return f.overview, f.err
}

func newDashboardServiceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	cleanupTestDB(t, db)
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

func newDashboardService(t *testing.T, db *gorm.DB, unread dashboardUnreadCounter, t3 dashboardT3WaitingSource) *DashboardService {
	t.Helper()
	// 默认不带 quota/bots 窄接口：AI 面板子块缺席，既有用例不受影响。
	return NewDashboardService(repository.NewDashboardRepository(db), unread, t3, nil, nil)
}

// newDashboardServiceWithAI 注入 quota/bots 假实现，供 AI 面板用例使用。
func newDashboardServiceWithAI(
	t *testing.T,
	db *gorm.DB,
	unread dashboardUnreadCounter,
	t3 dashboardT3WaitingSource,
	quota dashboardQuotaLister,
	bots dashboardBotUsageSource,
) *DashboardService {
	t.Helper()
	return NewDashboardService(repository.NewDashboardRepository(db), unread, t3, quota, bots)
}

func dashboardSeedUser(t *testing.T, db *gorm.DB) string {
	t.Helper()
	id := uuid.NewString()
	if err := db.Create(&model.User{ID: id, Username: "u" + id[:8], Email: id[:8] + "@example.com", PasswordHash: "x", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func dashboardSeedProject(t *testing.T, db *gorm.DB, userID, name string) string {
	t.Helper()
	id := uuid.NewString()
	if err := db.Create(&model.Project{ID: id, UserID: userID, Name: name, CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	return id
}

func dashboardSeedIssue(t *testing.T, db *gorm.DB, projectID string, created time.Time) string {
	t.Helper()
	id := uuid.NewString()
	if err := db.Create(&model.Issue{ID: id, ProjectID: projectID, Title: "i-" + id[:8], State: model.IssueStateOpen, CreatedAt: created, UpdatedAt: created}).Error; err != nil {
		t.Fatalf("create issue: %v", err)
	}
	return id
}

func groupKinds(groups []DashboardActionItemGroup) []api.DashboardActionItemKind {
	kinds := make([]api.DashboardActionItemKind, 0, len(groups))
	for _, g := range groups {
		kinds = append(kinds, g.Kind)
	}
	return kinds
}

func findGroup(groups []DashboardActionItemGroup, kind api.DashboardActionItemKind) *DashboardActionItemGroup {
	for i := range groups {
		if groups[i].Kind == kind {
			return &groups[i]
		}
	}
	return nil
}

// href 集中生成：六组各自的站内路径形态（路由变更时只改这一处）。
func TestDashboardActionItemHref(t *testing.T) {
	cases := []struct {
		kind api.DashboardActionItemKind
		want string
	}{
		{api.T3Waiting, "/threads/env-1/th-1"},
		{api.ReviewFailed, "/reviews?project=p-1"},
		{api.Recommendations, "/projects/p-1/issues/i-1"},
		{api.ShipFailed, "/projects/p-1/versions/v-1"},
		{api.QuotaAttention, "/quota"},
		{api.UnreadNotifications, "/notifications"},
	}
	for _, c := range cases {
		got := dashboardActionItemHref(c.kind, dashboardActionRef{
			projectID: "p-1", envID: "env-1", threadID: "th-1", issueID: "i-1", versionID: "v-1",
		})
		if got != c.want {
			t.Fatalf("kind %s: got %q want %q", c.kind, got, c.want)
		}
	}
}

// 组顺序固定；无 T3 环境（result=nil）时 t3_waiting 组缺席。
func TestDashboardActionGroupsOrderAndNoT3(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil})

	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	want := []api.DashboardActionItemKind{
		api.ReviewFailed, api.Recommendations, api.ShipFailed, api.QuotaAttention, api.UnreadNotifications,
	}
	got := groupKinds(resp.ActionItemGroups)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("group order: got %v want %v", got, want)
	}
	if findGroup(resp.ActionItemGroups, api.T3Waiting) != nil {
		t.Fatal("t3_waiting group must be absent when user has no environments")
	}
	if len(resp.DailyResolved) != dashboardResolvedWindowDays {
		t.Fatalf("daily_resolved must have %d days, got %d", dashboardResolvedWindowDays, len(resp.DailyResolved))
	}
	if resp.DailyResolved[0].ResolvedCount != 0 {
		t.Fatal("daily_resolved point must not carry projects field data")
	}
}

// t3_waiting：items 上限 5、complete 透传、href/detail 形状。
func TestDashboardT3WaitingGroup(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)

	items := make([]T3WaitingItem, 0, 7)
	for i := 0; i < 7; i++ {
		items = append(items, T3WaitingItem{
			EnvironmentID: "env-1", EnvironmentName: "工作站",
			ThreadID: fmt.Sprintf("th-%d", i), Title: fmt.Sprintf("线程 %d", i),
		})
	}
	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{
		result: &T3WaitingResult{Items: items, Complete: false},
	})

	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	g := findGroup(resp.ActionItemGroups, api.T3Waiting)
	if g == nil {
		t.Fatal("missing t3_waiting group")
	}
	if g.Count != 7 || len(g.Items) != 5 {
		t.Fatalf("count=%d items=%d, want 7/5", g.Count, len(g.Items))
	}
	if g.Complete == nil || *g.Complete {
		t.Fatal("complete must be present and false")
	}
	if g.Items[0].Href != "/threads/env-1/th-0" || api.Deref(g.Items[0].Detail) != "工作站" {
		t.Fatalf("bad item: %+v", g.Items[0])
	}
	if resp.ActionItemGroups[0].Kind != api.T3Waiting {
		t.Fatal("t3_waiting must be the first group")
	}
}

// T3 聚合出错 → 组降级为 count=0 complete=false；绝不 500。
func TestDashboardT3WaitingDegradedOnError(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{err: errors.New("boom")})

	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview must not fail: %v", err)
	}
	g := findGroup(resp.ActionItemGroups, api.T3Waiting)
	if g == nil {
		t.Fatal("t3_waiting group must degrade, not disappear")
	}
	if g.Count != 0 || g.Complete == nil || *g.Complete || len(g.Items) != 0 {
		t.Fatalf("degraded group shape wrong: %+v", g)
	}
}

// DB 组查询失败同样只降级该组（complete=false），绝不拖垮整页——与
// 静默清零的区别在于前端会显示「暂不可用」而不是「一切正常」。
func TestDashboardDBGroupDegradedOnError(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	if err := db.Exec("DROP TABLE versions").Error; err != nil {
		t.Fatalf("drop versions: %v", err)
	}
	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil})

	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview must not fail: %v", err)
	}
	g := findGroup(resp.ActionItemGroups, api.ShipFailed)
	if g == nil {
		t.Fatal("ship_failed group must degrade, not disappear")
	}
	if g.Count != 0 || g.Complete == nil || *g.Complete || len(g.Items) != 0 {
		t.Fatalf("degraded group shape wrong: %+v", g)
	}
}

// 各 DB 组计数/条目/href/搁置排除的端到端组装。
func TestDashboardDBGroups(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	now := time.Now().UTC()

	pid := uuid.NewString()
	if err := db.Create(&model.Project{ID: pid, UserID: userID, Name: "项目A", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	// 一条失败审查（同轮次、14 天内）
	finished := now.Add(-time.Hour)
	if err := db.Create(&model.ReviewPullRequest{
		ProjectID: pid, RepoFullName: "o/r", PRNumber: 3, IntegrationID: "int-1",
		UserID: userID, RoundNumber: 1, HeadSHA: "s", UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ReviewRun{
		ID: uuid.NewString(), UserID: userID, ProjectID: pid, Kind: model.ReviewKindCode,
		RepoFullName: "o/r", PRNumber: 3, HeadSHA: "s", RoundNumber: 1,
		IntegrationID: "int-1", EnvironmentID: "e", InstanceID: "i", Model: "m",
		ThreadID: "t", LaunchCommandID: "c", InitialMessageID: "m",
		Status: model.ReviewRunFailed, PRTitle: "修 bug", FinishedAt: &finished,
		DeadlineAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 一条推荐
	iid := uuid.NewString()
	if err := db.Create(&model.Issue{ID: iid, ProjectID: pid, Title: "写文档", State: model.IssueStateOpen, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.IssueRecommendation{IssueID: iid, ProjectID: pid, Priority: model.IssueRecommendationPriorityHigh, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	// 一条发版失败 + 一个失效配额账号
	if err := db.Create(&model.Version{
		ID: uuid.NewString(), ProjectID: pid, VersionNumber: "v2.0.0",
		Status: model.VersionStatusPending, ShipStatus: model.ShipStatusFailed,
		ShipHooksStatus: "pending", CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.QuotaAccount{
		ID: uuid.NewString(), UserID: userID, Provider: "claude", Label: "主账号",
		CredentialsEncrypted: []byte("x"), CredentialHint: "1", PlanName: "pro",
		Status: model.QuotaStatusCredentialInvalid, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 4}, &fakeT3WaitingSource{result: nil})
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}

	if g := findGroup(resp.ActionItemGroups, api.ReviewFailed); g == nil || g.Count != 1 || g.Items[0].Title != "修 bug" || g.Items[0].Href != "/reviews?project="+pid {
		t.Fatalf("review_failed group: %+v", g)
	}
	if g := findGroup(resp.ActionItemGroups, api.Recommendations); g == nil || g.Count != 1 || g.Items[0].Href != "/projects/"+pid+"/issues/"+iid {
		t.Fatalf("recommendations group: %+v", g)
	}
	if g := findGroup(resp.ActionItemGroups, api.ShipFailed); g == nil || g.Count != 1 || g.Items[0].Title != "v2.0.0" {
		t.Fatalf("ship_failed group: %+v", g)
	}
	if g := findGroup(resp.ActionItemGroups, api.QuotaAttention); g == nil || g.Count != 1 || g.Items[0].Href != "/quota" {
		t.Fatalf("quota group: %+v", g)
	}
	g := findGroup(resp.ActionItemGroups, api.UnreadNotifications)
	if g == nil || g.Count != 4 || len(g.Items) != 1 || g.Items[0].Title != "4 条未读通知" || g.Items[0].Href != "/notifications" {
		t.Fatalf("unread group: %+v", g)
	}
}

// WaitingThreads 真实实现：无环境返回 nil；环境未 ready 时 complete=false
// （PeekActive 非阻塞，注册 entry 后即返回 Syncing 视图）。
func TestWaitingThreadsNoEnvAndNotReady(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	result, err := e.th.WaitingThreads(ctx, userID)
	if err != nil || result != nil {
		t.Fatalf("no envs: want nil result, got %+v err=%v", result, err)
	}

	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "工作站", PairingUrl: e.fake.pairingURL(e.fake.baseURL)}); err != nil {
		t.Fatalf("create env: %v", err)
	}
	result, err = e.th.WaitingThreads(ctx, userID)
	if err != nil {
		t.Fatalf("WaitingThreads: %v", err)
	}
	if result == nil || result.Complete {
		t.Fatalf("env not ready yet: want complete=false, got %+v", result)
	}
}

// WaitingThreads：快照 ready 后 waiting 线程进条目（pendingRuntimeRequest
// 与 run 状态 waiting 同口径），running/subagent 排除，detail=环境名。
func TestWaitingThreadsAggregates(t *testing.T) {
	e := setupT3ServiceTest(t)
	userID := e.createUser(t)
	ctx := context.Background()

	e.fake.mu.Lock()
	e.fake.snapshot = shellSnapshotFixture([]any{}, []any{
		shellThread("th-w1", "等待审批", "queued", map[string]any{
			"pendingRuntimeRequest": map[string]any{"id": "rr-1", "kind": "approval", "createdAt": "2026-10-12T04:00:00.000Z"},
		}),
		shellThread("th-w2", "run 等待输入", "idle", map[string]any{
			"activityRunStatus": "waiting",
		}),
		shellThread("th-r", "正在跑", "running", nil),
		shellThread("th-sub", "子代理", "idle", map[string]any{
			"activityRunStatus": "waiting",
			"lineage":           map[string]any{"parentThreadId": "th-w1", "relationshipToParent": "subagent", "rootThreadId": "th-w1"},
		}),
	})
	e.fake.mu.Unlock()

	if _, err := e.env.Create(ctx, userID, &CreateT3EnvironmentRequest{Name: "工作站", PairingUrl: e.fake.pairingURL(e.fake.baseURL)}); err != nil {
		t.Fatalf("create env: %v", err)
	}

	envs, err := e.env.repo.ListByUserID(ctx, userID)
	if err != nil || len(envs) != 1 {
		t.Fatalf("list envs: %v", err)
	}

	// Peek 不等待首同步：轮询到活跃面 ready 再断言聚合结果。
	deadline := time.Now().Add(10 * time.Second)
	var result *T3WaitingResult
	for {
		result, err = e.th.WaitingThreads(ctx, userID)
		if err != nil {
			t.Fatalf("WaitingThreads: %v", err)
		}
		if result != nil && result.Complete {
			break
		}
		if time.Now().After(deadline) {
			view, _ := e.th.PeekShell(userID, envs[0].ID)
			t.Fatalf("timed out waiting for ready, last result=%+v view=%+v", result, view)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(result.Items) != 2 {
		t.Fatalf("expected 2 waiting threads, got %+v", result.Items)
	}
	titles := map[string]bool{}
	for _, it := range result.Items {
		titles[it.Title] = true
		if it.EnvironmentName != "工作站" {
			t.Fatalf("bad env name: %+v", it)
		}
	}
	if !titles["等待审批"] || !titles["run 等待输入"] {
		t.Fatalf("unexpected items: %+v", result.Items)
	}
}

// buildRelease：pending 全量计数+升序截前 3；recent 按 shipped_at 降序；未合并
// PR 关联的开启问题去重、搁置排除；href 走版本路由。
func TestDashboardReleaseBlock(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	other := dashboardSeedUser(t, db)
	now := time.Now().UTC()

	mkProject := func(name string, owner string, shelved bool) string {
		id := uuid.NewString()
		p := &model.Project{ID: id, UserID: owner, Name: name, CreatedAt: now, UpdatedAt: now}
		if shelved {
			s := now
			until := now.Add(24 * time.Hour)
			p.ShelvedAt = &s
			p.ShelvedUntil = &until
		}
		if err := db.Create(p).Error; err != nil {
			t.Fatal(err)
		}
		return id
	}
	pid := mkProject("项目A", userID, false)
	shelvedPid := mkProject("搁置项目", userID, true)
	otherPid := mkProject("他人项目", other, false)

	mkVersion := func(projectID, number string, status model.VersionStatus, created time.Time, shipped *time.Time) {
		if err := db.Create(&model.Version{
			ID: uuid.NewString(), ProjectID: projectID, VersionNumber: number,
			Status: status, CreatedAt: created, ShippedAt: shipped,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// pending 三条：创建顺序与期望展示顺序（升序）相反
	mkVersion(pid, "v3.0.0", model.VersionStatusPending, now.Add(-time.Hour), nil)
	mkVersion(pid, "v2.0.0", model.VersionStatusPending, now.Add(-3*time.Hour), nil)
	mkVersion(shelvedPid, "v9.9.9", model.VersionStatusPending, now.Add(-4*time.Hour), nil)
	// shipped 两条：40 天前的更早，展示时应排第二
	mkVersion(pid, "v1.1.0", model.VersionStatusShipped, now.Add(-41*24*time.Hour), ptrTime(now.Add(-40*24*time.Hour)))
	mkVersion(pid, "v1.0.0", model.VersionStatusShipped, now.Add(-2*24*time.Hour), ptrTime(now.Add(-24*time.Hour)))

	// 未合并 PR 关联：open issue + open PR → 计 1；同 issue 再关联 closed 未合并 PR 仍计 1（按 issue 去重）
	mkIssueWithPR := func(projectID string, state model.IssueState, prState model.IssuePullRequestState) {
		iid := uuid.NewString()
		if err := db.Create(&model.Issue{ID: iid, ProjectID: projectID, Title: "i", State: state, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.IssuePullRequest{
			ID: uuid.NewString(), IssueID: iid, ProjectID: projectID, RepoFullName: "o/r",
			Number: 1, State: prState, SyncedAt: now, CreatedAt: now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	mkIssueWithPR(pid, model.IssueStateOpen, model.IssuePullRequestStateOpen)
	mkIssueWithPR(otherPid, model.IssueStateOpen, model.IssuePullRequestStateOpen)
	// 已合并关联不算
	mkIssueWithPR(pid, model.IssueStateOpen, model.IssuePullRequestStateMerged)
	// 关闭的 issue 不算
	mkIssueWithPR(pid, model.IssueStateClosed, model.IssuePullRequestStateOpen)

	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil})
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	release := resp.Release
	if release == nil {
		t.Fatal("release block missing")
	}
	if release.Complete != nil {
		t.Fatal("release complete must be omitted on success")
	}
	if release.PendingCount != 2 || len(release.PendingVersions) != 2 {
		t.Fatalf("pending: count=%d rows=%d, want 2/2（搁置项目排除）", release.PendingCount, len(release.PendingVersions))
	}
	if release.PendingVersions[0].VersionNumber != "v2.0.0" || release.PendingVersions[1].VersionNumber != "v3.0.0" {
		t.Fatalf("pending must be created_at ascending: %+v", release.PendingVersions)
	}
	if len(release.RecentReleases) != 2 || release.RecentReleases[0].VersionNumber != "v1.0.0" || release.RecentReleases[1].VersionNumber != "v1.1.0" {
		t.Fatalf("recent must be shipped_at descending: %+v", release.RecentReleases)
	}
	if release.RecentReleases[0].Href != "/projects/"+pid+"/versions/"+release.RecentReleases[0].VersionId {
		t.Fatalf("bad href: %s", release.RecentReleases[0].Href)
	}
	if _, err := time.Parse(time.RFC3339, release.RecentReleases[0].ShippedAt); err != nil {
		t.Fatalf("shipped_at must parse: %v", err)
	}
	if release.UnmergedPrIssueCount != 1 {
		t.Fatalf("unmerged_pr_issue_count=%d, want 1（去重/关闭/已合并/他人排除）", release.UnmergedPrIssueCount)
	}
}

// release 块查询失败 → 整块降级 complete=false、结构完整，绝不 500。
func TestDashboardReleaseDegradedOnError(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	if err := db.Exec("DROP TABLE issue_pull_requests").Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}
	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil})
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview must not fail: %v", err)
	}
	if resp.Release == nil {
		t.Fatal("release block must degrade, not disappear")
	}
	if resp.Release.Complete == nil || *resp.Release.Complete {
		t.Fatal("release complete must be present and false")
	}
	if resp.Release.PendingVersions == nil || resp.Release.RecentReleases == nil {
		t.Fatal("degraded release must keep complete structure")
	}
}

// buildFlow：分桶恒等式（todo+in_progress==Σopen）、daily_created 固定 30 条
// 且只算 internal、周期中位数（偶数样本取均值、零时长剔除）、三档年龄之和
// ==Σopen；Σcycle completed_count ≤ Σdaily_resolved。
func TestDashboardFlowBlock(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	now := time.Now().UTC()
	pid := dashboardSeedProject(t, db, userID, "项目A")

	// 3 open（2 默认 todo + 1 显式 todo）+ 1 in_progress + 1 done
	for i := 0; i < 2; i++ {
		dashboardSeedIssue(t, db, pid, now)
	}
	iid := dashboardSeedIssue(t, db, pid, now)
	if err := db.Create(&model.IssueInternalMeta{IssueID: iid, WorkflowStatus: model.IssueWorkflowStatusTodo, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	prog := dashboardSeedIssue(t, db, pid, now)
	if err := db.Create(&model.IssueInternalMeta{IssueID: prog, WorkflowStatus: model.IssueWorkflowStatusInProgress, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	done := dashboardSeedIssue(t, db, pid, now)
	// 周期样本：2 天前 started，1 天前 completed（零时长样本另造一条）
	started := now.Add(-48 * time.Hour)
	completed := now.Add(-24 * time.Hour)
	if err := db.Create(&model.IssueInternalMeta{IssueID: done, WorkflowStatus: model.IssueWorkflowStatusDone, StartedAt: &started, CompletedAt: &completed, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	done2 := dashboardSeedIssue(t, db, pid, now)
	zero := now.Add(-24 * time.Hour)
	if err := db.Create(&model.IssueInternalMeta{IssueID: done2, WorkflowStatus: model.IssueWorkflowStatusDone, StartedAt: &zero, CompletedAt: &zero, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	// 今天创建 2 条 internal + 1 条 github
	dashboardSeedIssue(t, db, pid, now)
	dashboardSeedIssue(t, db, pid, now)
	if err := db.Create(&model.Issue{ID: uuid.NewString(), ProjectID: pid, Title: "gh", State: model.IssueStateOpen, Source: model.IssueSourceGitHub, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil})
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	flow := resp.Flow
	if flow == nil || flow.Complete != nil {
		t.Fatalf("flow must be present and complete: %+v", flow)
	}

	sumOpen := 0
	for _, row := range resp.OpenIssuesByProject {
		sumOpen += row.OpenIssueCount
	}
	if flow.TodoCount+flow.InProgressCount != sumOpen {
		t.Fatalf("identity todo+in_progress=%d != Σopen=%d", flow.TodoCount+flow.InProgressCount, sumOpen)
	}
	if flow.TodoCount != 6 || flow.InProgressCount != 1 {
		t.Fatalf("buckets=%d/%d, want 6/1（显式 todo 归待办；无 meta 的 open 含 github 同步也入桶）", flow.TodoCount, flow.InProgressCount)
	}
	if len(flow.DailyCreated) != dashboardResolvedWindowDays {
		t.Fatalf("daily_created=%d, want %d", len(flow.DailyCreated), dashboardResolvedWindowDays)
	}
	today := now.Truncate(24 * time.Hour).Format("2006-01-02")
	for _, point := range flow.DailyCreated {
		if point.Date == today && point.CreatedCount != 8 {
			t.Fatalf("today created=%d, want 8（全部 internal 创建，github 排除）", point.CreatedCount)
		}
	}
	if len(flow.CycleTimeByProject) != 1 {
		t.Fatalf("cycle rows=%d, want 1（零时长剔除后只剩项目A）", len(flow.CycleTimeByProject))
	}
	cycle := flow.CycleTimeByProject[0]
	if cycle.CompletedCount != 1 || cycle.MedianDays <= 0 {
		t.Fatalf("cycle=%+v, want 1 sample with positive median", cycle)
	}
	// 恒等式：Σcycle completed_count ≤ Σdaily_resolved
	sumResolved := 0
	for _, point := range resp.DailyResolved {
		sumResolved += point.ResolvedCount
	}
	sumCycle := 0
	for _, row := range flow.CycleTimeByProject {
		sumCycle += row.CompletedCount
	}
	if sumCycle > sumResolved {
		t.Fatalf("Σcycle %d > Σresolved %d", sumCycle, sumResolved)
	}
	if flow.BacklogAge.Under7d+flow.BacklogAge.D7To30d+flow.BacklogAge.Over30d != sumOpen {
		t.Fatalf("backlog age identity broken: %+v vs Σopen=%d", flow.BacklogAge, sumOpen)
	}
}

// dashboardCycleTimeByProject：空切片返回空列表；偶数样本取中间两数均值；
// 排序 completed_count 降序、项目名升序；截前 limit。
func TestDashboardCycleTimeMedian(t *testing.T) {
	// 空切片：无项目行
	if got := dashboardCycleTimeByProject(nil, 5); len(got) != 0 {
		t.Fatalf("empty samples must give empty list, got %+v", got)
	}

	// 偶数样本（1、3 天）→ 中位数 2.0
	p1 := repository.DashboardCycleTimeSample{ProjectID: "甲", ProjectName: "甲",
		StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	p1.CompletedAt = p1.StartedAt.Add(24 * time.Hour)
	p2 := repository.DashboardCycleTimeSample{ProjectID: "甲", ProjectName: "甲",
		StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	p2.CompletedAt = p2.StartedAt.Add(72 * time.Hour)
	got := dashboardCycleTimeByProject([]repository.DashboardCycleTimeSample{p1, p2}, 5)
	if len(got) != 1 || got[0].MedianDays != 2.0 || got[0].CompletedCount != 2 {
		t.Fatalf("median=%+v, want 2.0/2", got)
	}

	// 零时长剔除：started==completed 的样本不进计数
	zero := repository.DashboardCycleTimeSample{ProjectID: "乙", ProjectName: "乙",
		StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	got = dashboardCycleTimeByProject([]repository.DashboardCycleTimeSample{zero}, 5)
	if len(got) != 0 {
		t.Fatalf("zero-duration sample must be dropped, got %+v", got)
	}

	// 排序与截断：completed_count 降序，同数按项目名升序
	samples := []repository.DashboardCycleTimeSample{
		repository.DashboardCycleTimeSample{ProjectID: "丙", ProjectName: "丙", StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		repository.DashboardCycleTimeSample{ProjectID: "乙", ProjectName: "乙", StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		repository.DashboardCycleTimeSample{ProjectID: "甲", ProjectName: "甲", StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
		repository.DashboardCycleTimeSample{ProjectID: "甲", ProjectName: "甲", StartedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), CompletedAt: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)},
	}
	got = dashboardCycleTimeByProject(samples, 2)
	// 同 completed_count 按项目名字典序（Unicode 码点：丙 U+4E19 < 乙 U+4E59 < 甲 U+7532）
	if len(got) != 2 || got[0].ProjectName != "甲" || got[0].CompletedCount != 2 || got[1].ProjectName != "丙" {
		t.Fatalf("sort/limit broken: %+v", got)
	}
}

// flow 块子查询失败 → 整块降级 complete=false、结构完整、绝不 500。
// 去掉 started_at 列只打断 GetCycleTimeSamples（概览核心查询不读该列）。
func TestDashboardFlowDegradedOnError(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	if err := db.Exec("ALTER TABLE issue_internal_meta DROP COLUMN started_at").Error; err != nil {
		t.Fatalf("drop column: %v", err)
	}
	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil})
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview must not fail: %v", err)
	}
	if resp.Flow == nil {
		t.Fatal("flow block must degrade, not disappear")
	}
	if resp.Flow.Complete == nil || *resp.Flow.Complete {
		t.Fatal("flow complete must be present and false")
	}
	if resp.Flow.DailyCreated == nil || resp.Flow.CycleTimeByProject == nil {
		t.Fatal("degraded flow must keep complete structure")
	}
}

// buildAiReviews 四分桶与 pass_rate：在途优先；completed 但 code_status=failure
// → blocked（completed ≠ 通过）；分母 0 → pass_rate=null；窗口外排除。
func TestDashboardAiReviewsBuckets(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	now := time.Now().UTC()
	pid := dashboardSeedProject(t, db, userID, "项目A")

	seedPR := func(prNumber int, codeStatus string, runs []struct {
		status   model.ReviewRunStatus
		finished *time.Time
	}) {
		round := 3
		sha := "sha-3"
		if err := db.Create(&model.ReviewPullRequest{
			ProjectID: pid, RepoFullName: "o/r", PRNumber: prNumber, IntegrationID: "int-1",
			UserID: userID, RoundNumber: round, HeadSHA: sha, CodeStatus: codeStatus, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if err := db.Create(&model.ReviewRun{
				ID: uuid.NewString(), UserID: userID, ProjectID: pid, Kind: model.ReviewKindCode,
				RepoFullName: "o/r", PRNumber: prNumber, HeadSHA: sha, RoundNumber: round,
				IntegrationID: "int-1", EnvironmentID: "e", InstanceID: "i", Model: "m",
				ThreadID: "t", LaunchCommandID: "c", InitialMessageID: "m",
				Status: run.status, FinishedAt: run.finished, DeadlineAt: now, CreatedAt: now, UpdatedAt: now,
			}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}

	// PR1 success 终态 → passed
	seedPR(1, "success", []struct {
		status   model.ReviewRunStatus
		finished *time.Time
	}{{model.ReviewRunCompleted, ptrTime(now.Add(-2 * time.Hour))}})
	// PR2 completed 但 code_status=failure → blocked（completed ≠ 通过）
	seedPR(2, "failure", []struct {
		status   model.ReviewRunStatus
		finished *time.Time
	}{{model.ReviewRunCompleted, ptrTime(now.Add(-3 * time.Hour))}})
	// PR3 error → errored
	seedPR(3, "error", []struct {
		status   model.ReviewRunStatus
		finished *time.Time
	}{{model.ReviewRunFailed, ptrTime(now.Add(-4 * time.Hour))}})
	// PR4 pending 且有在途 run → in_flight
	seedPR(4, "pending", []struct {
		status   model.ReviewRunStatus
		finished *time.Time
	}{{model.ReviewRunRunning, nil}})
	// PR5 success 但仍有在途 run → in_flight（在途优先，不落 passed）
	seedPR(5, "success", []struct {
		status   model.ReviewRunStatus
		finished *time.Time
	}{{model.ReviewRunQueued, nil}})
	// PR6 窗口外终态 → 不计
	seedPR(6, "failure", []struct {
		status   model.ReviewRunStatus
		finished *time.Time
	}{{model.ReviewRunCompleted, ptrTime(now.Add(-40 * 24 * time.Hour))}})
	// PR7 pending、终态、窗口内（结论未回写）→ in_flight
	seedPR(7, "", []struct {
		status   model.ReviewRunStatus
		finished *time.Time
	}{{model.ReviewRunCompleted, ptrTime(now.Add(-time.Hour))}})

	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil})
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	reviews := resp.Ai.Reviews
	if reviews == nil || reviews.Complete != nil {
		t.Fatalf("reviews must be present and complete: %+v", reviews)
	}
	if reviews.Passed != 1 || reviews.Blocked != 1 || reviews.Errored != 1 || reviews.InFlight != 3 {
		t.Fatalf("buckets=%+v, want passed=1 blocked=1 errored=1 in_flight=3", reviews)
	}
	if reviews.PassRate == nil || *reviews.PassRate != 0.5 {
		t.Fatalf("pass_rate=%v, want 0.5", reviews.PassRate)
	}
}

// pass_rate 分母为 0（只有 in_flight）→ null，不代表 100%。
func TestDashboardAiReviewsPassRateZeroDenominator(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	now := time.Now().UTC()
	pid := dashboardSeedProject(t, db, userID, "项目A")

	if err := db.Create(&model.ReviewPullRequest{
		ProjectID: pid, RepoFullName: "o/r", PRNumber: 9, IntegrationID: "int-1",
		UserID: userID, RoundNumber: 1, HeadSHA: "s", CodeStatus: "pending", UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.ReviewRun{
		ID: uuid.NewString(), UserID: userID, ProjectID: pid, Kind: model.ReviewKindCode,
		RepoFullName: "o/r", PRNumber: 9, HeadSHA: "s", RoundNumber: 1,
		IntegrationID: "int-1", EnvironmentID: "e", InstanceID: "i", Model: "m",
		ThreadID: "t", LaunchCommandID: "c", InitialMessageID: "m",
		Status: model.ReviewRunQueued, DeadlineAt: now, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil})
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	reviews := resp.Ai.Reviews
	if reviews.InFlight != 1 || reviews.PassRate != nil {
		t.Fatalf("reviews=%+v, want in_flight=1 pass_rate=null", reviews)
	}
}

// 审查查询失败 → reviews 子块降级 complete=false，其余不 500。
func TestDashboardAiReviewsDegradedOnError(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	if err := db.Exec("DROP TABLE review_runs").Error; err != nil {
		t.Fatalf("drop table: %v", err)
	}
	svc := newDashboardService(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil})
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview must not fail: %v", err)
	}
	if resp.Ai == nil || resp.Ai.Reviews == nil {
		t.Fatal("ai/reviews must degrade, not disappear")
	}
	if resp.Ai.Reviews.Complete == nil || *resp.Ai.Reviews.Complete {
		t.Fatal("reviews complete must be present and false")
	}
	if resp.Ai.Complete == nil || *resp.Ai.Complete {
		t.Fatal("ai top-level complete must be false when all sub-blocks are incomplete")
	}
}

// buildAiBots：复用 GetBotUsageOverview 窄接口（7d/时区/请求/token 合计），
// next_schedule 走 DashboardRepository 查询；bot_count=0 合法（未配置机器人）。
func TestDashboardAiBotsBlock(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)
	now := time.Now().UTC()

	botID := uuid.NewString()
	if err := db.Create(&model.Bot{ID: botID, UserID: userID, Name: "值夜机器人", PersonaPrompt: "p", Model: "m", ProviderID: "pv", CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	next := now.Add(2 * time.Hour)
	if err := db.Create(&model.BotScheduleTask{
		ID: uuid.NewString(), BotID: botID, Name: "晚报", CronExpression: "* * * * *",
		Enabled: true, NextRunAt: &next, CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	cost := 1.5
	svc := newDashboardServiceWithAI(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil},
		nil,
		&fakeBotUsageSource{overview: &api.BotUsageOverview{
			Timezone: "Asia/Shanghai",
			Totals: api.BotUsageTotals{
				Cost:          &cost,
				TotalRequests: 12,
				TotalTokens:   3456,
			},
			Bots: []api.BotUsageBot{{BotId: botID, Name: "值夜机器人"}},
		}})
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	bots := resp.Ai.Bots
	if bots == nil || bots.Complete != nil {
		t.Fatalf("bots must be present and complete: %+v", bots)
	}
	if bots.BotCount != 1 || bots.Timezone != "Asia/Shanghai" || bots.TotalRequests != 12 || bots.TotalTokens != 3456 {
		t.Fatalf("bots block: %+v", bots)
	}
	if bots.Cost == nil || *bots.Cost != 1.5 {
		t.Fatalf("cost must pass through: %+v", bots.Cost)
	}
	if bots.NextSchedule == nil || bots.NextSchedule.TaskName != "晚报" || bots.NextSchedule.BotName != "值夜机器人" {
		t.Fatalf("next_schedule: %+v", bots.NextSchedule)
	}
	if _, err := time.Parse(time.RFC3339, bots.NextSchedule.NextRunAt); err != nil {
		t.Fatalf("next_run_at must parse: %v", err)
	}
}

// 用量服务错误（含 ErrInvalidParams/ErrBotNotFound 同型错误）→ 吞掉降级；
// next_schedule 查询失败也只降级本子块。
func TestDashboardAiBotsDegradedOnError(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)

	svc := newDashboardServiceWithAI(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil},
		nil, &fakeBotUsageSource{err: errors.New("boom")})
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview must not fail: %v", err)
	}
	if resp.Ai.Bots == nil || resp.Ai.Bots.Complete == nil || *resp.Ai.Bots.Complete {
		t.Fatalf("bots must degrade: %+v", resp.Ai.Bots)
	}
	// reviews 在空库查询成功（空桶不算不可用），顶层 complete 不出现；
	// 「全部在场子块都不可用」路径由 TestDashboardAiReviewsDegradedOnError 覆盖。
	if resp.Ai.Complete != nil {
		t.Fatalf("ai top-level complete must be omitted when any sub-block works: %+v", resp.Ai.Complete)
	}
}

// buildAiQuota：快照透传（前端算剩余百分比）；服务错误吞掉降级；窄接口
// nil 时子块整体缺席。
func TestDashboardAiQuotaBlock(t *testing.T) {
	db := newDashboardServiceTestDB(t)
	userID := dashboardSeedUser(t, db)

	accounts := []api.QuotaAccount{{Id: "acc-1", Label: "主账号", Provider: "claude", Status: "ok"}}
	svc := newDashboardServiceWithAI(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil},
		&fakeQuotaLister{accounts: accounts}, nil)
	resp, err := svc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview: %v", err)
	}
	if resp.Ai.Quota == nil || resp.Ai.Quota.Complete != nil || len(resp.Ai.Quota.Accounts) != 1 {
		t.Fatalf("quota block: %+v", resp.Ai.Quota)
	}
	if resp.Ai.Bots != nil {
		t.Fatal("bots sub-block must be absent when narrow interface is nil")
	}

	failSvc := newDashboardServiceWithAI(t, db, &fakeUnreadCounter{count: 0}, &fakeT3WaitingSource{result: nil},
		&fakeQuotaLister{err: errors.New("boom")}, nil)
	resp, err = failSvc.GetOverview(context.Background(), userID)
	if err != nil {
		t.Fatalf("GetOverview must not fail: %v", err)
	}
	if resp.Ai.Quota == nil || resp.Ai.Quota.Complete == nil || *resp.Ai.Quota.Complete || resp.Ai.Quota.Accounts == nil {
		t.Fatalf("quota must degrade with structure: %+v", resp.Ai.Quota)
	}
}

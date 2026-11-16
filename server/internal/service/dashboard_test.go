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
		&model.Version{}, &model.QuotaAccount{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newDashboardService(t *testing.T, db *gorm.DB, unread dashboardUnreadCounter, t3 dashboardT3WaitingSource) *DashboardService {
	t.Helper()
	return NewDashboardService(repository.NewDashboardRepository(db), unread, t3)
}

func dashboardSeedUser(t *testing.T, db *gorm.DB) string {
	t.Helper()
	id := uuid.NewString()
	if err := db.Create(&model.User{ID: id, Username: "u" + id[:8], Email: id[:8] + "@example.com", PasswordHash: "x", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
		t.Fatalf("create user: %v", err)
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

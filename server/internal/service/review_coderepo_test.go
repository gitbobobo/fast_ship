package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 代码仓库与公开仓库分离（musiver 场景）：公开仓库放 Issue/Release、代码仓库
// 承载 PR。审查链路一律走 project.CodeRepo()；webhook 反查与同步过滤只认
// project_review_settings.repo_full_name 快照。

// codeRepoEnv 公开仓库 = pub/public-proj、代码仓库 = gitbobobo/code-proj 的
// 审查执行环境（设置行已启用，快照落代码仓库小写）。
type codeRepoEnv struct {
	svc      *testServices
	review   *ReviewService
	fake     *githubapp.Fake
	user     *model.User
	project  *model.Project
	pubRepo  string
	codeRepo string
}

func newCodeRepoEnv(t *testing.T) *codeRepoEnv {
	t.Helper()
	svc := setupTestServices(t)
	fake := githubapp.NewFake()
	review := NewReviewService(svc.db, svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(svc.db),
		NewNotificationSink(svc.db),
		githubapp.StaticClients{Client: fake},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, nil, nil, repository.NewForgeIntegrationRepository(svc.db), svc.cfg, zap.NewNop())
	user := createTestUser(t, svc.db, "code-repo-user")
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		p.GithubOwner = "pub"
		p.GithubRepo = "public-proj"
		p.CodeGithubOwner = "GitBobobo"
		p.CodeGithubRepo = "Code-Proj"
	})
	now := time.Now().UTC()
	code, err := marshalCombinations([]api.ReviewCombination{testCombo("env-1")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	if err := svc.db.Create(&model.ProjectReviewSettings{
		IntegrationID:        model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:            project.ID,
		UserID:               user.ID,
		RepoFullName:         strings.ToLower(project.CodeRepoFullName()),
		Enabled:              true,
		TriggerOpened:        true,
		TriggerSynchronize:   true,
		CodeCombinationsJSON: code,
		CreatedAt:            now,
		UpdatedAt:            now,
	}).Error; err != nil {
		t.Fatalf("enable review: %v", err)
	}
	return &codeRepoEnv{
		svc: svc, review: review, fake: fake, user: user, project: project,
		pubRepo: "pub/public-proj", codeRepo: "gitbobobo/code-proj",
	}
}

// webhook：代码仓库 B 的 pull_request 事件能反查到项目并落防抖行（修复前
// 反查按公开仓库 live 字段比对，B 的事件被静默丢失）；公开仓库 A 的事件不
// 触发（A 不是审查仓库）。
func TestReviewWebhookResolvesByCodeRepo(t *testing.T) {
	e := newCodeRepoEnv(t)
	ctx := context.Background()

	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-code",
		prPayload("opened", e.codeRepo, 12, "sha-b1", false, false)); err != nil {
		t.Fatalf("code repo webhook: %v", err)
	}
	var debounce model.ReviewDebounce
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = 12",
		e.project.ID, e.codeRepo).First(&debounce).Error; err != nil {
		t.Fatalf("代码仓库事件应落防抖行: %v", err)
	}
	if debounce.HeadSHA != "sha-b1" {
		t.Fatalf("debounce head = %q, want sha-b1", debounce.HeadSHA)
	}

	// 公开仓库的事件找不到 enabled 快照匹配 → 忽略，不落任何行。
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-pub",
		prPayload("opened", e.pubRepo, 13, "sha-a1", false, false)); err != nil {
		t.Fatalf("public repo webhook: %v", err)
	}
	var n int64
	e.svc.db.Model(&model.ReviewDebounce{}).
		Where("project_id = ? AND repo_full_name = ?", e.project.ID, e.pubRepo).Count(&n)
	if n != 0 {
		t.Fatalf("公开仓库事件不应落防抖行, got %d", n)
	}
}

// 防抖到期开轮的仓库复核按 CodeRepo() 比对：代码仓库防抖行正常开轮。
func TestReviewDebounceConsumeWithCodeRepo(t *testing.T) {
	e := newCodeRepoEnv(t)
	now := time.Now().UTC()
	e.fake.SetPR(e.codeRepo, 21, &githubapi.PullRequest{HeadSHA: "sha-c"})
	e.svc.db.Create(&model.ReviewDebounce{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: e.codeRepo, PRNumber: 21,
		HeadSHA: "sha-c", Action: "synchronize", InstallationID: 42,
		FireAt: now.Add(-time.Second), UpdatedAt: now,
	})
	e.review.processDueDebounces(now)

	var runs []model.ReviewRun
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = 21",
		e.project.ID, e.codeRepo).Find(&runs).Error; err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
}

// 同仓库唯一按代码仓库判定：占用判定只读 enabled 设置行的 repo_full_name
// 快照——占用项目的 live 公开仓库已经换走（快照仍在）也能被识别为占用；
// 总览仓库列展示本项目实际审查的代码仓库。
func TestReviewSettingsTakenByCodeRepo(t *testing.T) {
	svc := setupTestServices(t)
	me := createTestUser(t, svc.db, "me-code")
	other := createTestUser(t, svc.db, "other-code")

	// 我的项目：公开 pub/one、代码 octo/Shared。
	mine := createTestProject(t, svc.db, me.ID, func(p *model.Project) {
		p.GithubOwner = "pub"
		p.GithubRepo = "one"
		p.CodeGithubOwner = "octo"
		p.CodeGithubRepo = "Shared"
	})
	// 对方项目：显式代码仓库 octo/shared 且已启用；公开仓库后来换去了
	// moved/elsewhere（enabled 行快照不受影响，仍占用 octo/shared）。
	otherProject := createTestProject(t, svc.db, other.ID, func(p *model.Project) {
		p.GithubOwner = "moved"
		p.GithubRepo = "elsewhere"
		p.CodeGithubOwner = "octo"
		p.CodeGithubRepo = "shared"
	})
	now := time.Now().UTC()
	if err := svc.db.Create(&model.ProjectReviewSettings{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     otherProject.ID, UserID: other.ID, RepoFullName: "octo/shared", Enabled: true,
		CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	rs, _ := readyReviewService(t, svc)
	ctx := context.Background()

	items, err := rs.ListReviewProjects(ctx, me.ID)
	if err != nil {
		t.Fatalf("ListReviewProjects: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1（代码仓库也算已关联）", len(items))
	}
	got := items[0]
	if got.GithubOwner != "octo" || got.GithubRepo != "Shared" {
		t.Fatalf("总览仓库应展示代码仓库原样大小写, got %q/%q", got.GithubOwner, got.GithubRepo)
	}
	if got.TakenBy == nil || *got.TakenBy != "user_other-code" {
		t.Fatalf("taken_by = %v, want user_other-code", got.TakenBy)
	}
	if got.CanManage {
		t.Fatal("can_manage 应为 false")
	}

	// PUT 开启 → 40922。
	env := createT3TestEnv(t, svc.db, me.ID, "env-code")
	createTestT3Binding(t, svc.db, mine.ID, env.ID, "t3-code")
	in := validSettingsInput(env.ID)
	if _, err := rs.UpdateSettings(ctx, me.ID, mine.ID, in); err == nil {
		t.Fatal("同代码仓库被占用时开启应失败")
	} else if code := appErrorCode(t, err); code != 40922 {
		t.Fatalf("code = %d, want 40922", code)
	}

	// GET 的 repo_full_name 展示实际审查仓库。
	settings, err := rs.GetSettings(ctx, me.ID, mine.ID)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.RepoFullName != "octo/Shared" {
		t.Fatalf("repo_full_name = %q, want octo/Shared", settings.RepoFullName)
	}
}

// syncPendingPRs 过滤：代码仓库账本行仍同步，公开仓库账本行（settings 快照
// 不匹配）不再同步。
func TestSyncPendingPRsFiltersByCodeRepoSnapshot(t *testing.T) {
	e := newCodeRepoEnv(t)
	now := time.Now().UTC()
	e.fake.SetPR(e.codeRepo, 31, &githubapi.PullRequest{
		RepoFullName: e.codeRepo, Number: 31, HeadSHA: "sha-sync",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: e.codeRepo, PRNumber: 31, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-sync", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	// 公开仓库的历史账本行也置了 pending：快照不匹配，不该发 GitHub 产出。
	e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: e.pubRepo, PRNumber: 32, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-old", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})

	e.review.syncPendingPRs(now.Add(time.Second))

	var codeLedger model.ReviewPullRequest
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = 31",
		e.project.ID, e.codeRepo).First(&codeLedger).Error; err != nil {
		t.Fatalf("load code ledger: %v", err)
	}
	if codeLedger.SyncPending {
		t.Fatal("代码仓库账本行应已同步（清 pending）")
	}
	var pubLedger model.ReviewPullRequest
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = 32",
		e.project.ID, e.pubRepo).First(&pubLedger).Error; err != nil {
		t.Fatalf("load pub ledger: %v", err)
	}
	if !pubLedger.SyncPending {
		t.Fatal("公开仓库账本行不应再同步（pending 保留）")
	}
	if e.fake.TotalCalls() == 0 {
		t.Fatal("代码仓库账本行应产生 GitHub 调用")
	}
}

// rerun 校验与取值走 CodeRepo：记录仓库 = 代码仓库可重跑；= 公开仓库视为
// 历史仓库拒绝。
func TestRerunReviewUsesCodeRepo(t *testing.T) {
	e := newCodeRepoEnv(t)
	ctx := context.Background()

	// 公开仓库的记录 → 视为历史仓库，拒绝。
	if _, err := e.review.RerunReview(ctx, e.user.ID, e.project.ID, 7, e.pubRepo); err == nil {
		t.Fatal("公开仓库记录不应能重跑（审查仓库是代码仓库）")
	}

	// 代码仓库的记录 → 受理开新轮。
	e.fake.SetPR(e.codeRepo, 7, &githubapi.PullRequest{
		RepoFullName: e.codeRepo, Number: 7, HeadSHA: "sha-rerun",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	got, err := e.review.RerunReview(ctx, e.user.ID, e.project.ID, 7, e.codeRepo)
	if err != nil {
		t.Fatalf("rerun by code repo: %v", err)
	}
	if !got.Accepted || got.HeadSHA != "sha-rerun" {
		t.Fatalf("rerun result = %+v", got)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = 7",
		e.project.ID, e.codeRepo).First(&run).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if run.TriggerAction != "manual" {
		t.Fatalf("trigger_action = %q, want manual", run.TriggerAction)
	}
}

// syncOnePR 准入复核（回归）：账本行被选出后项目经 ProjectService.Update 改了
// 代码仓库（真实路径：同事务停用审查、刷新快照、清 pending、epoch+1），重读到
// 的旧行不得再向旧仓库发任何 GitHub 请求，也不写脏 sync_error。
func TestSyncOnePRGivesUpAfterRepoChanged(t *testing.T) {
	e := newCodeRepoEnv(t)
	now := time.Now().UTC()
	e.fake.SetPR(e.codeRepo, 71, &githubapi.PullRequest{
		RepoFullName: e.codeRepo, Number: 71, HeadSHA: "sha-stale",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	ledger := &model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: e.codeRepo, PRNumber: 71, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-stale", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}
	if err := e.svc.db.Create(ledger).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	selected := *ledger // syncPendingPRs 选出时刻的快照

	// 模拟「选出之后项目改代码仓库」，走 ProjectService.Update 真实路径。
	projectSvc := newTestProjectService(e.svc)
	if _, err := projectSvc.Update(e.project.ID, e.user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "new/repository"},
	}); err != nil {
		t.Fatalf("update code repo: %v", err)
	}

	e.review.syncOnePR(&selected)

	if e.fake.TotalCalls() != 0 {
		t.Fatalf("换仓库后旧账本行不得再发 GitHub 请求: %v", e.fake.CallCounts())
	}
	var cur model.ReviewPullRequest
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = 71",
		e.project.ID, e.codeRepo).First(&cur).Error; err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if cur.SyncPending || cur.SyncEpoch != 2 || cur.SyncError != "" {
		t.Fatalf("账本应保持换仓库时的收尾态（pending=false epoch=2 error=空）, got pending=%v epoch=%d error=%q",
			cur.SyncPending, cur.SyncEpoch, cur.SyncError)
	}
}

// divergeLiveCodeRepo 直接改项目行的 code 字段，构造「live 代码仓库 ≠ 设置行
// 快照」的状态：真实写路径会刷新快照并停用审查，这里绕开写侧，专门锁住读侧
// 「只信快照、不回联 projects」的语义。
func divergeLiveCodeRepo(t *testing.T, db *gorm.DB, projectID string) {
	t.Helper()
	if err := db.Model(&model.Project{}).Where("id = ?", projectID).
		Updates(map[string]any{"code_github_owner": "octo", "code_github_repo": "other"}).Error; err != nil {
		t.Fatalf("diverge live code repo: %v", err)
	}
}

// webhook 反查只信设置行快照：live 代码仓库被改走后，快照仓库的事件仍能
// 反查到项目，live 仓库的事件找不到（回联 live 字段的变体会两个都错）。
func TestReviewWebhookResolvesBySnapshotNotLiveCodeRepo(t *testing.T) {
	e := newCodeRepoEnv(t)
	divergeLiveCodeRepo(t, e.svc.db, e.project.ID)
	ctx := context.Background()

	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-snap",
		prPayload("opened", e.codeRepo, 51, "sha-s1", false, false)); err != nil {
		t.Fatalf("snapshot repo webhook: %v", err)
	}
	var debounce model.ReviewDebounce
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ?", e.project.ID, e.codeRepo).
		First(&debounce).Error; err != nil {
		t.Fatalf("快照仓库事件应反查到项目: %v", err)
	}

	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-live",
		prPayload("opened", "octo/other", 52, "sha-l1", false, false)); err != nil {
		t.Fatalf("live repo webhook: %v", err)
	}
	var n int64
	e.svc.db.Model(&model.ReviewDebounce{}).
		Where("project_id = ? AND repo_full_name = ?", e.project.ID, "octo/other").Count(&n)
	if n != 0 {
		t.Fatalf("live 代码仓库的事件不应反查到项目, got %d 条防抖行", n)
	}
}

// 同仓库占用只信快照：占用项目的 live 代码仓库已改走（只剩快照还是
// octo/shared），本项目仍被判为被占用（GET taken_by/can_manage、总览、
// PUT 40922 一致）。
func TestReviewTakenBySnapshotNotLiveCodeRepo(t *testing.T) {
	svc := setupTestServices(t)
	me := createTestUser(t, svc.db, "me-snap")
	other := createTestUser(t, svc.db, "other-snap")

	mine := createTestProject(t, svc.db, me.ID, func(p *model.Project) {
		p.GithubOwner = "pub"
		p.GithubRepo = "mine"
		p.CodeGithubOwner = "octo"
		p.CodeGithubRepo = "shared"
	})
	occupied := createTestProject(t, svc.db, other.ID, func(p *model.Project) {
		p.GithubOwner = "pub"
		p.GithubRepo = "theirs"
		p.CodeGithubOwner = "octo"
		p.CodeGithubRepo = "shared"
	})
	now := time.Now().UTC()
	if err := svc.db.Create(&model.ProjectReviewSettings{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     occupied.ID, UserID: other.ID, RepoFullName: "octo/shared", Enabled: true,
		CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	// 占用项目的 live 代码仓库后来被改走，只剩 enabled 快照仍占 octo/shared。
	divergeLiveCodeRepo(t, svc.db, occupied.ID)

	rs, _ := readyReviewService(t, svc)
	ctx := context.Background()

	settings, err := rs.GetSettings(ctx, me.ID, mine.ID)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if settings.TakenBy == nil || *settings.TakenBy != "user_other-snap" {
		t.Fatalf("taken_by = %v, want user_other-snap（占用判定只信快照）", settings.TakenBy)
	}
	if settings.CanManage == nil || *settings.CanManage {
		t.Fatal("can_manage 应为 false")
	}

	items, err := rs.ListReviewProjects(ctx, me.ID)
	if err != nil {
		t.Fatalf("ListReviewProjects: %v", err)
	}
	var mineItem *api.ReviewProjectSummary
	for i := range items {
		if items[i].ProjectId == mine.ID {
			mineItem = &items[i]
		}
	}
	if mineItem == nil {
		t.Fatal("总览应包含本项目")
	}
	if mineItem.TakenBy == nil || *mineItem.TakenBy != "user_other-snap" || mineItem.CanManage {
		t.Fatalf("总览占用判定 = %+v, want taken_by=user_other-snap can_manage=false", mineItem)
	}

	env := createT3TestEnv(t, svc.db, me.ID, "env-snap")
	createTestT3Binding(t, svc.db, mine.ID, env.ID, "t3-snap")
	if _, err := rs.UpdateSettings(ctx, me.ID, mine.ID, validSettingsInput(env.ID)); err == nil {
		t.Fatal("快照占用下开启审查应失败")
	} else if code := appErrorCode(t, err); code != 40922 {
		t.Fatalf("code = %d, want 40922", code)
	}
}

// 同步过滤只信快照：快照匹配的 pending 账本行照常同步（清 pending），
// live 代码仓库偏离不影响。
func TestSyncPendingPRsUsesSnapshotNotLiveCodeRepo(t *testing.T) {
	e := newCodeRepoEnv(t)
	divergeLiveCodeRepo(t, e.svc.db, e.project.ID)
	now := time.Now().UTC()
	e.fake.SetPR(e.codeRepo, 61, &githubapi.PullRequest{
		RepoFullName: e.codeRepo, Number: 61, HeadSHA: "sha-snap",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: e.codeRepo, PRNumber: 61, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-snap", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})

	e.review.syncPendingPRs(now.Add(time.Second))

	var ledger model.ReviewPullRequest
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = 61",
		e.project.ID, e.codeRepo).First(&ledger).Error; err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if ledger.SyncPending {
		t.Fatal("快照匹配的 pending 行应照常同步（清 pending）")
	}
	if e.fake.TotalCalls() == 0 {
		t.Fatal("快照匹配的账本行应产生 GitHub 调用")
	}
}

// 防抖到期复核只信设置行快照：项目 live 代码仓库被改走（快照仍是本仓库）时，
// 快照仓库的防抖行照常开轮（回读 live CodeRepo 的变体会把这一轮丢掉）。
func TestReviewDebounceConsumeUsesSnapshotNotLiveCodeRepo(t *testing.T) {
	e := newCodeRepoEnv(t)
	divergeLiveCodeRepo(t, e.svc.db, e.project.ID)
	now := time.Now().UTC()
	e.fake.SetPR(e.codeRepo, 22, &githubapi.PullRequest{HeadSHA: "sha-snap"})
	if err := e.svc.db.Create(&model.ReviewDebounce{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: e.codeRepo, PRNumber: 22,
		HeadSHA: "sha-snap", Action: "synchronize", InstallationID: 42,
		FireAt: now.Add(-time.Second), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed debounce: %v", err)
	}

	e.review.processDueDebounces(now)

	var runs []model.ReviewRun
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = 22",
		e.project.ID, e.codeRepo).Find(&runs).Error; err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("快照仓库的防抖行应开一轮, runs = %d", len(runs))
	}
}

// rerun 也按设置行快照取仓库：live 代码仓库被改走后，快照仓库的记录仍可重跑，
// live 仓库的记录被视为「历史仓库」拒绝（回读 live 的版本正好相反）。
func TestRerunReviewUsesSnapshotNotLiveCodeRepo(t *testing.T) {
	e := newCodeRepoEnv(t)
	divergeLiveCodeRepo(t, e.svc.db, e.project.ID)
	ctx := context.Background()

	if _, err := e.review.RerunReview(ctx, e.user.ID, e.project.ID, 7, "octo/other"); err == nil {
		t.Fatal("live 仓库的记录应视为历史仓库拒绝")
	}

	e.fake.SetPR(e.codeRepo, 7, &githubapi.PullRequest{
		RepoFullName: e.codeRepo, Number: 7, HeadSHA: "sha-snap-rerun",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	got, err := e.review.RerunReview(ctx, e.user.ID, e.project.ID, 7, e.codeRepo)
	if err != nil {
		t.Fatalf("按快照仓库重跑: %v", err)
	}
	if !got.Accepted || got.HeadSHA != "sha-snap-rerun" {
		t.Fatalf("rerun result = %+v", got)
	}
}

// 同步写前守卫：远端副作用发出去收不回来，所以每次 GitHub 写之前复核本轮仍
// 有效；换仓库后守卫拦下写，且不产生任何 GitHub 调用。
func TestGuardedSyncClientBlocksWritesAfterRepoChanged(t *testing.T) {
	e := newCodeRepoEnv(t)
	now := time.Now().UTC()
	ledger := &model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: e.codeRepo, PRNumber: 81, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-guard", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}
	if err := e.svc.db.Create(ledger).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	client := guardedSyncClient{
		ReviewClient: forge.NewGitHubReviewClient(e.fake, 1),
		check:        func() error { return e.review.syncTargetStillCurrent(ledger, 1) },
	}
	ctx := context.Background()
	if err := client.CreateCommitStatus(ctx, e.codeRepo, "sha-guard", "success", "代码审查", "", ""); err != nil {
		t.Fatalf("目标仍有效时写应放行: %v", err)
	}
	before := e.fake.TotalCalls()

	projectSvc := newTestProjectService(e.svc)
	if _, err := projectSvc.Update(e.project.ID, e.user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "new/repository"},
	}); err != nil {
		t.Fatalf("update code repo: %v", err)
	}

	if err := client.CreateCommitStatus(ctx, e.codeRepo, "sha-guard", "success", "代码审查", "", ""); !errors.Is(err, errReviewSyncAbandoned) {
		t.Fatalf("换仓库后写应被拦, err = %v", err)
	}
	if e.fake.TotalCalls() != before {
		t.Fatalf("被拦的写不应产生 GitHub 调用（before=%d after=%d）", before, e.fake.TotalCalls())
	}
}

// doSyncPR 在首次远端写之前复核：项目换仓库后直接进入同步主体，只允许发生
// 只读的 GetPullRequest，任何写调用都不应发出。
func TestDoSyncPRStopsBeforeFirstWriteAfterRepoChanged(t *testing.T) {
	e := newCodeRepoEnv(t)
	now := time.Now().UTC()
	ledger := &model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: e.codeRepo, PRNumber: 91, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-dsync", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}
	if err := e.svc.db.Create(ledger).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	e.fake.SetPR(e.codeRepo, 91, &githubapi.PullRequest{
		RepoFullName: e.codeRepo, Number: 91, HeadSHA: "sha-dsync",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})

	projectSvc := newTestProjectService(e.svc)
	if _, err := projectSvc.Update(e.project.ID, e.user.ID, &UpdateProjectRequest{
		CodeRepositoryUrl: OptionalStringUpdate{Set: true, Value: "new/repository"},
	}); err != nil {
		t.Fatalf("update code repo: %v", err)
	}

	err := e.review.doSyncPR(context.Background(), ledger, 1, now)
	if !errors.Is(err, errReviewSyncAbandoned) {
		t.Fatalf("err = %v, want errReviewSyncAbandoned", err)
	}
	for name, n := range e.fake.CallCounts() {
		if strings.HasPrefix(name, "Create") || strings.HasPrefix(name, "Update") || name == "SubmitReview" {
			t.Fatalf("换仓库后不应发出写调用: %s=%d", name, n)
		}
	}
}

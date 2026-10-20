package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// INT-135 第三轮结构修复测试：账本按项目分区、启用接管收尾、启动请求
// 不可变重放（已签发令牌不重认领）、首次鉴权补 dispatched_at、设置保存
// 事务内仓库复核。

// seedRunWithFindings 完成态代码运行，带一条落在 diff 有效行上的 P1。
func (e *reviewEngineEnv) seedRunWithFindings(t *testing.T, repo string, pr int, sha string, now time.Time) *model.ReviewRun {
	t.Helper()
	run := e.seedRun(t, repo, pr, sha, model.ReviewRunCompleted, now)
	result := `{"summary":"有发现","findings":[{"level":"P1","file":"a","line":2,"body":"问题"}],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`
	if err := e.svc.db.Model(run).Update("result_json", result).Error; err != nil {
		t.Fatalf("update result: %v", err)
	}
	return run
}

// TestReviewLedgerPartitionedByProject A：同仓库另一个项目（已停用）的残留
// 账本行不进本项目的 webhook 判定——其 seen/运行/防抖/总账各自独立，新
// 项目对同仓库同 SHA 重新受理、轮次从 1 起算。
func TestReviewLedgerPartitionedByProject(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	ctx := context.Background()

	// 同仓库的另一个项目 B（曾开启后停用，残留全部执行态）。
	otherUser := createTestUser(t, e.svc.db, "other-owner")
	other := createTestProject(t, e.svc.db, otherUser.ID) // 默认同仓库 owner/repo
	e.svc.db.Create(&model.ProjectReviewSettings{
		ProjectID: other.ID, UserID: otherUser.ID, RepoFullName: repo,
		Enabled: false, CreatedAt: now, UpdatedAt: now,
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: other.ID, RepoFullName: repo, PRNumber: 55, UserID: otherUser.ID,
		InstallationID: 7, HeadSHA: "sha-b-head", RoundNumber: 3,
		CodeStatus: "success", SyncPending: true, SyncEpoch: 5, UpdatedAt: now,
	})
	e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: other.ID, RepoFullName: repo, PRNumber: 55, HeadSHA: "sha-x",
		AcceptedAt: now,
	})
	e.svc.db.Create(&model.ReviewDebounce{
		ProjectID: other.ID, RepoFullName: repo, PRNumber: 55, HeadSHA: "sha-x",
		Action: "synchronize", FireAt: now.Add(time.Hour), UpdatedAt: now,
	})
	e.svc.db.Create(&model.ReviewRun{
		ID: uuid.NewString(), UserID: otherUser.ID, ProjectID: other.ID,
		Kind: model.ReviewKindCode, RepoFullName: repo, PRNumber: 55,
		HeadSHA: "sha-x", RoundNumber: 3, EnvironmentID: "env-1",
		InstanceID: "claude", Model: "m",
		ThreadID: uuid.NewString(), LaunchCommandID: uuid.NewString(), InitialMessageID: uuid.NewString(),
		Status: model.ReviewRunRunning, DeadlineAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	})

	// 本项目受理同 SHA：B 的 seen 行与 B 的同 SHA 运行都不构成去重/吞并。
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-p1",
		prPayload("synchronize", repo, 55, "sha-x", false, false)); err != nil {
		t.Fatalf("webhook: %v", err)
	}

	var ownLedger, otherLedger model.ReviewPullRequest
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = 55",
		e.project.ID, repo).First(&ownLedger).Error; err != nil {
		t.Fatalf("本项目总账应已创建: %v", err)
	}
	if ownLedger.HeadSHA != "sha-x" {
		t.Fatalf("本项目总账 head = %q, want sha-x", ownLedger.HeadSHA)
	}
	if ownLedger.SyncPending {
		t.Fatal("webhook 不置 sync_pending；开轮后才置")
	}
	e.svc.db.First(&otherLedger, "project_id = ? AND repo_full_name = ? AND pr_number = 55",
		other.ID, repo)
	if otherLedger.HeadSHA != "sha-b-head" || otherLedger.RoundNumber != 3 ||
		!otherLedger.SyncPending || otherLedger.CodeStatus != "success" {
		t.Fatalf("其他项目总账被 webhook 改动: %+v", otherLedger)
	}

	var ownDebounce int64
	e.svc.db.Model(&model.ReviewDebounce{}).
		Where("project_id = ? AND head_sha = 'sha-x'", e.project.ID).Count(&ownDebounce)
	if ownDebounce != 1 {
		t.Fatalf("本项目防抖行 = %d, want 1", ownDebounce)
	}
	var otherDebounce int64
	e.svc.db.Model(&model.ReviewDebounce{}).
		Where("project_id = ?", other.ID).Count(&otherDebounce)
	if otherDebounce != 1 {
		t.Fatalf("其他项目防抖行被删 = %d, want 1", otherDebounce)
	}
	var otherRun model.ReviewRun
	e.svc.db.First(&otherRun, "project_id = ? AND pr_number = 55", other.ID)
	if otherRun.Status != model.ReviewRunRunning {
		t.Fatalf("其他项目运行被 webhook 收敛 = %q, want running", otherRun.Status)
	}

	// 防抖开火：轮次在项目内分配——B 已跑到第 3 轮，本项目仍从 1 起。
	e.svc.db.Model(&model.ReviewDebounce{}).Where("project_id = ?", e.project.ID).
		Update("fire_at", now.Add(-time.Second))
	e.review.processDueDebounces(now.Add(time.Second))

	var ownRun model.ReviewRun
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = 55",
		e.project.ID, repo).First(&ownRun).Error; err != nil {
		t.Fatalf("本项目运行应已创建: %v", err)
	}
	if ownRun.RoundNumber != 1 || ownRun.HeadSHA != "sha-x" ||
		ownRun.Status != model.ReviewRunDispatched {
		t.Fatalf("本项目运行 = round:%d head:%q status:%q, want 1/sha-x/dispatched",
			ownRun.RoundNumber, ownRun.HeadSHA, ownRun.Status)
	}
	e.svc.db.First(&ownLedger, "project_id = ? AND pr_number = 55", e.project.ID)
	if ownLedger.HeadSHA != "sha-x" || ownLedger.RoundNumber != 1 {
		t.Fatalf("本项目总账推进 = %q/round%d, want sha-x/1",
			ownLedger.HeadSHA, ownLedger.RoundNumber)
	}
	e.svc.db.First(&otherRun, "project_id = ? AND pr_number = 55", other.ID)
	if otherRun.Status != model.ReviewRunRunning || otherRun.RoundNumber != 3 {
		t.Fatalf("开轮后其他项目运行被改动 = %q/round%d", otherRun.Status, otherRun.RoundNumber)
	}
}

// TestReviewSyncSkipsDisabledProjectLedger A：停用/被接管项目的 sync_pending
// 残留不再发 GitHub 产出；重新启用后才同步。
func TestReviewSyncSkipsDisabledProjectLedger(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 77, &githubapi.PullRequest{
		RepoFullName: repo, Number: 77, HeadSHA: "sha-s",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 77, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-s", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	// 项目设置存在但未启用。
	e.enableReview(t, func(s *model.ProjectReviewSettings) { s.Enabled = false })

	e.review.syncPendingPRs(now)
	if got := e.fake.TotalCalls(); got != 0 {
		t.Fatalf("未启用项目的总账不应发 GitHub 产出，calls = %d", got)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "project_id = ? AND pr_number = 77", e.project.ID)
	if !ledger.SyncPending {
		t.Fatal("未启用项目的 sync_pending 应保留")
	}

	// 重新启用后同一份 pending 照常同步。
	e.svc.db.Model(&model.ProjectReviewSettings{}).
		Where("project_id = ?", e.project.ID).Update("enabled", true)
	e.review.syncPendingPRs(now.Add(time.Second))
	e.svc.db.First(&ledger, "project_id = ? AND pr_number = 77", e.project.ID)
	if ledger.SyncPending {
		t.Fatal("启用后 sync_pending 应已清除")
	}
}

// TestDispatchSkipsTokenIssuedClaim B：认领超窗但已签发过令牌的运行不再
// 重认领（重签会覆盖哈希使已建成线程令牌失效）；留给 deadline 超时兜底。
func TestDispatchSkipsTokenIssuedClaim(t *testing.T) {
	e := newReviewEngineEnv(t)
	now := time.Now().UTC()
	run := &model.ReviewRun{
		ID: "run-token-issued", UserID: e.user.ID, ProjectID: e.project.ID,
		Kind: model.ReviewKindCode, RepoFullName: "o/r", PRNumber: 8,
		HeadSHA: "sha", RoundNumber: 1,
		EnvironmentID: "env-1", InstanceID: "claude", Model: "m",
		ThreadID: "th", LaunchCommandID: "lc", InitialMessageID: "im",
		Status: model.ReviewRunDispatched,
	}
	claimed := now.Add(-2 * reviewDispatchClaimTimeout)
	run.DispatchStartedAt = &claimed
	run.DeadlineAt = now.Add(time.Hour)
	run.CreatedAt, run.UpdatedAt = now, now
	if err := e.svc.db.Create(run).Error; err != nil {
		t.Fatalf("create stale run: %v", err)
	}
	// 令牌已签发（只存哈希）：launchRun 到达过签发点，可能是已建成线程。
	if err := e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: sha256Hex("fsr_any"),
		CreatedAt: claimed, ExpiresAt: now.Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}

	e.review.dispatchRuns(now)

	var got model.ReviewRun
	e.svc.db.First(&got, "id = ?", run.ID)
	if got.Status != model.ReviewRunDispatched {
		t.Fatalf("已签发令牌的运行被重派发 = %q, want dispatched", got.Status)
	}
	if got.DispatchStartedAt == nil || !got.DispatchStartedAt.Equal(claimed) {
		t.Fatalf("dispatch_started_at 被重认领覆盖: %+v", got.DispatchStartedAt)
	}
}

// TestResolveRunTokenBackfillsDispatchedAt C：启动 RPC 结果未确认
// （dispatched_at 仍空）时，Agent 持令牌首次鉴权证明线程已建成——
// dispatched_at 补写，归档阶段才能捞到该运行。
func TestResolveRunTokenBackfillsDispatchedAt(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedRun(t, repo, 14, "sha-k", model.ReviewRunDispatched, now)
	claimed := now.Add(-time.Minute)
	e.svc.db.Model(run).Update("dispatch_started_at", claimed)
	raw := "fsr_" + strings.Repeat("b", 43)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: sha256Hex(raw), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	if _, err := e.review.ResolveRunToken(context.Background(), raw, true); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunRunning {
		t.Fatalf("status = %q, want running", after.Status)
	}
	if after.DispatchedAt == nil {
		t.Fatal("首次鉴权应补写 dispatched_at（线程存在已被令牌证明）")
	}

	// dispatched_at 已有值时不被覆盖。
	confirmed := now.Add(-30 * time.Second)
	run2 := e.seedRun(t, repo, 15, "sha-k2", model.ReviewRunDispatched, now)
	e.svc.db.Model(run2).Updates(map[string]any{
		"dispatch_started_at": claimed, "dispatched_at": confirmed,
	})
	raw2 := "fsr_" + strings.Repeat("c", 43)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run2.ID, TokenHash: sha256Hex(raw2), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	if _, err := e.review.ResolveRunToken(context.Background(), raw2, true); err != nil {
		t.Fatalf("resolve2: %v", err)
	}
	var after2 model.ReviewRun
	e.svc.db.First(&after2, "id = ?", run2.ID)
	if after2.DispatchedAt == nil || !after2.DispatchedAt.Equal(confirmed) {
		t.Fatalf("已有 dispatched_at 被覆盖: %+v", after2.DispatchedAt)
	}
}

// repoSwitchClient 包一层 GitHub 客户端：前置条件的 GetRepository 校验一过
// 就把项目仓库改掉——模拟保存期间并发改仓库；写事务的复核必须拦下这次保存。
type repoSwitchClient struct {
	githubapi.GitHubClient
	db        *gorm.DB
	projectID string
	newRepo   string
	done      bool
}

func (c *repoSwitchClient) GetRepository(ctx context.Context, inst int64, repo string) error {
	if !c.done {
		c.done = true
		if err := c.db.Model(&model.Project{}).Where("id = ?", c.projectID).
			Update("github_repo", c.newRepo).Error; err != nil {
			return err
		}
	}
	return c.GitHubClient.GetRepository(ctx, inst, repo)
}

// TestUpdateSettingsRepoChangedInTx D：前置校验用的项目快照与写事务时刻
// 不一致 → 400「项目仓库已变更，请刷新后重试」，设置不落库。
func TestUpdateSettingsRepoChangedInTx(t *testing.T) {
	svc := setupTestServices(t)
	user := createTestUser(t, svc.db, "u1")
	project := createTestProject(t, svc.db, user.ID)
	env := createT3TestEnv(t, svc.db, user.ID, "env1")
	createTestT3Binding(t, svc.db, project.ID, env.ID, "t3-1")

	fake := githubapp.NewFake()
	rs := newReviewTestService(t, svc,
		githubapp.StaticClients{Client: &repoSwitchClient{
			GitHubClient: fake, db: svc.db, projectID: project.ID, newRepo: "renamed",
		}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"))

	_, err := rs.UpdateSettings(context.Background(), user.ID, project.ID,
		validSettingsInput(env.ID))
	if code := appErrorCode(t, err); code != 40001 {
		t.Fatalf("code = %d, want 40001 (err=%v)", code, err)
	}
	if !strings.Contains(err.Error(), "项目仓库已变更") {
		t.Fatalf("message = %q, want 项目仓库已变更", err)
	}
	var n int64
	svc.db.Model(&model.ProjectReviewSettings{}).
		Where("project_id = ?", project.ID).Count(&n)
	if n != 0 {
		t.Fatal("仓库不一致时设置不得落库")
	}
}

// TestUpdateSettingsTakeoverCleansUpOtherProjects A：启用某仓库时同事务收敛
// 同仓库其他项目的残留执行态——未终态运行 taken_over、令牌消费、防抖删除、
// 总账 sync_pending 清除；本项目自己的行不动。
func TestUpdateSettingsTakeoverCleansUpOtherProjects(t *testing.T) {
	svc := setupTestServices(t)
	owner := createTestUser(t, svc.db, "owner")
	other := createTestUser(t, svc.db, "other")
	now := time.Now().UTC()
	repo := "owner/repo"

	pA := createTestProject(t, svc.db, owner.ID)
	pB := createTestProject(t, svc.db, other.ID) // 默认同仓库 owner/repo

	// B 曾开启同仓库后停用：设置行 disabled + 完整残留执行态。
	svc.db.Create(&model.ProjectReviewSettings{
		ProjectID: pB.ID, UserID: other.ID, RepoFullName: repo,
		Enabled: false, CreatedAt: now, UpdatedAt: now,
	})
	runB := &model.ReviewRun{
		ID: uuid.NewString(), UserID: other.ID, ProjectID: pB.ID,
		Kind: model.ReviewKindCode, RepoFullName: repo, PRNumber: 9,
		HeadSHA: "sha-b", RoundNumber: 2, EnvironmentID: "env-1",
		InstanceID: "claude", Model: "m",
		ThreadID: uuid.NewString(), LaunchCommandID: uuid.NewString(), InitialMessageID: uuid.NewString(),
		Status: model.ReviewRunRunning, DeadlineAt: now.Add(time.Hour),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := svc.db.Create(runB).Error; err != nil {
		t.Fatalf("seed runB: %v", err)
	}
	svc.db.Create(&model.ReviewRunToken{
		RunID: runB.ID, TokenHash: sha256Hex("fsr_b"),
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	svc.db.Create(&model.ReviewDebounce{
		ProjectID: pB.ID, RepoFullName: repo, PRNumber: 9, HeadSHA: "sha-b",
		Action: "synchronize", FireAt: now.Add(time.Hour), UpdatedAt: now,
	})
	svc.db.Create(&model.ReviewPullRequest{
		ProjectID: pB.ID, RepoFullName: repo, PRNumber: 9, UserID: other.ID,
		HeadSHA: "sha-b", RoundNumber: 2, SyncPending: true, SyncEpoch: 3, UpdatedAt: now,
	})
	svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: pB.ID, RepoFullName: repo, PRNumber: 9, HeadSHA: "sha-b",
		AcceptedAt: now,
	})
	// B 的已终态运行不被动。
	doneB := &model.ReviewRun{
		ID: uuid.NewString(), UserID: other.ID, ProjectID: pB.ID,
		Kind: model.ReviewKindCode, RepoFullName: repo, PRNumber: 9,
		HeadSHA: "sha-b0", RoundNumber: 1, EnvironmentID: "env-1",
		InstanceID: "claude", Model: "m",
		ThreadID: uuid.NewString(), LaunchCommandID: uuid.NewString(), InitialMessageID: uuid.NewString(),
		Status: model.ReviewRunCompleted, DeadlineAt: now.Add(time.Hour),
		FinishedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	if err := svc.db.Create(doneB).Error; err != nil {
		t.Fatalf("seed done run: %v", err)
	}

	// A 正常启用同仓库。
	env := createT3TestEnv(t, svc.db, owner.ID, "env1")
	createTestT3Binding(t, svc.db, pA.ID, env.ID, "t3-1")
	rs, _ := readyReviewService(t, svc)
	if _, err := rs.UpdateSettings(context.Background(), owner.ID, pA.ID,
		validSettingsInput(env.ID)); err != nil {
		t.Fatalf("enable: %v", err)
	}

	var run model.ReviewRun
	svc.db.First(&run, "id = ?", runB.ID)
	if run.Status != model.ReviewRunCancelled || run.ErrorCode != "taken_over" {
		t.Fatalf("被接管运行 = %q/%q, want cancelled/taken_over", run.Status, run.ErrorCode)
	}
	var token model.ReviewRunToken
	svc.db.First(&token, "run_id = ?", runB.ID)
	if token.ConsumedAt == nil {
		t.Fatal("被接管运行的令牌应已消费")
	}
	var debounceN int64
	svc.db.Model(&model.ReviewDebounce{}).Where("project_id = ?", pB.ID).Count(&debounceN)
	if debounceN != 0 {
		t.Fatalf("被接管项目防抖行应已删除, got %d", debounceN)
	}
	var ledger model.ReviewPullRequest
	svc.db.First(&ledger, "project_id = ? AND pr_number = 9", pB.ID)
	if ledger.SyncPending {
		t.Fatal("被接管项目总账 sync_pending 应已清除（不再发 GitHub 产出）")
	}
	// B 的已终态运行与 seen 账保留（历史记录不属于收敛范围）。
	var doneRun model.ReviewRun
	if err := svc.db.First(&doneRun, "id = ?", doneB.ID).Error; err != nil {
		t.Fatalf("load done run: %v", err)
	}
	if doneRun.Status != model.ReviewRunCompleted {
		t.Fatalf("已终态运行被接管改动 = %q", doneRun.Status)
	}
	var seenN int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ?", pB.ID).Count(&seenN)
	if seenN != 1 {
		t.Fatalf("被接管项目 seen 账应保留, got %d", seenN)
	}

	// A 的启用行已生效（读回验证）。
	out, err := rs.GetSettings(context.Background(), owner.ID, pA.ID)
	if err != nil || !out.Enabled {
		t.Fatalf("A 的启用未生效: %v %+v", err, out)
	}
}

// 项目换过仓库后，历史仓库的记录不能重跑：request 里带旧仓库时 400。
func TestRerunRejectsHistoricalRepo(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	e.project.GithubOwner, e.project.GithubRepo = "newowner", "newrepo"
	if err := e.svc.db.Model(e.project).Updates(map[string]any{
		"github_owner": "newowner", "github_repo": "newrepo"}).Error; err != nil {
		t.Fatalf("update project repo: %v", err)
	}
	_, err := e.review.RerunReview(context.Background(), e.user.ID, e.project.ID, 7, "oldowner/oldrepo")
	if err == nil {
		t.Fatal("旧仓库记录的重跑应被拒绝")
	}
	var appErr *errs.AppError
	if !errors.As(err, &appErr) || appErr.Code != errs.ErrInvalidParams.Code {
		t.Fatalf("err = %v, want 40001", err)
	}
}

// 记录页的 can_rerun 随项目仓库判断：历史仓库的记录为 false。
func TestReviewRecordsCanRerunTracksProjectRepo(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	now := time.Now().UTC()
	rows := []model.ReviewPullRequest{
		{ProjectID: e.project.ID, RepoFullName: "oldowner/oldrepo", PRNumber: 1, UserID: e.user.ID, UpdatedAt: now},
		{ProjectID: e.project.ID, RepoFullName: "newowner/newrepo", PRNumber: 2, UserID: e.user.ID, UpdatedAt: now},
	}
	if err := e.svc.db.Create(&rows).Error; err != nil {
		t.Fatalf("seed ledgers: %v", err)
	}
	e.project.GithubOwner, e.project.GithubRepo = "newowner", "newrepo"
	if err := e.svc.db.Model(e.project).Updates(map[string]any{
		"github_owner": "newowner", "github_repo": "newrepo"}).Error; err != nil {
		t.Fatalf("update project repo: %v", err)
	}
	records, err := e.review.ListReviewRecords(context.Background(), e.user.ID, e.project.ID, 10)
	if err != nil {
		t.Fatalf("list records: %v", err)
	}
	got := map[int]bool{}
	for _, r := range records {
		got[r.PrNumber] = r.CanRerun
	}
	if got[1] || !got[2] {
		t.Fatalf("can_rerun = %+v, want 旧仓库 false / 新仓库 true", got)
	}
}

// 项目换仓库后，旧仓库的 pending 账本不能被新仓库的启用设置带出去同步。
func TestReviewSyncSkipsHistoricalRepoLedger(t *testing.T) {
	e := newReviewEngineEnv(t)
	now := time.Now().UTC()
	current := strings.ToLower(e.project.GithubOwner + "/" + e.project.GithubRepo)
	historical := "oldowner/oldrepo"
	e.fake.SetPR(historical, 77, &githubapi.PullRequest{
		RepoFullName: historical, Number: 77, HeadSHA: "sha-h",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: historical, PRNumber: 77, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-h", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	e.enableReview(t) // 项目当前仓库 enabled

	e.review.syncPendingPRs(now)
	if got := e.fake.TotalCalls(); got != 0 {
		t.Fatalf("历史仓库账本不应发 GitHub 产出，calls = %d（current=%s）", got, current)
	}
}

// GitHub 已前进但 webhook 未处理时，同步不能拿新 head 的 PR 去发布旧轮结论。
func TestReviewSyncSkipsWhenRemoteHeadAdvanced(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	// 远端已经是 sha-new，账本/运行还是 sha-old。
	e.fake.SetPR(repo, 88, &githubapi.PullRequest{
		RepoFullName: repo, Number: 88, HeadSHA: "sha-new",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 88, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-old", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	e.enableReview(t)
	e.seedRun(t, repo, 88, "sha-old", model.ReviewRunCompleted, now)

	e.review.syncPendingPRs(now)
	if got := len(e.fake.StatusesForContext("代码审查")); got != 0 {
		t.Fatalf("远端 head 已前进时不应写 commit status，calls = %d", got)
	}
	if got := e.fake.Comments(repo, 88); len(got) != 0 {
		t.Fatalf("远端 head 已前进时不应写评论，comments = %v", got)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 88", repo)
	if !ledger.SyncPending {
		t.Fatal("sync_pending 应保留，等 webhook 推进 head")
	}
	if ledger.SyncError != "" {
		t.Fatalf("deferred 不算同步失败，sync_error = %q", ledger.SyncError)
	}
}

// epochBumpOnDiffClient 在读 diff 时推进 epoch：模拟最慢的一次调用期间新
// webhook 抢先进来；行内 review 提交前必须再复核 epoch。
type epochBumpOnDiffClient struct {
	githubapi.GitHubClient
	db   *gorm.DB
	repo string
	pr   int
	done bool
}

func (c *epochBumpOnDiffClient) GetPullRequestDiff(ctx context.Context, inst int64, repo string, number int) (string, error) {
	if !c.done {
		c.done = true
		if err := c.db.Model(&model.ReviewPullRequest{}).
			Where("repo_full_name = ? AND pr_number = ?", c.repo, c.pr).
			Update("sync_epoch", gorm.Expr("sync_epoch + 1")).Error; err != nil {
			return "", err
		}
	}
	return c.GitHubClient.GetPullRequestDiff(ctx, inst, repo, number)
}

func TestReviewInlineReviewRechecksEpoch(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 99, &githubapi.PullRequest{
		RepoFullName: repo, Number: 99, HeadSHA: "sha-i",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.fake.SetDiff("@@ -1,1 +1,2 @@\n a\n+b\n")
	e.seedRunWithFindings(t, repo, 99, "sha-i", now)
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 99, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-i", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	e.enableReview(t)
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &epochBumpOnDiffClient{GitHubClient: e.fake, db: e.svc.db, repo: repo, pr: 99}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, nil, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	if got := e.fake.ReviewEvents(repo); len(got) != 0 {
		t.Fatalf("epoch 推进后不应提交行内 review，events = %v", got)
	}
}

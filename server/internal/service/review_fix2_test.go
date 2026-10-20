package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// INT-135 第二轮审查修复测试：仓库键小写（W1）、同步中段 epoch 守卫（W2）、
// UTF-8 状态截断（W3）、installation_id 不覆盖（N5）、派发成功路径（N2）。

// TestReviewRepoCaseNormalization W1：项目仓库写成 GitHub 规范大小写、
// webhook 全小写时只落一套账；连续两轮共用同一本总账。
func TestReviewRepoCaseNormalization(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.project.GithubOwner, e.project.GithubRepo = "Gitbobobo", "Fast_Ship"
	if err := e.svc.db.Model(e.project).Updates(map[string]any{
		"github_owner": "Gitbobobo", "github_repo": "Fast_Ship"}).Error; err != nil {
		t.Fatalf("rename project repo: %v", err)
	}
	e.enableReview(t)
	repo := "gitbobobo/fast_ship"
	ctx := context.Background()
	now := time.Now().UTC()

	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-c1",
		prPayload("opened", repo, 91, "sha-c1", false, false)); err != nil {
		t.Fatalf("webhook 1: %v", err)
	}
	e.svc.db.Model(&model.ReviewDebounce{}).
		Where("repo_full_name = ? AND pr_number = 91", repo).
		Update("fire_at", now.Add(-time.Second))
	e.review.processDueDebounces(now.Add(time.Second))

	// 第二次推送：把首个 seen 行的 pr_updated_at 改旧，防过期 SHA 判定吞掉。
	e.svc.db.Model(&model.ReviewSeenSHA{}).Where("head_sha = 'sha-c1'").
		Update("pr_updated_at", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-c2",
		prPayload("synchronize", repo, 91, "sha-c2", false, false)); err != nil {
		t.Fatalf("webhook 2: %v", err)
	}
	e.svc.db.Model(&model.ReviewDebounce{}).
		Where("repo_full_name = ? AND pr_number = 91", repo).
		Update("fire_at", now.Add(-time.Second))
	e.review.processDueDebounces(now.Add(2 * time.Second))

	// 只有一本账，键全小写，轮次连续推进到 2。
	var ledgers []model.ReviewPullRequest
	if err := e.svc.db.Where("pr_number = 91").Find(&ledgers).Error; err != nil {
		t.Fatalf("ledgers: %v", err)
	}
	if len(ledgers) != 1 || ledgers[0].RepoFullName != repo {
		t.Fatalf("ledgers = %+v, want 1 row keyed %q", ledgers, repo)
	}
	if ledgers[0].RoundNumber != 2 || ledgers[0].HeadSHA != "sha-c2" {
		t.Fatalf("ledger round/head = %d/%q, want 2/sha-c2", ledgers[0].RoundNumber, ledgers[0].HeadSHA)
	}
	var mixedRuns, mixedDebounce, mixedSeen int64
	e.svc.db.Model(&model.ReviewRun{}).
		Where("pr_number = 91 AND repo_full_name <> ?", repo).Count(&mixedRuns)
	e.svc.db.Model(&model.ReviewDebounce{}).
		Where("repo_full_name <> lower(repo_full_name)").Count(&mixedDebounce)
	e.svc.db.Model(&model.ReviewSeenSHA{}).
		Where("pr_number = 91 AND repo_full_name <> ?", repo).Count(&mixedSeen)
	if mixedRuns+mixedDebounce+mixedSeen != 0 {
		t.Fatalf("mixed-case rows: runs=%d debounce=%d seen=%d", mixedRuns, mixedDebounce, mixedSeen)
	}
	var rounds []int
	if err := e.svc.db.Model(&model.ReviewRun{}).
		Where("repo_full_name = ? AND pr_number = 91", repo).
		Distinct().Order("round_number").Pluck("round_number", &rounds).Error; err != nil {
		t.Fatalf("rounds: %v", err)
	}
	if len(rounds) != 2 || rounds[0] != 1 || rounds[1] != 2 {
		t.Fatalf("rounds = %v, want [1 2]", rounds)
	}
}

// epochBumpClient 包一层 GitHub 客户端：写「代码审查」commit status 时先把
// 总账 sync_epoch 推进一步——模拟同步中途落地的新 webhook；W2 的守卫应让
// 本次同步的中段写库全部落空。
type epochBumpClient struct {
	githubapi.GitHubClient
	db   *gorm.DB
	repo string
	pr   int
	done bool
}

func (c *epochBumpClient) CreateCommitStatus(ctx context.Context, inst int64, repo, sha, state, sctx, desc, url string) error {
	if sctx == reviewCodeStatusContext && !c.done {
		c.done = true
		if err := c.db.Model(&model.ReviewPullRequest{}).
			Where("repo_full_name = ? AND pr_number = ?", c.repo, c.pr).
			Update("sync_epoch", gorm.Expr("sync_epoch + 1")).Error; err != nil {
			return err
		}
	}
	return c.GitHubClient.CreateCommitStatus(ctx, inst, repo, sha, state, sctx, desc, url)
}

// TestReviewSyncEpochGuard W2：同步途中 epoch 被推进 → 旧快照的状态不写回
// 总账，sync_pending 保留给新 epoch 的下轮重试。
func TestReviewSyncEpochGuard(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 77, &githubapi.PullRequest{
		RepoFullName: repo, Number: 77, HeadSHA: "sha-g",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	// 运行已完成且干净 → codeState=success ≠ 总账现值，触发中段写库。
	run := e.seedRun(t, repo, 77, "sha-g", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Update("result_json",
		`{"summary":"ok","findings":[],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`)
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 77, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-g", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})

	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &epochBumpClient{GitHubClient: e.fake, db: e.svc.db, repo: repo, pr: 77}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, nil, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	var after model.ReviewPullRequest
	e.svc.db.First(&after, "repo_full_name = ? AND pr_number = 77", repo)
	if after.CodeStatus != "" {
		t.Fatalf("code_status = %q，epoch 推进后旧同步不得写回", after.CodeStatus)
	}
	if !after.SyncPending {
		t.Fatal("sync_pending 应保留（留给新 epoch 的下轮重试）")
	}
	// commit status 网络写已发出（不可回滚），epoch 守卫挡的只是 DB 写回。
	if got := e.fake.StatusesForContext("代码审查"); len(got) != 1 {
		t.Fatalf("code status calls = %d, want 1", len(got))
	}
}

// TestStatusDescUTF8 W3：长中文描述按 rune 截断——产物是合法 UTF-8 且总长
// ≤140 字节；组合显示名有上限。
func TestStatusDescUTF8(t *testing.T) {
	long := strings.Repeat("审查发现问题：", 40)
	got := statusDesc(long)
	if len(got) > 140 {
		t.Fatalf("statusDesc len = %d, want ≤140", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatal("statusDesc 产出非法 UTF-8（截断劈断了多字节序列）")
	}
	if !strings.HasSuffix(got, "...") {
		t.Fatal("截断产物应以 ... 结尾")
	}
	short := "审查通过"
	if statusDesc(short) != short {
		t.Fatal("短串应原样返回")
	}

	label := runCombinationLabel(&model.ReviewRun{
		Model: strings.Repeat("模型名", 30), InstanceID: "inst",
	})
	if n := utf8.RuneCountInString(label); n > reviewLabelMaxRunes+1 {
		t.Fatalf("label runes = %d, want ≤%d（含省略号）", n, reviewLabelMaxRunes+1)
	}
	if !strings.HasSuffix(label, "…") {
		t.Fatal("超长标签应以省略号结尾")
	}
}

// TestReviewWebhookNoInstallationID N5：payload 未带 installation.id（0）时
// 不覆盖总账已知的安装 id。
func TestReviewWebhookNoInstallationID(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 66, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-old-i", RoundNumber: 1, UpdatedAt: now,
	})
	e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 66, HeadSHA: "sha-old-i",
		AcceptedAt: now,
	})

	// 去掉 installation 字段的 payload。
	var payload map[string]any
	if err := json.Unmarshal(prPayload("synchronize", repo, 66, "sha-new-i", false, false), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	delete(payload, "installation")
	body, _ := json.Marshal(payload)

	if err := e.review.HandleGitHubWebhook(context.Background(), "pull_request", "d-ni", body); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 66", repo)
	if ledger.InstallationID != 42 {
		t.Fatalf("installation_id = %d, want 42（payload 缺省不覆盖）", ledger.InstallationID)
	}
}

// TestReviewLaunchRunSuccess N2：派发成功路径——launch 确认后写 dispatched_at
// 与 t3_project_id，有界窗口内解析出 worktree_path，基线用完即释放。
func TestReviewLaunchRunSuccess(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-d")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	threadID := ""
	t3 := &fakeReviewT3{
		// 第 1 次列举是 launch 前基线（空），第 2 次是 launch 后多出一个路径。
		listQueue: [][]string{{}, {"/ws/wt-new"}},
		createFn: func(_ context.Context, _, _ string, req *T3CreateThreadRequest) (*T3ThreadLaunchResult, bool, error) {
			threadID = *req.ThreadId
			return &T3ThreadLaunchResult{ThreadId: *req.ThreadId}, true, nil
		},
	}
	review := e.withT3(t3, nil, nil)

	run := e.seedRun(t, repo, 33, "sha-d", model.ReviewRunDispatched, now)
	e.svc.db.Model(run).Update("environment_id", env.ID)

	review.launchRun(context.Background(), run.ID, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunDispatched {
		t.Fatalf("status = %q, want dispatched（launch 已确认，等回报）", after.Status)
	}
	if after.DispatchedAt == nil {
		t.Fatal("dispatched_at 应已落库")
	}
	if after.WorktreePath != "/ws/wt-new" {
		t.Fatalf("worktree_path = %q, want /ws/wt-new", after.WorktreePath)
	}
	if after.T3ProjectID != "t3p" || after.ThreadID != threadID {
		t.Fatalf("t3_project_id/thread_id = %q/%q", after.T3ProjectID, after.ThreadID)
	}
	if _, ok := review.baselineFor(run.ID); ok {
		t.Fatal("launch 后基线应已释放")
	}
	if len(t3.created) != 1 || t3.created[0].ProjectId != "t3p" {
		t.Fatalf("create requests = %+v", t3.created)
	}
}

// TestReviewCleanupCancelledNoDelete N2：取消且派发未确认的运行无法证明
// 工作区归属——worktree_state=failed，绝不调用 RemoveWorktree。
func TestReviewCleanupCancelledNoDelete(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)

	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-cx")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")

	// superseded 取消：派发已起步但 launch 未确认（无 dispatched_at/路径）。
	run := e.seedRun(t, repo, 34, "sha-cx", model.ReviewRunCancelled, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"dispatch_started_at": now, "finished_at": now,
		"error_code": "superseded", "environment_id": env.ID,
	})
	run.DispatchStartedAt, run.FinishedAt, run.ErrorCode, run.EnvironmentID =
		&now, &now, "superseded", env.ID

	review.cleanupRunWorktree(run, now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.WorktreeState != model.ReviewWorktreeFailed {
		t.Fatalf("worktree_state = %q, want failed", after.WorktreeState)
	}
	if len(t3.removed) != 0 {
		t.Fatalf("RemoveWorktree 不得被调用，removed = %v", t3.removed)
	}
}

// TestDispatchReclaimsStaleClaim 进程在认领后、建线程前退出时，运行会停在
// dispatched + dispatch_started_at 非空、dispatched_at 为空；超过认领窗口后
// 调度阶段必须重新认领（CreateThread 按预分配 ID 幂等），不能只等超时。
func TestDispatchReclaimsStaleClaim(t *testing.T) {
	e := newReviewEngineEnv(t)
	now := time.Now().UTC()
	run := &model.ReviewRun{
		ID: "run-stale-claim", UserID: e.user.ID, ProjectID: e.project.ID,
		Kind: model.ReviewKindCode, RepoFullName: "o/r", PRNumber: 7,
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

	e.review.dispatchRuns(now)

	var got model.ReviewRun
	if err := e.svc.db.Where("id = ?", run.ID).First(&got).Error; err != nil {
		t.Fatalf("load run: %v", err)
	}
	if got.DispatchStartedAt == nil || !got.DispatchStartedAt.After(claimed) {
		t.Fatalf("dispatch_started_at 未被重新认领: %+v", got.DispatchStartedAt)
	}
	// 测试环境没有 T3 访问面：重新认领后派发按预期失败落终态。
	if got.Status != model.ReviewRunFailed || got.ErrorCode != "dispatch_failed" {
		t.Fatalf("status = %s/%s, want failed/dispatch_failed", got.Status, got.ErrorCode)
	}
}

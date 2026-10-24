package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// INT-135 1c 执行引擎测试：webhook 触发/过滤、delivery 幂等、防抖消费开轮、
// 排队提升并发上限、超时、结果校验、汇总文案与视觉衔接判定。

// reviewEngineEnv 一套可用的审查执行测试环境（不落 T3——派发不在本文件
// 范围；GitHub 侧用 githubapp.Fake；media 默认恒成功的截图下载桩，需要
// 失败场景的测试自己换桩）。
type reviewEngineEnv struct {
	svc     *testServices
	review  *ReviewService
	fake    *githubapp.Fake
	media   *fakeReviewMedia
	user    *model.User
	project *model.Project
}

func newReviewEngineEnv(t *testing.T) *reviewEngineEnv {
	t.Helper()
	svc := setupTestServices(t)
	fake := githubapp.NewFake()
	media := fakeScreenshotMedia(t)
	review := NewReviewService(svc.db, svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(svc.db),
		NewNotificationSink(svc.db),
		githubapp.StaticClients{Client: fake},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, media, nil, svc.cfg, zap.NewNop())
	user := createTestUser(t, svc.db, "reviewer")
	project := createTestProject(t, svc.db, user.ID)
	return &reviewEngineEnv{svc: svc, review: review, fake: fake, media: media, user: user, project: project}
}

// enableReview 直接落启用设置（绕过绑定校验——执行路径只读落库 JSON）。
// repo_full_name 快照按项目当前审查仓库落小写：触发反查只认快照列。
func (e *reviewEngineEnv) enableReview(t *testing.T, opts ...func(*model.ProjectReviewSettings)) {
	t.Helper()
	code, err := marshalCombinations([]api.ReviewCombination{testCombo("env-1")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	settings := &model.ProjectReviewSettings{
		ProjectID:             e.project.ID,
		UserID:                e.user.ID,
		RepoFullName:          strings.ToLower(e.project.CodeRepoFullName()),
		Enabled:               true,
		TriggerOpened:         true,
		TriggerSynchronize:    true,
		TriggerReadyForReview: false,
		TriggerReopened:       false,
		CodeCombinationsJSON:  code,
		CreatedAt:             time.Now().UTC(),
		UpdatedAt:             time.Now().UTC(),
	}
	for _, opt := range opts {
		opt(settings)
	}
	if err := e.svc.db.Create(settings).Error; err != nil {
		t.Fatalf("enable review: %v", err)
	}
}

// prPayloadSeq 给测试 payload 生成单调递增的 updated_at。
var prPayloadSeq atomic.Int64

// prPayload 组一个 pull_request webhook body。
func prPayload(action, repo string, number int, headSHA string, draft, fork bool) []byte {
	headRepo := repo
	if fork {
		headRepo = "someone-else/" + strings.SplitN(repo, "/", 2)[1]
	}
	// 每次调用递增一秒：真实投递的 pull_request.updated_at 单调递增，
	// 固定时间戳会让「同一 SHA 的后续事件」被当成迟到投递而忽略。
	prPayloadSeq.Add(1)
	updatedAt := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC).
		Add(time.Duration(prPayloadSeq.Load()) * time.Second)
	payload := map[string]any{
		"action": action,
		"number": number,
		"pull_request": map[string]any{
			"draft":      draft,
			"html_url":   "https://github.com/" + repo + "/pull/" + fmt.Sprint(number),
			"title":      "PR title",
			"updated_at": updatedAt.Format(time.RFC3339),
			"head": map[string]any{
				"sha": headSHA, "ref": "feature",
				"repo": map[string]any{"fork": fork, "full_name": headRepo},
			},
			"base": map[string]any{
				"ref":  "main",
				"repo": map[string]any{"full_name": repo},
			},
		},
		"repository":   map[string]any{"full_name": repo},
		"installation": map[string]any{"id": 42},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

func (e *reviewEngineEnv) debounceCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	if err := e.svc.db.Model(&model.ReviewDebounce{}).Count(&n).Error; err != nil {
		t.Fatalf("count debounces: %v", err)
	}
	return n
}

// TestReviewWebhookTriggerAndFiltering 触发与事件过滤：正常 opened 落防抖行；
// draft/fork/非支持 action/未勾选触发/未关联仓库一律忽略且幂等。
func TestReviewWebhookTriggerAndFiltering(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()

	// 正常 opened → 防抖行 + seen SHA + 总账 sync_pending。
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-1",
		prPayload("opened", repo, 7, "sha-a", false, false)); err != nil {
		t.Fatalf("webhook opened: %v", err)
	}
	var debounce model.ReviewDebounce
	if err := e.svc.db.Where("repo_full_name = ? AND pr_number = 7", repo).First(&debounce).Error; err != nil {
		t.Fatalf("expected debounce row: %v", err)
	}
	if debounce.HeadSHA != "sha-a" || debounce.Action != "opened" {
		t.Fatalf("debounce head/action = %q/%q", debounce.HeadSHA, debounce.Action)
	}
	var seen int64
	e.svc.db.Model(&model.ReviewSeenSHA{}).Where("head_sha = 'sha-a'").Count(&seen)
	if seen != 1 {
		t.Fatalf("seen sha rows = %d", seen)
	}
	var ledger model.ReviewPullRequest
	if err := e.svc.db.Where("repo_full_name = ? AND pr_number = 7", repo).First(&ledger).Error; err != nil {
		t.Fatalf("expected ledger row: %v", err)
	}
	if ledger.HeadSHA != "sha-a" {
		t.Fatalf("ledger head = %q, want sha-a", ledger.HeadSHA)
	}
	if ledger.SyncPending {
		t.Fatal("webhook 不置 sync_pending（开轮时才置，避免拿空运行集发布）")
	}

	// draft / fork / 不支持 action / 非 pull_request 事件：全部忽略。
	for i, body := range [][]byte{
		prPayload("opened", repo, 8, "sha-b", true, false),
		prPayload("opened", repo, 8, "sha-b", false, true),
		prPayload("closed", repo, 8, "sha-b", false, false),
		prPayload("opened", "other/repo", 8, "sha-b", false, false),
	} {
		if err := e.review.HandleGitHubWebhook(ctx, "pull_request", fmt.Sprintf("d-x%d", i), body); err != nil {
			t.Fatalf("ignored webhook %d: %v", i, err)
		}
	}
	if err := e.review.HandleGitHubWebhook(ctx, "push", "d-push",
		prPayload("opened", repo, 9, "sha-c", false, false)); err != nil {
		t.Fatalf("push event: %v", err)
	}
	if got := e.debounceCount(t); got != 1 {
		t.Fatalf("debounce rows = %d, want 1", got)
	}
}

// TestReviewWebhookTriggerSettingsFiltering 未勾选的触发位不开轮；
// ready_for_review 勾选后受理。（未勾选动作仍登记 seen，故换 SHA 验证受理。）
func TestReviewWebhookTriggerSettingsFiltering(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()

	// ready_for_review 默认未勾选 → 不写防抖行。
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-1",
		prPayload("ready_for_review", repo, 3, "sha-r", false, false)); err != nil {
		t.Fatalf("ready_for_review: %v", err)
	}
	if got := e.debounceCount(t); got != 0 {
		t.Fatalf("debounce rows = %d, want 0", got)
	}

	// 勾选后受理（新 SHA——旧 SHA 已被未勾选投递登记为 seen）。
	e.svc.db.Model(&model.ProjectReviewSettings{}).Where("project_id = ?", e.project.ID).
		Update("trigger_ready_for_review", true)
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-2",
		prPayload("ready_for_review", repo, 3, "sha-r2", false, false)); err != nil {
		t.Fatalf("ready_for_review enabled: %v", err)
	}
	if got := e.debounceCount(t); got != 1 {
		t.Fatalf("debounce rows = %d, want 1", got)
	}
}

// TestReviewWebhookDeliveryIdempotent 同一 delivery 重复投递不再处理。
func TestReviewWebhookDeliveryIdempotent(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	body := prPayload("opened", repo, 5, "sha-1", false, false)

	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "dup-1", body); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	err := e.review.HandleGitHubWebhook(ctx, "pull_request", "dup-1", body)
	if err != ErrDeliveryDuplicated {
		t.Fatalf("second delivery err = %v, want ErrDeliveryDuplicated", err)
	}
	if got := e.debounceCount(t); got != 1 {
		t.Fatalf("debounce rows = %d, want 1", got)
	}
}

// TestReviewWebhookSupersedesStaleSHA 新 head 覆盖防抖行并取消旧 head 的在途运行。
func TestReviewWebhookSupersedesStaleSHA(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	newer := time.Now().UTC()

	// 旧 head 有一条 running 运行。
	old := &model.ReviewRun{
		ID: uuid.NewString(), UserID: e.user.ID, ProjectID: e.project.ID,
		Kind: model.ReviewKindCode, RepoFullName: repo, PRNumber: 4,
		HeadSHA: "sha-old", RoundNumber: 1, EnvironmentID: "env-1",
		InstanceID: "claude", Model: "m",
		ThreadID: uuid.NewString(), LaunchCommandID: uuid.NewString(), InitialMessageID: uuid.NewString(),
		Status: model.ReviewRunRunning, DeadlineAt: newer.Add(time.Hour),
		WorktreeState: model.ReviewWorktreeNone, CreatedAt: newer, UpdatedAt: newer,
	}
	if err := e.svc.db.Create(old).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-1",
		prPayload("synchronize", repo, 4, "sha-new", false, false)); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	var run model.ReviewRun
	e.svc.db.First(&run, "id = ?", old.ID)
	if run.Status != model.ReviewRunCancelled || run.ErrorCode != "superseded" {
		t.Fatalf("old run status = %q/%q, want cancelled/superseded", run.Status, run.ErrorCode)
	}
	var debounce model.ReviewDebounce
	if err := e.svc.db.First(&debounce).Error; err != nil || debounce.HeadSHA != "sha-new" {
		t.Fatalf("debounce head = %q, want sha-new", debounce.HeadSHA)
	}
}

// TestReviewDebounceConsumeOpensRound 到期防抖行消费：开轮 + 每组合一行运行 +
// 总账推进 + sync_pending；防抖行被删。
func TestReviewDebounceConsumeOpensRound(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	e.fake.SetPR(repo, 9, &githubapi.PullRequest{HeadSHA: "sha-9"})
	e.svc.db.Create(&model.ReviewDebounce{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 9,
		HeadSHA: "sha-9", Action: "synchronize", InstallationID: 42,
		PRTitle: "t", PRURL: "u", BaseRef: "main", HeadRef: "feature",
		FireAt: now.Add(-time.Second), UpdatedAt: now.Add(-time.Minute),
	})
	e.review.processDueDebounces(now)

	if got := e.debounceCount(t); got != 0 {
		t.Fatalf("debounce rows = %d, want 0", got)
	}
	var runs []model.ReviewRun
	if err := e.svc.db.Where("repo_full_name = ? AND pr_number = 9", repo).Find(&runs).Error; err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1（一个代码组合）", len(runs))
	}
	run := runs[0]
	if run.Kind != model.ReviewKindCode || run.HeadSHA != "sha-9" || run.RoundNumber != 1 {
		t.Fatalf("run kind/head/round = %q/%q/%d", run.Kind, run.HeadSHA, run.RoundNumber)
	}
	if run.Status != model.ReviewRunDispatched {
		t.Fatalf("run status = %q, want dispatched（名额内直接提升）", run.Status)
	}
	if !run.DeadlineAt.After(now) {
		t.Fatal("deadline should be promotion time + kind timeout")
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 9", repo)
	if ledger.HeadSHA != "sha-9" || ledger.RoundNumber != 1 || !ledger.SyncPending {
		t.Fatalf("ledger = %q/%d/pending=%v", ledger.HeadSHA, ledger.RoundNumber, ledger.SyncPending)
	}
}

// TestReviewQueuePromotionLimit 每用户在途上限 5：多余保持 queued 等名额。
func TestReviewQueuePromotionLimit(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	// 4 条在途 + 3 条排队 → 只提升 1 条（合计 5）。
	for i := 0; i < 4; i++ {
		e.seedRun(t, repo, 20+i, "sha-q"+fmt.Sprint(i), model.ReviewRunDispatched, now)
	}
	var queuedIDs []string
	for i := 0; i < 3; i++ {
		queuedIDs = append(queuedIDs, e.seedRun(t, repo, 30+i, "sha-w"+fmt.Sprint(i), model.ReviewRunQueued, now.Add(time.Duration(i)*time.Second)).ID)
	}
	e.review.promoteQueuedRuns(now)

	var dispatched, queued int64
	e.svc.db.Model(&model.ReviewRun{}).Where("status = ?", model.ReviewRunDispatched).Count(&dispatched)
	e.svc.db.Model(&model.ReviewRun{}).Where("status = ?", model.ReviewRunQueued).Count(&queued)
	if dispatched != 5 || queued != 2 {
		t.Fatalf("dispatched=%d queued=%d, want 5/2", dispatched, queued)
	}
	// 提升的是最老的一条。
	var promoted model.ReviewRun
	e.svc.db.First(&promoted, "id = ?", queuedIDs[0])
	if promoted.Status != model.ReviewRunDispatched {
		t.Fatalf("oldest queued status = %q, want dispatched", promoted.Status)
	}
}

// TestReviewMarkTimeouts 过 deadline 的 dispatched/running → timed_out +
// 令牌消费 + 总账 sync_pending + 通知。
func TestReviewMarkTimeouts(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	run := e.seedRun(t, repo, 11, "sha-t", model.ReviewRunRunning, now)
	e.svc.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).Update("deadline_at", now.Add(-time.Minute))
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "h", CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour),
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 11, ProjectID: e.project.ID, UserID: e.user.ID,
		UpdatedAt: now,
	})

	e.review.markTimeouts(now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunTimedOut {
		t.Fatalf("status = %q, want timed_out", after.Status)
	}
	var token model.ReviewRunToken
	e.svc.db.First(&token, "run_id = ?", run.ID)
	if token.ConsumedAt == nil {
		t.Fatal("token should be consumed")
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 11", repo)
	if !ledger.SyncPending {
		t.Fatal("sync_pending should be set")
	}
	var notification int64
	e.svc.db.Model(&model.Notification{}).
		Where("kind = ? AND user_id = ?", model.NotificationReviewRunTimeout, e.user.ID).Count(&notification)
	if notification != 1 {
		t.Fatalf("timeout notifications = %d, want 1", notification)
	}
}

// seedRun 落一行运行的最小助手。
func (e *reviewEngineEnv) seedRun(t *testing.T, repo string, pr int, head string, status model.ReviewRunStatus, now time.Time) *model.ReviewRun {
	t.Helper()
	run := &model.ReviewRun{
		ID: uuid.NewString(), UserID: e.user.ID, ProjectID: e.project.ID,
		Kind: model.ReviewKindCode, RepoFullName: repo, PRNumber: pr,
		HeadSHA: head, RoundNumber: 1, EnvironmentID: "env-1",
		InstanceID: "claude", Model: "m",
		ThreadID: uuid.NewString(), LaunchCommandID: uuid.NewString(), InitialMessageID: uuid.NewString(),
		Status: status, DeadlineAt: now.Add(time.Hour),
		WorktreeState: model.ReviewWorktreeNone, CreatedAt: now, UpdatedAt: now,
	}
	if err := e.svc.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return run
}

// TestReviewResultValidation 结果校验：非法字段 40004；合法 → completed +
// 令牌消费 + sync_pending；终态同文重放幂等、不同文 40106。
func TestReviewResultValidation(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	e.fake.SetPR(repo, 12, &githubapi.PullRequest{
		RepoFullName: repo, Number: 12, HeadSHA: "sha-r",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 12, ProjectID: e.project.ID, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-r", RoundNumber: 1, UpdatedAt: now,
	})

	newRun := func(status model.ReviewRunStatus) *model.ReviewRun {
		return e.seedRun(t, repo, 12, "sha-r", status, now)
	}

	// 非法 level → 40004。
	run1 := newRun(model.ReviewRunRunning)
	bad := `{"findings":[{"level":"SEV1","file":"a.go","body":"x"}],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	err := e.review.AgentSubmitResult(context.Background(), run1.ID, []byte(bad))
	assertErrCode(t, err, 40004)
	// rejected 缺 reason → 40004。
	badPrior := `{"findings":[],"prior":[{"status":"rejected","body":"b"}],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	assertErrCode(t, e.review.AgentSubmitResult(context.Background(), run1.ID, []byte(badPrior)), 40004)

	// 合法 → completed + sync_pending。
	good := `{"findings":[{"level":"P1","file":"a.go","line":3,"body":"nil deref"}],"prior":[],"summary":"found one","ui_changed":false,"ui_changed_since_last_visual":false}`
	if err := e.review.AgentSubmitResult(context.Background(), run1.ID, []byte(good)); err != nil {
		t.Fatalf("submit good: %v", err)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run1.ID)
	if after.Status != model.ReviewRunCompleted || after.Summary != "found one" {
		t.Fatalf("run status/summary = %q/%q", after.Status, after.Summary)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 12", repo)
	if !ledger.SyncPending {
		t.Fatal("sync_pending should be set after submit")
	}

	// 完全相同重放 → 幂等成功。
	if err := e.review.AgentSubmitResult(context.Background(), run1.ID, []byte(good)); err != nil {
		t.Fatalf("replay identical result: %v", err)
	}
	// 终态不同文 → 40106。
	diff := `{"findings":[],"prior":[],"summary":"other","ui_changed":false,"ui_changed_since_last_visual":false}`
	assertErrCode(t, e.review.AgentSubmitResult(context.Background(), run1.ID, []byte(diff)), 40106)
}

// TestReviewResultHeadChanged 提交时 head 已前进 → 40920 + 运行取消 + 令牌消费。
func TestReviewResultHeadChanged(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 13, &githubapi.PullRequest{
		RepoFullName: repo, Number: 13, HeadSHA: "sha-newer",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		RepoFullName: repo, PRNumber: 13, ProjectID: e.project.ID, UserID: e.user.ID,
		HeadSHA: "sha-old", RoundNumber: 1, UpdatedAt: now,
	})
	run := e.seedRun(t, repo, 13, "sha-old", model.ReviewRunRunning, now)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "h2", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	good := `{"findings":[],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(good))
	assertErrCode(t, err, 40920)
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunCancelled {
		t.Fatalf("status = %q, want cancelled", after.Status)
	}
}

// TestResolveRunToken 令牌解析：活令牌给身份并推进 running；消费/终态在
// requireLive 端点拒绝，result 端点放行。
func TestResolveRunToken(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	run := e.seedRun(t, repo, 14, "sha-k", model.ReviewRunDispatched, now)
	raw := "fsr_" + strings.Repeat("a", 43)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: sha256Hex(raw), CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	id, err := e.review.ResolveRunToken(context.Background(), raw, true)
	if err != nil || id.RunID != run.ID || id.UserID != e.user.ID {
		t.Fatalf("resolve: %+v err=%v", id, err)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunRunning {
		t.Fatalf("status = %q, want running after first auth", after.Status)
	}

	// 消费后 requireLive 拒绝、result 端点放行。
	consume := now.Add(time.Minute)
	e.svc.db.Model(&model.ReviewRunToken{}).Where("run_id = ?", run.ID).Update("consumed_at", consume)
	if _, err := e.review.ResolveRunToken(context.Background(), raw, true); err == nil {
		t.Fatal("consumed token must fail requireLive")
	}
	e.svc.db.Model(&model.ReviewRun{}).Where("id = ?", run.ID).Update("status", model.ReviewRunCompleted)
	if _, err := e.review.ResolveRunToken(context.Background(), raw, false); err != nil {
		t.Fatalf("terminal replay resolve: %v", err)
	}

	// 过期与伪造 → 40106。
	e.svc.db.Model(&model.ReviewRunToken{}).Where("run_id = ?", run.ID).Update("expires_at", now.Add(-time.Minute))
	if _, err := e.review.ResolveRunToken(context.Background(), raw, false); err == nil {
		t.Fatal("expired token must fail")
	}
	if _, err := e.review.ResolveRunToken(context.Background(), "fsr_unknown", true); err == nil {
		t.Fatal("unknown token must fail")
	}
}

// TestReviewSummarizeAndComment 汇总状态与评论文案。
func TestReviewSummarizeAndComment(t *testing.T) {
	now := time.Now().UTC()
	mkRun := func(status model.ReviewRunStatus, result string) model.ReviewRun {
		return model.ReviewRun{
			Kind: model.ReviewKindCode, Status: status, ResultJSON: result,
			InstanceID: "claude", Model: "m", EnvironmentID: "e",
			CreatedAt: now, UpdatedAt: now,
		}
	}
	pass := `{"findings":[],"prior":[],"summary":"ok","ui_changed":false,"ui_changed_since_last_visual":false}`
	fail := `{"findings":[{"level":"P0","file":"a.go","line":1,"body":"crash"},{"level":"Note","file":"b.go","body":"nit"}],"prior":[],"summary":"bad","ui_changed":false,"ui_changed_since_last_visual":false}`

	// 在途 → pending。
	if st, _ := summarizeCodeRuns([]model.ReviewRun{mkRun(model.ReviewRunRunning, "")}); st != "pending" {
		t.Fatalf("running state = %q", st)
	}
	// 有失败运行 → error。
	if st, _ := summarizeCodeRuns([]model.ReviewRun{mkRun(model.ReviewRunCompleted, pass), mkRun(model.ReviewRunFailed, "")}); st != "error" {
		t.Fatalf("mixed state = %q", st)
	}
	// P0 → failure，描述计数。
	st, desc := summarizeCodeRuns([]model.ReviewRun{mkRun(model.ReviewRunCompleted, fail)})
	if st != "failure" || !strings.Contains(desc, "P0 1 条") {
		t.Fatalf("failure state/desc = %q/%q", st, desc)
	}
	// 全净 → success。
	if st, _ := summarizeCodeRuns([]model.ReviewRun{mkRun(model.ReviewRunCompleted, pass)}); st != "success" {
		t.Fatalf("clean state = %q", st)
	}

	// 评论正文：标题/结论/分节 + 排序 + 遗留提示（inlineLines=nil：有行号的不加注）。
	body := buildCodeComment(reviewCommentMeta{RepoFullName: "o/r", HeadSHA: "0123456789abcdef", links: githubLinks("https://github.com"), RoundNumber: 1},
		[]model.ReviewRun{mkRun(model.ReviewRunCompleted, fail)}, nil, false, true)
	if !strings.Contains(body, reviewCodeCommentMarker) ||
		!strings.Contains(body, "## 代码审查 · 0123456 · 第 1 轮") ||
		!strings.Contains(body, "**P0** [a.go:1](https://github.com/o/r/blob/0123456789abcdef/a.go#L1)") {
		t.Fatalf("comment body missing finding: %s", body)
	}
	if !strings.Contains(body, "截图复核意见尚未处理") {
		t.Fatalf("comment body missing carry hint: %s", body)
	}
	// P0 在 Note（折叠块）之前。
	if strings.Index(body, "**P0**") > strings.Index(body, "<details>") {
		t.Fatal("findings should sort P0 before Note")
	}
}

// TestDecideVisual 视觉衔接判定表。
func TestDecideVisual(t *testing.T) {
	base := visualDecisionInput{
		CodeAllTerminal: true, CodeAllTerminalOK: true, HasVisualCombos: true,
		HasScreenshots: true, AnyUIChanged: true,
	}
	cases := []struct {
		name string
		mut  func(*visualDecisionInput)
		want visualDecision
	}{
		// 代码未全终态最优先：其他条件再齐备也只等待，不写任何视觉产出。
		{"代码未终态", func(in *visualDecisionInput) { in.CodeAllTerminal = false }, visualWait},
		{"代码未终态且截图已换", func(in *visualDecisionInput) {
			in.CodeAllTerminal = false
			in.ScreensChanged = true
		}, visualWait},
		{"代码未全净", func(in *visualDecisionInput) { in.CodeAllTerminalOK = false }, visualNone},
		{"截图已换但代码未全净", func(in *visualDecisionInput) {
			in.CodeAllTerminalOK = false
			in.ScreensChanged = true
		}, visualNone},
		{"无视觉组合", func(in *visualDecisionInput) { in.HasVisualCombos = false }, visualNone},
		{"无截图", func(in *visualDecisionInput) { in.HasScreenshots = false }, visualNone},
		{"无 UI 变化", func(in *visualDecisionInput) { in.AnyUIChanged = false }, visualNone},
		// E5：截图不可用不再是独立短路——本轮不开视觉（截图未变、上轮视觉后
		// 无 UI 改动）时探测根本不会发生，ScreensUnavailable 不能盖过沿用路径。
		{"截图不可用但本轮不开视觉", func(in *visualDecisionInput) { in.ScreensUnavailable = true }, visualNone},
		{"截图不可用且上轮遗留沿用 failure", func(in *visualDecisionInput) {
			in.ScreensUnavailable = true
			in.PriorVisualFailed = true
		}, visualCarryFailure},
		// 截图变了（本轮真的要开视觉）+ 下载全部失败 → 报「截图无法下载」。
		{"截图变了但下载失败", func(in *visualDecisionInput) {
			in.ScreensChanged = true
			in.ScreensUnavailable = true
		}, visualScreensUnavailable},
		{"截图变了", func(in *visualDecisionInput) { in.ScreensChanged = true }, visualStart},
		{"上轮后又有 UI 改动", func(in *visualDecisionInput) { in.AnyUIChangedSinceLastVisual = true }, visualStart},
		{"截图未变且遗留", func(in *visualDecisionInput) { in.PriorVisualFailed = true }, visualCarryFailure},
		// 沿用 success 只在「本轮确实无事可做」（截图未变且上轮视觉后无 UI
		// 改动）时发生；截图变了绝不沿用通过。
		{"截图未变且上轮通过", func(in *visualDecisionInput) { in.PriorVisualPassed = true }, visualCarrySuccess},
		{"截图变了不沿用通过", func(in *visualDecisionInput) {
			in.PriorVisualPassed = true
			in.ScreensChanged = true
		}, visualStart},
		{"截图未变且无遗留", func(in *visualDecisionInput) {}, visualNone},
	}
	for _, c := range cases {
		in := base
		c.mut(&in)
		if got := decideVisual(in); got != c.want {
			t.Fatalf("%s: decideVisual = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestReviewRerun 手动重跑：恒开新轮、取消在途、返回 head/round；未开启 → 40923。
func TestReviewRerun(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	// 未开启 → 40923。
	_, err := e.review.RerunReview(context.Background(), e.user.ID, e.project.ID, 1, "")
	assertErrCode(t, err, 40923)

	e.enableReview(t)
	e.fake.SetPR(repo, 2, &githubapi.PullRequest{
		RepoFullName: repo, Number: 2, HeadSHA: "sha-rr",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	old := e.seedRun(t, repo, 2, "sha-rr", model.ReviewRunRunning, now)

	out, err := e.review.RerunReview(context.Background(), e.user.ID, e.project.ID, 2, "")
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !out.Accepted || out.HeadSHA != "sha-rr" || out.RoundNumber != 2 {
		t.Fatalf("rerun out = %+v", out)
	}
	var oldRun model.ReviewRun
	e.svc.db.First(&oldRun, "id = ?", old.ID)
	if oldRun.Status != model.ReviewRunCancelled {
		t.Fatalf("old run status = %q", oldRun.Status)
	}
	var runs []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = 2 AND round_number = 2", repo).Find(&runs)
	if len(runs) != 1 || runs[0].Status != model.ReviewRunDispatched {
		t.Fatalf("round-2 runs = %+v", runs)
	}
	// F3：rerun 先解析真实安装 id（fake 默认 1）并落库，不写 0。
	if runs[0].InstallationID != 1 {
		t.Fatalf("run installation_id = %d, want 1（ResolveInstallation）", runs[0].InstallationID)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 2", repo)
	if ledger.InstallationID != 1 {
		t.Fatalf("ledger installation_id = %d, want 1", ledger.InstallationID)
	}
}

// assertErrCode AppError 码断言。
func assertErrCode(t *testing.T, err error, code int) {
	t.Helper()
	var ae *errs.AppError
	if err == nil || !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want code %d", err, code)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

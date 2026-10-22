package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// diffFailClient 只让 diff 读取失败：验证「单次 diff 读取失败只影响行内
// review」的软失败口径，其余 GitHub 面照常走 Fake。
type diffFailClient struct {
	githubapi.GitHubClient
}

func (c *diffFailClient) GetPullRequestDiff(context.Context, int64, string, int) (string, error) {
	return "", errors.New("上游 502")
}

// TestReviewDiffFailureKeepsTopLevelOutput diff 读取失败不再把整段同步判负：
// commit status 与顶层评论照常写出（已算好的结论不该被一次读失败扣住），
// 行内 review 推迟到下一轮——运行保持 posted_at 为空、账本保持 pending 并把
// 原因写进 sync_error，下一轮 diff 读得到时补发。同一份结论的重试不重复
// PATCH 顶层评论（每 tick 一次相同正文既耗配额又留编辑记录）。
func TestReviewDiffFailureKeepsTopLevelOutput(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 78, &githubapi.PullRequest{
		RepoFullName: repo, Number: 78, HeadSHA: "sha-d", State: "open",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	// 补发那一轮要能读到 diff：P1 落在右侧有效行 2 上才发得出行内 review。
	e.fake.SetDiff("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,1 +1,2 @@\n a\n+b\n")
	run := e.seedRunWithFindings(t, repo, 78, "sha-d", now)
	e.enableReview(t)
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 78, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-d", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &diffFailClient{GitHubClient: e.fake}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	if got := len(e.fake.StatusesForContext(reviewCodeStatusContext)); got == 0 {
		t.Fatal("diff 读取失败不应阻断代码 commit status 写入")
	}
	commentBody := ""
	for _, body := range e.fake.Comments(repo, 78) {
		if strings.Contains(body, reviewCodeCommentMarker) {
			commentBody = body
		}
	}
	if commentBody == "" {
		t.Fatal("diff 读取失败不应阻断顶层评论写入")
	}
	// 没读 diff 就无从判断行号是否有效，不臆断加「未行内标注」注记。
	if strings.Contains(commentBody, "未行内标注") {
		t.Fatal("本轮没读 diff，顶层评论不应加「未行内标注」注记")
	}
	if got := e.fake.ReviewEvents(repo); len(got) != 0 {
		t.Fatalf("diff 读取失败不应提交行内 review，events = %v", got)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.PostedAt != nil {
		t.Fatal("diff 读取失败不应把运行标记为已发布（行内意见会永久丢失）")
	}
	var ledger model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 78).First(&ledger)
	if !ledger.SyncPending {
		t.Fatal("行内阶段失败应保持 sync_pending，让下一轮同步重试")
	}
	if !strings.Contains(ledger.SyncError, "读取 diff 失败") {
		t.Fatalf("sync_error = %q, want 含「读取 diff 失败」", ledger.SyncError)
	}

	// 退避窗口过后的重试：同一份运行结论不再重写顶层评论（否则每次重试都 PATCH）。
	review.syncPendingPRs(now.Add(reviewSyncRetryBackoff + time.Second))
	counts := e.fake.CallCounts()
	if counts["CreatePullRequestComment"] != 1 || counts["UpdatePullRequestComment"] != 0 {
		t.Fatalf("评论写入 = create:%d update:%d, want 1/0（重试不重写相同正文）",
			counts["CreatePullRequestComment"], counts["UpdatePullRequestComment"])
	}
	if got := e.fake.ReviewEvents(repo); len(got) != 0 {
		t.Fatalf("重试仍读不到 diff，不应提交行内 review，events = %v", got)
	}

	// 下一轮（diff 读得到）补发行内 review：推迟而不是丢弃，并收尾账本。
	e.review.syncPendingPRs(now.Add(2 * reviewSyncRetryBackoff))
	if got := len(e.fake.ReviewEvents(repo)); got != 1 {
		t.Fatalf("下一轮应补发 1 条行内 review，实际 %d", got)
	}
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.PostedAt == nil {
		t.Fatal("行内 review 发出后应标记 posted_at")
	}
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 78).First(&ledger)
	if ledger.SyncPending || ledger.SyncError != "" {
		t.Fatalf("恢复后应收尾：pending=%v sync_error=%q", ledger.SyncPending, ledger.SyncError)
	}
}

// TestReviewDiffFailureWithoutPendingInlineKeepsSyncPending 行内运行都已发布
// （posted_at 非空）时 diff 仍读不到：错误照样上报、账本保持 pending，让
// 下一轮在 diff 恢复后重写顶层评论——本轮评论可能是按「没读 diff」口径写
// 的，吞掉错误会让「未行内标注」注记永久缺失。评论本身不重写（正文未变）。
func TestReviewDiffFailureWithoutPendingInlineKeepsSyncPending(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 79, &githubapi.PullRequest{
		RepoFullName: repo, Number: 79, HeadSHA: "sha-n", State: "open",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	run := e.seedRunWithFindings(t, repo, 79, "sha-n", now)
	e.svc.db.Model(run).Updates(map[string]any{"posted_at": now, "review_id": 7})
	e.enableReview(t)
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 79, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-n", RoundNumber: 1,
		CodeStatus: "failure", SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &diffFailClient{GitHubClient: e.fake}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	var ledger model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 79).First(&ledger)
	if !ledger.SyncPending {
		t.Fatal("没有待发布行内运行时 diff 错误也要保持 pending（顶层评论注记待补）")
	}
	if !strings.Contains(ledger.SyncError, "读取 diff 失败") {
		t.Fatalf("sync_error = %q, want 含「读取 diff 失败」", ledger.SyncError)
	}
	// 首轮仍要写出评论（此前没有），只是不重复改同一正文。
	counts := e.fake.CallCounts()
	if counts["CreatePullRequestComment"] != 1 || counts["UpdatePullRequestComment"] != 0 {
		t.Fatalf("评论写入 = create:%d update:%d, want 1/0（首次写出、重试不重写）",
			counts["CreatePullRequestComment"], counts["UpdatePullRequestComment"])
	}
}

// TestReviewDiffFailureRewritesCommentWhenBodyChanges diff 读不到且代码状态
// 没变时，评论正文仍可能变（多个组合里又完成一个、摘要/发现不同）：这轮必须
// 更新顶层评论，否则作者一直看到旧快照。防重复用「远端正文等于本轮正文」，
// 不能用代码状态做代理。
func TestReviewDiffFailureRewritesCommentWhenBodyChanges(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 80, &githubapi.PullRequest{
		RepoFullName: repo, Number: 80, HeadSHA: "sha-b", State: "open",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.enableReview(t)
	// 带行号 P1 才会触发 diff 读取（fetchInlineLines 只在这种发现存在时读）。
	seedCombo := func(modelName, summary string, status model.ReviewRunStatus) *model.ReviewRun {
		run := e.seedRun(t, repo, 80, "sha-b", status, now)
		result := fmt.Sprintf(
			`{"summary":%q,"findings":[{"level":"P1","file":"a","line":2,"body":%q}],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`,
			summary, summary)
		e.svc.db.Model(run).Updates(map[string]any{
			"model": modelName, "result_json": result, "summary": summary,
		})
		return run
	}
	first := seedCombo("m-a", "s1", model.ReviewRunCompleted)
	seedCombo("m-b", "s2", model.ReviewRunCompleted)
	seedCombo("m-c", "", model.ReviewRunRunning) // 还在跑：代码状态保持 pending
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 80, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-b", RoundNumber: 1,
		CodeStatus: "pending", SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &diffFailClient{GitHubClient: e.fake}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	var ledger model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 80).First(&ledger)
	if !ledger.SyncPending || ledger.CodeCommentID == 0 {
		t.Fatalf("首轮应写出评论并保持 pending：pending=%v comment=%d", ledger.SyncPending, ledger.CodeCommentID)
	}
	if counts := e.fake.CallCounts(); counts["UpdatePullRequestComment"] != 0 {
		t.Fatalf("首轮写评论用 create，update 次数 = %d", counts["UpdatePullRequestComment"])
	}

	// 同一 head 上某个已完成组合的摘要变了：代码状态仍是 pending，正文变了。
	e.svc.db.Model(first).Updates(map[string]any{
		"summary":     "s1b",
		"result_json": `{"summary":"s1b","findings":[{"level":"P1","file":"a","line":2,"body":"s1b"}],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`,
	})
	review.syncPendingPRs(now.Add(reviewSyncRetryBackoff + time.Second))

	counts := e.fake.CallCounts()
	if counts["UpdatePullRequestComment"] != 1 {
		t.Fatalf("update 次数 = %d, want 1（正文变了要更新）", counts["UpdatePullRequestComment"])
	}
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 80).First(&ledger)
	if !ledger.SyncPending {
		t.Fatal("diff 仍不可读，应保持 pending")
	}
}

// TestUpsertPRCommentDedupesOwnComment 顶层评论的去重必须认准自己的评论：
// 账本记的 comment id 优先；id 不可用时按「bot + marker 前缀」过滤。用户复制
// 粘贴含 marker 的同一段正文不能被当成自己的评论而跳过更新；正文确实一致的
// 情况下不 PATCH，并把找回的 id 补记回账本。
func TestUpsertPRCommentDedupesOwnComment(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.withT3(nil, nil, nil)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	const marker = reviewCodeCommentMarker
	ledger := &model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 84, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-c", RoundNumber: 1, UpdatedAt: time.Now().UTC(),
	}
	if err := e.svc.db.Create(ledger).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	// 用户先贴了一段带 marker 的正文（本轮正文），bot 评论仍是旧的。
	e.fake.AddIssueComment(repo, 84, "someone", marker+"\n本轮正文", time.Now().UTC())
	botID, err := e.fake.CreatePullRequestComment(ctx, 1, repo, 84, marker+"\n旧正文")
	if err != nil {
		t.Fatalf("seed bot comment: %v", err)
	}
	if err := review.upsertPRComment(ctx, e.fake, ledger, marker, marker+"\n本轮正文", "code_comment_id", botID); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := e.fake.CallCounts(); got["UpdatePullRequestComment"] != 1 {
		t.Fatalf("update = %d, want 1（bot 评论正文是旧的，必须更新）", got["UpdatePullRequestComment"])
	}

	// 正文一致 → 不 PATCH；id 不可用时按 bot+marker 找回并补记回账本。
	ledger.CodeCommentID = 0
	countsBefore := e.fake.CallCounts()["UpdatePullRequestComment"]
	if err := review.upsertPRComment(ctx, e.fake, ledger, marker, marker+"\n本轮正文", "code_comment_id", 0); err != nil {
		t.Fatalf("upsert dedupe: %v", err)
	}
	if got := e.fake.CallCounts()["UpdatePullRequestComment"]; got != countsBefore {
		t.Fatalf("正文一致不应再 PATCH：update %d → %d", countsBefore, got)
	}
	var after model.ReviewPullRequest
	if err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 84).First(&after).Error; err != nil {
		t.Fatalf("reload ledger: %v", err)
	}
	if after.CodeCommentID == 0 {
		t.Fatal("找回自己的评论后应把 id 补记回账本")
	}
}

// prErrClient 让 GetPullRequest 固定失败：同步在第一步就失败，用来看退避。
// 自己记调用次数（不走 Fake，错误路径不会留下 Fake 的调用记录）。
type prErrClient struct {
	githubapi.GitHubClient
	calls int
}

func (c *prErrClient) GetPullRequest(context.Context, int64, string, int) (*githubapi.PullRequest, error) {
	c.calls++
	return nil, errors.New("上游 502")
}

// TestReviewSyncFailureBacksOff 持续性失败不再每 10s 重放整条同步：同一 epoch
// 失败后等 reviewSyncRetryBackoff 再试；新事件推进 epoch 立即重新入队。
func TestReviewSyncFailureBacksOff(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.enableReview(t)
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 90, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-f", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	client := &prErrClient{GitHubClient: e.fake}
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: client},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)
	calls := client.calls
	var ledger model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 90).First(&ledger)
	if ledger.SyncFailedEpoch != 1 || !ledger.SyncPending {
		t.Fatalf("失败后应记 sync_failed_epoch=1 且保持 pending：%+v", ledger)
	}
	if calls != 1 {
		t.Fatalf("首次失败调用 = %d, want 1", calls)
	}

	// 退避窗口内不重试。
	review.syncPendingPRs(now.Add(10 * time.Second))
	if got := client.calls; got != calls {
		t.Fatalf("退避窗口内不应重试：%d → %d", calls, got)
	}
	// 窗口过后重试。
	review.syncPendingPRs(now.Add(reviewSyncRetryBackoff + time.Second))
	if got := client.calls; got != calls+1 {
		t.Fatalf("退避过后应重试：%d → %d", calls+1, got)
	}
	// 新事件推进 epoch：立即重新入队，不等退避。
	if err := e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", e.project.ID, repo, 90).
		Update("sync_epoch", gorm.Expr("sync_epoch + 1")).Error; err != nil {
		t.Fatalf("bump epoch: %v", err)
	}
	syncCalls := client.calls
	review.syncPendingPRs(now.Add(2 * time.Second))
	if got := client.calls; got != syncCalls+1 {
		t.Fatalf("epoch 推进后应立即重试：%d → %d", syncCalls+1, got)
	}
}

// TestReviewSyncCarriesVisualSuccessStatusOnce 沿用上一轮视觉通过时，成功状态
// 只在账本还不是 success 时写一次：diff 持续失败的每次重试不再重复发同一个
// commit status（GitHub 对同一 SHA+context 有 1000 条上限）。
func TestReviewSyncCarriesVisualSuccessStatusOnce(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 91, &githubapi.PullRequest{
		RepoFullName: repo, Number: 91, HeadSHA: "sha-w", State: "open",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.enableReview(t)
	// 上一轮（round 1）视觉通过；本轮（round 2）代码有带行号的 P1、没有视觉
	// 运行 → 沿用上一轮通过，同时触发 diff 读取。
	prior := e.seedRun(t, repo, 91, "sha-old", model.ReviewRunCompleted, now)
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
	})
	e.seedRunWithFindings(t, repo, 91, "sha-w", now)
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 91, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-w", RoundNumber: 2,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &diffFailClient{GitHubClient: e.fake}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)
	if got := len(e.fake.StatusesForContext(reviewVisualStatusContext)); got != 1 {
		t.Fatalf("沿用上一轮通过应写 1 条视觉状态，实际 %d", got)
	}
	review.syncPendingPRs(now.Add(reviewSyncRetryBackoff + time.Second))
	if got := len(e.fake.StatusesForContext(reviewVisualStatusContext)); got != 1 {
		t.Fatalf("重试不应重复写视觉状态，实际 %d 条", got)
	}
}

// commentFailOnce 让顶层评论创建失败一次：模拟「视觉状态已写成功、写评论
// 失败」的中间态，用来验证退避重试不重复追加 commit status。
type commentFailOnce struct {
	githubapi.GitHubClient
	failCreate bool
}

func (c *commentFailOnce) CreatePullRequestComment(ctx context.Context, installationID int64, repo string, number int, body string) (int64, error) {
	if c.failCreate {
		c.failCreate = false
		return 0, errors.New("评论接口 403")
	}
	return c.GitHubClient.CreatePullRequestComment(ctx, installationID, repo, number, body)
}

// TestReviewSyncRetryKeepsVisualStatusSingle 视觉状态写成功、随后写评论失败：
// 退避重试不再追加第二条相同 context 的 status（同一 SHA 有 1000 条上限），
// 评论补写成功后收尾。
func TestReviewSyncRetryKeepsVisualStatusSingle(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	body := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, 92, &githubapi.PullRequest{
		RepoFullName: repo, Number: 92, HeadSHA: "sha-cf2", Body: body,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-vf2")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// 上一轮视觉发现 blocker；本轮代码干净、UI 有改动、截图集合没变 → 沿用失败。
	prior := e.seedRun(t, repo, 92, "sha-prev2", model.ReviewRunCompleted, now.Add(-time.Hour))
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"有阻断","findings":[{"severity":"blocker","screenshot":"界面","dimension":"x","body":"b","fix":"f"}],"prior":[],"unmeasurable":[]}`,
	})
	run := e.seedRun(t, repo, 92, "sha-cf2", model.ReviewRunCompleted, now)
	e.svc.db.Model(run).Updates(map[string]any{
		"round_number": 2, "summary": "干净",
		"result_json": `{"summary":"干净","findings":[],"prior":[],"ui_changed":true,"ui_changed_since_last_visual":false}`,
	})
	fp := screenshotFingerprint(collectScreenshotURLs(body))
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 92, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-cf2", RoundNumber: 2,
		VisualScreensFingerprint: fp, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	client := &commentFailOnce{GitHubClient: e.fake, failCreate: true}
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: client},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)
	if got := len(e.fake.StatusesForContext(reviewVisualStatusContext)); got != 1 {
		t.Fatalf("首次应写 1 条视觉状态，实际 %d", got)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 92).First(&ledger)
	if !ledger.SyncPending {
		t.Fatal("评论写失败应保持 pending")
	}

	review.syncPendingPRs(now.Add(reviewSyncRetryBackoff + time.Second))
	if got := len(e.fake.StatusesForContext(reviewVisualStatusContext)); got != 1 {
		t.Fatalf("退避重试不应追加视觉状态，实际 %d 条", got)
	}
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 92).First(&ledger)
	if ledger.SyncPending {
		t.Fatal("评论补写成功后应收尾")
	}
}

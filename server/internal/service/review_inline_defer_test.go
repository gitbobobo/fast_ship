package service

import (
	"context"
	"errors"
	"fmt"
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

// diffFailClient 只让 diff 读取失败：验证「单次 diff 读取失败只影响行内
// review」的软失败口径，其余 GitHub 面照常走 Fake。
type diffFailClient struct {
	githubapi.GitHubClient
}

func (c *diffFailClient) GetPullRequestDiff(context.Context, int64, string, int) (string, error) {
	return "", errors.New("上游 502")
}

// TestReviewDiffFailureDefersCommentAndInline diff 读取失败（暂时态）时先不发
// 代码顶层评论（INT-233：不留「没读 diff」口径的半截评论）：commit status
// 照常写出，行内 review 推迟，运行保持 posted_at 为空、账本保持 pending 并把
// 原因写进 sync_error；退避重试仍读不到时依旧零评论；diff 恢复后才发一次
// 完整口径的评论并补发行内 review。
func TestReviewDiffFailureDefersCommentAndInline(t *testing.T) {
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
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 78, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-d", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &diffFailClient{GitHubClient: e.fake}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, repository.NewForgeIntegrationRepository(e.svc.db), e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	if got := len(e.fake.StatusesForContext(reviewCodeStatusContext)); got == 0 {
		t.Fatal("diff 读取失败不应阻断代码 commit status 写入")
	}
	for _, body := range e.fake.Comments(repo, 78) {
		if strings.Contains(body, reviewCodeCommentMarker) {
			t.Fatalf("diff 读不到不应发顶层评论，发出了: %s", body)
		}
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

	// 退避重试仍读不到 diff：依旧零评论、零行内。
	review.syncPendingPRs(now.Add(reviewSyncRetryBackoff + time.Second))
	if counts := e.fake.CallCounts(); counts["CreatePullRequestComment"] != 0 {
		t.Fatalf("重试仍读不到 diff 不应发评论，create=%d", counts["CreatePullRequestComment"])
	}

	// diff 恢复：评论只发一次（完整口径），行内 review 补发，账本收尾。
	e.review.syncPendingPRs(now.Add(2 * reviewSyncRetryBackoff))
	if counts := e.fake.CallCounts(); counts["CreatePullRequestComment"] != 1 {
		t.Fatalf("diff 恢复后应发 1 条顶层评论，create=%d", counts["CreatePullRequestComment"])
	}
	var commentBody string
	for _, body := range e.fake.Comments(repo, 78) {
		if strings.Contains(body, reviewCodeCommentMarker) {
			commentBody = body
		}
	}
	if commentBody == "" || !strings.Contains(commentBody, "<!-- fast-ship-round:sha-d:1 -->") {
		t.Fatalf("顶层评论应带本轮轮次标记: %s", commentBody)
	}
	if got := len(e.fake.ReviewEvents(repo)); got != 1 {
		t.Fatalf("diff 恢复后应补发 1 条行内 review，实际 %d", got)
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
	if ledger.CodeCommentID == 0 {
		t.Fatal("评论发出后账本应记 code_comment_id")
	}
}

// TestReviewDiffFailureWithoutPendingInlineKeepsSyncPending 行内运行都已发布
// （posted_at 非空）时 diff 仍读不到：顶层评论同样先不发（INT-233），错误
// 照样上报、账本保持 pending；diff 恢复后评论补发一次即收尾，不重复发。
func TestReviewDiffFailureWithoutPendingInlineKeepsSyncPending(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 79, &githubapi.PullRequest{
		RepoFullName: repo, Number: 79, HeadSHA: "sha-n", State: "open",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.fake.SetDiff("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,1 +1,2 @@\n a\n+b\n")
	run := e.seedRunWithFindings(t, repo, 79, "sha-n", now)
	e.svc.db.Model(run).Updates(map[string]any{"posted_at": now, "review_id": 7})
	e.enableReview(t)
	e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 79, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-n", RoundNumber: 1,
		CodeStatus: "failure", SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &diffFailClient{GitHubClient: e.fake}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, repository.NewForgeIntegrationRepository(e.svc.db), e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	var ledger model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 79).First(&ledger)
	if !ledger.SyncPending || !strings.Contains(ledger.SyncError, "读取 diff 失败") {
		t.Fatalf("diff 读不到应保持 pending 并记录原因：pending=%v err=%q", ledger.SyncPending, ledger.SyncError)
	}
	if counts := e.fake.CallCounts(); counts["CreatePullRequestComment"] != 0 {
		t.Fatalf("diff 读不到不应发顶层评论，create=%d", counts["CreatePullRequestComment"])
	}

	// diff 恢复：评论补发一次（行内都已发布，不再提交 review），账本收尾。
	e.review.syncPendingPRs(now.Add(reviewSyncRetryBackoff + time.Second))
	if counts := e.fake.CallCounts(); counts["CreatePullRequestComment"] != 1 {
		t.Fatalf("diff 恢复后应补发 1 条顶层评论，create=%d", counts["CreatePullRequestComment"])
	}
	if got := e.fake.ReviewEvents(repo); len(got) != 0 {
		t.Fatalf("行内都已发布不应再提交 review，events = %v", got)
	}
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 79).First(&ledger)
	if ledger.SyncPending || ledger.SyncError != "" || ledger.CodeCommentID == 0 {
		t.Fatalf("应收尾且评论 id 已记：pending=%v err=%q id=%d", ledger.SyncPending, ledger.SyncError, ledger.CodeCommentID)
	}
}

// TestReviewCommentPostedOnceWhenDiffReadable 组合陆续完成期间 diff 持续失败：
// 每个同步 tick 都不发顶层评论（在途组合不发 + diff 失败不发），diff 恢复后
// 只发一次、内容按当轮最新快照一次成文——不存在「先发旧快照再改写」。
func TestReviewCommentPostedOnceWhenDiffReadable(t *testing.T) {
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
	third := seedCombo("m-c", "", model.ReviewRunRunning) // 还在跑：只发 pending 状态
	e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 80, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-b", RoundNumber: 1,
		CodeStatus: "pending", SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &diffFailClient{GitHubClient: e.fake}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, repository.NewForgeIntegrationRepository(e.svc.db), e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	if counts := e.fake.CallCounts(); counts["CreatePullRequestComment"] != 0 {
		t.Fatalf("有在途组合不应发顶层评论，create=%d", counts["CreatePullRequestComment"])
	}

	// 在途组合完成 + 已完成组合的摘要更新（旧实现会在此改写评论）；diff 仍
	// 读不到：依旧不发。
	e.svc.db.Model(third).Updates(map[string]any{
		"status":      model.ReviewRunCompleted,
		"summary":     "s3",
		"result_json": `{"summary":"s3","findings":[{"level":"P1","file":"a","line":2,"body":"s3"}],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`,
	})
	e.svc.db.Model(first).Updates(map[string]any{
		"summary":     "s1b",
		"result_json": `{"summary":"s1b","findings":[{"level":"P1","file":"a","line":2,"body":"s1b"}],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`,
	})
	review.syncPendingPRs(now.Add(reviewSyncRetryBackoff + time.Second))
	if counts := e.fake.CallCounts(); counts["CreatePullRequestComment"] != 0 {
		t.Fatalf("diff 仍读不到不应发评论，create=%d", counts["CreatePullRequestComment"])
	}

	// diff 恢复：评论一次成文，含全部组合（含最后完成的 m-c）与最新摘要，
	// 无任何 PATCH（fake 无 update 通道，重复发帖会表现为 create>1）。
	e.review.syncPendingPRs(now.Add(2 * reviewSyncRetryBackoff))
	counts := e.fake.CallCounts()
	if counts["CreatePullRequestComment"] != 1 {
		t.Fatalf("diff 恢复后应只发 1 条评论，create=%d", counts["CreatePullRequestComment"])
	}
	var body string
	for _, c := range e.fake.Comments(repo, 80) {
		if strings.Contains(c, reviewCodeCommentMarker) {
			body = c
		}
	}
	for _, want := range []string{"s1b", "s2", "s3", "m-c", "<!-- fast-ship-round:sha-b:1 -->"} {
		if !strings.Contains(body, want) {
			t.Fatalf("评论缺 %q: %s", want, body)
		}
	}
	var ledger model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 80).First(&ledger)
	if ledger.SyncPending {
		t.Fatal("全部终态且 diff 可读后应收尾")
	}
}

// TestPostRoundCommentRecoversIDViaPrefix 找回口径（INT-233）：只认「bot 作者
// + marker + 轮次标记」两行前缀——用户粘贴的同文评论不算，旧轮的 bot 评论
// （无本轮轮次标记）也不算；找回命中不新建并把 id 补记回账本，账本 id 已在
// 时直接跳过。
func TestPostRoundCommentRecoversIDViaPrefix(t *testing.T) {
	e := newReviewEngineEnv(t)
	review := e.withT3(nil, nil)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	const marker = reviewCodeCommentMarker
	prefix := marker + "\n<!-- fast-ship-round:sha-c:1 -->"
	ledger := &model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 84, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-c", RoundNumber: 1, UpdatedAt: time.Now().UTC(),
	}
	if err := e.svc.db.Create(ledger).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	// 用户贴的同文评论、旧轮 bot 评论（只有 marker）都不该被认成本轮的。
	e.fake.AddIssueComment(repo, 84, "someone", prefix+"\n本轮正文", time.Now().UTC())
	if _, err := e.fake.CreatePullRequestComment(ctx, 1, repo, 84, marker+"\n旧轮正文"); err != nil {
		t.Fatalf("seed old bot comment: %v", err)
	}
	seeded := e.fake.CallCounts()["CreatePullRequestComment"]
	client := forge.NewGitHubReviewClient(e.fake, ledger.InstallationID)
	if err := review.postRoundComment(ctx, client, ledger, prefix, prefix+"\n本轮正文", "code_comment_id", 0); err != nil {
		t.Fatalf("postRoundComment: %v", err)
	}
	if got := e.fake.CallCounts()["CreatePullRequestComment"]; got != seeded+1 {
		t.Fatalf("create = %d, want %d（只有本轮新建的 bot 评论命中前缀）", got, seeded+1)
	}
	var after model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 84).First(&after)
	if after.CodeCommentID == 0 {
		t.Fatal("新建后应把 id 写回账本")
	}

	// 账本 id 已在：直接跳过，不再找回也不新建。
	idAfterCreate := after.CodeCommentID
	if err := review.postRoundComment(ctx, client, ledger, prefix, prefix+"\n本轮正文", "code_comment_id", idAfterCreate); err != nil {
		t.Fatalf("重试: %v", err)
	}
	if got := e.fake.CallCounts()["CreatePullRequestComment"]; got != seeded+1 {
		t.Fatalf("同轮重试不得重复新建, create=%d", got)
	}
	if got := e.fake.CallCounts()["FindPullRequestComment"]; got != 1 {
		t.Fatalf("id 已在时不应再找回, find=%d", got)
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
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 90, UserID: e.user.ID,
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
		nil, e.media, repository.NewForgeIntegrationRepository(e.svc.db), e.svc.cfg, zap.NewNop())
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
	body := "![界面](https://uiv.frp.musiver.cn/f/x.png)"
	e.fake.SetPR(repo, 91, &githubapi.PullRequest{
		RepoFullName: repo, Number: 91, HeadSHA: "sha-w", State: "open", Body: body,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	visualCombos, err := marshalCombinations([]api.ReviewCombination{testCombo("env-cs")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	e.enableReview(t, func(s *model.ProjectReviewSettings) {
		s.VisualCombinationsJSON = visualCombos
	})
	// 上一轮（round 1）视觉通过；本轮（round 2）截图集合未变（账本指纹与
	// 描述同集合）、上轮视觉后无 UI 改动 → 沿用上一轮通过；代码有带行号的
	// P1，同步同时触发 diff 读取。描述里没有截图时按新决策不沿用（visualNone）。
	prior := e.seedRun(t, repo, 91, "sha-old", model.ReviewRunCompleted, now)
	e.svc.db.Model(prior).Updates(map[string]any{
		"kind": model.ReviewKindVisual, "round_number": 1,
		"result_json": `{"summary":"通过","findings":[],"prior":[],"unmeasurable":[]}`,
	})
	e.seedRunWithFindings(t, repo, 91, "sha-w", now)
	fp := screenshotFingerprint(collectScreenshotURLs(body))
	e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 91, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-w", RoundNumber: 2,
		VisualScreensFingerprint: fp, SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &diffFailClient{GitHubClient: e.fake}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, repository.NewForgeIntegrationRepository(e.svc.db), e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)
	if got := len(e.fake.StatusesForContext(reviewVisualStatusContext)); got != 1 {
		t.Fatalf("沿用上一轮通过应写 1 条视觉状态，实际 %d", got)
	}
	review.syncPendingPRs(now.Add(reviewSyncRetryBackoff + time.Second))
	if got := len(e.fake.StatusesForContext(reviewVisualStatusContext)); got != 1 {
		t.Fatalf("重试不应重复写视觉状态，实际 %d 条", got)
	}
	// 沿用评论每轮新发（INT-233），且上一轮干净时不得宣称「意见尚未处理」。
	visualComment := ""
	for _, c := range e.fake.Comments(repo, 91) {
		if strings.Contains(c, reviewVisualCommentMarker) {
			visualComment = c
		}
	}
	if visualComment == "" {
		t.Fatal("沿用通过应新发一条视觉评论留痕")
	}
	if !strings.Contains(visualComment, "上一轮审查通过") || strings.Contains(visualComment, "意见尚未处理") {
		t.Fatalf("沿用通过的文案 = %q, want 「上一轮审查通过」", visualComment)
	}
	if !strings.Contains(visualComment, "<!-- fast-ship-round:sha-w:2:carry -->") {
		t.Fatalf("沿用评论应带 :carry 轮次标记: %s", visualComment)
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
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 92, UserID: e.user.ID,
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
		nil, e.media, repository.NewForgeIntegrationRepository(e.svc.db), e.svc.cfg, zap.NewNop())
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

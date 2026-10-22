package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// INT-152：GitHub 对「PR diff 超出上限」的拒绝（4xx + too_large，实测 406）是确定性
// 失败，退避重试永远撞同一堵墙。这两组用例钉住降级收尾的完整口径：产出照常
// 写、意见不丢、posted_at 留空留给自愈、账本终态化；以及 406 但没有结构化
// too_large 时不得误降级（那会把暂时性失败当成终态，行内意见被静默丢掉）。

// diffTooLargeClient 只让 diff 读取返回 GitHub 的超限拒绝；tooLarge 切回 false
// 后透传 Fake，用来验证同一 head 的自愈。自己记 diff 调用次数：降级收尾后
// 账本已清 pending，退避窗口内外的重试都不该再碰 diff。
type diffTooLargeClient struct {
	githubapi.GitHubClient
	tooLarge  bool
	diffCalls int
}

func (c *diffTooLargeClient) GetPullRequestDiff(ctx context.Context, installationID int64, repoFullName string, number int) (string, error) {
	c.diffCalls++
	if c.tooLarge {
		// 真实形态的超限响应体（GitHub 对 pulls/{n}/diff 的拒绝口径）。
		return "", &githubapi.StatusError{StatusCode: 406, Method: "GET",
			URL:  "/repos/o/r/pulls/78",
			Body: `{"message":"Sorry, the diff exceeded the maximum number of lines (20000)","errors":[{"resource":"PullRequest","field":"diff","code":"too_large","message":""}]}`}
	}
	return c.GitHubClient.GetPullRequestDiff(ctx, installationID, repoFullName, number)
}

// TestReviewSyncDiffTooLargeDowngradesThenSelfHeals 确定性超限的降级收尾：
// commit status 与顶层评论照常写，评论注明「GitHub 拒绝读取 PR diff」且 P0–P2
// 仍完整列出（不静默丢）；不发任何行内 review，运行 posted_at 保持 nil——
// 同一 head 的下一次同步事件 diff 读得到时补发行内 review、评论去掉降级提示
// （自愈而非永久终态，所以既不写 posted_at 也不留降级标记）。降级后账本清
// pending 与 sync_error，退避窗口内外都不再重试同一 head。
func TestReviewSyncDiffTooLargeDowngradesThenSelfHeals(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 78, &githubapi.PullRequest{
		RepoFullName: repo, Number: 78, HeadSHA: "sha-tl", State: "open",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	client := &diffTooLargeClient{GitHubClient: e.fake, tooLarge: true}
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: client},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())
	// 带行号 P1 才会触发 diff 读取（fetchInlineLines 只在这种发现存在时读）。
	run := e.seedRunWithFindings(t, repo, 78, "sha-tl", now)
	e.enableReview(t)
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 78, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-tl", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review.syncPendingPRs(now)

	if got := len(e.fake.StatusesForContext(reviewCodeStatusContext)); got == 0 {
		t.Fatal("diff 超限降级不应阻断代码 commit status 写入")
	}
	codeComment := ""
	for _, body := range e.fake.Comments(repo, 78) {
		if strings.Contains(body, reviewCodeCommentMarker) {
			codeComment = body
		}
	}
	if codeComment == "" {
		t.Fatal("diff 超限降级不应阻断顶层评论写入")
	}
	// 固定文案注明原因；意见仍在评论里，作者看得到全部 P0–P2。
	const tooLargeNotice = "本轮 GitHub 拒绝读取 PR diff（超出上限），未发行内 review：以下问题仍完整列出。"
	if !strings.Contains(codeComment, tooLargeNotice) {
		t.Fatalf("顶层评论应含降级提示，实际：%s", codeComment)
	}
	// 断言到具体发现（seed 的 P1 正文是「问题」），不能只匹配泛化字样：
	// 「未发现 P0–P2 问题」这类结论句也会含「问题」。
	if !strings.Contains(codeComment, "**P1**") || !strings.Contains(codeComment, "问题") {
		t.Fatalf("降级时 P0–P2 发现仍应完整列在顶层评论里，不能静默丢：%s", codeComment)
	}
	// 本轮没读 diff 就无从判断行号是否有效，不臆断加「未行内标注」注记。
	if strings.Contains(codeComment, "未行内标注") {
		t.Fatal("本轮没读 diff，顶层评论不应加「未行内标注」注记")
	}
	if got := e.fake.ReviewEvents(repo); len(got) != 0 {
		t.Fatalf("降级收尾不应提交行内 review，events = %v", got)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	// posted_at 留空是自愈的前提：一旦写上，下一次同步事件就不再补发行内意见。
	if after.PostedAt != nil {
		t.Fatal("降级收尾不应把运行标记为已发布（会关闭自愈通道）")
	}
	var ledger model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 78).First(&ledger)
	// 确定性失败的终态化：不再挂 sync_pending/sync_error（重试不可能成功）。
	if ledger.SyncPending || ledger.SyncError != "" {
		t.Fatalf("降级应收尾：pending=%v sync_error=%q", ledger.SyncPending, ledger.SyncError)
	}

	// 退避窗口内外都不再重试：降级已终态化，账本不再入队（对比暂时态失败的
	// 每 5 分钟退避重试——那是 INT-152 要消除的空转）。
	for _, at := range []time.Time{now.Add(10 * time.Second), now.Add(reviewSyncRetryBackoff + time.Second)} {
		review.syncPendingPRs(at)
	}
	if client.diffCalls != 1 {
		t.Fatalf("降级后 diff 调用 = %d, want 1（终态不再重试）", client.diffCalls)
	}
	if got := e.fake.ReviewEvents(repo); len(got) != 0 {
		t.Fatalf("降级后不应出现行内 review，events = %v", got)
	}

	// 自愈：基线分支回退/PR 拆分后 diff 又读得到。推进一次同步事件（置回
	// pending 并自增 epoch，与 webhook/rerun 的入队口径一致），行内 review
	// 补发、评论更新为无降级提示的正文。
	client.tooLarge = false
	e.fake.SetDiff("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,1 +1,2 @@\n a\n+b\n")
	if err := e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", e.project.ID, repo, 78).
		Updates(map[string]any{
			"sync_pending": true,
			"sync_epoch":   gorm.Expr("sync_epoch + 1"),
			"updated_at":   now.Add(time.Minute),
		}).Error; err != nil {
		t.Fatalf("重新入队: %v", err)
	}
	review.syncPendingPRs(now.Add(time.Minute))
	if got := len(e.fake.ReviewEvents(repo)); got != 1 {
		t.Fatalf("diff 可读后应补发 1 条行内 review，实际 %d", got)
	}
	for _, body := range e.fake.Comments(repo, 78) {
		if strings.Contains(body, reviewCodeCommentMarker) {
			codeComment = body
		}
	}
	if strings.Contains(codeComment, tooLargeNotice) {
		t.Fatal("diff 可读后评论正文不应再含降级提示")
	}
	if strings.Contains(codeComment, "未行内标注") {
		t.Fatal("行号 2 在 diff 右侧有效行内，不应加「未行内标注」注记")
	}
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.PostedAt == nil {
		t.Fatal("行内 review 补发后应标记 posted_at")
	}
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 78).First(&ledger)
	if ledger.SyncPending || ledger.SyncError != "" {
		t.Fatalf("自愈后应收尾：pending=%v sync_error=%q", ledger.SyncPending, ledger.SyncError)
	}
}

// diff406NoCodeClient 同样返回 406，但响应体没有结构化 too_large：按口径这不
// 是「diff 超限」，必须沿用暂时态行为（推迟 + 退避），不能凭状态码误降级成
// 终态——那会把别的原因（代理/网关 406 等）当成不可重试，行内意见静默丢掉。
type diff406NoCodeClient struct {
	githubapi.GitHubClient
}

func (c *diff406NoCodeClient) GetPullRequestDiff(context.Context, int64, string, int) (string, error) {
	return "", &githubapi.StatusError{StatusCode: 406, Method: "GET", URL: "/repos/o/r/pulls/81",
		Body: `{"message":"Unacceptable"}`}
}

// TestReviewSyncDiff406WithoutTooLargeStaysRetryable 406 但没有 too_large 仍按
// 可重试处理：commit status 与顶层评论照常写（与暂时态口径一致），账本保持
// sync_pending 并写「读取 diff 失败」，posted_at 保持 nil、无行内 review。
func TestReviewSyncDiff406WithoutTooLargeStaysRetryable(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 81, &githubapi.PullRequest{
		RepoFullName: repo, Number: 81, HeadSHA: "sha-406", State: "open",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	run := e.seedRunWithFindings(t, repo, 81, "sha-406", now)
	e.enableReview(t)
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 81, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-406", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &diff406NoCodeClient{GitHubClient: e.fake}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	if got := len(e.fake.StatusesForContext(reviewCodeStatusContext)); got == 0 {
		t.Fatal("暂时态 diff 失败不应阻断代码 commit status 写入")
	}
	commentBody := ""
	for _, body := range e.fake.Comments(repo, 81) {
		if strings.Contains(body, reviewCodeCommentMarker) {
			commentBody = body
		}
	}
	if commentBody == "" {
		t.Fatal("暂时态 diff 失败不应阻断顶层评论写入")
	}
	if strings.Contains(commentBody, "本轮 GitHub 拒绝读取 PR diff") {
		t.Fatal("没有结构化 too_large 不应按降级收尾渲染提示")
	}
	if got := e.fake.ReviewEvents(repo); len(got) != 0 {
		t.Fatalf("暂时态失败不应提交行内 review，events = %v", got)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.PostedAt != nil {
		t.Fatal("暂时态失败应保持 posted_at 为空（下一轮重试）")
	}
	var ledger model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 81).First(&ledger)
	if !ledger.SyncPending {
		t.Fatal("非确定性失败应保持 sync_pending，让下一轮同步重试")
	}
	if !strings.Contains(ledger.SyncError, "读取 diff 失败") {
		t.Fatalf("sync_error = %q, want 含「读取 diff 失败」", ledger.SyncError)
	}
}

// TestReviewSyncDiffTooLargeKeepsAllFindingsAndClearsStaleFailure 降级不能
// 「只保住一条发现」：多组合、P0/P1/P2 各自不同的正文必须全部留在顶层评论里；
// 已发布的运行不得被回退成未发布，未发布的运行保持 posted_at 为空（等自愈）。
// 降级是成功收尾：历史退避留下的 sync_error / sync_failed_epoch 必须一并清掉，
// 同一 head 的重复超限不再重放 diff、也不重复 PATCH 同一份正文。
func TestReviewSyncDiffTooLargeKeepsAllFindingsAndClearsStaleFailure(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 82, &githubapi.PullRequest{
		RepoFullName: repo, Number: 82, HeadSHA: "sha-mix", State: "open",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.enableReview(t)
	client := &diffTooLargeClient{GitHubClient: e.fake, tooLarge: true}
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: client},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())

	// 组合 A：已发行内 review（posted_at 非空），降级不得把它的终态改回去。
	postedAt := now.Add(-time.Hour)
	posted := e.seedRun(t, repo, 82, "sha-mix", model.ReviewRunCompleted, now)
	e.svc.db.Model(posted).Updates(map[string]any{
		"model":       "m-posted",
		"posted_at":   postedAt,
		"review_id":   int64(7),
		"updated_at":  now,
		"result_json": `{"summary":"已发布","findings":[{"level":"P1","file":"a","line":1,"body":"已发布组合的问题"}],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`,
	})
	// 组合 B：未发行内 review，三种级别各自不同的正文都要留在评论里。
	pending := e.seedRun(t, repo, 82, "sha-mix", model.ReviewRunCompleted, now)
	e.svc.db.Model(pending).Updates(map[string]any{
		"model": "m-pending",
		"result_json": `{"summary":"待发布","findings":[` +
			`{"level":"P0","file":"a","line":2,"body":"阻断问题正文"},` +
			`{"level":"P1","file":"a","line":2,"body":"严重问题正文"},` +
			`{"level":"P2","file":"a","line":2,"body":"次要问题正文"}],"prior":[],"ui_changed":false,"ui_changed_since_last_visual":false}`,
	})
	// 历史退避留下的失败状态：updated_at 推过退避窗口，模拟退避后的这一次重试。
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 82, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-mix", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 5, SyncFailedEpoch: 5, SyncError: "上一次同步失败",
		UpdatedAt: now.Add(-reviewSyncRetryBackoff - time.Minute),
	})
	review.syncPendingPRs(now)

	codeComment := ""
	for _, body := range e.fake.Comments(repo, 82) {
		if strings.Contains(body, reviewCodeCommentMarker) {
			codeComment = body
		}
	}
	for _, want := range []string{
		"本轮 GitHub 拒绝读取 PR diff（超出上限），未发行内 review：以下问题仍完整列出。",
		"**P0**", "**P1**", "**P2**",
		"阻断问题正文", "严重问题正文", "次要问题正文", "已发布组合的问题",
	} {
		if !strings.Contains(codeComment, want) {
			t.Fatalf("顶层评论缺少 %q：%s", want, codeComment)
		}
	}
	if got := e.fake.ReviewEvents(repo); len(got) != 0 {
		t.Fatalf("降级收尾不应提交行内 review，events = %v", got)
	}
	var afterPosted, afterPending model.ReviewRun
	e.svc.db.First(&afterPosted, "id = ?", posted.ID)
	e.svc.db.First(&afterPending, "id = ?", pending.ID)
	if afterPosted.PostedAt == nil || !afterPosted.PostedAt.Equal(postedAt) || afterPosted.ReviewID != 7 {
		t.Fatalf("已发布的运行被降级改写了：posted_at=%v review_id=%d", afterPosted.PostedAt, afterPosted.ReviewID)
	}
	if afterPending.PostedAt != nil {
		t.Fatal("未发布的运行应保持 posted_at 为空（留给同一 (head, round) 的自愈）")
	}
	var ledger model.ReviewPullRequest
	loadLedger := func() {
		e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
			e.project.ID, repo, 82).First(&ledger)
	}
	loadLedger()
	if ledger.SyncPending || ledger.SyncError != "" || ledger.SyncFailedEpoch != 0 {
		t.Fatalf("降级应收尾并清掉历史失败：pending=%v sync_error=%q failed_epoch=%d",
			ledger.SyncPending, ledger.SyncError, ledger.SyncFailedEpoch)
	}
	if client.diffCalls != 1 {
		t.Fatalf("diff 调用 = %d, want 1", client.diffCalls)
	}

	// 同一 (head, round) 的下一次同步事件（同轮事件推进 epoch）仍超限：再读一次
	// diff，但正文不变 → 不重复 PATCH；账本照旧收尾。
	if err := e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", e.project.ID, repo, 82).
		Updates(map[string]any{
			"sync_pending": true,
			"sync_epoch":   gorm.Expr("sync_epoch + 1"),
			"updated_at":   now.Add(time.Minute),
		}).Error; err != nil {
		t.Fatalf("重新入队: %v", err)
	}
	updatesBefore := e.fake.CallCounts()["UpdatePullRequestComment"]
	review.syncPendingPRs(now.Add(time.Minute))
	if client.diffCalls != 2 {
		t.Fatalf("同轮新事件应再读一次 diff，调用 = %d, want 2", client.diffCalls)
	}
	if got := e.fake.CallCounts()["UpdatePullRequestComment"]; got != updatesBefore {
		t.Fatalf("正文未变不应重复 PATCH：%d → %d", updatesBefore, got)
	}
	loadLedger()
	if ledger.SyncPending || ledger.SyncError != "" {
		t.Fatalf("再次降级后仍应收尾：pending=%v sync_error=%q", ledger.SyncPending, ledger.SyncError)
	}

	// 自愈：diff 又读得到 → 补发未发布运行的行内 review，评论去掉降级提示。
	client.tooLarge = false
	e.fake.SetDiff("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,1 +1,2 @@\n a\n+b\n")
	if err := e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND repo_full_name = ? AND pr_number = ?", e.project.ID, repo, 82).
		Updates(map[string]any{
			"sync_pending": true,
			"sync_epoch":   gorm.Expr("sync_epoch + 1"),
			"updated_at":   now.Add(2 * time.Minute),
		}).Error; err != nil {
		t.Fatalf("重新入队: %v", err)
	}
	review.syncPendingPRs(now.Add(2 * time.Minute))
	if got := len(e.fake.ReviewEvents(repo)); got != 1 {
		t.Fatalf("diff 可读后应只补发未发布组合的 1 条行内 review，实际 %d", got)
	}
	e.svc.db.First(&afterPending, "id = ?", pending.ID)
	if afterPending.PostedAt == nil {
		t.Fatal("补发行内 review 后应标记 posted_at")
	}
	for _, body := range e.fake.Comments(repo, 82) {
		if strings.Contains(body, reviewCodeCommentMarker) {
			codeComment = body
		}
	}
	if strings.Contains(codeComment, "本轮 GitHub 拒绝读取 PR diff") {
		t.Fatal("diff 可读后评论正文不应再含降级提示")
	}
	if !strings.Contains(codeComment, "阻断问题正文") || !strings.Contains(codeComment, "已发布组合的问题") {
		t.Fatal("恢复后评论应仍完整保留全部组合的发现")
	}
}

// epochBumpTooLargeClient 在读 diff 时推进账本 epoch，再返回超限拒绝：模拟
// 「最慢的一次调用期间新事件抢先进来」。降级结论不得按旧 epoch 清掉新 pending。
type epochBumpTooLargeClient struct {
	githubapi.GitHubClient
	db   *gorm.DB
	repo string
	pr   int
	done bool
}

func (c *epochBumpTooLargeClient) GetPullRequestDiff(ctx context.Context, inst int64, repo string, number int) (string, error) {
	if !c.done {
		c.done = true
		if err := c.db.Model(&model.ReviewPullRequest{}).
			Where("repo_full_name = ? AND pr_number = ?", c.repo, c.pr).
			Update("sync_epoch", gorm.Expr("sync_epoch + 1")).Error; err != nil {
			return "", err
		}
	}
	return "", &githubapi.StatusError{StatusCode: 406, Method: "GET", URL: "/repos/o/r/pulls/83",
		Body: `{"message":"Sorry, the diff exceeded the maximum number of lines (20000)","errors":[{"resource":"PullRequest","field":"diff","code":"too_large","message":""}]}`}
}

// TestReviewSyncDiffTooLargeDoesNotClearNewPending 读 diff 期间 epoch 被推进：
// 旧快照的降级结论不得清掉新事件的 pending（收尾按进入时的 epoch 条件写）。
func TestReviewSyncDiffTooLargeDoesNotClearNewPending(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 83, &githubapi.PullRequest{
		RepoFullName: repo, Number: 83, HeadSHA: "sha-epoch", State: "open",
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.enableReview(t)
	e.seedRunWithFindings(t, repo, 83, "sha-epoch", now)
	e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 83, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-epoch", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	})
	review := NewReviewService(e.svc.db, e.svc.projectRepo,
		repository.NewGitHubWebhookDeliveryRepository(e.svc.db),
		NewNotificationSink(e.svc.db),
		githubapp.StaticClients{Client: &epochBumpTooLargeClient{GitHubClient: e.fake, db: e.svc.db, repo: repo, pr: 83}},
		fakeReviewAppSource{ok: true},
		fakeReviewPublicURL("https://fs.example.com"),
		nil, e.media, nil, e.svc.cfg, zap.NewNop())
	review.syncPendingPRs(now)

	var ledger model.ReviewPullRequest
	e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ?",
		e.project.ID, repo, 83).First(&ledger)
	if ledger.SyncEpoch != 2 || !ledger.SyncPending {
		t.Fatalf("新事件推进的 pending 不能被旧 epoch 清掉：epoch=%d pending=%v",
			ledger.SyncEpoch, ledger.SyncPending)
	}
	if got := e.fake.ReviewEvents(repo); len(got) != 0 {
		t.Fatalf("降级不应提交行内 review，events = %v", got)
	}
}

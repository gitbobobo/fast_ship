package service

// INT-147 第二部分：已关闭/合并 PR 的兜底关卡——防抖到期不开轮、派发不建
// 线程、同步零 GitHub 写；rerun 拒跑、/result 按作废处理。重投/迟到的
// webhook 只重放原始 payload（PR 仍写着 open），三道关卡都以 GetPullRequest
// 的实时 State 为准，靠 githubapp.Fake 的 SetPR(State/Merged) 模拟实况。

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
)

// seedClosedDebounce 落一行到期防抖的公共助手。
func seedClosedDebounce(t *testing.T, e *reviewEngineEnv, repo string, pr int, head string, now time.Time) {
	t.Helper()
	if err := e.svc.db.Create(&model.ReviewDebounce{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: pr,
		HeadSHA: head, Action: "synchronize", InstallationID: 42,
		PRTitle: "t", PRURL: "u", BaseRef: "main", HeadRef: "feature",
		FireAt: now.Add(-time.Second), UpdatedAt: now.Add(-time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed debounce: %v", err)
	}
}

// TestReviewClosedDebounceSkipsRound 防抖到期 + PR 已合并：删行不开轮；
// 远端 head 与防抖行不一致同样删行不开轮；PR open 且 head 一致才开轮。
func TestReviewClosedDebounceSkipsRound(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)

	// 已合并：防抖行（closed 投递丢失/未到的残留）到期后不开轮。
	e.fake.SetPR(repo, 51, &githubapi.PullRequest{
		RepoFullName: repo, Number: 51, State: "closed", Merged: true,
		HeadSHA: "sha-c51", Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	seedClosedDebounce(t, e, repo, 51, "sha-c51", now)

	// PR 仍 open 但远端 head 已前进：防抖行记的是旧 head，不开轮。
	e.fake.SetPR(repo, 52, &githubapi.PullRequest{
		RepoFullName: repo, Number: 52, State: "open",
		HeadSHA: "sha-newer", Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	seedClosedDebounce(t, e, repo, 52, "sha-old", now)

	review.processDueDebounces(now)

	if got := e.debounceCount(t); got != 0 {
		t.Fatalf("已关闭/head 已偏的防抖行应被删，剩 %d", got)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 || len(t3.created) != 0 {
		t.Fatalf("不得开轮/建线程：runs=%d created=%d", runs, len(t3.created))
	}

	// 对照：PR open 且远端 head 与防抖行一致 → 正常开轮。
	e.fake.SetPR(repo, 53, &githubapi.PullRequest{
		RepoFullName: repo, Number: 53, State: "open",
		HeadSHA: "sha-c53", Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	seedClosedDebounce(t, e, repo, 53, "sha-c53", now)
	review.processDueDebounces(now)

	var opened []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = 53", repo).Find(&opened)
	if len(opened) != 1 || opened[0].HeadSHA != "sha-c53" {
		t.Fatalf("open PR 应正常开轮: %+v", opened)
	}
	if got := e.debounceCount(t); got != 0 {
		t.Fatalf("开轮后防抖行应已消费，剩 %d", got)
	}
}

// TestReviewDebounceReadFailureRetries PR 实况读不到（暂时性故障/PR 不可
// 见）：不开轮、不删行，但 fire_at 推后一个防抖窗口——否则这条永远读不到
// 的行会一直占住本 tick 的批量名额，后面的正常防抖行永远轮不到。
func TestReviewDebounceReadFailureRetries(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)

	// 不登记 PR：GetPullRequest 返回错误。
	seedClosedDebounce(t, e, repo, 56, "sha-c56", now)
	review.processDueDebounces(now)

	if got := e.debounceCount(t); got != 1 {
		t.Fatalf("读 PR 失败不应删行，防抖行 = %d，want 1", got)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 || len(t3.created) != 0 {
		t.Fatalf("读 PR 失败不得开轮/建线程：runs=%d created=%d", runs, len(t3.created))
	}
	var row model.ReviewDebounce
	if err := e.svc.db.Where("repo_full_name = ? AND pr_number = 56", repo).First(&row).Error; err != nil {
		t.Fatalf("防抖行: %v", err)
	}
	if !row.FireAt.After(now) {
		t.Fatalf("fire_at = %v，应推后到 now 之后", row.FireAt)
	}

	// 下一次到期前登记 PR 实况：推后的行按新 fire_at 正常开轮。
	e.fake.SetPR(repo, 56, &githubapi.PullRequest{
		RepoFullName: repo, Number: 56, State: "open",
		HeadSHA: "sha-c56", Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	review.processDueDebounces(row.FireAt.Add(time.Second))
	var opened []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = 56", repo).Find(&opened)
	if len(opened) != 1 {
		t.Fatalf("推后的防抖行应正常开轮: %+v", opened)
	}
}

// TestReviewDebounceDeterministicErrorDropsRow 实况读确定性失败（集成已删/
// 密文损坏/401/403/404）直接删行不推后：每轮重试结果相同，留着只会让行
// 永远消费不掉并按周期刷错；后续新事件会重建防抖行。
func TestReviewDebounceDeterministicErrorDropsRow(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)

	// 绑定不存在的 Forgejo 集成：resolver → findForgejoRow 行不存在
	// （ErrRecordNotFound）→ 确定性失败，直接删行不推后。
	if err := e.svc.db.Create(&model.ReviewDebounce{
		IntegrationID: "forge-gone", ProjectID: e.project.ID,
		RepoFullName: repo, PRNumber: 71, HeadSHA: "sha-71", Action: "synchronize",
		PRTitle: "t", PRURL: "u", BaseRef: "main", HeadRef: "f",
		FireAt: now.Add(-time.Second), UpdatedAt: now.Add(-time.Minute),
	}).Error; err != nil {
		t.Fatalf("seed debounce: %v", err)
	}
	review.processDueDebounces(now)

	if got := e.debounceCount(t); got != 0 {
		t.Fatalf("确定性失败的防抖行应删除，剩 %d", got)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 || len(t3.created) != 0 {
		t.Fatalf("确定性失败不得开轮/建线程：runs=%d created=%d", runs, len(t3.created))
	}
}

// TestReviewDebounceRateLimitRetries 限流 403（GitHub 主/二次限流正是回
// 403，响应体含 rate limit）是瞬时失败：不删行、fire_at 推后；同测试里
// 真权限 403（无 rate limit 字样）对照删行。
func TestReviewDebounceRateLimitRetries(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)

	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		model.ForgeIntegrationBuiltinGitHubID: &pollStubClient{errByRepo: map[string]error{
			repo: &forge.StatusError{StatusCode: 403, Body: "API rate limit exceeded for user"},
		}},
	}}
	seedClosedDebounce(t, e, repo, 72, "sha-72", now)
	review.processDueDebounces(now)

	if got := e.debounceCount(t); got != 1 {
		t.Fatalf("限流 403 不应删行，防抖行 = %d，want 1", got)
	}
	var row model.ReviewDebounce
	e.svc.db.Where("repo_full_name = ? AND pr_number = 72", repo).First(&row)
	if !row.FireAt.After(now) {
		t.Fatalf("限流 403 应推后 fire_at: %v", row.FireAt)
	}

	// 对照：无 rate limit 字样的真权限 403 → 确定性，删行。
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		model.ForgeIntegrationBuiltinGitHubID: &pollStubClient{errByRepo: map[string]error{
			repo: &forge.StatusError{StatusCode: 403, Body: "Forbidden"},
		}},
	}}
	review.processDueDebounces(row.FireAt.Add(time.Second))
	if got := e.debounceCount(t); got != 0 {
		t.Fatalf("权限 403 应删行，剩 %d", got)
	}
}

// TestReviewClosedDispatchCancelsRun 派发阶段 PR 已关闭：不建线程，运行
// cancelled + error_code=pr_closed，令牌已消费、总账 sync_pending 置位；
// 读 PR 失败（未登记）不落终态不签令牌，留给认领超窗重试。
func TestReviewClosedDispatchCancelsRun(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-closed-d")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)

	e.fake.SetPR(repo, 54, &githubapi.PullRequest{
		RepoFullName: repo, Number: 54, State: "closed",
		HeadSHA: "sha-c54", Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 54, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-c54", RoundNumber: 1, UpdatedAt: now,
	})

	// 排队期间 PR 被关：提升 → 派发认领 → 建线程前拦下，且令牌从未签发
	// （认领只覆盖无令牌运行，签发后才拦就会把它留成孤儿）。
	queued := e.seedRun(t, repo, 54, "sha-c54", model.ReviewRunQueued, now)
	e.svc.db.Model(queued).Update("environment_id", env.ID)

	review.promoteQueuedRuns(now)
	var mid model.ReviewRun
	e.svc.db.First(&mid, "id = ?", queued.ID)
	if mid.Status != model.ReviewRunDispatched {
		t.Fatalf("queued 应已提升为 dispatched, got %q", mid.Status)
	}
	review.dispatchRuns(now)

	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", queued.ID)
	if after.Status != model.ReviewRunCancelled || after.ErrorCode != "pr_closed" || after.FinishedAt == nil {
		t.Fatalf("run = %q/%q, want cancelled/pr_closed", after.Status, after.ErrorCode)
	}
	if len(t3.created) != 0 {
		t.Fatalf("已关闭 PR 不得建线程: %+v", t3.created)
	}
	var tokCount int64
	e.svc.db.Model(&model.ReviewRunToken{}).Where("run_id = ?", queued.ID).Count(&tokCount)
	if tokCount != 0 {
		t.Fatal("已关闭 PR 不得签发令牌")
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 54", repo)
	if !ledger.SyncPending {
		t.Fatal("sync_pending 应置位（交给同步阶段收尾 GitHub 产出）")
	}

	// 已带令牌的 dispatched 运行直接进 launchRun：同样终态，令牌一并消费。
	disp := e.seedRun(t, repo, 54, "sha-c54", model.ReviewRunDispatched, now)
	e.svc.db.Model(disp).Update("environment_id", env.ID)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: disp.ID, TokenHash: "h-c54", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})
	review.launchRun(context.Background(), disp.ID, now)

	var afterDisp model.ReviewRun
	e.svc.db.First(&afterDisp, "id = ?", disp.ID)
	if afterDisp.Status != model.ReviewRunCancelled || afterDisp.ErrorCode != "pr_closed" {
		t.Fatalf("run = %q/%q, want cancelled/pr_closed", afterDisp.Status, afterDisp.ErrorCode)
	}
	var token model.ReviewRunToken
	e.svc.db.First(&token, "run_id = ?", disp.ID)
	if token.ConsumedAt == nil {
		t.Fatal("令牌应已消费")
	}
	if len(t3.created) != 0 {
		t.Fatal("已关闭 PR 不得建线程")
	}

	// 读 PR 失败（fake 未登记、非确定性错误）：不落终态也不签令牌——
	// 认领超窗后重试。
	noPR := e.seedRun(t, repo, 55, "sha-c55", model.ReviewRunDispatched, now)
	e.svc.db.Model(noPR).Update("environment_id", env.ID)
	review.launchRun(context.Background(), noPR.ID, now)
	var kept model.ReviewRun
	e.svc.db.First(&kept, "id = ?", noPR.ID)
	if kept.Status != model.ReviewRunDispatched || kept.FinishedAt != nil {
		t.Fatalf("读 PR 失败应保持 dispatched 等重试: %q", kept.Status)
	}
	tokCount = 0
	e.svc.db.Model(&model.ReviewRunToken{}).Where("run_id = ?", noPR.ID).Count(&tokCount)
	if tokCount != 0 {
		t.Fatal("读 PR 失败不得签发令牌")
	}
	if len(t3.created) != 0 {
		t.Fatal("读 PR 失败不得建线程")
	}
}

// TestReviewDispatchAccessErrTerminals 派发前实况读的确定性失败不再等认领
// 超窗空转到 timeout：PR 404 且仓库可读按 PR 关闭终局 cancelled/pr_closed
// （GitHub/Forgejo 对无权限资源也回 404，用仓库级复核消歧）；权限 403 与
// PR+仓库双 404（凭证吊销/仓库转私有）落 failed/access_denied +
// dispatch_failed 通知；仓库复核瞬时失败保持 dispatched 等重认领；均不建
// 线程不签令牌，与防抖/轮询的确定性口径一致。
func TestReviewDispatchAccessErrTerminals(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	env := createT3TestEnv(t, e.svc.db, e.user.ID, "env-det-acc")
	createTestT3Binding(t, e.svc.db, e.project.ID, env.ID, "t3p")
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)

	e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 60, UserID: e.user.ID,
		InstallationID: 42, HeadSHA: "sha-c60", RoundNumber: 1, UpdatedAt: now,
	})
	gone := e.seedRun(t, repo, 60, "sha-c60", model.ReviewRunDispatched, now)
	e.svc.db.Model(gone).Update("environment_id", env.ID)
	e.fake.SetPRError(repo, &githubapi.StatusError{
		StatusCode: 404, Method: "GET", URL: "pulls/60", Body: "not found",
	})
	review.launchRun(context.Background(), gone.ID, now)
	var after404 model.ReviewRun
	e.svc.db.First(&after404, "id = ?", gone.ID)
	if after404.Status != model.ReviewRunCancelled || after404.ErrorCode != "pr_closed" || after404.FinishedAt == nil {
		t.Fatalf("404 run = %q/%q, want cancelled/pr_closed", after404.Status, after404.ErrorCode)
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 60", repo)
	if !ledger.SyncPending {
		t.Fatal("404 收尾应置 sync_pending（交给同步阶段清账）")
	}

	denied := e.seedRun(t, repo, 61, "sha-c61", model.ReviewRunDispatched, now)
	e.svc.db.Model(denied).Update("environment_id", env.ID)
	e.fake.SetPRError(repo, &githubapi.StatusError{
		StatusCode: 403, Method: "GET", URL: "pulls/61", Body: "forbidden",
	})
	review.launchRun(context.Background(), denied.ID, now)
	var after403 model.ReviewRun
	e.svc.db.First(&after403, "id = ?", denied.ID)
	if after403.Status != model.ReviewRunFailed || after403.ErrorCode != "access_denied" || after403.FinishedAt == nil {
		t.Fatalf("403 run = %q/%q, want failed/access_denied", after403.Status, after403.ErrorCode)
	}
	var notifCount int64
	e.svc.db.Model(&model.Notification{}).
		Where("kind = ?", model.NotificationReviewDispatchFailed).Count(&notifCount)
	if notifCount == 0 {
		t.Fatal("access_denied 应发 dispatch_failed 通知（凭证问题需用户处置）")
	}

	// PR 404 且仓库级同样确定性读不到（凭证吊销/仓库转私有也回 404）——
	// 不当「PR 已删」静默取消，按 access_denied 报真实原因。
	repo404 := e.seedRun(t, repo, 62, "sha-c62", model.ReviewRunDispatched, now)
	e.svc.db.Model(repo404).Update("environment_id", env.ID)
	e.fake.SetPRError(repo, &githubapi.StatusError{
		StatusCode: 404, Method: "GET", URL: "pulls/62", Body: "not found",
	})
	e.fake.SetRepoError(repo, &githubapi.StatusError{
		StatusCode: 404, Method: "GET", URL: "repos/x", Body: "not found",
	})
	review.launchRun(context.Background(), repo404.ID, now)
	var afterRepo404 model.ReviewRun
	e.svc.db.First(&afterRepo404, "id = ?", repo404.ID)
	if afterRepo404.Status != model.ReviewRunFailed || afterRepo404.ErrorCode != "access_denied" {
		t.Fatalf("双 404 run = %q/%q, want failed/access_denied",
			afterRepo404.Status, afterRepo404.ErrorCode)
	}

	// PR 404 但仓库级复核本身瞬时失败：不证明任何一边，等重认领。
	e.fake.SetRepoError(repo, fmt.Errorf("network down"))
	retry := e.seedRun(t, repo, 63, "sha-c63", model.ReviewRunDispatched, now)
	e.svc.db.Model(retry).Update("environment_id", env.ID)
	review.launchRun(context.Background(), retry.ID, now)
	var kept model.ReviewRun
	e.svc.db.First(&kept, "id = ?", retry.ID)
	if kept.Status != model.ReviewRunDispatched || kept.FinishedAt != nil {
		t.Fatalf("仓库复核瞬时失败应保持 dispatched 等重试: %q", kept.Status)
	}

	if len(t3.created) != 0 {
		t.Fatal("确定性失败不得建线程")
	}
	var tokCount int64
	e.svc.db.Model(&model.ReviewRunToken{}).
		Where("run_id IN ?", []string{gone.ID, denied.ID, repo404.ID, retry.ID}).Count(&tokCount)
	if tokCount != 0 {
		t.Fatal("确定性失败不得签发令牌")
	}
}

// TestReviewClosedSyncWritesNothing sync_pending 的 PR 已关闭：零 GitHub 写
// （status/顶层评论/行内 review 都不发、不建视觉运行），pending 清除、
// epoch 推进、在途运行取消。
func TestReviewClosedSyncWritesNothing(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()

	e.fake.SetPR(repo, 56, &githubapi.PullRequest{
		RepoFullName: repo, Number: 56, State: "closed", Merged: true,
		HeadSHA: "sha-c56", Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 56, UserID: e.user.ID,
		InstallationID: 1, HeadSHA: "sha-c56", RoundNumber: 1,
		SyncPending: true, SyncEpoch: 1, SyncError: "上一次同步失败：github 502", UpdatedAt: now,
	})
	run := e.seedRun(t, repo, 56, "sha-c56", model.ReviewRunRunning, now)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "h-c56", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	e.review.syncPendingPRs(now)

	counts := e.fake.CallCounts()
	for _, m := range []string{"CreateCommitStatus", "CreatePullRequestComment", "UpdatePullRequestComment", "SubmitReview"} {
		if counts[m] != 0 {
			t.Fatalf("已关闭 PR 不得有 GitHub 写 %s: %+v", m, counts)
		}
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 56", repo)
	if ledger.SyncPending || ledger.SyncEpoch != 2 {
		t.Fatalf("pending=%v epoch=%d, want false/2", ledger.SyncPending, ledger.SyncEpoch)
	}
	// 关闭收尾必须顺手清 sync_error：外层成功收尾按旧 epoch 更新会匹配 0 行，
	// pending 又已清、不会再进同步，残下的失败文案就永久留在账本上。
	if ledger.SyncError != "" {
		t.Fatalf("sync_error = %q, want 空", ledger.SyncError)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunCancelled || after.ErrorCode != "superseded" {
		t.Fatalf("在途运行 = %q/%q, want cancelled/superseded", after.Status, after.ErrorCode)
	}
	var token model.ReviewRunToken
	e.svc.db.First(&token, "run_id = ?", run.ID)
	if token.ConsumedAt == nil {
		t.Fatal("在途运行令牌应已消费")
	}
	var runCount int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 56").Count(&runCount)
	if runCount != 1 {
		t.Fatalf("不得新建（视觉）运行，runs=%d", runCount)
	}
}

// TestReviewClosedRerunRejected 已关闭 PR 手动重跑 → 「PR 已关闭」40001，
// 不开新轮。
func TestReviewClosedRerunRejected(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	e.fake.SetPR(repo, 57, &githubapi.PullRequest{
		RepoFullName: repo, Number: 57, State: "closed",
		HeadSHA: "sha-c57", Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})

	_, err := e.review.RerunReview(context.Background(), e.user.ID, e.project.ID, 57, "")
	if err == nil || !strings.Contains(err.Error(), "PR 已关闭") {
		t.Fatalf("rerun err = %v, want 含「PR 已关闭」", err)
	}
	assertErrCode(t, err, 40001)
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("已关闭 PR 不得开新轮, runs=%d", runs)
	}
}

// TestReviewClosedResultSuperseded /result 提交时 PR 已关闭 → 40920；
// 运行 cancelled/superseded + 令牌消费 + sync_pending。
func TestReviewClosedResultSuperseded(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	e.fake.SetPR(repo, 58, &githubapi.PullRequest{
		RepoFullName: repo, Number: 58, State: "closed",
		HeadSHA: "sha-c58", Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})
	e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 58, UserID: e.user.ID,
		HeadSHA: "sha-c58", RoundNumber: 1, UpdatedAt: now,
	})
	run := e.seedRun(t, repo, 58, "sha-c58", model.ReviewRunRunning, now)
	e.svc.db.Create(&model.ReviewRunToken{
		RunID: run.ID, TokenHash: "h-c58", CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	})

	good := `{"findings":[],"prior":[],"summary":"s","ui_changed":false,"ui_changed_since_last_visual":false}`
	err := e.review.AgentSubmitResult(context.Background(), run.ID, []byte(good))
	assertErrCode(t, err, 40920)
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunCancelled || after.ErrorCode != "superseded" {
		t.Fatalf("run = %q/%q, want cancelled/superseded", after.Status, after.ErrorCode)
	}
	var token model.ReviewRunToken
	e.svc.db.First(&token, "run_id = ?", run.ID)
	if token.ConsumedAt == nil {
		t.Fatal("令牌应已消费")
	}
	var ledger model.ReviewPullRequest
	e.svc.db.First(&ledger, "repo_full_name = ? AND pr_number = 58", repo)
	if !ledger.SyncPending {
		t.Fatal("sync_pending 应置位")
	}
}

// TestReviewArchiveSkipsNeverLaunched pr_closed 的取消发生在建线程之前
// （线程从未创建）：归档阶段不得把它当「线程游离」去 StopThread/Archive，
// 更不能把归档失败误报成 review_cleanup_failed 假告警。对照组：真正发出过
// 请求（dispatched_at 非空）的 cancelled 运行线程可能已建成，仍被归档。
func TestReviewArchiveSkipsNeverLaunched(t *testing.T) {
	e := newReviewEngineEnv(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)

	// pr_closed 且从未确认建线程（dispatch_started_at 已被派发认领写入）。
	pclosed := e.seedDispatchedRun(t, repo, 59, model.ReviewKindCode, model.ReviewRunCancelled, "", false, now)
	if err := e.svc.db.Model(pclosed).Updates(map[string]any{
		"dispatched_at": nil, "error_code": "pr_closed",
	}).Error; err != nil {
		t.Fatalf("seed pr_closed: %v", err)
	}
	// 对照组：已发出请求的 cancelled 运行。
	sent := e.seedDispatchedRun(t, repo, 60, model.ReviewKindCode, model.ReviewRunCancelled, "", false, now)
	e.svc.db.Model(sent).Update("error_code", "taken_over")

	review.archiveRuns(now)

	for _, th := range t3.stopped {
		if th == pclosed.ThreadID {
			t.Fatalf("从未建线程的运行不得尝试停线程: %v", t3.stopped)
		}
	}
	for _, th := range t3.archived {
		if th == pclosed.ThreadID {
			t.Fatalf("从未建线程的运行不得进入归档: %v", t3.archived)
		}
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("pr_closed 跳过归档不应产生回收失败通知")
	}
	found := false
	for _, th := range t3.archived {
		if th == sent.ThreadID {
			found = true
		}
	}
	if !found {
		t.Fatalf("已发出请求的 cancelled 运行仍应被归档: %v", t3.archived)
	}
	var after model.ReviewRun
	e.svc.db.First(&after, "id = ?", sent.ID)
	if after.ArchivedAt == nil {
		t.Fatal("对照组应写 archived_at")
	}

	// 回收阶段同理：从未建线程的运行直接落 removed（无线程/无工作区可清），
	// 不去读线程投影、不发回收失败通知（仍会被回收查询选入，只是立即收尾）。
	review.cleanupWorktrees(now)
	var cleaned model.ReviewRun
	e.svc.db.First(&cleaned, "id = ?", pclosed.ID)
	if cleaned.WorktreeState != model.ReviewWorktreeRemoved || cleaned.WorktreeError != "" {
		t.Fatalf("pr_closed 的 worktree_state/error = %q/%q, want removed/空",
			cleaned.WorktreeState, cleaned.WorktreeError)
	}
	for _, th := range t3.threadWTCalls {
		if th == pclosed.ThreadID {
			t.Fatalf("从未建线程的运行不得读线程投影: %v", t3.threadWTCalls)
		}
	}
	if e.cleanupFailedCount(t) != 0 {
		t.Fatal("pr_closed 收尾不应产生回收失败通知")
	}
}

// 密文损坏按确定性失败（fast-ship-bot 第 36 轮 P2）：GitHub App 部署配置
// 解密失败与 Forgejo 集成密文损坏同口径——防抖行删行不再推后、评论唤醒
// 判丢弃补登幂等账，而不是无限重试/500 刷错。
func TestReviewDecryptErrorsAreDeterministic(t *testing.T) {
	if !deterministicReviewAccessErr(errGitHubAppDecrypt) {
		t.Fatal("防抖分类应把 App 配置解密失败当确定性失败")
	}
	if !deterministicCommentWakeSetupErr(errGitHubAppDecrypt) {
		t.Fatal("评论唤醒分类应把 App 配置解密失败当确定性失败")
	}
}

// 防抖实况读的安装 id 兜底（fast-ship-bot 第 36 轮 P2）：GitHub 投递缺
// installation 快照（行内为 0）时先沿用总账快照，无总账才按仓库解析——
// 直接拿 0 换 token 会 404 并被确定性分支删行静默丢推送。
func TestReviewDebounceInstallationFallback(t *testing.T) {
	now := time.Now().UTC()

	// 案例一：总账有 installation 快照（7）——沿用，不发解析调用。
	t.Run("ledger-snapshot", func(t *testing.T) {
		e := newReviewEngineEnv(t)
		e.enableReview(t)
		repo := e.project.GithubOwner + "/" + e.project.GithubRepo
		t3 := &fakeReviewT3{}
		review := e.withT3(t3, nil, nil)
		e.fake.SetPR(repo, 81, &githubapi.PullRequest{
			RepoFullName: repo, Number: 81, State: "open", HeadSHA: "sha-81",
			HeadRepoFullName: repo, Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
		})
		if err := e.svc.db.Create(&model.ReviewPullRequest{
			ProjectID: e.project.ID, UserID: e.project.UserID,
			RepoFullName: repo, PRNumber: 81,
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
			HeadSHA:       "sha-81", RoundNumber: 1, UpdatedAt: now,
			InstallationID: 7,
		}).Error; err != nil {
			t.Fatalf("seed ledger: %v", err)
		}
		if err := e.svc.db.Create(&model.ReviewDebounce{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
			ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 81,
			HeadSHA: "sha-81", Action: "synchronize", InstallationID: 0,
			PRTitle: "t", PRURL: "u", BaseRef: "main", HeadRef: "feature",
			FireAt: now.Add(-time.Second), UpdatedAt: now.Add(-time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed debounce: %v", err)
		}

		review.processDueDebounces(now)

		for _, c := range e.fake.Calls {
			if c.Method == "ResolveInstallation" {
				t.Fatal("总账快照可用时不得发安装解析调用")
			}
		}
		var runs int64
		e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 81").Count(&runs)
		if runs == 0 {
			t.Fatal("应照常开轮")
		}
		var ledger model.ReviewPullRequest
		var run model.ReviewRun
		if err := e.svc.db.Where("pr_number = 81").First(&ledger).Error; err != nil {
			t.Fatalf("总账行应存在: %v", err)
		}
		if err := e.svc.db.Where("pr_number = 81 AND head_sha = ?", "sha-81").First(&run).Error; err != nil {
			t.Fatalf("运行行应存在: %v", err)
		}
		if ledger.InstallationID != 7 || run.InstallationID != 7 {
			t.Fatalf("installation 快照应沿总账值 7: ledger=%d run=%d", ledger.InstallationID, run.InstallationID)
		}
		if got := e.debounceCount(t); got != 0 {
			t.Fatalf("防抖行应被消费，剩 %d", got)
		}
	})

	// 案例二：无总账——按仓库解析安装（fake 固定返回 1），照常开轮。
	t.Run("resolve", func(t *testing.T) {
		e := newReviewEngineEnv(t)
		e.enableReview(t)
		repo := e.project.GithubOwner + "/" + e.project.GithubRepo
		t3 := &fakeReviewT3{}
		review := e.withT3(t3, nil, nil)
		e.fake.SetPR(repo, 82, &githubapi.PullRequest{
			RepoFullName: repo, Number: 82, State: "open", HeadSHA: "sha-82",
			HeadRepoFullName: repo, Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
		})
		if err := e.svc.db.Create(&model.ReviewDebounce{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
			ProjectID:     e.project.ID, RepoFullName: repo, PRNumber: 82,
			HeadSHA: "sha-82", Action: "synchronize", InstallationID: 0,
			PRTitle: "t", PRURL: "u", BaseRef: "main", HeadRef: "feature",
			FireAt: now.Add(-time.Second), UpdatedAt: now.Add(-time.Minute),
		}).Error; err != nil {
			t.Fatalf("seed debounce: %v", err)
		}

		review.processDueDebounces(now)

		resolved := false
		for _, c := range e.fake.Calls {
			if c.Method == "ResolveInstallation" {
				resolved = true
			}
		}
		if !resolved {
			t.Fatal("无总账快照时应按仓库解析安装")
		}
		var runs int64
		e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 82").Count(&runs)
		if runs == 0 {
			t.Fatal("解析后应照常开轮")
		}
		var ledger model.ReviewPullRequest
		var run model.ReviewRun
		if err := e.svc.db.Where("pr_number = 82").First(&ledger).Error; err != nil {
			t.Fatalf("总账行应存在: %v", err)
		}
		if err := e.svc.db.Where("pr_number = 82 AND head_sha = ?", "sha-82").First(&run).Error; err != nil {
			t.Fatalf("运行行应存在: %v", err)
		}
		if ledger.InstallationID != 1 || run.InstallationID != 1 {
			t.Fatalf("解析出的安装 id 应落账本与运行行: ledger=%d run=%d", ledger.InstallationID, run.InstallationID)
		}
	})
}

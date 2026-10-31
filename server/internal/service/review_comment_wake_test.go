package service

// 评论唤醒（INT-179）：issue_comment 顶层评论 @fast-ship-bot 也开新一轮
// 审查。覆盖守卫顺序（action/mention/PR 归属/启用反查/fork）、两阶段事务
// 编排（幂等重投、实拉确定性失败丢弃、瞬时错误后重投可重走、事务失败回滚
// 幂等账、head 采信与观察窗口竞态、复核停用/改绑、安装解析确定性分类、
// 预查后并发重投的判重兜底）、恒开新轮语义（同 head 也新轮、取消在途、
// 删防抖）与 Forgejo 集成路径。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/errs"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"gorm.io/gorm"
	"sync"
)

// issueCommentBody 组一个 issue_comment webhook body。installationID<0 表
// 示省略 installation 字段（GitHub 代理剥字段/Forgejo 无安装语义的形态）。
func issueCommentBody(body, repo string, number int, installationID int64, opts ...func(map[string]any)) []byte {
	issue := map[string]any{
		"number":       number,
		"pull_request": map[string]any{"html_url": "https://example.com/" + repo + "/pull/" + strconv.Itoa(number)},
	}
	payload := map[string]any{
		"action":     "created",
		"issue":      issue,
		"comment":    map[string]any{"body": body, "author_association": "OWNER"},
		"repository": map[string]any{"full_name": repo},
	}
	if installationID >= 0 {
		payload["installation"] = map[string]any{"id": installationID}
	}
	for _, opt := range opts {
		opt(payload)
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// dropIssuePullRequest 把 issue.pull_request 摘掉（普通 Issue 评论的形态）。
func dropIssuePullRequest(payload map[string]any) {
	issue := payload["issue"].(map[string]any)
	delete(issue, "pull_request")
}

// dropCommentUser 把 comment.user 摘掉（代理剥字段的形态：无法与 PR 作者
// 比对登录名）。
func dropCommentUser(payload map[string]any) {
	comment := payload["comment"].(map[string]any)
	delete(comment, "user")
}

// setCommentAction 覆盖 action（edited/deleted 等不唤醒）。
func setCommentAction(action string) func(map[string]any) {
	return func(payload map[string]any) { payload["action"] = action }
}

// setCommentUserType 覆盖评论作者类型（GitHub 把 App/机器人账号标为 Bot）。
func setCommentUserType(userType string) func(map[string]any) {
	return func(payload map[string]any) {
		payload["comment"].(map[string]any)["user"] = map[string]any{"type": userType, "login": "fast-ship-bot"}
	}
}

// setCommentAssociation 覆盖评论者与仓库/Issue 的关系（GitHub 入口的身份
// 过滤：仅 OWNER 可点名；COLLABORATOR 邀请可授 Read/Triage、MEMBER
// 只是组织成员身份、CONTRIBUTOR 是历史提交而非当前权限、NONE/未知无缘
// 者——均忽略；PR 作者走登录名比对例外）。
func setCommentAssociation(association string) func(map[string]any) {
	return func(payload map[string]any) {
		payload["comment"].(map[string]any)["author_association"] = association
	}
}

// setCommentLogin 覆盖评论者登录名（PR 作者例外：与实拉 PR 作者比对）。
func setCommentLogin(login string) func(map[string]any) {
	return func(payload map[string]any) {
		comment := payload["comment"].(map[string]any)
		user, _ := comment["user"].(map[string]any)
		if user == nil {
			user = map[string]any{}
			comment["user"] = user
		}
		user["login"] = login
	}
}

// TestReviewCommentWakeOpensRound 正向路径：PR 顶层评论点名机器人 → 实拉
// 实况 open → 恒开新轮（action=comment），落 seen（受理史与 push 同源）、
// 不落防抖行。
func TestReviewCommentWakeOpensRound(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	wakeAt := time.Now().UTC().Add(-30 * time.Minute)
	e.fake.SetPR(repo, 21, &githubapi.PullRequest{
		RepoFullName: repo, Number: 21, State: "open",
		HeadSHA: "sha-w1", HeadRepoFullName: repo, UpdatedAt: &wakeAt,
		Title: "wake me", HTMLURL: "https://github.com/" + repo + "/pull/21",
		BaseRef: "main", HeadRef: "feature",
	})

	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-cw-1",
		issueCommentBody("请再审一轮 @fast-ship-bot", repo, 21, 42)); err != nil {
		t.Fatalf("comment wake: %v", err)
	}

	var runs []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = 21", repo).Find(&runs)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	run := runs[0]
	if run.TriggerAction != "comment" || run.HeadSHA != "sha-w1" || run.RoundNumber != 1 {
		t.Fatalf("run = action=%s head=%s round=%d", run.TriggerAction, run.HeadSHA, run.RoundNumber)
	}
	if run.InstallationID != 42 {
		t.Fatalf("installation = %d, want 42", run.InstallationID)
	}

	// 开轮置 sync_pending，且不写 seen（评论唤醒不走 pull_request 受理面）。
	var ledger model.ReviewPullRequest
	if err := e.svc.db.Where("repo_full_name = ? AND pr_number = 21", repo).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if !ledger.SyncPending || ledger.RoundNumber != 1 {
		t.Fatalf("ledger = pending=%v round=%d, want true/1", ledger.SyncPending, ledger.RoundNumber)
	}
	// 受理登记 seen（fast-ship-bot 第 38 轮 P2：push 触发面的时序判定以
	// 当前 head 的 seen 行为基准，comment 受理必须进受理史）。
	var seen model.ReviewSeenSHA
	if err := e.svc.db.First(&seen).Error; err != nil {
		t.Fatalf("评论唤醒应写 review_seen_shas: %v", err)
	}
	if seen.HeadSHA != "sha-w1" || seen.PRUpdatedAt == nil {
		t.Fatalf("seen = head=%s pr_updated_at=%v", seen.HeadSHA, seen.PRUpdatedAt)
	}
	if got := e.debounceCount(t); got != 0 {
		t.Fatalf("评论唤醒不落防抖行，实得 %d 行", got)
	}
	// 幂等账已登记（github-default 前缀键）。
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-cw-1")).
		Count(&deliveries)
	if deliveries != 1 {
		t.Fatalf("幂等账 = %d, want 1", deliveries)
	}
}

// TestReviewCommentWakeAlwaysNewRound 同 head 唤醒的两种形态：同 head 有
// 在途轮时不重开（防连续点名作废在途审查），也不动未到期防抖行；第 1 轮
// 出结论后评论唤醒开第 2 轮（trigger_action=comment，同 head 也新轮），
// 评论前残留的未到期防抖行被删除——旧推送的防抖不再开火，只留下
// comment 轮。
func TestReviewCommentWakeAlwaysNewRound(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	ctx := context.Background()
	e.fake.SetPR(repo, 22, &githubapi.PullRequest{
		RepoFullName: repo, Number: 22, State: "open",
		HeadSHA: "sha-same", HeadRepoFullName: repo,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})

	// 第 1 轮：正常 push 触发（opened → 防抖 → 到期开轮）。
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-r1",
		prPayload("opened", repo, 22, "sha-same", false, false)); err != nil {
		t.Fatalf("webhook opened: %v", err)
	}
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)
	// 防抖窗口 60s：推进时钟让第 1 轮的防抖行到期开轮。
	review.processDueDebounces(now.Add(2 * time.Minute))
	var round1 []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = 22 AND round_number = 1", repo).Find(&round1)
	if len(round1) != 1 || round1[0].TriggerAction != "opened" {
		t.Fatalf("第 1 轮未开出: %+v", round1)
	}

	// 评论前再留一条未到期防抖行（模拟新 push 刚落行、尚未到期的形态）：
	// 不经 processDueDebounces 消费，验证评论唤醒对它的清理。
	if err := e.svc.db.Create(&model.ReviewDebounce{
		ProjectID: e.project.ID, RepoFullName: strings.ToLower(repo), PRNumber: 22,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-same", Action: "synchronize", InstallationID: 42,
		FireAt: now.Add(time.Minute), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed debounce: %v", err)
	}

	// 同 head 已有在途第 1 轮：评论唤醒不重开（两次点名互相作废在途轮、
	// 公开仓库可被连续点名饿死审查），也不动未到期防抖行；投递记幂等账
	// （确定跳过，重投按 202 去重）。
	if err := review.HandleGitHubWebhook(ctx, "issue_comment", "d-r2",
		issueCommentBody("@fast-ship-bot 再看一眼", repo, 22, 42)); err != nil {
		t.Fatalf("comment wake 1: %v", err)
	}
	var stillActive int64
	e.svc.db.Model(&model.ReviewRun{}).
		Where("repo_full_name = ? AND pr_number = 22 AND status IN ?", repo,
			[]model.ReviewRunStatus{model.ReviewRunQueued, model.ReviewRunDispatched, model.ReviewRunRunning}).
		Count(&stillActive)
	if stillActive != 1 {
		t.Fatalf("在途同 head 轮不应被作废，active = %d", stillActive)
	}
	if got := e.debounceCount(t); got != 1 {
		t.Fatalf("跳过路径不得动防抖行，剩 %d", got)
	}

	// 第 1 轮出结论（置终态）后评论唤醒：同 head 无在途 → 开第 2 轮。
	if err := e.svc.db.Model(&model.ReviewRun{}).
		Where("repo_full_name = ? AND pr_number = 22", repo).
		Updates(map[string]any{"status": model.ReviewRunCompleted, "finished_at": now.Add(3 * time.Minute)}).Error; err != nil {
		t.Fatalf("complete round1: %v", err)
	}
	if err := review.HandleGitHubWebhook(ctx, "issue_comment", "d-r3",
		issueCommentBody("@fast-ship-bot 结论有误，请重审", repo, 22, 42)); err != nil {
		t.Fatalf("comment wake 2: %v", err)
	}
	var round2 []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = 22 AND round_number = 2", repo).Find(&round2)
	if len(round2) != 1 || round2[0].TriggerAction != "comment" || round2[0].HeadSHA != "sha-same" {
		t.Fatalf("第 2 轮未按 comment 开出: %+v", round2)
	}
	if got := e.debounceCount(t); got != 0 {
		t.Fatalf("未到期防抖行应随唤醒删除，剩 %d", got)
	}
	// 防抖行已删：推进时钟后不再有任何轮次开火——只留下 comment 轮。
	review.processDueDebounces(now.Add(4 * time.Minute))
	var total int64
	e.svc.db.Model(&model.ReviewRun{}).Where("repo_full_name = ? AND pr_number = 22", repo).Count(&total)
	if total != 2 {
		t.Fatalf("防抖行不应再开火，总运行 = %d, want 2（第 1、2 轮）", total)
	}
}

// TestReviewCommentWakeVisualInflightDoesNotBlock 视觉运行在途不阻塞点名：
// 代码轮已出结论、视觉段在途（同 head）时评论唤醒照常开新代码轮，并把
// 在途视觉运行 superseded——视觉在途说明代码结论已发布，正是「结论有误
// 请重审」的主场景（fast-ship-bot 第 7 轮 P2：在途守卫只计 kind=code）。
func TestReviewCommentWakeVisualInflightDoesNotBlock(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	now := time.Now().UTC()
	ctx := context.Background()
	e.fake.SetPR(repo, 23, &githubapi.PullRequest{
		RepoFullName: repo, Number: 23, State: "open",
		HeadSHA: "sha-vis", HeadRepoFullName: repo,
		Title: "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})

	// 第 1 轮代码运行已出结论（终态），同 head 视觉运行在途。
	if err := e.review.HandleGitHubWebhook(ctx, "pull_request", "d-v1",
		prPayload("opened", repo, 23, "sha-vis", false, false)); err != nil {
		t.Fatalf("webhook opened: %v", err)
	}
	t3 := &fakeReviewT3{}
	review := e.withT3(t3, nil, nil)
	review.processDueDebounces(now.Add(2 * time.Minute))
	if err := e.svc.db.Model(&model.ReviewRun{}).
		Where("repo_full_name = ? AND pr_number = 23 AND kind = ?", repo, model.ReviewKindCode).
		Updates(map[string]any{"status": model.ReviewRunCompleted, "finished_at": now.Add(5 * time.Minute)}).Error; err != nil {
		t.Fatalf("complete code: %v", err)
	}
	visual := &model.ReviewRun{
		ID: "visual-inflight", UserID: e.project.UserID, ProjectID: e.project.ID,
		Kind: model.ReviewKindVisual, RepoFullName: strings.ToLower(repo), PRNumber: 23,
		HeadSHA: "sha-vis", RoundNumber: 1, IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		TriggerAction: "visual", ThreadID: "t-vis", LaunchCommandID: "lc-vis", InitialMessageID: "im-vis",
		Status: model.ReviewRunRunning, DeadlineAt: now.Add(30 * time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := e.svc.db.Create(visual).Error; err != nil {
		t.Fatalf("seed visual: %v", err)
	}

	// 点名：视觉在途不算「同 head 在途审查」，照常开新代码轮并取消视觉段。
	if err := review.HandleGitHubWebhook(ctx, "issue_comment", "d-vis-wake",
		issueCommentBody("@fast-ship-bot 代码结论有误，请重审", repo, 23, 42)); err != nil {
		t.Fatalf("comment wake: %v", err)
	}
	var round2 []model.ReviewRun
	e.svc.db.Where("repo_full_name = ? AND pr_number = 23 AND round_number = 2 AND kind = ?", repo, model.ReviewKindCode).Find(&round2)
	if len(round2) != 1 || round2[0].TriggerAction != "comment" {
		t.Fatalf("视觉在途时应照常开新代码轮: %+v", round2)
	}
	var vis model.ReviewRun
	if err := e.svc.db.Where("id = ?", "visual-inflight").First(&vis).Error; err != nil {
		t.Fatalf("visual run: %v", err)
	}
	if vis.Status != model.ReviewRunCancelled || vis.ErrorCode != "superseded" {
		t.Fatalf("在途视觉运行应被 superseded: status=%s err=%s", vis.Status, vis.ErrorCode)
	}
}

// TestReviewCommentWakeLegacyBareKeyDedup 存量裸键兼容（codex 第 8 轮 P2）：
// 集成前缀账键引入前登记过的裸 GUID 行，重投同 delivery 按幂等去重，
// 不重复受理、不开轮。
func TestReviewCommentWakeLegacyBareKeyDedup(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 36, &githubapi.PullRequest{
		RepoFullName: repo, Number: 36, State: "open", HeadSHA: "sha-legacy",
		HeadRepoFullName: repo,
	})
	// 存量裸键行（集成前缀引入前的形态，只可能属于 github-default）。
	if err := e.svc.db.Create(&model.GitHubWebhookDelivery{
		DeliveryID: "legacy-bare-guid", Event: "issue_comment", ReceivedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "legacy-bare-guid",
		issueCommentBody("@fast-ship-bot", repo, 36, 42)); !errors.Is(err, ErrDeliveryDuplicated) {
		t.Fatalf("存量裸键应按幂等去重，实得 %v", err)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("去重路径不得开轮，runs = %d", runs)
	}
}

// TestReviewCommentWakeAppNotConfigured webhook-secret-only 部署（无 App
// 身份）的永久配置态：reviewClient 链路返回 githubapp.ErrAppNotConfigured
// 哨兵 → 判确定性丢弃并补登幂等账，不让平台无限重投（codex 第 11 轮 P2）；
// 同 delivery 重投按幂等去重。
func TestReviewCommentWakeAppNotConfigured(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 57, &githubapi.PullRequest{
		RepoFullName: repo, Number: 57, State: "open", HeadSHA: "sha-noapp",
		HeadRepoFullName: repo,
	})
	e.review.clients = errClientResolver{err: githubapp.ErrAppNotConfigured}

	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-noapp",
		issueCommentBody("@fast-ship-bot", repo, 57, 42)); err != nil {
		t.Fatalf("App 未配置应判确定性丢弃返回 nil，实得 %v", err)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("丢弃路径不得开轮，runs = %d", runs)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-noapp",
		issueCommentBody("@fast-ship-bot", repo, 57, 42)); !errors.Is(err, ErrDeliveryDuplicated) {
		t.Fatalf("幂等账应落定，重投按去重，实得 %v", err)
	}
}

// TestReviewCommentWakeStaleLiveSnapshot 实拉快照时序判定：seen 行存有已
// 受理 head 的 pr_updated_at 时，实拉 UpdatedAt 更早 = 滞后快照（单 PR 读
// 偶发滞后/前置缓存）——采信会把总账回退到旧 head、新 head 永久漏审。
// 滞后快照判丢弃并落幂等账；新鲜快照（UpdatedAt 更晚）照常开轮并推进
// 总账（fast-ship-bot 第 17 轮 P2）。
func TestReviewCommentWakeStaleLiveSnapshot(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()

	t1 := time.Now().UTC().Add(-2 * time.Hour)
	t0 := t1.Add(-time.Hour)
	t2 := t1.Add(time.Hour)
	// 账本 head=sha-a（accepted at t1，seen 行带 pr_updated_at 基准）。
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: strings.ToLower(repo), PRNumber: 40,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-a", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: strings.ToLower(repo), PRNumber: 40,
		HeadSHA: "sha-a", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed seen: %v", err)
	}

	// 滞后快照：live head=sha-b 但 UpdatedAt=t0 早于已受理基准 t1 → 按瞬
	// 时失败处理（500，平台/对账重投拿到新鲜快照后自然恢复），不落任何账。
	e.fake.SetPR(repo, 40, &githubapi.PullRequest{
		RepoFullName: repo, Number: 40, State: "open", HeadSHA: "sha-b",
		HeadRepoFullName: repo, UpdatedAt: &t0,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-stale",
		issueCommentBody("@fast-ship-bot", repo, 40, 42)); err == nil {
		t.Fatal("滞后快照应返回 error（500，平台重投）")
	}
	var led model.ReviewPullRequest
	if err := e.svc.db.Where("pr_number = 40").First(&led).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if led.HeadSHA != "sha-a" {
		t.Fatalf("总账不得回退到滞后 head，实得 %s", led.HeadSHA)
	}
	var staleRuns, staleDeliveries int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&staleRuns)
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-stale")).
		Count(&staleDeliveries)
	if staleRuns != 0 || staleDeliveries != 0 {
		t.Fatalf("滞后快照不得开轮/烧幂等账: runs=%d deliveries=%d", staleRuns, staleDeliveries)
	}

	// 新鲜快照：UpdatedAt=t2 晚于基准 → 采信开轮，总账推进到 sha-b。
	e.fake.SetPR(repo, 40, &githubapi.PullRequest{
		RepoFullName: repo, Number: 40, State: "open", HeadSHA: "sha-b",
		HeadRepoFullName: repo, UpdatedAt: &t2,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-fresh",
		issueCommentBody("@fast-ship-bot", repo, 40, 42)); err != nil {
		t.Fatalf("新鲜快照应开轮: %v", err)
	}
	if err := e.svc.db.Where("pr_number = 40").First(&led).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if led.HeadSHA != "sha-b" {
		t.Fatalf("总账应推进到新鲜 head，实得 %s", led.HeadSHA)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("head_sha = ? AND trigger_action = ?", "sha-b", "comment").Count(&runs)
	if runs == 0 {
		t.Fatal("新鲜快照应按 comment 开轮")
	}
}

// TestReviewCommentWakeRedeliveryDup 同一 delivery id 重投按幂等去重
// （预查命中，不再实拉）；payload 守卫忽略的投递（无 mention）不登记幂等
// 账——忽略判定完全由 payload 决定，重投再忽略一遍零副作用。
func TestReviewCommentWakeRedeliveryDup(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 23, &githubapi.PullRequest{
		RepoFullName: repo, Number: 23, State: "open", HeadSHA: "sha-dup", HeadRepoFullName: repo,
	})

	body := issueCommentBody("@fast-ship-bot 看看", repo, 23, 42)
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-dup", body); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-dup", body); !errors.Is(err, ErrDeliveryDuplicated) {
		t.Fatalf("重投应 ErrDeliveryDuplicated，实得 %v", err)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = ?", 23).Count(&runs)
	if runs != 1 {
		t.Fatalf("重投不得重复开轮，runs = %d", runs)
	}

	// 无 mention 的投递（payload 守卫忽略）不登记幂等账。
	ignored := issueCommentBody("普通评论", repo, 24, 42)
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-ignored", ignored); err != nil {
		t.Fatalf("ignored: %v", err)
	}
	var ignoredLedger int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-ignored")).
		Count(&ignoredLedger)
	if ignoredLedger != 0 {
		t.Fatalf("payload 守卫忽略的投递不得登记幂等账，实得 %d 行", ignoredLedger)
	}
	// 重投：再忽略一遍（返回 nil），依旧零副作用。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-ignored", ignored); err != nil {
		t.Fatalf("忽略投递重投应静默忽略，实得 %v", err)
	}
}

// TestReviewCommentWakeGuards 守卫顺序：非 created 动作、无 mention（大小写
// 敏感于命中）、普通 Issue 评论、未启用仓库都不唤醒；mention 大小写不敏感。
func TestReviewCommentWakeGuards(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 25, &githubapi.PullRequest{
		RepoFullName: repo, Number: 25, State: "open", HeadSHA: "sha-g",
		HeadRepoFullName: repo, AuthorLogin: "pr-author",
	})
	// 未启用仓库也能实拉成功（如 App 装在别的仓库上）：启用判定由最终事务
	// 内的反查按设置行快照拒绝，不开轮。
	e.fake.SetPR("other/repo", 25, &githubapi.PullRequest{
		RepoFullName: "other/repo", Number: 25, State: "open", HeadSHA: "sha-other",
		HeadRepoFullName: "other/repo",
	})

	for i, body := range [][]byte{
		// 非 created（编辑/删除不唤醒）。
		issueCommentBody("@fast-ship-bot", repo, 25, 42, setCommentAction("edited")),
		// 无 mention。
		issueCommentBody("帮忙看看这个 PR", repo, 25, 42),
		// 普通 Issue 评论（issue.pull_request 缺失）。
		issueCommentBody("@fast-ship-bot", repo, 25, 42, dropIssuePullRequest),
		// 未启用审查的仓库。
		issueCommentBody("@fast-ship-bot", "other/repo", 25, 42),
		// 平台标记的 Bot 账号自评论：正文可能引用用户评论里的 mention，
		// 不排除会形成「发布结论 → 再唤醒」的无限审查循环。
		issueCommentBody("@fast-ship-bot 结论：发现问题", repo, 25, 42, setCommentUserType("Bot")),
		// 机器评论的隐藏标记固定在正文开头（与 upsertPRComment 的 own 判定
		// 同口径），marker 开头的整条评论不唤醒。
		issueCommentBody("<!-- fast-ship-code-review -->\n结论 含 @fast-ship-bot", repo, 25, 42),
		issueCommentBody("<!-- codex-pull-request-review-summary -->\n含 @fast-ship-bot", repo, 25, 42),
		// 无缘者（author_association NONE）：公开仓库陌生人原本触发不了
		// 任何审查（fork PR 被拒、无 push 权限），不能获得无限烧额度通道。
		issueCommentBody("@fast-ship-bot 看看", repo, 25, 42, setCommentAssociation("NONE")),
		// CONTRIBUTOR 是「历史提交过」而非当前权限（GitHub 官方定义），
		// 失去当前授权的历史贡献者同样不收（codex 第 10 轮 P2）。
		issueCommentBody("@fast-ship-bot 再看看", repo, 25, 42, setCommentAssociation("CONTRIBUTOR")),
		// MEMBER 只是组织成员身份（组织仓库基础权限可设为 Read，不代表写
		// 权限），同样不收（codex 第 14 轮 P2）。
		issueCommentBody("@fast-ship-bot 三看", repo, 25, 42, setCommentAssociation("MEMBER")),
		// COLLABORATOR 邀请可授 Read/Triage（codex 第 15 轮 P2），同样不收。
		issueCommentBody("@fast-ship-bot 四看", repo, 25, 42, setCommentAssociation("COLLABORATOR")),
	} {
		if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", fmt.Sprintf("d-g%d", i), body); err != nil {
			t.Fatalf("guard %d: %v", i, err)
		}
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("守卫不过不得开轮，runs = %d", runs)
	}

	// mention 大小写不敏感：命中即唤醒。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-g-upper",
		issueCommentBody("麻烦 @Fast-Ship-BOT 复审", repo, 25, 42)); err != nil {
		t.Fatalf("upper mention: %v", err)
	}
	// 人类引用/摘录机器人评论片段（marker 落在正文中间）不误杀：先把
	// 上一轮置终态（否则同 head 在途守卫会跳过），再断言开新轮。
	if err := e.svc.db.Model(&model.ReviewRun{}).
		Where("pr_number = 25").
		Updates(map[string]any{"status": model.ReviewRunCompleted, "finished_at": time.Now().UTC()}).Error; err != nil {
		t.Fatalf("complete: %v", err)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-g-quote",
		issueCommentBody("引用机器人结论片段 <!-- fast-ship-code-review --> 后 @fast-ship-bot 请再审", repo, 25, 42,
			setCommentAssociation("NONE"), setCommentLogin("pr-author"))); err != nil {
		t.Fatalf("quote marker: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 25").First(&run).Error; err != nil {
		t.Fatalf("大写 mention 应唤醒: %v", err)
	}
	var wakeCount int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 25").Count(&wakeCount)
	if wakeCount != 2 {
		t.Fatalf("大写 mention 与引用 marker 的人类评论各开一轮，runs = %d", wakeCount)
	}
	if run.TriggerAction != "comment" {
		t.Fatalf("trigger_action = %q, want comment", run.TriggerAction)
	}
}

// TestReviewCommentWakeDraftAndClosed PR 实况关卡：draft 与已关闭/合并的 PR
// 评论唤醒一律忽略（以 GetPullRequest 实时 State/Draft 为准，不信事件体）。
func TestReviewCommentWakeDraftAndClosed(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 26, &githubapi.PullRequest{
		RepoFullName: repo, Number: 26, State: "open", Draft: true, HeadSHA: "sha-draft",
	})
	e.fake.SetPR(repo, 27, &githubapi.PullRequest{
		RepoFullName: repo, Number: 27, State: "closed", Merged: true, HeadSHA: "sha-closed",
	})
	// 脏数据形态：state=open + merged=true（部分 Forgejo/Gitea 实例合并后
	// 的返回），同样不可审（fast-ship-bot 第 16 轮 P2：prReviewable 排
	// Merged 后与轮询口径一致）。
	e.fake.SetPR(repo, 28, &githubapi.PullRequest{
		RepoFullName: repo, Number: 28, State: "open", Merged: true, HeadSHA: "sha-merged",
	})

	for i, tc := range []struct{ number int }{{26}, {27}, {28}} {
		if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", fmt.Sprintf("d-dc%d", i),
			issueCommentBody("@fast-ship-bot", repo, tc.number, 42)); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("draft/关闭/合并 PR 不得开轮，runs = %d", runs)
	}
}

// TestReviewCommentWakeUnqualifiedNoAPI 显然无资格的点名（association
// 不在允许集且 payload 无登录名可比对作者）不必实拉：零 API 成本拒绝
// （codex 第 17 轮 P2）。带登录名的留待实拉后与 PR 作者比对（例外通道）。
func TestReviewCommentWakeUnqualifiedNoAPI(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 37, &githubapi.PullRequest{
		RepoFullName: repo, Number: 37, State: "open", HeadSHA: "sha-uq",
		HeadRepoFullName: repo,
	})

	before := e.fake.CallCounts()
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-uq",
		issueCommentBody("@fast-ship-bot", repo, 37, 42, setCommentAssociation("NONE"), dropCommentUser)); err != nil {
		t.Fatalf("无资格点名应静默忽略: %v", err)
	}
	after := e.fake.CallCounts()
	if after["GetPullRequest"] != before["GetPullRequest"] {
		t.Fatalf("无资格点名不得实拉，GetPullRequest %d -> %d", before["GetPullRequest"], after["GetPullRequest"])
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("无资格点名不得开轮，runs = %d", runs)
	}
}

func TestReviewCommentWakeSlugMention(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 38, &githubapi.PullRequest{
		RepoFullName: repo, Number: 38, State: "open", HeadSHA: "sha-slug",
		HeadRepoFullName: repo,
	})
	// 部署机器人句柄（App slug）也可唤醒：manifest 流创建的 App slug 形如
	// fast-ship-<host>，与规格字面量 @fast-ship-bot 不同（fast-ship-bot
	// 第 20 轮 P2）。
	e.review.appSource = stubAppSource{cfg: GitHubAppConfig{AppSlug: "Fast-Ship-Demo"}}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-slug",
		issueCommentBody("@fast-ship-demo 请再审", repo, 38, 42)); err != nil {
		t.Fatalf("slug mention: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 38").First(&run).Error; err != nil {
		t.Fatalf("slug 点名应唤醒: %v", err)
	}
	// 不含唤醒串的评论不唤醒。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-slug-x",
		issueCommentBody("@fast-ship-not-this", repo, 38, 42)); err != nil {
		t.Fatalf("无关字面量: %v", err)
	}
	var c int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 38").Count(&c)
	if c != 1 {
		t.Fatalf("不含唤醒串的评论不得开轮，runs = %d", c)
	}
}

// stubAppSource GitHubAppSource 桩：返回固定配置（slug 用例）。
type stubAppSource struct{ cfg GitHubAppConfig }

func (s stubAppSource) Current(context.Context) (GitHubAppConfig, bool, error) {
	return s.cfg, true, nil
}

// stubAppSourceErr GitHubAppSource 桩：读取恒报错（瞬时故障用例）。
type stubAppSourceErr struct{}

func (stubAppSourceErr) Current(context.Context) (GitHubAppConfig, bool, error) {
	return GitHubAppConfig{}, false, errors.New("injected app lookup failure")
}

// App slug 读取的瞬时故障按瞬时失败处理：评论可能用了 @<slug>，静默回落
// 字面量会让该唤醒被无声忽略（重投同样失败 → 永久丢失），透出 error 让
// 平台重投、不落账（codex 第 22 轮 P2）。
func TestReviewCommentWakeAppLookupError(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 39, &githubapi.PullRequest{
		RepoFullName: repo, Number: 39, State: "open", HeadSHA: "sha-apperr",
		HeadRepoFullName: repo,
	})
	e.review.appSource = stubAppSourceErr{}

	body := issueCommentBody("@fast-ship-bot", repo, 39, 42)
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-apperr", body); err == nil {
		t.Fatal("App 身份读取失败应透出 error（500，平台重投）")
	}
	var deliveries, runs int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).Count(&deliveries)
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 39").Count(&runs)
	if deliveries != 0 || runs != 0 {
		t.Fatalf("瞬时故障不落账: deliveries=%d runs=%d", deliveries, runs)
	}

	// 恢复后（换成可用桩）同 delivery 重投：完整重走并开轮。
	e.review.appSource = stubAppSource{cfg: GitHubAppConfig{AppSlug: "Fast-Ship-Demo"}}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-apperr", body); err != nil {
		t.Fatalf("恢复后重投应完整重走并开轮: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 39").First(&run).Error; err != nil {
		t.Fatalf("重投后应开出新轮: %v", err)
	}
}

// TestReviewCommentWakeUnboundRepoNoAPI 未绑定仓库零联网放大：点名评论
// 落在没有任何项目启用审查的仓库上时，不写幂等账、不做任何平台 API 调用
// （installation 解析与实拉都不会发生，fast-ship-bot 第 16 轮 P2）。
func TestReviewCommentWakeUnboundRepoNoAPI(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	ctx := context.Background()
	// 其他仓库有启用项目（证明反查按仓库分区），但 stranger/other 无绑定。
	e.fake.SetPR("stranger/other", 99, &githubapi.PullRequest{
		RepoFullName: "stranger/other", Number: 99, State: "open",
		HeadSHA: "sha-x", HeadRepoFullName: "stranger/other",
	})

	before := e.fake.CallCounts()
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-unbound",
		issueCommentBody("@fast-ship-bot", "stranger/other", 99, 42)); err != nil {
		t.Fatalf("未绑定仓库应静默忽略: %v", err)
	}
	after := e.fake.CallCounts()
	if after["GetPullRequest"] != before["GetPullRequest"] {
		t.Fatalf("未绑定仓库不得触发实拉 API，GetPullRequest %d -> %d", before["GetPullRequest"], after["GetPullRequest"])
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).Count(&deliveries)
	if deliveries != 0 {
		t.Fatalf("未绑定仓库不得写幂等账，实得 %d 行", deliveries)
	}
}

// TestReviewCommentWakeTrustsLiveHead head 采信语义：账本 head 因丢投递落
// 后于平台（账本 A、实拉 B）不再锁死唤醒——观察窗口内总账未被推进时采信
// 实拉 head 开轮，并把总账推进到 B。
func TestReviewCommentWakeTrustsLiveHead(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 28, &githubapi.PullRequest{
		RepoFullName: repo, Number: 28, State: "open", HeadSHA: "sha-live",
		HeadRepoFullName: repo,
	})
	// 已受理 head 是更早的 SHA（总账残留：投递丢了，推送从未被受理）。
	now := time.Now().UTC()
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     e.project.ID, UserID: e.user.ID,
		RepoFullName: strings.ToLower(repo), PRNumber: 28, HeadSHA: "sha-accepted",
		UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-trust",
		issueCommentBody("@fast-ship-bot", repo, 28, 42)); err != nil {
		t.Fatalf("trust live head: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("repo_full_name = ? AND pr_number = 28", strings.ToLower(repo)).First(&run).Error; err != nil {
		t.Fatalf("账本落后时评论应仍开轮: %v", err)
	}
	if run.TriggerAction != "comment" || run.HeadSHA != "sha-live" {
		t.Fatalf("run = action=%s head=%s, want comment/sha-live", run.TriggerAction, run.HeadSHA)
	}
	// openRoundTx 把总账推进到实拉 head——落后的 A 不再挡后续判定。
	var ledger model.ReviewPullRequest
	if err := e.svc.db.Where("project_id = ? AND pr_number = 28", e.project.ID).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ledger.HeadSHA != "sha-live" {
		t.Fatalf("总账 head = %s, want sha-live（采信实拉并推进）", ledger.HeadSHA)
	}
}

// seedOrAdvanceCommentWakeLedger 测试内模拟并发写总账：行存在则推进 head，
// 否则插入（评论唤醒的观察窗口竞态注入点）。
func seedOrAdvanceCommentWakeLedger(t *testing.T, e *reviewEngineEnv, repo string, pr int, head string) {
	t.Helper()
	var row model.ReviewPullRequest
	err := e.svc.db.Where("project_id = ? AND repo_full_name = ? AND pr_number = ? AND integration_id = ?",
		e.project.ID, strings.ToLower(repo), pr, model.ForgeIntegrationBuiltinGitHubID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := e.svc.db.Create(&model.ReviewPullRequest{
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
			ProjectID:     e.project.ID, UserID: e.user.ID,
			RepoFullName: strings.ToLower(repo), PRNumber: pr,
			HeadSHA: head, UpdatedAt: time.Now().UTC(),
		}).Error; err != nil {
			t.Fatalf("seed ledger: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("load ledger: %v", err)
	}
	if err := e.svc.db.Model(&row).Update("head_sha", head).Error; err != nil {
		t.Fatalf("advance ledger: %v", err)
	}
}

// TestReviewCommentWakeObservedWindowRace 观察窗口复核：实拉时总账被并发
// 推进（观察值与事务内重取不一致）→ 哨兵回滚不烧幂等账，外层重读重拉后
// 采信新观察值开轮——恰好两次实拉，幂等账随开轮落定。
func TestReviewCommentWakeObservedWindowRace(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 41, &githubapi.PullRequest{
		RepoFullName: repo, Number: 41, State: "open", HeadSHA: "sha-race-live",
		HeadRepoFullName: repo,
	})
	// 第一次实拉时抢先推进总账（模拟并发 webhook 受理新 head）：第一次的
	// 观察值（空）与事务内重取不一致 → 重试；第二次观察值已同步，放行。
	e.fake.SetPRHook(func(call int) {
		if call == 1 {
			seedOrAdvanceCommentWakeLedger(t, e, repo, 41, "sha-race-accepted")
		}
	})

	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-race",
		issueCommentBody("@fast-ship-bot", repo, 41, 42)); err != nil {
		t.Fatalf("race retry: %v", err)
	}
	e.fake.SetPRHook(nil)
	if got := e.fake.CallCounts()["GetPullRequest"]; got != 2 {
		t.Fatalf("GetPullRequest 调用 = %d, want 2（窗口竞态后恰好重试一次）", got)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 41").First(&run).Error; err != nil {
		t.Fatalf("重试后应开轮: %v", err)
	}
	if run.TriggerAction != "comment" || run.HeadSHA != "sha-race-live" {
		t.Fatalf("run = action=%s head=%s", run.TriggerAction, run.HeadSHA)
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-race")).
		Count(&deliveries)
	if deliveries != 1 {
		t.Fatalf("开轮后幂等账应落定，实得 %d", deliveries)
	}
}

// TestReviewCommentWakeObservedWindowGiveUp 观察窗口不收敛：每次实拉时总账
// 都被并发推进（两次尝试观察值都不一致）→ 放弃且**不登记**（fast-ship-bot
// 第 52 轮 P2：烧账+静默丢弃会永久吞掉合法唤醒），不开轮；后续投递/手动
// 重投可完整重走。
func TestReviewCommentWakeObservedWindowGiveUp(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 42, &githubapi.PullRequest{
		RepoFullName: repo, Number: 42, State: "open", HeadSHA: "sha-giveup-live",
		HeadRepoFullName: repo,
	})
	// 每次实拉都把总账推进到新值：两次尝试的观察值都不收敛。
	e.fake.SetPRHook(func(call int) {
		seedOrAdvanceCommentWakeLedger(t, e, repo, 42, fmt.Sprintf("sha-flutter-%d", call))
	})

	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-giveup",
		issueCommentBody("@fast-ship-bot", repo, 42, 42)); err != nil {
		t.Fatalf("不收敛应判丢弃返回 nil，实得 %v", err)
	}
	e.fake.SetPRHook(nil)
	if got := e.fake.CallCounts()["GetPullRequest"]; got != reviewCommentWakeRevalidateMax {
		t.Fatalf("GetPullRequest 调用 = %d, want %d（上限后放弃）", got, reviewCommentWakeRevalidateMax)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("未收敛不得开轮，runs = %d", runs)
	}
	// 放弃路径不登记幂等账：同投递重走可完整重走（新鲜观察+实拉）。
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-giveup")).
		Count(&deliveries)
	if deliveries != 0 {
		t.Fatalf("放弃路径不得登记幂等账，实得 %d", deliveries)
	}
	callsBefore := e.fake.CallCounts()["GetPullRequest"]
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-giveup",
		issueCommentBody("@fast-ship-bot", repo, 42, 42)); err != nil {
		t.Fatalf("同投递重走应完整重走不报错: %v", err)
	}
	if got := e.fake.CallCounts()["GetPullRequest"]; got <= callsBefore {
		t.Fatalf("重走应重新实拉: %d -> %d", callsBefore, got)
	}
}

// TestReviewCommentWakeRevalidatePhaseC 阶段二复核：取 PR 与开轮之间设置行
// 被停用/改绑仓库时不开轮（经 GetPullRequest 的钩子在两阶段之间改库）。每个
// 场景独立环境——前一场景的停用不得掩盖后一场景的改绑守卫。
func TestReviewCommentWakeRevalidatePhaseC(t *testing.T) {
	for i, tc := range []struct {
		name   string
		mutate func(*testing.T, *reviewEngineEnv)
	}{
		{
			// 停用审查。
			name: "disable",
			mutate: func(t *testing.T, e *reviewEngineEnv) {
				if err := e.svc.db.Model(&model.ProjectReviewSettings{}).
					Where("project_id = ?", e.project.ID).Update("enabled", false).Error; err != nil {
					t.Fatalf("disable: %v", err)
				}
			},
		},
		{
			// 改绑仓库（快照变更）。
			name: "rebind",
			mutate: func(t *testing.T, e *reviewEngineEnv) {
				if err := e.svc.db.Model(&model.ProjectReviewSettings{}).
					Where("project_id = ?", e.project.ID).Update("repo_full_name", "moved/repo").Error; err != nil {
					t.Fatalf("rebind: %v", err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newReviewEngineEnv(t)
			e.enableReview(t)
			repo := e.project.GithubOwner + "/" + e.project.GithubRepo
			e.fake.SetPR(repo, 29, &githubapi.PullRequest{
				RepoFullName: repo, Number: 29, State: "open", HeadSHA: "sha-pc",
				HeadRepoFullName: repo,
			})
			// 实拉返回时再改库：模拟实拉与最终事务开轮之间用户操作。
			e.fake.SetPRHook(func(int) { tc.mutate(t, e) })
			if err := e.review.HandleGitHubWebhook(context.Background(), "issue_comment",
				fmt.Sprintf("d-pc%d", i), issueCommentBody("@fast-ship-bot", repo, 29, 42)); err != nil {
				t.Fatalf("case %s: %v", tc.name, err)
			}
			e.fake.SetPRHook(nil)
			var runs int64
			e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
			if runs != 0 {
				t.Fatalf("case %s: 复核不过不得开轮，runs = %d", tc.name, runs)
			}
		})
	}
}

// TestReviewCommentWakeDeterministicDrop 实拉确定性失败（PR 已删 404）判
// 丢弃：返回 nil 且幂等账落库（重投 202 去重）；瞬时错误返回 error（handler
// 500，平台重投）且库中无幂等账——重投可完整重走。
func TestReviewCommentWakeDeterministicDrop(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()

	// 确定性失败：404。
	stub := pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		model.ForgeIntegrationBuiltinGitHubID: &pollStubClient{
			liveErr: &forge.StatusError{StatusCode: 404, Method: "GET", URL: "pr"},
		},
	}}
	e.review.clients = stub
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-drop",
		issueCommentBody("@fast-ship-bot", repo, 31, 42)); err != nil {
		t.Fatalf("确定性失败应判丢弃返回 nil，实得 %v", err)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("丢弃路径不得开轮，runs = %d", runs)
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-drop")).
		Count(&deliveries)
	if deliveries != 1 {
		t.Fatalf("丢弃路径幂等账应已登记，实得 %d", deliveries)
	}

	// 瞬时错误：返回 error 让 handler 回 500。
	e.review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		model.ForgeIntegrationBuiltinGitHubID: &pollStubClient{
			liveErr: errors.New("network timeout"),
		},
	}}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-transient",
		issueCommentBody("@fast-ship-bot", repo, 31, 42)); err == nil {
		t.Fatal("瞬时错误应返回 error（500，平台重投）")
	}
	// 瞬时错误不得烧幂等账：库里无登记，同 delivery 重投可重走全流程。
	var transientLedger int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-transient")).
		Count(&transientLedger)
	if transientLedger != 0 {
		t.Fatalf("瞬时错误不得登记幂等账，实得 %d 行", transientLedger)
	}
}

// TestReviewCommentWakeTransientRedeliveryReopens 瞬时错误后同 delivery 重投
// 可重走（本次修复的核心回归）：第一次实拉遇瞬时错误返回 error 且库中无任
// 何登记，随后客户端恢复、同一 delivery id 重投 → 完整重走并正常开轮。
// 旧实现阶段 A 单独提交幂等账，瞬时错误后重投被 202 去重，唤醒永久吞掉。
func TestReviewCommentWakeTransientRedeliveryReopens(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()

	stub := &pollStubClient{liveErr: errors.New("network timeout")}
	e.review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		model.ForgeIntegrationBuiltinGitHubID: stub,
	}}
	body := issueCommentBody("@fast-ship-bot 再审一轮", repo, 33, 42)

	// 第一次投递：实拉瞬时错误 → 返回 error，库中无幂等账。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-retry", body); err == nil {
		t.Fatal("瞬时错误应返回 error（500，平台重投）")
	}
	var ledgerRows int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-retry")).
		Count(&ledgerRows)
	if ledgerRows != 0 {
		t.Fatalf("瞬时错误后库中不得有幂等账，实得 %d 行", ledgerRows)
	}

	// 同 delivery 重投：客户端恢复 → 完整重走并开轮（action=comment）。
	stub.liveErr = nil
	stub.live = &forge.PullRequest{RepoFullName: repo, Number: 33, State: "open",
		HeadSHA: "sha-retry", HeadRepoFullName: repo,
		Title: "retry wake", HTMLURL: "https://github.com/" + repo + "/pull/33",
		BaseRef: "main", HeadRef: "feature"}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-retry", body); err != nil {
		t.Fatalf("重投应完整重走并开轮: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 33").First(&run).Error; err != nil {
		t.Fatalf("重投后应开出新轮: %v", err)
	}
	if run.TriggerAction != "comment" || run.HeadSHA != "sha-retry" {
		t.Fatalf("run = action=%s head=%s", run.TriggerAction, run.HeadSHA)
	}
	// 开轮成功后幂等账落定，再投同 delivery 按 202 去重。
	ledgerRows = 0
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-retry")).
		Count(&ledgerRows)
	if ledgerRows != 1 {
		t.Fatalf("开轮后幂等账应落定，实得 %d 行", ledgerRows)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-retry", body); !errors.Is(err, ErrDeliveryDuplicated) {
		t.Fatalf("三投应按幂等去重，实得 %v", err)
	}
}

// TestReviewCommentWakeTxRollbackRetainsRedelivery 最终事务失败不烧幂等账：
// 开轮中途失败（组合 JSON 损坏 → openRoundTx 解析报错）→ 整体回滚，库中既
// 无运行也无幂等账，同一 delivery 修复后重投可再走全流程。
func TestReviewCommentWakeTxRollbackRetainsRedelivery(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 34, &githubapi.PullRequest{
		RepoFullName: repo, Number: 34, State: "open", HeadSHA: "sha-tx",
		HeadRepoFullName: repo,
		Title:            "tx rollback", HTMLURL: "https://github.com/" + repo + "/pull/34",
		BaseRef: "main", HeadRef: "feature",
	})

	// 实拉返回时损坏组合 JSON：幂等登记之后 openRoundTx 解析失败 → 回滚。
	e.fake.SetPRHook(func(int) {
		if err := e.svc.db.Model(&model.ProjectReviewSettings{}).
			Where("project_id = ?", e.project.ID).
			Update("code_combinations_json", "{broken").Error; err != nil {
			t.Errorf("损坏组合 JSON: %v", err)
		}
	})
	body := issueCommentBody("@fast-ship-bot", repo, 34, 42)
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-txrb", body); err == nil {
		t.Fatal("开轮失败应返回 error（500，平台重投）")
	}
	e.fake.SetPRHook(nil)

	// 回滚验证：无运行、无账本推进、无幂等账——三样都必须不存在。
	var runs, ledger, deliveries int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 34").Count(&runs)
	e.svc.db.Model(&model.ReviewPullRequest{}).Where("pr_number = 34").Count(&ledger)
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-txrb")).
		Count(&deliveries)
	if runs != 0 || ledger != 0 || deliveries != 0 {
		t.Fatalf("事务失败应整体回滚: runs=%d ledger=%d deliveries=%d", runs, ledger, deliveries)
	}

	// 修复后同 delivery 重投：可再走全流程并开轮。
	restored, err := marshalCombinations([]api.ReviewCombination{testCombo("env-1")})
	if err != nil {
		t.Fatalf("marshal combos: %v", err)
	}
	if err := e.svc.db.Model(&model.ProjectReviewSettings{}).
		Where("project_id = ?", e.project.ID).
		Update("code_combinations_json", restored).Error; err != nil {
		t.Fatalf("恢复组合 JSON: %v", err)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-txrb", body); err != nil {
		t.Fatalf("重投应完整重走并开轮: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 34").First(&run).Error; err != nil {
		t.Fatalf("重投后应开出新轮: %v", err)
	}
	if run.TriggerAction != "comment" || run.HeadSHA != "sha-tx" {
		t.Fatalf("run = action=%s head=%s", run.TriggerAction, run.HeadSHA)
	}
}

// 时序基准（seen 行）查询的瞬时 DB 故障不得当作「无 seen 行」放行——那
// 正是需要拦滞后快照的时刻；非 NotFound 错误透出 500 重试，不落账
// （codex 第 22 轮 P2）。
func TestReviewCommentWakeStaleLookupError(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	now := time.Now().UTC()
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 41,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-a", RoundNumber: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	e.fake.SetPR(repo, 41, &githubapi.PullRequest{
		RepoFullName: repo, Number: 41, State: "open", HeadSHA: "sha-b",
		HeadRepoFullName: repo, UpdatedAt: &now,
	})

	failSeen := false
	if err := e.svc.db.Callback().Query().Before("gorm:query").Register("int179_fail_seen_select",
		func(tx *gorm.DB) {
			if failSeen && tx.Statement.Table == "review_seen_shas" {
				tx.AddError(errors.New("injected seen query failure"))
			}
		}); err != nil {
		t.Fatalf("注册回调失败: %v", err)
	}
	failSeen = true
	body := issueCommentBody("@fast-ship-bot", repo, 41, 42)
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-seenerr", body); err == nil {
		t.Fatal("时序基准查询失败应透出 error（500，平台重投）")
	}
	var deliveries, runs int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).Count(&deliveries)
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 41").Count(&runs)
	if deliveries != 0 || runs != 0 {
		t.Fatalf("瞬时故障不落账: deliveries=%d runs=%d", deliveries, runs)
	}

	// 恢复后同 delivery 重投：完整重走并开轮。
	failSeen = false
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-seenerr", body); err != nil {
		t.Fatalf("恢复后重投应完整重走并开轮: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 41").First(&run).Error; err != nil {
		t.Fatalf("重投后应开出新轮: %v", err)
	}
}

// TestReviewCommentWakeSettingsQueryError 设置行复核的库错误必须透出：
// 只有 ErrRecordNotFound（审查被移除）才是已确定的丢弃；其余查询失败要让
// 事务整体回滚、不烧幂等账，平台重投可完整重走（codex review P2 修复面）。
func TestReviewCommentWakeSettingsQueryError(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()

	e.fake.SetPR(repo, 35, &githubapi.PullRequest{
		RepoFullName: repo, Number: 35, State: "open", HeadSHA: "sha-dberr",
		HeadRepoFullName: repo,
		Title:            "db err", HTMLURL: "https://github.com/" + repo + "/pull/35",
		BaseRef: "main", HeadRef: "feature",
	})

	// 只拦设置行复核那条带 Select 列的查询（反查启用项目的联查无 Select
	// 列，不受影响）；在实拉 hook 里置位，保证失败发生在最终事务内。
	failSettings := false
	if err := e.svc.db.Callback().Query().Before("gorm:query").Register("int179_fail_settings_select",
		func(tx *gorm.DB) {
			if failSettings && tx.Statement.Table == "project_review_settings" && len(tx.Statement.Selects) > 0 {
				tx.AddError(errors.New("injected settings query failure"))
			}
		}); err != nil {
		t.Fatalf("注册回调失败: %v", err)
	}
	e.fake.SetPRHook(func(int) { failSettings = true })

	body := issueCommentBody("@fast-ship-bot", repo, 35, 42)
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-dberr", body); err == nil {
		t.Fatal("设置行查询失败应透出 error（500，平台重投）")
	}
	var deliveries, runs int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).Count(&deliveries)
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 35").Count(&runs)
	if deliveries != 0 || runs != 0 {
		t.Fatalf("库错误应整体回滚（不烧幂等账）: deliveries=%d runs=%d", deliveries, runs)
	}

	// 恢复后同 delivery 重投：完整重走并开轮。
	failSettings = false
	e.fake.SetPRHook(nil)
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-dberr", body); err != nil {
		t.Fatalf("恢复后重投应完整重走并开轮: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 35").First(&run).Error; err != nil {
		t.Fatalf("重投后应开出新轮: %v", err)
	}
	if run.TriggerAction != "comment" {
		t.Fatalf("trigger_action = %s", run.TriggerAction)
	}
}

// TestReviewCommentWakeForgejoIntegration Forgejo 路径：按集成 id 反查启用
// 项目、无安装语义（installation=0）、评论恒开新轮；GitHub 入口对绑定
// Forgejo 的项目不误触（设置行快照分区）。
func TestReviewCommentWakeForgejoIntegration(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-wake", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)
	// 单拉复核返回 open PR（list 不在评论唤醒路径，桩只喂 live）。
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: &pollStubClient{
			byRepo: map[string][]forge.PullRequest{},
			live: &forge.PullRequest{RepoFullName: "acme/app", Number: 61, State: "open",
				HeadSHA: "sha-fj", HeadRepoFullName: "acme/app",
				Title: "t", HTMLURL: "https://forge.example.com/acme/app/pulls/61",
				BaseRef: "main", HeadRef: "feat",
				UpdatedAt: ptrTime(time.Now().UTC())},
		},
		// GitHub 入口也能构造客户端（部署级 App 恒在）：实拉后由最终事务的
		// 反查按设置行快照分区拒绝，不误触 Forgejo 项目。
		model.ForgeIntegrationBuiltinGitHubID: &pollStubClient{},
	}}

	body := issueCommentBody("@fast-ship-bot 再审一次", "acme/app", 61, -1)
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "issue_comment", "d-fj", body); err != nil {
		t.Fatalf("forgejo wake: %v", err)
	}
	// Forgejo 的评论投递事件名随部署形态也可能是字面名 pull_request_comment，
	// 两个名字都进评论唤醒分支：换事件名再点名（无代码组合 → 无运行，
	// 不触发同 head 在途守卫），账本推进到第 2 轮。
	body2 := issueCommentBody("@fast-ship-bot 再看一眼", "acme/app", 61, -1)
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request_comment", "d-fj2", body2); err != nil {
		t.Fatalf("forgejo wake (pull_request_comment): %v", err)
	}
	// 空代码组合不产生 run 行；开轮以账本推进（sync_pending）为准。两次
	// 事件名都各自开轮：无运行行时轮次号都从 1 重算（1+max(empty)），
	// 断言两次投递都落了幂等账 + 账本最终 pending 在 sha-fj 上。
	var ledger model.ReviewPullRequest
	if err := svc.db.Where("project_id = ? AND integration_id = ? AND pr_number = ?",
		project.ID, forgeID, 61).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if !ledger.SyncPending || ledger.HeadSHA != "sha-fj" {
		t.Fatalf("账本未按 comment 开轮: pending=%v head=%s round=%d",
			ledger.SyncPending, ledger.HeadSHA, ledger.RoundNumber)
	}
	var fjDeliveries int64
	svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id IN ?", []string{deliveryLedgerKey(forgeID, "d-fj"), deliveryLedgerKey(forgeID, "d-fj2")}).
		Count(&fjDeliveries)
	if fjDeliveries != 2 {
		t.Fatalf("两个事件名的投递都应受理，幂等账 = %d 行", fjDeliveries)
	}
	if ledger.InstallationID != 0 {
		t.Fatalf("Forgejo 无安装语义, installation = %d", ledger.InstallationID)
	}

	// GitHub 入口同名仓库不误触 Forgejo 绑定的项目。
	if err := review.HandleGitHubWebhook(context.Background(), "issue_comment", "d-gh-cross",
		issueCommentBody("@fast-ship-bot", "acme/app", 61, 42)); err != nil {
		t.Fatalf("github cross: %v", err)
	}
	var count int64
	svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND integration_id = ?", project.ID, forgeID).Count(&count)
	if count != 1 {
		t.Fatalf("GitHub 投递不得误触 Forgejo 项目，账本 = %d 行", count)
	}
}

// TestReviewCommentWakeForkSkipped fork 守卫（F1）：跨仓 fork 的 PR 评论唤醒
// 不开轮（与 pull_request 触发面同一不变量），判定在最终事务内做出——幂等账
// 随事务落定，重投按 202 去重；已删 fork（实拉 head.repo 为空）同样不开轮。
func TestReviewCommentWakeForkSkipped(t *testing.T) {
	for i, headRepo := range []string{"someone-else/repo", ""} {
		e := newReviewEngineEnv(t)
		e.enableReview(t)
		repo := e.project.GithubOwner + "/" + e.project.GithubRepo
		ctx := context.Background()
		e.fake.SetPR(repo, 51, &githubapi.PullRequest{
			RepoFullName: repo, Number: 51, State: "open", HeadSHA: "sha-fork",
		})
		// SetPR 把空 head 仓库默认成 base 仓库（本地自测语义）；fork/已删
		// fork 用覆盖口显式指明。
		e.fake.SetPRHeadRepo(repo, 51, headRepo)

		if err := e.review.HandleGitHubWebhook(ctx, "issue_comment",
			fmt.Sprintf("d-fork%d", i), issueCommentBody("@fast-ship-bot", repo, 51, 42)); err != nil {
			t.Fatalf("case %d head=%q: fork 评论应静默忽略，实得 %v", i, headRepo, err)
		}
		var runs int64
		e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
		if runs != 0 {
			t.Fatalf("case %d: fork PR 不得开轮，runs = %d", i, runs)
		}
		// 判定在最终事务内做出：幂等账随事务提交落定。
		var deliveries int64
		e.svc.db.Model(&model.GitHubWebhookDelivery{}).
			Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, fmt.Sprintf("d-fork%d", i))).
			Count(&deliveries)
		if deliveries != 1 {
			t.Fatalf("case %d: fork 判丢弃的幂等账应落定，实得 %d", i, deliveries)
		}
		if err := e.review.HandleGitHubWebhook(ctx, "issue_comment",
			fmt.Sprintf("d-fork%d", i), issueCommentBody("@fast-ship-bot", repo, 51, 42)); !errors.Is(err, ErrDeliveryDuplicated) {
			t.Fatalf("case %d: 重投应按幂等去重，实得 %v", i, err)
		}
	}
}

// TestRerunReviewForkRejected fork 守卫（F1）：跨仓 fork 的 PR 不能手动重跑
// 审查（与评论唤醒、webhook 触发面同一不变量），文案与「PR 已关闭」并列。
func TestRerunReviewForkRejected(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	e.fake.SetPR(repo, 52, &githubapi.PullRequest{
		RepoFullName: repo, Number: 52, State: "open", HeadSHA: "sha-rerun-fork",
		HeadRepoFullName: "someone-else/" + strings.SplitN(repo, "/", 2)[1],
		Title:            "t", HTMLURL: "u", BaseRef: "main", HeadRef: "feature",
	})

	_, err := e.review.RerunReview(context.Background(), e.user.ID, e.project.ID, 52, "")
	if err == nil || !strings.Contains(err.Error(), "fork PR 不能重跑审查") {
		t.Fatalf("rerun err = %v, want 含「fork PR 不能重跑审查」", err)
	}
	assertErrCode(t, err, 40001)
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("fork PR 重跑不得开轮，runs = %d", runs)
	}
}

// TestReviewCommentWakeInstallationResolveFails installation 兜底解析的错误
// 分类（F5）：确定性失败（访问面未配置）判丢弃并补登幂等账；瞬时错误返回
// error（500，平台重投）且不落任何账。
func TestReviewCommentWakeInstallationResolveFails(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 53, &githubapi.PullRequest{
		RepoFullName: repo, Number: 53, State: "open", HeadSHA: "sha-inst",
		HeadRepoFullName: repo,
	})
	// 投递缺 installation 快照（payload 无 installation 字段）才走兜底解析。
	body := issueCommentBody("@fast-ship-bot", repo, 53, -1)

	// 确定性失败：访问面未配置（gh==nil）——重投只会得到同一结果。
	e.review.gh = nil
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-inst-1", body); err != nil {
		t.Fatalf("访问面未配置应判丢弃返回 nil，实得 %v", err)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("丢弃路径不得开轮，runs = %d", runs)
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-inst-1")).
		Count(&deliveries)
	if deliveries != 1 {
		t.Fatalf("确定性解析失败幂等账应落定，实得 %d", deliveries)
	}

	// 瞬时失败：fake 未配置安装 id，解析回非状态错误——透出 500 且不落账。
	e2 := newReviewEngineEnv(t)
	e2.enableReview(t)
	e2.fake.SetPR(repo, 53, &githubapi.PullRequest{
		RepoFullName: repo, Number: 53, State: "open", HeadSHA: "sha-inst",
		HeadRepoFullName: repo,
	})
	e2.fake.InstallationID = 0
	if err := e2.review.HandleGitHubWebhook(ctx, "issue_comment", "d-inst-2", body); err == nil {
		t.Fatal("瞬时解析失败应返回 error（500，平台重投）")
	}
	var deliveries2 int64
	e2.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-inst-2")).
		Count(&deliveries2)
	if deliveries2 != 0 {
		t.Fatalf("瞬时失败不得登记幂等账，实得 %d 行", deliveries2)
	}
}

// stubGHClientFor 构造 ClientFor 可注入错误的 gh 访问面（-fast-ship-bot
// 第 1 轮 P2：ClientFor 链路的 ErrInternal 是瞬时 DB 故障，不得判丢弃）。
type stubGHClientFor struct{ err error }

func (s stubGHClientFor) ClientFor(context.Context) (githubapi.GitHubClient, error) {
	return nil, s.err
}

// errClientResolver 审查客户端解析桩：ClientFor 直接返回指定错误。
type errClientResolver struct{ err error }

func (r errClientResolver) ClientFor(context.Context, string, string, int64) (forge.ReviewClient, error) {
	return nil, r.err
}

// TestReviewCommentWakeClientForTransientErr ClientFor 返回瞬时 AppError
// （如部署设置读取失败的 ErrInternal）时透出 500 不落账；只有访问面未
// 配置的哨兵才按确定性丢弃补登幂等账。
func TestReviewCommentWakeClientForTransientErr(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 54, &githubapi.PullRequest{
		RepoFullName: repo, Number: 54, State: "open", HeadSHA: "sha-cf",
		HeadRepoFullName: repo,
	})
	body := issueCommentBody("@fast-ship-bot", repo, 54, -1)

	origGH := e.review.gh
	e.review.gh = stubGHClientFor{err: errs.New(errs.ErrInternal.Code, "读取部署设置失败")}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-cf-1", body); err == nil {
		t.Fatal("ClientFor 瞬时 AppError 应透出 error（500，平台重投）")
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-cf-1")).
		Count(&deliveries)
	if deliveries != 0 {
		t.Fatalf("瞬时 AppError 不得烧幂等账，实得 %d 行", deliveries)
	}

	// 恢复后同 delivery 重投：完整重走并开轮。
	e.review.gh = origGH
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-cf-1", body); err != nil {
		t.Fatalf("恢复后重投应完整重走并开轮: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 54").First(&run).Error; err != nil {
		t.Fatalf("重投后应开出新轮: %v", err)
	}
}

// TestReviewCommentWakeFakeHeadRepoDefault fake 自测路径的 head 仓库默认：
// SetPR 未指明 HeadRepoFullName 时按同仓库处理，fork 守卫不在 fake 上恒
// 触发（fast-ship-bot 第 1 轮 P2：本地 simulate 永远无法自测唤醒）。
func TestReviewCommentWakeFakeHeadRepoDefault(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 56, &githubapi.PullRequest{
		RepoFullName: repo, Number: 56, State: "open", HeadSHA: "sha-fake",
		// 故意不设 HeadRepoFullName：验证 fake 默认按同仓库处理。
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-fake", issueCommentBody("@fast-ship-bot", repo, 56, 42)); err != nil {
		t.Fatalf("fake 默认同仓库应开轮: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 56").First(&run).Error; err != nil {
		t.Fatalf("应开出新轮: %v", err)
	}
}

// TestReviewCommentWakeConcurrentDuplicate 预查后的并发兜底（F7）：只读预查
// 通过后、最终事务前，同 delivery 的幂等行被并发写入（重投抢先生效）——
// 事务内 insert 判重兜底，返回 ErrDeliveryDuplicated，不开轮。
func TestReviewCommentWakeConcurrentDuplicate(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 54, &githubapi.PullRequest{
		RepoFullName: repo, Number: 54, State: "open", HeadSHA: "sha-dup-race",
		HeadRepoFullName: repo,
	})
	// 实拉时并发投递抢先把同一 delivery 登记进幂等账（预查已过，事务兜底）。
	e.fake.SetPRHook(func(int) {
		if err := e.svc.db.Create(&model.GitHubWebhookDelivery{
			DeliveryID: deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-dup-race"),
			Event:      "issue_comment",
			ReceivedAt: time.Now().UTC(),
		}).Error; err != nil {
			t.Errorf("seed concurrent delivery: %v", err)
		}
	})

	err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-dup-race",
		issueCommentBody("@fast-ship-bot", repo, 54, 42))
	e.fake.SetPRHook(nil)
	if !errors.Is(err, ErrDeliveryDuplicated) {
		t.Fatalf("并发重复应返回 ErrDeliveryDuplicated，实得 %v", err)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Count(&runs)
	if runs != 0 {
		t.Fatalf("并发重复不得开轮，runs = %d", runs)
	}
}

// payload 级作者预比对（codex 第 23 轮 P2）：PR 即 issue，issue.user.login
// 就是 PR 作者——有登录名的陌生人（非作者且非 OWNER）零实拉拒绝，不再花
// installation 级 API 预算；作者与属主例外照常唤醒。
func commentBodyWithUsers(body, repo string, number int, installationID int64,
	assoc, commentLogin, issueAuthor string) []byte {
	raw, err := json.Marshal(map[string]any{
		"action": "created",
		"issue": map[string]any{
			"number":       number,
			"pull_request": map[string]any{"html_url": "https://github.com/" + repo + "/pull/" + strconv.Itoa(number)},
			"user":         map[string]any{"login": issueAuthor},
		},
		"comment": map[string]any{
			"body": body, "author_association": assoc,
			"user": map[string]any{"login": commentLogin},
		},
		"repository":   map[string]any{"full_name": repo},
		"installation": map[string]any{"id": installationID},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func TestReviewCommentWakeStrangerPrefilterNoAPI(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 43, &githubapi.PullRequest{
		RepoFullName: repo, Number: 43, State: "open", HeadSHA: "sha-pf",
		HeadRepoFullName: repo, AuthorLogin: "pr-author",
	})
	callsBefore := e.fake.TotalCalls()

	// 陌生人（有登录名、非作者、非 OWNER）：零 API 拒绝——installation
	// 解析与实拉都不发生。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-pf-stranger",
		commentBodyWithUsers("@fast-ship-bot 请再审", repo, 43, 42, "NONE", "stranger", "pr-author")); err != nil {
		t.Fatalf("陌生人点名应零成本忽略: %v", err)
	}
	if got := e.fake.TotalCalls(); got != callsBefore {
		t.Fatalf("陌生人点名不得产生任何平台调用: %d -> %d", callsBefore, got)
	}

	// PR 作者本人：预比对通过，照常实拉开轮。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-pf-author",
		commentBodyWithUsers("@fast-ship-bot 请再审", repo, 43, 42, "NONE", "pr-author", "pr-author")); err != nil {
		t.Fatalf("作者点名应唤醒: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 43").First(&run).Error; err != nil {
		t.Fatalf("作者点名应开轮: %v", err)
	}

	// OWNER 例外（属主登录名与作者不同）：预比对不拦，实拉后 commentWakeAuthorized 放行。
	// 先把作者那轮置终态，绕开「同 head 在途守卫」（该守卫只拦在途轮）。
	if err := e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 43").
		Updates(map[string]any{"status": model.ReviewRunCompleted, "finished_at": time.Now().UTC()}).Error; err != nil {
		t.Fatalf("置终态: %v", err)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-pf-owner",
		commentBodyWithUsers("@fast-ship-bot 请再审", repo, 43, 42, "OWNER", "repo-owner", "pr-author")); err != nil {
		t.Fatalf("属主点名应唤醒: %v", err)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 43").Count(&runs)
	if runs != 2 {
		t.Fatalf("属主点名应再开一轮: runs = %d", runs)
	}
}

// 改绑窗口内的观察一致性（codex 第 23 轮 P2）：预查与观察是两次独立的
// 项目反查——改绑发生在两者之间时，滞后时序判定必须用「供给观察值的
// 那个项目」的 seen 行作基准，否则基准错位会让滞后快照绕过判定、把新
// 项目账本回退。回调在第二次设置行反查（观察阶段）时把绑定搬到项目 B。
func TestReviewCommentWakeStalenessUsesObservedProject(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()

	// 项目 B：绑定将搬到它名下，账本与 seen 都在 B。
	projectB := model.Project{ID: "p-int179-b", UserID: e.project.UserID, Name: "int179-b"}
	if err := e.svc.db.Create(&projectB).Error; err != nil {
		t.Fatalf("create project B: %v", err)
	}
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	t0 := t1.Add(-time.Hour)
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: projectB.ID, UserID: projectB.UserID,
		RepoFullName: repo, PRNumber: 44,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-a", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed B ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: projectB.ID, RepoFullName: repo, PRNumber: 44,
		HeadSHA: "sha-a", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed B seen: %v", err)
	}
	e.fake.SetPR(repo, 44, &githubapi.PullRequest{
		RepoFullName: repo, Number: 44, State: "open", HeadSHA: "sha-b",
		HeadRepoFullName: repo, UpdatedAt: &t0,
	})

	// 预查（第 1 次无 Select 设置行查询）返回 A；在观察阶段（第 2 次）把
	// 绑定搬到 B——窗口内改绑。
	settingsQueries := 0
	if err := e.svc.db.Callback().Query().Before("gorm:query").Register("int179_rebind_midwindow",
		func(tx *gorm.DB) {
			if tx.Statement.Table != "project_review_settings" || len(tx.Statement.Selects) > 0 {
				return
			}
			settingsQueries++
			if settingsQueries == 2 {
				if err := e.svc.db.Model(&model.ProjectReviewSettings{}).
					Where("project_id = ?", e.project.ID).
					Update("project_id", projectB.ID).Error; err != nil {
					t.Logf("rebind failed: %v", err)
				}
			}
		}); err != nil {
		t.Fatalf("注册回调失败: %v", err)
	}

	// 滞后快照（live t0 < B 的 seen 基准 t1）：必须用 B 的 seen 拦下（返回
	// error 500），而不是因基准错位（查 A 的 seen → 无行）放行开轮。
	body := issueCommentBody("@fast-ship-bot", repo, 44, 42)
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-rebind", body); err == nil {
		t.Fatal("改绑窗口内滞后快照应按瞬时失败拒绝（500）")
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 44").Count(&runs)
	if runs != 0 {
		t.Fatalf("滞后快照不得开轮: runs = %d", runs)
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).Count(&deliveries)
	if deliveries != 0 {
		t.Fatalf("瞬时失败不落账: deliveries = %d", deliveries)
	}
}

// observeAcceptedHead 返回供给观察值的项目：改绑后观察跟随新项目，调用
// 方（滞后时序判定）用同一项目 ID 查基准——契约测试钉住返回值配对。
func TestReviewCommentWakeObserveReturnsSupplyingProject(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 45,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-obs", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	head, projectID, err := e.review.observeAcceptedHead(ctx, model.ForgeIntegrationBuiltinGitHubID, repo, 45)
	if err != nil || head != "sha-obs" || projectID != e.project.ID {
		t.Fatalf("observe = (%q, %q, %v), want (sha-obs, %q, nil)", head, projectID, err, e.project.ID)
	}

	// 改绑到项目 B：观察跟随新绑定，返回值同步换项目。
	projectB := model.Project{ID: "p-int179-obs", UserID: e.project.UserID, Name: "int179-obs"}
	if err := e.svc.db.Create(&projectB).Error; err != nil {
		t.Fatalf("create project B: %v", err)
	}
	if err := e.svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ?", e.project.ID).
		Update("project_id", projectB.ID).Error; err != nil {
		t.Fatalf("move ledger: %v", err)
	}
	if err := e.svc.db.Model(&model.ProjectReviewSettings{}).
		Where("project_id = ?", e.project.ID).
		Update("project_id", projectB.ID).Error; err != nil {
		t.Fatalf("rebind settings: %v", err)
	}
	head, projectID, err = e.review.observeAcceptedHead(ctx, model.ForgeIntegrationBuiltinGitHubID, repo, 45)
	if err != nil || head != "sha-obs" || projectID != projectB.ID {
		t.Fatalf("rebind 后 observe = (%q, %q, %v), want (sha-obs, %q, nil)", head, projectID, err, projectB.ID)
	}
}

// fork 否定判定缓存（fast-ship-bot 第 26 轮 P2）：fork PR 作者本人点名
// 会通过 payload 预过滤（评论者=作者），每条评论都要花一次 installation
// 解析 + 一次实拉才被 fork 守卫拒绝——公开仓库上可无限放大 App API 成本。
// 命中过 fork 判定的 (repo, PR) 短时缓存后，重复点名零 API 成本拒绝。
func TestReviewCommentWakeForkDenyCache(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 52, &githubapi.PullRequest{
		RepoFullName: repo, Number: 52, State: "open", HeadSHA: "sha-fork2",
		HeadRepoFullName: "someone-else/repo", AuthorLogin: "fork-author",
	})

	// 第一条：走完整流程（安装解析 + 实拉），事务内 fork 守卫拒绝并落幂等账。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-fc-1",
		commentBodyWithUsers("@fast-ship-bot 请再审", repo, 52, 42, "NONE", "fork-author", "fork-author")); err != nil {
		t.Fatalf("fork 点名应静默忽略: %v", err)
	}
	afterFirst := e.fake.TotalCalls()

	// 第二条（不同投递）：缓存命中，零新增平台调用。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-fc-2",
		commentBodyWithUsers("@fast-ship-bot 请再审", repo, 52, 42, "NONE", "fork-author", "fork-author")); err != nil {
		t.Fatalf("缓存命中应静默忽略: %v", err)
	}
	if got := e.fake.TotalCalls(); got != afterFirst {
		t.Fatalf("缓存命中后不得产生平台调用: %d -> %d", afterFirst, got)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 52").Count(&runs)
	if runs != 0 {
		t.Fatalf("fork PR 不得开轮: runs = %d", runs)
	}

	// 缓存过期后恢复实拉判定：把该 PR 改回同仓后，下一条点名照常开轮。
	e.fake.SetPR(repo, 52, &githubapi.PullRequest{
		RepoFullName: repo, Number: 52, State: "open", HeadSHA: "sha-fork2",
		AuthorLogin: "fork-author",
	})
	e.review.forkDeny.Store(fmt.Sprintf("%s/%s#52", model.ForgeIntegrationBuiltinGitHubID, repo),
		time.Now().UTC().Add(-time.Minute))
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-fc-3",
		commentBodyWithUsers("@fast-ship-bot 请再审", repo, 52, 42, "NONE", "fork-author", "fork-author")); err != nil {
		t.Fatalf("缓存过期后同仓点名应唤醒: %v", err)
	}
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 52").Count(&runs)
	if runs != 1 {
		t.Fatalf("缓存过期后应照常开轮: runs = %d", runs)
	}
}

// 评论唤醒受理的 head 也要有滞后时序基准（fast-ship-bot 第 26 轮 P2）：
// 评论唤醒不写 seen，受理时把实拉快照时刻落进总账 PRUpdatedAt——同一
// head 的第二次点名若读到滞后快照，按瞬时失败拒绝而不是回退总账。
func TestReviewCommentWakeHeadBaselineAdvances(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	e.fake.SetPR(repo, 53, &githubapi.PullRequest{
		RepoFullName: repo, Number: 53, State: "open", HeadSHA: "sha-base",
		HeadRepoFullName: repo, AuthorLogin: "pr-author", UpdatedAt: &t1,
	})

	// 第一次点名：受理 head sha-base，总账 PRUpdatedAt = t1。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-base-1",
		commentBodyWithUsers("@fast-ship-bot 请再审", repo, 53, 42, "NONE", "pr-author", "pr-author")); err != nil {
		t.Fatalf("第一次点名应开轮: %v", err)
	}
	var ledger model.ReviewPullRequest
	if err := e.svc.db.Where("pr_number = 53").First(&ledger).Error; err != nil {
		t.Fatalf("总账行应存在: %v", err)
	}
	if ledger.PRUpdatedAt == nil || !ledger.PRUpdatedAt.Equal(t1) {
		t.Fatalf("总账应落实拉快照时刻: %v", ledger.PRUpdatedAt)
	}

	// 第二次点名读到滞后快照（同一 head、更早的 updated_at）：同 head 无
	// 从回退总账，照常采信开新轮（fast-ship-bot 第 33 轮 P2）；总账 head
	// 不变，基准随受理快照推进。
	t0 := t1.Add(-time.Hour)
	e.fake.SetPR(repo, 53, &githubapi.PullRequest{
		RepoFullName: repo, Number: 53, State: "open", HeadSHA: "sha-base",
		HeadRepoFullName: repo, AuthorLogin: "pr-author", UpdatedAt: &t0,
	})
	// 首轮仍在途：同 head 再点名被在途守卫跳过——关键是不得按滞后误拒（500）。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-base-2",
		commentBodyWithUsers("@fast-ship-bot 请再审", repo, 53, 42, "NONE", "pr-author", "pr-author")); err != nil {
		t.Fatalf("同 head 滞后时间戳应采信（在途守卫拦截而非 500）: %v", err)
	}
	var rounds int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 53").Count(&rounds)
	if rounds != 1 {
		t.Fatalf("在途守卫应拦截重复开轮: runs = %d", rounds)
	}
	var after model.ReviewPullRequest
	if err := e.svc.db.Where("pr_number = 53").First(&after).Error; err != nil {
		t.Fatalf("总账行应仍在: %v", err)
	}
	if after.HeadSHA != "sha-base" {
		t.Fatalf("总账 head 不得变化: %s", after.HeadSHA)
	}
	// 同 head 滞后快照是有意放行的，但账本基准不得被其更早时间戳往回拨
	// （fast-ship-bot 第 46 轮 P2，与 seen upsert 的 CASE 取最大同口径）。
	if after.PRUpdatedAt == nil || !after.PRUpdatedAt.Equal(t1) {
		t.Fatalf("账本基准不得回拨: %v", after.PRUpdatedAt)
	}
}

// 同刻异 head = 时序歧义（codex 第 28 轮 P2）：Forgejo 秒级精度下同一秒
// 两次推进，滞后读可能拿到旧 head 且 updated_at 与基准相等——严格 Before
// 拦不住，须与触发面同刻异 head 歧义同口径按瞬时失败重投。
func TestReviewCommentWakeTiedTimestampAmbiguity(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 46,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-a", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 46,
		HeadSHA: "sha-a", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed seen: %v", err)
	}
	// 同刻（t1）但 head 不同：滞后读到的旧 head 快照——该 head 曾受理过
	// （有 seen 行），复核判旧、拒绝。
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 46,
		HeadSHA: "sha-older", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed seen old: %v", err)
	}
	e.fake.SetPR(repo, 46, &githubapi.PullRequest{
		RepoFullName: repo, Number: 46, State: "open", HeadSHA: "sha-older",
		HeadRepoFullName: repo, UpdatedAt: &t1,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-tied",
		issueCommentBody("@fast-ship-bot", repo, 46, 42)); err == nil {
		t.Fatal("同刻异 head 的曾受理旧快照应按瞬时失败拒绝（500）")
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 46").Count(&runs)
	if runs != 0 {
		t.Fatalf("歧义快照不得开轮: runs = %d", runs)
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-tied")).
		Count(&deliveries)
	if deliveries != 0 {
		t.Fatalf("瞬时失败不落账: deliveries = %d", deliveries)
	}
}

// 同刻异 head 的另一种情况（fast-ship-bot 第 29 轮 P2）：账本因丢投递落
// 后、实拉读到同秒内的新 head——复核后采信开轮（「采信实拉 head」语义），
// 不再无条件重投把唤醒锁死。
func TestReviewCommentWakeTiedNewHeadAccepted(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 47,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-a", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 47,
		HeadSHA: "sha-a", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed seen: %v", err)
	}
	// 同刻（t1）新 head sha-b（从未受理）：复核一致且无 seen 行 → 采信开轮。
	e.fake.SetPR(repo, 47, &githubapi.PullRequest{
		RepoFullName: repo, Number: 47, State: "open", HeadSHA: "sha-b",
		HeadRepoFullName: repo, UpdatedAt: &t1,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-tied-new",
		issueCommentBody("@fast-ship-bot", repo, 47, 42)); err != nil {
		t.Fatalf("同秒新 head 应采信开轮: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 47").First(&run).Error; err != nil {
		t.Fatalf("应开出新轮: %v", err)
	}
	if run.HeadSHA != "sha-b" {
		t.Fatalf("新轮 head = %s, want sha-b", run.HeadSHA)
	}
}

// 永久配置错误（部署设置解密失败）回落字面量集合（fast-ship-bot 第 29 轮
// P2）：重投只会得到同一结果，500 会让全部平台的评论唤醒（含 Forgejo 与
// 普通讨论评论）卡死；字面量唤醒串不受 App 身份读取失败影响。
func TestReviewCommentWakeAppDecryptErrorFallback(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 48, &githubapi.PullRequest{
		RepoFullName: repo, Number: 48, State: "open", HeadSHA: "sha-decrypt",
		HeadRepoFullName: repo,
	})
	e.review.appSource = appSourceDecryptErr{}

	// 字面量点名照常唤醒（不 500）。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-dec-1",
		issueCommentBody("@fast-ship-bot", repo, 48, 42)); err != nil {
		t.Fatalf("解密失败应回落字面量而非卡死: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 48").First(&run).Error; err != nil {
		t.Fatalf("字面量点名应开轮: %v", err)
	}
	// 非唤醒评论照常静默忽略。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-dec-2",
		issueCommentBody("普通讨论", repo, 48, 42)); err != nil {
		t.Fatalf("非唤醒评论应忽略: %v", err)
	}
}

// appSourceDecryptErr GitHubAppSource 桩：读取恒报解密失败（永久配置错误）。
type appSourceDecryptErr struct{}

func (appSourceDecryptErr) Current(context.Context) (GitHubAppConfig, bool, error) {
	return GitHubAppConfig{}, false, errGitHubAppDecrypt
}

// 同刻异 head 的受理史要看运行行（codex 第 30 轮 P2）：评论唤醒不写
// seen——其受理过的 head 只有运行记录；滞后读同刻旧 head 时只查 seen
// 会误判成「同秒新 head」采信、把总账回退到旧 head。
func TestReviewCommentWakeTiedCommentAcceptedHeadRejected(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 54,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-b", RoundNumber: 2, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 54,
		HeadSHA: "sha-b", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed seen: %v", err)
	}
	// 更早的 head sha-a 由评论唤醒受理过：有运行行、无 seen 行。
	if err := e.svc.db.Create(&model.ReviewRun{
		ID: "run-tied-54", UserID: e.project.UserID, ProjectID: e.project.ID,
		Kind: model.ReviewKindCode, RepoFullName: repo, PRNumber: 54,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-a", RoundNumber: 1, Status: model.ReviewRunCompleted,
		TriggerAction: "comment",
	}).Error; err != nil {
		t.Fatalf("seed comment run: %v", err)
	}
	// 滞后读：同刻（t1）旧 head sha-a → 复核发现运行行 → 拒绝，不回退。
	e.fake.SetPR(repo, 54, &githubapi.PullRequest{
		RepoFullName: repo, Number: 54, State: "open", HeadSHA: "sha-a",
		HeadRepoFullName: repo, UpdatedAt: &t1,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-tied-run",
		issueCommentBody("@fast-ship-bot", repo, 54, 42)); err == nil {
		t.Fatal("同刻旧 head（评论唤醒受理过）应按瞬时失败拒绝")
	}
	var after model.ReviewPullRequest
	if err := e.svc.db.Where("pr_number = 54").First(&after).Error; err != nil {
		t.Fatalf("总账行应仍在: %v", err)
	}
	if after.HeadSHA != "sha-b" {
		t.Fatalf("总账不得回退: head = %s", after.HeadSHA)
	}
}

// 受理史判定按观察项目过滤（codex 第 30 轮 P2）：改绑后旧项目的 seen 行
// 不得错杀新项目的合法同刻异 head——观察与受理史判定用同一项目快照。
func TestReviewCommentWakeTiedHistoryScopedToObservedProject(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()

	projectB := model.Project{ID: "p-int179-tie", UserID: e.project.UserID, Name: "int179-tie"}
	if err := e.svc.db.Create(&projectB).Error; err != nil {
		t.Fatalf("create project B: %v", err)
	}
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	// B 的账本与基准（观察将来自 B）。
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: projectB.ID, UserID: projectB.UserID,
		RepoFullName: repo, PRNumber: 55,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-b", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed B ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: projectB.ID, RepoFullName: repo, PRNumber: 55,
		HeadSHA: "sha-b", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed B seen: %v", err)
	}
	// 旧项目 A 的 seen 行：sha-old 曾在 A 受理过——改绑到 B 后它不在 B 的
	// 受理史里，不得错杀。
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 55,
		HeadSHA: "sha-old", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed A seen: %v", err)
	}
	e.fake.SetPR(repo, 55, &githubapi.PullRequest{
		RepoFullName: repo, Number: 55, State: "open", HeadSHA: "sha-old",
		HeadRepoFullName: repo, UpdatedAt: &t1,
	})

	// 预查返回 A；观察阶段（第 2 次设置行反查）把绑定搬到 B——改绑窗口。
	settingsQueries := 0
	if err := e.svc.db.Callback().Query().Before("gorm:query").Register("int179_tie_rebind",
		func(tx *gorm.DB) {
			if tx.Statement.Table != "project_review_settings" || len(tx.Statement.Selects) > 0 {
				return
			}
			settingsQueries++
			if settingsQueries == 2 {
				if err := e.svc.db.Model(&model.ProjectReviewSettings{}).
					Where("project_id = ?", e.project.ID).
					Update("project_id", projectB.ID).Error; err != nil {
					t.Logf("rebind failed: %v", err)
				}
			}
		}); err != nil {
		t.Fatalf("注册回调失败: %v", err)
	}

	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-tie-scope",
		issueCommentBody("@fast-ship-bot", repo, 55, 42)); err != nil {
		t.Fatalf("B 的受理史不含 sha-old，应采信开轮而非拒绝: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 55").First(&run).Error; err != nil {
		t.Fatalf("应开出新轮: %v", err)
	}
	if run.ProjectID != projectB.ID || run.HeadSHA != "sha-old" {
		t.Fatalf("新轮应落在观察项目 B: project=%s head=%s", run.ProjectID, run.HeadSHA)
	}
}

// 复核快照本身也要过时序判定（codex 第 31 轮 P2）：tied 分支的确认读
// head 一致但 updated_at 早于基准 = 两次都是滞后快照，采信同样回退总账。
func TestReviewCommentWakeTiedRecheckStaleRejected(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	t0 := t1.Add(-time.Hour)
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 56,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-b", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 56,
		HeadSHA: "sha-b", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed seen: %v", err)
	}
	// 第一次读：同刻异 head sha-new（未受理，tied）；复核读：同 head 但
	// updated_at 更早（仍是滞后快照）→ 拒绝。
	e.fake.SetPR(repo, 56, &githubapi.PullRequest{
		RepoFullName: repo, Number: 56, State: "open", HeadSHA: "sha-new",
		HeadRepoFullName: repo, UpdatedAt: &t1,
	})
	calls := 0
	e.fake.SetPRHook(func(call int) {
		calls = call
		if call >= 2 {
			e.fake.SetPR(repo, 56, &githubapi.PullRequest{
				RepoFullName: repo, Number: 56, State: "open", HeadSHA: "sha-new",
				HeadRepoFullName: repo, UpdatedAt: &t0,
			})
		}
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-tied-stale2",
		issueCommentBody("@fast-ship-bot", repo, 56, 42)); err == nil {
		t.Fatal("复核快照仍滞后应按瞬时失败拒绝（500）")
	}
	if calls < 2 {
		t.Fatalf("应发生复核实拉: calls = %d", calls)
	}
	var after model.ReviewPullRequest
	if err := e.svc.db.Where("pr_number = 56").First(&after).Error; err != nil {
		t.Fatalf("总账行应仍在: %v", err)
	}
	if after.HeadSHA != "sha-b" {
		t.Fatalf("总账不得回退: head = %s", after.HeadSHA)
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-tied-stale2")).
		Count(&deliveries)
	if deliveries != 0 {
		t.Fatalf("瞬时失败不落账: deliveries = %d", deliveries)
	}
}

// 事务结果处置（fast-ship-bot 第 32 轮 P2）：forkDenied 与提交失败并存时
// 不得把提交错误吞成 202——幂等账已随回滚撤销，必须透出让平台重投。
func TestCommentWakeTxOutcome(t *testing.T) {
	txErr := errors.New("commit failure")

	// forkDenied + 提交失败：错误透出，不写否定缓存。
	deny, err := commentWakeTxOutcome(true, txErr)
	if err == nil || deny {
		t.Fatalf("提交失败必须透出: deny=%v err=%v", deny, err)
	}
	// 观察窗口哨兵原样透传（外层重读重拉，不写缓存）。
	deny, err = commentWakeTxOutcome(true, errCommentWakeObservedHeadMoved)
	if !errors.Is(err, errCommentWakeObservedHeadMoved) || deny {
		t.Fatalf("哨兵应透传: deny=%v err=%v", deny, err)
	}
	// fork 否定 + 事务成功：写缓存。
	deny, err = commentWakeTxOutcome(true, nil)
	if err != nil || !deny {
		t.Fatalf("fork 否定应写缓存: deny=%v err=%v", deny, err)
	}
	// 正常开轮。
	deny, err = commentWakeTxOutcome(false, nil)
	if err != nil || deny {
		t.Fatalf("正常开轮: deny=%v err=%v", deny, err)
	}
}

// 同 head 早时间戳不是滞后误杀（fast-ship-bot 第 33 轮 P2）：滞后判定的
// 目的是防总账回退到旧 head——实拉 head == 已受理 head 时无从回退，缓存/
// 只读副本滞后返回的同 head 快照照常采信开轮。
func TestReviewCommentWakeSameHeadLaggedTimestampAccepted(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	t0 := t1.Add(-time.Hour)
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 57,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-a", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 57,
		HeadSHA: "sha-a", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed seen: %v", err)
	}
	// 同一 head、更早的 updated_at（缓存滞后快照）。
	e.fake.SetPR(repo, 57, &githubapi.PullRequest{
		RepoFullName: repo, Number: 57, State: "open", HeadSHA: "sha-a",
		HeadRepoFullName: repo, UpdatedAt: &t0,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-same-head",
		issueCommentBody("@fast-ship-bot", repo, 57, 42)); err != nil {
		t.Fatalf("同 head 滞后时间戳应采信开轮: %v", err)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 57").Count(&runs)
	if runs != 1 {
		t.Fatalf("应开出新轮: runs = %d", runs)
	}
}

// 最终事务的项目身份复核（codex 第 35 轮 P2）：预查/观察与最终事务之间
// 改绑（A→B）且两项目账本 head 字符串相同时——观察基准（A 的 seen 行）
// 不可跨项目沿用，事务必须比对项目身份，不一致即哨兵重读重拉、在新绑定
// 下重新观察实拉。
func TestReviewCommentWakeTxBindsObservedProject(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()

	projectB := model.Project{ID: "p-int179-tx", UserID: e.project.UserID, Name: "int179-tx"}
	if err := e.svc.db.Create(&projectB).Error; err != nil {
		t.Fatalf("create project B: %v", err)
	}
	t1 := time.Now().UTC().Add(-3 * time.Hour)
	t2 := t1.Add(time.Hour)
	// A：观察将来自它（head sha-x，基准 t1）。
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 58,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-x", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed A ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 58,
		HeadSHA: "sha-x", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed A seen: %v", err)
	}
	// B：同名 head（字符串巧合），基准更新。
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: projectB.ID, UserID: projectB.UserID,
		RepoFullName: repo, PRNumber: 58,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-x", RoundNumber: 1, UpdatedAt: t2,
	}).Error; err != nil {
		t.Fatalf("seed B ledger: %v", err)
	}
	// 实拉快照：同 head、时间戳 t1（对 A 基准合法）。
	e.fake.SetPR(repo, 58, &githubapi.PullRequest{
		RepoFullName: repo, Number: 58, State: "open", HeadSHA: "sha-x",
		HeadRepoFullName: repo, UpdatedAt: &t1,
	})

	// 改绑发生在观察之后、最终事务之前（实拉钩子里执行——观察与事务之间
	// 的无锁窗口；在事务内的查询回调里做会与 sqlite 写锁死锁）。
	e.fake.SetPRHook(func(int) {
		if err := e.svc.db.Model(&model.ProjectReviewSettings{}).
			Where("project_id = ?", e.project.ID).
			Update("project_id", projectB.ID).Error; err != nil {
			t.Logf("rebind failed: %v", err)
		}
	})

	// 哨兵重读重拉后在新绑定（B）下收敛开轮。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-tx-bind",
		issueCommentBody("@fast-ship-bot", repo, 58, 42)); err != nil {
		t.Fatalf("哨兵重读后应在新绑定下收敛: %v", err)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 58").First(&run).Error; err != nil {
		t.Fatalf("应开出新轮: %v", err)
	}
	if run.ProjectID != projectB.ID {
		t.Fatalf("新轮应落在改绑后的项目 B: %s", run.ProjectID)
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-tx-bind")).
		Count(&deliveries)
	if deliveries != 1 {
		t.Fatalf("幂等账只应落一次: %d", deliveries)
	}
}

// 同刻异 head 复核的确定性失败（fast-ship-bot 第 40 轮 P2）：复核实拉失败
// 与首次实拉同口径分类——PR 已删等确定性失败判丢弃补登幂等账，不再按瞬
// 时失败刷满重投上限后发误导通知。
func TestReviewCommentWakeTiedRecheckDeterministic(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	t1 := time.Now().UTC().Add(-2 * time.Hour)
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 59,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-b", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 59,
		HeadSHA: "sha-b", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed seen: %v", err)
	}
	// 第一次读：同刻异 head（tied）；复核读：PR 已删（404，确定性失败）。
	e.fake.SetPR(repo, 59, &githubapi.PullRequest{
		RepoFullName: repo, Number: 59, State: "open", HeadSHA: "sha-new",
		HeadRepoFullName: repo, UpdatedAt: &t1,
	})
	e.fake.SetPRHook(func(call int) {
		if call >= 2 {
			e.fake.SetPRError(repo, &githubapi.StatusError{
				StatusCode: 404, Method: "GET", URL: "/repos/" + repo + "/pulls/59",
			})
		}
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-tied-gone",
		issueCommentBody("@fast-ship-bot", repo, 59, 42)); err != nil {
		t.Fatalf("复核确定性失败应判丢弃返回 nil: %v", err)
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-tied-gone")).
		Count(&deliveries)
	if deliveries != 1 {
		t.Fatalf("确定性丢弃应补登幂等账: %d", deliveries)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 59").Count(&runs)
	if runs != 0 {
		t.Fatalf("不得开轮: runs = %d", runs)
	}
}

// 守卫拆分（fast-ship-bot 第 40 轮 Note）：不依赖唤醒串的守卫先行——无关
// 投递（普通讨论评论）零 App 身份读取成本；唤醒投递照常走完整流程。
func TestReviewCommentWakeBaseGuardsBeforeAppLookup(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 60, &githubapi.PullRequest{
		RepoFullName: repo, Number: 60, State: "open", HeadSHA: "sha-base",
		HeadRepoFullName: repo,
	})
	e.review.appSource = stubAppSourceErr{} // App 身份读取恒失败

	// base 守卫可拒绝的投递（action/非 PR/机器标记）在 App 身份读取之前
	// 零成本拒绝——App 读取恒失败也不影响它们的忽略语义。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-base-edit",
		issueCommentBody("@fast-ship-bot 请再审", repo, 60, 42, func(m map[string]any) {
			m["action"] = "edited"
		})); err != nil {
		t.Fatalf("编辑评论应在 App 身份读取前被拒绝: %v", err)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-base-issue",
		issueCommentBody("@fast-ship-bot 请再审", repo, 60, 42, func(m map[string]any) {
			delete(m["issue"].(map[string]any), "pull_request")
		})); err != nil {
		t.Fatalf("普通 Issue 评论应在 App 身份读取前被拒绝: %v", err)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-base-bot",
		issueCommentBody("@fast-ship-bot 请再审", repo, 60, 42, func(m map[string]any) {
			m["comment"].(map[string]any)["user"] = map[string]any{"type": "Bot"}
		})); err != nil {
		t.Fatalf("机器自评论应在 App 身份读取前被拒绝: %v", err)
	}
	// 普通讨论评论（base 通过、需唤醒串判定）：App 读取失败按第 22 轮语义
	// 透出 500（slug-only 唤醒不得静默丢失），不落账。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-base-plain",
		issueCommentBody("普通讨论，不需要审查", repo, 60, 42)); err == nil {
		t.Fatal("唤醒串读取失败应透出（500）")
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 60").Count(&runs)
	if runs != 0 {
		t.Fatalf("无关/编辑投递不得开轮: runs = %d", runs)
	}
}

// 调度 kick 只随状态变化（codex 第 41 轮 P2）：忽略类投递（未点名）返回
// changed=false，成功开轮返回 true——调用方据此跳过无谓的调度 tick。
func TestReviewCommentWakeChangedFlag(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 61, &githubapi.PullRequest{
		RepoFullName: repo, Number: 61, State: "open", HeadSHA: "sha-kick",
		HeadRepoFullName: repo,
	})

	// 未点名的普通 PR 评论：忽略，changed=false。
	changed, err := e.review.handleCommentWake(ctx, model.ForgeIntegrationBuiltinGitHubID,
		"issue_comment", "d-kick-0", issueCommentBody("普通讨论", repo, 61, 42))
	if err != nil || changed {
		t.Fatalf("忽略投递应 (false, nil): changed=%v err=%v", changed, err)
	}
	// 点名唤醒：开出新轮，changed=true。
	changed, err = e.review.handleCommentWake(ctx, model.ForgeIntegrationBuiltinGitHubID,
		"issue_comment", "d-kick-1", issueCommentBody("@fast-ship-bot 请再审", repo, 61, 42))
	if err != nil || !changed {
		t.Fatalf("开轮投递应 (true, nil): changed=%v err=%v", changed, err)
	}
	// 同 head 再点名（在途守卫跳过、不开轮）：changed=false，不得误 kick。
	changed, err = e.review.handleCommentWake(ctx, model.ForgeIntegrationBuiltinGitHubID,
		"issue_comment", "d-kick-2", issueCommentBody("@fast-ship-bot 请再审", repo, 61, 42))
	if err != nil || changed {
		t.Fatalf("在途跳过应 (false, nil): changed=%v err=%v", changed, err)
	}
	// 重复投递：去重路径返回 ErrDeliveryDuplicated（调用方按错误处理，不 kick）。
	_, err = e.review.handleCommentWake(ctx, model.ForgeIntegrationBuiltinGitHubID,
		"issue_comment", "d-kick-1", issueCommentBody("@fast-ship-bot 请再审", repo, 61, 42))
	if !errors.Is(err, ErrDeliveryDuplicated) {
		t.Fatalf("重复投递应 ErrDeliveryDuplicated: %v", err)
	}
}

// 重新受理刷新 seen 时戳（codex 第 42 轮 P2）：force-push 回已见 head 的
// 评论唤醒受理后，seen 行的 pr_updated_at 刷新为最新快照——滞后基准若停
// 留旧值，中间时间戳的滞后读会绕过 stale 判定回退总账。
func TestReviewCommentWakeReacceptRefreshesSeen(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	t1 := time.Now().UTC().Add(-3 * time.Hour)
	t3 := t1.Add(2 * time.Hour)
	t2 := t1.Add(time.Hour)
	// sha-c 曾受理（seen/账本基准 t1）；现 force-push 回同一 SHA、快照更新到 t3。
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 62,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-c", RoundNumber: 1, UpdatedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 62,
		HeadSHA: "sha-c", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: &t1, AcceptedAt: t1,
	}).Error; err != nil {
		t.Fatalf("seed seen: %v", err)
	}
	e.fake.SetPR(repo, 62, &githubapi.PullRequest{
		RepoFullName: repo, Number: 62, State: "open", HeadSHA: "sha-c",
		HeadRepoFullName: repo, UpdatedAt: &t3,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-reaccept",
		issueCommentBody("@fast-ship-bot", repo, 62, 42)); err != nil {
		t.Fatalf("重受理应开轮: %v", err)
	}
	var seen model.ReviewSeenSHA
	if err := e.svc.db.Where("head_sha = ?", "sha-c").First(&seen).Error; err != nil {
		t.Fatalf("seen 行应存在: %v", err)
	}
	if seen.PRUpdatedAt == nil || !seen.PRUpdatedAt.Equal(t3) {
		t.Fatalf("seen 时戳应刷新为 t3: %v", seen.PRUpdatedAt)
	}

	// 之后的唤醒读到滞后快照：旧 head sha-old、时间戳 t2 ∈ (t1, t3)——
	// 刷新后的基准（t3）能拦下；停留在旧值（t1）时会被采信回退。
	var runCount int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 62 AND head_sha = ?", "sha-old").Count(&runCount)
	if runCount == 0 {
		if err := e.svc.db.Create(&model.ReviewRun{
			ID: "run-t62-old", UserID: e.project.UserID, ProjectID: e.project.ID,
			Kind: model.ReviewKindCode, RepoFullName: repo, PRNumber: 62,
			IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
			HeadSHA:       "sha-old", RoundNumber: 1, Status: model.ReviewRunCompleted,
			TriggerAction: "comment",
		}).Error; err != nil {
			t.Fatalf("seed old run: %v", err)
		}
	}
	e.fake.SetPR(repo, 62, &githubapi.PullRequest{
		RepoFullName: repo, Number: 62, State: "open", HeadSHA: "sha-old",
		HeadRepoFullName: repo, UpdatedAt: &t2,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-reaccept-lag",
		issueCommentBody("@fast-ship-bot", repo, 62, 42)); err == nil {
		t.Fatal("中间时间戳滞后读应按瞬时失败拒绝（500）")
	}
	var after model.ReviewPullRequest
	if err := e.svc.db.Where("pr_number = 62").First(&after).Error; err != nil {
		t.Fatalf("总账行应仍在: %v", err)
	}
	if after.HeadSHA != "sha-c" {
		t.Fatalf("总账不得回退: head = %s", after.HeadSHA)
	}

	// 同 head 分支有意放行的早时间戳（缓存/只读副本滞后）：不得把 seen 基
	// 准往回拨（fast-ship-bot 第 43 轮 P2）。
	t0 := t1.Add(-time.Hour)
	e.fake.SetPR(repo, 62, &githubapi.PullRequest{
		RepoFullName: repo, Number: 62, State: "open", HeadSHA: "sha-c",
		HeadRepoFullName: repo, UpdatedAt: &t0,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-reaccept-lag0",
		issueCommentBody("@fast-ship-bot", repo, 62, 42)); err != nil {
		t.Fatalf("同 head 早时间戳应采信: %v", err)
	}
	if err := e.svc.db.Where("head_sha = ?", "sha-c").First(&seen).Error; err != nil {
		t.Fatalf("seen 行应存在: %v", err)
	}
	if seen.PRUpdatedAt == nil || !seen.PRUpdatedAt.Equal(t3) {
		t.Fatalf("seen 基准不得往回拨: %v", seen.PRUpdatedAt)
	}

	// 缺时间戳的快照（适配器/平台漏字段）：不得把基准清成 NULL。
	e.fake.SetPR(repo, 62, &githubapi.PullRequest{
		RepoFullName: repo, Number: 62, State: "open", HeadSHA: "sha-c",
		HeadRepoFullName: repo,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-reaccept-nil",
		issueCommentBody("@fast-ship-bot", repo, 62, 42)); err != nil {
		t.Fatalf("缺时间戳快照应采信: %v", err)
	}
	if err := e.svc.db.Where("head_sha = ?", "sha-c").First(&seen).Error; err != nil {
		t.Fatalf("seen 行应存在: %v", err)
	}
	if seen.PRUpdatedAt == nil || !seen.PRUpdatedAt.Equal(t3) {
		t.Fatalf("seen 基准不得清空: %v", seen.PRUpdatedAt)
	}
}

// App 句柄短缓存（fast-ship-bot 第 44 轮 P2）：订阅 issue_comment 后所有
// 评论都进唤醒分支，逐条读部署设置（DB 读 + AES 解密）是按评论线性放大
// 的固定成本——短缓存把成本压到每 TTL 一次。
func TestReviewCommentWakeSlugCached(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 63, &githubapi.PullRequest{
		RepoFullName: repo, Number: 63, State: "open", HeadSHA: "sha-slug2",
		HeadRepoFullName: repo,
	})
	counter := &countingAppSource{cfg: GitHubAppConfig{AppSlug: "Fast-Ship-Demo"}}
	e.review.appSource = counter

	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-sc-1",
		issueCommentBody("@fast-ship-demo 请再审", repo, 63, 42)); err != nil {
		t.Fatalf("第一次点名: %v", err)
	}
	if counter.reads != 1 {
		t.Fatalf("第一次点名应发生一次读取: %d", counter.reads)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-sc-2",
		issueCommentBody("@fast-ship-demo 请再审", repo, 63, 42)); err != nil {
		t.Fatalf("第二次点名: %v", err)
	}
	if counter.reads != 1 {
		t.Fatalf("缓存生效后不得再读部署设置: reads = %d", counter.reads)
	}
	// 第二次点名走到唤醒判定（slug 由缓存供给）后被同 head 在途守卫跳过
	// （comment 恒开新轮只对不在途的 head）——缓存判别器是读取计数。
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 63").Count(&runs)
	if runs != 1 {
		t.Fatalf("第二轮应被同 head 在途守卫跳过: runs = %d", runs)
	}
}

// countingAppSource GitHubAppSource 桩：带读取计数（缓存用例）。
type countingAppSource struct {
	cfg   GitHubAppConfig
	mu    sync.Mutex
	reads int
}

func (c *countingAppSource) Current(context.Context) (GitHubAppConfig, bool, error) {
	c.mu.Lock()
	c.reads++
	cfg := c.cfg
	c.mu.Unlock()
	return cfg, true, nil
}

// 缓存窗口内 slug 变更的复核（codex 第 47 轮 P2）：唤醒串未命中且正文含
// @ 时强制刷新 App 身份再判——新句柄的唤醒不被 202 静默吞掉；不含 @ 的
// 普通评论不受影响。
func TestReviewCommentWakeCachedMissRefreshesSlug(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 64, &githubapi.PullRequest{
		RepoFullName: repo, Number: 64, State: "open", HeadSHA: "sha-reslug",
		HeadRepoFullName: repo,
	})
	counter := &countingAppSource{cfg: GitHubAppConfig{AppSlug: "Old-Slug"}}
	e.review.appSource = counter

	// 预热缓存（旧 slug Old-Slug）。
	if _, _, err := e.review.commentWakeMentions(ctx); err != nil {
		t.Fatalf("预热缓存: %v", err)
	}
	// 部署切换：slug 变为 New-Slug。
	counter.mu.Lock()
	counter.cfg = GitHubAppConfig{AppSlug: "New-Slug"}
	counter.mu.Unlock()

	// 用新句柄点名：缓存未命中 + 含 @ → 强制刷新 → 唤醒照常受理。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-reslug",
		issueCommentBody("@new-slug 请再审", repo, 64, 42)); err != nil {
		t.Fatalf("新句柄点名应唤醒: %v", err)
	}
	counter.mu.Lock()
	reads := counter.reads
	counter.mu.Unlock()
	if reads != 2 {
		t.Fatalf("未命中应强制刷新一次: reads = %d", reads)
	}
	var run model.ReviewRun
	if err := e.svc.db.Where("pr_number = 64").First(&run).Error; err != nil {
		t.Fatalf("新句柄点名应开轮: %v", err)
	}

	// 不含 @ 的普通评论：不触发强制刷新（缓存照用）。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-reslug-plain",
		issueCommentBody("普通讨论，不需要审查", repo, 64, 42)); err != nil {
		t.Fatalf("普通评论应忽略: %v", err)
	}
	counter.mu.Lock()
	readsAfter := counter.reads
	counter.mu.Unlock()
	if readsAfter != reads {
		t.Fatalf("不含 @ 的未命中不得强制刷新: %d -> %d", reads, readsAfter)
	}
}

// seen 行存在但快照时刻为空也回落总账基准（codex 第 50 轮 P2）：tied 重
// 验等缺 updatedAt 的受理写的 seen 行 PRUpdatedAt 为 NULL——只查 seen 会
// 得到无基准、放行滞后读回退总账。
func TestReviewCommentWakeNilSeenFallsBackToLedger(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	t2 := time.Now().UTC().Add(-2 * time.Hour)
	t1 := t2.Add(-time.Hour)
	if err := e.svc.db.Create(&model.ReviewPullRequest{
		ProjectID: e.project.ID, UserID: e.project.UserID,
		RepoFullName: repo, PRNumber: 92,
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		HeadSHA:       "sha-b", RoundNumber: 1, UpdatedAt: t2, PRUpdatedAt: &t2,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	if err := e.svc.db.Create(&model.ReviewSeenSHA{
		ProjectID: e.project.ID, RepoFullName: repo, PRNumber: 92,
		HeadSHA: "sha-b", IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		PRUpdatedAt: nil, AcceptedAt: t2,
	}).Error; err != nil {
		t.Fatalf("seed seen: %v", err)
	}
	// 滞后读：旧 head sha-a（时间戳 t1 < 账本基准 t2）。
	e.fake.SetPR(repo, 92, &githubapi.PullRequest{
		RepoFullName: repo, Number: 92, State: "open", HeadSHA: "sha-a",
		HeadRepoFullName: repo, UpdatedAt: &t1,
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-nilseen",
		issueCommentBody("@fast-ship-bot", repo, 92, 42)); err == nil {
		t.Fatal("seen 行空时戳应回落账本基准拦滞后读（500）")
	}
	var after model.ReviewPullRequest
	if err := e.svc.db.Where("pr_number = 92").First(&after).Error; err != nil {
		t.Fatalf("总账行应仍在: %v", err)
	}
	if after.HeadSHA != "sha-b" {
		t.Fatalf("总账不得回退: head = %s", after.HeadSHA)
	}
}

// 作者预比对先于 App 身份读取（codex 第 51 轮 P2）：有登录名的陌生人
// （非作者且非 OWNER）的任意 @ 评论在缓存未命中强制刷新之前就被拒绝，
// 公开仓库的评论流量不得放大成部署设置读取。
func TestReviewCommentWakeStrangerBeforeFreshRead(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 65, &githubapi.PullRequest{
		RepoFullName: repo, Number: 65, State: "open", HeadSHA: "sha-str2",
		HeadRepoFullName: repo,
	})
	counter := &countingAppSource{cfg: GitHubAppConfig{AppSlug: "Fast-Ship-Demo"}}
	e.review.appSource = counter
	// 预热缓存（未命中刷新只在缓存命中时触发，先填充）。
	if _, _, err := e.review.commentWakeMentions(ctx); err != nil {
		t.Fatalf("预热缓存: %v", err)
	}

	// 陌生人（登录名与 payload 作者不同、非 OWNER）@ 任意句柄：预比对拒绝，
	// 不得触发强制刷新。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-str2",
		commentBodyWithUsers("@whatever-slug 看看", repo, 65, 42, "NONE", "stranger", "pr-author")); err != nil {
		t.Fatalf("陌生人 @ 评论应零成本拒绝: %v", err)
	}
	counter.mu.Lock()
	reads := counter.reads
	counter.mu.Unlock()
	if reads != 1 {
		t.Fatalf("预比对应在强制刷新之前: reads = %d", reads)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 65").Count(&runs)
	if runs != 0 {
		t.Fatalf("陌生人不得开轮: runs = %d", runs)
	}
}

// 启用基线（INT-167，fast-ship-bot 第 52 轮 P2）：评论时刻早于 enabled_at
// 基线的存量投递（对账重投/手动重放）不得按当前 live head 开轮——静默忽
// 略并随事务落幂等账；基线之后的评论照常受理。
func TestReviewCommentWakeEnabledAtBaseline(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 66, &githubapi.PullRequest{
		RepoFullName: repo, Number: 66, State: "open", HeadSHA: "sha-enable",
		HeadRepoFullName: repo,
	})
	// 启用基线：1 小时前。
	enabledAt := time.Now().UTC().Add(-time.Hour)
	if err := e.svc.db.Model(&model.ProjectReviewSettings{}).
		Where("project_id = ?", e.project.ID).
		Update("enabled_at", enabledAt).Error; err != nil {
		t.Fatalf("set enabled_at: %v", err)
	}

	// 启用前的存量评论（对账重投模拟）：忽略并落幂等账。
	stale := issueCommentBody("@fast-ship-bot", repo, 66, 42, func(m map[string]any) {
		m["comment"].(map[string]any)["created_at"] = enabledAt.Add(-30 * time.Minute).Format(time.RFC3339)
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-enable-stale", stale); err != nil {
		t.Fatalf("启用前投递应静默忽略: %v", err)
	}
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-enable-stale")).
		Count(&deliveries)
	if deliveries != 1 {
		t.Fatalf("忽略判定应随事务落幂等账: %d", deliveries)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 66").Count(&runs)
	if runs != 0 {
		t.Fatalf("启用前投递不得开轮: runs = %d", runs)
	}

	// 基线之后的评论照常受理开轮。
	fresh := issueCommentBody("@fast-ship-bot", repo, 66, 42, func(m map[string]any) {
		m["comment"].(map[string]any)["created_at"] = enabledAt.Add(30 * time.Minute).Format(time.RFC3339)
	})
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-enable-fresh", fresh); err != nil {
		t.Fatalf("基线后评论应受理: %v", err)
	}
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 66").Count(&runs)
	if runs != 1 {
		t.Fatalf("基线后评论应开轮: runs = %d", runs)
	}
}

// 词边界（fast-ship-bot 第 56 轮 P2）：@fast-ship-bot2/@fast-ship-bots
// 这类真实存在的其他账号不得误判成点名机器人；[bot] 后缀形态不受影响。
func TestReviewCommentWakeWordBoundary(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 67, &githubapi.PullRequest{
		RepoFullName: repo, Number: 67, State: "open", HeadSHA: "sha-wb",
		HeadRepoFullName: repo,
	})

	// 相似名账号：不是点名，忽略。
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-wb-2",
		issueCommentBody("@fast-ship-bot2 请再审", repo, 67, 42)); err != nil {
		t.Fatalf("相似名评论应忽略: %v", err)
	}
	// 精确句柄 + [bot] 后缀形态：都是点名，各开一轮（轮次间置终态绕开
	// 同 head 在途守卫）。
	for i, body := range []string{"@fast-ship-bot 请再审", "@fast-ship-bot[bot] 请再审"} {
		if i > 0 {
			if err := e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 67").
				Updates(map[string]any{"status": model.ReviewRunCompleted, "finished_at": time.Now().UTC()}).Error; err != nil {
				t.Fatalf("置终态: %v", err)
			}
		}
		if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", fmt.Sprintf("d-wb-hit-%d", i),
			issueCommentBody(body, repo, 67, 42)); err != nil {
			t.Fatalf("精确句柄应唤醒: %v", err)
		}
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 67").Count(&runs)
	if runs != 2 {
		t.Fatalf("两次精确点名应各开一轮: runs = %d", runs)
	}
}

// 强制刷新同代只做一次（fast-ship-bot 第 56 轮 P2）：授权用户连续含 @ 的
// 未命中评论不放大成逐条设置读取。
func TestReviewCommentWakeForceRefreshOncePerGeneration(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 68, &githubapi.PullRequest{
		RepoFullName: repo, Number: 68, State: "open", HeadSHA: "sha-fr",
		HeadRepoFullName: repo,
	})
	counter := &countingAppSource{cfg: GitHubAppConfig{AppSlug: "Fast-Ship-Demo"}}
	e.review.appSource = counter

	// 三条含 @ 但未命中缓存的授权评论：预热 1 次 + 首条强制刷新 1 次，
	// 第二、三条走 forcedRefreshed 标记不再刷新。
	for i := 0; i < 3; i++ {
		if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", fmt.Sprintf("d-fr-%d", i),
			commentBodyWithUsers("@someone-else 帮忙看下", repo, 68, 42, "OWNER", "pr-author", "pr-author")); err != nil {
			t.Fatalf("含 @ 未命中评论应忽略: %v", err)
		}
	}
	counter.mu.Lock()
	reads := counter.reads
	counter.mu.Unlock()
	if reads != 2 {
		t.Fatalf("同缓存代内强制刷新只应一次（预热+刷新）: reads = %d", reads)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 68").Count(&runs)
	if runs != 0 {
		t.Fatalf("未命中不得开轮: runs = %d", runs)
	}
}

// 解密失败降级态入短缓存（fast-ship-bot 第 56 轮 P2）：密文损坏期间的评
// 论不逐条重读部署设置，成本回到每 TTL 一次。
func TestReviewCommentWakeDecryptFallbackCached(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 69, &githubapi.PullRequest{
		RepoFullName: repo, Number: 69, State: "open", HeadSHA: "sha-dec2",
		HeadRepoFullName: repo,
	})
	counter := &countingDecryptSource{}
	e.review.appSource = counter

	for i := 0; i < 3; i++ {
		if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", fmt.Sprintf("d-dec-%d", i),
			issueCommentBody("@fast-ship-bot 请再审", repo, 69, 42)); err != nil {
			t.Fatalf("解密失败应回落字面量: %v", err)
		}
	}
	counter.mu.Lock()
	reads := counter.reads
	counter.mu.Unlock()
	if reads != 1 {
		t.Fatalf("降级态应入短缓存（3 条评论 1 次读取）: reads = %d", reads)
	}
	var runs int64
	e.svc.db.Model(&model.ReviewRun{}).Where("pr_number = 69").Count(&runs)
	if runs != 1 {
		t.Fatalf("字面量点名应开轮: runs = %d", runs)
	}
}

// countingDecryptSource GitHubAppSource 桩：读取恒报解密失败并计数。
type countingDecryptSource struct {
	mu    sync.Mutex
	reads int
}

func (c *countingDecryptSource) Current(context.Context) (GitHubAppConfig, bool, error) {
	c.mu.Lock()
	c.reads++
	c.mu.Unlock()
	return GitHubAppConfig{}, false, errGitHubAppDecrypt
}

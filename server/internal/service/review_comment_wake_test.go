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
// 实况 open → 恒开新轮（action=comment），不写 seen、不落防抖行。
func TestReviewCommentWakeOpensRound(t *testing.T) {
	e := newReviewEngineEnv(t)
	e.enableReview(t)
	repo := e.project.GithubOwner + "/" + e.project.GithubRepo
	ctx := context.Background()
	e.fake.SetPR(repo, 21, &githubapi.PullRequest{
		RepoFullName: repo, Number: 21, State: "open",
		HeadSHA: "sha-w1", HeadRepoFullName: repo,
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
	var seen int64
	e.svc.db.Model(&model.ReviewSeenSHA{}).Count(&seen)
	if seen != 0 {
		t.Fatalf("评论唤醒不得写 review_seen_shas，实得 %d 行", seen)
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
// 都被并发推进（两次尝试观察值都不一致）→ 放弃并补登幂等账，不开轮；
// 重投按 202 去重，不再空转。
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
	// 幂等账已补登：重投按 202 去重。
	var deliveries int64
	e.svc.db.Model(&model.GitHubWebhookDelivery{}).
		Where("delivery_id = ?", deliveryLedgerKey(model.ForgeIntegrationBuiltinGitHubID, "d-giveup")).
		Count(&deliveries)
	if deliveries != 1 {
		t.Fatalf("放弃路径幂等账应已登记，实得 %d", deliveries)
	}
	if err := e.review.HandleGitHubWebhook(ctx, "issue_comment", "d-giveup",
		issueCommentBody("@fast-ship-bot", repo, 42, 42)); !errors.Is(err, ErrDeliveryDuplicated) {
		t.Fatalf("重投应按幂等去重，实得 %v", err)
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

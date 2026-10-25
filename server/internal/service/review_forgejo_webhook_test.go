package service

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/godbobo/fast_ship/server/internal/forge"
	"github.com/godbobo/fast_ship/server/internal/model"
	"gorm.io/gorm"
)

// Forgejo webhook 入口与轮询补漏（INT-167 任务 3）的 service 层定向测试：
// delivery 幂等、synchronized→synchronize 归一开轮、draft/closed 语义、与
// webhook 同 SHA 去重、单项目失败不中断整轮。

// forgejoPullPayload Forgejo pull_request webhook 的最小 payload（Gitea 形态：
// 推送动作是 synchronized，无 installation 字段）。
func forgejoPullPayload(action, repo string, number int, sha string, draft bool) []byte {
	return []byte(`{
		"action": "` + action + `", "number": ` + strconv.Itoa(number) + `,
		"pull_request": {
			"draft": ` + strconv.FormatBool(draft) + `,
			"html_url": "https://forge.example.com/` + repo + `/pulls/` + strconv.Itoa(number) + `",
			"title": "T",
			"updated_at": "2026-01-02T03:04:05Z",
			"head": {"sha": "` + sha + `", "ref": "feat", "repo": {"fork": false, "full_name": "` + repo + `"}},
			"base": {"ref": "main", "repo": {"full_name": "` + repo + `"}}
		},
		"repository": {"full_name": "` + repo + `"}
	}`)
}

// forgejoPullPayloadNoTS 不带 updated_at 的投递：自托管实例版本参差或代
// 理改写 payload 时该字段可能缺失。
func forgejoPullPayloadNoTS(action, repo string, number int, sha string) []byte {
	return []byte(`{
		"action": "` + action + `", "number": ` + strconv.Itoa(number) + `,
		"pull_request": {
			"draft": false,
			"html_url": "https://forge.example.com/` + repo + `/pulls/` + strconv.Itoa(number) + `",
			"title": "T",
			"head": {"sha": "` + sha + `", "ref": "feat", "repo": {"fork": false, "full_name": "` + repo + `"}},
			"base": {"ref": "main", "repo": {"full_name": "` + repo + `"}}
		},
		"repository": {"full_name": "` + repo + `"}
	}`)
}

// seedForgejoReviewProject 落一个绑定 Forgejo 且启用审查的项目：设置行
// integration_id 是真实集成 id，触发项可指定。
func seedForgejoReviewProject(t *testing.T, svc *testServices, integrationID, username, repo string, triggers map[string]bool) (*model.Project, *model.User) {
	t.Helper()
	user := createTestUser(t, svc.db, username)
	project := createTestProject(t, svc.db, user.ID, func(p *model.Project) {
		owner, name, _ := strings.Cut(repo, "/")
		p.GithubOwner = owner
		p.GithubRepo = name
		p.CodeIntegrationID = integrationID
	})
	now := time.Now().UTC()
	settings := &model.ProjectReviewSettings{
		ProjectID: project.ID, UserID: user.ID,
		RepoFullName: repo, IntegrationID: integrationID, Enabled: true,
		TriggerOpened: true, TriggerSynchronize: true,
		CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}
	if triggers != nil {
		settings.TriggerOpened = triggers["opened"]
		settings.TriggerSynchronize = triggers["synchronize"]
	}
	if err := svc.db.Create(settings).Error; err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	return project, user
}

// TestHandleForgejoWebhook_SynchronizedOpensRound synchronized 动作（Forgejo
// 拼法）触发开轮前置：归一成 synchronize 登记 seen + 写防抖行，键都按集成 id
// 分区。
func TestHandleForgejoWebhook_SynchronizedOpensRound(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-sync", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)

	body := forgejoPullPayload("synchronized", "acme/app", 5, "sha-sync-1", false)
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-sync-1", body); err != nil {
		t.Fatalf("HandleForgejoWebhook: %v", err)
	}

	var seen model.ReviewSeenSHA
	if err := svc.db.Where("project_id = ? AND integration_id = ? AND pr_number = ?",
		project.ID, forgeID, 5).First(&seen).Error; err != nil {
		t.Fatalf("seen row: %v", err)
	}
	if seen.HeadSHA != "sha-sync-1" {
		t.Fatalf("seen head = %q", seen.HeadSHA)
	}
	var debounce model.ReviewDebounce
	if err := svc.db.Where("project_id = ? AND integration_id = ?", project.ID, forgeID).
		First(&debounce).Error; err != nil {
		t.Fatalf("debounce row: %v", err)
	}
	if debounce.HeadSHA != "sha-sync-1" || debounce.Action != "synchronize" {
		t.Fatalf("debounce = sha=%q action=%q, want sha-sync-1/synchronize", debounce.HeadSHA, debounce.Action)
	}
	if debounce.InstallationID != 0 {
		t.Fatalf("Forgejo 无安装语义, installation = %d", debounce.InstallationID)
	}

	// 非 pull_request 事件只落 delivery，不进触发面。
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "push", "d-sync-2", []byte(`{"ref":"main"}`)); err != nil {
		t.Fatalf("push event: %v", err)
	}
	var seenCount int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ?", project.ID).Count(&seenCount)
	if seenCount != 1 {
		t.Fatalf("seen rows = %d, want 1", seenCount)
	}
}

// TestHandleForgejoWebhook_IgnoreAndDedup draft 忽略（delivery 仍落账）；同
// delivery 重放幂等。
func TestHandleForgejoWebhook_IgnoreAndDedup(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-ignore", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)
	ctx := context.Background()

	// draft → 忽略触发（无 seen/防抖），delivery 照常落账。
	if err := review.HandleForgejoWebhook(ctx, forgeID, "pull_request", "d-draft",
		forgejoPullPayload("opened", "acme/app", 9, "sha-draft", true)); err != nil {
		t.Fatalf("draft webhook: %v", err)
	}
	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ?", project.ID).Count(&n)
	if n != 0 {
		t.Fatalf("draft 不得登记 seen, rows = %d", n)
	}
	var debounce int64
	svc.db.Model(&model.ReviewDebounce{}).Where("project_id = ?", project.ID).Count(&debounce)
	if debounce != 0 {
		t.Fatalf("draft 不得写防抖, rows = %d", debounce)
	}

	// 同 delivery 重放：首次受理，重放 ErrDeliveryDuplicated，账里仍只有一行。
	payload := forgejoPullPayload("opened", "acme/app", 9, "sha-9", false)
	if err := review.HandleForgejoWebhook(ctx, forgeID, "pull_request", "d-replay", payload); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	err := review.HandleForgejoWebhook(ctx, forgeID, "pull_request", "d-replay", payload)
	if !errors.Is(err, ErrDeliveryDuplicated) {
		t.Fatalf("replay err = %v, want ErrDeliveryDuplicated", err)
	}
	svc.db.Model(&model.GitHubWebhookDelivery{}).Where("delivery_id = ?", "d-replay").Count(&n)
	if n != 1 {
		t.Fatalf("delivery rows = %d, want 1", n)
	}
}

// TestHandleForgejoWebhook_ClosedCancelsInFlight closed 取消在途运行与防抖，
// 推进 epoch 让在途同步停下；账本 head 保留。
func TestHandleForgejoWebhook_ClosedCancelsInFlight(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, user := seedForgejoReviewProject(t, svc, forgeID, "fj-closed", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)
	now := time.Now().UTC()

	run := &model.ReviewRun{
		IntegrationID: forgeID,
		ID:            "run-closed-1", UserID: user.ID, ProjectID: project.ID,
		Kind: model.ReviewKindCode, RepoFullName: "acme/app", PRNumber: 3, HeadSHA: "sha-old",
		RoundNumber: 1, EnvironmentID: "env", InstanceID: "i", Model: "m",
		ThreadID: "th", LaunchCommandID: "lc", InitialMessageID: "im",
		Status: model.ReviewRunQueued, DeadlineAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := svc.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if err := svc.db.Create(&model.ReviewDebounce{
		IntegrationID: forgeID, ProjectID: project.ID,
		RepoFullName: "acme/app", PRNumber: 3, HeadSHA: "sha-old",
		Action: "synchronize", FireAt: now.Add(time.Hour), UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed debounce: %v", err)
	}
	if err := svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: forgeID, ProjectID: project.ID, UserID: user.ID,
		RepoFullName: "acme/app", PRNumber: 3, HeadSHA: "sha-old",
		SyncPending: true, SyncEpoch: 1, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}

	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-closed",
		forgejoPullPayload("closed", "acme/app", 3, "sha-old", false)); err != nil {
		t.Fatalf("closed webhook: %v", err)
	}

	var gotRun model.ReviewRun
	svc.db.First(&gotRun, "id = ?", run.ID)
	if gotRun.Status != model.ReviewRunCancelled || gotRun.ErrorCode != "superseded" {
		t.Fatalf("run = %s/%s, want cancelled/superseded", gotRun.Status, gotRun.ErrorCode)
	}
	var debounce int64
	svc.db.Model(&model.ReviewDebounce{}).Where("project_id = ?", project.ID).Count(&debounce)
	if debounce != 0 {
		t.Fatalf("debounce rows = %d, want 0", debounce)
	}
	var ledger model.ReviewPullRequest
	svc.db.First(&ledger, "project_id = ? AND pr_number = ?", project.ID, 3)
	if ledger.SyncPending || ledger.SyncEpoch != 2 || ledger.HeadSHA != "sha-old" {
		t.Fatalf("ledger = pending=%v epoch=%d head=%s, want false/2/sha-old",
			ledger.SyncPending, ledger.SyncEpoch, ledger.HeadSHA)
	}
}

// pollStubClient 轮询补漏的 Forgejo 客户端桩：按仓库名返回预置 PR 或错误
// （其余方法不在测试路径，经接口内嵌兜底）。
type pollStubClient struct {
	forge.ReviewClient
	byRepo    map[string][]forge.PullRequest
	errByRepo map[string]error
	live      *forge.PullRequest // 单拉复核返回的「实时」PR
}

func (c *pollStubClient) ListOpenPullRequests(_ context.Context, repo string) ([]forge.PullRequest, error) {
	if err, ok := c.errByRepo[repo]; ok {
		return nil, err
	}
	return c.byRepo[repo], nil
}

// GetPullRequest 防抖实况读路径用：errByRepo 命中的仓库返回错误。
func (c *pollStubClient) GetPullRequest(_ context.Context, repo string, number int) (*forge.PullRequest, error) {
	if err, ok := c.errByRepo[repo]; ok {
		return nil, err
	}
	if c.live != nil {
		return c.live, nil
	}
	return &forge.PullRequest{RepoFullName: repo, Number: number, State: "open"}, nil
}

// pollStubResolver 按集成 id 返回各自客户端（bad/good 集成分开注入）。
type pollStubResolver struct {
	byIntegration map[string]forge.ReviewClient
}

func (r pollStubResolver) ClientFor(_ context.Context, integrationID string, _ string, _ int64) (forge.ReviewClient, error) {
	if client, ok := r.byIntegration[integrationID]; ok {
		return client, nil
	}
	return nil, errors.New("无该集成的桩客户端")
}

// TestForgejoPoll_AcceptsUnseenSHA 未见 SHA → 受理（动作按 opened 归类）；
// 同 SHA 重复轮询 → 不重复登记；draft → 跳过；勾选位关闭 → 登记 seen 但不
// 开轮（补漏不绕过用户没勾选的动作）。
func TestForgejoPoll_AcceptsUnseenSHA(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-poll", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: &pollStubClient{byRepo: map[string][]forge.PullRequest{
			"acme/app": {
				{Number: 11, RepoFullName: "acme/app", Title: "new", HeadSHA: "sha-p1", BaseRef: "main", HeadRef: "feat"},
				{Number: 12, RepoFullName: "acme/app", Title: "draft", HeadSHA: "sha-p2", Draft: true},
			},
		}},
	}}

	review.pollForgejoOpenPRs(time.Now().UTC())

	var seen11 model.ReviewSeenSHA
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 11).
		First(&seen11).Error; err != nil {
		t.Fatalf("PR11 seen: %v", err)
	}
	var debounce model.ReviewDebounce
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 11).
		First(&debounce).Error; err != nil {
		t.Fatalf("PR11 debounce: %v", err)
	}
	if debounce.Action != "opened" || debounce.HeadSHA != "sha-p1" {
		t.Fatalf("debounce = %s/%s, want opened/sha-p1", debounce.Action, debounce.HeadSHA)
	}
	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND pr_number = ?", project.ID, 12).Count(&n)
	if n != 0 {
		t.Fatalf("draft PR12 不得登记 seen, rows = %d", n)
	}

	// 同 SHA 第二轮：不重复登记（seen 主键唯一），防抖行不新增。
	review.pollForgejoOpenPRs(time.Now().UTC())
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND pr_number = ?", project.ID, 11).Count(&n)
	if n != 1 {
		t.Fatalf("同 SHA 重复轮询 seen rows = %d, want 1", n)
	}
	svc.db.Model(&model.ReviewDebounce{}).Where("project_id = ?", project.ID).Count(&n)
	if n != 1 {
		t.Fatalf("debounce rows = %d, want 1", n)
	}

	// TriggerOpened 关闭的项目：未见 SHA 登记 seen 但不写防抖（补漏不绕过勾选位）。
	project2, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-poll-off", "beta/web",
		map[string]bool{"opened": false, "synchronize": false})
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: &pollStubClient{byRepo: map[string][]forge.PullRequest{
			"beta/web": {{Number: 41, RepoFullName: "beta/web", Title: "y", HeadSHA: "sha-p4"}},
		}},
	}}
	review.pollForgejoOpenPRs(time.Now().UTC())
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND pr_number = ?", project2.ID, 41).Count(&n)
	if n != 1 {
		t.Fatalf("关闭勾选位也应登记 seen, rows = %d", n)
	}
	svc.db.Model(&model.ReviewDebounce{}).Where("project_id = ?", project2.ID).Count(&n)
	if n != 0 {
		t.Fatalf("未勾选动作不得写防抖, rows = %d", n)
	}
}

// TestForgejoPoll_DedupsWithWebhook webhook 先受理的 SHA 轮询再见到不重复受理：
// seen 只有一行、防抖不被改写、不取消旧轮。
func TestForgejoPoll_DedupsWithWebhook(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-dedup", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: &pollStubClient{byRepo: map[string][]forge.PullRequest{
			"acme/app": {{Number: 21, RepoFullName: "acme/app", Title: "x", HeadSHA: "sha-w1", BaseRef: "main", HeadRef: "feat"}},
		}},
	}}

	// webhook（synchronized → synchronize）先受理。
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-w1",
		forgejoPullPayload("synchronized", "acme/app", 21, "sha-w1", false)); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	var before model.ReviewDebounce
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 21).
		First(&before).Error; err != nil {
		t.Fatalf("webhook debounce: %v", err)
	}

	review.pollForgejoOpenPRs(time.Now().UTC())

	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND pr_number = ?", project.ID, 21).Count(&n)
	if n != 1 {
		t.Fatalf("seen rows = %d, want 1（webhook/poll 同 SHA 只受理一次）", n)
	}
	var after model.ReviewDebounce
	svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 21).First(&after)
	if after.HeadSHA != before.HeadSHA || after.Action != "synchronize" {
		t.Fatalf("debounce 漂移: %s/%s → %s/%s", before.HeadSHA, before.Action, after.HeadSHA, after.Action)
	}
}

// TestForgejoPoll_SameHeadStaysClosedOpenedOff opened 勾选位关闭时，首次
// 轮询登记 seen+推进 head 但不开轮；同 SHA 的后续轮询不得把无新推送归成
// synchronize 再补一轮——head 没变就是没有新事件。
func TestForgejoPoll_SameHeadStaysClosedOpenedOff(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-poll-samehead", "acme/app",
		map[string]bool{"opened": false, "synchronize": true})
	review := newReviewTestService(t, svc, nil, nil, nil)
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: &pollStubClient{byRepo: map[string][]forge.PullRequest{
			"acme/app": {{Number: 51, RepoFullName: "acme/app", Title: "x", HeadSHA: "sha-same"}},
		}},
	}}

	review.pollForgejoOpenPRs(time.Now().UTC())
	review.pollForgejoOpenPRs(time.Now().UTC())

	var n int64
	svc.db.Model(&model.ReviewDebounce{}).Where("project_id = ?", project.ID).Count(&n)
	if n != 0 {
		t.Fatalf("同 SHA 重复轮询不得写防抖, rows = %d", n)
	}
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ?", project.ID).Count(&n)
	if n != 1 {
		t.Fatalf("seen rows = %d, want 1", n)
	}
	var ledger model.ReviewPullRequest
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 51).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ledger.HeadSHA != "sha-same" {
		t.Fatalf("ledger head = %q, want sha-same", ledger.HeadSHA)
	}
}

// TestForgejoPoll_StaleSnapshotDoesNotRegress 轮询读到旧 head A 后 webhook
// 先受理新 head B：带旧 updated_at 的 A 必须按过期忽略（总账不退回 A、不
// 取消 B 的防抖）；updated_at 比已受理 head 新的快照仍正常补漏。
func TestForgejoPoll_StaleSnapshotDoesNotRegress(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-stale", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)

	// webhook 先受理新 head B（payload 的 updated_at 固定 2026-01-02T03:04:05Z）。
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-b1",
		forgejoPullPayload("synchronized", "acme/app", 61, "sha-b", false)); err != nil {
		t.Fatalf("webhook B: %v", err)
	}

	stale := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // 早于 webhook 的 updated_at
	fresh := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC) // 晚于 webhook 的 updated_at
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: &pollStubClient{byRepo: map[string][]forge.PullRequest{
			"acme/app": {{Number: 61, RepoFullName: "acme/app", Title: "old",
				HeadSHA: "sha-a", UpdatedAt: &stale}},
		}},
	}}
	review.pollForgejoOpenPRs(time.Now().UTC())

	// 过期快照 A：不登记 seen、不写防抖、总账仍是 B。
	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND pr_number = ? AND head_sha = ?",
		project.ID, 61, "sha-a").Count(&n)
	if n != 0 {
		t.Fatalf("过期快照不得登记 seen, rows = %d", n)
	}
	var ledger model.ReviewPullRequest
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 61).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ledger.HeadSHA != "sha-b" {
		t.Fatalf("ledger head = %q, want sha-b（不得退回旧快照）", ledger.HeadSHA)
	}
	var debounce model.ReviewDebounce
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 61).First(&debounce).Error; err != nil {
		t.Fatalf("debounce: %v", err)
	}
	if debounce.HeadSHA != "sha-b" {
		t.Fatalf("debounce head = %q, want sha-b", debounce.HeadSHA)
	}

	// updated_at 更新的快照照常补漏（webhook 真正漏投新 head 的场景）。
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: &pollStubClient{byRepo: map[string][]forge.PullRequest{
			"acme/app": {{Number: 61, RepoFullName: "acme/app", Title: "newer",
				HeadSHA: "sha-c", UpdatedAt: &fresh}},
		}},
	}}
	review.pollForgejoOpenPRs(time.Now().UTC())
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 61).First(&ledger).Error; err != nil {
		t.Fatalf("ledger2: %v", err)
	}
	if ledger.HeadSHA != "sha-c" {
		t.Fatalf("ledger head = %q, want sha-c（新快照应受理）", ledger.HeadSHA)
	}
}

// rerunForgejoStubClient RerunReview 正向路径的 Forgejo 客户端桩：只实现
// GetPullRequest（事务内其余调用不经客户端）。
type rerunForgejoStubClient struct {
	forge.ReviewClient
	pr *forge.PullRequest
}

func (c *rerunForgejoStubClient) GetPullRequest(_ context.Context, _ string, _ int) (*forge.PullRequest, error) {
	return c.pr, nil
}

// TestRerunReviewForgejoIntegration Forgejo 绑定的项目手动重跑：不再被硬
// 编码拒绝；resolver 取到集成客户端，installationID=0，事务内复核后开轮，
// 运行带 forgejo 集成 id。
func TestRerunReviewForgejoIntegration(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, user := seedForgejoReviewProject(t, svc, forgeID, "fj-rerun", "acme/app", nil)
	now := time.Now().UTC()
	if err := svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: forgeID, ProjectID: project.ID, UserID: user.ID,
		RepoFullName: "acme/app", PRNumber: 71, HeadSHA: "sha-r2", UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	review := newReviewTestService(t, svc, nil, nil, nil)
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: &rerunForgejoStubClient{pr: &forge.PullRequest{
			RepoFullName: "acme/app", Number: 71, HeadSHA: "sha-r2", State: "open",
			Title: "t", HTMLURL: "https://forge.example.com/acme/app/pulls/71",
			BaseRef: "main", HeadRef: "feat",
		}},
	}}

	out, err := review.RerunReview(context.Background(), user.ID, project.ID, 71, "acme/app")
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if !out.Accepted || out.HeadSHA != "sha-r2" {
		t.Fatalf("rerun out = %+v", out)
	}
	// 空代码组合不产生 run 行；开轮以账本推进（新轮 + sync_pending）为准。
	var ledger model.ReviewPullRequest
	if err := svc.db.Where("project_id = ? AND pr_number = ? AND integration_id = ?",
		project.ID, 71, forgeID).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if !ledger.SyncPending || ledger.RoundNumber != out.RoundNumber || ledger.HeadSHA != "sha-r2" {
		t.Fatalf("账本未推进: %+v", ledger)
	}
	if ledger.InstallationID != 0 {
		t.Fatalf("Forgejo 无安装语义, installation = %d", ledger.InstallationID)
	}
}

// TestRerunReviewRejectsOtherIntegrationRecord 改绑集成后，同仓库同号 PR
// 的旧集成记录不得重跑：当前集成下没有账本、其他集成下有同号账本即拒绝
// （请求只可能来自旧记录）；从未审过的 PR（两侧都没账本）不受限。
func TestRerunReviewRejectsOtherIntegrationRecord(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, user := seedForgejoReviewProject(t, svc, forgeID, "fj-rerun-x", "acme/app", nil)
	now := time.Now().UTC()
	// GitHub 时代的旧账本：同 repo 同号，但归属 github-default。
	if err := svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: model.ForgeIntegrationBuiltinGitHubID,
		ProjectID:     project.ID, UserID: user.ID,
		RepoFullName: "acme/app", PRNumber: 81, HeadSHA: "sha-gh", UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed gh ledger: %v", err)
	}
	review := newReviewTestService(t, svc, nil, nil, nil)

	if _, err := review.RerunReview(context.Background(), user.ID, project.ID, 81, "acme/app"); err == nil {
		t.Fatal("其他集成的同号记录不应能重跑")
	}
}

// TestForgejoPoll_ProjectFailureIsolated 单项目失败（集成不可用）只记日志，
// 其他项目照常补漏；漂移项目（项目行已改绑）跳过。
func TestForgejoPoll_ProjectFailureIsolated(t *testing.T) {
	svc := setupTestServices(t)
	badForge := createTestForgeIntegration(t, svc.db, "https://bad.example.com")
	goodForge := createTestForgeIntegration(t, svc.db, "https://good.example.com")
	seedForgejoReviewProject(t, svc, badForge.ID, "fj-bad", "bad/app", nil)
	project2, _ := seedForgejoReviewProject(t, svc, goodForge.ID, "fj-good", "good/app", nil)

	// 漂移项目：设置行指向 goodForge，项目行已改绑 badForge → 跳过。
	user3 := createTestUser(t, svc.db, "fj-drift")
	project3 := createTestProject(t, svc.db, user3.ID, func(p *model.Project) {
		p.GithubOwner = "good"
		p.GithubRepo = "drift"
		p.CodeIntegrationID = badForge.ID
	})
	now := time.Now().UTC()
	if err := svc.db.Create(&model.ProjectReviewSettings{
		ProjectID: project3.ID, UserID: user3.ID,
		RepoFullName: "good/drift", IntegrationID: goodForge.ID, Enabled: true,
		TriggerOpened: true, CodeCombinationsJSON: "[]", VisualCombinationsJSON: "[]",
		CreatedAt: now, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed drift settings: %v", err)
	}

	review := newReviewTestService(t, svc, nil, nil, nil)
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		badForge.ID: &pollStubClient{errByRepo: map[string]error{
			"bad/app": errors.New("integration unreachable"),
		}},
		goodForge.ID: &pollStubClient{byRepo: map[string][]forge.PullRequest{
			"good/app":   {{Number: 31, RepoFullName: "good/app", Title: "ok", HeadSHA: "sha-g1"}},
			"good/drift": {{Number: 32, RepoFullName: "good/drift", Title: "drift", HeadSHA: "sha-g2"}},
		}},
	}}

	// bad 项目列表读取失败：只记日志；good 项目受理，drift 项目跳过。
	review.pollForgejoOpenPRs(time.Now().UTC())

	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ?", project2.ID).Count(&n)
	if n != 1 {
		t.Fatalf("good 项目应受理, seen = %d", n)
	}
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ?", project3.ID).Count(&n)
	if n != 0 {
		t.Fatalf("漂移项目应跳过, seen = %d", n)
	}
	svc.db.Model(&model.ReviewSeenSHA{}).Where("pr_number = ?", 32).Count(&n)
	if n != 0 {
		t.Fatalf("drift PR32 不得登记, rows = %d", n)
	}
}

// TestForgejoPoll_DeterministicErrorDisablesReview 轮询确定性失败（仓库已删
// 404、集成密文损坏）自动停用该项目审查设置——不每 tick 固定刷错；瞬时错误
// 不碰设置。修复后人工重开即可恢复（与改绑压回 disabled 同一先例）。
func TestForgejoPoll_DeterministicErrorDisablesReview(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-dead", "dead/app", nil)
	projectFlaky, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-flaky", "flaky/app", nil)

	projectLimit, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-limit", "limited/app", nil)

	review := newReviewTestService(t, svc, nil, nil, nil)
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: &pollStubClient{errByRepo: map[string]error{
			"dead/app":    &forge.StatusError{StatusCode: 404, Body: "repo gone"},
			"flaky/app":   errors.New("dial tcp: i/o timeout"),
			"limited/app": &forge.StatusError{StatusCode: 403, Body: "API rate limit exceeded"},
		}},
	}}

	review.pollForgejoOpenPRs(time.Now().UTC())

	var dead, flaky, limited model.ProjectReviewSettings
	if err := svc.db.First(&dead, "project_id = ?", project.ID).Error; err != nil {
		t.Fatalf("dead settings: %v", err)
	}
	if dead.Enabled {
		t.Fatal("404 确定性失败后审查设置应被停用")
	}
	if err := svc.db.First(&flaky, "project_id = ?", projectFlaky.ID).Error; err != nil {
		t.Fatalf("flaky settings: %v", err)
	}
	if !flaky.Enabled {
		t.Fatal("瞬时错误不得停用审查设置")
	}
	// 限流 403（GitHub 主/二次限流正是回 403）是瞬时失败：不得停用，
	// 否则限流高峰会把整个项目的审查永久关掉。
	if err := svc.db.First(&limited, "project_id = ?", projectLimit.ID).Error; err != nil {
		t.Fatalf("limited settings: %v", err)
	}
	if !limited.Enabled {
		t.Fatal("限流 403 不得停用审查设置")
	}
}

// TestForgejoPoll_DisableCancelsRuns 确定性停用与显式停用同口径收尾：在途
// 运行 cancelled/disabled + 令牌消费 + 防抖删行 + sync_pending 清除 epoch
// 推进——否则调度按 status 捡走残留运行继续开线程。
func TestForgejoPoll_DisableCancelsRuns(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, user := seedForgejoReviewProject(t, svc, forgeID, "fj-cancels", "cancels/app", nil)
	now := time.Now().UTC()
	run := &model.ReviewRun{
		IntegrationID: forgeID, ID: "run-cancel-1", UserID: user.ID, ProjectID: project.ID,
		Kind: model.ReviewKindCode, RepoFullName: "cancels/app", PRNumber: 7, HeadSHA: "sha-7",
		RoundNumber: 1, EnvironmentID: "env", InstanceID: "i", Model: "m",
		ThreadID: "th", LaunchCommandID: "lc", InitialMessageID: "im",
		Status: model.ReviewRunDispatched, DeadlineAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := svc.db.Create(run).Error; err != nil {
		t.Fatalf("seed run: %v", err)
	}
	svc.db.Create(&model.ReviewRunToken{RunID: run.ID, TokenHash: "h", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	svc.db.Create(&model.ReviewDebounce{
		IntegrationID: forgeID, ProjectID: project.ID, RepoFullName: "cancels/app",
		PRNumber: 7, HeadSHA: "sha-7", Action: "opened", FireAt: now.Add(time.Minute), UpdatedAt: now,
	})
	svc.db.Create(&model.ReviewPullRequest{
		IntegrationID: forgeID, ProjectID: project.ID, RepoFullName: "cancels/app",
		PRNumber: 7, HeadSHA: "sha-7", RoundNumber: 1, SyncPending: true, SyncEpoch: 3, UpdatedAt: now,
	})

	review := newReviewTestService(t, svc, nil, nil, nil)
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: &pollStubClient{errByRepo: map[string]error{
			"cancels/app": &forge.StatusError{StatusCode: 404, Body: "repo gone"},
		}},
	}}
	review.pollForgejoOpenPRs(now)

	var after model.ReviewRun
	svc.db.First(&after, "id = ?", run.ID)
	if after.Status != model.ReviewRunCancelled || after.ErrorCode != "disabled" {
		t.Fatalf("在途运行 = %q/%q, want cancelled/disabled", after.Status, after.ErrorCode)
	}
	var tok model.ReviewRunToken
	svc.db.First(&tok, "run_id = ?", run.ID)
	if tok.ConsumedAt == nil {
		t.Fatal("停用应消费令牌")
	}
	var n int64
	svc.db.Model(&model.ReviewDebounce{}).Where("project_id = ?", project.ID).Count(&n)
	if n != 0 {
		t.Fatalf("停用应删防抖行, rows=%d", n)
	}
	var ledger model.ReviewPullRequest
	svc.db.First(&ledger, "project_id = ?", project.ID)
	if ledger.SyncPending || ledger.SyncEpoch != 4 {
		t.Fatalf("ledger = pending=%v epoch=%d, want false/4", ledger.SyncPending, ledger.SyncEpoch)
	}
}

// TestForgejoPoll_StaleSnapshotCannotDisable 轮询在途期间用户改绑/换仓库并
// 重新启用：旧快照的确定性错误不得关掉新绑定（谓词带快照 integration_id+
// repo_full_name，RowsAffected=0 → 不取消不日志）。
func TestForgejoPoll_StaleSnapshotCannotDisable(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-stale", "stale/app", nil)
	var snapshot model.ProjectReviewSettings
	if err := svc.db.First(&snapshot, "project_id = ?", project.ID).Error; err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// 模拟轮询在途期间的改绑：存储行换成新集成+新仓库，快照仍是旧值。
	newForge := createTestForgeIntegration(t, svc.db, "https://new.example.com")
	if err := svc.db.Model(&model.ProjectReviewSettings{}).
		Where("project_id = ?", project.ID).
		Updates(map[string]any{
			"integration_id": newForge.ID, "repo_full_name": "fresh/app",
		}).Error; err != nil {
		t.Fatalf("rebind: %v", err)
	}

	review := newReviewTestService(t, svc, nil, nil, nil)
	if err := review.disableReviewOnDeterministic(&snapshot,
		&forge.StatusError{StatusCode: 404, Body: "repo gone"}); err != nil {
		t.Fatalf("旧快照确定性错误应被吞掉: %v", err)
	}
	var stored model.ProjectReviewSettings
	svc.db.First(&stored, "project_id = ?", project.ID)
	if !stored.Enabled {
		t.Fatal("旧快照不得停用新绑定的审查设置")
	}
}

// TestForgejoPoll_DecryptErrorDisablesReview 集成密文损坏（解密失败哨兵）
// 同属确定性失败：resolver 构造即错 → 设置被停用而不是每 tick 刷错。
func TestForgejoPoll_DecryptErrorDisablesReview(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-crypt", "crypt/app", nil)

	review := newReviewTestService(t, svc, nil, nil, nil)
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		forgeID: decryptFailClient{},
	}}

	review.pollForgejoOpenPRs(time.Now().UTC())

	var settings model.ProjectReviewSettings
	if err := svc.db.First(&settings, "project_id = ?", project.ID).Error; err != nil {
		t.Fatalf("settings: %v", err)
	}
	if settings.Enabled {
		t.Fatal("密文损坏确定性失败后审查设置应被停用")
	}
}

// TestUpsertPRCommentRateLimitDoesNotDuplicate marker 找回的顶层评论 PATCH
// 撞限流 403：报错重试而不是当「不是我们的」回退新建——否则限流窗口每次
// 同步都会多发一条重复顶层审查评论。对照：真权限 403 照常回退新建。
func TestUpsertPRCommentRateLimitDoesNotDuplicate(t *testing.T) {
	svc := setupTestServices(t)
	review := newReviewTestService(t, svc, nil, nil, nil)
	ledger := &model.ReviewPullRequest{
		IntegrationID: "forge-x", ProjectID: "p1",
		RepoFullName: "acme/app", PRNumber: 5,
	}

	rateLimited := &upsertStubClient{patchErr: &forge.StatusError{StatusCode: 403, Body: "API rate limit exceeded"}}
	if err := review.upsertPRComment(context.Background(), rateLimited, ledger,
		"<!-- m -->", "body", "code_comment_id", 0); err == nil {
		t.Fatal("限流 403 应返回错误走重试")
	}
	if rateLimited.created != 0 {
		t.Fatalf("限流 403 不得新建重复顶层评论, created=%d", rateLimited.created)
	}

	forbidden := &upsertStubClient{patchErr: &forge.StatusError{StatusCode: 403, Body: "Forbidden"}}
	if err := review.upsertPRComment(context.Background(), forbidden, ledger,
		"<!-- m -->", "body", "code_comment_id", 0); err != nil {
		t.Fatalf("权限 403 应回退新建: %v", err)
	}
	if forbidden.created != 1 {
		t.Fatalf("权限 403 应回退新建一条, created=%d", forbidden.created)
	}
}

// upsertStubClient 顶层评论 upsert 路径桩：marker 找回已存评论，PATCH
// 结果与新建计数可观测。
type upsertStubClient struct {
	forge.ReviewClient
	patchErr error
	created  int
}

func (c *upsertStubClient) ListIssueCommentsForPR(context.Context, string, int) ([]forge.IssueComment, error) {
	return nil, nil
}

func (c *upsertStubClient) FindPullRequestComment(context.Context, string, int, string) (int64, bool, error) {
	return 900, true, nil
}

func (c *upsertStubClient) UpdatePullRequestComment(context.Context, string, int64, string) error {
	return c.patchErr
}

func (c *upsertStubClient) CreatePullRequestComment(context.Context, string, int, string) (int64, error) {
	c.created++
	return 901, nil
}

// decryptFailClient 密文损坏场景桩：reviewClient 的解密在 resolver 内发生——
// 桩绕过 resolver，让 ListOpenPullRequests 直接返回解密哨兵（同一判定面）。
type decryptFailClient struct{ forge.ReviewClient }

func (decryptFailClient) ListOpenPullRequests(context.Context, string) ([]forge.PullRequest, error) {
	return nil, errForgeIntegrationDecrypt
}

// TestHandleForgejoWebhook_CrossRepoForkRejected webhook 的 fork 过滤按
// head/base 仓库归属判定而非 repo.fork 布尔：head 在别的仓库的 PR 不受理；
// 仓库本身是 fork（payload 里 fork:true）但 head/base 同仓库的 PR 照常受理。
func TestHandleForgejoWebhook_CrossRepoForkRejected(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-fork", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)
	ctx := context.Background()

	// head 在 fork 仓库（o/other）、base 在本仓库 → 跨仓库 PR 不受理。
	crossRepo := []byte(`{
		"action": "opened", "number": 31,
		"pull_request": {
			"draft": false, "title": "fork pr", "updated_at": "2026-01-02T03:04:05Z",
			"head": {"sha": "sha-fork", "ref": "feat", "repo": {"full_name": "other/app"}},
			"base": {"ref": "main", "repo": {"full_name": "acme/app"}}
		},
		"repository": {"full_name": "acme/app"}
	}`)
	if err := review.HandleForgejoWebhook(ctx, forgeID, "pull_request", "d-fork-1", crossRepo); err != nil {
		t.Fatalf("cross-repo webhook: %v", err)
	}
	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ?", project.ID).Count(&n)
	if n != 0 {
		t.Fatalf("跨仓库 fork PR 不得登记 seen, rows = %d", n)
	}

	// 仓库本身是 fork（repo.fork=true）但 head/base 同仓库 → 照常受理。
	sameRepoOnFork := []byte(`{
		"action": "opened", "number": 32,
		"pull_request": {
			"draft": false, "title": "same-repo on a fork", "updated_at": "2026-01-02T03:04:05Z",
			"head": {"sha": "sha-same", "ref": "feat", "repo": {"fork": true, "full_name": "acme/app"}},
			"base": {"ref": "main", "repo": {"fork": true, "full_name": "acme/app"}}
		},
		"repository": {"full_name": "acme/app", "fork": true}
	}`)
	if err := review.HandleForgejoWebhook(ctx, forgeID, "pull_request", "d-fork-2", sameRepoOnFork); err != nil {
		t.Fatalf("same-repo-on-fork webhook: %v", err)
	}
	var seen model.ReviewSeenSHA
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 32).First(&seen).Error; err != nil {
		t.Fatalf("同仓库 PR（仓库本身是 fork）应受理: %v", err)
	}
	if seen.HeadSHA != "sha-same" {
		t.Fatalf("seen head = %q", seen.HeadSHA)
	}
}

// TestHandleForgejoWebhook_SameTimestampDifferentHead 秒级 updated_at 下连推：
// payload 固定同一时间戳，两个不同 head 的投递都须受理——同刻异 head 先经
// 实时复核（live head 与 payload 一致才进），同刻同 head 早退。
func TestHandleForgejoWebhook_SameTimestampDifferentHead(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-ts", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) // 与 payload 同刻
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{
		// sha-t2 投递与 sha-t1 同刻：复核现拉到 sha-t2 才放行受理。
		forgeID: &pollStubClient{
			live: &forge.PullRequest{Number: 62, RepoFullName: "acme/app", HeadSHA: "sha-t2", UpdatedAt: &ts, State: "open"},
		},
	}}

	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-t1",
		forgejoPullPayload("synchronized", "acme/app", 62, "sha-t1", false)); err != nil {
		t.Fatalf("webhook sha-t1: %v", err)
	}
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-t2",
		forgejoPullPayload("synchronized", "acme/app", 62, "sha-t2", false)); err != nil {
		t.Fatalf("webhook sha-t2: %v", err)
	}
	// 同时刻第二个 head 不得按过期丢弃：seen 登记 + 防抖指向最新 head。
	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND pr_number = ? AND head_sha = ?",
		project.ID, 62, "sha-t2").Count(&n)
	if n != 1 {
		t.Fatalf("同时刻不同 head 必须登记 seen, rows = %d", n)
	}
	var debounce model.ReviewDebounce
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 62).First(&debounce).Error; err != nil {
		t.Fatalf("debounce: %v", err)
	}
	if debounce.HeadSHA != "sha-t2" {
		t.Fatalf("debounce head = %q, want sha-t2", debounce.HeadSHA)
	}
	// 同时刻同 head 的重复投递仍早退（不产生第二条防抖写差异——再次投递
	// 后 head 不变即可证明未误开轮）。
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-t3",
		forgejoPullPayload("synchronized", "acme/app", 62, "sha-t2", false)); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 62).First(&debounce).Error; err != nil {
		t.Fatalf("debounce2: %v", err)
	}
	if debounce.HeadSHA != "sha-t2" {
		t.Fatalf("debounce head after redelivery = %q, want sha-t2", debounce.HeadSHA)
	}
}

// TestHandleForgejoWebhook_TiedDeliveryRevalidatesLive 同刻异 head 的 webhook
// 投递：payload head 不是平台现状（现拉到别的 head）→ 丢弃不回滚总账；
// payload head 即平台现状 → 复核放行受理（乱序到达的是总账里那条）。
func TestHandleForgejoWebhook_TiedDeliveryRevalidatesLive(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-tiedwh", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) // 与 payload 同刻
	client := &pollStubClient{
		live: &forge.PullRequest{Number: 72, RepoFullName: "acme/app", HeadSHA: "sha-b", UpdatedAt: &ts, State: "open"},
	}
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{forgeID: client}}

	// sha-b 先受理（无既有 head，不走同刻守卫）。
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-b",
		forgejoPullPayload("synchronized", "acme/app", 72, "sha-b", false)); err != nil {
		t.Fatalf("webhook sha-b: %v", err)
	}
	// sha-a 迟到的同刻投递：live 仍是 sha-b → 该 payload 是旧态，丢弃，
	// 总账与防抖不得回滚到 sha-a。
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-a",
		forgejoPullPayload("synchronized", "acme/app", 72, "sha-a", false)); err != nil {
		t.Fatalf("webhook sha-a: %v", err)
	}
	var ledger model.ReviewPullRequest
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 72).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ledger.HeadSHA != "sha-b" {
		t.Fatalf("ledger head = %q, want sha-b（旧投递不得回滚总账）", ledger.HeadSHA)
	}
	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND pr_number = ? AND head_sha = ?",
		project.ID, 72, "sha-a").Count(&n)
	if n != 0 {
		t.Fatalf("旧投递 sha-a 不得登记 seen, rows = %d", n)
	}

	// 平台实际已回到 sha-a（force-push 回退）：live=sha-a 时同一 head 的
	// 投递复核放行——受理并把总账推进回 sha-a。
	client.live = &forge.PullRequest{Number: 72, RepoFullName: "acme/app", HeadSHA: "sha-a", UpdatedAt: &ts, State: "open"}
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-a2",
		forgejoPullPayload("synchronized", "acme/app", 72, "sha-a", false)); err != nil {
		t.Fatalf("webhook sha-a2: %v", err)
	}
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 72).First(&ledger).Error; err != nil {
		t.Fatalf("ledger2: %v", err)
	}
	if ledger.HeadSHA != "sha-a" {
		t.Fatalf("ledger head = %q, want sha-a（复核通过须受理）", ledger.HeadSHA)
	}
	var debounce model.ReviewDebounce
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 72).First(&debounce).Error; err != nil {
		t.Fatalf("debounce: %v", err)
	}
	if debounce.HeadSHA != "sha-a" {
		t.Fatalf("debounce head = %q, want sha-a", debounce.HeadSHA)
	}
}

// TestHandleForgejoWebhook_MissingTimestampAmbiguous 缺 updated_at 的异
// head 投递不得直放：判不出新旧按歧义交实拉复核——live 与 payload head 不
// 符（旧态）→ 丢弃且不回滚总账；live 一致（真回滚）→ 复核后受理。同时
// 覆盖「复核判丢弃的投递补登幂等账」：确定态丢弃后 delivery 已登记，平台
// 手动 redeliver 同 GUID 直接 202 去重。
func TestHandleForgejoWebhook_MissingTimestampAmbiguous(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-nots", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)
	client := &pollStubClient{
		live: &forge.PullRequest{Number: 90, RepoFullName: "acme/app", HeadSHA: "sha-b", State: "open"},
	}
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{forgeID: client}}

	// sha-b 正常受理（带时间戳）。
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-b",
		forgejoPullPayload("synchronized", "acme/app", 90, "sha-b", false)); err != nil {
		t.Fatalf("webhook sha-b: %v", err)
	}
	// 缺时间戳的乱序旧投递 sha-a：live 仍是 sha-b → 复核判旧态丢弃，
	// 总账不得回滚到 sha-a。
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-a",
		forgejoPullPayloadNoTS("synchronized", "acme/app", 90, "sha-a")); err != nil {
		t.Fatalf("webhook sha-a no-ts: %v", err)
	}
	var ledger model.ReviewPullRequest
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 90).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ledger.HeadSHA != "sha-b" {
		t.Fatalf("ledger head = %q, want sha-b（缺时间戳旧投递不得回滚总账）", ledger.HeadSHA)
	}
	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND pr_number = ? AND head_sha = ?",
		project.ID, 90, "sha-a").Count(&n)
	if n != 0 {
		t.Fatalf("sha-a 不得登记 seen, rows = %d", n)
	}
	// 复核判丢弃的投递已补登幂等账：同 GUID 手动 redeliver 直接 202 去重。
	svc.db.Model(&model.GitHubWebhookDelivery{}).Where("delivery_id = ?", "d-a").Count(&n)
	if n != 1 {
		t.Fatalf("dropped delivery d-a 应补登幂等账, rows = %d", n)
	}
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-a",
		forgejoPullPayloadNoTS("synchronized", "acme/app", 90, "sha-a")); !errors.Is(err, ErrDeliveryDuplicated) {
		t.Fatalf("redelivery d-a: err=%v, want ErrDeliveryDuplicated", err)
	}

	// 平台真回滚到 sha-a：live=sha-a 时缺时间戳的投递经复核放行受理。
	client.live = &forge.PullRequest{Number: 90, RepoFullName: "acme/app", HeadSHA: "sha-a", State: "open"}
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-a3",
		forgejoPullPayloadNoTS("synchronized", "acme/app", 90, "sha-a")); err != nil {
		t.Fatalf("webhook sha-a3: %v", err)
	}
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 90).First(&ledger).Error; err != nil {
		t.Fatalf("ledger2: %v", err)
	}
	if ledger.HeadSHA != "sha-a" {
		t.Fatalf("ledger head = %q, want sha-a（复核通过的缺戳投递须受理）", ledger.HeadSHA)
	}
}

// TestReviewTrigger_TiedBaseBindsObservedHead tiedBase 必须绑定复核时观察
// 到的已受理 head：复核与重进之间总账前进了（同秒第三个 push 抢先），
// tiedBase 失配 → 守卫再报歧义交调用方按新快照重验；命中才放行受理。
func TestReviewTrigger_TiedBaseBindsObservedHead(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-tiedbase", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	// 总账停在 sha-b（payload 固定同刻）。
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-b",
		forgejoPullPayload("synchronized", "acme/app", 73, "sha-b", false)); err != nil {
		t.Fatalf("webhook sha-b: %v", err)
	}
	mkTrigger := func(head, tiedBase string) *reviewWebhookTrigger {
		return &reviewWebhookTrigger{
			action: "synchronize", number: 73, repoFullName: "acme/app",
			headSHA: head, headRepoFull: "acme/app", baseRepoFull: "acme/app",
			updatedAt: &ts, tiedBase: tiedBase,
		}
	}
	// tiedBase 与当前 head 失配（总账在复核后前进到别的 head）→ 哨兵再
	// 触发并携带新观察值。
	var tied *tiedHeadError
	err := svc.db.Transaction(func(tx *gorm.DB) error {
		return review.handleReviewTriggerTx(tx, forgeID, mkTrigger("sha-a", "sha-x"), time.Now().UTC())
	})
	if !errors.As(err, &tied) || tied.observedHead != "sha-b" {
		t.Fatalf("stale tiedBase: err=%v observed=%v", err, tied)
	}
	var ledger model.ReviewPullRequest
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 73).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ledger.HeadSHA != "sha-b" {
		t.Fatalf("ledger head = %q, want sha-b（失配复核不得受理）", ledger.HeadSHA)
	}
	// tiedBase 命中当前 head → 复核有效，放行受理。
	if err := svc.db.Transaction(func(tx *gorm.DB) error {
		return review.handleReviewTriggerTx(tx, forgeID, mkTrigger("sha-a", "sha-b"), time.Now().UTC())
	}); err != nil {
		t.Fatalf("matched tiedBase: %v", err)
	}
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 73).First(&ledger).Error; err != nil {
		t.Fatalf("ledger2: %v", err)
	}
	if ledger.HeadSHA != "sha-a" {
		t.Fatalf("ledger head = %q, want sha-a", ledger.HeadSHA)
	}
}

// TestForgejoPoll_RespectsEnabledBaseline 启用前的存量 open PR 不算「丢的
// webhook 事件」——首 tick 全量开轮与 GitHub 只审开启后新事件不对称：
// updated_at 早于 enabled_at 基线（含时偏宽限外）的快照跳过，基线后的照常。
func TestForgejoPoll_RespectsEnabledBaseline(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-base", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)

	baseline := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	if err := svc.db.Model(&model.ProjectReviewSettings{}).
		Where("project_id = ?", project.ID).Update("enabled_at", baseline).Error; err != nil {
		t.Fatalf("baseline: %v", err)
	}
	stale := baseline.Add(-2 * time.Minute)  // 早于 基线-宽限：存量
	fresh := baseline.Add(-30 * time.Second) // 宽限内：可能是刚丢的事件
	client := &pollStubClient{byRepo: map[string][]forge.PullRequest{
		"acme/app": {
			{Number: 1, RepoFullName: "acme/app", HeadSHA: "stale-sha", UpdatedAt: &stale},
			{Number: 2, RepoFullName: "acme/app", HeadSHA: "fresh-sha", UpdatedAt: &fresh},
		},
	}}
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{forgeID: client}}
	review.pollForgejoOpenPRs(time.Now().UTC())

	var n int64
	svc.db.Model(&model.ReviewPullRequest{}).
		Where("project_id = ? AND pr_number = 1", project.ID).Count(&n)
	if n != 0 {
		t.Fatalf("存量 PR 不该被首 tick 开轮（账本 %d 行）", n)
	}
	var ledger model.ReviewPullRequest
	if err := svc.db.Where("project_id = ? AND pr_number = 2", project.ID).
		First(&ledger).Error; err != nil || ledger.HeadSHA != "fresh-sha" {
		t.Fatalf("基线后的 PR 应受理: err=%v head=%q", err, ledger.HeadSHA)
	}
}

// TestForgejoPoll_TiedLiveClosedDropped 轮询复核的 live PR 在快照与实拉之间
// 被关闭 → 与 webhook 复核同口径丢弃：已关 PR 不开轮、不回滚总账。
func TestForgejoPoll_TiedLiveClosedDropped(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-tiedcl", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)

	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-b",
		forgejoPullPayload("synchronized", "acme/app", 74, "sha-b", false)); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) // 与 payload 同刻
	client := &pollStubClient{
		byRepo: map[string][]forge.PullRequest{
			"acme/app": {{Number: 74, RepoFullName: "acme/app", HeadSHA: "sha-a", UpdatedAt: &ts}},
		},
		// 实拉时 PR 已关：同刻快照虽是歧义，closed 是确定态——丢弃。
		live: &forge.PullRequest{Number: 74, RepoFullName: "acme/app", HeadSHA: "sha-c", UpdatedAt: &ts, State: "closed"},
	}
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{forgeID: client}}
	review.pollForgejoOpenPRs(time.Now().UTC())

	var ledger model.ReviewPullRequest
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 74).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ledger.HeadSHA != "sha-b" {
		t.Fatalf("ledger head = %q, want sha-b（已关 PR 的 live 不得受理）", ledger.HeadSHA)
	}
	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND pr_number = ? AND head_sha IN ?",
		project.ID, 74, []string{"sha-a", "sha-c"}).Count(&n)
	if n != 0 {
		t.Fatalf("已关 PR 的 head 不得登记 seen, rows = %d", n)
	}
}

// TestForgejoPoll_TiedSnapshotRevalidated 轮询快照与已受理 head 同刻异 SHA：
// 快照可能先于 webhook 受理被捕获（opened 丢件、列表读到旧 head）——受理
// 它会把总账回滚、取消新 head 的轮次并留下永久漏审；必须先现拉实时 PR。
// 实时 head 与总账相同 → 快照是旧态，无事可补；实时又前进 → 受理新 head。
func TestForgejoPoll_TiedSnapshotRevalidated(t *testing.T) {
	svc := setupTestServices(t)
	forgeID := createTestForgeIntegration(t, svc.db, "https://forge.example.com").ID
	project, _ := seedForgejoReviewProject(t, svc, forgeID, "fj-tied", "acme/app", nil)
	review := newReviewTestService(t, svc, nil, nil, nil)

	// webhook 先受理 sha-b（payload updated_at 固定 2026-01-02T03:04:05Z）。
	if err := review.HandleForgejoWebhook(context.Background(), forgeID, "pull_request", "d-b",
		forgejoPullPayload("synchronized", "acme/app", 71, "sha-b", false)); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) // 与 payload 同刻
	client := &pollStubClient{
		byRepo: map[string][]forge.PullRequest{
			// 列表快照还是旧 head sha-a（webhook 受理前读的），与 sha-b 同刻。
			"acme/app": {{Number: 71, RepoFullName: "acme/app", HeadSHA: "sha-a", UpdatedAt: &ts}},
		},
		live: &forge.PullRequest{Number: 71, RepoFullName: "acme/app", HeadSHA: "sha-b", UpdatedAt: &ts, State: "open"},
	}
	review.clients = pollStubResolver{byIntegration: map[string]forge.ReviewClient{forgeID: client}}
	review.pollForgejoOpenPRs(time.Now().UTC())

	// 快照回滚不得发生：总账与防抖仍是 sha-b，sha-a 未登记 seen。
	var ledger model.ReviewPullRequest
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 71).First(&ledger).Error; err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if ledger.HeadSHA != "sha-b" {
		t.Fatalf("ledger head = %q, want sha-b（旧快照不得回滚总账）", ledger.HeadSHA)
	}
	var debounce model.ReviewDebounce
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 71).First(&debounce).Error; err != nil {
		t.Fatalf("debounce: %v", err)
	}
	if debounce.HeadSHA != "sha-b" {
		t.Fatalf("debounce head = %q, want sha-b", debounce.HeadSHA)
	}
	var n int64
	svc.db.Model(&model.ReviewSeenSHA{}).Where("project_id = ? AND pr_number = ? AND head_sha = ?",
		project.ID, 71, "sha-a").Count(&n)
	if n != 0 {
		t.Fatalf("旧快照 sha-a 不得登记 seen, rows = %d", n)
	}

	// 实时拉到的 head 又前进到 sha-c（快照依旧 sha-a 同刻）：按正常受理
	// 路径进，C 成为新 head 并获得防抖行——平局复核后新 head 不漏审。
	client.live = &forge.PullRequest{Number: 71, RepoFullName: "acme/app", HeadSHA: "sha-c", UpdatedAt: &ts, State: "open"}
	review.pollForgejoOpenPRs(time.Now().UTC())
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 71).First(&ledger).Error; err != nil {
		t.Fatalf("ledger2: %v", err)
	}
	if ledger.HeadSHA != "sha-c" {
		t.Fatalf("ledger head = %q, want sha-c", ledger.HeadSHA)
	}
	if err := svc.db.Where("project_id = ? AND pr_number = ?", project.ID, 71).First(&debounce).Error; err != nil {
		t.Fatalf("debounce3: %v", err)
	}
	if debounce.HeadSHA != "sha-c" {
		t.Fatalf("debounce head = %q, want sha-c", debounce.HeadSHA)
	}
}

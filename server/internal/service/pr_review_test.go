package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/api"
	"github.com/godbobo/fast_ship/server/internal/config"
	"github.com/godbobo/fast_ship/server/internal/githubapi"
	"github.com/godbobo/fast_ship/server/internal/model"
	"github.com/godbobo/fast_ship/server/internal/pkg/githubapp"
	"github.com/godbobo/fast_ship/server/internal/repository"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// GitHub PR 自动评审（INT-93）定向测试。调度器/事件部分复用 automation_test.go
// 的夹具；webhook/防抖/排队/同步用例见本文件分组。

// ---- 步骤 1：索引与状态语义 ----

// 部分唯一索引只约束 cron/issue 触发：同一触发器第二条未结束 cron 行必须被拒绝。
func TestPRIndex_RejectsSecondUnfinishedCronRun(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)
	tr := env.addTrigger(t, a, func(tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerCron
		tr.CronExpression = "*/10 * * * *"
	})

	env.seedRun(t, a, func(r *model.AutomationRun) {
		r.TriggerID = tr.ID
		r.TriggerType = model.AutomationTriggerCron
		r.TriggerKey = "cron:2026-01-01T00:00:00Z"
	})
	// busy 路径的失败行「有 finished_at」不算活动；这里放一条真正的未结束行验证唯一索引。
	err := env.db.Create(&model.AutomationRun{
		ID:                 uuid.NewString(),
		UserID:             a.UserID,
		AutomationID:       a.ID,
		TriggerID:          tr.ID,
		TriggerType:        model.AutomationTriggerCron,
		TriggerKey:         "cron:2026-01-02T00:00:00Z",
		TriggeredAt:        time.Now().UTC(),
		ConfigSnapshotJSON: snapshotJSON(a, tr),
		EnvironmentID:      a.EnvironmentID,
		T3ProjectID:        a.T3ProjectID,
		ThreadID:           uuid.NewString(),
		LaunchCommandID:    uuid.NewString(),
		InitialMessageID:   uuid.NewString(),
		Status:             model.RunStatusPendingRun,
		DeadlineAt:         time.Now().UTC().Add(time.Hour),
		PostState:          model.AutomationPostStateNone,
	}).Error
	if err == nil {
		t.Fatal("second unfinished cron run must violate partial unique index")
	}
}

// 同一自动化的两个 cron 触发器可以各有未结束活动行（并发约束在触发器维度）。
func TestPRIndex_AllowsActiveRunsOnSiblingTriggers(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)
	tr1 := env.addTrigger(t, a, func(tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerCron
		tr.CronExpression = "*/10 * * * *"
	})
	tr2 := env.addTrigger(t, a, func(tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerIssueCreated
	})
	env.seedRun(t, a, func(r *model.AutomationRun) {
		r.TriggerID = tr1.ID
		r.TriggerType = model.AutomationTriggerCron
		r.TriggerKey = "cron:2026-01-01T00:00:00Z"
	})
	env.seedRun(t, a, func(r *model.AutomationRun) {
		r.TriggerID = tr2.ID
		r.TriggerType = model.AutomationTriggerIssueCreated
	})
	if n := countRuns(t, env.db, "automation_id = ? AND finished_at IS NULL", a.ID); n != 2 {
		t.Fatalf("active runs across sibling triggers = %d, want 2", n)
	}
}

// PR 触发不受「同一触发器至多一个活动运行」约束：一轮多模型并行即多行未结束。
func TestPRIndex_AllowsTwoUnfinishedPRRuns(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)
	env.addTrigger(t, a, func(tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPROpened
	})
	env.seedRun(t, a, func(r *model.AutomationRun) {
		r.TriggerType = model.AutomationTriggerGitHubPROpened
		r.TriggerKey = "pr:octocat/hw:1:abc:def:claude"
		r.RepoFullName = "octocat/hw"
		r.PRNumber = 1
		r.HeadSHA = "abc"
		r.RoundNumber = 1
		r.ModelInstanceID = "inst-a"
		r.ModelName = "claude"
	})
	env.seedRun(t, a, func(r *model.AutomationRun) {
		r.TriggerType = model.AutomationTriggerGitHubPROpened
		r.TriggerKey = "pr:octocat/hw:1:abc:ghi:glm"
		r.RepoFullName = "octocat/hw"
		r.PRNumber = 1
		r.HeadSHA = "abc"
		r.RoundNumber = 1
		r.ModelInstanceID = "inst-b"
		r.ModelName = "glm"
	})
	if n := countRuns(t, env.db, "automation_id = ?", a.ID); n != 2 {
		t.Fatalf("unfinished PR runs = %d, want 2", n)
	}
}

// queued 不被认领通道取走：排队名额未提升前，调度循环不能把它派发出去。
func TestQueued_NotClaimedByListClaimableTx(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)
	env.addTrigger(t, a, func(tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerIssueCreated
	})
	env.seedRun(t, a, func(r *model.AutomationRun) {
		r.Status = model.AutomationRunQueued
		r.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		r.RepoFullName = "octocat/hw"
		r.PRNumber = 7
		r.HeadSHA = "abc123"
	})
	// 对照：空串状态可以被取走。
	env.seedRun(t, a, func(r *model.AutomationRun) {
		r.TriggerKey = "event:" + uuid.NewString()
	})

	items, err := env.runs.ListClaimableTx(env.db, 50)
	if err != nil {
		t.Fatalf("list claimable: %v", err)
	}
	for _, item := range items {
		if item.Status == model.AutomationRunQueued {
			t.Fatal("queued run must not be claimable")
		}
	}
	if len(items) != 1 {
		t.Fatalf("claimable = %d, want 1", len(items))
	}
}

// cancelled 计入终态。
func TestCancelledStatus_Terminal(t *testing.T) {
	if !(model.AutomationRunCancelled).Terminal() {
		t.Fatal("cancelled must be terminal")
	}
	if (model.AutomationRunQueued).Terminal() {
		t.Fatal("queued must not be terminal")
	}
}

// cron 忙碌路径不回退：该触发器已有活动运行时第二次 cron 触发仍记 failed/automation_busy。
func TestCronBusy_StillRecordsAutomationBusy(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	stale := time.Now().UTC().Add(-time.Hour)
	a := env.createAutomation(t, user.ID, nil)
	env.addTrigger(t, a, func(tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerCron
		tr.CronExpression = "*/10 * * * *"
		tr.NextRunAt = &stale
	})
	env.seedRun(t, a, func(r *model.AutomationRun) {
		r.TriggerType = model.AutomationTriggerCron
		r.TriggerKey = "cron:existing"
	})

	env.sched.dispatchDueCrons(time.Now().UTC())

	var busy model.AutomationRun
	if err := env.db.Where("automation_id = ? AND error_code = ?", a.ID, "automation_busy").
		First(&busy).Error; err != nil {
		t.Fatalf("busy run not recorded: %v", err)
	}
	if busy.Status != model.AutomationRunFailed {
		t.Fatalf("busy status = %q, want failed", busy.Status)
	}
}

// ---- PR 评审夹具 ----

type fakeThreadStopper struct {
	stops []string // threadID 记录
}

func (f *fakeThreadStopper) StopThread(ctx context.Context, userID, envID, threadID string) (*T3ThreadStopResult, error) {
	f.stops = append(f.stops, threadID)
	return &T3ThreadStopResult{Stopped: true, RunId: nil}, nil
}

type prReviewTestEnv struct {
	db      *gorm.DB
	pr      *PRReviewService
	fake    *githubapp.Fake
	stopper *fakeThreadStopper
	runs    *repository.AutomationRunRepository
}

func setupPRReviewTestEnv(t *testing.T) *prReviewTestEnv {
	t.Helper()
	dsn := "file:" + uuid.NewString() + "?mode=memory&cache=shared&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.User{}, &model.Project{}, &model.T3Environment{},
		&model.Automation{}, &model.AutomationTrigger{}, &model.AutomationRun{}, &model.AutomationRunToken{}, &model.Notification{},
		&model.GitHubWebhookDelivery{}, &model.GitHubPRDebounce{}, &model.GitHubPRReview{}, &model.GitHubPRSeenSHA{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_trigger_key ON automation_runs(trigger_id, trigger_key)")
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_active_once
ON automation_runs(trigger_id)
WHERE trigger_type IN ('cron','issue_created','issue_status_changed','issue_label_added')
  AND (finished_at IS NULL OR post_state = 'pending')`)
	db.Exec("CREATE INDEX IF NOT EXISTS idx_automation_runs_pr_head ON automation_runs(repo_full_name, pr_number, head_sha)")
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_notifications_user_dedup ON notifications(user_id, dedup_key)")

	fake := githubapp.NewFake()
	stopper := &fakeThreadStopper{}
	cfg := &config.Config{Server: config.ServerConfig{PublicURL: "https://fs.example.com"}}
	logger := zap.NewNop()
	pr := NewPRReviewService(db,
		repository.NewAutomationRepository(db),
		repository.NewAutomationRunRepository(db),
		repository.NewProjectRepository(db),
		repository.NewGitHubWebhookDeliveryRepository(db),
		repository.NewGitHubPRDebounceRepository(db),
		repository.NewGitHubPRReviewRepository(db),
		githubapp.StaticClients{Client: fake}, stopper, NewNotificationSink(db), cfg, logger)
	return &prReviewTestEnv{
		db: db, pr: pr, fake: fake, stopper: stopper,
		runs: repository.NewAutomationRunRepository(db),
	}
}

// createUser 建用户 + 关联 GitHub 仓库的项目。
func (e *prReviewTestEnv) createUserWithRepo(t *testing.T, owner, repo string) (*model.User, *model.Project) {
	t.Helper()
	user := &model.User{ID: uuid.NewString(), Username: "u-" + uuid.NewString()[:8], Email: uuid.NewString()[:8] + "@t", PasswordHash: "x"}
	if err := e.db.Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	project := &model.Project{
		ID:                  uuid.NewString(),
		UserID:              user.ID,
		Name:                "p-" + uuid.NewString()[:8],
		GithubOwner:         owner,
		GithubRepo:          repo,
		PublicGitHubTokenID: "tok-pr-review",
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}
	if err := e.db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	return user, project
}

// createPRRule 建 PR 触发规则（默认 opened、防抖 60、两个模型）。mutate 同时
// 拿到自动化本体与触发器行（INT-118 起触发字段在触发器上）。
func (e *prReviewTestEnv) createPRRule(t *testing.T, user *model.User, mutate func(a *model.Automation, tr *model.AutomationTrigger)) *model.Automation {
	t.Helper()
	now := time.Now().UTC()
	env := &model.T3Environment{
		ID: uuid.NewString(), UserID: user.ID, Name: "env-" + uuid.NewString()[:8],
		Address: "https://t3.example.com", BearerTokenEncrypted: []byte("x"),
		BearerExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	if err := e.db.Create(env).Error; err != nil {
		t.Fatalf("create env: %v", err)
	}
	sels := []prModelSelection{
		{InstanceId: "inst-a", Model: "claude"},
		{InstanceId: "inst-b", Model: "glm"},
	}
	selsJSON, _ := json.Marshal(sels)
	a := &model.Automation{
		ID:             uuid.NewString(),
		UserID:         user.ID,
		Name:           "PR 评审",
		Enabled:        true,
		CreatedAt:      now,
		UpdatedAt:      now,
		EnvironmentID:  env.ID,
		T3ProjectID:    "t3-proj",
		PromptTemplate: "评审 {{repo}} #{{pr_number}}",
		RuntimeMode:    "full-access",
		TimeoutSeconds: 3600,
		PostAction:     model.AutomationPostNone,
	}
	tr := &model.AutomationTrigger{
		ID:                  uuid.NewString(),
		AutomationID:        a.ID,
		TriggerType:         model.AutomationTriggerGitHubPROpened,
		Position:            0,
		ModelSelectionsJSON: string(selsJSON),
		DebounceSeconds:     60,
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if mutate != nil {
		mutate(a, tr)
	}
	if err := e.db.Create(a).Error; err != nil {
		t.Fatalf("create automation: %v", err)
	}
	tr.AutomationID = a.ID
	if err := e.db.Create(tr).Error; err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	return a
}

// webhookPayload 构造 GitHub 官方 pull_request 事件形状的 payload。
func webhookPayload(action, repo string, number int, sha string, draft, fork bool) []byte {
	return webhookPayloadAt(action, repo, number, sha, draft, fork, time.Time{})
}

func webhookPayloadAt(action, repo string, number int, sha string, draft, fork bool, updatedAt time.Time) []byte {
	owner, name, _ := strings.Cut(repo, "/")
	updatedField := ""
	if !updatedAt.IsZero() {
		updatedField = fmt.Sprintf(`"updated_at": %q,`, updatedAt.UTC().Format(time.RFC3339))
	}
	return []byte(fmt.Sprintf(`{
		"action": %q,
		"number": %d,
		"pull_request": {
			%s
			"draft": %t,
			"html_url": "https://github.com/%s/pull/%d",
			"title": "Add feature",
			"body": "desc",
			"head": {"sha": %q, "ref": "feature", "repo": {"fork": %t, "full_name": %q}},
			"base": {"ref": "main", "repo": {"full_name": %q}}
		},
		"repository": {"full_name": %q, "owner": {"login": %q}, "name": %q},
		"installation": {"id": 12345}
	}`, action, number, updatedField, draft, repo, number, sha, fork, repo, repo, repo, owner, name))
}

// handleWebhook 以给定 action/SHA 投递事件。
func (e *prReviewTestEnv) handleWebhook(t *testing.T, delivery, action, repo string, number int, sha string, draft, fork bool) error {
	t.Helper()
	return e.handleWebhookAt(t, delivery, action, repo, number, sha, draft, fork, time.Time{})
}

func (e *prReviewTestEnv) handleWebhookAt(t *testing.T, delivery, action, repo string, number int, sha string, draft, fork bool, updatedAt time.Time) error {
	t.Helper()
	return e.pr.HandleGitHubWebhook(context.Background(), "pull_request", delivery,
		webhookPayloadAt(action, repo, number, sha, draft, fork, updatedAt), "")
}

// handleWebhookAs 以指定验签身份（appUserID，空串=config 身份）投递事件：
// 用户身份场景用，模拟验签已命中该用户的候选。
func (e *prReviewTestEnv) handleWebhookAs(t *testing.T, delivery, action, repo string, number int, sha string, appUserID string) error {
	t.Helper()
	return e.pr.HandleGitHubWebhook(context.Background(), "pull_request", delivery,
		webhookPayload(action, repo, number, sha, false, false), appUserID)
}

func countDebounces(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&model.GitHubPRDebounce{}).Count(&n).Error; err != nil {
		t.Fatalf("count debounces: %v", err)
	}
	return n
}

// ---- 场景 1：delivery 重复 → 202，不产生第二轮 ----

func TestWebhook_DeliveryDedup(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })

	if err := env.handleWebhook(t, "d-1", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if err := env.handleWebhook(t, "d-1", "opened", "octocat/hw", 1, "sha-1", false, false); err != ErrDeliveryDuplicated {
		t.Fatalf("second delivery err = %v, want ErrDeliveryDuplicated", err)
	}
	if n := countRuns(t, env.db, "repo_full_name = ?", "octocat/hw"); n != 2 {
		t.Fatalf("runs = %d, want 2 (one round, two models)", n)
	}
}

// ---- 场景 2：draft、fork、未关联仓库不写运行 ----

func TestWebhook_IgnoresDraftForkUnlinked(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })

	if err := env.handleWebhook(t, "d-draft", "opened", "octocat/hw", 1, "sha-d", true, false); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if err := env.handleWebhook(t, "d-fork", "opened", "octocat/hw", 2, "sha-f", false, true); err != nil {
		t.Fatalf("fork: %v", err)
	}
	if err := env.handleWebhook(t, "d-unlinked", "opened", "other/repo", 3, "sha-u", false, false); err != nil {
		t.Fatalf("unlinked: %v", err)
	}
	if n := countRuns(t, env.db, "1=1"); n != 0 {
		t.Fatalf("runs = %d, want 0", n)
	}
	if n := countDebounces(t, env.db); n != 0 {
		t.Fatalf("debounces = %d, want 0", n)
	}
}

func TestWebhook_IgnoresRepoWithoutGitHubToken(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, project := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	if err := env.db.Model(project).Update("public_github_token_id", "").Error; err != nil {
		t.Fatalf("clear token: %v", err)
	}

	if err := env.handleWebhook(t, "d-notoken", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	if n := countRuns(t, env.db, "1=1"); n != 0 {
		t.Fatalf("runs = %d, want 0 (unbound repo is not authorized)", n)
	}
}

func TestOnLaunchCancelled_MarksStopAttempted(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	run := env.seedPRRun(t, a, "octocat/hw", 1, "sha-1", model.AutomationRunCancelled)
	now := nowUTC()
	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", run.ID).
		Updates(map[string]any{"dispatch_started_at": now, "finished_at": now, "error_code": "superseded"}).Error; err != nil {
		t.Fatalf("mark claimed: %v", err)
	}
	if err := env.db.Where("id = ?", run.ID).First(run).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}

	env.pr.OnLaunchCancelled(run, now)
	if len(env.stopper.stops) != 1 || env.stopper.stops[0] != run.ThreadID {
		t.Fatalf("stops = %v, want [%s]", env.stopper.stops, run.ThreadID)
	}
	env.pr.SyncPendingReviews(now.Add(time.Second))
	if len(env.stopper.stops) != 1 {
		t.Fatalf("stops after sync = %d, want 1 (stop_attempted_at)", len(env.stopper.stops))
	}
}

// ---- 场景 3：防抖窗口合并推送；到点每模型一行；同 SHA+模型被唯一键吃掉 ----

func TestWebhook_DebounceWindowMergesPushes(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
	})
	// opened 规则不响应 synchronize。
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		a.Name = "opened 规则"
		tr.TriggerType = model.AutomationTriggerGitHubPROpened
	})
	tr := env.firstTrigger(t, a)

	if err := env.handleWebhook(t, "d-1", "synchronize", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("first push: %v", err)
	}
	var d1 model.GitHubPRDebounce
	if err := env.db.Where("trigger_id = ?", tr.ID).First(&d1).Error; err != nil {
		t.Fatalf("debounce row: %v", err)
	}
	if d1.HeadSHA != "sha-1" {
		t.Fatalf("head sha = %q", d1.HeadSHA)
	}
	if n := countRuns(t, env.db, "1=1"); n != 0 {
		t.Fatalf("runs during window = %d, want 0", n)
	}

	// 窗口内第二次推送：只改 SHA 与 fire_at，不产生新行/新运行。
	if err := env.handleWebhook(t, "d-2", "synchronize", "octocat/hw", 1, "sha-2", false, false); err != nil {
		t.Fatalf("second push: %v", err)
	}
	var rows []model.GitHubPRDebounce
	if err := env.db.Find(&rows).Error; err != nil {
		t.Fatalf("list debounces: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("debounce rows = %d, want 1 (opened 规则不响应 synchronize)", len(rows))
	}
	if rows[0].HeadSHA != "sha-2" {
		t.Fatalf("head sha = %q, want sha-2 (latest wins)", rows[0].HeadSHA)
	}
	if rows[0].Action != "synchronize" {
		t.Fatalf("action = %q, want synchronize", rows[0].Action)
	}
	if !rows[0].FireAt.After(d1.FireAt.Add(-time.Second)) {
		t.Fatalf("fire_at = %v, want reset (>= previous %v)", rows[0].FireAt, d1.FireAt)
	}

	// 到点开轮：每个模型一行。
	env.pr.ProcessDueDebounces(rows[0].FireAt.Add(time.Second))
	if n := countRuns(t, env.db, "repo_full_name = ?", "octocat/hw"); n != 2 {
		t.Fatalf("runs after fire = %d, want 2 (one per model)", n)
	}
	if n := countDebounces(t, env.db); n != 0 {
		t.Fatalf("debounce rows after fire = %d, want 0 (consumed)", n)
	}

	// 同一 SHA+模型被唯一键吃掉：重放同一 SHA 不产生新行。
	if err := env.db.Create(&model.GitHubPRDebounce{
		TriggerID: env.firstTrigger(t, a).ID, RepoFullName: "octocat/hw", PRNumber: 1,
		UserID: user.ID, ProjectID: "p", InstallationID: 12345,
		Action: "synchronize", HeadSHA: "sha-2", FireAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}).Error; err != nil {
		t.Fatalf("reseed debounce: %v", err)
	}
	env.pr.ProcessDueDebounces(time.Now().UTC().Add(time.Minute))
	if n := countRuns(t, env.db, "repo_full_name = ?", "octocat/hw"); n != 2 {
		t.Fatalf("runs after duplicate fire = %d, want 2 (unique key eats dup)", n)
	}
	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").First(&rev).Error; err != nil {
		t.Fatalf("review: %v", err)
	}
	if rev.RoundNumber != 1 {
		t.Fatalf("duplicate SHA fire advanced round to %d, want 1", rev.RoundNumber)
	}
}

// 同一触发器、同一仓库+PR，两个验签身份（config 空串与用户）各占一行防抖，
// 各自到点开轮、互不覆盖互不误删（INT-119：防抖账按验签身份分区）。
func TestWebhook_DebouncePartitionedByIdentity(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	alice, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, alice, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 60
	})
	tr := env.firstTrigger(t, a)

	// config 身份与 alice 身份先后投递同一 repo+PR：命中同一条规则（config
	// 身份按规则归属开轮，规则是 alice 的），各写一行防抖窗口。
	if err := env.handleWebhookAs(t, "d-cfg", "synchronize", "octocat/hw", 1, "sha-cfg", ""); err != nil {
		t.Fatalf("config identity delivery: %v", err)
	}
	if err := env.handleWebhookAs(t, "d-alice", "synchronize", "octocat/hw", 1, "sha-alice", alice.ID); err != nil {
		t.Fatalf("alice identity delivery: %v", err)
	}
	var rows []model.GitHubPRDebounce
	if err := env.db.Where("trigger_id = ?", tr.ID).Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatalf("debounce rows = %d %v, want 2 (one per identity)", len(rows), err)
	}
	shaByID := map[string]string{}
	for _, r := range rows {
		shaByID[r.AppUserID] = r.HeadSHA
	}
	if shaByID[""] != "sha-cfg" || shaByID[alice.ID] != "sha-alice" {
		t.Fatalf("debounce rows by identity = %v, want config=sha-cfg alice=sha-alice", shaByID)
	}

	// config 身份的窗口先到期：只消费 config 那行，alice 的窗口原样保留。
	if err := env.db.Model(&model.GitHubPRDebounce{}).
		Where("trigger_id = ? AND app_user_id = ?", tr.ID, "").
		Update("fire_at", time.Now().UTC().Add(time.Second)).Error; err != nil {
		t.Fatalf("rearm config window: %v", err)
	}
	var aliceFireAt time.Time
	if err := env.db.Model(&model.GitHubPRDebounce{}).
		Where("trigger_id = ? AND app_user_id = ?", tr.ID, alice.ID).
		Select("fire_at").Scan(&aliceFireAt).Error; err != nil || aliceFireAt.IsZero() {
		t.Fatalf("alice fire_at: %v %v", aliceFireAt, err)
	}
	env.pr.ProcessDueDebounces(time.Now().UTC().Add(2 * time.Second))
	var live []model.GitHubPRDebounce
	if err := env.db.Where("trigger_id = ?", tr.ID).Find(&live).Error; err != nil || len(live) != 1 {
		t.Fatalf("debounce rows after config fired = %d %v, want 1 (alice's)", len(live), err)
	}
	if len(live) == 1 && (live[0].AppUserID != alice.ID || live[0].HeadSHA != "sha-alice") {
		t.Fatalf("survivor = %s/%s, want alice/sha-alice", live[0].AppUserID, live[0].HeadSHA)
	}
	// config 开的轮按 config 身份记（运行行 app_user_id=''、SHA 是它的）。
	var cfgRuns, aliceRuns []model.AutomationRun
	if err := env.db.Where("repo_full_name = ? AND app_user_id = ?", "octocat/hw", "").Find(&cfgRuns).Error; err != nil || len(cfgRuns) != 2 {
		t.Fatalf("config runs = %d %v, want 2", len(cfgRuns), err)
	}
	for _, run := range cfgRuns {
		if run.HeadSHA != "sha-cfg" || run.Status == model.AutomationRunCancelled {
			t.Fatalf("config run = %s/%s, want sha-cfg live", run.HeadSHA, run.Status)
		}
	}

	// alice 的窗口到点：按她自己的 SHA 开轮，config 的在途不受影响。
	if err := env.db.Model(&model.GitHubPRDebounce{}).
		Where("trigger_id = ? AND app_user_id = ?", tr.ID, alice.ID).
		Update("fire_at", time.Now().UTC().Add(time.Second)).Error; err != nil {
		t.Fatalf("rearm alice window: %v", err)
	}
	env.pr.ProcessDueDebounces(time.Now().UTC().Add(2 * time.Second))
	if n := countDebounces(t, env.db); n != 0 {
		t.Fatalf("debounce rows after both fired = %d, want 0", n)
	}
	if err := env.db.Where("repo_full_name = ? AND app_user_id = ?", "octocat/hw", alice.ID).Find(&aliceRuns).Error; err != nil || len(aliceRuns) != 2 {
		t.Fatalf("alice runs = %d %v, want 2", len(aliceRuns), err)
	}
	for _, run := range aliceRuns {
		if run.HeadSHA != "sha-alice" || run.Status == model.AutomationRunCancelled {
			t.Fatalf("alice run = %s/%s, want sha-alice live", run.HeadSHA, run.Status)
		}
	}
	for _, run := range cfgRuns {
		var fresh model.AutomationRun
		if err := env.db.Where("id = ?", run.ID).First(&fresh).Error; err != nil {
			t.Fatalf("reload config run: %v", err)
		}
		if fresh.Status == model.AutomationRunCancelled {
			t.Fatalf("config run %s cancelled by alice's round", run.ID)
		}
	}
}

// ---- 场景 4：debounce=0 在请求事务里开轮并提升 ----

func TestWebhook_DebounceZeroOpensRoundInTx(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })

	if err := env.handleWebhook(t, "d-1", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	var runs []model.AutomationRun
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").Find(&runs).Error; err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(runs))
	}
	for _, run := range runs {
		if run.Status != model.RunStatusPendingRun {
			t.Fatalf("run status = %q, want '' (promoted from queued)", run.Status)
		}
		if !strings.HasPrefix(run.TriggerKey, "pr:octocat%2Fhw:1:sha-1:") {
			t.Fatalf("trigger_key = %q", run.TriggerKey)
		}
		if run.RoundNumber != 1 {
			t.Fatalf("round = %d, want 1", run.RoundNumber)
		}
		if run.InstallationID != 12345 {
			t.Fatalf("installation = %d, want 12345", run.InstallationID)
		}
		// config_snapshot.model_selection 是单模型对象。
		var snap automationConfigSnapshot
		if err := json.Unmarshal([]byte(run.ConfigSnapshotJSON), &snap); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if len(snap.ModelSelection) == 0 {
			t.Fatal("snapshot must carry single model_selection")
		}
		var sel map[string]any
		if err := json.Unmarshal(snap.ModelSelection, &sel); err != nil || sel["instance_id"] == nil {
			t.Fatalf("model_selection = %s", snap.ModelSelection)
		}
	}
	// 轮次账已建且待同步。
	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").First(&rev).Error; err != nil {
		t.Fatalf("review row: %v", err)
	}
	if !rev.SyncPending || rev.RoundNumber != 1 {
		t.Fatalf("review row pending=%t round=%d", rev.SyncPending, rev.RoundNumber)
	}
	// webhook 事务期间 GitHub 调用数为 0（kick 不会立即执行同步；直查计数）。
	if n := env.fake.TotalCalls(); n != 0 {
		t.Fatalf("github calls during webhook = %d, want 0", n)
	}
}

// ---- 场景 5：第 6 个运行保持 queued；结束后下一次提升 ----

func TestPromote_InflightCapAndRelease(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })

	// 占 4 个在途名额（手工 seed 的 PR 运行），留 1 个空位。
	for i := 0; i < prReviewMaxInflightPerUser-1; i++ {
		env.seedPRRun(t, a, "octocat/hw", 100+i, fmt.Sprintf("seed-sha-%d", i), model.RunStatusPendingRun)
	}
	// 新一轮：2 个模型 → 1 个提升到 ''、1 个保持 queued。
	if err := env.handleWebhook(t, "d-1", "opened", "octocat/hw", 1, "sha-new", false, false); err != nil {
		t.Fatalf("webhook: %v", err)
	}

	var queued []model.AutomationRun
	if err := env.db.Where("repo_full_name = ? AND status = ?", "octocat/hw", model.AutomationRunQueued).Find(&queued).Error; err != nil {
		t.Fatalf("queued: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("queued = %d, want 1", len(queued))
	}
	inflight, err := env.runs.CountPRInflightTx(env.db, user.ID)
	if err != nil {
		t.Fatalf("count inflight: %v", err)
	}
	if inflight != prReviewMaxInflightPerUser {
		t.Fatalf("inflight = %d, want %d (queued 不计入)", inflight, prReviewMaxInflightPerUser)
	}

	// 结束一个在途运行 → 下一次提升把它变成 ''。
	now := time.Now().UTC()
	if err := env.db.Model(&model.AutomationRun{}).
		Where("repo_full_name = ? AND status = ?", "octocat/hw", model.RunStatusPendingRun).
		Order("triggered_at ASC").Limit(1).
		Updates(map[string]any{"status": model.AutomationRunCompleted, "finished_at": now}).Error; err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		return env.pr.promoteQueuedPRRunsTx(tx, user.ID, now)
	}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	var promoted model.AutomationRun
	if err := env.db.Where("id = ?", queued[0].ID).First(&promoted).Error; err != nil {
		t.Fatalf("reload queued: %v", err)
	}
	if promoted.Status != model.RunStatusPendingRun {
		t.Fatalf("promoted status = %q, want ''", promoted.Status)
	}
	// 超时从真正开始派发算：deadline 被重置到未来。
	if !promoted.DeadlineAt.After(now) {
		t.Fatalf("deadline = %v, want reset to future", promoted.DeadlineAt)
	}
}

func TestPromote_CrossPRMarksReviewPending(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	var seed *model.AutomationRun
	for i := 0; i < prReviewMaxInflightPerUser; i++ {
		run := env.seedPRRun(t, a, "octocat/hw", 100+i, fmt.Sprintf("seed-sha-%d", i), model.RunStatusPendingRun)
		if i == 0 {
			seed = run
		}
	}
	if err := env.handleWebhook(t, "d-b", "opened", "octocat/hw", 2, "sha-b", false, false); err != nil {
		t.Fatalf("webhook B: %v", err)
	}
	var queued []model.AutomationRun
	if err := env.db.Where("pr_number = ? AND status = ?", 2, model.AutomationRunQueued).Find(&queued).Error; err != nil {
		t.Fatalf("queued: %v", err)
	}
	if len(queued) != 2 {
		t.Fatalf("queued on PR B = %d, want 2", len(queued))
	}

	now := time.Now().UTC()
	env.pr.SyncPendingReviews(now)
	if comment := env.fake.LastCommentBody(); !strings.Contains(comment, "排队中") {
		t.Fatalf("queued comment = %q, want 排队中", comment)
	}
	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", seed.ID).
		Updates(map[string]any{"status": model.AutomationRunCompleted, "finished_at": now}).Error; err != nil {
		t.Fatalf("finish seed: %v", err)
	}
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		return env.pr.promoteQueuedPRRunsTx(tx, user.ID, now)
	}); err != nil {
		t.Fatalf("promote: %v", err)
	}

	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ? AND pr_number = ?", "octocat/hw", 2).First(&rev).Error; err != nil {
		t.Fatalf("review B: %v", err)
	}
	if !rev.SyncPending {
		t.Fatal("promoted PR B must be sync_pending so GitHub leaves 排队中")
	}
	env.pr.SyncPendingReviews(now.Add(time.Second))
	if comment := env.fake.LastCommentBody(); !strings.Contains(comment, "进行中") {
		t.Fatalf("comment after promote = %q, want 进行中", comment)
	}
}

// firstTrigger 该自动化的第一个触发器（夹具里每条规则至少挂一个）。
func (e *prReviewTestEnv) firstTrigger(t *testing.T, a *model.Automation) *model.AutomationTrigger {
	t.Helper()
	var tr model.AutomationTrigger
	if err := e.db.Where("automation_id = ?", a.ID).Order("position ASC, created_at ASC").First(&tr).Error; err != nil {
		t.Fatalf("first trigger: %v", err)
	}
	return &tr
}

// seedPRRun 直接落一条 PR 运行（trigger_id/trigger_type 取该规则的第一个触发器；
// trigger_key 带随机后缀，允许同 SHA 多行布置现场）。
func (e *prReviewTestEnv) seedPRRun(t *testing.T, a *model.Automation, repo string, prNumber int, sha string, status model.AutomationRunStatus) *model.AutomationRun {
	t.Helper()
	tr := e.firstTrigger(t, a)
	now := time.Now().UTC()
	run := &model.AutomationRun{
		ID:                 uuid.NewString(),
		UserID:             a.UserID,
		AutomationID:       a.ID,
		TriggerID:          tr.ID,
		TriggerType:        tr.TriggerType,
		TriggerKey:         prTriggerKey(repo, prNumber, sha, "inst-"+uuid.NewString()[:8], "model-x", ""),
		TriggerContextJSON: "{}",
		TriggeredAt:        now,
		ConfigSnapshotJSON: snapshotJSON(a, tr),
		EnvironmentID:      a.EnvironmentID,
		T3ProjectID:        a.T3ProjectID,
		ThreadID:           uuid.NewString(),
		LaunchCommandID:    uuid.NewString(),
		InitialMessageID:   uuid.NewString(),
		Status:             status,
		DeadlineAt:         now.Add(time.Duration(a.TimeoutSeconds) * time.Second),
		PostState:          model.AutomationPostStateNone,
		RepoFullName:       repo,
		PRNumber:           prNumber,
		HeadSHA:            sha,
		RoundNumber:        1,
		InstallationID:     12345,
		ModelInstanceID:    "inst-x",
		ModelName:          "model-x",
	}
	if err := e.db.Create(run).Error; err != nil {
		t.Fatalf("seed pr run: %v", err)
	}
	return run
}

// ---- 场景 6：新 SHA 取消旧 SHA 的排队与进行中 ----

func TestWebhook_SupersedeCancelsOldSHARuns(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })

	// 旧 SHA：一个排队中（未认领）。
	queued := env.seedPRRun(t, a, "octocat/hw", 1, "sha-1", model.AutomationRunQueued)
	claimed := nowUTC()
	// 排队运行也签发一个令牌：取消后必须被消费（回报立即失效）。
	if err := env.db.Create(&model.AutomationRunToken{
		RunID:     queued.ID,
		TokenHash: "hash-" + queued.ID,
		CreatedAt: claimed,
		ExpiresAt: claimed.Add(time.Hour),
	}).Error; err != nil {
		t.Fatalf("seed token: %v", err)
	}

	if err := env.handleWebhook(t, "d-2", "synchronize", "octocat/hw", 1, "sha-2", false, false); err != nil {
		t.Fatalf("webhook: %v", err)
	}

	for _, id := range []string{queued.ID} {
		var run model.AutomationRun
		if err := env.db.Where("id = ?", id).First(&run).Error; err != nil {
			t.Fatalf("reload run %s: %v", id, err)
		}
		if run.Status != model.AutomationRunCancelled || run.ErrorCode != "superseded" || run.PostState != model.AutomationPostStateNone {
			t.Fatalf("run %s status=%s err=%s post=%s, want cancelled/superseded/none", id, run.Status, run.ErrorCode, run.PostState)
		}
		if run.FinishedAt == nil {
			t.Fatalf("run %s must have finished_at", id)
		}
	}
	var token model.AutomationRunToken
	if err := env.db.Where("run_id = ?", queued.ID).First(&token).Error; err != nil {
		t.Fatalf("token: %v", err)
	}
	if token.ConsumedAt == nil {
		t.Fatal("token must be consumed on supersede")
	}
	// webhook 事务期间 fake GitHub 调用数为 0。
	if n := env.fake.TotalCalls(); n != 0 {
		t.Fatalf("github calls during webhook tx = %d, want 0", n)
	}

	// 排队取消不调 StopThread（queued 没有被认领过）。
	env.pr.SyncPendingReviews(nowUTC().Add(time.Second))
	if len(env.stopper.stops) != 0 {
		t.Fatalf("stops for queued-only cancel = %v, want empty", env.stopper.stops)
	}
	// 评论里出现「已取消」（没有新轮时取消也要同步出去）。
	env.pr.SyncPendingReviews(nowUTC().Add(time.Second))
	if comment := env.fake.LastCommentBody(); !strings.Contains(comment, "已取消") {
		t.Fatalf("comment should show cancelled: %s", comment)
	}

	// 再造一个「已认领后被取消」的运行：sync 应对它调 StopThread。
	running2 := env.seedPRRun(t, a, "octocat/hw", 1, "sha-2", model.AutomationRunRunning)
	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", running2.ID).
		Updates(map[string]any{"dispatch_started_at": claimed}).Error; err != nil {
		t.Fatalf("mark claimed2: %v", err)
	}
	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", running2.ID).
		Updates(map[string]any{
			"status": model.AutomationRunCancelled, "finished_at": nowUTC(),
			"error_code": "superseded", "post_state": model.AutomationPostStateNone,
		}).Error; err != nil {
		t.Fatalf("cancel running2: %v", err)
	}
	env.pr.SyncPendingReviews(nowUTC().Add(2 * time.Second))
	if len(env.stopper.stops) != 1 || env.stopper.stops[0] != running2.ThreadID {
		t.Fatalf("stops = %v, want [running2.ThreadID]", env.stopper.stops)
	}
	// 只尝试一次：再次 sync 不重复中断。
	env.pr.SyncPendingReviews(nowUTC().Add(3 * time.Second))
	if len(env.stopper.stops) != 1 {
		t.Fatalf("stops after resync = %d, want 1 (stop_attempted_at marker)", len(env.stopper.stops))
	}
}

// 取消释放名额：满载后新 SHA 取消在途运行，新一轮可立即提升。
func TestWebhook_SupersedeReleasesSlots(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 0
	})
	for i := 0; i < prReviewMaxInflightPerUser; i++ {
		env.seedPRRun(t, a, "octocat/hw", 1, "sha-1b", model.RunStatusPendingRun)
	}
	// 同 SHA 的新推送不取消在途：新一轮保持排队（5 个名额已满）。
	if err := env.handleWebhook(t, "d-1", "synchronize", "octocat/hw", 1, "sha-1b", false, false); err != nil {
		t.Fatalf("webhook 1: %v", err)
	}
	if n := countRunsQueued(t, env.db, "octocat/hw"); n != 2 {
		t.Fatalf("queued = %d, want 2", n)
	}
	// 新 SHA 取消全部 5 个在途 → 新一轮（sha-2，2 个模型）全部可提升。
	if err := env.handleWebhook(t, "d-2", "synchronize", "octocat/hw", 1, "sha-2", false, false); err != nil {
		t.Fatalf("webhook 2: %v", err)
	}
	if n := countRunsQueued(t, env.db, "octocat/hw"); n != 0 {
		t.Fatalf("queued after supersede = %d, want 0 (all promoted)", n)
	}
	inflight, err := env.runs.CountPRInflightTx(env.db, user.ID)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if inflight != 2 {
		t.Fatalf("inflight = %d, want 2 (new round only)", inflight)
	}
}

func countRunsQueued(t *testing.T, db *gorm.DB, repo string) int64 {
	t.Helper()
	return countRuns(t, db, "repo_full_name = ? AND status = ?", repo, model.AutomationRunQueued)
}

// ---- 场景 7：timed_out / failed → check failure，评论格失败 ----

func TestSync_TerminalStatesMapToChecksAndComment(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })

	// 同一轮（repo+pr+round）三个模型运行：完成、超时、排队（覆盖三种映射）。
	done := env.seedPRRun(t, a, "octocat/hw", 1, "sha-1", model.AutomationRunCompleted)
	broken := env.seedPRRun(t, a, "octocat/hw", 1, "sha-1", model.AutomationRunTimedOut)
	waiting := env.seedPRRun(t, a, "octocat/hw", 1, "sha-1", model.AutomationRunQueued)
	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", broken.ID).
		Updates(map[string]any{"error_code": "run_timeout", "finished_at": nowUTC(),
			"model_name": "glm-timeout", "model_instance_id": "inst-t"}).Error; err != nil {
		t.Fatalf("mark timeout: %v", err)
	}
	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", done.ID).
		Updates(map[string]any{"model_name": "claude-done", "model_instance_id": "inst-c"}).Error; err != nil {
		t.Fatalf("rename done: %v", err)
	}
	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", waiting.ID).
		Updates(map[string]any{"model_name": "gpt-wait", "model_instance_id": "inst-w"}).Error; err != nil {
		t.Fatalf("rename waiting: %v", err)
	}
	if err := env.db.Create(&model.GitHubPRReview{
		RepoFullName: "octocat/hw", PRNumber: 1, InstallationID: 12345,
		RoundNumber: 1, HeadSHA: "sha-1", SyncPending: true, UpdatedAt: nowUTC(),
	}).Error; err != nil {
		t.Fatalf("seed review: %v", err)
	}

	env.pr.SyncPendingReviews(nowUTC().Add(time.Second))

	comment := env.fake.LastCommentBody()
	if !strings.Contains(comment, "失败") || !strings.Contains(comment, "完成") || !strings.Contains(comment, "排队中") {
		t.Fatalf("comment missing status cells: %s", comment)
	}
	checks := env.fake.CheckRuns("octocat/hw")
	if len(checks) != 3 {
		t.Fatalf("checks = %d, want 3 (one per model/run)", len(checks))
	}
	conclusions := map[string]string{}
	var hasFailure, hasSuccess, hasInProgress bool
	for _, c := range checks {
		conclusions[c.Name] = string(c.Update.Conclusion)
		switch c.Update.Conclusion {
		case githubapi.ConclusionFailure:
			hasFailure = true
		case githubapi.ConclusionSuccess:
			hasSuccess = true
		}
		if c.Update.Status == githubapi.CheckQueued {
			hasInProgress = true
		}
	}
	if !hasFailure || !hasSuccess || !hasInProgress {
		t.Fatalf("conclusions = %v, want failure + success + queued", conclusions)
	}
	// 同步成功后清位。
	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").First(&rev).Error; err != nil {
		t.Fatalf("review: %v", err)
	}
	if rev.SyncPending || rev.SyncError != "" {
		t.Fatalf("sync pending=%t err=%q, want cleared", rev.SyncPending, rev.SyncError)
	}
}

// 超时路径把 sync_pending 置位（调度循环的 markTimeouts 事务末尾）。
func TestTimeout_MarksPRReviewPending(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	run := env.seedPRRun(t, a, "octocat/hw", 1, "sha-1", model.AutomationRunDispatched)
	past := nowUTC().Add(-time.Minute)
	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", run.ID).
		Update("deadline_at", past).Error; err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if err := env.db.Create(&model.GitHubPRReview{
		RepoFullName: "octocat/hw", PRNumber: 1, InstallationID: 12345,
		RoundNumber: 1, HeadSHA: "sha-1", SyncPending: false, UpdatedAt: nowUTC(),
	}).Error; err != nil {
		t.Fatalf("seed review: %v", err)
	}

	sink := NewNotificationSink(env.db)
	sched := NewAutomationScheduler(env.db,
		repository.NewAutomationRepository(env.db), env.runs,
		repository.NewAutomationRunTokenRepository(env.db), nil, sink,
		repository.NewIssueRepository(env.db),
		&config.Config{Server: config.ServerConfig{PublicURL: "https://fs.example.com"}}, zap.NewNop())
	sched.markTimeouts(nowUTC())

	var fresh model.AutomationRun
	if err := env.db.Where("id = ?", run.ID).First(&fresh).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if fresh.Status != model.AutomationRunTimedOut {
		t.Fatalf("status = %q, want timed_out", fresh.Status)
	}
	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").First(&rev).Error; err != nil {
		t.Fatalf("review: %v", err)
	}
	if !rev.SyncPending {
		t.Fatal("timeout must set sync_pending")
	}
}

// ---- 场景 10：同一 PR 只更新同一条评论，每个模型一个 check ----

func TestSync_OneCommentPerPRAndPerModelChecks(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 0
	})

	// 第一轮：开轮 + 同步。
	if err := env.handleWebhook(t, "d-1", "synchronize", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	env.pr.SyncPendingReviews(nowUTC().Add(time.Second))
	if comments := env.fake.Comments("octocat/hw", 1); len(comments) != 1 {
		t.Fatalf("comments after round 1 = %d, want 1", len(comments))
	}
	checks1 := env.fake.CheckRuns("octocat/hw")
	if len(checks1) != 2 {
		t.Fatalf("checks after round 1 = %d, want 2 (one per model)", len(checks1))
	}
	for _, c := range checks1 {
		if !strings.HasPrefix(c.Name, "Fast Ship review (") {
			t.Fatalf("check name = %q, want Fast Ship review (<model>)", c.Name)
		}
	}

	// 第二轮（新 SHA）：评论仍是同一条；新 SHA 新 check。
	if err := env.handleWebhook(t, "d-2", "synchronize", "octocat/hw", 1, "sha-2", false, false); err != nil {
		t.Fatalf("round 2: %v", err)
	}
	env.pr.SyncPendingReviews(nowUTC().Add(2 * time.Second))
	if comments := env.fake.Comments("octocat/hw", 1); len(comments) != 1 {
		t.Fatalf("comments after round 2 = %d, want 1 (same thread updated)", len(comments))
	}
	comment := env.fake.LastCommentBody()
	if !strings.Contains(comment, "sha-2") {
		var revRow model.GitHubPRReview
		env.db.Where("repo_full_name = ?", "octocat/hw").First(&revRow)
		t.Fatalf("comment should show current round head sha: %q (round=%d pending=%t err=%q)", comment, revRow.RoundNumber, revRow.SyncPending, revRow.SyncError)
	}
	// 旧轮运行已取消、旧 check 更新为 cancelled；新 SHA 各模型新 check。
	var round2Runs []model.AutomationRun
	if err := env.db.Where("repo_full_name = ? AND head_sha = ?", "octocat/hw", "sha-2").
		Find(&round2Runs).Error; err != nil {
		t.Fatalf("round2 runs: %v", err)
	}
	for _, run := range round2Runs {
		if run.CheckRunID == nil {
			t.Fatalf("round2 run %s must have check", run.ID)
		}
	}
	var round1Runs []model.AutomationRun
	if err := env.db.Where("repo_full_name = ? AND head_sha = ?", "octocat/hw", "sha-1").
		Find(&round1Runs).Error; err != nil {
		t.Fatalf("round1 runs: %v", err)
	}
	checkByID := map[int64]fakeCheckState{}
	for _, c := range env.fake.CheckRuns("octocat/hw") {
		checkByID[c.ID] = fakeCheckState{name: c.Name, conclusion: string(c.Update.Conclusion)}
	}
	for _, run := range round1Runs {
		if run.Status != model.AutomationRunCancelled {
			t.Fatalf("round1 run status = %q, want cancelled", run.Status)
		}
		if run.CheckRunID == nil {
			t.Fatalf("round1 run %s must have check", run.ID)
		}
		st, ok := checkByID[*run.CheckRunID]
		if !ok {
			t.Fatalf("round1 run check %d missing", *run.CheckRunID)
		}
		if st.conclusion != "cancelled" {
			t.Fatalf("round1 check conclusion = %q, want cancelled", st.conclusion)
		}
	}
}

type fakeCheckState struct {
	name       string
	conclusion string
}

// ---- 403 退回合并 check ----

func TestSync_ChecksForbiddenFallsBackToMergedCheck(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	env.fake.ForbidChecks(true)

	if err := env.handleWebhook(t, "d-1", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("round: %v", err)
	}
	env.pr.SyncPendingReviews(nowUTC().Add(time.Second))

	checks := env.fake.CheckRuns("octocat/hw")
	if len(checks) != 1 {
		t.Fatalf("checks = %d, want 1 merged check", len(checks))
	}
	if checks[0].Name != "Fast Ship review" {
		t.Fatalf("merged check name = %q", checks[0].Name)
	}
	// 两个运行共享同一个 check id。
	var runs []model.AutomationRun
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").Find(&runs).Error; err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 2 || runs[0].CheckRunID == nil || *runs[0].CheckRunID != *runs[1].CheckRunID {
		t.Fatalf("runs must share the merged check id")
	}
}

func TestSync_ChecksFullyForbiddenStillPushesComment(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	env.fake.ForbidAllChecks(true)

	if err := env.handleWebhook(t, "d-1", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("round: %v", err)
	}
	env.pr.SyncPendingReviews(nowUTC().Add(time.Second))

	if n := len(env.fake.CheckRuns("octocat/hw")); n != 0 {
		t.Fatalf("checks = %d, want 0 when Checks API fully forbidden", n)
	}
	if comments := env.fake.Comments("octocat/hw", 1); len(comments) != 1 {
		t.Fatalf("comments = %d, want 1 (comment still pushed)", len(comments))
	}
	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").First(&rev).Error; err != nil {
		t.Fatalf("review: %v", err)
	}
	if rev.SyncPending {
		t.Fatalf("sync_pending still set; sync_error=%q", rev.SyncError)
	}
	if rev.SyncError != "" {
		t.Fatalf("sync_error = %q, want empty", rev.SyncError)
	}
	var runs []model.AutomationRun
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").Find(&runs).Error; err != nil {
		t.Fatalf("runs: %v", err)
	}
	for _, run := range runs {
		if run.CheckRunID != nil {
			t.Fatalf("run %s has check id %d, want none", run.ID, *run.CheckRunID)
		}
	}
}

// ---- 派发提示词：PR 占位符与代发端点说明 ----

func TestBuildRunPrompt_PRPlaceholders(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.DebounceSeconds = 0
		a.PromptTemplate = "审查 {{repo}} #{{pr_number}} {{pr_title}} {{pr_url}} @{{head_sha}}"
	})

	run := env.seedPRRun(t, a, "octocat/hw", 1, "sha-1", model.RunStatusPendingRun)
	if err := env.db.Model(&model.AutomationRun{}).Where("id = ?", run.ID).
		Update("trigger_context_json", `{"project_id":"p","action":"synchronize","repo_full_name":"octocat/hw","pr_number":1,"pr_title":"Add feature","pr_url":"https://github.com/octocat/hw/pull/1","head_sha":"sha-1"}`).Error; err != nil {
		t.Fatalf("set ctx: %v", err)
	}
	fresh, _ := env.runs.FindByID(context.Background(), run.ID)

	sched := NewAutomationScheduler(env.db,
		repository.NewAutomationRepository(env.db), env.runs,
		repository.NewAutomationRunTokenRepository(env.db), nil, NewNotificationSink(env.db),
		repository.NewIssueRepository(env.db),
		&config.Config{Server: config.ServerConfig{PublicURL: "https://fs.example.com"}}, zap.NewNop())
	prompt := sched.buildRunPrompt(fresh, parseAutomationSnapshot(fresh.ConfigSnapshotJSON), "fsr_token", "https://fs.example.com")

	for _, want := range []string{
		"审查 octocat/hw #1 Add feature https://github.com/octocat/hw/pull/1 @sha-1",
		"/api/automation-agent/pull-request/diff",
		"/api/automation-agent/pull-request/review",
		"fsr_token",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	// 未替换的占位符被清空。
	if strings.Contains(prompt, "{{repo}}") {
		t.Fatal("unreplaced placeholder must be cleared")
	}
}

func TestReviewUpsert_PreservesCommentID(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	reviews := repository.NewGitHubPRReviewRepository(env.db)
	now := nowUTC()
	if err := env.db.Create(&model.GitHubPRReview{
		RepoFullName: "octocat/hw", PRNumber: 1, InstallationID: 1,
		CommentID: 42, RoundNumber: 1, HeadSHA: "sha-1", SyncPending: false, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		return reviews.UpsertTx(tx, &model.GitHubPRReview{
			RepoFullName: "octocat/hw", PRNumber: 1, InstallationID: 1,
			RoundNumber: 2, HeadSHA: "sha-2", SyncPending: true, UpdatedAt: now.Add(time.Second),
		})
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").First(&rev).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if rev.CommentID != 42 {
		t.Fatalf("comment_id = %d, want 42 (new round must keep the robot comment)", rev.CommentID)
	}
	if rev.RoundNumber != 2 || rev.HeadSHA != "sha-2" || !rev.SyncPending {
		t.Fatalf("round/sha/pending not advanced: %+v", rev)
	}
}

func TestCheckUpdateForGroup_TerminalStates(t *testing.T) {
	t.Parallel()
	failed := model.AutomationRun{Status: model.AutomationRunFailed, HeadSHA: "abc"}
	timedOut := model.AutomationRun{Status: model.AutomationRunTimedOut, HeadSHA: "abc"}
	cancelled := model.AutomationRun{Status: model.AutomationRunCancelled, HeadSHA: "abc"}
	completed := model.AutomationRun{Status: model.AutomationRunCompleted, HeadSHA: "abc"}
	queued := model.AutomationRun{Status: model.AutomationRunQueued, HeadSHA: "abc"}
	running := model.AutomationRun{Status: model.AutomationRunRunning, HeadSHA: "abc"}

	g := checkUpdateForGroup([]model.AutomationRun{failed, timedOut})
	if g.Status != githubapi.CheckCompleted || g.Conclusion != githubapi.ConclusionFailure {
		t.Fatalf("failed round = %s/%s, want completed/failure", g.Status, g.Conclusion)
	}
	g = checkUpdateForGroup([]model.AutomationRun{cancelled, cancelled})
	if g.Status != githubapi.CheckCompleted || g.Conclusion != githubapi.ConclusionCancelled {
		t.Fatalf("cancelled round = %s/%s, want completed/cancelled", g.Status, g.Conclusion)
	}
	g = checkUpdateForGroup([]model.AutomationRun{completed, cancelled})
	if g.Status != githubapi.CheckCompleted || g.Conclusion != githubapi.ConclusionSuccess {
		t.Fatalf("completed+cancelled = %s/%s, want completed/success", g.Status, g.Conclusion)
	}
	g = checkUpdateForGroup([]model.AutomationRun{queued, running})
	if g.Status != githubapi.CheckInProgress {
		t.Fatalf("still running = %s, want in_progress", g.Status)
	}
}

// 超时/结束后调度循环必须提升排队——不能只在开轮事务里 promote。
func TestTick_PromotesQueuedAfterInflightDrops(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	for i := 0; i < prReviewMaxInflightPerUser-1; i++ {
		env.seedPRRun(t, a, "octocat/hw", 100+i, fmt.Sprintf("seed-sha-%d", i), model.RunStatusPendingRun)
	}
	if err := env.handleWebhook(t, "d-1", "opened", "octocat/hw", 1, "sha-new", false, false); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	var queued model.AutomationRun
	if err := env.db.Where("repo_full_name = ? AND status = ?", "octocat/hw", model.AutomationRunQueued).
		First(&queued).Error; err != nil {
		t.Fatalf("queued: %v", err)
	}
	now := time.Now().UTC()
	if err := env.db.Model(&model.AutomationRun{}).
		Where("repo_full_name = ? AND status = ?", "octocat/hw", model.RunStatusPendingRun).
		Order("triggered_at ASC").Limit(1).
		Updates(map[string]any{"status": model.AutomationRunCompleted, "finished_at": now}).Error; err != nil {
		t.Fatalf("finish run: %v", err)
	}

	sched := NewAutomationScheduler(env.db,
		repository.NewAutomationRepository(env.db), env.runs,
		repository.NewAutomationRunTokenRepository(env.db), nil, NewNotificationSink(env.db),
		repository.NewIssueRepository(env.db),
		&config.Config{Server: config.ServerConfig{PublicURL: "https://fs.example.com"}}, zap.NewNop())
	sched.UsePRReview(env.pr)
	sched.tick(now)

	var promoted model.AutomationRun
	if err := env.db.Where("id = ?", queued.ID).First(&promoted).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if promoted.Status != model.RunStatusPendingRun {
		t.Fatalf("tick did not promote queued: status=%q", promoted.Status)
	}
}

// 同一 SHA 的多条规则共享一轮，不能各自 NextRound 把评论账推到空轮。
func TestOpenRound_SameSHASharesRoundAcrossRules(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		a.Name = "第二条 opened"
		tr.DebounceSeconds = 0
	})
	if err := env.handleWebhook(t, "d-1", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	var runs []model.AutomationRun
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").Find(&runs).Error; err != nil {
		t.Fatalf("runs: %v", err)
	}
	if len(runs) != 4 {
		t.Fatalf("runs = %d, want 4 (2 rules × 2 models)", len(runs))
	}
	for _, run := range runs {
		if run.RoundNumber != 1 {
			t.Fatalf("run %s round = %d, want 1 (shared SHA)", run.ID, run.RoundNumber)
		}
	}
	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").First(&rev).Error; err != nil {
		t.Fatalf("review: %v", err)
	}
	if rev.RoundNumber != 1 {
		t.Fatalf("review round = %d, want 1", rev.RoundNumber)
	}
}

// 新 SHA 丢掉其它规则上旧 SHA 的防抖行，避免 opened 窗口对过期提交开火。
func TestWebhook_StaleOpenedDebounceDroppedOnNewSHA(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPROpened
		tr.DebounceSeconds = 60
	})
	if err := env.handleWebhook(t, "d-open", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("opened: %v", err)
	}
	if n := countDebounces(t, env.db); n != 1 {
		t.Fatalf("debounce after opened = %d, want 1", n)
	}
	if err := env.handleWebhook(t, "d-sync", "synchronize", "octocat/hw", 1, "sha-2", false, false); err != nil {
		t.Fatalf("synchronize: %v", err)
	}
	if n := countDebounces(t, env.db); n != 0 {
		t.Fatalf("stale opened debounce survived new SHA: %d", n)
	}
	env.pr.ProcessDueDebounces(time.Now().UTC().Add(2 * time.Minute))
	if n := countRuns(t, env.db, "head_sha = ?", "sha-1"); n != 0 {
		t.Fatalf("stale SHA runs = %d, want 0", n)
	}
}

// 迟到的 opened(A) 不能把已经在跑的 SHA B 取消并重开 A。
func TestWebhook_StaleOpenedDoesNotCancelNewerSHA(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		a.Name = "synchronize"
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 0
	})

	if err := env.handleWebhook(t, "d-open", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("opened: %v", err)
	}
	if err := env.handleWebhook(t, "d-sync", "synchronize", "octocat/hw", 1, "sha-2", false, false); err != nil {
		t.Fatalf("synchronize: %v", err)
	}
	if n := countRuns(t, env.db, "head_sha = ? AND status = ?", "sha-2", model.RunStatusPendingRun); n != 2 {
		t.Fatalf("sha-2 pending = %d, want 2", n)
	}
	if n := countRuns(t, env.db, "head_sha = ? AND status = ?", "sha-1", model.AutomationRunCancelled); n != 2 {
		t.Fatalf("sha-1 cancelled = %d, want 2", n)
	}

	if err := env.handleWebhook(t, "d-open-late", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("late opened: %v", err)
	}
	if n := countRuns(t, env.db, "head_sha = ? AND status <> ?", "sha-2", model.AutomationRunCancelled); n != 2 {
		t.Fatalf("sha-2 still inflight = %d, want 2 (late opened must not cancel)", n)
	}
	if n := countRuns(t, env.db, "head_sha = ? AND status <> ?", "sha-1", model.AutomationRunCancelled); n != 0 {
		t.Fatalf("sha-1 reopened = %d, want 0", n)
	}
}

// 迟到的 synchronize(旧 SHA) 同样不能取消当前 head。
func TestWebhook_StaleSynchronizeDoesNotCancelNewerSHA(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		a.Name = "synchronize"
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 0
	})

	if err := env.handleWebhook(t, "d-open", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("opened: %v", err)
	}
	if err := env.handleWebhook(t, "d-sync", "synchronize", "octocat/hw", 1, "sha-2", false, false); err != nil {
		t.Fatalf("synchronize: %v", err)
	}
	if err := env.handleWebhook(t, "d-sync-late", "synchronize", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("late synchronize: %v", err)
	}
	if n := countRuns(t, env.db, "head_sha = ? AND status = ?", "sha-2", model.RunStatusPendingRun); n != 2 {
		t.Fatalf("sha-2 pending = %d, want 2", n)
	}
	if n := countRuns(t, env.db, "head_sha = ? AND status <> ?", "sha-1", model.AutomationRunCancelled); n != 0 {
		t.Fatalf("sha-1 inflight after late sync = %d, want 0", n)
	}
}

// 防抖窗口内还没有 review/运行时，迟到的旧 SHA 不能删掉当前 debounce。
func TestWebhook_DelayedSHADoesNotDropNewerDebounce(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPROpened
		tr.DebounceSeconds = 60
	})
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		a.Name = "synchronize"
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 60
	})

	if err := env.handleWebhook(t, "d-open", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("opened: %v", err)
	}
	if err := env.handleWebhook(t, "d-sync", "synchronize", "octocat/hw", 1, "sha-2", false, false); err != nil {
		t.Fatalf("synchronize: %v", err)
	}
	if n := countDebounces(t, env.db); n != 1 {
		t.Fatalf("debounce after sha-2 = %d, want 1", n)
	}
	var d model.GitHubPRDebounce
	if err := env.db.First(&d).Error; err != nil {
		t.Fatalf("debounce: %v", err)
	}
	if d.HeadSHA != "sha-2" {
		t.Fatalf("debounce sha = %s, want sha-2", d.HeadSHA)
	}

	if err := env.handleWebhook(t, "d-open-late", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("late opened: %v", err)
	}
	if err := env.handleWebhook(t, "d-sync-late", "synchronize", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("late synchronize: %v", err)
	}
	if err := env.db.First(&d).Error; err != nil {
		t.Fatalf("reload debounce: %v", err)
	}
	if d.HeadSHA != "sha-2" {
		t.Fatalf("late delivery replaced debounce sha = %s, want sha-2", d.HeadSHA)
	}

	env.pr.ProcessDueDebounces(time.Now().UTC().Add(2 * time.Minute))
	if n := countRuns(t, env.db, "head_sha = ?", "sha-1"); n != 0 {
		t.Fatalf("stale SHA runs = %d, want 0", n)
	}
	if n := countRuns(t, env.db, "head_sha = ?", "sha-2"); n == 0 {
		t.Fatal("sha-2 round missing after debounce fired")
	}
}

// A 已开轮（评审账仍是 A）、B 还在 debounce 时，迟到的 A 不能删掉 B。
func TestWebhook_PendingDebouncePreferredOverReviewHead(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPROpened
		tr.DebounceSeconds = 0
	})
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		a.Name = "synchronize"
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 60
	})

	if err := env.handleWebhook(t, "d-open", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("opened: %v", err)
	}
	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ? AND pr_number = ?", "octocat/hw", 1).First(&rev).Error; err != nil {
		t.Fatalf("review after open: %v", err)
	}
	if rev.HeadSHA != "sha-1" {
		t.Fatalf("review sha after open = %s, want sha-1", rev.HeadSHA)
	}

	if err := env.handleWebhook(t, "d-sync", "synchronize", "octocat/hw", 1, "sha-2", false, false); err != nil {
		t.Fatalf("synchronize: %v", err)
	}
	var d model.GitHubPRDebounce
	if err := env.db.First(&d).Error; err != nil {
		t.Fatalf("debounce: %v", err)
	}
	if d.HeadSHA != "sha-2" {
		t.Fatalf("debounce sha = %s, want sha-2", d.HeadSHA)
	}
	if err := env.db.Where("repo_full_name = ? AND pr_number = ?", "octocat/hw", 1).First(&rev).Error; err != nil {
		t.Fatalf("review after sync: %v", err)
	}
	if rev.HeadSHA != "sha-1" {
		t.Fatalf("review still sha-1 until B fires, got %s", rev.HeadSHA)
	}

	if err := env.handleWebhook(t, "d-open-late", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("late opened: %v", err)
	}
	if err := env.handleWebhook(t, "d-sync-late", "synchronize", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("late synchronize: %v", err)
	}
	if err := env.db.First(&d).Error; err != nil {
		t.Fatalf("reload debounce: %v", err)
	}
	if d.HeadSHA != "sha-2" {
		t.Fatalf("late A dropped B debounce, sha = %s", d.HeadSHA)
	}

	env.pr.ProcessDueDebounces(time.Now().UTC().Add(2 * time.Minute))
	if n := countRuns(t, env.db, "head_sha = ? AND status <> ?", "sha-1", model.AutomationRunCancelled); n != 0 {
		t.Fatalf("live sha-1 after B fired = %d, want 0", n)
	}
	if n := countRuns(t, env.db, "head_sha = ?", "sha-2"); n == 0 {
		t.Fatal("sha-2 round missing after debounce fired")
	}
}

type blockingSubmitReview struct {
	githubapi.GitHubClient
	entered chan struct{}
	release chan struct{}
}

func (b *blockingSubmitReview) SubmitReview(ctx context.Context, installationID int64, repoFullName string, number int, in githubapi.ReviewInput) error {
	select {
	case <-b.entered:
	default:
		close(b.entered)
	}
	<-b.release
	return b.GitHubClient.SubmitReview(ctx, installationID, repoFullName, number, in)
}

// webhook 取消必须等 in-flight SubmitReview 释放 per-PR 锁，不能插在重读与提交之间。
func TestWebhook_CancelWaitsForInFlightSubmitReview(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		a.Name = "synchronize"
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 0
	})
	if err := env.handleWebhook(t, "d-open", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("opened: %v", err)
	}
	var run model.AutomationRun
	if err := env.db.Where("head_sha = ?", "sha-1").First(&run).Error; err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := env.db.Model(&run).Update("status", model.AutomationRunDispatched).Error; err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}
	run.Status = model.AutomationRunDispatched

	blocker := &blockingSubmitReview{
		GitHubClient: env.fake,
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	agent := NewAutomationAgentService(env.db, env.runs,
		repository.NewAutomationRunTokenRepository(env.db), nil, NewNotificationSink(env.db), zap.NewNop())
	agent.SetPRGitHub(githubapp.StaticClients{Client: blocker})
	agent.SetPRReview(env.pr)

	body := "ok"
	req := &api.AgentPullRequestReviewRequest{Event: api.COMMENT, Body: &body}
	identity := &RunTokenIdentity{UserID: run.UserID, RunID: run.ID}

	submitDone := make(chan error, 1)
	go func() {
		_, err := agent.AgentSubmitPullRequestReview(context.Background(), identity, req)
		submitDone <- err
	}()
	select {
	case <-blocker.entered:
	case err := <-submitDone:
		t.Fatalf("submit returned before GitHub call: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SubmitReview")
	}

	webhookDone := make(chan error, 1)
	go func() {
		webhookDone <- env.pr.HandleGitHubWebhook(context.Background(), "pull_request", "d-sync",
			webhookPayload("synchronize", "octocat/hw", 1, "sha-2", false, false), "")
	}()

	// 持锁期间 webhook 不得把 sha-1 取消掉。
	select {
	case err := <-webhookDone:
		close(blocker.release)
		t.Fatalf("webhook returned while SubmitReview held the lock: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	var still model.AutomationRun
	if err := env.db.Where("id = ?", run.ID).First(&still).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if still.Status == model.AutomationRunCancelled {
		close(blocker.release)
		t.Fatal("run cancelled while SubmitReview held the lock")
	}

	close(blocker.release)
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for submit")
	}
	select {
	case err := <-webhookDone:
		if err != nil {
			t.Fatalf("webhook: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for webhook")
	}

	if err := env.db.Where("id = ?", run.ID).First(&still).Error; err != nil {
		t.Fatalf("reload after webhook: %v", err)
	}
	if still.Status != model.AutomationRunCancelled {
		t.Fatalf("status after webhook = %s, want cancelled", still.Status)
	}
}

// ---- INT-119 身份分区：两个用户各自开轮同一 repo+PR ----

// recordingClients 记录每次 ClientFor 的身份（评审账行应按自己的验签身份解析
// GitHub 访问面），client 恒返回同一 fake。
type recordingClients struct {
	mu     sync.Mutex
	fake   *githubapp.Fake
	called []string
}

func (r *recordingClients) ClientFor(_ context.Context, userID string) (githubapi.GitHubClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.called = append(r.called, userID)
	return r.fake, nil
}

func (r *recordingClients) identities() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.called...)
}

// TestWebhook_TwoIdentitiesSamePR 各自身份对同一 repo+PR 开轮：产生两行评审账
// （app_user_id 不同）互不覆盖；取消/评论/check 按身份解析，A 的轮不被 B 的
// 投递取消，同步按评审账身份取 client（不再用 runs[0].UserID 猜）。
func TestWebhook_TwoIdentitiesSamePR(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	alice, _ := env.createUserWithRepo(t, "octocat", "hw")
	bob, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, alice, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	env.createPRRule(t, bob, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })

	// alice 的 App 身份开轮（payload 相同，仅验签身份不同）。
	if err := env.handleWebhookAs(t, "d-a", "opened", "octocat/hw", 1, "sha-1", alice.ID); err != nil {
		t.Fatalf("alice round: %v", err)
	}
	// bob 的 App 身份对同一 repo+PR+SHA 开轮。
	if err := env.handleWebhookAs(t, "d-b", "opened", "octocat/hw", 1, "sha-1", bob.ID); err != nil {
		t.Fatalf("bob round: %v", err)
	}

	var revs []model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").Find(&revs).Error; err != nil {
		t.Fatalf("reviews: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("review rows = %+v, want one per identity", revs)
	}
	revByID := map[string]model.GitHubPRReview{}
	for _, rev := range revs {
		revByID[rev.AppUserID] = rev
	}
	if revByID[alice.ID].HeadSHA != "sha-1" || revByID[bob.ID].HeadSHA != "sha-1" {
		t.Fatalf("review rows by identity = %+v, want sha-1 for both", revByID)
	}
	for _, rev := range revs {
		if rev.HeadSHA != "sha-1" || rev.RoundNumber != 1 || !rev.SyncPending {
			t.Fatalf("review %s = sha %s round %d pending %t", rev.AppUserID, rev.HeadSHA, rev.RoundNumber, rev.SyncPending)
		}
	}

	// 各身份的运行归属各自用户，互不取消。
	var aliceRuns, bobRuns []model.AutomationRun
	if err := env.db.Where("repo_full_name = ? AND user_id = ?", "octocat/hw", alice.ID).Find(&aliceRuns).Error; err != nil {
		t.Fatalf("alice runs: %v", err)
	}
	if err := env.db.Where("repo_full_name = ? AND user_id = ?", "octocat/hw", bob.ID).Find(&bobRuns).Error; err != nil {
		t.Fatalf("bob runs: %v", err)
	}
	if len(aliceRuns) != 2 || len(bobRuns) != 2 {
		t.Fatalf("runs = %d/%d, want 2/2 (one per model per identity)", len(aliceRuns), len(bobRuns))
	}
	for _, run := range append(append([]model.AutomationRun(nil), aliceRuns...), bobRuns...) {
		if run.Status != model.RunStatusPendingRun {
			t.Fatalf("run %s status = %q, want still live", run.ID, run.Status)
		}
	}

	// 同步按评审账身份取 client：只出现 alice/bob 两个身份。
	rc := &recordingClients{fake: env.fake}
	identityPR := NewPRReviewService(env.db,
		repository.NewAutomationRepository(env.db),
		repository.NewAutomationRunRepository(env.db),
		repository.NewProjectRepository(env.db),
		repository.NewGitHubWebhookDeliveryRepository(env.db),
		repository.NewGitHubPRDebounceRepository(env.db),
		repository.NewGitHubPRReviewRepository(env.db),
		rc, env.stopper, NewNotificationSink(env.db),
		&config.Config{Server: config.ServerConfig{PublicURL: "https://fs.example.com"}}, zap.NewNop())
	identityPR.SyncPendingReviews(nowUTC().Add(time.Second))
	got := rc.identities()
	if len(got) == 0 {
		t.Fatal("client identities empty, want calls per review row")
	}
	// 每行的同步会多次取 client（逐 run 建 check、评论、check 状态），但身份
	// 集合必须恰好是两个评审行身份，绝不能出现别人的身份。
	seenUsers := map[string]bool{}
	for _, id := range got {
		seenUsers[id] = true
	}
	if len(seenUsers) != 2 || !seenUsers[alice.ID] || !seenUsers[bob.ID] {
		t.Fatalf("client identities = %v, want only [%s %s]", got, alice.ID, bob.ID)
	}
	// 两行都同步成功清位。
	var pending int64
	env.db.Model(&model.GitHubPRReview{}).Where("sync_pending = ?", true).Count(&pending)
	if pending != 0 {
		t.Fatalf("sync_pending rows = %d, want 0", pending)
	}

	// 迟到的 alice 身份投递旧 SHA 不能动 bob 的行：取消按身份分区。
	if err := env.handleWebhookAs(t, "d-a-late", "opened", "octocat/hw", 1, "sha-0", alice.ID); err != nil {
		t.Fatalf("alice stale: %v", err)
	}
	var stillBob int64
	env.db.Model(&model.AutomationRun{}).
		Where("user_id = ? AND status <> ?", bob.ID, model.AutomationRunCancelled).Count(&stillBob)
	if stillBob != 2 {
		t.Fatalf("bob live runs after alice stale delivery = %d, want 2", stillBob)
	}
	// alice 的旧行不受影响（head_sha 仍是 sha-1）。
	var revAlice model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ? AND app_user_id = ?", "octocat/hw", alice.ID).First(&revAlice).Error; err != nil {
		t.Fatalf("reload alice review: %v", err)
	}
	if revAlice.HeadSHA != "sha-1" {
		t.Fatalf("alice review sha = %s, want sha-1", revAlice.HeadSHA)
	}
}

// GitHub 先投递 synchronize(B)、再迟到 synchronize(A)（A 从未 seen）不能取消 B。
func TestWebhook_OutOfOrderSynchronizeDoesNotCancelNewerSHA(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 0
	})
	t1 := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	if err := env.handleWebhookAt(t, "d-b", "synchronize", "octocat/hw", 1, "sha-2", false, false, t2); err != nil {
		t.Fatalf("synchronize B: %v", err)
	}
	if n := countRuns(t, env.db, "head_sha = ?", "sha-2"); n == 0 {
		t.Fatal("sha-2 round missing")
	}
	if err := env.handleWebhookAt(t, "d-a-late", "synchronize", "octocat/hw", 1, "sha-1", false, false, t1); err != nil {
		t.Fatalf("late synchronize A: %v", err)
	}
	if n := countRuns(t, env.db, "head_sha = ? AND status <> ?", "sha-2", model.AutomationRunCancelled); n == 0 {
		t.Fatal("late A cancelled current B")
	}
	if n := countRuns(t, env.db, "head_sha = ?", "sha-1"); n != 0 {
		t.Fatalf("stale A opened runs = %d, want 0", n)
	}
}

// 同一 updated_at 的乱序 synchronize 也不能取消当前 head。
func TestWebhook_EqualTimeSynchronizeDoesNotCancelNewerSHA(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 0
	})
	t0 := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	if err := env.handleWebhookAt(t, "d-b", "synchronize", "octocat/hw", 1, "sha-2", false, false, t0); err != nil {
		t.Fatalf("synchronize B: %v", err)
	}
	if err := env.handleWebhookAt(t, "d-a-late", "synchronize", "octocat/hw", 1, "sha-1", false, false, t0); err != nil {
		t.Fatalf("equal-time synchronize A: %v", err)
	}
	if n := countRuns(t, env.db, "head_sha = ? AND status <> ?", "sha-2", model.AutomationRunCancelled); n == 0 {
		t.Fatal("equal-time A cancelled current B")
	}
	if n := countRuns(t, env.db, "head_sha = ?", "sha-1"); n != 0 {
		t.Fatalf("stale A opened runs = %d, want 0", n)
	}
}

// 超时终态必须等 in-flight SubmitReview 释放 per-PR 锁。
func TestTimeout_WaitsForInFlightSubmitReview(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	if err := env.handleWebhook(t, "d-open", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("opened: %v", err)
	}
	var run model.AutomationRun
	if err := env.db.Where("head_sha = ?", "sha-1").First(&run).Error; err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := env.db.Model(&run).Updates(map[string]any{
		"status":      model.AutomationRunDispatched,
		"deadline_at": nowUTC().Add(-time.Minute),
	}).Error; err != nil {
		t.Fatalf("mark dispatched: %v", err)
	}
	if err := env.db.Where("id = ?", run.ID).First(&run).Error; err != nil {
		t.Fatalf("reload run: %v", err)
	}

	blocker := &blockingSubmitReview{
		GitHubClient: env.fake,
		entered:      make(chan struct{}),
		release:      make(chan struct{}),
	}
	agent := NewAutomationAgentService(env.db, env.runs,
		repository.NewAutomationRunTokenRepository(env.db), nil, NewNotificationSink(env.db), zap.NewNop())
	agent.SetPRGitHub(githubapp.StaticClients{Client: blocker})
	agent.SetPRReview(env.pr)

	sink := NewNotificationSink(env.db)
	sched := NewAutomationScheduler(env.db,
		repository.NewAutomationRepository(env.db), env.runs,
		repository.NewAutomationRunTokenRepository(env.db), nil, sink,
		repository.NewIssueRepository(env.db),
		&config.Config{Server: config.ServerConfig{PublicURL: "https://fs.example.com"}}, zap.NewNop())
	sched.UsePRReview(env.pr)

	body := "ok"
	req := &api.AgentPullRequestReviewRequest{Event: api.COMMENT, Body: &body}
	identity := &RunTokenIdentity{UserID: run.UserID, RunID: run.ID}

	submitDone := make(chan error, 1)
	go func() {
		_, err := agent.AgentSubmitPullRequestReview(context.Background(), identity, req)
		submitDone <- err
	}()
	select {
	case <-blocker.entered:
	case err := <-submitDone:
		t.Fatalf("submit returned before GitHub call: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SubmitReview")
	}

	timeoutDone := make(chan struct{})
	go func() {
		sched.timeoutRun(&run, nowUTC())
		close(timeoutDone)
	}()
	select {
	case <-timeoutDone:
		close(blocker.release)
		t.Fatal("timeout returned while SubmitReview held the lock")
	case <-time.After(200 * time.Millisecond):
	}
	var still model.AutomationRun
	if err := env.db.Where("id = ?", run.ID).First(&still).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if still.Status == model.AutomationRunTimedOut {
		close(blocker.release)
		t.Fatal("run timed out while SubmitReview held the lock")
	}

	close(blocker.release)
	select {
	case err := <-submitDone:
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for submit")
	}
	select {
	case <-timeoutDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for timeoutRun")
	}
	if err := env.db.Where("id = ?", run.ID).First(&still).Error; err != nil {
		t.Fatalf("reload after timeout: %v", err)
	}
	if still.Status != model.AutomationRunTimedOut {
		t.Fatalf("status after timeout = %s, want timed_out", still.Status)
	}
}

// 同步清位不能盖掉期间新置的 pending（新 SHA / 新终态）。
func TestClearPendingSync_DoesNotClobberNewerPending(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	reviews := repository.NewGitHubPRReviewRepository(env.db)
	t0 := nowUTC()
	if err := env.db.Create(&model.GitHubPRReview{
		RepoFullName: "octocat/hw", PRNumber: 1, InstallationID: 1,
		RoundNumber: 1, HeadSHA: "sha-1", SyncPending: true, UpdatedAt: t0,
	}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	var snap model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").First(&snap).Error; err != nil {
		t.Fatalf("snap: %v", err)
	}
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		return reviews.UpsertTx(tx, &model.GitHubPRReview{
			RepoFullName: "octocat/hw", PRNumber: 1, InstallationID: 1,
			RoundNumber: 2, HeadSHA: "sha-2", SyncPending: true, UpdatedAt: t0.Add(2 * time.Second),
		})
	}); err != nil {
		t.Fatalf("upsert newer: %v", err)
	}
	if err := reviews.ClearPendingSync(context.Background(), &snap, t0.Add(3*time.Second)); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var rev model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").First(&rev).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !rev.SyncPending || rev.HeadSHA != "sha-2" || rev.RoundNumber != 2 {
		t.Fatalf("newer pending clobbered: pending=%t sha=%s round=%d", rev.SyncPending, rev.HeadSHA, rev.RoundNumber)
	}
}

// 已保存 comment_id 时直接 PATCH，不再按标记扫第一页。
func TestPushComment_PrefersSavedCommentID(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	id, err := env.fake.CreatePullRequestComment(context.Background(), 1, "octocat/hw", 1, prReviewCommentMarker+"\n旧正文")
	if err != nil {
		t.Fatalf("seed comment: %v", err)
	}
	env.seedPRRun(t, a, "octocat/hw", 1, "sha-1", model.AutomationRunCompleted)
	if err := env.db.Create(&model.GitHubPRReview{
		RepoFullName: "octocat/hw", PRNumber: 1, InstallationID: 12345,
		CommentID: id, RoundNumber: 1, HeadSHA: "sha-1", SyncPending: true, UpdatedAt: nowUTC(),
	}).Error; err != nil {
		t.Fatalf("seed review: %v", err)
	}
	before := env.fake.CallCounts()
	env.pr.SyncPendingReviews(nowUTC().Add(time.Second))
	after := env.fake.CallCounts()
	if after["FindPullRequestComment"]-before["FindPullRequestComment"] != 0 {
		t.Fatalf("must not scan by marker when comment_id is saved")
	}
	if after["UpdatePullRequestComment"]-before["UpdatePullRequestComment"] != 1 {
		t.Fatalf("UpdateIssueComment delta = %d, want 1", after["UpdatePullRequestComment"]-before["UpdatePullRequestComment"])
	}
	if comment := env.fake.LastCommentBody(); !strings.Contains(comment, "完成") {
		t.Fatalf("comment not updated: %s", comment)
	}
}

// ---- 场景：防抖窗口内项目范围被改窄 → 旧窗口作废 ----

func TestWebhook_DebounceDroppedWhenProjectScopeChanged(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, projectA := env.createUserWithRepo(t, "octocat", "hw")
	projectB := &model.Project{
		ID: uuid.NewString(), UserID: user.ID, Name: "p-b-" + uuid.NewString()[:8],
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := env.db.Create(projectB).Error; err != nil {
		t.Fatalf("create project B: %v", err)
	}

	a := env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		pid := projectA.ID
		tr.IssueProjectID = &pid
	})
	if err := env.handleWebhook(t, "d-1", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("webhook: %v", err)
	}
	var d model.GitHubPRDebounce
	if err := env.db.Where("trigger_id = ?", env.firstTrigger(t, a).ID).First(&d).Error; err != nil {
		t.Fatalf("debounce row: %v", err)
	}

	// 窗口内把触发器的项目范围改到 B：到点后旧项目 A 的窗口必须作废。
	if err := env.db.Model(&model.AutomationTrigger{}).
		Where("id = ?", d.TriggerID).
		Update("issue_project_id", projectB.ID).Error; err != nil {
		t.Fatalf("narrow scope: %v", err)
	}
	env.pr.ProcessDueDebounces(d.FireAt.Add(time.Second))

	if n := countRuns(t, env.db, "repo_full_name = ?", "octocat/hw"); n != 0 {
		t.Fatalf("runs after stale-scope fire = %d, want 0", n)
	}
	if n := countDebounces(t, env.db); n != 0 {
		t.Fatalf("debounce rows after fire = %d, want 0 (consumed)", n)
	}
}

// ---- INT-119 第三轮：身份持久化与按身份分区 ----

// TestReviewUpsert_ScopedToIdentity 同 PR 同时存在 config/A/B 三行评审账时，
// config 身份（AppUserID 空串）的 upsert 只推进自己的行——更新条件显式带全三
// 列主键，GORM 不能把空串身份当零值从条件里省掉、退化成按 (repo, pr) 更新。
func TestReviewUpsert_ScopedToIdentity(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	reviews := repository.NewGitHubPRReviewRepository(env.db)
	now := nowUTC()
	seed := func(appUserID string, commentID int64) model.GitHubPRReview {
		rev := model.GitHubPRReview{
			RepoFullName: "octocat/hw", PRNumber: 1, AppUserID: appUserID,
			InstallationID: 1, CommentID: commentID, RoundNumber: 1, HeadSHA: "sha-1",
			SyncEpoch: 7, UpdatedAt: now,
		}
		if err := env.db.Create(&rev).Error; err != nil {
			t.Fatalf("seed %q: %v", appUserID, err)
		}
		return rev
	}
	seed("", 1)
	a := seed("alice", 2)
	b := seed("bob", 3)

	if err := env.db.Transaction(func(tx *gorm.DB) error {
		return reviews.UpsertTx(tx, &model.GitHubPRReview{
			RepoFullName: "octocat/hw", PRNumber: 1, AppUserID: "",
			InstallationID: 9, RoundNumber: 5, HeadSHA: "sha-9", SyncPending: true,
			UpdatedAt: now.Add(time.Second),
		})
	}); err != nil {
		t.Fatalf("config upsert: %v", err)
	}

	var cfgRow model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ? AND app_user_id = ?", "octocat/hw", "").First(&cfgRow).Error; err != nil {
		t.Fatalf("reload config row: %v", err)
	}
	if cfgRow.RoundNumber != 5 || cfgRow.HeadSHA != "sha-9" || cfgRow.InstallationID != 9 ||
		cfgRow.CommentID != 1 || cfgRow.SyncEpoch != 8 || !cfgRow.SyncPending {
		t.Fatalf("config row not advanced: %+v", cfgRow)
	}
	for _, want := range []model.GitHubPRReview{a, b} {
		var got model.GitHubPRReview
		if err := env.db.Where("repo_full_name = ? AND app_user_id = ?", "octocat/hw", want.AppUserID).First(&got).Error; err != nil {
			t.Fatalf("reload %s: %v", want.AppUserID, err)
		}
		if got.CommentID != want.CommentID || got.RoundNumber != want.RoundNumber ||
			got.HeadSHA != want.HeadSHA || got.SyncEpoch != want.SyncEpoch ||
			got.InstallationID != want.InstallationID || got.SyncPending {
			t.Fatalf("row %s touched by config upsert: %+v (want %+v)", want.AppUserID, got, want)
		}
	}
}

// TestReviewIdentity_PersistedOnRun 开轮身份落进运行行：config 身份开轮时
// run.user_id 是规则所有者而 AppUserID 是空串，身份解析必须返回空串（老实现
// 反推评审账会在这里选错 App）；用户身份开轮 AppUserID 即该用户；防抖到点开轮
// 写入防抖行记录的身份。
func TestReviewIdentity_PersistedOnRun(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })
	env.createPRRule(t, user, func(a *model.Automation, tr *model.AutomationTrigger) {
		a.Name = "synchronize（防抖）"
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
	})

	// config 身份开轮：规则所有者是 user，身份必须仍是 config。
	if err := env.handleWebhook(t, "d-1", "opened", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("config round: %v", err)
	}
	var cfgRun model.AutomationRun
	if err := env.db.Where("head_sha = ?", "sha-1").First(&cfgRun).Error; err != nil {
		t.Fatalf("config run: %v", err)
	}
	if cfgRun.UserID != user.ID || cfgRun.AppUserID != "" {
		t.Fatalf("config run owner=%s app=%q, want owner=user app=''", cfgRun.UserID, cfgRun.AppUserID)
	}
	if got := env.pr.ReviewIdentityForRun(context.Background(), &cfgRun); got != "" {
		t.Fatalf("identity for config run = %q, want '' (must not fall back to rule owner)", got)
	}

	// 用户身份投递进防抖窗口：防抖行记录该身份；到点开轮把身份写进运行行。
	if err := env.handleWebhookAs(t, "d-2", "synchronize", "octocat/hw", 1, "sha-2", user.ID); err != nil {
		t.Fatalf("debounce delivery: %v", err)
	}
	var d model.GitHubPRDebounce
	if err := env.db.Where("repo_full_name = ? AND pr_number = ?", "octocat/hw", 1).First(&d).Error; err != nil {
		t.Fatalf("debounce row: %v", err)
	}
	if d.AppUserID != user.ID || d.HeadSHA != "sha-2" {
		t.Fatalf("debounce row app=%q sha=%s, want %s/sha-2", d.AppUserID, d.HeadSHA, user.ID)
	}
	env.pr.ProcessDueDebounces(nowUTC().Add(2 * time.Minute))
	var userRun model.AutomationRun
	if err := env.db.Where("head_sha = ?", "sha-2").First(&userRun).Error; err != nil {
		t.Fatalf("debounced run: %v", err)
	}
	if userRun.AppUserID != user.ID {
		t.Fatalf("debounced run app_user_id = %q, want %s", userRun.AppUserID, user.ID)
	}
	if got := env.pr.ReviewIdentityForRun(context.Background(), &userRun); got != user.ID {
		t.Fatalf("identity for user run = %q, want %s", got, user.ID)
	}
}

// TestMarkPRReviewPending_OnlyTouchesOwnIdentityRow 终态/超时置位只推进本运行
// 开轮身份的那一行，其他身份行的 pending/epoch 原样不动。
func TestMarkPRReviewPending_OnlyTouchesOwnIdentityRow(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	now := nowUTC()
	for _, appUserID := range []string{"", "alice", "bob"} {
		if err := env.db.Create(&model.GitHubPRReview{
			RepoFullName: "octocat/hw", PRNumber: 1, AppUserID: appUserID,
			InstallationID: 1, RoundNumber: 1, HeadSHA: "sha-1", SyncEpoch: 3, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed %q: %v", appUserID, err)
		}
	}

	run := &model.AutomationRun{
		TriggerType:  model.AutomationTriggerGitHubPRSynchronize,
		RepoFullName: "octocat/hw", PRNumber: 1, AppUserID: "alice",
		InstallationID: 1, RoundNumber: 1, HeadSHA: "sha-1",
	}
	if err := env.db.Transaction(func(tx *gorm.DB) error {
		return markPRReviewPendingTx(tx, run, now.Add(time.Second))
	}); err != nil {
		t.Fatalf("mark pending: %v", err)
	}

	var aliceRow model.GitHubPRReview
	if err := env.db.Where("app_user_id = ?", "alice").First(&aliceRow).Error; err != nil {
		t.Fatalf("reload alice: %v", err)
	}
	if !aliceRow.SyncPending || aliceRow.SyncEpoch != 4 {
		t.Fatalf("alice row pending=%t epoch=%d, want true/4", aliceRow.SyncPending, aliceRow.SyncEpoch)
	}
	for _, appUserID := range []string{"", "bob"} {
		var row model.GitHubPRReview
		if err := env.db.Where("app_user_id = ?", appUserID).First(&row).Error; err != nil {
			t.Fatalf("reload %q: %v", appUserID, err)
		}
		if row.SyncPending || row.SyncEpoch != 3 {
			t.Fatalf("row %q touched: pending=%t epoch=%d", appUserID, row.SyncPending, row.SyncEpoch)
		}
	}
}

// perUserClients 按身份返回各自的 fake GitHub：共享同一套规则/项目/库（远端
// 数据同源），但评论与 check 按身份落到各自 fake——模拟按身份拒绝的远端。
// 未登记身份（含 config）返回错误。
type perUserClients struct {
	byUser map[string]*githubapp.Fake
}

func (p *perUserClients) ClientFor(_ context.Context, userID string) (githubapi.GitHubClient, error) {
	f := p.byUser[userID]
	if f == nil {
		return nil, fmt.Errorf("no github client for identity %q", userID)
	}
	return f, nil
}

// TestSync_ThreeIdentitiesIndependent config/alice/bob 三个身份对同一 repo+PR
// 各开一轮：评论按身份独立（正文各含自己的 marker、comment_id 各指向自己
// fake、A 的评论不被 B 找回或更新）、check 归属各自 fake；一个身份推新 SHA 只
// 取消/推进自己的轮，其余身份的运行、评论原样不动。
func TestSync_ThreeIdentitiesIndependent(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	alice, _ := env.createUserWithRepo(t, "octocat", "hw")
	bob, _ := env.createUserWithRepo(t, "octocat", "hw")
	// alice 的规则收 synchronize（她后续推新 SHA 用 synchronize 取消旧轮），
	// bob 的规则收 opened。config 投递不带身份过滤，命中 alice 的规则即以
	// config 身份给 alice 的规则开轮（UserID=alice、AppUserID='' 的关键形态）。
	env.createPRRule(t, alice, func(a *model.Automation, tr *model.AutomationTrigger) {
		tr.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		tr.DebounceSeconds = 0
	})
	env.createPRRule(t, bob, func(a *model.Automation, tr *model.AutomationTrigger) { tr.DebounceSeconds = 0 })

	fakeCfg, fakeA, fakeB := githubapp.NewFake(), githubapp.NewFake(), githubapp.NewFake()
	pr := NewPRReviewService(env.db,
		repository.NewAutomationRepository(env.db),
		repository.NewAutomationRunRepository(env.db),
		repository.NewProjectRepository(env.db),
		repository.NewGitHubWebhookDeliveryRepository(env.db),
		repository.NewGitHubPRDebounceRepository(env.db),
		repository.NewGitHubPRReviewRepository(env.db),
		&perUserClients{byUser: map[string]*githubapp.Fake{
			"": fakeCfg, alice.ID: fakeA, bob.ID: fakeB,
		}},
		env.stopper, NewNotificationSink(env.db),
		&config.Config{Server: config.ServerConfig{PublicURL: "https://fs.example.com"}}, zap.NewNop())

	deliver := func(delivery, action, sha, appUserID string) {
		t.Helper()
		if err := pr.HandleGitHubWebhook(context.Background(), "pull_request", delivery,
			webhookPayload(action, "octocat/hw", 1, sha, false, false), appUserID); err != nil {
			t.Fatalf("delivery %s (%q): %v", delivery, appUserID, err)
		}
	}
	// 各身份各用不同 SHA（同 SHA 会被 (trigger_id, trigger_key) 唯一键收敛
	// 成一轮，与身份无关——那是既有的去重语义）：三条评审账 (repo, pr, 身份)
	// 各自推进、各自同步。
	deliver("d-cfg", "synchronize", "sha-c1", "")
	deliver("d-a", "synchronize", "sha-a1", alice.ID)
	deliver("d-b", "opened", "sha-b1", bob.ID)
	pr.SyncPendingReviews(nowUTC().Add(time.Second))

	markerCfg := prReviewCommentMarkerFor("")
	markerA := prReviewCommentMarkerFor(alice.ID)
	markerB := prReviewCommentMarkerFor(bob.ID)
	bodyA, bodyB := fakeA.LastCommentBody(), fakeB.LastCommentBody()
	if !strings.Contains(bodyA, markerA) || strings.Contains(bodyA, markerB) || strings.Contains(bodyA, markerCfg) {
		t.Fatalf("alice comment marker wrong: %s", bodyA)
	}
	if !strings.Contains(bodyB, markerB) || strings.Contains(bodyB, markerA) || strings.Contains(bodyB, markerCfg) {
		t.Fatalf("bob comment marker wrong: %s", bodyB)
	}
	if body := fakeCfg.LastCommentBody(); !strings.Contains(body, markerCfg) || strings.Contains(body, markerA) {
		t.Fatalf("config comment marker wrong: %s", body)
	}

	// 各身份的评审行 comment_id 不同，且各自指向自己 fake 里的评论。
	var revs []model.GitHubPRReview
	if err := env.db.Where("repo_full_name = ?", "octocat/hw").Find(&revs).Error; err != nil {
		t.Fatalf("reviews: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("review rows = %d, want 3", len(revs))
	}
	ids := map[string]int64{}
	for _, rev := range revs {
		ids[rev.AppUserID] = rev.CommentID
		if rev.CommentID == 0 {
			t.Fatalf("review %s comment_id not saved: %+v", rev.AppUserID, rev)
		}
	}
	// 各 fake 恰好一条评论（各自的评论独立，没有身份把别人的评论找回改）。
	for name, f := range map[string]*githubapp.Fake{"config": fakeCfg, "alice": fakeA, "bob": fakeB} {
		if comments := f.Comments("octocat/hw", 1); len(comments) != 1 {
			t.Fatalf("%s comments = %d, want 1 (independent per identity)", name, len(comments))
		}
	}
	// 各身份的 comment_id 指向自己 fake 里的那条评论：按身份 marker 在自己 fake
	// 上能找回，别人的 marker 找不到（评论不会跨身份被找回改）。
	findByMarker := func(f *githubapp.Fake, marker string) (int64, bool) {
		id, found, err := f.FindPullRequestComment(context.Background(), 1, "octocat/hw", 1, marker)
		if err != nil {
			t.Fatalf("find comment: %v", err)
		}
		return id, found
	}
	for name, pair := range map[string]struct {
		fake   *githubapp.Fake
		marker string
		id     int64
	}{
		"config": {fakeCfg, markerCfg, ids[""]},
		"alice":  {fakeA, markerA, ids[alice.ID]},
		"bob":    {fakeB, markerB, ids[bob.ID]},
	} {
		id, found := findByMarker(pair.fake, pair.marker)
		if !found || id != pair.id {
			t.Fatalf("%s marker finds id=%d found=%t, want own comment %d", name, id, found, pair.id)
		}
	}
	// 别人的 marker 在 bob 的 fake 上找不到：B 的同步不会把 A 的评论找回来改。
	if _, found := findByMarker(fakeB, markerA); found {
		t.Fatal("alice's comment must not be findable by bob's marker")
	}
	// check 归属：每个身份自己的 fake 上每模型一个 check，别人的 fake 没有额外的。
	for name, f := range map[string]*githubapp.Fake{"config": fakeCfg, "alice": fakeA, "bob": fakeB} {
		if checks := f.CheckRuns("octocat/hw"); len(checks) != 2 {
			t.Fatalf("%s checks = %d, want 2 (one per model)", name, len(checks))
		}
	}

	// alice 推新 SHA：她的旧轮取消、新轮开启、她的评论原地更新；
	// config 与 bob 的运行、评论完全不变。
	deliver("d-a-2", "synchronize", "sha-a2", alice.ID)
	pr.SyncPendingReviews(nowUTC().Add(2 * time.Second))

	var aliceLive, otherLive int64
	env.db.Model(&model.AutomationRun{}).
		Where("app_user_id = ? AND status <> ?", alice.ID, model.AutomationRunCancelled).Count(&aliceLive)
	env.db.Model(&model.AutomationRun{}).
		Where("app_user_id IN (?, ?) AND status <> ?", "", bob.ID, model.AutomationRunCancelled).Count(&otherLive)
	if aliceLive != 2 {
		t.Fatalf("alice live runs = %d, want 2 (new SHA round)", aliceLive)
	}
	if otherLive != 4 {
		t.Fatalf("config+bob live runs = %d, want 4 (untouched by alice's SHA)", otherLive)
	}
	if comments := fakeA.Comments("octocat/hw", 1); len(comments) != 1 {
		t.Fatalf("alice comments = %d, want 1 (updated in place)", len(comments))
	}
	if body := fakeA.LastCommentBody(); !strings.Contains(body, "sha-a2") || !strings.Contains(body, markerA) {
		t.Fatalf("alice comment not advanced to sha-a2: %s", body)
	}
	if comments := fakeB.Comments("octocat/hw", 1); len(comments) != 1 || fakeB.LastCommentBody() != bodyB {
		t.Fatalf("bob comment touched by alice's SHA: %d %q", len(fakeB.Comments("octocat/hw", 1)), fakeB.LastCommentBody())
	}
	if comments := fakeCfg.Comments("octocat/hw", 1); len(comments) != 1 {
		t.Fatalf("config comments = %d, want 1", len(comments))
	}
}

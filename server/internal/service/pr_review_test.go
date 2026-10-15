package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/godbobo/fast_ship/server/internal/config"
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

// 部分唯一索引只约束 cron/issue 触发：同一规则第二条未结束 cron 行必须被拒绝。
func TestPRIndex_RejectsSecondUnfinishedCronRun(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, nil)

	env.seedRun(t, a, func(r *model.AutomationRun) {
		r.TriggerType = model.AutomationTriggerCron
		r.TriggerKey = "cron:2026-01-01T00:00:00Z"
	})
	// busy 路径的失败行「有 finished_at」不算活动；这里放一条真正的未结束行验证唯一索引。
	err := env.db.Create(&model.AutomationRun{
		ID:                 uuid.NewString(),
		UserID:             a.UserID,
		AutomationID:       a.ID,
		TriggerType:        model.AutomationTriggerCron,
		TriggerKey:         "cron:2026-01-02T00:00:00Z",
		TriggeredAt:        time.Now().UTC(),
		ConfigSnapshotJSON: snapshotJSON(a),
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

// PR 触发不受「同一自动化至多一个活动运行」约束：一轮多模型并行即多行未结束。
func TestPRIndex_AllowsTwoUnfinishedPRRuns(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	a := env.createAutomation(t, user.ID, func(x *model.Automation) {
		x.TriggerType = model.AutomationTriggerGitHubPROpened
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
	a := env.createAutomation(t, user.ID, func(x *model.Automation) {
		x.TriggerType = model.AutomationTriggerGitHubPRSynchronize
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
		r.TriggerType = model.AutomationTriggerIssueCreated
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

// cron 忙碌路径不回退：已有活动运行时第二条 cron 触发仍记 failed/automation_busy。
func TestCronBusy_StillRecordsAutomationBusy(t *testing.T) {
	env := setupAutomationTestEnv(t)
	user := env.createUser(t)
	stale := time.Now().UTC().Add(-time.Hour)
	a := env.createAutomation(t, user.ID, func(x *model.Automation) {
		x.TriggerType = model.AutomationTriggerCron
		x.CronExpression = "*/10 * * * *"
		x.NextRunAt = &stale
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
		&model.Automation{}, &model.AutomationRun{}, &model.AutomationRunToken{}, &model.Notification{},
		&model.GitHubWebhookDelivery{}, &model.GitHubPRDebounce{}, &model.GitHubPRReview{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_automation_trigger ON automation_runs(automation_id, trigger_key)")
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_automation_runs_active_once
ON automation_runs(automation_id)
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
		fake, stopper, NewNotificationSink(db), cfg, logger)
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
		ID:          uuid.NewString(),
		UserID:      user.ID,
		Name:        "p-" + uuid.NewString()[:8],
		GithubOwner: owner,
		GithubRepo:  repo,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	if err := e.db.Create(project).Error; err != nil {
		t.Fatalf("create project: %v", err)
	}
	return user, project
}

// createPRRule 建 PR 触发规则（默认 opened、防抖 60、两个模型）。
func (e *prReviewTestEnv) createPRRule(t *testing.T, user *model.User, mutate func(*model.Automation)) *model.Automation {
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
		ID:                  uuid.NewString(),
		UserID:              user.ID,
		Name:                "PR 评审",
		Enabled:             true,
		CreatedAt:           now,
		UpdatedAt:           now,
		EnvironmentID:       env.ID,
		T3ProjectID:         "t3-proj",
		TriggerType:         model.AutomationTriggerGitHubPROpened,
		PromptTemplate:      "评审 {{repo}} #{{pr_number}}",
		ModelSelectionsJSON: string(selsJSON),
		DebounceSeconds:     60,
		RuntimeMode:         "full-access",
		TimeoutSeconds:      3600,
		PostAction:          model.AutomationPostNone,
	}
	if mutate != nil {
		mutate(a)
	}
	if err := e.db.Create(a).Error; err != nil {
		t.Fatalf("create automation: %v", err)
	}
	return a
}

// webhookPayload 构造 GitHub 官方 pull_request 事件形状的 payload。
func webhookPayload(action, repo string, number int, sha string, draft, fork bool) []byte {
	owner, name, _ := strings.Cut(repo, "/")
	return []byte(fmt.Sprintf(`{
		"action": %q,
		"number": %d,
		"pull_request": {
			"draft": %t,
			"html_url": "https://github.com/%s/pull/%d",
			"title": "Add feature",
			"body": "desc",
			"head": {"sha": %q, "ref": "feature", "repo": {"fork": %t, "full_name": %q}},
			"base": {"ref": "main", "repo": {"full_name": %q}}
		},
		"repository": {"full_name": %q, "owner": {"login": %q}, "name": %q},
		"installation": {"id": 12345}
	}`, action, number, draft, repo, number, sha, fork, repo, repo, repo, owner, name))
}

// handleWebhook 以给定 action/SHA 投递事件。
func (e *prReviewTestEnv) handleWebhook(t *testing.T, delivery, action, repo string, number int, sha string, draft, fork bool) error {
	t.Helper()
	return e.pr.HandleGitHubWebhook(context.Background(), "pull_request", delivery,
		webhookPayload(action, repo, number, sha, draft, fork))
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
	env.createPRRule(t, user, func(a *model.Automation) { a.DebounceSeconds = 0 })

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
	env.createPRRule(t, user, func(a *model.Automation) { a.DebounceSeconds = 0 })

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

// ---- 场景 3：防抖窗口合并推送；到点每模型一行；同 SHA+模型被唯一键吃掉 ----

func TestWebhook_DebounceWindowMergesPushes(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(x *model.Automation) {
		x.TriggerType = model.AutomationTriggerGitHubPRSynchronize
	})
	// opened 规则不响应 synchronize。
	env.createPRRule(t, user, func(x *model.Automation) {
		x.Name = "opened 规则"
		x.TriggerType = model.AutomationTriggerGitHubPROpened
	})

	if err := env.handleWebhook(t, "d-1", "synchronize", "octocat/hw", 1, "sha-1", false, false); err != nil {
		t.Fatalf("first push: %v", err)
	}
	var d1 model.GitHubPRDebounce
	if err := env.db.Where("automation_id = ?", a.ID).First(&d1).Error; err != nil {
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
		AutomationID: a.ID, RepoFullName: "octocat/hw", PRNumber: 1,
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

// ---- 场景 4：debounce=0 在请求事务里开轮并提升 ----

func TestWebhook_DebounceZeroOpensRoundInTx(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	env.createPRRule(t, user, func(a *model.Automation) { a.DebounceSeconds = 0 })

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
	a := env.createPRRule(t, user, func(x *model.Automation) { x.DebounceSeconds = 0 })

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
	a := env.createPRRule(t, user, func(x *model.Automation) { x.DebounceSeconds = 0 })
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

// seedPRRun 直接落一条 PR 运行（trigger_key 带随机后缀，允许同 SHA 多行布置现场）。
func (e *prReviewTestEnv) seedPRRun(t *testing.T, a *model.Automation, repo string, prNumber int, sha string, status model.AutomationRunStatus) *model.AutomationRun {
	t.Helper()
	now := time.Now().UTC()
	run := &model.AutomationRun{
		ID:                 uuid.NewString(),
		UserID:             a.UserID,
		AutomationID:       a.ID,
		TriggerType:        a.TriggerType,
		TriggerKey:         prTriggerKey(repo, prNumber, sha, "inst-"+uuid.NewString()[:8], "model-x"),
		TriggerContextJSON: "{}",
		TriggeredAt:        now,
		ConfigSnapshotJSON: snapshotJSON(a),
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
	a := env.createPRRule(t, user, func(x *model.Automation) { x.DebounceSeconds = 0 })

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
	a := env.createPRRule(t, user, func(x *model.Automation) {
		x.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		x.DebounceSeconds = 0
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
	a := env.createPRRule(t, user, func(x *model.Automation) { x.DebounceSeconds = 0 })

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
		case githubapp.ConclusionFailure:
			hasFailure = true
		case githubapp.ConclusionSuccess:
			hasSuccess = true
		}
		if c.Update.Status == githubapp.CheckQueued {
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
	a := env.createPRRule(t, user, func(x *model.Automation) { x.DebounceSeconds = 0 })
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
	env.createPRRule(t, user, func(x *model.Automation) {
		x.TriggerType = model.AutomationTriggerGitHubPRSynchronize
		x.DebounceSeconds = 0
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
	env.createPRRule(t, user, func(x *model.Automation) { x.DebounceSeconds = 0 })
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
	env.createPRRule(t, user, func(x *model.Automation) { x.DebounceSeconds = 0 })
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
	a := env.createPRRule(t, user, func(x *model.Automation) {
		x.DebounceSeconds = 0
		x.PromptTemplate = "审查 {{repo}} #{{pr_number}} {{pr_title}} {{pr_url}} @{{head_sha}}"
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
	if g.Status != githubapp.CheckCompleted || g.Conclusion != githubapp.ConclusionFailure {
		t.Fatalf("failed round = %s/%s, want completed/failure", g.Status, g.Conclusion)
	}
	g = checkUpdateForGroup([]model.AutomationRun{cancelled, cancelled})
	if g.Status != githubapp.CheckCompleted || g.Conclusion != githubapp.ConclusionCancelled {
		t.Fatalf("cancelled round = %s/%s, want completed/cancelled", g.Status, g.Conclusion)
	}
	g = checkUpdateForGroup([]model.AutomationRun{completed, cancelled})
	if g.Status != githubapp.CheckCompleted || g.Conclusion != githubapp.ConclusionSuccess {
		t.Fatalf("completed+cancelled = %s/%s, want completed/success", g.Status, g.Conclusion)
	}
	g = checkUpdateForGroup([]model.AutomationRun{queued, running})
	if g.Status != githubapp.CheckInProgress {
		t.Fatalf("still running = %s, want in_progress", g.Status)
	}
}

// 超时/结束后调度循环必须提升排队——不能只在开轮事务里 promote。
func TestTick_PromotesQueuedAfterInflightDrops(t *testing.T) {
	env := setupPRReviewTestEnv(t)
	user, _ := env.createUserWithRepo(t, "octocat", "hw")
	a := env.createPRRule(t, user, func(x *model.Automation) { x.DebounceSeconds = 0 })
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
	env.createPRRule(t, user, func(x *model.Automation) { x.DebounceSeconds = 0 })
	env.createPRRule(t, user, func(x *model.Automation) {
		x.Name = "第二条 opened"
		x.DebounceSeconds = 0
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
	env.createPRRule(t, user, func(x *model.Automation) {
		x.TriggerType = model.AutomationTriggerGitHubPROpened
		x.DebounceSeconds = 60
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
	a := env.createPRRule(t, user, func(x *model.Automation) { x.DebounceSeconds = 0 })
	id, err := env.fake.CreateIssueComment(context.Background(), 1, "octocat/hw", 1, prReviewCommentMarker+"\n旧正文")
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
	if after["FindIssueCommentByMarker"]-before["FindIssueCommentByMarker"] != 0 {
		t.Fatalf("must not scan by marker when comment_id is saved")
	}
	if after["UpdateIssueComment"]-before["UpdateIssueComment"] != 1 {
		t.Fatalf("UpdateIssueComment delta = %d, want 1", after["UpdateIssueComment"]-before["UpdateIssueComment"])
	}
	if comment := env.fake.LastCommentBody(); !strings.Contains(comment, "完成") {
		t.Fatalf("comment not updated: %s", comment)
	}
}
